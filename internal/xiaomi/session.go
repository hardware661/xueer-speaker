// Package xiaomi talks to Xiaomi's cloud on behalf of one speaker account:
// login renewal, MiNA (playback, conversations) and MIoT (spec actions).
// Every Xiaomi quirk lives here so the rest of xueer-speaker never sees a
// passToken, a ubus envelope or an error code.
//
// Login (see login.go, learned 2026-11-14): the program owns its own
// passToken, bound to a fixed deviceId. A serviceToken per sid is minted by
// serviceLogin → sts with a clientSign; changing the deviceId or following
// the sts redirect without clientSign gets 70016 / 401 and must never happen.
package xiaomi

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Endpoints are the Xiaomi hosts; tests point them at a fake.
type Endpoints struct {
	Account string // https://account.xiaomi.com
	MiNA    string // https://api2.mina.mi.com
	MiIO    string // https://api.io.mi.com/app
	Profile string // https://userprofile.mina.mi.com
}

// Production endpoints.
var Production = Endpoints{
	Account: "https://account.xiaomi.com",
	MiNA:    "https://api2.mina.mi.com",
	MiIO:    "https://api.io.mi.com/app",
	Profile: "https://userprofile.mina.mi.com",
}

// Credentials are what login left behind. DeviceID is fixed for the life of
// the passToken.
type Credentials struct {
	UserID    string `json:"userId"`
	PassToken string `json:"passToken"`
	DeviceID  string `json:"deviceId"`
}

// Health is the login state, exported as speaker_login_state.
type Health int

const (
	HealthOK            Health = 0
	HealthRenewing      Health = 1
	HealthLoginRequired Health = 2
	HealthRiskControl   Health = 3
)

// Errors callers branch on.
var (
	ErrLoginRequired = errors.New("xiaomi: login required (passToken rejected)")
	ErrRiskControl   = errors.New("xiaomi: rate limited by Xiaomi")
	ErrUnauthorized  = errors.New("xiaomi: service token rejected")
)

const (
	sidMiNA = "micoapi"
	sidMiIO = "xiaomiio"
	uaLogin = "APP/com.xiaomi.mihome APPV/11.3.203 iosPassportSDK/4.2.50 iOS/26.3.1 L/zh_CN"
	uaMiNA  = "MICO/AndroidApp/@SHIP.TO.2A2FE0D7@/2.4.40"
)

type token struct {
	value     string
	ssecurity string
}

// Session holds one account's tokens and renews them. Safe for concurrent use;
// only one renewal per sid runs at a time.
type Session struct {
	ep    Endpoints
	creds Credentials
	http  *http.Client

	mu     sync.Mutex
	tokens map[string]token
	health Health
}

// NewSession returns a session; nothing is fetched until the first call.
func NewSession(ep Endpoints, c Credentials) *Session {
	return &Session{
		ep:    ep,
		creds: c,
		http: &http.Client{
			Timeout:       20 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		tokens: map[string]token{},
	}
}

// Health reports the login state.
func (s *Session) Health() Health {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.health
}

// serviceToken returns a cached token for sid, minting one if needed.
func (s *Session) serviceToken(ctx context.Context, sid string, renew bool) (token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.health == HealthLoginRequired {
		return token{}, ErrLoginRequired
	}
	if t, ok := s.tokens[sid]; ok && !renew {
		return t, nil
	}
	s.health = HealthRenewing
	t, err := s.login(ctx, sid)
	switch {
	case errors.Is(err, ErrLoginRequired):
		s.health = HealthLoginRequired
	case errors.Is(err, ErrRiskControl):
		s.health = HealthRiskControl
	case err == nil:
		s.health = HealthOK
		s.tokens[sid] = t
	default:
		s.health = HealthOK // network trouble does not change the login state
	}
	return t, err
}

// login runs serviceLogin → sts for sid. Called with s.mu held.
func (s *Session) login(ctx context.Context, sid string) (token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.ep.Account+"/pass/serviceLogin?sid="+sid+"&_json=true", nil)
	if err != nil {
		return token{}, err
	}
	req.Header.Set("User-Agent", uaLogin)
	req.Header.Set("Cookie", fmt.Sprintf("sdkVersion=3.9; deviceId=%s; userId=%s; passToken=%s",
		s.creds.DeviceID, s.creds.UserID, s.creds.PassToken))
	resp, err := s.http.Do(req)
	if err != nil {
		return token{}, err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	var r struct {
		Code      int             `json:"code"`
		Nonce     json.RawMessage `json:"nonce"`
		SSecurity string          `json:"ssecurity"`
		Location  string          `json:"location"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(string(body), "&&&START&&&")), &r); err != nil {
		return token{}, fmt.Errorf("xiaomi: serviceLogin %s: bad response (HTTP %d)", sid, resp.StatusCode)
	}
	switch r.Code {
	case 0:
	case 70022:
		return token{}, ErrRiskControl
	default:
		return token{}, fmt.Errorf("%w: serviceLogin %s code %d", ErrLoginRequired, sid, r.Code)
	}
	nonce := strings.Trim(string(r.Nonce), `"`)
	sum := sha1.Sum([]byte("nonce=" + nonce + "&" + r.SSecurity))
	sign := base64.StdEncoding.EncodeToString(sum[:])
	sreq, err := http.NewRequestWithContext(ctx, http.MethodGet, r.Location+"&clientSign="+url.QueryEscape(sign), nil)
	if err != nil {
		return token{}, err
	}
	sreq.Header.Set("User-Agent", uaLogin)
	sresp, err := s.http.Do(sreq)
	if err != nil {
		return token{}, err
	}
	io.Copy(io.Discard, sresp.Body)
	sresp.Body.Close()
	for _, c := range sresp.Cookies() {
		if c.Name == "serviceToken" && c.Value != "" {
			return token{value: c.Value, ssecurity: r.SSecurity}, nil
		}
	}
	return token{}, fmt.Errorf("xiaomi: sts %s: no serviceToken (HTTP %d)", sid, sresp.StatusCode)
}

// do sends a request built by mk with the sid's serviceToken. On 401 it
// renews once and replays.
func (s *Session) do(ctx context.Context, sid string, mk func(t token) (*http.Request, error)) ([]byte, error) {
	for attempt := range 2 {
		t, err := s.serviceToken(ctx, sid, attempt > 0)
		if err != nil {
			return nil, err
		}
		req, err := mk(t)
		if err != nil {
			return nil, err
		}
		resp, err := s.http.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized {
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("xiaomi: %s HTTP %d", req.URL.Path, resp.StatusCode)
		}
		return body, nil
	}
	return nil, ErrUnauthorized
}

// Device is one speaker on the account.
type Device struct {
	DeviceID   string `json:"deviceID"`
	MiotDID    string `json:"miotDID"`
	Name       string `json:"name"`
	Alias      string `json:"alias"`
	Hardware   string `json:"hardware"`
	RomVersion string `json:"romVersion"`
	Presence   string `json:"presence"`
	Serial     string `json:"serialNumber"`
	MAC        string `json:"mac"`
}

// Devices lists the account's speakers (MiNA).
func (s *Session) Devices(ctx context.Context) ([]Device, error) {
	body, err := s.do(ctx, sidMiNA, func(t token) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			s.ep.MiNA+"/admin/v2/device_list?master=0&requestId="+requestID(), nil)
		if err != nil {
			return nil, err
		}
		s.minaHeaders(req, t, nil)
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	var r struct {
		Code int      `json:"code"`
		Data []Device `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("xiaomi: device_list: %w", err)
	}
	if r.Code != 0 {
		return nil, fmt.Errorf("xiaomi: device_list code %d", r.Code)
	}
	return r.Data, nil
}

func (s *Session) minaHeaders(req *http.Request, t token, d *Device) {
	req.Header.Set("User-Agent", uaMiNA)
	c := "userId=" + s.creds.UserID + "; serviceToken=" + t.value
	if d != nil {
		c += "; deviceId=" + d.DeviceID + "; sn=" + d.Serial + "; hardware=" + d.Hardware
	}
	req.Header.Set("Cookie", c)
}

func requestID() string {
	return "app_ios_" + fmt.Sprint(time.Now().UnixNano())
}
