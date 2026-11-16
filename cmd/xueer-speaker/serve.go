package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hardware661/xueer-speaker/internal/announcer"
	"github.com/hardware661/xueer-speaker/internal/audio"
	"github.com/hardware661/xueer-speaker/internal/command"
	"github.com/hardware661/xueer-speaker/internal/conductor"
	"github.com/hardware661/xueer-speaker/internal/control"
	"github.com/hardware661/xueer-speaker/internal/jukebox"
	"github.com/hardware661/xueer-speaker/internal/listener"
	"github.com/hardware661/xueer-speaker/internal/music"
	"github.com/hardware661/xueer-speaker/internal/pcmouth"
	"github.com/hardware661/xueer-speaker/internal/schedule"
	"github.com/hardware661/xueer-speaker/internal/server"
	"github.com/hardware661/xueer-speaker/internal/store"
	"github.com/hardware661/xueer-speaker/internal/voice"
	"github.com/hardware661/xueer-speaker/internal/xiaomi"
)

// serveFlags are serve's settings. Each one can also come from the
// environment as XS_<NAME> (XS_BASE, XS_HUSIC …); a flag given on the
// command line wins.
type serveFlags struct {
	set                                    *flag.FlagSet
	data, cache, keys, xiaomi, base        *string
	edgeTTS, public, internal, probe, auth *string
	speaker, husic, husicKey, reminders    *string
	persona, owner                         *string
}

func newServeFlags() serveFlags {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	return serveFlags{
		set:       fs,
		data:      fs.String("data", "/data", "数据目录：数据库、钥匙、小米登录都放这里"),
		cache:     fs.String("cache", "/data/cache", "语音缓存目录"),
		keys:      fs.String("keys", "", "钥匙文件（空 = <data>/keys.json，第一次启动自动生成）"),
		xiaomi:    fs.String("xiaomi", "", "小米登录文件（空 = <data>/xiaomi.json，在面板里登录后生成）"),
		base:      fs.String("base", "", "音箱拉语音用的地址，音箱必须访问得到（如 http://192.168.1.10:8100）；空 = 第一次打开面板时的地址"),
		edgeTTS:   fs.String("edge-tts", "edge-tts", "edge-tts 命令"),
		public:    fs.String("public", ":8100", "面板、MCP、音箱取语音的端口"),
		internal:  fs.String("internal", "127.0.0.1:8101", "给脚本 / 机器人 / 命令行的端口（/v1、/admin）"),
		probe:     fs.String("probe", ":8102", "健康检查和指标端口（healthz / readyz / metrics）"),
		auth:      fs.String("auth", "password", "面板登录：password = 面板自己的密码；header = 前面的反向代理已登录并设 X-Owner-User"),
		speaker:   fs.String("speaker", "", "用哪台音箱（名字、did 或 MAC）；空 = 在面板里选，账号下只有一台时自动用它"),
		husic:     fs.String("husic", "", "Husic 地址（如 http://husic:18788）；空 = 不接放歌"),
		husicKey:  fs.String("husic-key", "", "Husic 服务钥匙文件（Husic 的 -service-key-file）"),
		reminders: fs.String("reminders", "", "提醒来源地址：GET 返回今天的提醒 [{at,text}]，面板列出来；空 = 不列"),
		persona:   fs.String("persona", "雪儿", "AI 说话时用的名字"),
		owner:     fs.String("owner", "主人", "AI 怎么称呼你"),
	}
}

// parseWithEnv parses args after filling every flag not given from XS_<NAME>.
func parseWithEnv(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return err
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	var err error
	fs.VisitAll(func(f *flag.Flag) {
		if given[f.Name] || err != nil {
			return
		}
		if v, ok := os.LookupEnv("XS_" + strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_"))); ok {
			err = f.Value.Set(v)
		}
	})
	return err
}

func serve(args []string) error {
	f := newServeFlags()
	if err := parseWithEnv(f.set, args); err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	for _, d := range []string{*f.data, *f.cache} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	or := func(v, def string) string {
		if v == "" {
			return def
		}
		return v
	}
	k, err := loadOrCreateKeys(or(*f.keys, filepath.Join(*f.data, "keys.json")))
	if err != nil {
		return err
	}
	acct, err := xiaomi.NewAccount(xiaomi.Production, or(*f.xiaomi, filepath.Join(*f.data, "xiaomi.json")))
	if err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(*f.data, "speaker.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	st.MarkOrphans("服务重启了，这句没念成")
	if *f.speaker != "" {
		st.SetSetting("speaker", *f.speaker)
	}
	if *f.base != "" {
		st.SetSetting("base", strings.TrimRight(*f.base, "/"))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	dev := &device{acct: acct, store: st, log: log}
	host := audio.New(func() string { return st.Setting("base", "") }, nil)
	host.Log = log
	cond := conductor.New(dev, conductor.Options{Fetches: host.Fetches})
	dev.cond = cond
	go cond.Run(ctx)
	vo := voice.New(*f.cache, voice.EdgeCLI{Bin: *f.edgeTTS})
	// Every line plays on the PC while xueer-speaker-pc is connected,
	// otherwise on the speaker.
	pc := pcmouth.New(k.PC)
	dev.pc = pc
	ann := announcer.New(vo, host, pc.Route(cond), nil)
	ann.Quiet = &control.Quiet{}
	ctl := &controls{dev: dev, quiet: ann.Quiet, ann: ann, store: st, log: log, owner: *f.owner}
	ctl.restore()
	go ctl.watch(ctx)
	srv := server.New(server.Config{
		Auth: server.Auth(*f.auth), Log: log, Persona: server.Persona{Name: *f.persona, Owner: *f.owner},
		MCPTokens: k.MCP, BotKey: k.Bot, CLIKey: k.CLI, Audio: host, PC: pc.Handler(), PCKey: k.PC,
		Xiaomi: dev, Controls: ctl, SelfTest: selfTest(dev, vo, host),
	}, ann, dev, st)
	if *f.reminders != "" {
		srv.SetReminders(fetchReminders(*f.reminders, k.Bot))
	}
	lis := listener.New(dev, nil)
	srv.SetEarHealth(func() any { return lis.Health() })
	ann.Record(ownLines{srv.Recorder(), lis})
	if *f.husic != "" {
		key, err := os.ReadFile(*f.husicKey)
		if err != nil {
			return fmt.Errorf("-husic needs -husic-key: %w", err)
		}
		lib := music.NewClient(*f.husic, strings.TrimSpace(string(key)))
		jb := jukebox.New(lib, host, cond.Command)
		jb.PC = pcPlayer{dev: dev, lib: lib}
		jb.Idle = func() bool { return cond.State() == conductor.StateIdle }
		dev.jb, dev.lib = jb, lib
		srv.SetPlayer(jb)
		disp := &command.Dispatcher{
			Play:  jb.Start,
			Send:  cond.Command,
			State: cond.State,
			PC:    jb.Control,
			Owner: *f.owner,
			Say: func(text string) {
				ann.Say(ctx, announcer.Request{Text: text, Source: "listener", Native: srv.Voice() == "native"})
			},
		}
		// Music is going: our own session, or the PC's Husic playing (asked
		// at most every 10 s; Where alone stays "pc" long after it stopped).
		var husicAt time.Time
		var husicOn bool
		music := func() bool {
			if s := cond.State(); s != conductor.StateIdle && s != conductor.StateSpeaking {
				return true
			}
			if jb.Where() != jukebox.WherePC {
				return false
			}
			if time.Since(husicAt) > 10*time.Second {
				husicAt, husicOn = time.Now(), jb.PC.Playing(ctx)
			}
			return husicOn
		}
		go runListener(ctx, log, lis, disp, st, srv, music)
	}

	go housekeeping(ctx, st, vo)
	go runSchedules(ctx, log, st, ann, dev, srv, *f.owner)

	servers := []*http.Server{
		{Addr: *f.public, Handler: srv.Public()},
		{Addr: *f.internal, Handler: srv.Internal()},
		{Addr: *f.probe, Handler: srv.Probe()},
	}
	errc := make(chan error, len(servers))
	for _, h := range servers {
		h.ReadHeaderTimeout = 10 * time.Second
		h.BaseContext = func(net.Listener) context.Context { return ctx }
		go func() {
			if err := h.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- fmt.Errorf("%s: %w", h.Addr, err)
			}
		}()
	}
	log.Info("serving", "version", version, "public", *f.public, "internal", *f.internal, "probe", *f.probe, "auth", *f.auth)
	select {
	case <-ctx.Done():
	case err := <-errc:
		stop()
		return err
	}
	shut, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, h := range servers {
		h.Shutdown(shut)
	}
	return nil
}

// ownLines tells the listener every line we queue, so a line of ours that
// shows up in the conversation log is never taken for the owner's command.
type ownLines struct {
	announcer.Recorder
	lis *listener.Listener
}

func (o ownLines) Queued(e announcer.Entry) {
	o.lis.Said(e.Text)
	o.Recorder.Queued(e)
}

// runListener reads the conversation log: once a second while our music
// plays or the owner just spoke, every two seconds otherwise (once
// a second around the clock drew 140 errors from Xiaomi in a night).
// The panel switch turns it off; AI lines and schedules carry on regardless.
func runListener(ctx context.Context, log *slog.Logger, lis *listener.Listener, disp *command.Dispatcher, st *store.Store, srv *server.Server, music func() bool) {
	failing := false
	var lastHeard time.Time
	lis.Deliver(func(r xiaomi.Record) {
		seen := time.Now()
		lastHeard = seen
		action := disp.Handle(ctx, r.Query)
		if action != "" {
			// lag = said → read by us; took = us → done (the command's own
			// speaker calls), so a slow voice command can be split.
			log.Info("heard", "action", action,
				"lag_ms", seen.Sub(time.UnixMilli(r.TimeMS)).Milliseconds(), "took_ms", time.Since(seen).Milliseconds())
		}
		st.AddHeard(store.Heard{At: time.UnixMilli(r.TimeMS), Query: r.Query, Action: action})
	})
	for {
		wait := 5 * time.Second
		if srv.Listening() {
			wait = listener.Pace(music(), lastHeard, time.Now())
			if back := lis.Poll(ctx); back > 0 {
				if !failing {
					log.Warn("listener: conversation log unreadable, backing off", "wait", back.String(), "err", lis.Health().LastErr)
				}
				failing, wait = true, back
			} else if failing {
				log.Info("listener: conversation log readable again")
				failing = false
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// runSchedules fires the panel's timetable once a minute boundary passes.
func runSchedules(ctx context.Context, log *slog.Logger, st *store.Store, ann *announcer.Announcer, dev *device, srv *server.Server, owner string) {
	loc := time.Local // TZ in the container
	// What already ran survives a restart (a release at 08:01 must
	// not fire 08:00 again).
	var fired map[int64]time.Time
	json.Unmarshal([]byte(st.Setting("schedule_fired", "{}")), &fired)
	run := schedule.NewRunnerFrom(fired)
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		entries, err := st.Schedules()
		if err != nil {
			log.Warn("schedules", "err", err)
			continue
		}
		due := run.Due(entries, time.Now().In(loc))
		if len(due) > 0 {
			if b, err := json.Marshal(run.Fired()); err == nil {
				st.SetSetting("schedule_fired", string(b))
			}
		}
		for _, e := range due {
			log.Info("schedule fired", "id", e.ID, "action", e.Action)
			if e.Date != "" { // just once: done, switch it off
				off := e
				off.Disabled = true
				st.UpdateSchedule(off)
			}
			switch e.Action {
			case schedule.ActionStop:
				dev.Stop(ctx)
			case schedule.ActionSay:
				if _, err := ann.Say(ctx, announcer.Request{Text: e.Arg, Source: "schedule", Native: srv.Voice() == "native"}); err != nil {
					log.Warn("schedule say", "id", e.ID, "err", err)
				}
			case schedule.ActionPlay:
				err := srv.Play(ctx, e.Arg)
				if errors.Is(err, jukebox.ErrHusicClosed) {
					ann.Say(ctx, announcer.Request{Text: owner + "，到点放歌了，可电脑上的 Husic 没开着，开一下我再放", Source: "schedule"})
				} else if err != nil {
					log.Warn("schedule play", "id", e.ID, "err", err)
				}
			}
		}
	}
}

func housekeeping(ctx context.Context, st *store.Store, vo *voice.Voice) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		st.Prune(time.Now().Add(-30 * 24 * time.Hour))
		vo.Prune(24 * time.Hour)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// device is the speaker as the conductor and the panel see it. It finds the
// speaker lazily (Xiaomi may be unreachable at boot) and caches the status
// read that the panel and Prometheus share.
type device struct {
	acct  *xiaomi.Account
	store *store.Store // "speaker": which speaker on the account
	log   *slog.Logger
	cond  *conductor.Conductor
	jb    *jukebox.Jukebox // set when Husic is wired; nil otherwise
	lib   *music.Client
	pc    *pcmouth.Hub

	mu     sync.Mutex
	sp     *xiaomi.Speaker
	snap   server.Status
	snapAt time.Time
	misses int // status reads failed in a row
}

// errNoLogin / errNoSpeaker: the panel has not finished setting up.
var (
	errNoLogin   = errors.New("还没登录小米：在面板里登录")
	errNoSpeaker = errors.New("还没选音箱：在面板里选一台")
)

func (d *device) speaker(ctx context.Context) (*xiaomi.Speaker, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sp != nil {
		return d.sp, nil
	}
	s := d.acct.Session()
	if s == nil {
		return nil, errNoLogin
	}
	key := d.store.Setting("speaker", "")
	if key == "" {
		// One speaker on the account: use it without asking.
		devs, err := s.Devices(ctx)
		if err != nil {
			return nil, err
		}
		if len(devs) != 1 {
			return nil, errNoSpeaker
		}
		key = devs[0].DeviceID
		d.store.SetSetting("speaker", key)
	}
	sp, err := s.Speaker(ctx, key)
	if err != nil {
		return nil, err
	}
	d.sp = sp
	return sp, nil
}

// forget drops the cached speaker after a login or a new choice.
func (d *device) forget() {
	d.mu.Lock()
	d.sp, d.snapAt = nil, time.Time{}
	d.mu.Unlock()
}

// ---- server.Xiaomi: the panel's 小米 page ----

func (d *device) LoginState() string { return loginNames[d.acct.Health()] }

func miErr(err error) error {
	switch {
	case errors.Is(err, xiaomi.ErrBadPassword):
		return server.ErrXiaomiPassword
	case errors.Is(err, xiaomi.ErrBadCode):
		return server.ErrXiaomiCode
	case errors.Is(err, xiaomi.ErrRiskControl):
		return server.ErrXiaomiLimited
	}
	return err
}

func (d *device) Login(ctx context.Context, user, password string) (bool, error) {
	err := d.acct.Login(ctx, user, password)
	if errors.Is(err, xiaomi.ErrNeedCode) {
		return true, nil
	}
	d.forget()
	return false, miErr(err)
}

func (d *device) Verify(ctx context.Context, code string) error {
	err := d.acct.Verify(ctx, code)
	d.forget()
	return miErr(err)
}

func (d *device) Logout() error {
	d.forget()
	d.store.SetSetting("speaker", "")
	return d.acct.Logout()
}

func (d *device) Speakers(ctx context.Context) ([]server.SpeakerInfo, error) {
	s := d.acct.Session()
	if s == nil {
		return nil, errNoLogin
	}
	devs, err := s.Devices(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]server.SpeakerInfo, 0, len(devs))
	for _, x := range devs {
		name := x.Alias
		if name == "" {
			name = x.Name
		}
		out = append(out, server.SpeakerInfo{ID: x.DeviceID, Name: name, Model: x.Hardware, Online: x.Presence == "online"})
	}
	return out, nil
}

func (d *device) Choose(ctx context.Context, id string) error {
	list, err := d.Speakers(ctx)
	if err != nil {
		return err
	}
	for _, x := range list {
		if x.ID == id {
			d.store.SetSetting("speaker", id)
			d.forget()
			return nil
		}
	}
	return errors.New("账号下没有这台音箱")
}

func (d *device) Chosen() string { return d.store.Setting("speaker", "") }

func (d *device) call(ctx context.Context, f func(*xiaomi.Speaker) error) error {
	sp, err := d.speaker(ctx)
	if err != nil {
		return err
	}
	return f(sp)
}

// traced runs a speaker call that moves the speaker and logs when it went
// out and how long Xiaomi took, so a real-device timeline can be read from
// the container log next to the /a/ fetch lines.
func (d *device) traced(ctx context.Context, what string, f func(*xiaomi.Speaker) error) error {
	t0 := time.Now()
	err := d.call(ctx, f)
	d.log.Info("speaker", "call", what, "ms", time.Since(t0).Milliseconds(), "err", err)
	return err
}

func (d *device) Push(ctx context.Context, url string) error {
	return d.traced(ctx, "push", func(sp *xiaomi.Speaker) error { return sp.PlayMusic(ctx, url) })
}
func (d *device) SpeakNative(ctx context.Context, text string) error {
	return d.call(ctx, func(sp *xiaomi.Speaker) error { return sp.TextToSpeech(ctx, text) })
}
func (d *device) Control(ctx context.Context, op xiaomi.Op) error {
	return d.traced(ctx, string(op), func(sp *xiaomi.Speaker) error { return sp.Control(ctx, op) })
}
func (d *device) SetVolume(ctx context.Context, v int) error {
	return d.call(ctx, func(sp *xiaomi.Speaker) error { return sp.SetVolume(ctx, v) })
}

// PanelVolume is the panel's volume: the whole PC's while the PC is on,
// the speaker's otherwise.
func (d *device) PanelVolume(ctx context.Context, v int) error {
	if d.pc != nil && d.pc.Online() {
		return d.pc.SetVolume(ctx, v)
	}
	if err := d.SetVolume(ctx, v); err != nil {
		return err
	}
	// The status read is cached for 10 s; without this the panel slider
	// jumped back to the old volume after a drag.
	d.mu.Lock()
	d.snap.Volume = v
	d.mu.Unlock()
	return nil
}
func (d *device) Status(ctx context.Context) (xiaomi.Status, error) {
	var st xiaomi.Status
	err := d.call(ctx, func(sp *xiaomi.Speaker) (err error) { st, err = sp.Status(ctx); return })
	return st, err
}
func (d *device) Conversations(ctx context.Context, limit int, beforeMS int64) ([]xiaomi.Record, error) {
	var recs []xiaomi.Record
	err := d.call(ctx, func(sp *xiaomi.Speaker) (err error) { recs, err = sp.Conversations(ctx, limit, beforeMS); return })
	return recs, err
}
func (d *device) BluetoothPlaying(ctx context.Context) (bool, error) {
	var bt xiaomi.BluetoothState
	err := d.call(ctx, func(sp *xiaomi.Speaker) (err error) { bt, err = sp.Bluetooth(ctx); return })
	return bt.Playing, err
}
func (d *device) SetLoop(ctx context.Context, t int) error {
	return d.traced(ctx, fmt.Sprintf("loop-%d", t), func(sp *xiaomi.Speaker) error { return sp.SetLoop(ctx, t) })
}

// Stop ends the music wherever it plays: the PC's Husic while the PC is on
// (without touching the speaker unless our own session runs), our speaker
// session otherwise.
func (d *device) Stop(ctx context.Context) error {
	if d.jb != nil {
		return d.jb.Do(ctx, "stop")
	}
	d.cond.Command(conductor.Command{Kind: conductor.CmdStop})
	return nil
}

// pcPlayer is the owner's PC as jukebox.PC: on the speaker's Bluetooth,
// with Husic open.
type pcPlayer struct {
	dev *device
	lib *music.Client
}

// Connected: music goes to the PC's Husic when the PC is on (its mouth is
// connected) or is on the speaker's Bluetooth.
func (p pcPlayer) Connected(ctx context.Context) bool {
	if p.dev.pc != nil && p.dev.pc.Online() {
		return true
	}
	var bt xiaomi.BluetoothState
	err := p.dev.call(ctx, func(sp *xiaomi.Speaker) (err error) { bt, err = sp.Bluetooth(ctx); return })
	return err == nil && bt.Connected
}

// Playing: the PC's open Husic is playing (it reports its state).
func (p pcPlayer) Playing(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	np, err := p.lib.NowPlaying(ctx)
	return err == nil && np.Open && np.Playing
}

func (p pcPlayer) Command(ctx context.Context, action, what string) error {
	err := p.lib.Command(ctx, action, what)
	if errors.Is(err, music.ErrNoPlayer) {
		return jukebox.ErrHusicClosed
	}
	return err
}

var loginNames = map[xiaomi.Health]string{
	xiaomi.HealthNoAccount: "none", xiaomi.HealthOK: "ok", xiaomi.HealthRenewing: "renewing",
	xiaomi.HealthLoginRequired: "login-required", xiaomi.HealthRiskControl: "risk-control",
}

// Snapshot reads the speaker at most every 10 s.
func (d *device) Snapshot(ctx context.Context) server.Status {
	d.mu.Lock()
	if time.Since(d.snapAt) < 10*time.Second {
		s := d.snap
		d.mu.Unlock()
		d.fill(ctx, &s)
		return s
	}
	d.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	snap := server.Status{}
	st, err := d.Status(ctx)
	if err != nil {
		snap.Error = err.Error()
		// One slow or failed read is not "offline" (the panel said so
		// while the speaker played); only two in a row are.
		d.mu.Lock()
		d.misses++
		if d.misses < 2 && d.snap.Online {
			snap.Online, snap.Volume, snap.Error = true, d.snap.Volume, ""
		}
		d.mu.Unlock()
	} else {
		snap.Online, snap.Volume = true, st.Volume
		d.mu.Lock()
		d.misses = 0
		d.mu.Unlock()
	}
	snap.Login = d.LoginState()
	d.mu.Lock()
	d.snap, d.snapAt = snap, time.Now()
	d.mu.Unlock()
	d.fill(ctx, &snap)
	return snap
}

// fill adds who plays the music — our own player or the PC's Husic — and,
// while the PC is on, its volume (the panel's slider is the PC's then).
func (d *device) fill(ctx context.Context, s *server.Status) {
	s.State, s.Playing = string(d.cond.State()), d.cond.Playing()
	s.PC = d.pc != nil && d.pc.Online()
	s.Where = ""
	if s.Playing != "" {
		s.Where, s.Paused = "speaker", s.State == string(conductor.StateUserPaused)
	}
	if s.PC {
		if st, ok := d.pc.State(); ok {
			s.Volume = st.Volume
		}
	}
	if d.lib != nil {
		hctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		np, err := d.lib.NowPlaying(hctx)
		cancel()
		s.Husic = err == nil && np.Open
		if s.PC && s.Where == "" && s.Husic && np.Title != "" {
			s.Where, s.Playing, s.Artist, s.Paused = "pc", np.Title, np.Artist, !np.Playing
		}
	}
}

// fetchReminders reads today's reminders for the panel from u: GET answers
// [{"at": RFC 3339, "text": "…"}], with the say key as the bearer (the same
// key that source uses to make the speaker talk).
func fetchReminders(u, key string) func(context.Context) ([]server.Reminder, error) {
	hc := &http.Client{Timeout: 3 * time.Second}
	return func(ctx context.Context) ([]server.Reminder, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := hc.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("reminders: HTTP %d", resp.StatusCode)
		}
		var out []server.Reminder
		return out, json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 64<<10)).Decode(&out)
	}
}
