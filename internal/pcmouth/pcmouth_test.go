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

type fakeSpeaker struct {
	mu  sync.Mutex
	got []conductor.Speech
}

func (f *fakeSpeaker) Speak(sp conductor.Speech) <-chan conductor.Outcome {
	f.mu.Lock()
	f.got = append(f.got, sp)
	f.mu.Unlock()
	ch := make(chan conductor.Outcome, 1)
	ch <- conductor.Outcome{Voice: conductor.VoiceEdge}
	return ch
}

func (f *fakeSpeaker) said() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

// report posts the PC's state like the Windows program.
func report(base, body string) error {
	r, _ := http.NewRequest("POST", base+"/pc/state", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer pc-key")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		return errors.New(resp.Status)
	}
	return nil
}

// jobs records what the PC was asked to say.
type jobs struct {
	mu   sync.Mutex
	urls []string
}

func (j *jobs) add(u string) {
	j.mu.Lock()
	j.urls = append(j.urls, u)
	j.mu.Unlock()
}

func (j *jobs) list() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.urls...)
}

// Owner 2026-11-20: while the owner speaks into xueer-ear, a line said aloud
// is typed into what they are saying. Every line waits for the round to end
// and then they are all said, in the order they came.
func TestLinesWaitWhileTheOwnerDictatesThenPlayInOrder(t *testing.T) {
	h := New("pc-key")
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	var said jobs
	stop := agent(t, srv.URL, func(j Job) string { said.add(j.URL); return "" })
	defer stop()
	waitOnline(t, h, true)
	if err := report(srv.URL, `{"dictating":true}`); err != nil {
		t.Fatal(err)
	}
	if !h.Dictating() {
		t.Fatal("dictating not taken")
	}
	sp := &fakeSpeaker{}
	r := h.Route(sp)
	var outs []<-chan conductor.Outcome
	for _, u := range []string{"u1", "u2", "u3"} {
		outs = append(outs, r.Speak(conductor.Speech{URL: u, Duration: time.Millisecond}))
	}
	// Xiao Ai's own voice waits too: the speaker is next to the owner.
	outs = append(outs, r.Speak(conductor.Speech{Text: "原声"}))
	time.Sleep(300 * time.Millisecond)
	if n := len(said.list()); n != 0 || sp.said() != 0 {
		t.Fatalf("spoke while the owner was dictating: pc %d speaker %d", n, sp.said())
	}
	if err := report(srv.URL, `{"dictating":false}`); err != nil {
		t.Fatal(err)
	}
	for i, out := range outs[:3] {
		if o := <-out; o.Voice != VoicePC || o.Err != nil {
			t.Fatalf("line %d: %+v", i, o)
		}
	}
	if o := <-outs[3]; o.Voice != conductor.VoiceEdge {
		t.Fatalf("native: %+v", o)
	}
	if got := said.list(); strings.Join(got, " ") != "u1 u2 u3" {
		t.Fatalf("order %v", got)
	}
}

// The owner started dictating while a line played: the PC stopped it, and it
// is said again — with a fresh link, the old one may have expired — once the
// round ends.
func TestALineStoppedByDictationIsSaidAgainAfterwards(t *testing.T) {
	h := New("pc-key")
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	var said jobs
	stop := agent(t, srv.URL, func(j Job) string {
		said.add(j.URL)
		if len(said.list()) == 1 {
			report(srv.URL, `{"dictating":true}`)
			return Interrupted
		}
		return ""
	})
	defer stop()
	waitOnline(t, h, true)
	out := h.Route(&fakeSpeaker{}).Speak(conductor.Speech{URL: "old", Duration: time.Millisecond,
		Relink: func() string { return "fresh" }})
	time.Sleep(300 * time.Millisecond)
	if got := said.list(); len(got) != 1 {
		t.Fatalf("said again while dictating: %v", got)
	}
	report(srv.URL, `{"dictating":false}`)
	if o := <-out; o.Voice != VoicePC || o.Err != nil {
		t.Fatalf("%+v", o)
	}
	if got := said.list(); strings.Join(got, " ") != "old fresh" {
		t.Fatalf("jobs %v", got)
	}
}

// The PC goes away mid-round: no one says the owner is dictating any more,
// so the line goes to the speaker as it would with the PC off.
func TestPCGoneWhileDictatingHandsTheLineToTheSpeaker(t *testing.T) {
	h := New("pc-key")
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	stop := agent(t, srv.URL, func(Job) string { return "" })
	waitOnline(t, h, true)
	report(srv.URL, `{"dictating":true}`)
	sp := &fakeSpeaker{}
	out := h.Route(sp).Speak(conductor.Speech{URL: "u", Duration: time.Millisecond})
	time.Sleep(200 * time.Millisecond)
	if sp.said() != 0 {
		t.Fatal("spoke while dictating")
	}
	stop()
	if o := <-out; o.Voice != conductor.VoiceEdge || sp.said() != 1 {
		t.Fatalf("%+v", o)
	}
}

// A round that never ends (xueer-ear hung) holds a line for MaxHold at most.
func TestALineWaitsAtMostMaxHold(t *testing.T) {
	h := New("pc-key")
	h.MaxHold = 200 * time.Millisecond
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	stop := agent(t, srv.URL, func(Job) string { return "" })
	defer stop()
	waitOnline(t, h, true)
	report(srv.URL, `{"dictating":true}`)
	began := time.Now()
	if o := <-h.Route(&fakeSpeaker{}).Speak(conductor.Speech{URL: "u", Duration: time.Millisecond}); o.Voice != VoicePC {
		t.Fatalf("%+v", o)
	}
	if waited := time.Since(began); waited < 200*time.Millisecond || waited > 2*time.Second {
		t.Fatalf("waited %s", waited)
	}
}

func TestStateTakesVolumeAndDictatingSeparately(t *testing.T) {
	h := New("pc-key")
	srv := httptest.NewServer(h.Handler())
	defer srv.Close()
	stop := agent(t, srv.URL, func(Job) string { return "" })
	defer stop()
	waitOnline(t, h, true)
	if err := report(srv.URL, `{"dictating":true}`); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.State(); ok || !h.Dictating() {
		t.Fatal("dictating alone must not invent a volume")
	}
	if err := report(srv.URL, `{"volume":40}`); err != nil {
		t.Fatal(err)
	}
	if st, ok := h.State(); !ok || st.Volume != 40 || !h.Dictating() {
		t.Fatalf("volume alone must keep dictating: %+v %v", st, h.Dictating())
	}
	for _, bad := range []string{`{}`, `{"volume":101}`, `{"dictating":"yes"}`} {
		if err := report(srv.URL, bad); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
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
