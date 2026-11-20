// Package pcagent is the resident program on the PC: it keeps the speaker
// server's listen stream open, so the server knows the PC is on, and plays
// every line it is sent.
// Platform-free; the Windows build supplies Play.
package pcagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hardware661/xueer-speaker/internal/pcmouth"
)

// Agent connects to Base (the server, e.g. http://192.168.1.10:8100) with Key.
type Agent struct {
	Base   string
	Key    string
	Client *http.Client // nil = http.DefaultClient
	Play   func(ctx context.Context, clip []byte) error
	// Volume reads / sets the system volume (0–100); nil = not supported.
	GetVolume func() (int, error)
	SetVolume func(int) error
	Log       *log.Logger   // nil = silent
	Retry     time.Duration // first reconnect delay, doubling to MaxRetry (2 s)

	// MaxRetry caps the reconnect delay (1 min).
	MaxRetry time.Duration
	// OnState, if set, hears every change: connected to the server or not.
	OnState func(connected bool)
	// Dictating, if set, says whether the owner is dictating on this PC
	// (xueer-ear, see EarDictating). It is read every Watch: the server hears
	// of each change and holds lines meanwhile, and a line being said stops
	// when a round starts (the server says it again afterwards).
	Dictating func() bool
	Watch     time.Duration // 300 ms

	mu      sync.Mutex
	paused  bool
	saying  sync.WaitGroup // lines being said right now
	wake    chan struct{}  // closed when paused changes
	stops   map[uint64]context.CancelCauseFunc
	stopSeq uint64
}

// errDictating is what a line stopped by dictation reports to the server.
var errDictating = errors.New(pcmouth.Interrupted)

func (a *Agent) dictating() bool { return a.Dictating != nil && a.Dictating() }

// interrupt stops every line being said: the owner started dictating.
func (a *Agent) interrupt() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, stop := range a.stops {
		stop(errDictating)
	}
}

// EarDictating reads xueer-ear's status.json (%LOCALAPPDATA%\XueerEar):
// whether a dictation round is on. xueer-ear rewrites the file at least
// every 5 s while it runs, so a file older than 15 s (xueer-ear quit or
// hung) counts as not dictating.
func EarDictating(status []byte, now time.Time) bool {
	var st struct {
		Dictating bool  `json:"dictating"`
		UpdatedMS int64 `json:"updated_ms"`
	}
	if json.Unmarshal(status, &st) != nil || !st.Dictating {
		return false
	}
	age := now.Sub(time.UnixMilli(st.UpdatedMS))
	return age < 15*time.Second && age > -time.Minute
}

// Drain waits (up to limit) for the lines being said to finish, so quitting
// does not cut a sentence (an update restart did).
func (a *Agent) Drain(limit time.Duration) {
	done := make(chan struct{})
	go func() { a.saying.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(limit):
	}
}

// SetPaused stops (true) or resumes taking lines. While paused the PC is
// not connected, so the server sends every line to the speaker instead.
func (a *Agent) SetPaused(p bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.paused == p {
		return
	}
	a.paused = p
	if a.wake != nil {
		close(a.wake)
		a.wake = nil
	}
}

// Paused reports SetPaused.
func (a *Agent) Paused() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.paused
}

// changed is closed the next time SetPaused changes the state.
func (a *Agent) changed() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.wake == nil {
		a.wake = make(chan struct{})
	}
	return a.wake
}

func (a *Agent) state(connected bool) {
	if a.OnState != nil {
		a.OnState(connected)
	}
}

// Run stays connected until ctx ends.
func (a *Agent) Run(ctx context.Context) {
	retry, maxRetry := a.Retry, a.MaxRetry
	if retry <= 0 {
		retry = 2 * time.Second
	}
	if maxRetry <= 0 {
		maxRetry = time.Minute
	}
	delay := retry
	for ctx.Err() == nil {
		changed := a.changed()
		if a.Paused() {
			a.state(false)
			select {
			case <-ctx.Done():
				return
			case <-changed:
			}
			delay = retry
			continue
		}
		lctx, cancel := context.WithCancel(ctx)
		go func() { // pausing hangs up at once
			select {
			case <-changed:
				cancel()
			case <-lctx.Done():
			}
		}()
		began := time.Now()
		err := a.listen(lctx)
		cancel()
		a.state(false)
		if ctx.Err() != nil {
			return
		}
		if a.Paused() {
			continue
		}
		if time.Since(began) > time.Minute {
			delay = retry // it was a working connection: come back fast
		}
		a.logf("disconnected: %v; retry in %s", err, delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, maxRetry)
	}
}

// stream has no overall timeout; the server pings every 15 s, so a stream
// silent for longer than idle is dead.
const idle = 45 * time.Second

func (a *Agent) listen(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", a.Base+"/pc/listen", nil)
	req.Header.Set("Authorization", "Bearer "+a.Key)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := a.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("listen: HTTP %d", resp.StatusCode)
	}
	a.logf("connected")
	a.state(true)
	go a.report(ctx)
	alive := time.AfterFunc(idle, cancel)
	defer alive.Stop()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		alive.Reset(idle)
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		var j pcmouth.Job
		if err := json.Unmarshal([]byte(data), &j); err != nil || j.ID == "" {
			continue
		}
		switch j.Kind {
		case "volume":
			go a.volume(j)
		default:
			// A line being said is finished even if the stream drops (a
			// server release, 11-15 17:33: cut mid-sentence); only quitting
			// the program stops it.
			a.saying.Add(1)
			go func() { defer a.saying.Done(); a.say(context.WithoutCancel(ctx), j) }()
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errors.New("stream ended")
}

// say plays one line and reports how it went. While the owner is dictating
// it is not started; if a round starts while it plays, it stops.
func (a *Agent) say(ctx context.Context, j pcmouth.Job) {
	if a.dictating() {
		a.done(j.ID, errDictating)
		return
	}
	ctx, stop := context.WithCancelCause(ctx)
	a.mu.Lock()
	if a.stops == nil {
		a.stops = map[uint64]context.CancelCauseFunc{}
	}
	a.stopSeq++
	n := a.stopSeq
	a.stops[n] = stop
	a.mu.Unlock()
	err := a.fetchAndPlay(ctx, j.URL)
	if errors.Is(context.Cause(ctx), errDictating) {
		err = errDictating
	}
	a.mu.Lock()
	delete(a.stops, n)
	a.mu.Unlock()
	stop(nil)
	a.done(j.ID, err)
}

func (a *Agent) volume(j pcmouth.Job) {
	err := errors.New("volume not supported here")
	if a.SetVolume != nil {
		err = a.SetVolume(j.Volume)
	}
	a.done(j.ID, err)
}

// report tells the server, while the stream is up, the system volume (the
// owner may turn it on the PC) and whether the owner is dictating, now and
// whenever either changes; a round starting stops the line being said.
func (a *Agent) report(ctx context.Context) {
	if a.GetVolume == nil && a.Dictating == nil {
		return
	}
	watch := a.Watch
	if watch <= 0 {
		watch = 300 * time.Millisecond
	}
	tick := time.NewTicker(watch)
	defer tick.Stop()
	lastVolume, sentTalk, talking := -1, false, false
	first := true
	var volumeAt time.Time
	for {
		body := map[string]any{}
		if a.GetVolume != nil && time.Since(volumeAt) >= 2*time.Second {
			volumeAt = time.Now()
			if v, err := a.GetVolume(); err == nil && v != lastVolume {
				body["volume"] = v
			}
		}
		if a.Dictating != nil {
			now := a.Dictating()
			if now && !talking {
				a.interrupt()
			}
			talking = now
			if first || now != sentTalk {
				body["dictating"] = now
			}
		}
		if len(body) > 0 && a.post("/pc/state", body) == nil {
			if v, ok := body["volume"].(int); ok {
				lastVolume = v
			}
			if d, ok := body["dictating"].(bool); ok {
				sentTalk, first = d, false
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (a *Agent) done(id string, err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
		a.logf("job %s: %v", id, err)
	}
	a.post("/pc/done", map[string]string{"id": id, "err": msg})
}

func (a *Agent) post(path string, v any) error {
	body, _ := json.Marshal(v)
	rctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(rctx, "POST", a.Base+path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+a.Key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client().Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: HTTP %d", path, resp.StatusCode)
	}
	return nil
}

func (a *Agent) fetchAndPlay(ctx context.Context, url string) error {
	if !strings.HasPrefix(url, a.Base+"/a/") {
		return errors.New("clip is not on the speaker server") // never fetch anything else
	}
	fctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(fctx, "GET", url, nil)
	resp, err := a.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch clip: HTTP %d", resp.StatusCode)
	}
	clip, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	pctx, cancel2 := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel2()
	return a.Play(pctx, clip)
}

func (a *Agent) client() *http.Client {
	if a.Client != nil {
		return a.Client
	}
	return http.DefaultClient
}

func (a *Agent) logf(f string, v ...any) {
	if a.Log != nil {
		a.Log.Printf(f, v...)
	}
}
