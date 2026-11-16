package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hardware661/xueer-speaker/internal/audio"
	"github.com/hardware661/xueer-speaker/internal/server"
	"github.com/hardware661/xueer-speaker/internal/voice"

	"github.com/hardware661/xueer-speaker/internal/announcer"
	"github.com/hardware661/xueer-speaker/internal/control"
	"github.com/hardware661/xueer-speaker/internal/store"
	"github.com/hardware661/xueer-speaker/internal/tools"
	"github.com/hardware661/xueer-speaker/internal/xiaomi"
)

// controls is the speaker tool's and the panel's 勿扰 for the service: the
// speaker's own volume, do-not-disturb (ours, which keeps lines, plus Xiao
// Ai's own switch) and "do".
type controls struct {
	dev   *device
	quiet *control.Quiet
	ann   *announcer.Announcer
	store *store.Store
	log   *slog.Logger
	owner string
}

func (c *controls) Volume(ctx context.Context) (int, error) {
	st, err := c.dev.Status(ctx)
	return st.Volume, err
}

// SetVolume sets the speaker's volume (the panel's slider is the one that
// follows the PC while it is on).
func (c *controls) SetVolume(ctx context.Context, v int) error { return c.dev.SetVolume(ctx, v) }

func (c *controls) Do(ctx context.Context, text string, silent bool) error {
	if err := control.CheckDirective(text); err != nil {
		return err
	}
	return c.dev.call(ctx, func(sp *xiaomi.Speaker) error { return sp.Do(ctx, text, silent) })
}

func (c *controls) DNDState() tools.DNDState {
	if u := c.quiet.Until(); !u.IsZero() {
		return tools.DNDState{On: true, Until: u.Format("15:04")}
	}
	return tools.DNDState{}
}

// DND switches both layers. Xiao Ai's own switch is best effort: ours is the
// one that keeps our lines quiet.
func (c *controls) DND(ctx context.Context, on bool, until string) (tools.DNDState, error) {
	if on {
		t, err := control.ParseUntil(until, time.Now())
		if err != nil {
			return tools.DNDState{}, err
		}
		c.quiet.Start(t)
		c.store.SetSetting("dnd_until", fmt.Sprint(t.Unix()))
		c.xiaoAi(ctx, true)
		return tools.DNDState{On: true, Until: t.Format("15:04")}, nil
	}
	held := c.quiet.Stop()
	c.store.SetSetting("dnd_until", "")
	c.xiaoAi(ctx, false)
	c.report(ctx, held)
	return tools.DNDState{Held: held}, nil
}

func (c *controls) xiaoAi(ctx context.Context, on bool) {
	err := c.dev.call(ctx, func(sp *xiaomi.Speaker) error { return sp.SetDoNotDisturb(ctx, on) })
	if err != nil {
		c.log.Warn("xiao ai do-not-disturb", "on", on, "err", err)
	}
}

// report says once, aloud, how many lines were kept while it was quiet.
func (c *controls) report(ctx context.Context, held int) {
	if held == 0 {
		return
	}
	text := fmt.Sprintf("%s，勿扰的时候攒了 %d 句，面板上都能看到。", c.owner, held)
	if _, err := c.ann.Say(ctx, announcer.Request{Text: text, Source: "dnd", Loud: true}); err != nil {
		c.log.Warn("dnd report", "err", err)
	}
}

// restore brings back a do-not-disturb that was on before a restart.
func (c *controls) restore() {
	var unix int64
	if _, err := fmt.Sscan(c.store.Setting("dnd_until", ""), &unix); err == nil && time.Unix(unix, 0).After(time.Now()) {
		c.quiet.Start(time.Unix(unix, 0))
	}
}

// watch switches it off by itself when its time comes.
func (c *controls) watch(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if held, ended := c.quiet.Expired(); ended {
			c.store.SetSetting("dnd_until", "")
			c.xiaoAi(ctx, false)
			c.report(ctx, held)
			c.log.Info("do-not-disturb ended", "held", held)
		}
	}
}

// selfTest is the panel's 测试一下: each step on its own, so the owner sees
// where it stops. The speaker says two short lines (native, then Xiaoxiao).
func selfTest(dev *device, vo *voice.Voice, host *audio.Host) func(context.Context) []server.TestStep {
	return func(ctx context.Context) []server.TestStep {
		var steps []server.TestStep
		add := func(name string, err error, okDetail string) bool {
			s := server.TestStep{Name: name, OK: err == nil, Detail: okDetail}
			if err != nil {
				s.Detail = err.Error()
			}
			steps = append(steps, s)
			return err == nil
		}
		if !add("小米登录", loginErr(dev), "") {
			return steps
		}
		st, err := dev.Status(ctx)
		if !add("找到音箱、在线", err, fmt.Sprintf("音量 %d", st.Volume)) {
			return steps
		}
		if !add("小爱原声", dev.SpeakNative(ctx, "测试一下，这是小爱原声。"), "") {
			return steps
		}
		clip, err := vo.Render(ctx, "这是晓晓的声音。")
		if !add("生成晓晓的声音", err, "") {
			return steps
		}
		link := host.Speech(clip.Path)
		if !add("推给音箱", dev.Push(ctx, link), "") {
			return steps
		}
		deadline := time.Now().Add(12 * time.Second)
		for !host.Fetched(link) && time.Now().Before(deadline) {
			time.Sleep(300 * time.Millisecond)
		}
		if host.Fetched(link) {
			add("音箱来取语音", nil, "")
		} else {
			add("音箱来取语音", fmt.Errorf("音箱 12 秒内没来取：看「接入」页最下面的地址，音箱访问不到它（不能是 localhost，要和音箱在同一个局域网）"), "")
		}
		return steps
	}
}

func loginErr(dev *device) error {
	switch dev.LoginState() {
	case "ok", "renewing":
		return nil
	case "none":
		return errors.New("还没登录小米")
	case "risk-control":
		return errors.New("小米暂时限流，过两小时再试")
	}
	return errors.New("登录失效了，重新登录")
}
