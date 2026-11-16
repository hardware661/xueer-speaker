package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hardware661/xueer-speaker/internal/announcer"
	"github.com/hardware661/xueer-speaker/internal/store"
	"github.com/hardware661/xueer-speaker/internal/tools"
	"github.com/hardware661/xueer-speaker/internal/voice"
)

type fakeSayer struct {
	got  []announcer.Request
	fail error
}

func (f *fakeSayer) Say(_ context.Context, r announcer.Request) (announcer.Result, error) {
	if f.fail != nil {
		return announcer.Result{}, f.fail
	}
	f.got = append(f.got, r)
	return announcer.Result{ID: "id1"}, nil
}

type fakeDev struct{ vol, ops []string }

func (f *fakeDev) Snapshot(context.Context) Status {
	return Status{Online: true, Login: "ok", State: "idle", Volume: 40}
}
func (f *fakeDev) SetVolume(_ context.Context, v int) error { f.vol = append(f.vol, "v"); return nil }
func (f *fakeDev) PanelVolume(_ context.Context, v int) error {
	f.vol = append(f.vol, "panel")
	return nil
}
func (f *fakeDev) Stop(context.Context) error { f.ops = append(f.ops, "stop"); return nil }

func newServer(t *testing.T) (*Server, *fakeSayer, *fakeDev) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	say, dev := &fakeSayer{}, &fakeDev{}
	s := New(Config{
		Auth:      AuthHeader,
		MCPTokens: map[string]string{"tok-claude": "claude", "tok-codex": "codex"},
		BotKey:    "bot-key",
		CLIKey:    "cli-key",
		Audio:     http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("mp3")) }),
	}, say, dev, st)
	return s, say, dev
}

func do(h http.Handler, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPortsDoNotLeakIntoEachOther(t *testing.T) {
	s, _, _ := newServer(t)
	pub, in := s.Public(), s.Internal()
	for _, p := range []string{"/v1/say", "/admin/status", "/metrics", "/healthz"} {
		if c := do(pub, "GET", p, "", nil).Code; c != 404 {
			t.Errorf("public %s = %d, want 404", p, c)
		}
	}
	for _, p := range []string{"/mcp", "/a/x.mp3", "/api/status", "/"} {
		if c := do(in, "GET", p, "", nil).Code; c != 404 {
			t.Errorf("internal %s = %d, want 404", p, c)
		}
	}
	if c := do(pub, "GET", "/a/x.mp3", "", nil).Code; c != 200 {
		t.Errorf("public /a/ = %d", c)
	}
}

func TestInternalSayNeedsTheBotKeyAndMapsErrors(t *testing.T) {
	s, say, _ := newServer(t)
	in := s.Internal()
	if c := do(in, "POST", "/v1/say", `{"text":"主人。","source":"bot:alert"}`, nil).Code; c != 401 {
		t.Fatalf("no key = %d", c)
	}
	if c := do(in, "POST", "/v1/say", `{"text":"主人。","source":"bot:alert"}`, map[string]string{"Authorization": "Bearer cli-key"}).Code; c != 401 {
		t.Fatalf("cli key on /v1 = %d", c)
	}
	rec := do(in, "POST", "/v1/say", `{"text":"主人。","source":"bot:alert"}`, map[string]string{"Authorization": "Bearer bot-key"})
	if rec.Code != 202 || !strings.Contains(rec.Body.String(), `"id":"id1"`) || say.got[0].Source != "bot:alert" {
		t.Fatalf("say = %d %s %+v", rec.Code, rec.Body, say.got)
	}
	for err, want := range map[error]int{voice.ErrTooLong: 400, announcer.ErrRateLimited: 429, announcer.ErrDuplicate: 429, errors.New("x"): 500} {
		say.fail = err
		rec := do(in, "POST", "/v1/say", `{"text":"主人。"}`, map[string]string{"Authorization": "Bearer bot-key"})
		if rec.Code != want || !strings.Contains(rec.Body.String(), `"error":{"code":`) {
			t.Errorf("%v → %d %s, want %d", err, rec.Code, rec.Body, want)
		}
	}
}

func TestBotSourceIsForcedToBotPrefix(t *testing.T) {
	s, say, _ := newServer(t)
	do(s.Internal(), "POST", "/v1/say", `{"text":"主人。","source":"claude"}`, map[string]string{"Authorization": "Bearer bot-key"})
	if say.got[0].Source != "bot:claude" {
		t.Fatalf("source = %q", say.got[0].Source)
	}
}

func TestAdminUsesCLIKey(t *testing.T) {
	s, say, dev := newServer(t)
	in := s.Internal()
	h := map[string]string{"Authorization": "Bearer cli-key", "Content-Type": "application/json"}
	if c := do(in, "POST", "/admin/say", `{"text":"测试。","native":true}`, h).Code; c != 202 || !say.got[0].Native || say.got[0].Source != "cli" {
		t.Fatalf("admin say = %d %+v", c, say.got)
	}
	if c := do(in, "POST", "/admin/volume", `{"volume":50}`, h).Code; c != 204 || len(dev.vol) != 1 {
		t.Fatalf("volume = %d", c)
	}
	if c := do(in, "POST", "/admin/stop", ``, h).Code; c != 204 || len(dev.ops) != 1 {
		t.Fatalf("stop = %d", c)
	}
	if c := do(in, "GET", "/admin/status", ``, map[string]string{"Authorization": "Bearer bot-key"}).Code; c != 401 {
		t.Fatalf("bot key on admin = %d", c)
	}
}

func TestHealthAndMetricsAreOpenOnTheInternalPort(t *testing.T) {
	s, _, _ := newServer(t)
	in := s.Internal()
	if c := do(in, "GET", "/healthz", "", nil).Code; c != 200 {
		t.Fatalf("healthz = %d", c)
	}
	if c := do(in, "GET", "/readyz", "", nil).Code; c != 200 {
		t.Fatalf("readyz = %d", c)
	}
	rec := do(in, "GET", "/metrics", "", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "speaker_online 1") || !strings.Contains(rec.Body.String(), "speaker_login_state 0") {
		t.Fatalf("metrics = %d %s", rec.Code, rec.Body)
	}
}

func TestPanelAPINeedsOwnerAndSameOriginJSON(t *testing.T) {
	s, say, _ := newServer(t)
	pub := s.Public()
	owner := map[string]string{"X-Owner-User": "hardware"}
	if c := do(pub, "GET", "/api/status", "", nil).Code; c != 401 {
		t.Fatalf("no owner = %d", c)
	}
	rec := do(pub, "GET", "/api/status", "", owner)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"online":true`) {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	cross := map[string]string{"X-Owner-User": "hardware", "Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}
	if c := do(pub, "POST", "/api/say", `{"text":"主人。"}`, cross).Code; c != 403 {
		t.Fatalf("cross-site = %d", c)
	}
	form := map[string]string{"X-Owner-User": "hardware", "Content-Type": "text/plain", "Sec-Fetch-Site": "same-origin"}
	if c := do(pub, "POST", "/api/say", `{"text":"主人。"}`, form).Code; c != 415 {
		t.Fatalf("text/plain = %d", c)
	}
	ok := map[string]string{"X-Owner-User": "hardware", "Content-Type": "application/json", "Sec-Fetch-Site": "same-origin"}
	if c := do(pub, "POST", "/api/say", `{"text":"主人。"}`, ok).Code; c != 202 || say.got[0].Source != "panel" {
		t.Fatalf("panel say = %d %+v", c, say.got)
	}
	if c := do(pub, "GET", "/", "", owner).Code; c != 200 {
		t.Fatalf("panel page = %d", c)
	}
}

func TestPanelSettingsPersist(t *testing.T) {
	s, _, _ := newServer(t)
	ok := map[string]string{"X-Owner-User": "hardware", "Content-Type": "application/json", "Sec-Fetch-Site": "same-origin"}
	if c := do(s.Public(), "POST", "/api/settings", `{"voice":"native"}`, ok).Code; c != 204 {
		t.Fatalf("settings = %d", c)
	}
	if s.Voice() != "native" {
		t.Fatalf("voice = %q", s.Voice())
	}
	if c := do(s.Public(), "POST", "/api/settings", `{"voice":"robot"}`, ok).Code; c != 400 {
		t.Fatalf("bad voice = %d", c)
	}
}

func TestMCPSayAndStatus(t *testing.T) {
	s, say, _ := newServer(t)
	srv := httptest.NewServer(s.Public())
	defer srv.Close()

	call := func(token string) (*mcp.ClientSession, error) {
		hc := &http.Client{Transport: bearer{token}}
		c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
		return c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: hc, MaxRetries: -1}, nil)
	}
	if _, err := call("wrong"); err == nil {
		t.Fatal("wrong token must not connect")
	}
	cs, err := call("tok-codex")
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "speaker_say", Arguments: map[string]any{"text": "主人，Codex 这边做完了。"}})
	if err != nil || res.IsError {
		t.Fatalf("say: %v %+v", err, res)
	}
	if len(say.got) != 1 || say.got[0].Source != "codex" || say.got[0].Text != "主人，Codex 这边做完了。" {
		t.Fatalf("got %+v", say.got)
	}
	say.fail = voice.ErrTooLong
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "speaker_say", Arguments: map[string]any{"text": "x"}})
	if err != nil || !res.IsError {
		t.Fatalf("too long must be a tool error: %v %+v", err, res)
	}
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "speaker", Arguments: map[string]any{"action": "status"}})
	if err != nil || res.IsError {
		t.Fatalf("status: %v %+v", err, res)
	}
	b, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(b), `"online":true`) {
		t.Fatalf("status = %s", b)
	}
	tools, _ := cs.ListTools(ctx, nil)
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "speaker,speaker_say" && strings.Join(names, ",") != "speaker_say,speaker" {
		t.Fatalf("tools = %v (only speaker_say and speaker)", names)
	}
}

// The speaker tool: volume, do-not-disturb and "do", through Controls.
func TestMCPSpeakerTool(t *testing.T) {
	s, _, _ := newServer(t)
	ctl := &fakeControls{vol: 40}
	s.cfg.Controls = ctl
	s.mcp = s.mcpHandler()
	srv := httptest.NewServer(s.Public())
	defer srv.Close()
	c := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearer{"tok-claude"}}, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	ctx := context.Background()
	call := func(args map[string]any) (string, bool) {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "speaker", Arguments: args})
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		b, _ := json.Marshal(res.StructuredContent)
		if res.IsError {
			for _, ct := range res.Content {
				if tc, ok := ct.(*mcp.TextContent); ok {
					return tc.Text, true
				}
			}
		}
		return string(b), res.IsError
	}
	if out, bad := call(map[string]any{"action": "volume"}); bad || !strings.Contains(out, `"volume":40`) {
		t.Fatalf("read volume = %s", out)
	}
	if out, bad := call(map[string]any{"action": "volume", "volume": 25}); bad || ctl.vol != 25 || !strings.Contains(out, `"volume":25`) {
		t.Fatalf("set volume = %s %d", out, ctl.vol)
	}
	if out, bad := call(map[string]any{"action": "dnd", "on": true, "until": "08:00"}); bad || ctl.dndUntil != "08:00" || !strings.Contains(out, `"dnd":true`) {
		t.Fatalf("dnd on = %s", out)
	}
	if out, bad := call(map[string]any{"action": "dnd", "on": false}); bad || ctl.dndUntil != "off" {
		t.Fatalf("dnd off = %s", out)
	}
	if _, bad := call(map[string]any{"action": "do", "text": "明天八点叫我", "silent": true}); bad || ctl.did != "明天八点叫我/true" {
		t.Fatalf("do = %q", ctl.did)
	}
	if out, bad := call(map[string]any{"action": "do", "text": "帮我买牛奶"}); !bad || !strings.Contains(out, "不能") {
		t.Fatalf("unsafe do = %s %v", out, bad)
	}
	if _, bad := call(map[string]any{"action": "dance"}); !bad {
		t.Fatal("unknown action accepted")
	}
	if out, _ := call(map[string]any{"action": "status"}); !strings.Contains(out, `"online":true`) || !strings.Contains(out, `"dnd"`) {
		t.Fatalf("status = %s", out)
	}
}

type fakeControls struct {
	vol      int
	dndUntil string
	did      string
}

func (f *fakeControls) Volume(context.Context) (int, error)      { return f.vol, nil }
func (f *fakeControls) SetVolume(_ context.Context, v int) error { f.vol = v; return nil }
func (f *fakeControls) DND(_ context.Context, on bool, until string) (tools.DNDState, error) {
	if !on {
		f.dndUntil = "off"
		return tools.DNDState{}, nil
	}
	f.dndUntil = until
	return tools.DNDState{On: true, Until: "08:00"}, nil
}
func (f *fakeControls) DNDState() tools.DNDState {
	return tools.DNDState{On: f.dndUntil != "" && f.dndUntil != "off"}
}
func (f *fakeControls) Do(_ context.Context, text string, silent bool) error {
	f.did = fmt.Sprintf("%s/%v", text, silent)
	return nil
}

type bearer struct{ tok string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.tok)
	return http.DefaultTransport.RoundTrip(r)
}

func TestProbePortOnlyHasHealthAndMetrics(t *testing.T) {
	s, _, _ := newServer(t)
	p := s.Probe()
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		if c := do(p, "GET", path, "", nil).Code; c != 200 {
			t.Errorf("%s = %d", path, c)
		}
	}
	for _, path := range []string{"/v1/status", "/admin/status", "/mcp", "/"} {
		if c := do(p, "GET", path, "", map[string]string{"Authorization": "Bearer cli-key"}).Code; c != 404 {
			t.Errorf("%s = %d, want 404", path, c)
		}
	}
}

type fakePlayer struct{ got []string }

func (f *fakePlayer) Play(_ context.Context, what string) error {
	f.got = append(f.got, what)
	if what == "playlist:不存在" {
		return fmt.Errorf("%w: 不存在", ErrNoPlaylist)
	}
	return nil
}
func (f *fakePlayer) Playlists(context.Context) ([]string, error) {
	return []string{"睡前", "通勤"}, nil
}
func (f *fakePlayer) Do(_ context.Context, action string) error {
	f.got = append(f.got, "do:"+action)
	return nil
}

func TestPanelVolumeIsThePanelsCLIVolumeIsTheSpeakers(t *testing.T) {
	s, _, dev := newServer(t)
	ok := map[string]string{"X-Owner-User": "hardware", "Content-Type": "application/json", "Sec-Fetch-Site": "same-origin"}
	if c := do(s.Public(), "POST", "/api/volume", `{"volume":0}`, ok).Code; c != 204 {
		t.Fatalf("panel = %d", c)
	}
	if strings.Join(dev.vol, ",") != "panel" {
		t.Fatalf("vol %v", dev.vol)
	}
}

func TestPanelMusicControls(t *testing.T) {
	s, _, _ := newServer(t)
	p := &fakePlayer{}
	s.SetPlayer(p)
	pub := s.Public()
	ok := map[string]string{"X-Owner-User": "hardware", "Sec-Fetch-Site": "same-origin"}
	for _, a := range []string{"pause", "resume", "next", "stop"} {
		if c := do(pub, "POST", "/api/music/"+a, "", ok).Code; c != 204 {
			t.Fatalf("%s = %d", a, c)
		}
	}
	if c := do(pub, "POST", "/api/music/rm", "", ok).Code; c != 404 {
		t.Fatalf("bad action = %d", c)
	}
	if strings.Join(p.got, ",") != "do:pause,do:resume,do:next,do:stop" {
		t.Fatalf("got %v", p.got)
	}
}

func TestPanelSchedulesCRUD(t *testing.T) {
	s, _, _ := newServer(t)
	pub := s.Public()
	ok := map[string]string{"X-Owner-User": "hardware", "Content-Type": "application/json", "Sec-Fetch-Site": "same-origin"}
	rec := do(pub, "POST", "/api/schedules", `{"hour":8,"minute":0,"days":[1,2,3,4,5],"action":"play","arg":"liked"}`, ok)
	if rec.Code != 201 || !strings.Contains(rec.Body.String(), `"id":`) {
		t.Fatalf("add = %d %s", rec.Code, rec.Body)
	}
	if c := do(pub, "POST", "/api/schedules", `{"hour":25,"action":"play"}`, ok).Code; c != 400 {
		t.Fatalf("bad entry = %d", c)
	}
	rec = do(pub, "GET", "/api/schedules", "", ok)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"arg":"liked"`) || !strings.Contains(rec.Body.String(), `"next":`) {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if c := do(pub, "PUT", "/api/schedules/1", `{"hour":9,"minute":30,"action":"stop","disabled":true}`, ok).Code; c != 204 {
		t.Fatalf("update = %d", c)
	}
	if c := do(pub, "PUT", "/api/schedules/99", `{"hour":9,"action":"stop"}`, ok).Code; c != 404 {
		t.Fatalf("update missing = %d", c)
	}
	if c := do(pub, "DELETE", "/api/schedules/1", "", ok).Code; c != 204 {
		t.Fatalf("delete = %d", c)
	}
	// "just tomorrow" round-trips with its date.
	tomorrow := time.Now().In(localTime).AddDate(0, 0, 1).Format(time.DateOnly)
	if c := do(pub, "POST", "/api/schedules", `{"hour":8,"minute":0,"date":"`+tomorrow+`","action":"play","arg":"liked"}`, ok).Code; c != 201 {
		t.Fatalf("add once = %d", c)
	}
	if rec := do(pub, "GET", "/api/schedules", "", ok); !strings.Contains(rec.Body.String(), `"date":"`+tomorrow+`"`) || !strings.Contains(rec.Body.String(), `"next":`) {
		t.Fatalf("list once = %s", rec.Body)
	}
	cross := map[string]string{"X-Owner-User": "hardware", "Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}
	if c := do(pub, "DELETE", "/api/schedules/1", "", cross).Code; c != 403 {
		t.Fatalf("cross-site delete = %d", c)
	}
}

func TestPanelPlayNowAndPlaylists(t *testing.T) {
	s, _, _ := newServer(t)
	p := &fakePlayer{}
	s.SetPlayer(p)
	pub := s.Public()
	ok := map[string]string{"X-Owner-User": "hardware", "Content-Type": "application/json", "Sec-Fetch-Site": "same-origin"}
	if c := do(pub, "POST", "/api/play", `{"what":"liked"}`, ok).Code; c != 202 || p.got[0] != "liked" {
		t.Fatalf("play = %d %v", c, p.got)
	}
	if c := do(pub, "POST", "/api/play", `{"what":"playlist:不存在"}`, ok).Code; c != 404 {
		t.Fatalf("missing playlist = %d", c)
	}
	rec := do(pub, "GET", "/api/playlists", "", ok)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "睡前") {
		t.Fatalf("playlists = %d %s", rec.Code, rec.Body)
	}
}

func TestPlayWithoutHusicSaysSo(t *testing.T) {
	s, _, _ := newServer(t)
	ok := map[string]string{"X-Owner-User": "hardware", "Content-Type": "application/json", "Sec-Fetch-Site": "same-origin"}
	rec := do(s.Public(), "POST", "/api/play", `{"what":"liked"}`, ok)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "husic") {
		t.Fatalf("play without husic = %d %s", rec.Code, rec.Body)
	}
}

func TestPanelFilesAreNeverCachedByOthers(t *testing.T) {
	s, _, _ := newServer(t)
	page := do(s.Public(), "GET", "/", "", map[string]string{"X-Owner-User": "hardware"}).Body.String()
	if !strings.Contains(page, "/assets/panel.js?v=") || !strings.Contains(page, "/assets/panel.css?v=") {
		t.Fatalf("page links are not stamped")
	}
	rec := do(s.Public(), "GET", "/assets/panel.js", "", map[string]string{"X-Owner-User": "hardware"})
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "private") {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	if c := do(s.Public(), "GET", "/assets/panel.js", "", nil).Code; c != 401 {
		t.Fatalf("logged out = %d", c)
	}
}

func TestTrayRoutesTakeThePCKey(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	dev := &fakeDev{}
	s := New(Config{Auth: AuthHeader, MCPTokens: map[string]string{"tok-claude": "claude"}, BotKey: "bot", CLIKey: "cli", PC: http.NotFoundHandler(), PCKey: "pc-key", Audio: http.NotFoundHandler()}, &fakeSayer{}, dev, st)
	p := &fakePlayer{}
	s.SetPlayer(p)
	pub := s.Public()
	pc := map[string]string{"Authorization": "Bearer pc-key", "Content-Type": "application/json"}
	if rec := do(pub, "GET", "/pc/status", "", pc); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"online":true`) {
		t.Fatalf("status %d %s", rec.Code, rec.Body)
	}
	if c := do(pub, "POST", "/pc/music/pause", "", pc).Code; c != 204 || p.got[0] != "do:pause" {
		t.Fatalf("music %d %v", c, p.got)
	}
	if c := do(pub, "POST", "/pc/play", `{"what":"liked"}`, pc).Code; c != 202 || p.got[1] != "liked" {
		t.Fatalf("play %d %v", c, p.got)
	}
	if c := do(pub, "POST", "/pc/volume", `{"volume":30}`, pc).Code; c != 204 || dev.vol[0] != "panel" {
		t.Fatalf("volume %d %v", c, dev.vol)
	}
	if c := do(pub, "GET", "/pc/status", "", map[string]string{"Authorization": "Bearer nope"}).Code; c != 401 {
		t.Fatalf("wrong key = %d", c)
	}
	if c := do(pub, "GET", "/pc/status", "", map[string]string{"X-Owner-User": "hardware"}).Code; c != 401 {
		t.Fatalf("owner login is not the PC key: %d", c)
	}
}

// the panel's 今天 shows today.s reminders too, read-only.
func TestPanelListsTheBotsReminders(t *testing.T) {
	s, _, _ := newServer(t)
	s.SetReminders(func(context.Context) ([]Reminder, error) {
		return []Reminder{{At: time.Date(2026, 11, 16, 15, 0, 0, 0, localTime), Text: "喝水"}}, nil
	})
	ok := map[string]string{"X-Owner-User": "hardware"}
	rec := do(s.Public(), "GET", "/api/reminders", "", ok)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"text":"喝水"`) {
		t.Fatalf("= %d %s", rec.Code, rec.Body)
	}
	s.SetReminders(nil)
	if rec := do(s.Public(), "GET", "/api/reminders", "", ok); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("no bot = %d %s", rec.Code, rec.Body)
	}
}

func TestPanelDND(t *testing.T) {
	s, _, _ := newServer(t)
	ctl := &fakeControls{}
	s.cfg.Controls = ctl
	ok := map[string]string{"X-Owner-User": "me", "Content-Type": "application/json", "Sec-Fetch-Site": "same-origin"}
	rec := do(s.Public(), "POST", "/api/dnd", `{"on":true,"until":"08:00"}`, ok)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"dnd":true`) || ctl.dndUntil != "08:00" {
		t.Fatalf("on = %d %s", rec.Code, rec.Body)
	}
	if rec := do(s.Public(), "GET", "/api/status", "", ok); !strings.Contains(rec.Body.String(), `"dnd":true`) {
		t.Fatalf("status = %s", rec.Body)
	}
	if rec := do(s.Public(), "POST", "/api/dnd", `{"on":false}`, ok); rec.Code != 200 || ctl.dndUntil != "off" {
		t.Fatalf("off = %d %s", rec.Code, rec.Body)
	}
	if c := do(s.Public(), "POST", "/api/dnd", `{"on":true}`, nil).Code; c != 401 {
		t.Fatalf("no login = %d", c)
	}
}
