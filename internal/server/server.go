// Package server is xueer-speaker's HTTP face. The listeners never share a
// route:
//
//   - public: /mcp for AI clients, /a/ for the speaker, /pc/ for the Windows
//     tray, the panel (/ and /api/*, behind its own login or a proxy's);
//   - internal (local or a private network): /v1/* for scripts and bots,
//     /admin/* for the CLI, /metrics /healthz /readyz;
//   - probe: /metrics /healthz /readyz only.
package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hardware661/xueer-speaker/internal/announcer"
	"github.com/hardware661/xueer-speaker/internal/schedule"
	"github.com/hardware661/xueer-speaker/internal/store"
	"github.com/hardware661/xueer-speaker/internal/tools"
	"github.com/hardware661/xueer-speaker/internal/voice"
)

//go:embed web
var webFS embed.FS

// Sayer queues lines (announcer.Announcer).
type Sayer interface {
	Say(ctx context.Context, r announcer.Request) (announcer.Result, error)
}

// Device is what the panel and CLI may do to the speaker directly.
type Device interface {
	Snapshot(ctx context.Context) Status
	SetVolume(ctx context.Context, v int) error // the speaker's (CLI, bot)
	// PanelVolume is the panel's slider: the PC's system volume while the
	// PC is on, the speaker's otherwise.
	PanelVolume(ctx context.Context, v int) error
	Stop(ctx context.Context) error
}

// Player starts music sessions (Husic → conductor). what is "liked" or
// "playlist:<name>". Nil until Husic is wired up.
type Player interface {
	Play(ctx context.Context, what string) error
	Playlists(ctx context.Context) ([]string, error)
	// Do is next / pause / resume / stop, wherever the music plays.
	Do(ctx context.Context, action string) error
}

// ErrNoHusic / ErrNoPlaylist are what a Player returns for those cases.
var (
	ErrNoHusic = errors.New("husic not connected")
	// ErrHusicClosed: the owner's PC is on the speaker but no Husic is open
	// on it, so nothing can play the music there.
	ErrHusicClosed = errors.New("电脑上的 Husic 没开着")
	ErrNoPlaylist  = errors.New("no such playlist")
)

// Status is the speaker as the panel, CLI and speaker_status see it.
type Status struct {
	Online  bool   `json:"online"`
	Login   string `json:"login"` // ok / renewing / login-required / risk-control
	State   string `json:"state"` // conductor state
	Volume  int    `json:"volume"`
	Playing string `json:"playing,omitempty"` // song title while ours plays (or the PC's Husic)
	Artist  string `json:"artist,omitempty"`  // the PC's Husic only
	Paused  bool   `json:"paused,omitempty"`  // the music is paused
	Where   string `json:"where,omitempty"`   // who plays music: speaker / pc
	PC      bool   `json:"pc"`                // the owner's PC is on and takes every line
	Husic   bool   `json:"husic"`             // a Husic is open (the PC's exe or a browser tab)
	HusicOn bool   `json:"husic_wired"`       // Husic is configured at all
	Voice   string `json:"voice"`             // auto / native
	Error   string `json:"error,omitempty"`   // why the speaker could not be read
	DND     bool   `json:"dnd"`               // do-not-disturb: lines are kept, not spoken
	DNDTill string `json:"dnd_until,omitempty"`
}

// Config is the server's secrets and parts.
type Config struct {
	Auth      Auth              // password (default) / header
	Log       *slog.Logger      // nil = discard
	Persona   Persona           // who speaks; empty = 雪儿 calling 主人
	Xiaomi    Xiaomi            // the account the panel logs in; nil = no 小米 page
	Controls  Controls          // volume, do-not-disturb, "do" for the speaker tool; nil = status only
	MCPTokens map[string]string // bearer → source name (claude / codex / spare)
	BotKey    string
	CLIKey    string
	Audio     http.Handler // /a/
	PC        http.Handler // /pc/ (xueer-speaker-pc; checks its own key)
	PCKey     string       // the same key, for the tray's /pc/status, /pc/music, /pc/volume
}

// Server holds the routes.
type Server struct {
	cfg   Config
	say   Sayer
	dev   Device
	store *store.Store
	mcp   http.Handler

	playerMu sync.Mutex
	player   Player

	earHealth func() any // listener.Health, set when voice commands are wired
	reminders func(context.Context) ([]Reminder, error)

	gate gate

	mu      sync.Mutex
	counts  map[[3]string]int // source, voice, result → n
	started time.Time
}

// New wires the routes.
func New(cfg Config, say Sayer, dev Device, st *store.Store) *Server {
	if cfg.Auth == "" {
		cfg.Auth = AuthPassword
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	s := &Server{cfg: cfg, say: say, dev: dev, store: st, counts: map[[3]string]int{}, started: time.Now()}
	s.mcp = s.mcpHandler()
	s.initGate()
	return s
}

// SetPlayer connects music playback (after Husic is reachable).
func (s *Server) SetPlayer(p Player) {
	s.playerMu.Lock()
	s.player = p
	s.playerMu.Unlock()
}

func (s *Server) getPlayer() Player {
	s.playerMu.Lock()
	defer s.playerMu.Unlock()
	return s.player
}

// Voice is the panel's voice switch: "auto" (Xiaoxiao, native as fallback)
// or "native".
func (s *Server) Voice() string { return s.store.Setting("voice", "auto") }

// Listening is the panel's switch for voice commands.
func (s *Server) Listening() bool { return s.store.Setting("listener", "on") == "on" }

// Recorder returns where the announcer reports lines: the store, plus the
// counters behind speaker_say_total.
func (s *Server) Recorder() announcer.Recorder { return recorder{s} }

type recorder struct{ s *Server }

func (r recorder) Queued(e announcer.Entry) {
	r.s.store.AddSay(store.Say{ID: e.ID, At: e.At, Source: e.Source, Text: e.Text})
}

func (r recorder) Spoken(e announcer.Entry) {
	r.s.store.FinishSay(e.ID, e.Voice, e.Err)
	result := "ok"
	if e.Err != "" {
		result = "error"
	}
	r.s.mu.Lock()
	r.s.counts[[3]string{metricSource(e.Source), e.Voice, result}]++
	r.s.mu.Unlock()
}

// metricSource keeps label cardinality bounded.
func metricSource(src string) string {
	if i := strings.IndexByte(src, ':'); i > 0 {
		src = src[:i]
	}
	switch src {
	case "claude", "codex", "grok", "spare", "bot", "panel", "cli", "schedule", "listener":
		return src
	}
	return "other"
}

// ---- public listener -------------------------------------------------------

// Public is the handler behind the edge.
func (s *Server) Public() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", s.mcp)
	mux.Handle("/a/", s.cfg.Audio)
	if s.cfg.PC != nil {
		mux.Handle("/pc/", s.cfg.PC)
		// The PC's tray menu: what plays, the music buttons and the volume,
		// with the PC's key instead of the owner's login.
		mux.HandleFunc("GET /pc/status", s.key(s.cfg.PCKey, s.apiStatus))
		mux.HandleFunc("POST /pc/music/{action}", s.key(s.cfg.PCKey, s.apiMusic))
		mux.HandleFunc("POST /pc/volume", s.key(s.cfg.PCKey, s.apiPanelVolume))
		mux.HandleFunc("POST /pc/play", s.key(s.cfg.PCKey, s.apiPlay))
	}
	mux.HandleFunc("GET /api/auth", s.apiAuth)
	if s.cfg.Auth == AuthPassword {
		mux.HandleFunc("POST /api/auth/setup", s.sameOrigin(s.apiSetup))
		mux.HandleFunc("POST /api/auth/login", s.sameOrigin(s.apiLogin))
		mux.HandleFunc("POST /api/auth/logout", s.sameOriginNoBody(s.apiLogout))
	}
	if s.cfg.Xiaomi != nil {
		mux.HandleFunc("GET /api/xiaomi", s.owner(s.apiXiaomi))
		mux.HandleFunc("POST /api/xiaomi/login", s.owner(s.sameOrigin(s.apiXiaomiLogin)))
		mux.HandleFunc("POST /api/xiaomi/verify", s.owner(s.sameOrigin(s.apiXiaomiVerify)))
		mux.HandleFunc("POST /api/xiaomi/logout", s.owner(s.sameOriginNoBody(s.apiXiaomiLogout)))
		mux.HandleFunc("POST /api/xiaomi/speaker", s.owner(s.sameOrigin(s.apiXiaomiSpeaker)))
	}
	mux.HandleFunc("GET /api/connect", s.owner(s.apiConnect))
	mux.HandleFunc("GET /api/status", s.owner(func(w http.ResponseWriter, r *http.Request) { s.learnBase(r); s.apiStatus(w, r) }))
	mux.HandleFunc("GET /api/history", s.owner(s.apiHistory))
	mux.HandleFunc("POST /api/say", s.owner(s.sameOrigin(s.apiSay)))
	mux.HandleFunc("POST /api/volume", s.owner(s.sameOrigin(s.apiPanelVolume)))
	mux.HandleFunc("POST /api/stop", s.owner(s.sameOrigin(s.apiStop)))
	mux.HandleFunc("POST /api/settings", s.owner(s.sameOrigin(s.apiSettings)))
	mux.HandleFunc("GET /api/schedules", s.owner(s.apiSchedules))
	mux.HandleFunc("GET /api/reminders", s.owner(s.apiReminders))
	mux.HandleFunc("POST /api/schedules", s.owner(s.sameOrigin(s.apiAddSchedule)))
	mux.HandleFunc("PUT /api/schedules/{id}", s.owner(s.sameOrigin(s.apiUpdateSchedule)))
	mux.HandleFunc("DELETE /api/schedules/{id}", s.owner(s.sameOriginNoBody(s.apiDeleteSchedule)))
	mux.HandleFunc("POST /api/play", s.owner(s.sameOrigin(s.apiPlay)))
	mux.HandleFunc("POST /api/music/{action}", s.owner(s.sameOriginNoBody(s.apiMusic)))
	mux.HandleFunc("GET /api/playlists", s.owner(s.apiPlaylists))
	static, _ := fs.Sub(webFS, "web")
	files := http.FileServerFS(static)
	index := stampAssets(static)
	page := s.owner
	if s.cfg.Auth == AuthPassword {
		page = func(h http.HandlerFunc) http.HandlerFunc { return h } // the page itself shows the login form
	}
	mux.HandleFunc("GET /{$}", page(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(index)
	}))
	// Panel files are the owner's: never kept by Cloudflare or a shared cache
	// (Cloudflare served a stale panel.js for hours, also logged out).
	mux.Handle("GET /assets/", page(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-cache")
		files.ServeHTTP(w, r)
	}))
	return securityHeaders(mux)
}

// stampAssets adds a content hash to the page's /assets/ links, so a
// browser or cache never pairs a new page with an old script.
func stampAssets(static fs.FS) []byte {
	page, _ := fs.ReadFile(static, "index.html")
	for _, name := range []string{"panel.css", "panel.js"} {
		b, _ := fs.ReadFile(static, "assets/"+name)
		sum := sha256.Sum256(b)
		page = bytes.ReplaceAll(page, []byte("/assets/"+name+`"`), []byte("/assets/"+name+"?v="+hex.EncodeToString(sum[:4])+`"`))
	}
	return page
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/a/") {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "same-origin")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		}
		h.ServeHTTP(w, r)
	})
}

// sameOrigin rejects cross-site writes: the browser must say same-origin and
// send JSON (which a plain HTML form cannot).
func (s *Server) sameOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if site := r.Header.Get("Sec-Fetch-Site"); site != "same-origin" {
			writeErr(w, http.StatusForbidden, "cross_site", "只接受本站页面发起的修改")
			return
		}
		if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
			writeErr(w, http.StatusUnsupportedMediaType, "json_only", "只收 JSON")
			return
		}
		next(w, r)
	}
}

// sameOriginNoBody is sameOrigin for requests without a body (DELETE).
func (s *Server) sameOriginNoBody(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Sec-Fetch-Site") != "same-origin" {
			writeErr(w, http.StatusForbidden, "cross_site", "只接受本站页面发起的修改")
			return
		}
		next(w, r)
	}
}

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.status(r.Context()))
}

func (s *Server) status(ctx context.Context) Status {
	st := s.dev.Snapshot(ctx)
	st.Voice = s.Voice()
	st.HusicOn = s.getPlayer() != nil
	if c := s.cfg.Controls; c != nil {
		d := c.DNDState()
		st.DND, st.DNDTill = d.On, d.Until
	}
	return st
}

func (s *Server) apiHistory(w http.ResponseWriter, r *http.Request) {
	says, _ := s.store.Says(50)
	heard, _ := s.store.Heard(30)
	if says == nil {
		says = []store.Say{}
	}
	if heard == nil {
		heard = []store.Heard{}
	}
	out := map[string]any{"says": says, "heard": heard, "listener": s.Listening()}
	if h := s.earHealth; h != nil {
		out["ear"] = h()
	}
	writeJSON(w, http.StatusOK, out)
}

// Reminder is one of the bot's reminders for today (read-only on the panel).
type Reminder struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// SetReminders lets the panel list the bot's reminders.
func (s *Server) SetReminders(f func(context.Context) ([]Reminder, error)) { s.reminders = f }

func (s *Server) apiReminders(w http.ResponseWriter, r *http.Request) {
	out := []Reminder{}
	if f := s.reminders; f != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		if got, err := f(ctx); err == nil && got != nil {
			out = got
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// SetEarHealth lets the panel show how reading the conversation log goes.
func (s *Server) SetEarHealth(f func() any) { s.earHealth = f }

type sayBody struct {
	Text   string `json:"text"`
	Source string `json:"source"`
	Volume int    `json:"volume"`
	Native bool   `json:"native"`
}

func (s *Server) apiSay(w http.ResponseWriter, r *http.Request) {
	var b sayBody
	if !readJSON(w, r, &b) {
		return
	}
	s.doSay(w, r.Context(), announcer.Request{Text: b.Text, Source: "panel", Volume: b.Volume, Native: b.Native})
}

func (s *Server) apiVolume(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Volume int `json:"volume"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	if b.Volume < 6 { // the speaker's own floor
		writeErr(w, http.StatusBadRequest, "bad_volume", "音箱音量 6～100")
		return
	}
	s.doVolume(w, r.Context(), b.Volume, s.dev.SetVolume)
}

func (s *Server) apiPanelVolume(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Volume int `json:"volume"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	s.doVolume(w, r.Context(), b.Volume, s.dev.PanelVolume)
}

func (s *Server) apiStop(w http.ResponseWriter, r *http.Request) {
	s.doStop(w, r.Context())
}

func (s *Server) apiSettings(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Voice    *string `json:"voice"`
		Listener *bool   `json:"listener"`
		Base     *string `json:"base"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	if b.Base != nil {
		u, err := url.Parse(strings.TrimSpace(*b.Base))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" {
			writeErr(w, http.StatusBadRequest, "bad_base", "要写 http:// 或 https:// 开头、音箱访问得到的地址")
			return
		}
		s.store.SetSetting("base", strings.TrimRight(u.String(), "/"))
	}
	if b.Voice != nil {
		if *b.Voice != "auto" && *b.Voice != "native" {
			writeErr(w, http.StatusBadRequest, "bad_voice", "嗓子只有 auto / native")
			return
		}
		s.store.SetSetting("voice", *b.Voice)
	}
	if b.Listener != nil {
		v := "off"
		if *b.Listener {
			v = "on"
		}
		s.store.SetSetting("listener", v)
	}
	w.WriteHeader(http.StatusNoContent)
}

type scheduleView struct {
	schedule.Entry
	Next *time.Time `json:"next,omitempty"`
}

func (s *Server) apiSchedules(w http.ResponseWriter, r *http.Request) {
	entries, err := s.store.Schedules()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store", err.Error())
		return
	}
	now := time.Now().In(localTime)
	out := make([]scheduleView, 0, len(entries))
	for _, e := range entries {
		v := scheduleView{Entry: e}
		if t, ok := e.Next(now); ok {
			v.Next = &t
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) apiAddSchedule(w http.ResponseWriter, r *http.Request) {
	var e schedule.Entry
	if !readJSON(w, r, &e) {
		return
	}
	if err := e.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_entry", err.Error())
		return
	}
	id, err := s.store.AddSchedule(e)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "store", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) apiUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", "id 不对")
		return
	}
	var e schedule.Entry
	if !readJSON(w, r, &e) {
		return
	}
	if err := e.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_entry", err.Error())
		return
	}
	e.ID = id
	switch err := s.store.UpdateSchedule(e); {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "没有这条定时")
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "store", err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) apiDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_id", "id 不对")
		return
	}
	switch err := s.store.DeleteSchedule(id); {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "没有这条定时")
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "store", err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) apiPlay(w http.ResponseWriter, r *http.Request) {
	var b struct {
		What string `json:"what"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	s.doPlay(w, r.Context(), b.What)
}

func (s *Server) doPlay(w http.ResponseWriter, ctx context.Context, what string) {
	p := s.getPlayer()
	if p == nil {
		writeErr(w, http.StatusServiceUnavailable, "no_husic", "还没接上 husic")
		return
	}
	switch err := p.Play(ctx, what); {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]string{"playing": what})
	case errors.Is(err, ErrHusicClosed):
		writeErr(w, http.StatusConflict, "husic_closed", "电脑连着音箱，但电脑上的 Husic 没开着：开一下再放")
	case errors.Is(err, ErrNoHusic):
		writeErr(w, http.StatusServiceUnavailable, "no_husic", err.Error())
	case errors.Is(err, ErrNoPlaylist):
		writeErr(w, http.StatusNotFound, "no_playlist", err.Error())
	default:
		writeErr(w, http.StatusBadGateway, "play", err.Error())
	}
}

// Play starts music for the scheduler, same rules as the panel button.
func (s *Server) Play(ctx context.Context, what string) error {
	p := s.getPlayer()
	if p == nil {
		return ErrNoHusic
	}
	return p.Play(ctx, what)
}

// apiMusic is the panel's next / pause / resume / stop for the music (owner
// while the PC is on they go to its Husic and never touch the
// speaker, whose pause would reach the PC over Bluetooth).
func (s *Server) apiMusic(w http.ResponseWriter, r *http.Request) {
	action := r.PathValue("action")
	if action != "next" && action != "pause" && action != "resume" && action != "stop" {
		writeErr(w, http.StatusNotFound, "bad_action", "没有这个操作")
		return
	}
	p := s.getPlayer()
	if p == nil {
		if action == "stop" {
			s.doStop(w, r.Context())
			return
		}
		writeErr(w, http.StatusServiceUnavailable, "no_husic", "还没接上 husic")
		return
	}
	switch err := p.Do(r.Context(), action); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, ErrHusicClosed):
		writeErr(w, http.StatusConflict, "husic_closed", "电脑上的 Husic 没开着")
	default:
		writeErr(w, http.StatusBadGateway, "music", err.Error())
	}
}

func (s *Server) apiPlaylists(w http.ResponseWriter, r *http.Request) {
	p := s.getPlayer()
	if p == nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	names, err := p.Playlists(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "husic", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, names)
}

// localTime is the schedules' clock: the service's local time (TZ, which
// the image sets to Asia/Shanghai).
var localTime = time.Local

// ---- internal listener -----------------------------------------------------

// Probe is the handler mapped to the host for Prometheus, Kuma and the
// container healthcheck: nothing but health and metrics.
func (s *Server) Probe() http.Handler {
	mux := http.NewServeMux()
	s.probeRoutes(mux)
	return mux
}

func (s *Server) probeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /metrics", s.metrics)
}

// Internal is the handler on the container network.
func (s *Server) Internal() http.Handler {
	mux := http.NewServeMux()
	s.probeRoutes(mux)

	mux.HandleFunc("POST /v1/say", s.key(s.cfg.BotKey, func(w http.ResponseWriter, r *http.Request) {
		var b sayBody
		if !readJSON(w, r, &b) {
			return
		}
		s.doSay(w, r.Context(), announcer.Request{Text: b.Text, Source: "bot:" + strings.TrimPrefix(b.Source, "bot:"), Volume: b.Volume})
	}))
	mux.HandleFunc("GET /v1/status", s.key(s.cfg.BotKey, s.apiStatus))

	mux.HandleFunc("POST /admin/say", s.key(s.cfg.CLIKey, func(w http.ResponseWriter, r *http.Request) {
		var b sayBody
		if !readJSON(w, r, &b) {
			return
		}
		s.doSay(w, r.Context(), announcer.Request{Text: b.Text, Source: "cli", Volume: b.Volume, Native: b.Native})
	}))
	mux.HandleFunc("GET /admin/status", s.key(s.cfg.CLIKey, s.apiStatus))
	mux.HandleFunc("GET /admin/history", s.key(s.cfg.CLIKey, s.apiHistory))
	mux.HandleFunc("POST /admin/volume", s.key(s.cfg.CLIKey, s.apiVolume))
	mux.HandleFunc("POST /admin/stop", s.key(s.cfg.CLIKey, s.apiStop))
	return mux
}

func (s *Server) key(want string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || want == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "钥匙不对")
			return
		}
		next(w, r)
	}
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	st := s.dev.Snapshot(r.Context())
	if st.Login == "login-required" || st.Login == "risk-control" {
		http.Error(w, "xiaomi "+st.Login, http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ready\n"))
}

func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	st := s.dev.Snapshot(r.Context())
	login := map[string]int{"ok": 0, "renewing": 1, "login-required": 2, "risk-control": 3}[st.Login]
	online := 0
	if st.Online {
		online = 1
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# HELP speaker_login_state Xiaomi login: 0 ok, 1 renewing, 2 login required, 3 risk control.\n# TYPE speaker_login_state gauge\nspeaker_login_state %d\n", login)
	fmt.Fprintf(w, "# HELP speaker_online Whether the speaker answered the last status read.\n# TYPE speaker_online gauge\nspeaker_online %d\n", online)
	s.mu.Lock()
	keys := make([][3]string, 0, len(s.counts))
	for k := range s.counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
	fmt.Fprint(w, "# HELP speaker_say_total Lines spoken, by source, voice and result.\n# TYPE speaker_say_total counter\n")
	for _, k := range keys {
		fmt.Fprintf(w, "speaker_say_total{source=%q,voice=%q,result=%q} %d\n", k[0], k[1], k[2], s.counts[k])
	}
	s.mu.Unlock()
}

// ---- shared actions --------------------------------------------------------

func (s *Server) doSay(w http.ResponseWriter, ctx context.Context, req announcer.Request) {
	if s.Voice() == "native" {
		req.Native = true
	}
	res, err := s.say.Say(ctx, req)
	if err != nil {
		code, status := sayError(err)
		writeErr(w, status, code, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": res.ID})
}

func sayError(err error) (string, int) {
	switch {
	case errors.Is(err, voice.ErrTooLong):
		return "too_long", http.StatusBadRequest
	case errors.Is(err, voice.ErrEmpty):
		return "empty", http.StatusBadRequest
	case errors.Is(err, announcer.ErrRateLimited):
		return "rate_limited", http.StatusTooManyRequests
	case errors.Is(err, announcer.ErrDuplicate):
		return "duplicate", http.StatusTooManyRequests
	}
	return "internal", http.StatusInternalServerError
}

func (s *Server) doVolume(w http.ResponseWriter, ctx context.Context, v int, set func(context.Context, int) error) {
	if v < 0 || v > 100 {
		writeErr(w, http.StatusBadRequest, "bad_volume", "音量 0～100")
		return
	}
	if err := set(ctx, v); err != nil {
		writeErr(w, http.StatusBadGateway, "speaker", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) doStop(w http.ResponseWriter, ctx context.Context) {
	if err := s.dev.Stop(ctx); err != nil {
		writeErr(w, http.StatusBadGateway, "speaker", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- MCP -------------------------------------------------------------------

// tokenSource is who called, from the bearer (claude / codex / grok …).
func tokenSource(req *mcp.CallToolRequest) string {
	if ti := req.Extra.TokenInfo; ti != nil && ti.UserID != "" {
		return ti.UserID
	}
	return "other"
}

func (s *Server) mcpHandler() http.Handler {
	p := tools.Persona{Name: s.cfg.Persona.Name, Owner: s.cfg.Persona.Owner}
	srv := mcp.NewServer(&mcp.Implementation{Name: "xueer-speaker", Version: "1"}, &mcp.ServerOptions{Instructions: p.Instructions()})
	tools.Register(srv, mcpBackend{s}, p, tokenSource)
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	verify := func(_ context.Context, tok string, _ *http.Request) (*auth.TokenInfo, error) {
		for want, src := range s.cfg.MCPTokens {
			if subtle.ConstantTimeCompare([]byte(tok), []byte(want)) == 1 {
				return &auth.TokenInfo{UserID: src, Expiration: time.Now().Add(time.Hour)}, nil
			}
		}
		return nil, auth.ErrInvalidToken
	}
	return auth.RequireBearerToken(verify, nil)(h)
}

// ---- helpers ---------------------------------------------------------------

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_json", "请求体不是合法 JSON")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
