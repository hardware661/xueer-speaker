// Package pcmouth is the PC as a mouth. A small resident program on the PC
// keeps one listen stream open; while it is open every line plays on the PC, and the speaker is only
// used when it is not. The PC fetches the clip from /a/ itself and answers
// with done.
package pcmouth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hardware661/xueer-speaker/internal/conductor"
)

var (
	ErrOffline = errors.New("pcmouth: PC not connected")
	ErrTimeout = errors.New("pcmouth: PC did not finish the line in time")
)

// Job is one thing for the PC to do: say a line (Kind "say", URL) or set
// the system volume (Kind "volume", Volume 0–100).
type Job struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	URL    string `json:"url,omitempty"`
	Volume int    `json:"volume,omitempty"`
}

// State is what the PC last reported about itself.
type State struct {
	Volume int `json:"volume"` // Windows master volume, 0–100
}

type conn struct {
	jobs chan Job
	gone chan struct{}
	once sync.Once
}

func (c *conn) close() { c.once.Do(func() { close(c.gone) }) }

// Hub is safe for concurrent use.
type Hub struct {
	Grace time.Duration // on top of the clip length before giving up (10 s)
	Ping  time.Duration // keep-alive on the listen stream (15 s)

	key  string
	play sync.Mutex // one line at a time, like the speaker

	mu      sync.Mutex
	cur     *conn
	state   *State
	waiting map[string]chan string
}

// New returns a Hub accepting the PC with key.
func New(key string) *Hub {
	return &Hub{Grace: 10 * time.Second, Ping: 15 * time.Second, key: key, waiting: map[string]chan string{}}
}

// Online reports whether the PC is connected.
func (h *Hub) Online() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cur != nil
}

// State is the PC's last report; false while it is not connected or has
// not reported yet.
func (h *Hub) State() (State, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.cur == nil || h.state == nil {
		return State{}, false
	}
	return *h.state, true
}

// SetVolume sets the PC's system volume. It does not wait behind a line
// being said.
func (h *Hub) SetVolume(ctx context.Context, v int) error {
	err := h.run(ctx, Job{Kind: "volume", Volume: v}, 5*time.Second)
	if err == nil {
		h.mu.Lock()
		h.state = &State{Volume: v}
		h.mu.Unlock()
	}
	return err
}

// Play sends url to the PC and waits until it has played it (length is the
// clip duration). A PC that does not answer is dropped so the next line goes
// to the speaker straight away.
func (h *Hub) Play(ctx context.Context, url string, length time.Duration) error {
	h.play.Lock()
	defer h.play.Unlock()
	return h.run(ctx, Job{Kind: "say", URL: url}, length+h.Grace)
}

// run sends j and waits for the PC's answer for at most limit.
func (h *Hub) run(ctx context.Context, j Job, limit time.Duration) error {
	h.mu.Lock()
	c := h.cur
	if c == nil {
		h.mu.Unlock()
		return ErrOffline
	}
	j.ID = newID()
	done := make(chan string, 1)
	h.waiting[j.ID] = done
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.waiting, j.ID)
		h.mu.Unlock()
	}()
	t := time.NewTimer(limit)
	defer t.Stop()
	select {
	case c.jobs <- j:
	case <-c.gone:
		return ErrOffline
	case <-t.C:
		h.drop(c)
		return ErrTimeout
	}
	// The PC has the job. It finishes a line even if the stream drops (a
	// release, a network blip) and reports on its next connection, so wait
	// for done rather than giving the line to the speaker too (heard twice).
	select {
	case e := <-done:
		if e != "" {
			return fmt.Errorf("pcmouth: %s", e)
		}
		return nil
	case <-t.C:
		h.drop(c)
		return ErrTimeout
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Hub) drop(c *conn) {
	h.mu.Lock()
	if h.cur == c {
		h.cur, h.state = nil, nil
	}
	h.mu.Unlock()
	c.close()
}

// Handler serves GET /pc/listen, POST /pc/done and POST /pc/state
// (bearer = the PC key).
func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /pc/listen", h.auth(h.listen))
	mux.HandleFunc("POST /pc/done", h.auth(h.done))
	mux.HandleFunc("POST /pc/state", h.auth(h.report))
	return mux
}

func (h *Hub) report(w http.ResponseWriter, r *http.Request) {
	var st State
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&st); err != nil || st.Volume < 0 || st.Volume > 100 {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	if h.cur != nil {
		h.state = &st
	}
	h.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (h *Hub) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if h.key == "" || subtle.ConstantTimeCompare([]byte(got), []byte(h.key)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (h *Hub) listen(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	c := &conn{jobs: make(chan Job), gone: make(chan struct{})}
	h.mu.Lock()
	old := h.cur
	h.cur = c // the newest PC connection wins
	h.mu.Unlock()
	if old != nil {
		old.close()
	}
	defer h.drop(c)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, ": hello\n\n")
	fl.Flush()
	ping := time.NewTicker(h.Ping)
	defer ping.Stop()
	for {
		select {
		case j := <-c.jobs:
			b, _ := json.Marshal(j)
			if _, err := fmt.Fprintf(w, "event: say\ndata: %s\n\n", b); err != nil {
				return
			}
			fl.Flush()
		case <-ping.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			fl.Flush()
		case <-c.gone:
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (h *Hub) done(w http.ResponseWriter, r *http.Request) {
	var d struct {
		ID  string `json:"id"`
		Err string `json:"err"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&d); err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	h.mu.Lock()
	ch := h.waiting[d.ID]
	h.mu.Unlock()
	if ch != nil {
		select {
		case ch <- d.Err:
		default:
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// VoicePC marks a line the owner's PC played.
const VoicePC conductor.Voice = "pc"

// Speaker plays speech on the speaker (conductor.Conductor).
type Speaker interface {
	Speak(conductor.Speech) <-chan conductor.Outcome
}

// Route returns a Speaker that plays every rendered line on the PC while it
// is connected and hands the rest to speaker: lines in Xiao Ai's own voice
// (there is no clip to send) and lines the PC failed to play.
func (h *Hub) Route(speaker Speaker) Speaker { return router{h, speaker} }

type router struct {
	h  *Hub
	sp Speaker
}

func (r router) Speak(sp conductor.Speech) <-chan conductor.Outcome {
	if sp.URL == "" || !r.h.Online() {
		return r.sp.Speak(sp)
	}
	out := make(chan conductor.Outcome, 1)
	go func() {
		if err := r.h.Play(context.Background(), sp.URL, sp.Duration); err != nil {
			out <- <-r.sp.Speak(sp)
			return
		}
		out <- conductor.Outcome{Voice: VoicePC}
	}()
	return out
}
