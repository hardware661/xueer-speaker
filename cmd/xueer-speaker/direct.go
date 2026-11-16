package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hardware661/xueer-speaker/internal/control"
	"github.com/hardware661/xueer-speaker/internal/tools"
	"github.com/hardware661/xueer-speaker/internal/voice"
	"github.com/hardware661/xueer-speaker/internal/xiaomi"
)

// The serverless way: this program talks to Xiaomi straight from the
// user's machine, and Xiao Ai speaks in its own voice. Nothing has to run
// in between, the speaker never fetches anything, so no LAN and no Docker.
// State (login, chosen speaker, do-not-disturb) lives in one directory.

// homeDir is where the serverless state lives: XS_HOME, or the user's
// config directory, or /data inside the container.
func homeDir() string {
	if d := os.Getenv("XS_HOME"); d != "" {
		return d
	}
	if d := os.Getenv("XS_DATA"); d != "" {
		return d
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "xueer-speaker")
	}
	return "."
}

// endpoints are the Xiaomi hosts; tests point them at a fake.
var endpoints = xiaomi.Production

// direct is the serverless backend for the CLI and its stdio MCP.
type direct struct {
	dir  string
	acct *xiaomi.Account
	out  io.Writer

	mu sync.Mutex
	sp *xiaomi.Speaker
	// limits, same as the service: the same line within 10 s once, at most
	// 6 a minute; kept in memory (one MCP process per AI client).
	sent []sentLine
}

type sentLine struct {
	at   time.Time
	text string
}

// directState is state.json: the chosen speaker and do-not-disturb.
type directState struct {
	Speaker  string `json:"speaker,omitempty"`  // MiNA deviceID
	Name     string `json:"name,omitempty"`     // for status
	DNDUntil int64  `json:"dndUntil,omitempty"` // unix seconds; 0 = off
	Held     int    `json:"held,omitempty"`     // lines kept quiet
}

func openDirect(out io.Writer) (*direct, error) {
	dir := homeDir()
	xf := envOr("XS_XIAOMI", filepath.Join(dir, "xiaomi.json"))
	acct, err := xiaomi.NewAccount(endpoints, xf)
	if err != nil {
		return nil, err
	}
	return &direct{dir: dir, acct: acct, out: out}, nil
}

func (d *direct) statePath() string { return filepath.Join(d.dir, "state.json") }

func (d *direct) state() directState {
	var s directState
	if b, err := os.ReadFile(d.statePath()); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func (d *direct) save(s directState) error {
	b, _ := json.MarshalIndent(s, "", "  ")
	if err := os.MkdirAll(d.dir, 0o700); err != nil {
		return err
	}
	tmp := d.statePath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, d.statePath())
}

var errNotLoggedIn = errors.New("还没登录小米：先运行 xueer-speaker login")

func (d *direct) speaker(ctx context.Context) (*xiaomi.Speaker, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sp != nil {
		return d.sp, nil
	}
	s := d.acct.Session()
	if s == nil {
		return nil, errNotLoggedIn
	}
	key := envOr("XS_SPEAKER", d.state().Speaker)
	if key == "" {
		devs, err := s.Devices(ctx)
		if err != nil {
			return nil, err
		}
		if len(devs) != 1 {
			return nil, errors.New("账号下有好几台音箱：运行 xueer-speaker login 选一台")
		}
		key = devs[0].DeviceID
	}
	sp, err := s.Speaker(ctx, key)
	if err != nil {
		return nil, err
	}
	d.sp = sp
	return sp, nil
}

// quiet reports whether do-not-disturb is on, and switches it off (once,
// with the count of held lines) when its time has passed.
func (d *direct) quiet(ctx context.Context) bool {
	s := d.state()
	if s.DNDUntil == 0 {
		return false
	}
	if time.Now().Before(time.Unix(s.DNDUntil, 0)) {
		return true
	}
	d.DND(ctx, false, "")
	return false
}

func (d *direct) admit(text string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	kept := d.sent[:0]
	for _, s := range d.sent {
		if now.Sub(s.at) < time.Minute {
			kept = append(kept, s)
		}
	}
	d.sent = kept
	for _, s := range d.sent {
		if s.text == text && now.Sub(s.at) < 10*time.Second {
			return errors.New("刚说过一模一样的话，这句没重复念")
		}
	}
	if len(d.sent) >= 6 {
		return errors.New("一分钟里话太多了，这句没念")
	}
	d.sent = append(d.sent, sentLine{at: now, text: text})
	return nil
}

// sayNative speaks text in Xiao Ai's own voice: MiNA first, MIoT 7-3 if
// that fails (the same order the service falls back in).
func sayNative(ctx context.Context, sp *xiaomi.Speaker, text string) error {
	if err := sp.TextToSpeech(ctx, text); err == nil {
		return nil
	} else if err2 := sp.Action(ctx, 7, 3, []any{text}); err2 != nil {
		return fmt.Errorf("小爱没念出来：%v；%v", err, err2)
	}
	return nil
}

// ---- tools.Backend ----

func (d *direct) Say(ctx context.Context, _ string, text string) (string, error) {
	text = voice.Clean(text)
	if text == "" {
		return "", voice.ErrEmpty
	}
	if len([]rune(text)) > voice.MaxRunes {
		return "", voice.ErrTooLong
	}
	if err := d.admit(text); err != nil {
		return "", err
	}
	if d.quiet(ctx) {
		s := d.state()
		s.Held++
		d.save(s)
		return "quiet", nil
	}
	sp, err := d.speaker(ctx)
	if err != nil {
		return "", err
	}
	return "ok", sayNative(ctx, sp, text)
}

// directStatus is what status prints and the speaker tool returns.
type directStatus struct {
	Login    string `json:"login"`
	Speaker  string `json:"speaker,omitempty"`
	Online   bool   `json:"online"`
	Volume   int    `json:"volume,omitempty"`
	Playing  string `json:"playing,omitempty"`
	DND      bool   `json:"dnd"`
	DNDUntil string `json:"dnd_until,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (d *direct) Status(ctx context.Context) any { return d.status(ctx) }

func (d *direct) status(ctx context.Context) directStatus {
	st := directStatus{Login: loginNames[d.acct.Health()]}
	if s := d.state(); s.DNDUntil != 0 && time.Now().Before(time.Unix(s.DNDUntil, 0)) {
		st.DND, st.DNDUntil = true, time.Unix(s.DNDUntil, 0).Format("15:04")
	}
	sp, err := d.speaker(ctx)
	if err != nil {
		st.Error = err.Error()
		st.Login = loginNames[d.acct.Health()]
		return st
	}
	dev := sp.Device()
	st.Speaker = strings.TrimSpace(dev.Alias + " " + dev.Hardware)
	ps, err := sp.Status(ctx)
	if err != nil {
		st.Error = err.Error()
	} else {
		st.Online, st.Volume, st.Playing = true, ps.Volume, string(ps.State)
	}
	st.Login = loginNames[d.acct.Health()]
	return st
}

func (d *direct) Volume(ctx context.Context) (int, error) {
	sp, err := d.speaker(ctx)
	if err != nil {
		return 0, err
	}
	st, err := sp.Status(ctx)
	return st.Volume, err
}

func (d *direct) SetVolume(ctx context.Context, v int) error {
	sp, err := d.speaker(ctx)
	if err != nil {
		return err
	}
	return sp.SetVolume(ctx, v)
}

func (d *direct) Do(ctx context.Context, text string, silent bool) error {
	if err := control.CheckDirective(text); err != nil {
		return err
	}
	sp, err := d.speaker(ctx)
	if err != nil {
		return err
	}
	return sp.Do(ctx, text, silent)
}

func (d *direct) DND(ctx context.Context, on bool, until string) (tools.DNDState, error) {
	s := d.state()
	sp, spErr := d.speaker(ctx)
	if on {
		t, err := control.ParseUntil(until, time.Now())
		if err != nil {
			return tools.DNDState{}, err
		}
		s.DNDUntil = t.Unix()
		if err := d.save(s); err != nil {
			return tools.DNDState{}, err
		}
		if spErr == nil {
			sp.SetDoNotDisturb(ctx, true) // best effort; ours is what keeps lines quiet
		}
		return tools.DNDState{On: true, Until: t.Format("15:04")}, nil
	}
	held := s.Held
	wasOn := s.DNDUntil != 0
	s.DNDUntil, s.Held = 0, 0
	if err := d.save(s); err != nil {
		return tools.DNDState{}, err
	}
	if spErr == nil {
		sp.SetDoNotDisturb(ctx, false)
		if wasOn && held > 0 {
			sayNative(ctx, sp, fmt.Sprintf("勿扰的时候攒了 %d 句，没念出来。", held))
		}
	}
	return tools.DNDState{Held: held}, spErr
}

// ---- commands ----

func directCmd(cmd string, args []string) error {
	d, err := openDirect(os.Stdout)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	switch cmd {
	case "login":
		cancel()
		return d.login(os.Stdin)
	case "logout":
		if err := d.acct.Logout(); err != nil {
			return err
		}
		os.Remove(d.statePath())
		fmt.Println("已退出小米账号")
		return nil
	case "say":
		if len(args) == 0 {
			return errors.New("要说什么：xueer-speaker say 做好了")
		}
		id, err := d.Say(ctx, "cli", strings.Join(args, " "))
		if err != nil {
			return err
		}
		if id == "quiet" {
			fmt.Println("勿扰中：记下了，没念")
		}
		return nil
	case "volume":
		if len(args) == 0 {
			v, err := d.Volume(ctx)
			if err != nil {
				return err
			}
			fmt.Println(v)
			return nil
		}
		v, err := strconv.Atoi(args[0])
		if err != nil || v < 0 || v > 100 {
			return errors.New("音量要在 0～100")
		}
		return d.SetVolume(ctx, v)
	case "do":
		silent := len(args) > 0 && args[0] == "--silent"
		if silent {
			args = args[1:]
		}
		return d.Do(ctx, strings.Join(args, " "), silent)
	case "dnd":
		return d.dndCmd(ctx, args)
	case "status":
		b, _ := json.MarshalIndent(d.status(ctx), "", "  ")
		fmt.Println(string(b))
		return nil
	case "test":
		return d.selfTest(ctx)
	case "mcp":
		cancel()
		p := tools.Persona{Name: envOr("XS_PERSONA", ""), Owner: envOr("XS_OWNER", "")}
		srv := mcp.NewServer(&mcp.Implementation{Name: "xueer-speaker", Version: version}, &mcp.ServerOptions{Instructions: p.Instructions()})
		tools.Register(srv, d, p, func(*mcp.CallToolRequest) string { return "" })
		sctx, stop := signalContext()
		defer stop()
		return srv.Run(sctx, &mcp.StdioTransport{})
	}
	return fmt.Errorf("不认识的命令 %q，看 xueer-speaker -h", cmd)
}

func (d *direct) dndCmd(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "status" {
		st := d.status(ctx)
		if st.DND {
			fmt.Println("勿扰中，到", st.DNDUntil)
		} else {
			fmt.Println("没开勿扰")
		}
		return nil
	}
	switch args[0] {
	case "on":
		until := ""
		if len(args) > 2 && args[1] == "--until" {
			until = args[2]
		}
		r, err := d.DND(ctx, true, until)
		if err != nil {
			return err
		}
		fmt.Println("勿扰开了，到", r.Until)
		return nil
	case "off":
		r, err := d.DND(ctx, false, "")
		if r.Held > 0 {
			fmt.Printf("勿扰关了，期间攒了 %d 句\n", r.Held)
		} else if err == nil {
			fmt.Println("勿扰关了")
		}
		return err
	}
	return errors.New("dnd on [--until 08:00] | off | status")
}

// login asks for the account, password and (when Xiaomi wants one) the
// code, in the terminal, then lets the user pick a speaker.
func (d *direct) login(in io.Reader) error {
	r := bufio.NewReader(in)
	ask := func(q string) string {
		fmt.Print(q)
		s, _ := r.ReadString('\n')
		return strings.TrimSpace(s)
	}
	user := envOr("XS_XIAOMI_USER", "")
	if user == "" {
		user = ask("小米账号（手机号 / 邮箱 / 小米 ID）：")
	}
	pw := os.Getenv("XS_XIAOMI_PASSWORD")
	if pw == "" {
		pw = readPassword("密码（不显示）：", r)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err := d.acct.Login(ctx, user, pw)
	if errors.Is(err, xiaomi.ErrNeedCode) {
		code := ask("小米发了验证码到绑定的手机或邮箱，填进来：")
		cctx, ccancel := context.WithTimeout(context.Background(), time.Minute)
		defer ccancel()
		err = d.acct.Verify(cctx, code)
	}
	switch {
	case errors.Is(err, xiaomi.ErrBadPassword):
		return errors.New("账号或密码不对")
	case errors.Is(err, xiaomi.ErrBadCode):
		return errors.New("验证码不对，重新运行 login")
	case errors.Is(err, xiaomi.ErrRiskControl):
		return errors.New("小米暂时限流，过两小时再试")
	case err != nil:
		return err
	}
	devs, err := d.acct.Session().Devices(ctx)
	if err != nil {
		return err
	}
	if len(devs) == 0 {
		return errors.New("登录成功，但账号下没有音箱")
	}
	pick := 0
	if len(devs) > 1 {
		for i, x := range devs {
			fmt.Printf("  %d. %s（%s）%s\n", i+1, firstNonEmpty(x.Alias, x.Name), x.Hardware, map[bool]string{true: "", false: " 不在线"}[x.Presence == "online"])
		}
		n, _ := strconv.Atoi(ask("用哪一台？填序号："))
		if n < 1 || n > len(devs) {
			return errors.New("序号不对，重新运行 login")
		}
		pick = n - 1
	}
	s := d.state()
	s.Speaker, s.Name = devs[pick].DeviceID, firstNonEmpty(devs[pick].Alias, devs[pick].Name)
	if err := d.save(s); err != nil {
		return err
	}
	fmt.Printf("好了：用「%s」。试一下：xueer-speaker test\n", s.Name)
	return nil
}

// selfTest walks the chain one step at a time and says where it stops.
func (d *direct) selfTest(ctx context.Context) error {
	step := func(name string, err error) error {
		if err != nil {
			fmt.Printf("✗ %s：%v\n", name, err)
			return err
		}
		fmt.Printf("✓ %s\n", name)
		return nil
	}
	if d.acct.Session() == nil {
		return step("小米登录", errNotLoggedIn)
	}
	sp, err := d.speaker(ctx)
	if step("小米登录、找到音箱", err) != nil {
		return err
	}
	st, err := sp.Status(ctx)
	if step("音箱在线", err) != nil {
		return err
	}
	fmt.Printf("  音量 %d\n", st.Volume)
	if st.Volume < 15 {
		fmt.Println("  音量很小，听不见的话先 xueer-speaker volume 40")
	}
	if d.quiet(ctx) {
		fmt.Println("  现在开着勿扰，测试照样会念")
	}
	if step("小爱念一句", sayNative(ctx, sp, "测试一下，能听到就说明好了。")) != nil {
		return err
	}
	fmt.Println("听到了就是好了；没听到看看音箱是不是在用蓝牙放别的东西。")
	return nil
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}
