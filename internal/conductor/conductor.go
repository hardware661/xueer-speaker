// Package conductor is the only code that moves the speaker.
// Speech, voice commands and the music queue send it events; one goroutine
// owns the speaker state, so they never fight over it.
package conductor

import (
	"context"
	"sync"
	"time"

	"github.com/hardware661/xueer-speaker/internal/audio"
	"github.com/hardware661/xueer-speaker/internal/xiaomi"
)

// Device is what the conductor needs from the speaker.
type Device interface {
	Push(ctx context.Context, url string) error // player_play_music (or play_url), replaces what plays
	SpeakNative(ctx context.Context, text string) error
	Control(ctx context.Context, op xiaomi.Op) error
	SetVolume(ctx context.Context, v int) error
	Status(ctx context.Context) (xiaomi.Status, error)
	SetLoop(ctx context.Context, t int) error // player_set_loop
	// BluetoothPlaying: a device on Bluetooth is making sound; the speaker
	// then plays it instead of its own player.
	BluetoothPlaying(ctx context.Context) (bool, error)
}

// State is the conductor's view of the speaker.
type State string

const (
	StateIdle            State = "idle"
	StateSpeaking        State = "speaking"
	StatePlaying         State = "playing"
	StatePausedForSpeech State = "paused-for-speech"
	StateUserPaused      State = "user-paused"
)

// Track is one song ready to push.
type Track struct {
	URL      string // an /a/ link
	Title    string
	Duration time.Duration
}

// Queue yields songs; false means it ran out.
type Queue interface {
	Next(ctx context.Context) (Track, bool)
}

// Voice names which voice actually spoke.
type Voice string

const (
	VoiceEdge   Voice = "edge"
	VoiceNative Voice = "native"
)

// Speech is one line to say.
type Speech struct {
	URL      string        // /a/ link to the rendered clip; "" = native voice only
	Text     string        // for the native fallback
	Duration time.Duration // clip length
	Volume   int           // 0 = leave as is
}

// Outcome reports how a Speech went.
type Outcome struct {
	Voice Voice
	Err   error
}

// CmdKind is a music / voice command.
type CmdKind int

const (
	CmdPlay        CmdKind = iota // start a session from Queue
	CmdNext                       // skip to the next song
	CmdStop                       // end the session
	CmdHeardPause                 // the owner said a native "pause"
	CmdHeardResume                // the owner said a native "continue"
	CmdPause                      // the panel pauses our session
	CmdResume                     // the panel resumes it
)

// Command is sent by the listener (FromVoice) or the HTTP API.
type Command struct {
	Kind      CmdKind
	Queue     Queue // CmdPlay
	FromVoice bool  // Xiao Ai is answering this sentence: interrupt it first
	// Done, if set, is closed once the command has been carried out (the
	// PC's Husic is only told to play after Xiao Ai has been silenced).
	Done chan struct{}
	// Hushed, if set, is closed after the first pause→stop round, when Xiao
	// Ai has gone quiet; the second round (in case she starts again) still
	// follows. The PC's Husic ignores the Bluetooth pauses of that second
	// round, so it need not wait for it (three seconds saved).
	Hushed chan struct{}
}

// Options tune timing; zero values get production defaults.
//
// A pushed clip is played once because of player_set_loop type 2 (see
// speakClip). The fetch log only tells when playback began (the player
// status never changes for a pushed clip), which paces the queue.
type Options struct {
	Fetches         func(url string) []audio.Fetch // what the speaker fetched for this /a/ link
	FetchWait       time.Duration                  // how long a clip may go unfetched before falling back (15 s)
	FetchPoll       time.Duration                  // how often Fetches is read while a clip plays (100 ms)
	SettleAfterStop time.Duration                  // between stopping a failed clip and the native fallback (1.5 s)
	SpeechPad       time.Duration                  // extra wait after a line, before the next one (1 s)
	Poll            time.Duration                  // status poll while music plays (2 s)
	InterruptGap    time.Duration                  // between the two pause→stop rounds (600 ms)
	PauseTimeout    time.Duration                  // a native pause longer than this ends the session (30 min)
	MaxSession      time.Duration                  // a music session never runs longer (2 h)
	// NativeOnly speaks every line in Xiao Ai's own voice and never pushes a
	// clip through the music player: the switch for when Edge or the clip
	// channel misbehaves.
	NativeOnly bool
}

func (o *Options) defaults() {
	set := func(d *time.Duration, v time.Duration) {
		if *d == 0 {
			*d = v
		}
	}
	set(&o.FetchWait, 15*time.Second) // 11-15 real OH2P: 6 s happened; 5 s fell back by mistake
	set(&o.FetchPoll, 100*time.Millisecond)
	set(&o.SettleAfterStop, 1500*time.Millisecond)
	set(&o.SpeechPad, time.Second)
	set(&o.Poll, 2*time.Second)
	set(&o.InterruptGap, 600*time.Millisecond)
	set(&o.PauseTimeout, 30*time.Minute)
	set(&o.MaxSession, 2*time.Hour)
	if o.Fetches == nil {
		o.Fetches = func(string) []audio.Fetch { return nil }
	}
}

type speakReq struct {
	sp   Speech
	done chan Outcome
}

// Conductor serialises everything that touches the speaker.
type Conductor struct {
	dev  Device
	opt  Options
	say  chan speakReq
	cmds chan Command

	mu      sync.Mutex
	state   State
	playing string // title of our song, for the panel

	// session (only touched by Run's goroutine)
	queue    Queue
	current  Track
	started  time.Time
	pausedAt time.Time
	last     xiaomi.Status
	haveLast bool
	pushedAt time.Time // when the current song was pushed
}

// New returns a conductor; call Run.
func New(d Device, opt Options) *Conductor {
	opt.defaults()
	return &Conductor{dev: d, opt: opt, say: make(chan speakReq, 64), cmds: make(chan Command, 16), state: StateIdle}
}

// State is safe to call from anywhere.
func (c *Conductor) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// Playing is the title of the song our session is on, "" otherwise.
func (c *Conductor) Playing() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.playing
}

func (c *Conductor) setPlaying(title string) {
	c.mu.Lock()
	c.playing = title
	c.mu.Unlock()
}

func (c *Conductor) setState(s State) {
	c.mu.Lock()
	c.state = s
	c.mu.Unlock()
}

// Speak queues a line; the channel gets one Outcome when it has been spoken.
func (c *Conductor) Speak(sp Speech) <-chan Outcome {
	done := make(chan Outcome, 1)
	c.say <- speakReq{sp: sp, done: done}
	return done
}

// Command queues a command. Commands are handled before pending speech.
func (c *Conductor) Command(cmd Command) { c.cmds <- cmd }

// Run owns the speaker until ctx ends.
func (c *Conductor) Run(ctx context.Context) {
	tick := time.NewTicker(c.opt.Poll)
	defer tick.Stop()
	for {
		// Commands first: the owner is standing next to the speaker.
		select {
		case cmd := <-c.cmds:
			c.handle(ctx, cmd)
			continue
		default:
		}
		select {
		case <-ctx.Done():
			return
		case cmd := <-c.cmds:
			c.handle(ctx, cmd)
		case r := <-c.say:
			r.done <- c.speak(ctx, r.sp)
		case <-tick.C:
			c.watch(ctx)
		}
	}
}

func (c *Conductor) handle(ctx context.Context, cmd Command) {
	if cmd.Done != nil {
		defer close(cmd.Done)
	}
	switch {
	case cmd.FromVoice && cmd.Kind == CmdPlay:
		// Our push replaces whatever the player holds, Xiao Ai's answer
		// included, so one pause to cut her short is enough (two full
		// rounds cost five seconds before the song even went out).
		c.dev.Control(ctx, xiaomi.OpPause)
	case cmd.FromVoice:
		c.interrupt(ctx, cmd.Hushed)
	case cmd.Hushed != nil:
		close(cmd.Hushed)
	}
	switch cmd.Kind {
	case CmdPlay:
		if cmd.Queue == nil {
			return
		}
		c.queue, c.started = cmd.Queue, time.Now()
		c.advance(ctx)
	case CmdNext:
		if c.queue != nil {
			c.advance(ctx)
		}
	case CmdStop:
		if !cmd.FromVoice { // a voice stop was already interrupted above
			c.dev.Control(ctx, xiaomi.OpStop)
		}
		c.endSession()
	case CmdHeardPause:
		if c.State() == StatePlaying {
			c.pausedAt = time.Now()
			c.setState(StateUserPaused)
		}
	case CmdHeardResume:
		if c.State() == StateUserPaused {
			c.haveLast = false
			c.setState(StatePlaying)
		}
	case CmdPause:
		if c.State() == StatePlaying {
			c.dev.Control(ctx, xiaomi.OpPause)
			c.pausedAt = time.Now()
			c.setState(StateUserPaused)
		}
	case CmdResume:
		if c.State() == StateUserPaused {
			// "play" after "pause" leaves a pushed mp3 silent on the OH2P:
			// push the song again (it restarts).
			if err := c.dev.Push(ctx, c.current.URL); err != nil {
				c.advance(ctx)
				return
			}
			c.haveLast, c.pushedAt = false, time.Now()
			c.setState(StatePlaying)
		}
	}
}

// interrupt silences Xiao Ai's own answer: pause→stop twice (Songloft).
func (c *Conductor) interrupt(ctx context.Context, hushed chan struct{}) {
	for i := range 2 {
		c.dev.Control(ctx, xiaomi.OpPause)
		if i == 0 && hushed != nil {
			// Xiao Ai stops talking at the first pause; whoever waits on
			// hushed (the PC's Husic) goes now. 11-15 16:25: the stop
			// after it took 1.7 s of the 2.6 s before Husic was told.
			close(hushed)
		}
		c.dev.Control(ctx, xiaomi.OpStop)
		if i == 0 {
			sleep(ctx, c.opt.InterruptGap)
		}
	}
}

func (c *Conductor) advance(ctx context.Context) {
	for range 3 { // three unplayable songs in a row end the session
		t, ok := c.queue.Next(ctx)
		if !ok {
			c.endSession()
			return
		}
		if err := c.dev.Push(ctx, t.URL); err == nil {
			c.current, c.haveLast, c.pushedAt = t, false, time.Now()
			c.setPlaying(t.Title)
			c.setState(StatePlaying)
			return
		}
	}
	c.endSession()
}

func (c *Conductor) endSession() {
	c.queue, c.current, c.haveLast = nil, Track{}, false
	c.setPlaying("")
	c.setState(StateIdle)
}

// watch follows the playing song and moves on when it ends.
func (c *Conductor) watch(ctx context.Context) {
	switch c.State() {
	case StateUserPaused:
		if time.Since(c.pausedAt) > c.opt.PauseTimeout {
			c.endSession()
		}
		return
	case StatePlaying:
	default:
		return
	}
	if time.Since(c.started) > c.opt.MaxSession {
		c.dev.Control(ctx, xiaomi.OpStop)
		c.endSession()
		return
	}
	// The PC on Bluetooth took the speaker: our song is gone and will not
	// come back by itself (03:20, real device). End rather than pretend.
	if bt, err := c.dev.BluetoothPlaying(ctx); err == nil && bt {
		c.endSession()
		return
	}
	// The OH2P keeps reporting status 1 and position 0 for an external mp3,
	// so the fetch log and the clock decide: never fetched = it did
	// not play; fetched = it ends at its start plus its length.
	var began time.Time // first fetch since the latest push (a line in between re-pushes the song)
	for _, x := range c.opt.Fetches(c.current.URL) {
		if !x.At.Before(c.pushedAt) {
			began = x.At
			break
		}
	}
	if began.IsZero() {
		if time.Since(c.pushedAt) > c.opt.FetchWait {
			c.advance(ctx)
		}
		return
	}
	if c.current.Duration > 0 && time.Since(began) > c.current.Duration+c.opt.SpeechPad {
		c.advance(ctx)
		return
	}
	cur, err := c.dev.Status(ctx)
	if err != nil {
		return
	}
	if c.haveLast && songEnded(c.last, cur) {
		c.advance(ctx)
		return
	}
	c.last, c.haveLast = cur, true
}

// speak says one line, pausing and resuming music around it.
func (c *Conductor) speak(ctx context.Context, sp Speech) Outcome {
	prev := c.State()
	if prev == StatePlaying {
		c.setState(StatePausedForSpeech)
	} else {
		c.setState(StateSpeaking)
	}
	var restore int
	if sp.Volume > 0 {
		if st, err := c.dev.Status(ctx); err == nil {
			restore = st.Volume
		}
		c.dev.SetVolume(ctx, sp.Volume)
	}
	out := c.speakClip(ctx, sp)
	if restore > 0 {
		c.dev.SetVolume(ctx, restore)
	}
	if prev == StatePlaying && c.queue != nil {
		// REPLACE_ALL took the song away; put the same song back.
		if err := c.dev.Push(ctx, c.current.URL); err == nil {
			c.haveLast, c.pushedAt = false, time.Now()
			c.setState(StatePlaying)
			return out
		}
		c.advance(ctx)
		return out
	}
	if prev == StateUserPaused {
		c.setState(StateUserPaused)
	} else {
		c.setState(StateIdle)
	}
	return out
}

func (c *Conductor) speakClip(ctx context.Context, sp Speech) Outcome {
	if sp.URL != "" && !c.opt.NativeOnly {
		// No pause/stop before the push: on the OH2P that cut the clip short.
		if err := c.dev.Push(ctx, sp.URL); err == nil {
			// Loop type 2 right after the push (hass-xiaomi-miot #2970): the
			// player plays the clip once, then goes idle and hands the sound
			// back to the Bluetooth source (the PC's Husic) by itself.
			// Real OH2P: heard once, the Bluetooth source back at once, no refetch.
			if err := c.dev.SetLoop(ctx, loopOnce); err == nil && c.awaitPlayed(ctx, sp) {
				return Outcome{Voice: VoiceEdge}
			}
		}
		c.dev.Control(ctx, xiaomi.OpStop) // never stack two voices
		// Give the player a moment to settle: right after a stop, the native
		// voice was cut off at its first words.
		sleep(ctx, c.opt.SettleAfterStop)
	}
	err := c.dev.SpeakNative(ctx, sp.Text)
	if err == nil {
		sleep(ctx, nativeDuration(sp.Text)+c.opt.SpeechPad)
	}
	return Outcome{Voice: VoiceNative, Err: err}
}

// loopOnce is the player_set_loop type that plays a list once and stops
// (0 single repeat, 1 list repeat, 3 random; 2 is undocumented).
const loopOnce = 2

// awaitPlayed waits until the clip has had time to play: false if the
// speaker never fetched it. It only paces the queue so the next line does
// not cut this one; the speaker stops by itself.
func (c *Conductor) awaitPlayed(ctx context.Context, sp Speech) bool {
	deadline := time.Now().Add(c.opt.FetchWait)
	for {
		f := c.opt.Fetches(sp.URL)
		if len(f) > 0 {
			start := playStart(f)
			if !start.IsZero() && time.Now().After(start.Add(sp.Duration+c.opt.SpeechPad)) {
				return true
			}
		} else if time.Now().After(deadline) {
			return false
		}
		if ctx.Err() != nil {
			return true
		}
		sleep(ctx, c.opt.FetchPoll)
	}
}

// playStart is when playback began: the whole fetch after the tail (ID3v1)
// fetch, or the first fetch if the speaker skipped the tail; zero while the
// speaker is still probing (whole then tail, no start yet).
func playStart(f []audio.Fetch) time.Time {
	for i, x := range f {
		if !x.Whole {
			for _, y := range f[i+1:] {
				if y.Whole {
					return y.At
				}
			}
			return time.Time{}
		}
	}
	return f[0].At
}

// nativeDuration estimates Xiao Ai reading speed (xiaogpt: about 4.5 chars/s).
func nativeDuration(text string) time.Duration {
	return time.Duration(len([]rune(text))) * time.Second * 2 / 9
}

func sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
