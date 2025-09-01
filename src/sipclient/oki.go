package sipclient

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwebrtc/go-sip-ua/pkg/account"
	"github.com/cloudwebrtc/go-sip-ua/pkg/session"
	"github.com/cloudwebrtc/go-sip-ua/pkg/stack"
	"github.com/cloudwebrtc/go-sip-ua/pkg/ua"
	"github.com/cloudwebrtc/go-sip-ua/pkg/utils"
	"github.com/ghettovoice/gosip/log"
	"github.com/ghettovoice/gosip/sip"
	"github.com/ghettovoice/gosip/sip/parser"
)

type OkiSIP struct {
	logger    log.Logger
	stack     *stack.SipStack
	ua        *ua.UserAgent
	profile   *account.Profile
	recipient sip.SipUri

	// config
	listen    string // e.g., 0.0.0.0:5060
	transport string // udp|tcp|wss
	server    string // host:port of proxy/registrar
	domain    string // SIP domain for URIs
	user      string
	password  string
	expires   int
}

func NewFromEnv() (*OkiSIP, error) {
	srv := strings.TrimSpace(os.Getenv("OKI_SIP_SERVER")) // host:port
	if srv == "" {
		return nil, fmt.Errorf("OKI_SIP_SERVER not set")
	}
	user := os.Getenv("OKI_SIP_USER")
	pass := os.Getenv("OKI_SIP_PASSWORD")
	if user == "" || pass == "" {
		return nil, fmt.Errorf("OKI_SIP_USER/OKI_SIP_PASSWORD must be set")
	}
	listen := os.Getenv("OKI_SIP_LISTEN")
	if listen == "" {
		listen = ":0"
	}
	transport := strings.ToLower(os.Getenv("OKI_SIP_TRANSPORT"))
	if transport == "" {
		transport = "udp"
	}
	domain := os.Getenv("OKI_SIP_DOMAIN")
	if domain == "" {
		// default to server host
		host, _, _ := net.SplitHostPort(srv)
		if host == "" {
			host = srv
		}
		domain = host
	}
	exp := 1800
	if v := os.Getenv("OKI_SIP_EXPIRES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			exp = n
		}
	}

	o := &OkiSIP{
		logger:    utils.NewLogrusLogger(log.InfoLevel, "OkiSIP", nil),
		listen:    listen,
		transport: transport,
		server:    srv,
		domain:    domain,
		user:      user,
		password:  pass,
		expires:   exp,
	}
	return o, nil
}

func (o *OkiSIP) Start() error {
	st := stack.NewSipStack(&stack.SipStackConfig{
		UserAgent:  "tacnet-odenwakun/oki",
		Extensions: []string{"replaces", "outbound"},
		Dns:        "8.8.8.8",
	})
	if err := st.Listen(o.transport, o.listen); err != nil {
		return err
	}

	u := ua.NewUserAgent(&ua.UserAgentConfig{SipStack: st})

	// Handlers (主にログと後始末)
	u.InviteStateHandler = func(sess *session.Session, req *sip.Request, resp *sip.Response, state session.Status) {
		o.logger.Infof("InviteState: state=%v dir=%s", state, sess.Direction())
		// 今回は発信専用。受信はログのみ。
	}

	u.RegisterStateHandler = func(state account.RegisterState) {
		o.logger.Infof("Register: user=%s status=%v expires=%v", state.Account.AuthInfo.AuthUser, state.StatusCode, state.Expiration)
	}

	// Profile/recipient
	aor, err := parser.ParseUri(fmt.Sprintf("sip:%s@%s", o.user, o.domain))
	if err != nil {
		return err
	}
	prof := account.NewProfile(aor.Clone(), "tacnet-odenwakun",
		&account.AuthInfo{AuthUser: o.user, Password: o.password, Realm: ""},
		uint32(o.expires), st)
	recp, err := parser.ParseSipUri(fmt.Sprintf("sip:%s;transport=%s", o.server, o.transport))
	if err != nil {
		return err
	}

	o.stack = st
	o.ua = u
	o.profile = prof
	o.recipient = recp

	// Register
	if _, err := o.ua.SendRegister(o.profile, o.recipient, o.profile.Expires, nil); err != nil {
		return err
	}
	// 少し待って登録の安定化
	time.Sleep(2 * time.Second)
	return nil
}

func (o *OkiSIP) Invite(number string) error {
	if o.ua == nil || o.profile == nil {
		return fmt.Errorf("SIP not initialized")
	}
	if strings.TrimSpace(number) == "" {
		return fmt.Errorf("empty number")
	}
	// 宛先
	called, err := parser.ParseUri(fmt.Sprintf("sip:%s@%s", number, o.domain))
	if err != nil {
		return err
	}
	// 実送信先（プロキシ）
	recp := o.recipient
	// 遅延オファー: SDPはnilでINVITEを送る（相手が200 OKでSDPオファー）
	go o.ua.Invite(o.profile, called, recp, nil)
	return nil
}

func (o *OkiSIP) Shutdown() {
	if o.ua != nil {
		// unregister
		if reg, err := o.ua.SendRegister(o.profile, o.recipient, 0, nil); err == nil {
			_ = reg // fire and forget
		}
		o.ua.Shutdown()
	}
	// no udp resource
}
