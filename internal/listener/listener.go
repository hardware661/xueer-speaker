// Package listener reads the speaker's conversation log and hands each new
// sentence the owner said to a callback, exactly once.
//
// The algorithm combines what the comparable projects learnt
// (docs/research/competitors-2026-11.md §3.4):
//   - the first successful read only sets a baseline; history is never replayed;
//   - the cursor is the server's timestamp, never the local clock;
//   - a failed read changes nothing and backs off (up to 30 s);
//   - two records are read to see whether more arrived; if so, older pages
//     are fetched until the cursor is reached, then delivered oldest first;
//   - requestIds seen recently are remembered so a re-stamped record is not
//     delivered twice;
//   - lines we read aloud ourselves in the last 30 s are ignored.
//
// Only Query (what the owner said) is looked at, never Xiao Ai's answers.
package listener

import (
	"context"
	"sync"
	"time"

	"github.com/hardware661/xueer-speaker/internal/xiaomi"
)

// Log is the conversation source (xiaomi.Speaker).
type Log interface {
	Conversations(ctx context.Context, limit int, beforeMS int64) ([]xiaomi.Record, error)
}

const (
	pageSize    = 10
	maxPages    = 3
	rememberIDs = 1000
	ownWindow   = 30 * time.Second
	maxBackoff  = 30 * time.Second
)

// Listener is driven by Poll; it is not goroutine-safe except for Said.
type Listener struct {
	log     Log
	deliver func(xiaomi.Record)
	now     func() time.Time

	ready   bool
	cursor  int64 // newest server time seen
	seen    map[string]struct{}
	order   []string
	backoff time.Duration

	mu    sync.Mutex
	own   []ownLine
	reads []readResult // last healthWindow of reads, for the panel's light
}

type readResult struct {
	at  time.Time
	err string
}

// healthWindow is how far back Health looks.
const healthWindow = 5 * time.Minute

// Health is how reading the conversation log went lately (the panel shows
// a light: it can fail for hours without anyone noticing otherwise).
type Health struct {
	Reads    int    `json:"reads"`
	Failures int    `json:"failures"`
	LastErr  string `json:"last_err,omitempty"`
}

// Health is safe to call from anywhere.
func (l *Listener) Health() Health {
	l.mu.Lock()
	defer l.mu.Unlock()
	var h Health
	for _, r := range l.reads {
		if l.now().Sub(r.at) > healthWindow {
			continue
		}
		h.Reads++
		if r.err != "" {
			h.Failures++
			h.LastErr = r.err
		}
	}
	return h
}

func (l *Listener) note(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := readResult{at: l.now()}
	if err != nil {
		r.err = err.Error()
	}
	kept := l.reads[:0]
	for _, x := range l.reads {
		if r.at.Sub(x.at) <= healthWindow {
			kept = append(kept, x)
		}
	}
	l.reads = append(kept, r)
}

// Pace is how long to wait between reads: once a second while our music
// plays or the owner spoke in the last half minute (a "next" may follow),
// every two seconds otherwise.
func Pace(music bool, lastHeard, now time.Time) time.Duration {
	if music || (!lastHeard.IsZero() && now.Sub(lastHeard) < 30*time.Second) {
		return time.Second
	}
	return 2 * time.Second
}

type ownLine struct {
	text string
	at   time.Time
}

// New returns a listener calling deliver for each new record.
func New(log Log, deliver func(xiaomi.Record)) *Listener {
	return &Listener{log: log, deliver: deliver, now: time.Now, seen: map[string]struct{}{}}
}

// Deliver sets the callback when it could not be given to New.
func (l *Listener) Deliver(f func(xiaomi.Record)) { l.deliver = f }

// Said records a line we just made the speaker say.
func (l *Listener) Said(text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.own = append(l.own, ownLine{normalize(text), now})
	kept := l.own[:0]
	for _, o := range l.own {
		if now.Sub(o.at) < ownWindow {
			kept = append(kept, o)
		}
	}
	l.own = kept
}

func (l *Listener) isOwn(query string) bool {
	q := normalize(query)
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, o := range l.own {
		if o.text == q && l.now().Sub(o.at) < ownWindow {
			return true
		}
	}
	return false
}

// Poll reads the log once. It returns 0 after a success, or how long the
// caller should wait before the next try after a failure.
func (l *Listener) Poll(ctx context.Context) time.Duration {
	recs, err := l.log.Conversations(ctx, 2, 0)
	l.note(err)
	if err != nil {
		l.backoff = min(max(l.backoff*2, time.Second), maxBackoff)
		return l.backoff
	}
	l.backoff = 0
	if !l.ready {
		for _, r := range recs {
			l.cursor = max(l.cursor, r.TimeMS)
			l.remember(r.RequestID)
		}
		l.ready = true
		return 0
	}
	fresh := newer(recs, l.cursor)
	if len(fresh) == len(recs) && len(recs) > 0 {
		// Both are new: there may be more between them and the cursor.
		before := recs[len(recs)-1].TimeMS
		for range maxPages {
			page, err := l.log.Conversations(ctx, pageSize, before)
			if err != nil || len(page) == 0 {
				break
			}
			more := newer(page, l.cursor)
			fresh = append(fresh, more...)
			if len(more) < len(page) {
				break
			}
			before = page[len(page)-1].TimeMS
		}
	}
	for i := len(fresh) - 1; i >= 0; i-- { // oldest first
		r := fresh[i]
		l.cursor = max(l.cursor, r.TimeMS)
		if l.known(r.RequestID) {
			continue
		}
		l.remember(r.RequestID)
		if r.Query == "" || l.isOwn(r.Query) {
			continue
		}
		l.deliver(r)
	}
	return 0
}

func newer(recs []xiaomi.Record, cursor int64) []xiaomi.Record {
	var out []xiaomi.Record
	for _, r := range recs {
		if r.TimeMS > cursor {
			out = append(out, r)
		}
	}
	return out
}

func (l *Listener) known(id string) bool {
	if id == "" {
		return false
	}
	_, ok := l.seen[id]
	return ok
}

func (l *Listener) remember(id string) {
	if id == "" {
		return
	}
	if _, ok := l.seen[id]; ok {
		return
	}
	l.seen[id] = struct{}{}
	l.order = append(l.order, id)
	if len(l.order) > rememberIDs {
		delete(l.seen, l.order[0])
		l.order = l.order[1:]
	}
}
