// Package jukebox turns "what to play" from the panel or the timetable
// ("liked" or "playlist:<name>") into a queue of /a/ song links and hands it
// to the conductor. It is the server.Player the panel and schedules use.
package jukebox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hardware661/xueer-speaker/internal/conductor"
	"github.com/hardware661/xueer-speaker/internal/music"
	"github.com/hardware661/xueer-speaker/internal/server"
)

// Library is the owner's Husic library (music.Client).
type Library interface {
	Liked(ctx context.Context, order func(int) []int) (*music.Queue, error)
	Playlist(ctx context.Context, name string, order func(int) []int) (*music.Queue, error)
	Playlists(ctx context.Context) ([]string, error)
}

// Host publishes a song as an /a/ link (audio.Host).
type Host interface {
	Song(resolve func() (string, error), length time.Duration) string
}

// PC is the owner's PC as a music player: when it is on the
// speaker's Bluetooth, its Husic plays and the sound reaches the speaker
// that way; the speaker's own player would lose to any sound the PC makes.
type PC interface {
	Connected(ctx context.Context) bool // the PC is on the speaker's Bluetooth
	// Playing: the open Husic is playing music right now.
	Playing(ctx context.Context) bool
	// Command asks the open Husic to play / next / pause / resume / stop;
	// ErrHusicClosed when no Husic is open.
	Command(ctx context.Context, action, what string) error
}

// ErrHusicClosed: the PC is on the speaker but no Husic is open on it.
var ErrHusicClosed = server.ErrHusicClosed

// Where says who plays the music.
type Where string

const (
	WhereNone    Where = ""
	WherePC      Where = "pc"
	WhereSpeaker Where = "speaker"
)

// Jukebox implements server.Player.
type Jukebox struct {
	lib  Library
	host Host
	send func(conductor.Command)
	PC   PC          // nil = the speaker always plays
	Idle func() bool // our own speaker session is idle (conductor); nil = assume busy

	mu       sync.Mutex
	where    Where
	pausedAt time.Time // when we last paused the PC's Husic (a "continue" may follow)
	playedAt time.Time // when we last told it to play (it reports playing a moment later)
}

// resumeWindow: a "continue" this long after our pause still goes to Husic.
const resumeWindow = 30 * time.Minute

// Where is who plays the current music.
func (j *Jukebox) Where() Where {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.where
}

func (j *Jukebox) setWhere(w Where) {
	j.mu.Lock()
	j.where = w
	j.mu.Unlock()
}

// pcOn: the PC is on right now. Where the
// last song played does not count: after the PC goes off (Bluetooth off,
// tray quit) the speaker must play, 11-15 16:56 it was told Husic.
func (j *Jukebox) pcOn(ctx context.Context) bool {
	if j.PC != nil && j.PC.Connected(ctx) {
		return true
	}
	if j.Where() == WherePC {
		j.setWhere(WhereNone)
	}
	return false
}

// silence quiets the speaker before the PC acts: Xiao Ai, who is answering
// a voice command, and our own speaker session if one runs. It waits until
// that is done, so the speaker's pause (which reaches the PC over
// Bluetooth) comes before the PC's command, not after it (otherwise it cuts
// each song after two seconds). A panel click on an idle speaker sends
// nothing to it at all.
func (j *Jukebox) silence(fromVoice bool) {
	if !fromVoice && j.Idle != nil && j.Idle() {
		return
	}
	// Wait only until Xiao Ai has gone quiet: the PC's Husic ignores
	// Bluetooth pauses for a few seconds after a remote play (husic
	// remoteHold), so the speaker's second silencing round cannot cut it.
	hushed := make(chan struct{})
	j.send(conductor.Command{Kind: conductor.CmdStop, FromVoice: fromVoice, Hushed: hushed})
	select {
	case <-hushed:
	case <-time.After(8 * time.Second):
	}
}

// Control passes next / pause / resume / stop to the PC's Husic when the
// music is the PC's; handled false = the caller's speaker session.
//
// A voice command is Husic's only while its music plays, or for a
// "continue" soon after we paused it: otherwise "关闭" / "暂停"
// is meant for Xiao Ai herself (her alarm, her news) and is left alone.
func (j *Jukebox) Control(ctx context.Context, action string, fromVoice bool) (handled bool, err error) {
	if !j.pcOn(ctx) {
		return false, nil
	}
	if fromVoice && !j.husicsTurn(ctx, action) {
		return false, nil
	}
	j.silence(fromVoice)
	err = j.PC.Command(ctx, action, "")
	j.mu.Lock()
	switch {
	case action == "stop":
		j.where, j.pausedAt = WhereNone, time.Time{}
	case err == nil && action == "pause":
		j.where, j.pausedAt = WherePC, time.Now()
	case err == nil:
		j.where, j.pausedAt = WherePC, time.Time{}
	}
	j.mu.Unlock()
	return true, err
}

func (j *Jukebox) husicsTurn(ctx context.Context, action string) bool {
	if j.PC.Playing(ctx) {
		return true
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.playedAt.IsZero() && time.Since(j.playedAt) < 15*time.Second {
		return true
	}
	return action == "resume" && !j.pausedAt.IsZero() && time.Since(j.pausedAt) < resumeWindow
}

// Do is the panel's next / pause / resume / stop, wherever the music is.
func (j *Jukebox) Do(ctx context.Context, action string) error {
	if handled, err := j.Control(ctx, action, false); handled {
		if action == "stop" && errors.Is(err, ErrHusicClosed) {
			return nil // nothing to stop
		}
		return err
	}
	kind, ok := map[string]conductor.CmdKind{"next": conductor.CmdNext, "stop": conductor.CmdStop,
		"pause": conductor.CmdPause, "resume": conductor.CmdResume}[action]
	if !ok {
		return fmt.Errorf("不认识的操作 %q", action)
	}
	j.send(conductor.Command{Kind: kind})
	return nil
}

// New returns a jukebox sending play commands with send (Conductor.Command).
func New(lib Library, host Host, send func(conductor.Command)) *Jukebox {
	return &Jukebox{lib: lib, host: host, send: send}
}

// Play starts what: "liked" or "playlist:<name>" (panel, timetable).
func (j *Jukebox) Play(ctx context.Context, what string) error { return j.Start(ctx, what, false) }

// Start is Play; fromVoice means Xiao Ai is answering the owner's sentence
// and must be silenced first.
func (j *Jukebox) Start(ctx context.Context, what string, fromVoice bool) error {
	if j.pcOn(ctx) {
		j.silence(fromVoice)
		err := j.PC.Command(ctx, "play", what)
		if errors.Is(err, ErrHusicClosed) {
			// No Husic open on the PC: the speaker plays by itself.
			// Xiao Ai was silenced just now.
			return j.startOnSpeaker(ctx, what, false)
		}
		if err != nil {
			return err
		}
		j.mu.Lock()
		j.where, j.playedAt, j.pausedAt = WherePC, time.Now(), time.Time{}
		j.mu.Unlock()
		return nil
	}
	return j.startOnSpeaker(ctx, what, fromVoice)
}

// startOnSpeaker plays what on the speaker's own player.
func (j *Jukebox) startOnSpeaker(ctx context.Context, what string, fromVoice bool) error {
	var q *music.Queue
	var err error
	switch name, isList := strings.CutPrefix(what, "playlist:"); {
	case what == "liked":
		q, err = j.lib.Liked(ctx, nil)
	case isList && name != "":
		q, err = j.lib.Playlist(ctx, name, nil)
	default:
		return fmt.Errorf("不认识要放的 %q", what)
	}
	switch {
	case errors.Is(err, music.ErrNoPlaylist):
		return fmt.Errorf("%w: %s", server.ErrNoPlaylist, what)
	case err != nil:
		return err
	}
	t := &tracks{q: q, host: j.host}
	t.warm() // find the first playable song while Xiao Ai is being cut short
	j.send(conductor.Command{Kind: conductor.CmdPlay, Queue: t, FromVoice: fromVoice})
	j.setWhere(WhereSpeaker)
	return nil
}

// Playlists lists the owner's playlists for the panel.
func (j *Jukebox) Playlists(ctx context.Context) ([]string, error) { return j.lib.Playlists(ctx) }

// maxSkips unplayable songs in a row end the round (NetEase down, say)
// rather than walking the whole library.
const maxSkips = 5

// tracks publishes each song only when the conductor reaches it, so the
// short-lived link is fresh. A song is resolved once first: the speaker
// would otherwise sit on a dead link until the conductor gives up on it
// (one liked song had no NetEase copy any more).
type tracks struct {
	q     *music.Queue
	host  Host
	first chan warmTrack // the first song, looked up ahead of time
	mu    sync.Mutex     // the warm-up and the conductor never walk q together
}

type warmTrack struct {
	t  conductor.Track
	ok bool
}

// warm resolves the first song in the background (a NetEase lookup takes a
// second or two) so the push can go out the moment the speaker is ready.
func (t *tracks) warm() {
	first := make(chan warmTrack, 1)
	t.first = first
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		tr, ok := t.next(ctx)
		first <- warmTrack{tr, ok}
	}()
}

func (t *tracks) Next(ctx context.Context) (conductor.Track, bool) {
	if t.first != nil {
		first := t.first
		t.first = nil
		select {
		case w := <-first:
			return w.t, w.ok
		case <-ctx.Done():
			return conductor.Track{}, false
		}
	}
	return t.next(ctx)
}

func (t *tracks) next(ctx context.Context) (conductor.Track, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	var s music.Song
	for skips := 0; ; skips++ {
		var ok bool
		if s, ok = t.q.Next(ctx); !ok || skips >= maxSkips {
			return conductor.Track{}, false
		}
		if s.Resolve == nil {
			break
		}
		if u, err := s.Resolve(); err == nil {
			s.Resolve = reuseOnce(u, s.Resolve)
			break
		}
	}
	title := s.Title
	if s.Artist != "" {
		title += " · " + s.Artist
	}
	return conductor.Track{URL: t.host.Song(s.Resolve, s.Duration), Title: title, Duration: s.Duration}, true
}

// reuseOnce hands the link just looked up to the speaker's first fetch
// instead of asking NetEase again (a second or so before sound, 11-15);
// later fetches (a re-push after a line) get a fresh one.
func reuseOnce(u string, fresh func() (string, error)) func() (string, error) {
	var mu sync.Mutex
	at := time.Now()
	return func() (string, error) {
		mu.Lock()
		if u != "" && time.Since(at) < 2*time.Minute {
			got := u
			u = ""
			mu.Unlock()
			return got, nil
		}
		mu.Unlock()
		return fresh()
	}
}
