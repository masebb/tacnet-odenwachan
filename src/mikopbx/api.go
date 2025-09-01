package mikopbx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL  string
	login    string
	password string
	http     *http.Client
	debug    bool
}

func NewClient(baseURL, login, password string) (*Client, error) {
	if baseURL == "" {
		return nil, errors.New("baseURL is required")
	}
	jar, _ := cookiejar.New(nil)
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		login:    login,
		password: password,
		http:     &http.Client{Timeout: 15 * time.Second, Jar: jar},
		debug:    false,
	}, nil
}

// SetDebug allows toggling debug logging at runtime (overrides env default).
func (c *Client) SetDebug(v bool) { c.debug = v }

// Authenticate obtains a PHPSESSID cookie if credentials are provided.
func (c *Client) Authenticate() error {
	if c.login == "" || c.password == "" {
		// Assume no auth needed (e.g., localhost) per docs.
		return nil
	}
	form := url.Values{}
	form.Set("login", c.login)
	form.Set("password", c.password)
	req, err := http.NewRequest("POST", c.baseURL+"/admin-cabinet/session/start", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	if c.debug {
		bodyPreview := sanitizeFormPreview(form)
		log.Printf("[MikoPBX][REQ] POST %s Headers: {Content-Type: %s, X-Requested-With: XMLHttpRequest} Body: %s",
			req.URL.String(), req.Header.Get("Content-Type"), bodyPreview)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if c.debug {
		log.Printf("[MikoPBX][RES] %s %s", resp.Status, req.URL.String())
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("auth failed: %s (%s)", resp.Status, string(b))
	}
	// Body contains JSON {success:true,...} but cookie is what we need; cookie jar captures it.
	return nil
}

type PeersStatusesResponse struct {
	Result bool `json:"result"`
	Data   []struct {
		ID    string `json:"id"`
		State string `json:"state"`
	} `json:"data"`
}

// Peer詳細（名前など）
// getSipPeer のレスポンス（必要なフィールドのみ）
type SipPeerResponse struct {
	Result bool `json:"result"`
	Data   struct {
		EndpointName string `json:"EndpointName"`
		// state や他のメタ情報は今回は未使用
	} `json:"data"`
}

type RegistryResponse struct {
	Result bool `json:"result"`
	Data   []struct {
		State    string `json:"state"`
		ID       string `json:"id"`
		Username string `json:"username"`
		Host     string `json:"host"`
	} `json:"data"`
}

func (c *Client) GetPeersStatuses() (PeersStatusesResponse, error) {
	var out PeersStatusesResponse
	url := c.baseURL + "/pbxcore/api/sip/getPeersStatuses"
	status, b, err := c.getWithRetry(url)
	if err != nil {
		return out, err
	}
	if status != http.StatusOK {
		return out, fmt.Errorf("getPeersStatuses %d: %s", status, string(b))
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (c *Client) GetRegistry() (RegistryResponse, error) {
	var out RegistryResponse
	url := c.baseURL + "/pbxcore/api/sip/getRegistry"
	status, b, err := c.getWithRetry(url)
	if err != nil {
		return out, err
	}
	if status != http.StatusOK {
		return out, fmt.Errorf("getRegistry %d: %s", status, string(b))
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, err
	}
	return out, nil
}

// 指定したPeer IDの詳細を取得して表示名を返す（見つからなければ空文字）
func (c *Client) GetPeerName(id string) (string, error) {
	if id == "" {
		return "", nil
	}
	payload := map[string]string{"peer": id}
	status, b := c.postJSONWithRetry("/pbxcore/api/sip/getSipPeer", payload)
	if status != http.StatusOK {
		return "", fmt.Errorf("getSipPeer %d: %s", status, string(b))
	}
	var out SipPeerResponse
	if err := json.Unmarshal(b, &out); err != nil {
		return "", err
	}
	if !out.Result {
		return "", nil
	}
	return out.Data.EndpointName, nil
}

// Optional helper retained for compatibility: returns a synthetic http.Response using unlimited retry logic.
func (c *Client) PostJSON(path string, payload any) (*http.Response, error) {
	status, body := c.postJSONWithRetry(path, payload)
	resp := &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	return resp, nil
}

// --- Internal retry helpers ---
func (c *Client) getWithRetry(u string) (int, []byte, error) {
	backoff := time.Second
	attempt := 1
	for {
		req, _ := http.NewRequest("GET", u, nil)
		if c.debug {
			log.Printf("[MikoPBX][REQ] GET %s (attempt=%d)", req.URL.String(), attempt)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if c.debug {
				log.Printf("[MikoPBX][ERR] GET %s error: %v (retry in %s)", u, err, backoff)
			}
			time.Sleep(backoff + jitter())
			backoff = nextBackoff(backoff)
			attempt++
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if c.debug {
			log.Printf("[MikoPBX][RES] %s %s Body: %s", resp.Status, u, previewJSON(b, 2000))
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			_ = c.Authenticate()
			time.Sleep(200*time.Millisecond + jitter()/2)
			attempt++
			continue
		}
		if resp.StatusCode >= 500 {
			if c.debug {
				log.Printf("[MikoPBX][RETRY] %s returned %d, retry in %s", u, resp.StatusCode, backoff)
			}
			time.Sleep(backoff + jitter())
			backoff = nextBackoff(backoff)
			attempt++
			continue
		}
		return resp.StatusCode, b, nil
	}
}

func (c *Client) postJSONWithRetry(path string, payload any) (int, []byte) {
	body, _ := json.Marshal(payload)
	url := c.baseURL + path
	backoff := time.Second
	attempt := 1
	for {
		req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if c.debug {
			log.Printf("[MikoPBX][REQ] POST %s Headers: {Content-Type: %s} Body: %s (attempt=%d)", url, req.Header.Get("Content-Type"), previewJSON(body, 2000), attempt)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if c.debug {
				log.Printf("[MikoPBX][ERR] POST %s error: %v (retry in %s)", url, err, backoff)
			}
			time.Sleep(backoff + jitter())
			backoff = nextBackoff(backoff)
			attempt++
			continue
		}
		rb, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if c.debug {
			log.Printf("[MikoPBX][RES] %s %s Body: %s", resp.Status, url, previewJSON(rb, 2000))
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			_ = c.Authenticate()
			time.Sleep(200*time.Millisecond + jitter()/2)
			attempt++
			continue
		}
		if resp.StatusCode >= 500 {
			if c.debug {
				log.Printf("[MikoPBX][RETRY] %s returned %d, retry in %s", url, resp.StatusCode, backoff)
			}
			time.Sleep(backoff + jitter())
			backoff = nextBackoff(backoff)
			attempt++
			continue
		}
		return resp.StatusCode, rb
	}
}

func nextBackoff(cur time.Duration) time.Duration {
	max := 60 * time.Second
	n := cur * 2
	if n > max {
		n = max
	}
	return n
}

func jitter() time.Duration {
	// 0-400ms jitter
	return time.Duration(rand.Intn(401)) * time.Millisecond
}

// Helpers
func sanitizeFormPreview(v url.Values) string {
	cp := url.Values{}
	for k, vals := range v {
		if strings.EqualFold(k, "password") {
			cp[k] = []string{"***"}
			continue
		}
		cp[k] = vals
	}
	s := cp.Encode()
	if len(s) > 2000 {
		s = s[:2000] + "…"
	}
	return s
}

func previewJSON(b []byte, limit int) string {
	trimmed := strings.TrimSpace(string(b))
	if len(trimmed) > limit {
		return trimmed[:limit] + "…"
	}
	return trimmed
}
