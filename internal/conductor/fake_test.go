package conductor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hardware661/xueer-speaker/internal/audio"
	"github.com/hardware661/xueer-speaker/internal/xiaomi"
)

// fakeDevice records what the conductor asks the speaker to do.
type fakeDevice struct {
	mu       sync.Mutex
	calls    []string
	status   xiaomi.Status
	failPush bool
	btPlay   bool

	pushedAt map[string]time.Time
}

func (d *fakeDevice) log(s string) { d.mu.Lock(); d.calls = append(d.calls, s); d.mu.Unlock() }

func (d *fakeDevice) Push(_ context.Context, url string) error {
	d.log("push " + url)
	d.mu.Lock()
	if d.pushedAt == nil {
		d.pushedAt = map[string]time.Time{}
	}
	d.pushedAt[url] = time.Now()
	d.mu.Unlock()
	if d.failPush {
		return fmt.Errorf("refused")
	}
	return nil
}
func (d *fakeDevice) SpeakNative(_ context.Context, text string) error {
	d.log("native " + text)
	return nil
}
func (d *fakeDevice) Control(_ context.Context, op xiaomi.Op) error {
	d.log("control " + string(op))
	return nil
}
func (d *fakeDevice) SetVolume(_ context.Context, v int) error {
	d.log(fmt.Sprintf("volume %d", v))
	return nil
}
func (d *fakeDevice) Status(context.Context) (xiaomi.Status, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.status, nil
}
func (d *fakeDevice) SetLoop(_ context.Context, t int) error {
	d.log(fmt.Sprintf("loop %d", t))
	return nil
}
func (d *fakeDevice) BluetoothPlaying(context.Context) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.btPlay, nil
}
func (d *fakeDevice) setBT(on bool)             { d.mu.Lock(); d.btPlay = on; d.mu.Unlock() }
func (d *fakeDevice) setStatus(s xiaomi.Status) { d.mu.Lock(); d.status = s; d.mu.Unlock() }

func (d *fakeDevice) history() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.Join(d.calls, " | ")
}

// waitFor polls until cond holds or fails the test.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// step is one request the fake speaker makes for a clip, at offset after the push.
type step struct {
	after time.Duration
	whole bool
}

// realHTTPS is what the OH2P did over https on 2026-11-14, scaled 1 s → 1 ms
// for a 7.6 ms clip: probe, tail, start playing (and, before loop type 2, a
// refetch 2.5 ms after the end).
var realHTTPS = []step{{3 * time.Millisecond, true}, {5 * time.Millisecond, false}, {8 * time.Millisecond, true}, {18 * time.Millisecond, true}}

// fetchesFrom replays steps relative to when the device received each push.
func fetchesFrom(d *fakeDevice, steps []step) func(string) []audio.Fetch {
	return func(url string) []audio.Fetch {
		d.mu.Lock()
		at, ok := d.pushedAt[url]
		d.mu.Unlock()
		if !ok {
			return nil
		}
		var out []audio.Fetch
		for _, s := range steps {
			if time.Since(at) >= s.after {
				out = append(out, audio.Fetch{At: at.Add(s.after), Whole: s.whole})
			}
		}
		return out
	}
}
