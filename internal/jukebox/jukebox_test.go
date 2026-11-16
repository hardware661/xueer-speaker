package jukebox

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hardware661/xueer-speaker/internal/conductor"
	"github.com/hardware661/xueer-speaker/internal/music"
	"github.com/hardware661/xueer-speaker/internal/server"
)

type fakeLib struct {
	liked, list *music.Queue
	err         error
	asked       string
}

func (f *fakeLib) Liked(ctx context.Context, _ func(int) []int) (*music.Queue, error) {
	f.asked = "liked"
	return f.liked, f.err
}
func (f *fakeLib) Playlist(ctx context.Context, name string, _ func(int) []int) (*music.Queue, error) {
	f.asked = "playlist:" + name
	return f.list, f.err
}
func (f *fakeLib) Playlists(context.Context) ([]string, error) { return []string{"早上听"}, nil }

type fakeHost struct {
	mu sync.Mutex // like audio.Host: songs are published from the warm-up too
	n  int
}

func (h *fakeHost) Song(resolve func() (string, error), length time.Duration) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.n++
	return "https://speaker/a/song"
}

func TestPlayLikedSendsQueueToConductor(t *testing.T) {
	lib := &fakeLib{liked: music.NewQueue([]music.Song{{Title: "晴天", Duration: 200 * time.Second}})}
	var got []conductor.Command
	j := New(lib, &fakeHost{}, func(c conductor.Command) { got = append(got, c) })
	if err := j.Play(context.Background(), "liked"); err != nil {
		t.Fatal(err)
	}
	if lib.asked != "liked" || len(got) != 1 || got[0].Kind != conductor.CmdPlay {
		t.Fatalf("asked %q cmds %+v", lib.asked, got)
	}
	tr, ok := got[0].Queue.Next(context.Background())
	if !ok || tr.Title != "晴天" || tr.URL != "https://speaker/a/song" || tr.Duration != 200*time.Second {
		t.Fatalf("track %+v %v", tr, ok)
	}
	if _, ok := got[0].Queue.Next(context.Background()); ok {
		t.Fatal("queue should end")
	}
}

func TestPlayPlaylistByName(t *testing.T) {
	lib := &fakeLib{list: music.NewQueue([]music.Song{{Title: "早安"}})}
	j := New(lib, &fakeHost{}, func(conductor.Command) {})
	if err := j.Play(context.Background(), "playlist:早上听"); err != nil {
		t.Fatal(err)
	}
	if lib.asked != "playlist:早上听" {
		t.Fatalf("asked %q", lib.asked)
	}
}

func TestErrorsMapToServerErrors(t *testing.T) {
	j := New(&fakeLib{err: music.ErrNoPlaylist}, &fakeHost{}, func(conductor.Command) { t.Fatal("no command on error") })
	if err := j.Play(context.Background(), "playlist:睡觉"); !errors.Is(err, server.ErrNoPlaylist) {
		t.Fatalf("err %v", err)
	}
	if err := j.Play(context.Background(), "别的"); err == nil {
		t.Fatal("unknown what must fail")
	}
}

func TestUnplayableSongsAreSkippedBeforePush(t *testing.T) {
	bad := func() (string, error) { return "", music.ErrUnplayable }
	good := func() (string, error) { return "https://m701.music.126.net/x.mp3", nil }
	lib := &fakeLib{liked: music.NewQueue([]music.Song{{Title: "坏", Resolve: bad}, {Title: "好", Resolve: good}, {Title: "又坏", Resolve: bad}})}
	var got []conductor.Command
	j := New(lib, &fakeHost{}, func(c conductor.Command) { got = append(got, c) })
	if err := j.Play(context.Background(), "liked"); err != nil {
		t.Fatal(err)
	}
	q := got[0].Queue
	if tr, ok := q.Next(context.Background()); !ok || tr.Title != "好" {
		t.Fatalf("first %+v %v", tr, ok)
	}
	if _, ok := q.Next(context.Background()); ok {
		t.Fatal("trailing unplayable song must end the queue")
	}
}

func TestStartFromVoiceMarksTheCommand(t *testing.T) {
	lib := &fakeLib{liked: music.NewQueue([]music.Song{{Title: "晴天"}})}
	var got []conductor.Command
	j := New(lib, &fakeHost{}, func(c conductor.Command) { got = append(got, c) })
	j.Start(context.Background(), "liked", true)
	lib.liked = music.NewQueue([]music.Song{{Title: "晴天"}}) // each session gets its own queue, as from Husic
	j.Play(context.Background(), "liked")
	if !got[0].FromVoice || got[1].FromVoice {
		t.Fatalf("%+v", got)
	}
}

type fakePC struct {
	connected bool
	open      bool
	playing   bool
	sent      []string
}

func (f *fakePC) Connected(context.Context) bool { return f.connected }
func (f *fakePC) Playing(context.Context) bool   { return f.open && f.playing }
func (f *fakePC) Command(_ context.Context, action, what string) error {
	if !f.open {
		return ErrHusicClosed
	}
	f.sent = append(f.sent, action+":"+what)
	return nil
}

// The owner's PC on the speaker's Bluetooth plays the music itself (owner
// 11-15): the speaker's own player would lose to the PC's sound.
func TestPlayGoesToThePCWhenItIsOnTheSpeaker(t *testing.T) {
	lib := &fakeLib{liked: music.NewQueue([]music.Song{{Title: "晴天"}})}
	var cmds []conductor.Command
	pc := &fakePC{connected: true, open: true}
	j := New(lib, &fakeHost{}, record(&cmds))
	j.PC = pc
	if err := j.Start(context.Background(), "playlist:开车", true); err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 || cmds[0].Kind != conductor.CmdStop || !cmds[0].FromVoice || strings.Join(pc.sent, ",") != "play:playlist:开车" {
		t.Fatalf("cmds %+v pc %v", cmds, pc.sent)
	}
	if j.Where() != WherePC {
		t.Fatalf("where %q", j.Where())
	}
	j.Control(context.Background(), "next", true) // Husic may not report playing yet
	if strings.Join(pc.sent, ",") != "play:playlist:开车,next:" {
		t.Fatalf("pc %v", pc.sent)
	}
}

// the PC is on but Husic is closed — the speaker
// plays by itself instead of asking the owner to open Husic.
func TestHusicClosedOnThePCMeansTheSpeakerPlays(t *testing.T) {
	lib := &fakeLib{liked: music.NewQueue([]music.Song{{Title: "晴天"}})}
	var cmds []conductor.Command
	j := New(lib, &fakeHost{}, record(&cmds))
	j.PC = &fakePC{connected: true}
	if err := j.Start(context.Background(), "liked", true); err != nil {
		t.Fatalf("err %v", err)
	}
	last := cmds[len(cmds)-1]
	if last.Kind != conductor.CmdPlay || last.FromVoice || j.Where() != WhereSpeaker {
		t.Fatalf("cmds %+v where %q", cmds, j.Where())
	}
}

func TestNoPCMeansTheSpeakerPlays(t *testing.T) {
	lib := &fakeLib{liked: music.NewQueue([]music.Song{{Title: "晴天"}})}
	var cmds []conductor.Command
	j := New(lib, &fakeHost{}, func(c conductor.Command) { cmds = append(cmds, c) })
	j.PC = &fakePC{connected: false, open: true}
	if err := j.Start(context.Background(), "liked", false); err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 1 || cmds[0].Kind != conductor.CmdPlay || j.Where() != WhereSpeaker {
		t.Fatalf("cmds %+v where %q", cmds, j.Where())
	}
}

// record keeps commands and answers Done at once, like a conductor that has
// silenced the speaker.
func record(cmds *[]conductor.Command) func(conductor.Command) {
	return func(c conductor.Command) {
		*cmds = append(*cmds, c)
		if c.Hushed != nil {
			close(c.Hushed)
		}
		if c.Done != nil {
			close(c.Done)
		}
	}
}

// 11-15: the speaker's "silence Xiao Ai" pause reached the PC over Bluetooth
// two seconds after Husic had started, cutting the song. The PC is told to
// play only once the speaker has been silenced.
func TestThePCIsToldOnlyAfterTheSpeakerIsSilenced(t *testing.T) {
	var order []string
	pc := &orderPC{order: &order}
	j := New(&fakeLib{}, &fakeHost{}, func(c conductor.Command) {
		go func() {
			time.Sleep(30 * time.Millisecond)
			order = append(order, "silenced")
			close(c.Hushed)
		}()
	})
	j.PC = pc
	if err := j.Start(context.Background(), "liked", true); err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "silenced,play" {
		t.Fatalf("order %v", order)
	}
}

type orderPC struct{ order *[]string }

func (o *orderPC) Connected(context.Context) bool { return true }
func (o *orderPC) Playing(context.Context) bool   { return true }
func (o *orderPC) Command(_ context.Context, action, _ string) error {
	*o.order = append(*o.order, action)
	return nil
}

// A panel click while the PC is on never touches an idle speaker (its pause
// would reach the PC over Bluetooth); the PC's Husic does it all.
func TestPanelControlsGoOnlyToThePC(t *testing.T) {
	var cmds []conductor.Command
	pc := &fakePC{connected: true, open: true}
	j := New(&fakeLib{}, &fakeHost{}, record(&cmds))
	j.PC, j.Idle = pc, func() bool { return true }
	for _, a := range []string{"pause", "resume", "next", "stop"} {
		if err := j.Do(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Play(context.Background(), "liked"); err != nil {
		t.Fatal(err)
	}
	if len(cmds) != 0 || strings.Join(pc.sent, ",") != "pause:,resume:,next:,stop:,play:liked" {
		t.Fatalf("cmds %+v pc %v", cmds, pc.sent)
	}
}

// PC off: the panel's controls are the speaker session's.
func TestPanelControlsWithThePCOffGoToTheSpeaker(t *testing.T) {
	var cmds []conductor.Command
	j := New(&fakeLib{}, &fakeHost{}, record(&cmds))
	j.PC = &fakePC{}
	j.Do(context.Background(), "pause")
	j.Do(context.Background(), "next")
	if len(cmds) != 2 || cmds[0].Kind != conductor.CmdPause || cmds[1].Kind != conductor.CmdNext {
		t.Fatalf("cmds %+v", cmds)
	}
}

func TestTheCheckedLinkIsReusedOnce(t *testing.T) {
	n := 0
	r := reuseOnce("https://first", func() (string, error) { n++; return "https://fresh", nil })
	if u, _ := r(); u != "https://first" || n != 0 {
		t.Fatalf("%s %d", u, n)
	}
	if u, _ := r(); u != "https://fresh" || n != 1 {
		t.Fatalf("%s %d", u, n)
	}
}

// 11-15 16:56: a song had played on the PC, then the PC went off (Bluetooth
// off, tray quit); "播放我的歌单" must go to the speaker, not to Husic.
func TestAfterThePCGoesOffTheSpeakerPlays(t *testing.T) {
	lib := &fakeLib{liked: music.NewQueue([]music.Song{{Title: "晴天"}})}
	var cmds []conductor.Command
	pc := &fakePC{connected: true, open: true}
	j := New(lib, &fakeHost{}, record(&cmds))
	j.PC = pc
	j.Start(context.Background(), "liked", true)
	if j.Where() != WherePC {
		t.Fatalf("where %q", j.Where())
	}
	pc.connected, pc.open = false, false
	lib.liked = music.NewQueue([]music.Song{{Title: "晴天"}})
	if err := j.Start(context.Background(), "liked", true); err != nil {
		t.Fatal(err)
	}
	if j.Where() != WhereSpeaker || cmds[len(cmds)-1].Kind != conductor.CmdPlay {
		t.Fatalf("where %q cmds %+v", j.Where(), cmds)
	}
	if handled, _ := j.Control(context.Background(), "pause", false); handled {
		t.Fatal("PC is off: pause is the speaker's")
	}
}

// with the PC on, a short voice command is Husic's only while its
// music plays (or was paused by voice a moment ago). Otherwise it is Xiao
// Ai's own ("小爱同学，关闭" for her alarm): not cut short, not sent to Husic.
func TestVoiceControlsAreHusicsOnlyWhileItsMusicPlays(t *testing.T) {
	var cmds []conductor.Command
	pc := &fakePC{connected: true, open: true}
	j := New(&fakeLib{}, &fakeHost{}, record(&cmds))
	j.PC = pc
	for _, a := range []string{"stop", "pause", "resume", "next"} {
		if handled, _ := j.Control(context.Background(), a, true); handled {
			t.Fatalf("%s taken from Xiao Ai while Husic was quiet", a)
		}
	}
	if len(cmds) != 0 || len(pc.sent) != 0 {
		t.Fatalf("cmds %+v pc %v", cmds, pc.sent)
	}
	pc.playing = true
	if handled, _ := j.Control(context.Background(), "pause", true); !handled {
		t.Fatal("pause while Husic plays is Husic's")
	}
	pc.playing = false // Husic now reports paused
	if handled, _ := j.Control(context.Background(), "resume", true); !handled {
		t.Fatal("resume right after a voice pause is Husic's")
	}
	pc.playing = false
	if handled, _ := j.Control(context.Background(), "pause", true); handled {
		t.Fatal("pausing a quiet Husic is not ours")
	}
	if strings.Join(pc.sent, ",") != "pause:,resume:" {
		t.Fatalf("pc %v", pc.sent)
	}
}
