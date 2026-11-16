package command

import (
	"context"

	"errors"
	"github.com/hardware661/xueer-speaker/internal/jukebox"
	"strings"
	"testing"

	"github.com/hardware661/xueer-speaker/internal/conductor"
)

type rig struct {
	state  conductor.State
	played []string
	voice  []bool
	cmds   []conductor.Command
	said   []string
	err    error
}

func (r *rig) d() *Dispatcher {
	return &Dispatcher{
		Play: func(_ context.Context, what string, fromVoice bool) error {
			r.played = append(r.played, what)
			r.voice = append(r.voice, fromVoice)
			return r.err
		},
		Send:  func(c conductor.Command) { r.cmds = append(r.cmds, c) },
		State: func() conductor.State { return r.state },
		Say:   func(text string) { r.said = append(r.said, text) },
	}
}

func TestPlayLikedAndPlaylistInterruptXiaoAi(t *testing.T) {
	r := &rig{state: conductor.StateIdle}
	d := r.d()
	if a := d.Handle(context.Background(), "播放我的歌单"); a != "放我喜欢的歌" {
		t.Fatalf("action %q", a)
	}
	if a := d.Handle(context.Background(), "播放我的开车歌单。"); a != "放歌单「开车」" {
		t.Fatalf("action %q", a)
	}
	if strings.Join(r.played, ",") != "liked,playlist:开车" || !r.voice[0] || !r.voice[1] {
		t.Fatalf("played %v voice %v", r.played, r.voice)
	}
}

func TestNextAndStopOnlyTouchOurOwnSession(t *testing.T) {
	r := &rig{state: conductor.StateIdle}
	d := r.d()
	if a := d.Handle(context.Background(), "下一首"); a != "" || len(r.cmds) != 0 {
		t.Fatalf("idle next: %q %v", a, r.cmds)
	}
	if a := d.Handle(context.Background(), "停止"); a != "" || len(r.cmds) != 0 {
		t.Fatalf("idle stop: %q %v", a, r.cmds)
	}
	r.state = conductor.StatePlaying
	d.Handle(context.Background(), "下一首")
	d.Handle(context.Background(), "别放了")
	if len(r.cmds) != 2 || r.cmds[0].Kind != conductor.CmdNext || !r.cmds[0].FromVoice || r.cmds[1].Kind != conductor.CmdStop || !r.cmds[1].FromVoice {
		t.Fatalf("cmds %+v", r.cmds)
	}
}

func TestNativePauseResumeAreFollowedNotInterrupted(t *testing.T) {
	r := &rig{state: conductor.StatePlaying}
	d := r.d()
	if a := d.Handle(context.Background(), "暂停"); a != "跟着暂停" {
		t.Fatalf("action %q", a)
	}
	r.state = conductor.StateUserPaused
	d.Handle(context.Background(), "继续播放")
	if len(r.cmds) != 2 || r.cmds[0].Kind != conductor.CmdHeardPause || r.cmds[0].FromVoice || r.cmds[1].Kind != conductor.CmdHeardResume {
		t.Fatalf("cmds %+v", r.cmds)
	}
}

func TestHusicDownIsSaidOutLoud(t *testing.T) {
	r := &rig{state: conductor.StateIdle, err: errors.New("husic: dial tcp: refused")}
	d := r.d()
	if a := d.Handle(context.Background(), "播放我的歌单"); !strings.HasPrefix(a, "没放成") {
		t.Fatalf("action %q", a)
	}
	if len(r.said) != 1 || !strings.Contains(r.said[0], "Husic") {
		t.Fatalf("said %v", r.said)
	}
}

func TestOtherSentencesAreLeftToXiaoAi(t *testing.T) {
	r := &rig{state: conductor.StatePlaying}
	if a := r.d().Handle(context.Background(), "现在几点"); a != "" || len(r.cmds)+len(r.played) != 0 {
		t.Fatalf("%q %v %v", a, r.cmds, r.played)
	}
}

// When the music plays on the PC, the voice commands go there.
func TestControlsGoToThePCWhenItPlays(t *testing.T) {
	r := &rig{state: conductor.StateIdle}
	d := r.d()
	var pc []string
	d.PC = func(_ context.Context, action string, fromVoice bool) (bool, error) {
		if !fromVoice {
			t.Fatal("voice controls must silence Xiao Ai")
		}
		pc = append(pc, action)
		return true, nil
	}
	for _, q := range []string{"下一首", "暂停", "继续播放", "停止"} {
		if a := d.Handle(context.Background(), q); a == "" {
			t.Fatalf("%s not handled", q)
		}
	}
	if strings.Join(pc, ",") != "next,pause,resume,stop" || len(r.cmds) != 0 {
		t.Fatalf("pc %v cmds %+v", pc, r.cmds)
	}
}

func TestHusicClosedIsSaid(t *testing.T) {
	r := &rig{state: conductor.StateIdle, err: jukeboxClosed}
	d := r.d()
	if a := d.Handle(context.Background(), "播放我的歌单"); !strings.Contains(a, "没开") {
		t.Fatalf("action %q", a)
	}
	if len(r.said) != 1 || !strings.Contains(r.said[0], "电脑上的 Husic 没开着") {
		t.Fatalf("said %v", r.said)
	}
}

var jukeboxClosed = jukebox.ErrHusicClosed
