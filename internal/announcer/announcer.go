// Package announcer turns "say this" requests into speech: it rate-limits and
// de-duplicates, renders Xiaoxiao's voice, publishes the clip under /a/ and
// hands it to the conductor. Say returns as soon as the line is queued (v5
// §5.1: callers never wait for the speaker).
package announcer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/hardware661/xueer-speaker/internal/conductor"
	"github.com/hardware661/xueer-speaker/internal/control"
	"github.com/hardware661/xueer-speaker/internal/voice"
)

// Limits.
const (
	perSourcePerMinute = 6
	totalPerMinute     = 12
	dedupWindow        = 10 * time.Second
	historySize        = 200
)

var errNative = errors.New("native voice asked for")

// VoiceQuiet is the Entry voice of a line kept during do-not-disturb.
const VoiceQuiet = "quiet"

var (
	ErrRateLimited = errors.New("announcer: too many lines, try later")
	ErrDuplicate   = errors.New("announcer: same line said moments ago")
)

// Renderer makes a clip (voice.Voice).
type Renderer interface {
	Render(ctx context.Context, text string) (voice.Clip, error)
}

// Publisher exposes a clip file as an /a/ link (audio.Host).
type Publisher interface {
	Speech(file string) string
}

// Speaker plays speech (conductor.Conductor).
type Speaker interface {
	Speak(conductor.Speech) <-chan conductor.Outcome
}

// Request is one line to say.
type Request struct {
	Text   string
	Source string // claude / codex / grok / bot:alert / panel / cli …
	Volume int
	Native bool // Xiao Ai's own voice (panel / CLI test)
	Loud   bool // speaks even during do-not-disturb (the morning report)
}

// Recorder keeps the history somewhere durable (store). Spoken is called from
// another goroutine once the speaker is done.
type Recorder interface {
	Queued(Entry)
	Spoken(Entry)
}

// Result identifies a queued line.
type Result struct {
	ID string
}

// Entry is one line in the history shown on the panel.
type Entry struct {
	ID     string    `json:"id"`
	At     time.Time `json:"at"`
	Source string    `json:"source"`
	Text   string    `json:"text"`
	Voice  string    `json:"voice"` // edge / native / "" while queued
	Err    string    `json:"err,omitempty"`
}

// Announcer is safe for concurrent use.
type Announcer struct {
	// Quiet, when set and on, keeps lines (recorded, voice "quiet") instead
	// of speaking them.
	Quiet *control.Quiet

	r   Renderer
	pub Publisher
	sp  Speaker
	now func() time.Time

	mu      sync.Mutex
	sent    []sentLine // last minute
	history []*Entry
	rec     Recorder
}

type sentLine struct {
	at     time.Time
	source string
	text   string
}

// New returns an Announcer.
func New(r Renderer, pub Publisher, sp Speaker, now func() time.Time) *Announcer {
	if now == nil {
		now = time.Now
	}
	return &Announcer{r: r, pub: pub, sp: sp, now: now}
}

// Record sends every line to r as well; call before the first Say.
func (a *Announcer) Record(r Recorder) { a.rec = r }

// Say checks limits, renders and queues the line.
func (a *Announcer) Say(ctx context.Context, req Request) (Result, error) {
	text := voice.Clean(req.Text)
	if err := a.admit(req.Source, text); err != nil {
		return Result{}, err
	}
	if a.Quiet != nil && !req.Loud && a.Quiet.On() {
		if text == "" {
			a.forget(req.Source, text)
			return Result{}, voice.ErrEmpty
		}
		if utf8.RuneCountInString(text) > voice.MaxRunes {
			a.forget(req.Source, text)
			return Result{}, voice.ErrTooLong
		}
		a.Quiet.Hold()
		e := Entry{ID: newID(), At: a.now(), Source: req.Source, Text: text, Voice: VoiceQuiet}
		a.mu.Lock()
		a.history = append(a.history, &e)
		a.mu.Unlock()
		if a.rec != nil {
			q := e
			q.Voice = ""
			a.rec.Queued(q)
			a.rec.Spoken(e)
		}
		return Result{ID: e.ID}, nil
	}
	sp := conductor.Speech{Text: text, Volume: req.Volume}
	var clip voice.Clip
	var err error
	if req.Native {
		err = errNative
		if utf8.RuneCountInString(text) > voice.MaxRunes {
			err = voice.ErrTooLong
		} else if text == "" {
			err = voice.ErrEmpty
		}
	} else {
		clip, err = a.r.Render(ctx, req.Text)
	}
	switch {
	case errors.Is(err, voice.ErrTooLong), errors.Is(err, voice.ErrEmpty):
		a.forget(req.Source, text)
		return Result{}, err
	case errors.Is(err, errNative):
	case err != nil:
		// Edge is down: Xiao Ai's own voice still calls the owner.
	default:
		sp.URL, sp.Duration = a.pub.Speech(clip.Path), clip.Duration
	}
	e := &Entry{ID: newID(), At: a.now(), Source: req.Source, Text: text}
	a.mu.Lock()
	a.history = append(a.history, e)
	if len(a.history) > historySize {
		a.history = a.history[len(a.history)-historySize:]
	}
	queued := *e
	a.mu.Unlock()
	if a.rec != nil {
		a.rec.Queued(queued)
	}
	done := a.sp.Speak(sp)
	go func() {
		o := <-done
		a.mu.Lock()
		e.Voice = string(o.Voice)
		if o.Err != nil {
			e.Err = o.Err.Error()
		}
		spoken := *e
		a.mu.Unlock()
		if a.rec != nil {
			a.rec.Spoken(spoken)
		}
	}()
	return Result{ID: e.ID}, nil
}

func (a *Announcer) admit(source, text string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	kept := a.sent[:0]
	for _, s := range a.sent {
		if now.Sub(s.at) < time.Minute {
			kept = append(kept, s)
		}
	}
	a.sent = kept
	mine := 0
	for _, s := range a.sent {
		if s.source == source {
			mine++
			if s.text == text && now.Sub(s.at) < dedupWindow {
				return ErrDuplicate
			}
		}
	}
	if mine >= perSourcePerMinute || len(a.sent) >= totalPerMinute {
		return ErrRateLimited
	}
	a.sent = append(a.sent, sentLine{at: now, source: source, text: text})
	return nil
}

// forget undoes admit for a line the caller must rephrase.
func (a *Announcer) forget(source, text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.sent) - 1; i >= 0; i-- {
		if a.sent[i].source == source && a.sent[i].text == text {
			a.sent = slices.Delete(a.sent, i, i+1)
			return
		}
	}
}

// History returns up to n most recent lines, newest first.
func (a *Announcer) History(n int) []Entry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Entry, 0, n)
	for i := len(a.history) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, *a.history[i])
	}
	return out
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
