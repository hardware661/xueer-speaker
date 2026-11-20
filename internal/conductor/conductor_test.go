package conductor

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hardware661/xueer-speaker/internal/xiaomi"
)

type fakeQueue struct {
	n     atomic.Int32
	limit int32
	dur   time.Duration // song length; 0 = 1 s
}

func (q *fakeQueue) Next(context.Context) (Track, bool) {
	i := q.n.Add(1)
	if q.limit > 0 && i > q.limit {
		return Track{}, false
	}
	d := q.dur
	if d == 0 {
		d = time.Second
	}
	return Track{URL: "song" + string(rune('0'+i)), Title: "t", Duration: d}, true
}

// start runs a conductor whose fake speaker fetches clips like the real
// OH2P (steps == nil) or as the steps say.
func start(t *testing.T, steps []step) (*Conductor, *fakeDevice) {
	t.Helper()
	return startWith(t, &fakeDevice{status: xiaomi.Status{State: xiaomi.StateIdle, Volume: 30}}, steps)
}

func startWith(t *testing.T, d *fakeDevice, steps []step) (*Conductor, *fakeDevice) {
	t.Helper()
	if steps == nil {
		steps = realHTTPS
	}
	c := New(d, Options{
		Fetches:         fetchesFrom(d, steps),
		FetchWait:       50 * time.Millisecond,
		SettleAfterStop: time.Millisecond,
		FetchPoll:       time.Millisecond,
		SpeechPad:       10 * time.Millisecond,
		Poll:            5 * time.Millisecond,
		InterruptGap:    time.Millisecond,
		PauseTimeout:    80 * time.Millisecond,
		MaxSession:      time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go c.Run(ctx)
	return c, d
}

func say(t *testing.T, c *Conductor, sp Speech) Outcome {
	t.Helper()
	select {
	case o := <-c.Speak(sp):
		return o
	case <-time.After(2 * time.Second):
		t.Fatal("speech never finished")
		return Outcome{}
	}
}

const clipLen = 7600 * time.Microsecond // matches realHTTPS

func TestSpeakPushesClipSetsLoopOnceAndLeavesTheSpeakerAlone(t *testing.T) {
	// No pause/stop before the push (11-14: it cut the clip short); loop type 2
	// after it (11-15: played once, Husic came back by itself). No stop and no
	// Bluetooth call afterwards: that is what broke Husic coming back.
	c, d := start(t, nil)
	t0 := time.Now()
	o := say(t, c, Speech{URL: "clip1", Text: "主人。", Duration: clipLen})
	if o.Err != nil || o.Voice != VoiceEdge {
		t.Fatalf("outcome = %+v", o)
	}
	// Playback starts at the 8 ms fetch; the line is done after its length + pad (10 ms).
	if took := time.Since(t0); took < 25*time.Millisecond || took > 60*time.Millisecond {
		t.Fatalf("finished after %v, want start 8 ms + clip 7.6 ms + pad 10 ms", took)
	}
	if d.history() != "push clip1 | loop 2" {
		t.Fatalf("calls = %s", d.history())
	}
	if c.State() != StateIdle {
		t.Fatalf("state = %s", c.State())
	}
}

func TestRefusedPushFallsBackToNativeVoice(t *testing.T) {
	c, d := start(t, nil)
	d.failPush = true
	o := say(t, c, Speech{URL: "clip1", Text: "主人。", Duration: clipLen})
	if o.Voice != VoiceNative || o.Err != nil {
		t.Fatalf("outcome = %+v", o)
	}
	if !strings.HasSuffix(d.history(), "control stop | native 主人。") {
		t.Fatalf("calls = %s", d.history())
	}
}

func TestNeverFetchedFallsBackOnceNotTwice(t *testing.T) {
	c, d := start(t, []step{})
	o := say(t, c, Speech{URL: "clip1", Text: "主人。", Duration: clipLen})
	if o.Voice != VoiceNative {
		t.Fatalf("outcome = %+v", o)
	}
	if got := strings.Count(d.history(), "native"); got != 1 {
		t.Fatalf("native %d times: %s", got, d.history())
	}
}

func TestVolumeIsRestoredAfterSpeech(t *testing.T) {
	c, d := start(t, nil)
	say(t, c, Speech{URL: "clip1", Text: "主人。", Duration: clipLen, Volume: 80})
	if d.history() != "volume 80 | push clip1 | loop 2 | volume 30" {
		t.Fatalf("calls = %s", d.history())
	}
}

func TestMusicAdvancesWhenSongEnds(t *testing.T) {
	c, d := start(t, nil)
	c.Command(Command{Kind: CmdPlay, Queue: &fakeQueue{}})
	waitFor(t, "first song", func() bool { return strings.Contains(d.history(), "push song1") })
	d.setStatus(xiaomi.Status{State: xiaomi.StatePlaying, PositionMS: 195_000, DurationMS: 200_000})
	time.Sleep(20 * time.Millisecond)
	d.setStatus(xiaomi.Status{State: xiaomi.StateIdle})
	waitFor(t, "second song", func() bool { return strings.Contains(d.history(), "push song2") })
	if c.State() != StatePlaying {
		t.Fatalf("state = %s", c.State())
	}
}

func TestSpeechDuringMusicResumesTheSameSong(t *testing.T) {
	c, d := start(t, nil)
	c.Command(Command{Kind: CmdPlay, Queue: &fakeQueue{}})
	waitFor(t, "song", func() bool { return strings.Contains(d.history(), "push song1") })
	d.setStatus(xiaomi.Status{State: xiaomi.StatePlaying, PositionMS: 60_000, DurationMS: 200_000})
	say(t, c, Speech{URL: "clip1", Text: "主人。", Duration: clipLen})
	waitFor(t, "resume", func() bool {
		return strings.HasSuffix(d.history(), "push clip1 | loop 2 | push song1")
	})
	if strings.Contains(d.history(), "song2") {
		t.Fatalf("speech must not skip the song: %s", d.history())
	}
}

func TestNativePauseKeepsSessionThenTimesOut(t *testing.T) {
	c, d := start(t, nil)
	c.Command(Command{Kind: CmdPlay, Queue: &fakeQueue{}})
	waitFor(t, "song", func() bool { return strings.Contains(d.history(), "push song1") })
	d.setStatus(xiaomi.Status{State: xiaomi.StatePlaying, PositionMS: 30_000, DurationMS: 200_000})
	time.Sleep(15 * time.Millisecond)
	c.Command(Command{Kind: CmdHeardPause})
	d.setStatus(xiaomi.Status{State: xiaomi.StatePaused, PositionMS: 30_000, DurationMS: 200_000})
	waitFor(t, "user-paused", func() bool { return c.State() == StateUserPaused })
	waitFor(t, "session ends after pause timeout", func() bool { return c.State() == StateIdle })
	if strings.Contains(d.history(), "song2") {
		t.Fatalf("pause must not advance: %s", d.history())
	}
}

func TestNativeResumeContinuesSession(t *testing.T) {
	c, d := start(t, nil)
	c.Command(Command{Kind: CmdPlay, Queue: &fakeQueue{}})
	waitFor(t, "song", func() bool { return strings.Contains(d.history(), "push song1") })
	c.Command(Command{Kind: CmdHeardPause})
	waitFor(t, "user-paused", func() bool { return c.State() == StateUserPaused })
	c.Command(Command{Kind: CmdHeardResume})
	waitFor(t, "playing", func() bool { return c.State() == StatePlaying })
}

func TestStopEndsSessionForGood(t *testing.T) {
	c, d := start(t, nil)
	c.Command(Command{Kind: CmdPlay, Queue: &fakeQueue{}})
	waitFor(t, "song", func() bool { return strings.Contains(d.history(), "push song1") })
	c.Command(Command{Kind: CmdStop, FromVoice: true})
	waitFor(t, "idle", func() bool { return c.State() == StateIdle })
	d.setStatus(xiaomi.Status{State: xiaomi.StateIdle})
	time.Sleep(30 * time.Millisecond)
	if strings.Contains(d.history(), "song2") {
		t.Fatalf("stopped session advanced: %s", d.history())
	}
}

func TestVoicePlayCutsXiaoAiOnceAndPushesAtOnce(t *testing.T) {
	c, d := start(t, nil)
	c.Command(Command{Kind: CmdPlay, Queue: &fakeQueue{}, FromVoice: true})
	waitFor(t, "song", func() bool { return strings.Contains(d.history(), "push song1") })
	want := "control pause | push song1"
	if d.history() != want {
		t.Fatalf("calls = %s", d.history())
	}
}

func TestQueueRunsOut(t *testing.T) {
	c, d := start(t, nil)
	c.Command(Command{Kind: CmdPlay, Queue: &fakeQueue{limit: 1}})
	waitFor(t, "song", func() bool { return strings.Contains(d.history(), "push song1") })
	d.setStatus(xiaomi.Status{State: xiaomi.StatePlaying, PositionMS: 195_000, DurationMS: 200_000})
	time.Sleep(15 * time.Millisecond)
	d.setStatus(xiaomi.Status{State: xiaomi.StateIdle})
	waitFor(t, "idle", func() bool { return c.State() == StateIdle })
}

func TestNativeOnlyModeNeverTouchesThePlayer(t *testing.T) {
	d := &fakeDevice{status: xiaomi.Status{State: xiaomi.StateIdle, Volume: 30}}
	c := New(d, Options{NativeOnly: true, SpeechPad: time.Millisecond, Poll: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go c.Run(ctx)
	o := say(t, c, Speech{URL: "clip1", Text: "主人。", Duration: time.Millisecond})
	if o.Voice != VoiceNative || d.history() != "native 主人。" {
		t.Fatalf("outcome=%+v calls=%s", o, d.history())
	}
}

func TestPlayingTitleIsVisible(t *testing.T) {
	c, d := start(t, nil)
	c.Command(Command{Kind: CmdPlay, Queue: &fakeQueue{}})
	waitFor(t, "song", func() bool { return strings.Contains(d.history(), "push song1") })
	waitFor(t, "title", func() bool { return c.Playing() == "t" })
	c.Command(Command{Kind: CmdStop})
	waitFor(t, "no title", func() bool { return c.Playing() == "" })
}

func TestPanelStopSilencesEvenWhenIdle(t *testing.T) {
	// A stuck clip or Xiao Ai's own music: the panel's stop must reach the speaker.
	c, d := start(t, nil)
	c.Command(Command{Kind: CmdStop})
	waitFor(t, "stop sent", func() bool { return d.history() == "control stop" })
}

// The OH2P reports position 0 for an external mp3 the whole time (11-14), so
// a song ends by the clock: playback started at its fetch, plus its length.
func TestSongEndsByTheClockWhenPositionNeverMoves(t *testing.T) {
	c, d := start(t, nil)
	d.setStatus(xiaomi.Status{State: xiaomi.StatePlaying}) // what the OH2P says, forever
	c.Command(Command{Kind: CmdPlay, Queue: &fakeQueue{dur: 30 * time.Millisecond}})
	waitFor(t, "second song", func() bool { return strings.Contains(d.history(), "push song2") })
}

// A song the speaker never fetched did not play (02:38: nothing came out,
// the panel said "playing" for half an hour). It counts as unplayable.
func TestSongNeverFetchedIsSkipped(t *testing.T) {
	c, d := start(t, []step{}) // the speaker fetches nothing
	c.Command(Command{Kind: CmdPlay, Queue: &fakeQueue{limit: 2}})
	waitFor(t, "session over", func() bool {
		return strings.Contains(d.history(), "push song2") && c.State() == StateIdle
	})
	if c.Playing() != "" {
		t.Fatalf("still shows %q", c.Playing())
	}
}

// When the PC's Bluetooth takes the speaker, our song is gone for good
// (03:20, real device): the session ends instead of pretending, and a line
// spoken later is not followed by a stale song.
func TestBluetoothTakingTheSpeakerEndsTheSession(t *testing.T) {
	c, d := start(t, nil)
	c.Command(Command{Kind: CmdPlay, Queue: &fakeQueue{dur: time.Hour}})
	waitFor(t, "song", func() bool { return c.State() == StatePlaying })
	d.setBT(true)
	waitFor(t, "session over", func() bool { return c.State() == StateIdle })
	d.setBT(false)
	say(t, c, Speech{URL: "clip1", Text: "主人。", Duration: clipLen})
	time.Sleep(30 * time.Millisecond)
	if strings.Count(d.history(), "push song1") != 1 {
		t.Fatalf("stale song pushed again: %s", d.history())
	}
}

func TestDoneIsClosedAfterTheInterrupt(t *testing.T) {
	c, d := start(t, nil)
	done := make(chan struct{})
	c.Command(Command{Kind: CmdStop, FromVoice: true, Done: done})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("never done")
	}
	if d.history() != "control pause | control stop | control pause | control stop" {
		t.Fatalf("calls = %s", d.history())
	}
}

func TestHushedIsClosedAfterTheFirstRound(t *testing.T) {
	c, d := start(t, nil)
	hushed, done := make(chan struct{}), make(chan struct{})
	c.Command(Command{Kind: CmdStop, FromVoice: true, Hushed: hushed, Done: done})
	<-hushed
	if got := d.history(); !strings.HasPrefix(got, "control pause") || strings.Count(got, "pause") != 1 {
		t.Fatalf("at hushed: %s", got)
	}
	<-done
	if got := d.history(); got != "control pause | control stop | control pause | control stop" {
		t.Fatalf("at done: %s", got)
	}
}

func TestPanelResumePushesTheSongAgain(t *testing.T) {
	c, d := start(t, nil)
	c.Command(Command{Kind: CmdPlay, Queue: &fakeQueue{dur: time.Hour}})
	waitFor(t, "song", func() bool { return strings.Contains(d.history(), "push song1") })
	c.Command(Command{Kind: CmdPause})
	waitFor(t, "paused", func() bool { return c.State() == StateUserPaused })
	c.Command(Command{Kind: CmdResume})
	waitFor(t, "resumed", func() bool { return c.State() == StatePlaying })
	if h := d.history(); !strings.HasSuffix(h, "control pause | push song1") {
		t.Fatalf("calls = %s", h)
	}
}

func TestIncompleteAudioProbeFallsBack(t *testing.T) {
	c, _ := start(t, []step{{time.Millisecond, true}, {2 * time.Millisecond, false}})
	out := say(t, c, Speech{Text: "好", URL: "speech", Duration: clipLen})
	if out.Voice != VoiceNative || out.Err != nil {
		t.Fatalf("expected native fallback after incomplete probe, got %+v", out)
	}
}
