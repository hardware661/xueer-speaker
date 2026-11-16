package xiaomi

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Login errors the panel turns into plain words.
var (
	ErrNeedCode    = errors.New("xiaomi: an SMS / email code was sent; call Verify with it")
	ErrBadCode     = errors.New("xiaomi: wrong verification code")
	ErrBadPassword = errors.New("xiaomi: wrong account or password")
)

// Login is one password login of a Xiaomi account (sid=micoapi). Start sends
// the password; if Xiaomi wants a second factor it sends a code and Start
// returns ErrNeedCode, and Verify finishes with that code.
//
// What makes it work (learned on a real account, 2026-11-14): the deviceId
// is chosen once and kept for the life of the passToken; after the code the
// redirect chain is walked one hop at a time and stops before sts — sts
// without clientSign answers 401, and that 401's Set-Cookie would overwrite
// the passToken just issued. The passToken is then turned into a session by
// Session, which signs its own sts call.
type Login struct {
	ep       Endpoints
	deviceID string
	http     *http.Client

	mu     sync.Mutex
	verify string // verifyPhone / verifyEmail path while a code is pending
	flag   string
}

// NewLogin starts a login with deviceID ("" = a new random one).
func NewLogin(ep Endpoints, deviceID string) *Login {
	if deviceID == "" {
		b := make([]byte, 8)
		rand.Read(b)
		deviceID = "an_" + hex.EncodeToString(b)
	}
	jar, _ := cookiejar.New(nil)
	if u, err := url.Parse(ep.Account); err == nil {
		jar.SetCookies(u, []*http.Cookie{{Name: "sdkVersion", Value: "3.9", Path: "/"}, {Name: "deviceId", Value: deviceID, Path: "/"}})
	}
	return &Login{ep: ep, deviceID: deviceID, http: &http.Client{
		Timeout:       30 * time.Second,
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type loginResp struct {
	Code            int    `json:"code"`
	Desc            string `json:"desc"`
	Description     string `json:"description"`
	Tips            string `json:"tips"`
	QS              string `json:"qs"`
	Sid             string `json:"sid"`
	Sign            string `json:"_sign"`
	Callback        string `json:"callback"`
	Location        string `json:"location"`
	NotificationURL string `json:"notificationUrl"`
	Flag            int    `json:"flag"`
}

func (l *Login) call(ctx context.Context, method, path string, form url.Values) (loginResp, *http.Response, error) {
	u := path
	if !strings.HasPrefix(u, "http") {
		u = l.ep.Account + path
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return loginResp{}, nil, err
	}
	req.Header.Set("User-Agent", uaLogin)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := l.http.Do(req)
	if err != nil {
		return loginResp{}, nil, err
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	var r loginResp
	if s := strings.TrimSpace(strings.TrimPrefix(string(b), "&&&START&&&")); strings.HasPrefix(s, "{") {
		json.Unmarshal([]byte(s), &r)
	}
	return r, resp, nil
}

// Start logs in with the account (phone, email or Xiaomi ID) and password.
func (l *Login) Start(ctx context.Context, user, password string) (*Credentials, error) {
	r, _, err := l.call(ctx, http.MethodGet, "/pass/serviceLogin?sid="+sidMiNA+"&_json=true", nil)
	if err != nil {
		return nil, err
	}
	sum := md5.Sum([]byte(password))
	r, _, err = l.call(ctx, http.MethodPost, "/pass/serviceLoginAuth2", url.Values{
		"_json": {"true"}, "qs": {r.QS}, "sid": {r.Sid}, "_sign": {r.Sign}, "callback": {r.Callback},
		"user": {user}, "hash": {strings.ToUpper(hex.EncodeToString(sum[:]))},
	})
	if err != nil {
		return nil, err
	}
	switch {
	case r.Code == 70016:
		return nil, ErrBadPassword
	case r.Code == 70022:
		return nil, ErrRiskControl
	case r.Code != 0:
		return nil, fmt.Errorf("xiaomi: login code %d: %s", r.Code, r.Desc)
	case r.NotificationURL != "":
		return nil, l.sendCode(ctx, r.NotificationURL)
	}
	return l.credentials()
}

// sendCode opens the verification page Xiaomi pointed at and asks for a
// code by SMS (or email, when that is the only way the account offers).
func (l *Login) sendCode(ctx context.Context, notification string) error {
	if _, _, err := l.call(ctx, http.MethodGet, notification, nil); err != nil {
		return err
	}
	u, err := url.Parse(notification)
	if err != nil {
		return err
	}
	q := u.Query()
	sid := q.Get("sid")
	if sid == "" {
		sid = sidMiNA
	}
	list, _, err := l.call(ctx, http.MethodGet, "/identity/list?sid="+url.QueryEscape(sid)+"&supportedMask=0&_locale=zh_CN&context="+url.QueryEscape(q.Get("context")), nil)
	if err != nil {
		return err
	}
	kind, flag := "Phone", 4
	if list.Flag == 8 {
		kind, flag = "Email", 8
	}
	if _, _, err := l.call(ctx, http.MethodGet, fmt.Sprintf("/identity/auth/verify%s?_flag=%d&_json=true", kind, flag), nil); err != nil {
		return err
	}
	r, _, err := l.call(ctx, http.MethodPost, "/identity/auth/send"+kind+"Ticket", url.Values{"retry": {"0"}, "icode": {""}, "_json": {"true"}})
	if err != nil {
		return err
	}
	if r.Code == 70022 {
		return ErrRiskControl
	}
	if r.Code != 0 {
		return fmt.Errorf("xiaomi: sending the code failed (code %d) %s", r.Code, r.Tips)
	}
	l.mu.Lock()
	l.verify, l.flag = "/identity/auth/verify"+kind, fmt.Sprint(flag)
	l.mu.Unlock()
	return ErrNeedCode
}

// Verify finishes a login that Start left waiting for a code.
func (l *Login) Verify(ctx context.Context, code string) (*Credentials, error) {
	l.mu.Lock()
	path, flag := l.verify, l.flag
	l.mu.Unlock()
	if path == "" {
		return nil, errors.New("xiaomi: no code is pending; log in with the password first")
	}
	r, _, err := l.call(ctx, http.MethodPost, fmt.Sprintf("%s?_dc=%d", path, time.Now().UnixMilli()), url.Values{
		"_flag": {flag}, "ticket": {strings.TrimSpace(code)}, "trust": {"true"}, "_json": {"true"},
	})
	if err != nil {
		return nil, err
	}
	if r.Code != 0 || r.Location == "" {
		if r.Code == 70022 {
			return nil, ErrRiskControl
		}
		return nil, ErrBadCode
	}
	// Walk result/check → serviceLoginAuth2/end by hand and stop before sts.
	loc := r.Location
	for hop := 0; hop < 8 && loc != ""; hop++ {
		u, err := url.Parse(loc)
		if err != nil {
			return nil, err
		}
		if strings.HasSuffix(u.Path, "/sts") {
			break
		}
		_, resp, err := l.call(ctx, http.MethodGet, loc, nil)
		if err != nil {
			return nil, err
		}
		next := resp.Header.Get("Location")
		if next != "" && !strings.HasPrefix(next, "http") {
			next = l.ep.Account + next
		}
		loc = next
	}
	l.mu.Lock()
	l.verify = ""
	l.mu.Unlock()
	return l.credentials()
}

// credentials reads what the login left in the cookie jar.
func (l *Login) credentials() (*Credentials, error) {
	u, err := url.Parse(l.ep.Account)
	if err != nil {
		return nil, err
	}
	c := &Credentials{DeviceID: l.deviceID}
	for _, k := range l.http.Jar.Cookies(u) {
		switch k.Name {
		case "passToken":
			c.PassToken = k.Value
		case "userId":
			c.UserID = k.Value
		}
	}
	if c.PassToken == "" || c.UserID == "" {
		return nil, errors.New("xiaomi: login finished without a passToken")
	}
	return c, nil
}
