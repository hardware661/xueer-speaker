package announcer

import (
	"context"
	"errors"
	"github.com/hardware661/xueer-speaker/internal/control"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hardware661/xueer-speaker/internal/conductor"
	"github.com/hardware661/xueer-speaker/internal/voice"
)

type fakeRenderer struct{ fail error }

func (f fakeRenderer) Render(_ context.Context, text string) (voice.Clip, error) {
	if f.fail != nil {
		return voice.Clip{}, f.fail
	}
	return voice.Clip{Path: "/tmp/" + text + ".mp3", Duration: time.Second, Text: text}, nil
}

type fakePublisher struct{}

func (fakePublisher) Speech(file string) string { return "https://s/a/" + file }

type fakeSpeaker struct{ got []conductor.Speech }

func (f *fakeSpeaker) Speak(sp conductor.Speech) <-chan conductor.Outcome {
	f.got = append(f.got, sp)
	ch := make(chan conductor.Outcome, 1)
	ch <- conductor.Outcome{Voice: conductor.VoiceEdge}
	return ch
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newA(r Renderer) (*Announcer, *fakeSpeaker, *clock) {
	sp := &fakeSpeaker{}
	c := &clock{t: time.Date(2026, 11, 14, 21, 0, 0, 0, time.UTC)}
	return New(r, fakePublisher{}, sp, c.now), sp, c
}

func TestSayRendersPublishesAndSpeaks(t *testing.T) {
	a, sp, _ := newA(fakeRenderer{})
	res, err := a.Say(context.Background(), Request{Text: "主人。", Source: "claude", Volume: 70})
	if err != nil {
		t.Fatal(err)
	}
	if res.ID == "" || len(sp.got) != 1 || sp.got[0].URL != "https://s/a//tmp/主人。.mp3" || sp.got[0].Volume != 70 || sp.got[0].Text != "主人。" {
		t.Fatalf("res=%+v got=%+v", res, sp.got)
	}
}

func TestTooLongAndEmptyAreCallerErrors(t *testing.T) {
	a, _, _ := newA(fakeRenderer{fail: voice.ErrTooLong})
	if _, err := a.Say(context.Background(), Request{Text: "x", Source: "claude"}); !errors.Is(err, voice.ErrTooLong) {
		t.Fatalf("err = %v", err)
	}
}

func TestEdgeDownStillSpeaksNatively(t *testing.T) {
	a, sp, _ := newA(fakeRenderer{fail: errors.New("edge 403")})
	if _, err := a.Say(context.Background(), Request{Text: "主人。", Source: "codex"}); err != nil {
		t.Fatal(err)
	}
	if sp.got[0].URL != "" || sp.got[0].Text != "主人。" {
		t.Fatalf("want native-only speech, got %+v", sp.got[0])
	}
}

func TestSameTextFromSameSourceWithin10sIsDropped(t *testing.T) {
	a, sp, c := newA(fakeRenderer{})
	ctx := context.Background()
	a.Say(ctx, Request{Text: "主人。", Source: "claude"})
	if _, err := a.Say(ctx, Request{Text: "主人。", Source: "claude"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("err = %v", err)
	}
	a.Say(ctx, Request{Text: "主人。", Source: "codex"}) // another source is fine
	c.t = c.t.Add(11 * time.Second)
	if _, err := a.Say(ctx, Request{Text: "主人。", Source: "claude"}); err != nil {
		t.Fatalf("after 10 s: %v", err)
	}
	if len(sp.got) != 3 {
		t.Fatalf("spoken %d", len(sp.got))
	}
}

func TestRateLimits(t *testing.T) {
	a, _, c := newA(fakeRenderer{})
	ctx := context.Background()
	for i := range 6 {
		if _, err := a.Say(ctx, Request{Text: string(rune('a' + i)), Source: "claude"}); err != nil {
			t.Fatalf("#%d: %v", i, err)
		}
	}
	if _, err := a.Say(ctx, Request{Text: "z", Source: "claude"}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("7th from one source: %v", err)
	}
	for i := range 6 {
		a.Say(ctx, Request{Text: string(rune('a' + i)), Source: "codex"})
	}
	if _, err := a.Say(ctx, Request{Text: "y", Source: "grok"}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("13th overall: %v", err)
	}
	c.t = c.t.Add(61 * time.Second)
	if _, err := a.Say(ctx, Request{Text: "z", Source: "claude"}); err != nil {
		t.Fatalf("after a minute: %v", err)
	}
}

func TestHistoryKeepsRecentLines(t *testing.T) {
	a, _, _ := newA(fakeRenderer{})
	a.Say(context.Background(), Request{Text: "主人。", Source: "claude"})
	var h []Entry
	for range 200 { // the outcome is filled in asynchronously
		if h = a.History(10); len(h) == 1 && h[0].Voice != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if len(h) != 1 || h[0].Source != "claude" || h[0].Text != "主人。" || h[0].Voice != "edge" || h[0].Err != "" {
		t.Fatalf("history = %+v", h)
	}
}

type fakeRecorder struct {
	mu   sync.Mutex
	adds []Entry
	done map[string]string
}

func (f *fakeRecorder) Queued(e Entry) {
	f.mu.Lock()
	f.adds = append(f.adds, e)
	f.mu.Unlock()
}
func (f *fakeRecorder) Spoken(e Entry) {
	f.mu.Lock()
	if f.done == nil {
		f.done = map[string]string{}
	}
	f.done[e.ID] = e.Voice
	f.mu.Unlock()
}

func TestRecorderSeesQueuedAndSpoken(t *testing.T) {
	a, _, _ := newA(fakeRenderer{})
	rec := &fakeRecorder{}
	a.Record(rec)
	res, _ := a.Say(context.Background(), Request{Text: "主人。", Source: "claude"})
	for range 200 {
		rec.mu.Lock()
		v := rec.done[res.ID]
		rec.mu.Unlock()
		if v != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.adds) != 1 || rec.adds[0].ID != res.ID || rec.adds[0].Text != "主人。" || rec.done[res.ID] != "edge" {
		t.Fatalf("adds=%+v done=%v", rec.adds, rec.done)
	}
}

func TestNativeRequestSkipsXiaoxiao(t *testing.T) {
	a, sp, _ := newA(fakeRenderer{fail: errors.New("must not render")})
	if _, err := a.Say(context.Background(), Request{Text: "测试。", Source: "panel", Native: true}); err != nil {
		t.Fatal(err)
	}
	if sp.got[0].URL != "" {
		t.Fatalf("native request rendered a clip: %+v", sp.got[0])
	}
}

func TestNativeRequestStillRefusesLongText(t *testing.T) {
	a, _, _ := newA(fakeRenderer{})
	long := strings.Repeat("好", voice.MaxRunes+1)
	if _, err := a.Say(context.Background(), Request{Text: long, Source: "panel", Native: true}); !errors.Is(err, voice.ErrTooLong) {
		t.Fatalf("err = %v", err)
	}
}

// Do-not-disturb: the line is queued and recorded, never sent to the
// speaker, and counted so the morning report can say how many.
func TestQuietKeepsLinesWithoutSpeaking(t *testing.T) {
	a, sp, c := newA(fakeRenderer{})
	q := &control.Quiet{Now: c.now}
	a.Quiet = q
	q.Start(c.t.Add(8 * time.Hour))
	rec := &fakeRecorder{}
	a.Record(rec)
	if _, err := a.Say(context.Background(), Request{Text: "主人，做完了。", Source: "claude"}); err != nil {
		t.Fatal(err)
	}
	if len(sp.got) != 0 {
		t.Fatalf("spoken while quiet: %+v", sp.got)
	}
	if len(rec.adds) != 1 || len(rec.done) != 1 || rec.done[rec.adds[0].ID] != "quiet" {
		t.Fatalf("recorded = %+v %+v", rec.adds, rec.done)
	}
	if n := q.Stop(); n != 1 {
		t.Fatalf("held = %d", n)
	}
	// Loud lines (the morning report) go through even while quiet.
	q.Start(c.t.Add(time.Hour))
	if _, err := a.Say(context.Background(), Request{Text: "早上好。", Source: "dnd", Loud: true}); err != nil || len(sp.got) != 1 {
		t.Fatalf("loud line: %v %d", err, len(sp.got))
	}
}
