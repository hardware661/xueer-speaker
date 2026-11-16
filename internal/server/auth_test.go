package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/hardware661/xueer-speaker/internal/store"
)

// newPasswordServer is a server in the default auth mode, with its log kept
// so a test can read the one-time setup code from it.
func newPasswordServer(t *testing.T) (*Server, *bytes.Buffer) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var logs bytes.Buffer
	s := New(Config{
		MCPTokens: map[string]string{"tok": "ai"}, BotKey: "bot", CLIKey: "cli", PCKey: "pc",
		Audio: http.NotFoundHandler(), Log: slog.New(slog.NewTextHandler(&logs, nil)),
	}, &fakeSayer{}, &fakeDev{}, st)
	return s, &logs
}

var jsonSame = map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "same-origin"}

func setupCode(t *testing.T, logs *bytes.Buffer) string {
	t.Helper()
	m := regexp.MustCompile(`setup_code=(\d{6})`).FindStringSubmatch(logs.String())
	if m == nil {
		t.Fatalf("no setup code in the log: %s", logs)
	}
	return m[1]
}

func cookieFrom(t *testing.T, rec interface{ Result() *http.Response }) string {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == "xs_session" && c.Value != "" {
			if !c.HttpOnly {
				t.Fatal("session cookie must be HttpOnly")
			}
			return c.Name + "=" + c.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

func withCookie(c string, h map[string]string) map[string]string {
	out := map[string]string{"Cookie": c}
	for k, v := range h {
		out[k] = v
	}
	return out
}

func TestPasswordModeFirstSetupThenLogin(t *testing.T) {
	s, logs := newPasswordServer(t)
	pub := s.Public()

	if rec := do(pub, "GET", "/api/auth", "", nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"setup":true`) {
		t.Fatalf("auth before setup = %d %s", rec.Code, rec.Body)
	}
	if c := do(pub, "GET", "/api/status", "", nil).Code; c != 401 {
		t.Fatalf("status before setup = %d", c)
	}
	// The page itself loads, so it can show the setup form.
	if c := do(pub, "GET", "/", "", nil).Code; c != 200 {
		t.Fatalf("page = %d", c)
	}
	code := setupCode(t, logs)
	if c := do(pub, "POST", "/api/auth/setup", `{"code":"000000","password":"long enough"}`, jsonSame).Code; c != 403 {
		t.Fatalf("wrong setup code = %d", c)
	}
	if c := do(pub, "POST", "/api/auth/setup", `{"code":"`+code+`","password":"short"}`, jsonSame).Code; c != 400 {
		t.Fatalf("short password = %d", c)
	}
	rec := do(pub, "POST", "/api/auth/setup", `{"code":"`+code+`","password":"long enough"}`, jsonSame)
	if rec.Code != 204 {
		t.Fatalf("setup = %d %s", rec.Code, rec.Body)
	}
	cookie := cookieFrom(t, rec)
	if c := do(pub, "GET", "/api/status", "", withCookie(cookie, nil)).Code; c != 200 {
		t.Fatalf("status with session = %d", c)
	}
	// Setup is done: it cannot be run again to take over the panel.
	if c := do(pub, "POST", "/api/auth/setup", `{"code":"`+code+`","password":"another one"}`, jsonSame).Code; c != 409 {
		t.Fatalf("second setup = %d", c)
	}
	if c := do(pub, "POST", "/api/auth/login", `{"password":"wrong one"}`, jsonSame).Code; c != 401 {
		t.Fatalf("wrong password = %d", c)
	}
	rec = do(pub, "POST", "/api/auth/login", `{"password":"long enough"}`, jsonSame)
	if rec.Code != 204 {
		t.Fatalf("login = %d", rec.Code)
	}
	cookie2 := cookieFrom(t, rec)
	if c := do(pub, "POST", "/api/auth/logout", ``, withCookie(cookie2, map[string]string{"Sec-Fetch-Site": "same-origin"})).Code; c != 204 {
		t.Fatalf("logout = %d", c)
	}
	if c := do(pub, "GET", "/api/status", "", withCookie(cookie2, nil)).Code; c != 401 {
		t.Fatalf("status after logout = %d", c)
	}
	// The owner header means nothing in password mode.
	if c := do(pub, "GET", "/api/status", "", map[string]string{"X-Owner-User": "x"}).Code; c != 401 {
		t.Fatalf("header in password mode = %d", c)
	}
}

func TestPasswordModeSlowsDownGuessing(t *testing.T) {
	s, logs := newPasswordServer(t)
	pub := s.Public()
	do(pub, "POST", "/api/auth/setup", `{"code":"`+setupCode(t, logs)+`","password":"long enough"}`, jsonSame)
	for i := 0; i < 5; i++ {
		do(pub, "POST", "/api/auth/login", `{"password":"guess"}`, jsonSame)
	}
	if c := do(pub, "POST", "/api/auth/login", `{"password":"long enough"}`, jsonSame).Code; c != 429 {
		t.Fatalf("after 5 failures = %d, want 429", c)
	}
}

func TestPasswordAndSessionSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	open := func() (*Server, *bytes.Buffer) {
		st, err := store.Open(filepath.Join(dir, "s.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		var logs bytes.Buffer
		s := New(Config{MCPTokens: map[string]string{"tok": "ai"}, Audio: http.NotFoundHandler(), Log: slog.New(slog.NewTextHandler(&logs, nil))}, &fakeSayer{}, &fakeDev{}, st)
		return s, &logs
	}
	s, logs := open()
	rec := do(s.Public(), "POST", "/api/auth/setup", `{"code":"`+setupCode(t, logs)+`","password":"long enough"}`, jsonSame)
	cookie := cookieFrom(t, rec)

	s, logs = open()
	if strings.Contains(logs.String(), "setup_code") {
		t.Fatal("setup code logged again although a password is set")
	}
	if c := do(s.Public(), "GET", "/api/status", "", withCookie(cookie, nil)).Code; c != 200 {
		t.Fatalf("session after restart = %d", c)
	}
}

func TestHeaderModeTrustsTheProxy(t *testing.T) {
	s, _, _ := newServer(t) // Auth: header
	if c := do(s.Public(), "GET", "/api/auth", "", map[string]string{"X-Owner-User": "me"}).Code; c != 200 {
		t.Fatalf("auth = %d", c)
	}
	if c := do(s.Public(), "POST", "/api/auth/setup", `{"code":"1","password":"long enough"}`, jsonSame).Code; c != 404 {
		t.Fatalf("setup in header mode = %d", c)
	}
}
