package audio

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newHost(t *testing.T) (*Host, *clock, string) {
	t.Helper()
	c := &clock{now: time.Date(2026, 11, 14, 20, 0, 0, 0, time.UTC)}
	h := New(func() string { return "https://speaker.example" }, c.Now)
	f := filepath.Join(t.TempDir(), "x.mp3")
	os.WriteFile(f, []byte("ID3fake-mp3-bytes"), 0o600)
	return h, c, f
}

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestSpeechLinkServesFileThenExpiresAfterFirstFetch(t *testing.T) {
	h, c, f := newHost(t)
	u := h.Speech(f)
	if !strings.HasPrefix(u, "https://speaker.example/a/") || !strings.HasSuffix(u, ".mp3") {
		t.Fatalf("url = %s", u)
	}
	path := strings.TrimPrefix(u, "https://speaker.example")
	rec := get(h, path)
	if rec.Code != 200 || rec.Body.String() != "ID3fake-mp3-bytes" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("first fetch: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	c.advance(50 * time.Second)
	if get(h, path).Code != 200 {
		t.Fatal("refetch within 60 s should work")
	}
	c.advance(20 * time.Second)
	if get(h, path).Code != 404 {
		t.Fatal("60 s after first fetch the link must be gone")
	}
}

func TestUnfetchedSpeechLinkExpiresAfterFiveMinutes(t *testing.T) {
	h, c, f := newHost(t)
	path := strings.TrimPrefix(h.Speech(f), "https://speaker.example")
	c.advance(5*time.Minute + time.Second)
	if get(h, path).Code != 404 {
		t.Fatal("must expire")
	}
}

func TestSongLinkRedirectsAndLastsForTheSong(t *testing.T) {
	h, c, _ := newHost(t)
	u := h.Song(func() (string, error) { return "https://m701.music.126.net/x.mp3?sig=abc", nil }, 4*time.Minute)
	path := strings.TrimPrefix(u, "https://speaker.example")
	rec := get(h, path)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://m701.music.126.net/x.mp3?sig=abc" {
		t.Fatalf("got %d %v", rec.Code, rec.Header())
	}
	c.advance(8 * time.Minute) // song 4 min + 5 min grace: still alive at 8 min even after fetches
	if get(h, path).Code != http.StatusFound {
		t.Fatal("song link must outlive repeated fetches")
	}
	c.advance(2 * time.Minute)
	if get(h, path).Code != 404 {
		t.Fatal("song link must expire after duration + 5 min")
	}
}

func TestFetchesAreReported(t *testing.T) {
	h, _, f := newHost(t)
	path := strings.TrimPrefix(h.Speech(f), "https://speaker.example")
	if h.Fetched(path) {
		t.Fatal("not fetched yet")
	}
	get(h, path)
	if !h.Fetched(path) {
		t.Fatal("fetch not recorded")
	}
}

func TestUnknownAndWrongMethod(t *testing.T) {
	h, _, f := newHost(t)
	if rec := get(h, "/a/nope.mp3"); rec.Code != 404 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("unknown must 404 no-store: %d %v", rec.Code, rec.Header())
	}
	path := strings.TrimPrefix(h.Speech(f), "https://speaker.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d", rec.Code)
	}
}

func getRange(h http.Handler, path, rng string) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
}

func TestFetchLogKeepsTimesAndWhetherWholeFile(t *testing.T) {
	// 2026-11-14 on the OH2P: whole GET, tail range, bytes=0- (starts playing),
	// bytes=0- again ~2.5 s after the clip ended.
	h, c, f := newHost(t)
	u := h.Speech(f)
	path := strings.TrimPrefix(u, "https://speaker.example")
	getRange(h, path, "")
	c.advance(2 * time.Second)
	getRange(h, path, "bytes=13-")
	c.advance(3 * time.Second)
	getRange(h, path, "bytes=0-")
	got := h.Fetches(u)
	if len(got) != 3 || !got[0].Whole || got[1].Whole || !got[2].Whole {
		t.Fatalf("fetches = %+v", got)
	}
	if got[2].At.Sub(got[0].At) != 5*time.Second {
		t.Fatalf("times = %+v", got)
	}
	if h.Fetches("https://speaker.example/a/unknown.mp3") != nil {
		t.Fatal("unknown link must have no fetches")
	}
}

func body(h http.Handler, path, rng string) []byte {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Body.Bytes()
}

func TestProbeTailStartPatternStillPlaysTheClip(t *testing.T) {
	// 2026-11-14 22:05 real OH2P: whole probe, tail, whole start — every fetch gets the clip.
	h, c, f := newHost(t)
	u := h.Speech(f)
	path := strings.TrimPrefix(u, "https://speaker.example")
	body(h, path, "")
	c.advance(2 * time.Second)
	body(h, path, "bytes=13-")
	c.advance(3 * time.Second)
	if b := body(h, path, "bytes=0-"); string(b) != "ID3fake-mp3-bytes" {
		t.Fatalf("start fetch after the tail must be the clip: %q", b)
	}
	got := h.Fetches(u)
	if len(got) != 3 || !got[2].Whole || got[1].Whole {
		t.Fatalf("fetches = %+v", got)
	}
}

// Song fetches are logged too: a song the speaker never came for did not play.
func TestSongFetchesAreLogged(t *testing.T) {
	h, _, _ := newHost(t)
	u := h.Song(func() (string, error) { return "https://m701.music.126.net/x.mp3", nil }, time.Minute)
	if h.Fetches(u) != nil {
		t.Fatal("not fetched yet")
	}
	get(h, strings.TrimPrefix(u, "https://speaker.example"))
	if f := h.Fetches(u); len(f) != 1 || !f[0].Whole {
		t.Fatalf("fetches %+v", f)
	}
}
