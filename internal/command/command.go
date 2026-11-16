// Package command decides what to do about a sentence the owner said to
// Xiao Ai: start our music, skip or stop our own session, follow Xiao Ai's
// native pause / continue. Anything else is Xiao Ai's.
package command

import (
	"context"
	"errors"

	"github.com/hardware661/xueer-speaker/internal/conductor"
	"github.com/hardware661/xueer-speaker/internal/jukebox"
	"github.com/hardware661/xueer-speaker/internal/listener"
	"github.com/hardware661/xueer-speaker/internal/server"
)

// Dispatcher wires heard sentences to the jukebox and the conductor.
type Dispatcher struct {
	Play  func(ctx context.Context, what string, fromVoice bool) error
	Send  func(conductor.Command)
	State func() conductor.State
	Say   func(text string) // a short line back to the owner
	Owner string            // how the owner is called ("主人")
	// PC passes next / pause / resume / stop to the owner's PC when the
	// music is the PC's (jukebox.Control: it silences Xiao Ai first);
	// handled false = ours on the speaker. Nil = the speaker always plays.
	PC func(ctx context.Context, action string, fromVoice bool) (handled bool, err error)
}

// onPC hands a voice control to the PC.
func (d *Dispatcher) onPC(ctx context.Context, action string) bool {
	if d.PC == nil {
		return false
	}
	handled, _ := d.PC(ctx, action, true)
	return handled
}

// Handle acts on one sentence and returns what was done, for the panel's
// log ("" = not ours, left to Xiao Ai).
func (d *Dispatcher) Handle(ctx context.Context, query string) string {
	h := listener.Match(query)
	ours := func() bool {
		s := d.State()
		return s == conductor.StatePlaying || s == conductor.StateUserPaused || s == conductor.StatePausedForSpeech
	}
	switch h.Kind {
	case listener.IntentPlayLiked:
		return d.play(ctx, "liked", "放我喜欢的歌")
	case listener.IntentPlayList:
		return d.play(ctx, "playlist:"+h.Arg, "放歌单「"+h.Arg+"」")
	case listener.IntentNext:
		if d.onPC(ctx, "next") {
			return "电脑上下一首"
		}
		if !ours() {
			return ""
		}
		d.Send(conductor.Command{Kind: conductor.CmdNext, FromVoice: true})
		return "下一首"
	case listener.IntentHeardStop:
		if d.onPC(ctx, "stop") {
			return "电脑上停了"
		}
		if !ours() {
			return ""
		}
		d.Send(conductor.Command{Kind: conductor.CmdStop, FromVoice: true})
		return "停了"
	case listener.IntentHeardPause:
		if d.onPC(ctx, "pause") {
			return "电脑上暂停"
		}
		if d.State() != conductor.StatePlaying {
			return ""
		}
		d.Send(conductor.Command{Kind: conductor.CmdHeardPause})
		return "跟着暂停"
	case listener.IntentHeardResume:
		if d.onPC(ctx, "resume") {
			return "电脑上继续"
		}
		if d.State() != conductor.StateUserPaused {
			return ""
		}
		d.Send(conductor.Command{Kind: conductor.CmdHeardResume})
		return "跟着继续"
	}
	return ""
}

func (d *Dispatcher) play(ctx context.Context, what, done string) string {
	err := d.Play(ctx, what, true)
	switch {
	case err == nil:
		return done
	case errors.Is(err, jukebox.ErrHusicClosed):
		d.Say(d.Owner + "，电脑上的 Husic 没开着，开一下我再放")
		return "没放成：电脑上的 Husic 没开"
	case errors.Is(err, server.ErrNoPlaylist):
		d.Say("没找到这个歌单")
		return "没放成：没有这个歌单"
	default:
		d.Say("Husic 现在连不上，放不了歌")
		return "没放成：" + err.Error()
	}
}
