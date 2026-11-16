package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

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
