// Command xueer-speaker turns the Xiao Ai speaker into Xueer's mouth.
//
// "serve" runs the service. The other subcommands are diagnostics that talk
// to Xiaomi directly with the login the panel saved (<data>/xiaomi.json);
// "client" talks to a running serve through its internal port instead, so it
// never competes with it for the speaker or the login.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hardware661/xueer-speaker/internal/xiaomi"
)

const usage = `用法：xueer-speaker <命令>

不用服务器（小爱原声，登录信息存在 ~/.config/xueer-speaker，XS_HOME 可改）：

  login                       登录小米（账号、密码、短信验证码），选音箱
  say <文字>                  小爱念一句（勿扰时只记下）
  volume [0-100]              不带数字读音量，带就设
  do [--silent] <一句话>      让小爱照这句话去做，如「明天八点叫我」
  dnd on [--until 08:00] | off | status   勿扰（不带 --until 最多 12 小时）
  status                      登录、音箱、音量、勿扰
  test                        自检：一步一步试，念一句
  mcp                         给 Claude Code / Codex 用的本机 MCP（stdio）
  logout                      退出小米账号

服务（晓晓的声音、面板、定时，用 Docker 跑）：

  serve …                     跑服务（参数看 serve -h，都能用 XS_ 开头的环境变量设）
  client <say|status|history|volume|stop> …   经内网口调运行中的 serve（XS_ADMIN_URL、XS_CLI_KEY_FILE）
  mcp-local -url … -key-file … 把服务的 MCP 转成本机 stdio
  check                       容器健康检查：GET 127.0.0.1:8102/readyz
  version                     版本

排障（直连小米；可加 -token 登录文件、-did 音箱；服务在跑时少用，免得被小米限流）：
`

// version is set at build time (-X main.version=<tag>).
var version = "dev"

func main() {
	tokenFile := flag.String("token", envOr("XS_XIAOMI", filepath.Join(homeDir(), "xiaomi.json")), "小米登录文件（login 或面板登录后生成）")
	did := flag.String("did", envOr("XS_SPEAKER", ""), "音箱（名字、did 或 MAC；空 = 账号下第一台）")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	var err error
	switch flag.Arg(0) {
	case "serve":
		err = serve(flag.Args()[1:])
	case "client":
		err = client(flag.Args()[1:])
	case "mcp-local":
		err = mcpLocal(flag.Args()[1:])
	case "check":
		err = check()
	case "version":
		fmt.Println(version)
	case "login", "logout", "say", "do", "dnd", "test", "mcp":
		err = directCmd(flag.Arg(0), flag.Args()[1:])
	case "status", "volume":
		// Diagnostics used to have these too; with -token / -did they keep
		// their raw output, otherwise they are the serverless commands.
		if isFlagSet("token") || isFlagSet("did") {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			err = run(ctx, *tokenFile, *did, flag.Args())
			cancel()
		} else {
			err = directCmd(flag.Arg(0), flag.Args()[1:])
		}
	default:
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		err = run(ctx, *tokenFile, *did, flag.Args())
		cancel()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(1)
	}
}

func isFlagSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func run(ctx context.Context, tokenFile, did string, args []string) error {
	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		return err
	}
	var creds xiaomi.Credentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return fmt.Errorf("%s: %w", tokenFile, err)
	}
	s := xiaomi.NewSession(xiaomi.Production, creds)
	cmd, rest := args[0], args[1:]
	if cmd == "devices" {
		devs, err := s.Devices(ctx)
		if err != nil {
			return err
		}
		for _, d := range devs {
			fmt.Printf("%s\t%s\t%s\t%s\tmiot=%s\n", d.Name, d.Hardware, d.RomVersion, d.Presence, d.MiotDID)
		}
		return nil
	}
	if did == "" {
		devs, err := s.Devices(ctx)
		if err != nil {
			return err
		}
		if len(devs) == 0 {
			return errors.New("账号下没有音箱")
		}
		did = devs[0].DeviceID
	}
	sp, err := s.Speaker(ctx, did)
	if err != nil {
		return err
	}
	need := func(n int) error {
		if len(rest) < n {
			return errors.New("参数不够，看 -h")
		}
		return nil
	}
	switch cmd {
	case "status":
		st, err := sp.Status(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("state=%s volume=%d position=%dms duration=%dms\nraw=%s\n", st.State, st.Volume, st.PositionMS, st.DurationMS, st.Raw)
		return nil
	case "say-native":
		if err := need(1); err != nil {
			return err
		}
		return sp.Action(ctx, 7, 3, []any{strings.Join(rest, " ")})
	case "say-mina":
		if err := need(1); err != nil {
			return err
		}
		return sp.TextToSpeech(ctx, strings.Join(rest, " "))
	case "play":
		if err := need(1); err != nil {
			return err
		}
		return sp.PlayMusic(ctx, rest[0])
	case "play-url":
		if err := need(1); err != nil {
			return err
		}
		return sp.PlayURL(ctx, rest[0])
	case "loop":
		if err := need(1); err != nil {
			return err
		}
		v, err := strconv.Atoi(rest[0])
		if err != nil {
			return err
		}
		return sp.SetLoop(ctx, v)
	case "seq":
		// Run several commands on one session, e.g. seq stop 0 status (numbers = sleep seconds).
		for _, c := range rest {
			if n, err := strconv.ParseFloat(c, 64); err == nil {
				time.Sleep(time.Duration(n * float64(time.Second)))
				continue
			}
			t0 := time.Now()
			var err error
			switch c {
			case "pause":
				err = sp.Control(ctx, xiaomi.OpPause)
			case "resume":
				err = sp.Control(ctx, xiaomi.OpPlay)
			case "stop":
				err = sp.Control(ctx, xiaomi.OpStop)
			case "push":
				err = sp.PlayMusic(ctx, os.Getenv("XS_URL"))
			case "status":
				var st xiaomi.Status
				st, err = sp.Status(ctx)
				fmt.Printf("  %s\n", st.Raw)
			default:
				err = fmt.Errorf("seq 不认识 %q", c)
			}
			fmt.Printf("%s %s %v (%dms)\n", time.Now().Format("15:04:05.000"), c, err, time.Since(t0).Milliseconds())
		}
		return nil
	case "ubus":
		// 只读排障：ubus <path> <method> [json]
		if err := need(2); err != nil {
			return err
		}
		msg := "{}"
		if len(rest) > 2 {
			msg = rest[2]
		}
		info, err := sp.Raw(ctx, rest[0], rest[1], json.RawMessage(msg))
		fmt.Println(info)
		return err
	case "tone":
		if err := need(1); err != nil {
			return err
		}
		return sp.PlayTone(ctx, rest[0])
	case "qplay":
		if err := need(1); err != nil {
			return err
		}
		return sp.QPlay(ctx, rest[0])
	case "pause":
		return sp.Control(ctx, xiaomi.OpPause)
	case "resume":
		return sp.Control(ctx, xiaomi.OpPlay)
	case "stop":
		return sp.Control(ctx, xiaomi.OpStop)
	case "volume":
		if err := need(1); err != nil {
			return err
		}
		v, err := strconv.Atoi(rest[0])
		if err != nil {
			return err
		}
		return sp.SetVolume(ctx, v)
	case "action":
		if err := need(2); err != nil {
			return err
		}
		siid, aiid, err := twoInts(rest[0], rest[1])
		if err != nil {
			return err
		}
		var in []any
		if len(rest) > 2 {
			if err := json.Unmarshal([]byte(rest[2]), &in); err != nil {
				return fmt.Errorf("参数要是 JSON 数组：%w", err)
			}
		}
		return sp.Action(ctx, siid, aiid, in)
	case "getprop":
		if err := need(2); err != nil {
			return err
		}
		siid, piid, err := twoInts(rest[0], rest[1])
		if err != nil {
			return err
		}
		v, err := sp.Prop(ctx, siid, piid)
		if err != nil {
			return err
		}
		fmt.Println(v)
		return nil
	case "conversations":
		fs := flag.NewFlagSet("conversations", flag.ContinueOnError)
		n := fs.Int("n", 5, "条数")
		if err := fs.Parse(rest); err != nil {
			return err
		}
		recs, err := sp.Conversations(ctx, *n, 0)
		if err != nil {
			return err
		}
		for _, r := range recs {
			fmt.Printf("%s\t%s\t%s\tanswers=%s\n", time.UnixMilli(r.TimeMS).Format("01-02 15:04:05"), r.RequestID, r.Query, r.Answers)
		}
		return nil
	}
	if cmd == "watch-bt" {
		return watchBT(ctx, sp, 40*time.Second)
	}
	if cmd == "watch-interrupt" {
		return watchInterrupt(ctx, sp, rest)
	}
	return fmt.Errorf("不认识的命令 %q，看 -h", cmd)
}

func twoInts(a, b string) (int, int, error) {
	x, err := strconv.Atoi(a)
	if err != nil {
		return 0, 0, err
	}
	y, err := strconv.Atoi(b)
	return x, y, err
}
