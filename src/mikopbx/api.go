package mikopbx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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
	req, _ := http.NewRequest("GET", url, nil)
	if c.debug {
		log.Printf("[MikoPBX][REQ] GET %s", req.URL.String())
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if c.debug {
		log.Printf("[MikoPBX][RES] %s %s Body: %s", resp.Status, req.URL.String(), previewJSON(b, 2000))
	}
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("getPeersStatuses %s: %s", resp.Status, string(b))
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, err
	}
	return out, nil
}

func (c *Client) GetRegistry() (RegistryResponse, error) {
	var out RegistryResponse
	url := c.baseURL + "/pbxcore/api/sip/getRegistry"
	req, _ := http.NewRequest("GET", url, nil)
	if c.debug {
		log.Printf("[MikoPBX][REQ] GET %s", req.URL.String())
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if c.debug {
		log.Printf("[MikoPBX][RES] %s %s Body: %s", resp.Status, req.URL.String(), previewJSON(b, 2000))
	}
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("getRegistry %s: %s", resp.Status, string(b))
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
	resp, err := c.PostJSON("/pbxcore/api/sip/getSipPeer", payload)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("getSipPeer %s: %s", resp.Status, string(b))
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

// Optional helper for getSipPeer if needed later.
func (c *Client) PostJSON(path string, payload any) (*http.Response, error) {
	b, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.debug {
		log.Printf("[MikoPBX][REQ] POST %s Headers: {Content-Type: %s} Body: %s", req.URL.String(), req.Header.Get("Content-Type"), previewJSON(b, 2000))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if c.debug {
		rb, _ := io.ReadAll(resp.Body)
		log.Printf("[MikoPBX][RES] %s %s Body: %s", resp.Status, req.URL.String(), previewJSON(rb, 2000))
		resp.Body = io.NopCloser(bytes.NewReader(rb))
	}
	return resp, nil
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
