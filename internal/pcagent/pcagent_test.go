package pcagent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hardware661/xueer-speaker/internal/pcmouth"
)

// The agent against the real hub: it comes online, plays what it is sent
// (fetching the clip itself) and reconnects after the server drops it.
func TestAgentPlaysLinesAndReconnects(t *testing.T) {
	hub := pcmouth.New("pc-key")
	mux := http.NewServeMux()
	mux.Handle("/pc/", hub.Handler())
	mux.HandleFunc("GET /a/x.mp3", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("mp3!")) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var mu sync.Mutex
	var heard []string
	a := &Agent{Base: srv.URL, Key: "pc-key", Retry: 20 * time.Millisecond,
		Play: func(_ context.Context, clip []byte) error {
			mu.Lock()
			heard = append(heard, string(clip))
			mu.Unlock()
			return nil
		}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)

	wait(t, hub.Online)
	if err := hub.Play(ctx, srv.URL+"/a/x.mp3", time.Second); err != nil {
		t.Fatal(err)
	}
	srv.CloseClientConnections() // server restart / network blip
	time.Sleep(50 * time.Millisecond)
	wait(t, hub.Online)
	if err := hub.Play(ctx, srv.URL+"/a/x.mp3", time.Second); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(heard) != 2 || heard[0] != "mp3!" {
		t.Fatalf("heard %q", heard)
	}
}

func TestPlaybackErrorIsReported(t *testing.T) {
	hub := pcmouth.New("pc-key")
	mux := http.NewServeMux()
	mux.Handle("/pc/", hub.Handler())
	mux.HandleFunc("GET /a/x.mp3", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("mp3!")) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	a := &Agent{Base: srv.URL, Key: "pc-key", Retry: 20 * time.Millisecond,
		Play: func(context.Context, []byte) error { return errors.New("no output device") }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	wait(t, hub.Online)
	if err := hub.Play(ctx, srv.URL+"/a/x.mp3", time.Second); err == nil {
		t.Fatal("want the PC's error")
	}
	// A clip that cannot be fetched is reported too.
	if err := hub.Play(ctx, srv.URL+"/a/missing.mp3", time.Second); err == nil {
		t.Fatal("want a fetch error")
	}
}

func TestWrongKeyKeepsRetrying(t *testing.T) {
	hub := pcmouth.New("pc-key")
	srv := httptest.NewServer(hub.Handler())
	defer srv.Close()
	a := &Agent{Base: srv.URL, Key: "nope", Retry: 10 * time.Millisecond, Play: func(context.Context, []byte) error { return nil }}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	a.Run(ctx)
	if hub.Online() {
		t.Fatal("wrong key came online")
	}
}

func wait(t *testing.T, ok func() bool) {
	t.Helper()
	for range 300 {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out")
}

// While the PC is on, the panel's volume is the PC's system volume (owner
// 11-15); the agent reports it and applies what it is sent.
func TestVolumeIsReportedAndSet(t *testing.T) {
	hub := pcmouth.New("pc-key")
	srv := httptest.NewServer(hub.Handler())
	defer srv.Close()
	var mu sync.Mutex
	vol := 53
	a := &Agent{Base: srv.URL, Key: "pc-key", Retry: 20 * time.Millisecond, Play: func(context.Context, []byte) error { return nil },
		GetVolume: func() (int, error) { mu.Lock(); defer mu.Unlock(); return vol, nil },
		SetVolume: func(v int) error { mu.Lock(); vol = v; mu.Unlock(); return nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	wait(t, func() bool { st, ok := hub.State(); return ok && st.Volume == 53 })
	if err := hub.SetVolume(ctx, 20); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if vol != 20 {
		t.Fatalf("vol %d", vol)
	}
}

// Pausing hangs up (the server then sends lines to the speaker); resuming
// comes back at once.
func TestPauseHangsUpAndResumeReconnects(t *testing.T) {
	hub := pcmouth.New("pc-key")
	srv := httptest.NewServer(hub.Handler())
	defer srv.Close()
	var mu sync.Mutex
	var states []bool
	a := &Agent{Base: srv.URL, Key: "pc-key", Retry: 20 * time.Millisecond, Play: func(context.Context, []byte) error { return nil },
		OnState: func(c bool) { mu.Lock(); states = append(states, c); mu.Unlock() }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	wait(t, hub.Online)
	a.SetPaused(true)
	wait(t, func() bool { return !hub.Online() })
	time.Sleep(50 * time.Millisecond)
	if hub.Online() {
		t.Fatal("came back while paused")
	}
	a.SetPaused(false)
	wait(t, hub.Online)
	mu.Lock()
	defer mu.Unlock()
	if len(states) < 3 || !states[0] || states[len(states)-1] != true {
		t.Fatalf("states %v", states)
	}
}

// 11-15 17:33: a server release dropped the stream while a line was being
// said, and the line was cut. It must finish.
func TestALineFinishesWhenTheStreamDrops(t *testing.T) {
	hub := pcmouth.New("pc-key")
	mux := http.NewServeMux()
	mux.Handle("/pc/", hub.Handler())
	mux.HandleFunc("GET /a/x.mp3", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("mp3!")) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	started, finished := make(chan struct{}), make(chan error, 1)
	a := &Agent{Base: srv.URL, Key: "pc-key", Retry: 20 * time.Millisecond,
		Play: func(ctx context.Context, _ []byte) error {
			close(started)
			select {
			case <-time.After(200 * time.Millisecond):
				finished <- nil
			case <-ctx.Done():
				finished <- ctx.Err()
			}
			return nil
		}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	wait(t, hub.Online)
	go hub.Play(context.Background(), srv.URL+"/a/x.mp3", time.Second)
	<-started
	srv.CloseClientConnections() // the release
	if err := <-finished; err != nil {
		t.Fatalf("line cut: %v", err)
	}
}
