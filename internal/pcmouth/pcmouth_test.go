package pcmouth

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hardware661/xueer-speaker/internal/conductor"
)

// agent connects like the Windows program and answers each line with err.
func agent(t *testing.T, base string, answer func(job Job) string) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/pc/listen", nil)
	req.Header.Set("Authorization", "Bearer pc-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("listen: %v %v", err, resp)
	}
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			d, ok := strings.CutPrefix(sc.Text(), "data: ")
			if !ok {
				continue
			}
			var j Job
			json.Unmarshal([]byte(d), &j)
			body, _ := json.Marshal(map[string]string{"id": j.ID, "err": answer(j)})
			r, _ := http.NewRequest("POST", base+"/pc/done", strings.NewReader(string(body)))
			r.Header.Set("Authorization", "Bearer pc-key")
			if res, err := http.DefaultClient.Do(r); err == nil {
				res.Body.Close()
			}
		}
	}()
	return cancel
}

func waitOnline(t *testing.T, h *Hub, want bool) {
	t.Helper()
	for range 200 {
		if h.Online() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("online = %v, want %v", h.Online(), want)
}

func TestLineGoesToThePCAndWaitsForIt(t *testing.T) {
	h := New("pc-key")
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	if h.Online() {
		t.Fatal("no PC yet")
	}
	var got Job
	stop := agent(t, srv.URL, func(j Job) string { got = j; return "" })
	waitOnline(t, h, true)
	if err := h.Play(context.Background(), "https://s/a/x.mp3", time.Second); err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://s/a/x.mp3" || got.ID == "" {
		t.Fatalf("job %+v", got)
	}
	stop()
	waitOnline(t, h, false)
}

func TestPCFailureIsReturned(t *testing.T) {
	h := New("pc-key")
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	stop := agent(t, srv.URL, func(Job) string { return "no audio device" })
	defer stop()
	waitOnline(t, h, true)
	if err := h.Play(context.Background(), "https://s/a/x.mp3", time.Second); err == nil || !strings.Contains(err.Error(), "no audio device") {
		t.Fatalf("err %v", err)
	}
}

func TestSilentPCTimesOut(t *testing.T) {
	h := New("pc-key")
	h.Grace = 50 * time.Millisecond
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	stop := agent(t, srv.URL, func(Job) string { time.Sleep(time.Second); return "" })
	defer stop()
	waitOnline(t, h, true)
	if err := h.Play(context.Background(), "u", 10*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("err %v", err)
	}
}

func TestNoPCAndWrongKey(t *testing.T) {
	h := New("pc-key")
	if err := h.Play(context.Background(), "u", time.Second); !errors.Is(err, ErrOffline) {
		t.Fatalf("err %v", err)
	}
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/pc/listen", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("wrong key = %d", resp.StatusCode)
	}
}

type fakeSpeaker struct{ got []conductor.Speech }

func (f *fakeSpeaker) Speak(sp conductor.Speech) <-chan conductor.Outcome {
	f.got = append(f.got, sp)
	ch := make(chan conductor.Outcome, 1)
	ch <- conductor.Outcome{Voice: conductor.VoiceEdge}
	return ch
}

func TestRouteGoesToThePCWhileItIsOn(t *testing.T) {
	h := New("pc-key")
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	sp := &fakeSpeaker{}
	r := h.Route(sp)

	// PC off: the speaker says it.
	if o := <-r.Speak(conductor.Speech{URL: "u1", Duration: time.Second}); o.Voice != conductor.VoiceEdge || len(sp.got) != 1 {
		t.Fatalf("off: %+v %+v", o, sp.got)
	}
	// PC on: the PC says it, the speaker hears nothing.
	stop := agent(t, srv.URL, func(Job) string { return "" })
	waitOnline(t, h, true)
	if o := <-r.Speak(conductor.Speech{URL: "u2", Duration: time.Second}); o.Voice != VoicePC || o.Err != nil || len(sp.got) != 1 {
		t.Fatalf("on: %+v %+v", o, sp.got)
	}
	// Xiao Ai's own voice only exists on the speaker.
	if o := <-r.Speak(conductor.Speech{Text: "原声"}); o.Voice != conductor.VoiceEdge || len(sp.got) != 2 {
		t.Fatalf("native: %+v %+v", o, sp.got)
	}
	stop()
	waitOnline(t, h, false)
}

func TestRouteFallsBackToTheSpeakerWhenThePCFails(t *testing.T) {
	h := New("pc-key")
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	sp := &fakeSpeaker{}
	stop := agent(t, srv.URL, func(Job) string { return "no audio device" })
	defer stop()
	waitOnline(t, h, true)
	if o := <-h.Route(sp).Speak(conductor.Speech{URL: "u", Duration: time.Second}); o.Voice != conductor.VoiceEdge || len(sp.got) != 1 {
		t.Fatalf("%+v %+v", o, sp.got)
	}
}

func TestVolumeGoesToThePCAndItsReportIsKept(t *testing.T) {
	h := New("pc-key")
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	if err := h.SetVolume(context.Background(), 30); !errors.Is(err, ErrOffline) {
		t.Fatalf("offline: %v", err)
	}
	var got Job
	stop := agent(t, srv.URL, func(j Job) string { got = j; return "" })
	defer stop()
	waitOnline(t, h, true)
	if _, ok := h.State(); ok {
		t.Fatal("no report yet")
	}
	r, _ := http.NewRequest("POST", srv.URL+"/pc/state", strings.NewReader(`{"volume":53}`))
	r.Header.Set("Authorization", "Bearer pc-key")
	resp, err := http.DefaultClient.Do(r)
	if err != nil || resp.StatusCode != 204 {
		t.Fatalf("report: %v %v", err, resp)
	}
	if st, ok := h.State(); !ok || st.Volume != 53 {
		t.Fatalf("state %+v %v", st, ok)
	}
	if err := h.SetVolume(context.Background(), 30); err != nil {
		t.Fatal(err)
	}
	if got.Kind != "volume" || got.Volume != 30 {
		t.Fatalf("job %+v", got)
	}
	if st, _ := h.State(); st.Volume != 30 {
		t.Fatalf("state after set %+v", st)
	}
}

// The stream drops after the PC took the line: the PC still says it and
// reports later; the speaker must not say it as well.
func TestDropAfterHandOverWaitsForThePC(t *testing.T) {
	h := New("pc-key")
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	release := make(chan struct{})
	var mu sync.Mutex
	var job Job
	stop := agent(t, srv.URL, func(j Job) string {
		mu.Lock()
		job = j
		mu.Unlock()
		<-release
		return ""
	})
	defer stop()
	waitOnline(t, h, true)
	res := make(chan error, 1)
	go func() { res <- h.Play(context.Background(), "u", 5*time.Second) }()
	for range 200 {
		mu.Lock()
		got := job.ID
		mu.Unlock()
		if got != "" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	srv.CloseClientConnections() // stream gone while the PC is still talking
	select {
	case err := <-res:
		t.Fatalf("gave up on the PC at the drop: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release) // the PC finishes and reports (its done POST reconnects)
	if err := <-res; err != nil {
		t.Fatal(err)
	}
}
