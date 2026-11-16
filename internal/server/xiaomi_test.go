package server

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeMi struct {
	health    string
	speakers  []SpeakerInfo
	chosen    string
	needCode  bool
	loginErr  error
	codeErr   error
	loggedOut bool
}

func (f *fakeMi) LoginState() string { return f.health }
func (f *fakeMi) Login(_ context.Context, user, password string) (bool, error) {
	if f.loginErr != nil {
		return false, f.loginErr
	}
	if !f.needCode {
		f.health = "ok"
	}
	return f.needCode, nil
}
func (f *fakeMi) Verify(_ context.Context, code string) error {
	if f.codeErr != nil {
		return f.codeErr
	}
	f.health = "ok"
	return nil
}
func (f *fakeMi) Logout() error { f.loggedOut, f.health = true, "none"; return nil }
func (f *fakeMi) Speakers(context.Context) ([]SpeakerInfo, error) {
	return f.speakers, nil
}
func (f *fakeMi) Choose(_ context.Context, key string) error {
	for _, s := range f.speakers {
		if s.ID == key {
			f.chosen = key
			return nil
		}
	}
	return errors.New("no such speaker")
}
func (f *fakeMi) Chosen() string { return f.chosen }

func TestPanelXiaomiLoginWithCodeAndSpeakerChoice(t *testing.T) {
	s, _, _ := newServer(t)
	mi := &fakeMi{health: "none", needCode: true, speakers: []SpeakerInfo{{ID: "d1", Name: "客厅", Model: "OH2P", Online: true}}}
	s.cfg.Xiaomi = mi
	pub := s.Public()
	ok := map[string]string{"X-Owner-User": "me", "Content-Type": "application/json", "Sec-Fetch-Site": "same-origin"}

	rec := do(pub, "POST", "/api/xiaomi/login", `{"user":"a","password":"b"}`, ok)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"need_code":true`) {
		t.Fatalf("login = %d %s", rec.Code, rec.Body)
	}
	if c := do(pub, "POST", "/api/xiaomi/verify", `{"code":"123456"}`, ok).Code; c != 204 {
		t.Fatalf("verify = %d", c)
	}
	rec = do(pub, "GET", "/api/xiaomi", "", ok)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"login":"ok"`) || !strings.Contains(rec.Body.String(), `"name":"客厅"`) {
		t.Fatalf("xiaomi = %d %s", rec.Code, rec.Body)
	}
	if c := do(pub, "POST", "/api/xiaomi/speaker", `{"id":"nope"}`, ok).Code; c != 400 {
		t.Fatalf("bad speaker = %d", c)
	}
	if c := do(pub, "POST", "/api/xiaomi/speaker", `{"id":"d1"}`, ok).Code; c != 204 || mi.chosen != "d1" {
		t.Fatalf("choose = %d %q", c, mi.chosen)
	}
	if c := do(pub, "POST", "/api/xiaomi/logout", ``, map[string]string{"X-Owner-User": "me", "Sec-Fetch-Site": "same-origin"}).Code; c != 204 || !mi.loggedOut {
		t.Fatalf("logout = %d", c)
	}
	if c := do(pub, "POST", "/api/xiaomi/login", `{"user":"a","password":"b"}`, nil).Code; c != 401 {
		t.Fatalf("login without panel login = %d", c)
	}
}

func TestPanelXiaomiErrorsAreReadable(t *testing.T) {
	s, _, _ := newServer(t)
	s.cfg.Xiaomi = &fakeMi{health: "none", loginErr: ErrXiaomiPassword}
	ok := map[string]string{"X-Owner-User": "me", "Content-Type": "application/json", "Sec-Fetch-Site": "same-origin"}
	rec := do(s.Public(), "POST", "/api/xiaomi/login", `{"user":"a","password":"b"}`, ok)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "密码") {
		t.Fatalf("= %d %s", rec.Code, rec.Body)
	}
}

func TestConnectInfoShowsMCPAddressAndKey(t *testing.T) {
	s, _, _ := newServer(t)
	rec := do(s.Public(), "GET", "/api/connect", "", map[string]string{"X-Owner-User": "me"}) // httptest's host is example.com
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"mcp_url":"http://example.com/mcp"`) || !strings.Contains(body, `"mcp_key":"tok-`) || !strings.Contains(body, `"pc_key"`) {
		t.Fatalf("= %d %s", rec.Code, body)
	}
	if c := do(s.Public(), "GET", "/api/connect", "", nil).Code; c != 401 {
		t.Fatalf("connect without login = %d", c)
	}
}

func TestPersonaShapesTheToolText(t *testing.T) {
	s, _, _ := newServer(t)
	s.cfg.Persona = Persona{Name: "小白", Owner: "老板"}
	d := s.sayDescription()
	if !strings.Contains(d, "小白") || !strings.Contains(d, "老板") || strings.Contains(d, "雪儿") || strings.Contains(d, "主人") {
		t.Fatalf("description = %s", d)
	}
	if !strings.Contains(DefaultPersona.describe(), "雪儿") || !strings.Contains(DefaultPersona.describe(), "主人") {
		t.Fatal("default persona is 雪儿 calling 主人")
	}
}

func TestPanelSavesTheSpeakerBase(t *testing.T) {
	s, _, _ := newServer(t)
	ok := map[string]string{"X-Owner-User": "me", "Content-Type": "application/json", "Sec-Fetch-Site": "same-origin"}
	if c := do(s.Public(), "POST", "/api/settings", `{"base":"localhost:8100"}`, ok).Code; c != 400 {
		t.Fatalf("bad base = %d", c)
	}
	if c := do(s.Public(), "POST", "/api/settings", `{"base":"http://192.168.1.9:8100/"}`, ok).Code; c != 204 {
		t.Fatalf("base = %d", c)
	}
	if b := s.store.Setting("base", ""); b != "http://192.168.1.9:8100" {
		t.Fatalf("stored %q", b)
	}
}

func TestStatusLearnsTheBaseFromTheFirstVisit(t *testing.T) {
	s, _, _ := newServer(t)
	do(s.Public(), "GET", "/api/status", "", map[string]string{"X-Owner-User": "me"})
	if b := s.store.Setting("base", ""); b != "http://example.com" {
		t.Fatalf("base = %q", b)
	}
}
