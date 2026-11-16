package listener

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hardware661/xueer-speaker/internal/xiaomi"
)

// fakeLog is the speaker's conversation log, newest first.
type fakeLog struct {
	mu    sync.Mutex
	recs  []xiaomi.Record
	fail  bool
	pages []int64 // "before" values asked for
}

func (f *fakeLog) Conversations(_ context.Context, limit int, beforeMS int64) ([]xiaomi.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pages = append(f.pages, beforeMS)
	if f.fail {
		return nil, errors.New("network")
	}
	var out []xiaomi.Record
	for _, r := range f.recs {
		if beforeMS > 0 && r.TimeMS >= beforeMS {
			continue
		}
		out = append(out, r)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (f *fakeLog) add(t int64, q, id string) {
	f.mu.Lock()
	f.recs = append([]xiaomi.Record{{TimeMS: t, Query: q, RequestID: id}}, f.recs...)
	f.mu.Unlock()
}

func newL(log *fakeLog) (*Listener, *[]string) {
	var got []string
	l := New(log, func(r xiaomi.Record) { got = append(got, r.Query) })
	return l, &got
}

func TestFirstPollOnlySetsBaseline(t *testing.T) {
	log := &fakeLog{}
	log.add(1000, "旧的话", "r1")
	l, got := newL(log)
	l.Poll(context.Background())
	if len(*got) != 0 {
		t.Fatalf("history replayed: %v", *got)
	}
	log.add(2000, "雪儿放歌", "r2")
	l.Poll(context.Background())
	if len(*got) != 1 || (*got)[0] != "雪儿放歌" {
		t.Fatalf("got %v", *got)
	}
}

func TestManyNewRecordsDeliveredOldestFirstViaPaging(t *testing.T) {
	log := &fakeLog{}
	log.add(1000, "基线", "r0")
	l, got := newL(log)
	l.Poll(context.Background())
	for i := 1; i <= 5; i++ {
		log.add(int64(1000+i*100), string(rune('a'+i-1)), "r"+string(rune('0'+i)))
	}
	l.Poll(context.Background())
	if want := "abcde"; join(*got) != want {
		t.Fatalf("got %q want %q", join(*got), want)
	}
}

func TestSameRequestIDNeverTwice(t *testing.T) {
	log := &fakeLog{}
	l, got := newL(log)
	l.Poll(context.Background())
	log.add(2000, "雪儿放歌", "r2")
	l.Poll(context.Background())
	// Xiaomi sometimes re-stamps a record; same requestId must not repeat.
	log.add(2500, "雪儿放歌", "r2")
	l.Poll(context.Background())
	if len(*got) != 1 {
		t.Fatalf("got %v", *got)
	}
}

func TestFailureDoesNotMoveBaselineAndBacksOff(t *testing.T) {
	log := &fakeLog{fail: true}
	l, got := newL(log)
	d1 := l.Poll(context.Background())
	d2 := l.Poll(context.Background())
	if d2 <= d1 {
		t.Fatalf("no backoff: %v then %v", d1, d2)
	}
	log.fail = false
	log.add(1000, "一直都在的旧话", "r1")
	l.Poll(context.Background()) // first success = baseline, must not replay
	if len(*got) != 0 {
		t.Fatalf("history replayed after failures: %v", *got)
	}
}

func TestOwnWordsAreIgnored(t *testing.T) {
	log := &fakeLog{}
	l, got := newL(log)
	l.Poll(context.Background())
	l.Said("雪儿放歌") // a line we just read aloud (AIs often start with 雪儿)
	log.add(2000, "雪儿放歌", "r2")
	l.Poll(context.Background())
	if len(*got) != 0 {
		t.Fatalf("own words treated as command: %v", *got)
	}
}

func join(s []string) string {
	out := ""
	for _, x := range s {
		out += x
	}
	return out
}

// polling once a second around the clock drew errors from Xiaomi
// (11-15 22:00 → 01:07, 140 failures). Once a second only while it matters.
func TestPaceIsFastOnlyWhileSomethingIsGoingOn(t *testing.T) {
	now := time.Date(2026, 11, 16, 2, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		music     bool
		lastHeard time.Time
		want      time.Duration
	}{
		{"idle", false, time.Time{}, 2 * time.Second},
		{"our music plays", true, time.Time{}, time.Second},
		{"the owner just spoke", false, now.Add(-20 * time.Second), time.Second},
		{"spoke a while ago", false, now.Add(-2 * time.Minute), 2 * time.Second},
	}
	for _, c := range cases {
		if got := Pace(c.music, c.lastHeard, now); got != c.want {
			t.Fatalf("%s: %v", c.name, got)
		}
	}
}

func TestHealthCountsRecentFailuresAndKeepsTheReason(t *testing.T) {
	log := &fakeLog{}
	l, _ := newL(log)
	clk := time.Date(2026, 11, 16, 2, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return clk }
	for range 8 {
		l.Poll(context.Background())
	}
	log.fail = true
	for range 2 {
		l.Poll(context.Background())
	}
	h := l.Health()
	if h.Reads != 10 || h.Failures != 2 || h.LastErr != "network" {
		t.Fatalf("health %+v", h)
	}
	clk = clk.Add(6 * time.Minute)
	if h := l.Health(); h.Reads != 0 || h.Failures != 0 {
		t.Fatalf("old reads must age out: %+v", h)
	}
}
