package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hardware661/xueer-speaker/internal/announcer"
	"github.com/hardware661/xueer-speaker/internal/conductor"
	"github.com/hardware661/xueer-speaker/internal/control"
	"github.com/hardware661/xueer-speaker/internal/store"
	"github.com/hardware661/xueer-speaker/internal/voice"
	"github.com/hardware661/xueer-speaker/internal/xiaomi"
)

type nativeOnly struct{}

func (nativeOnly) Render(context.Context, string) (voice.Clip, error) {
	return voice.Clip{}, io.ErrUnexpectedEOF // Edge down: the line goes native
}

type spoken struct{ lines []string }

func (s *spoken) Speak(sp conductor.Speech) <-chan conductor.Outcome {
	s.lines = append(s.lines, sp.Text)
	ch := make(chan conductor.Outcome, 1)
	ch <- conductor.Outcome{Voice: conductor.VoiceNative}
	return ch
}

type nopPub struct{}

func (nopPub) Speech(string) string { return "" }

func newControls(t *testing.T, dir string) (*controls, *spoken) {
	t.Helper()
	st, err := store.Open(filepath.Join(dir, "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	acct, _ := xiaomi.NewAccount(xiaomi.Production, filepath.Join(dir, "none.json")) // no login: Xiao Ai's switch just logs
	sp := &spoken{}
	ann := announcer.New(nativeOnly{}, nopPub{}, sp, nil)
	ann.Quiet = &control.Quiet{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := &controls{dev: &device{acct: acct, store: st, log: log}, quiet: ann.Quiet, ann: ann, store: st, log: log, owner: "主人"}
	return c, sp
}

func TestDNDKeepsLinesThenReportsOnce(t *testing.T) {
	c, sp := newControls(t, t.TempDir())
	ctx := context.Background()
	d, err := c.DND(ctx, true, "2h")
	if err != nil || !d.On || d.Until == "" {
		t.Fatalf("on = %+v %v", d, err)
	}
	for _, line := range []string{"主人，做完了。", "主人，又做完一个。"} {
		if _, err := c.ann.Say(ctx, announcer.Request{Text: line, Source: "claude"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(sp.lines) != 0 {
		t.Fatalf("spoken while quiet: %v", sp.lines)
	}
	if !c.DNDState().On {
		t.Fatal("state not on")
	}
	d, err = c.DND(ctx, false, "")
	if err != nil || d.Held != 2 {
		t.Fatalf("off = %+v %v", d, err)
	}
	if len(sp.lines) != 1 || !strings.Contains(sp.lines[0], "攒了 2 句") {
		t.Fatalf("report = %v", sp.lines)
	}
	c.DND(ctx, false, "")
	if len(sp.lines) != 1 {
		t.Fatal("reported twice")
	}
}

func TestDNDSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	c, _ := newControls(t, dir)
	c.DND(context.Background(), true, "3h")
	c.store.Close()
	c2, _ := newControls(t, dir)
	c2.restore()
	if !c2.DNDState().On {
		t.Fatal("do-not-disturb lost on restart")
	}
	if u := c2.quiet.Until(); u.Before(time.Now().Add(2 * time.Hour)) {
		t.Fatalf("until = %v", u)
	}
}

func TestDoRefusesUnsafeSentences(t *testing.T) {
	c, _ := newControls(t, t.TempDir())
	if err := c.Do(context.Background(), "帮我下单", false); err == nil {
		t.Fatal("unsafe do accepted")
	}
}
