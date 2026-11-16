// Package audio hands the speaker short-lived, unguessable links under /a/.
// The speaker cannot send credentials, so the link itself is the secret: 32
// random bytes, never logged in full, gone after a few minutes.
//
//   - speech: /a/<token>.mp3 serves a rendered clip; 5 minutes, or 60 s after
//     the first fetch, whichever is sooner.
//   - song: /a/<token> redirects to a freshly resolved direct link; lives for
//     the song's length plus 5 minutes regardless of fetches (the speaker
//     reconnects and sends Range requests mid-song).
package audio

import (
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	speechTTL        = 5 * time.Minute
	speechAfterFetch = 60 * time.Second
	songGrace        = 5 * time.Minute
)

type entry struct {
	file      string                 // speech
	resolve   func() (string, error) // song
	expires   time.Time
	fetchedAt time.Time
	fetches   []Fetch // speech only; the conductor reads when playback began from it
}

// Fetch is one request the speaker made for a speech link.
type Fetch struct {
	At    time.Time
	Whole bool // no Range, or a Range from byte 0: the player (re)starts the clip
}

// maxFetches bounds the log per link.
const maxFetches = 16

// Host owns the /a/ links.
type Host struct {
	base func() string // where the speaker reaches this service; read per link
	now  func() time.Time
	Log  *slog.Logger // optional: one line per speech fetch (link prefix only)

	mu    sync.Mutex
	links map[string]*entry // key: path without "/a/"
}

// New returns a Host whose links start with base() (e.g.
// http://192.168.1.10:8100); base is read for every link, so it can be
// learnt or changed while running.
func New(base func() string, now func() time.Time) *Host {
	if now == nil {
		now = time.Now
	}
	return &Host{base: base, now: now, links: map[string]*entry{}}
}

func (h *Host) prefix() string { return strings.TrimRight(h.base(), "/") + "/a/" }

func token() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Speech publishes a clip file and returns its URL.
func (h *Host) Speech(file string) string {
	key := token() + ".mp3"
	h.mu.Lock()
	h.sweep()
	h.links[key] = &entry{file: file, expires: h.now().Add(speechTTL)}
	h.mu.Unlock()
	return h.prefix() + key
}

// Song publishes a song; resolve is called on every fetch so the speaker
// always gets a fresh direct link.
func (h *Host) Song(resolve func() (string, error), length time.Duration) string {
	key := token()
	h.mu.Lock()
	h.sweep()
	h.links[key] = &entry{resolve: resolve, expires: h.now().Add(length + songGrace)}
	h.mu.Unlock()
	return h.prefix() + key
}

// Fetched reports whether the speaker has fetched the link at path or URL.
func (h *Host) Fetched(link string) bool {
	key := keyOf(link)
	h.mu.Lock()
	defer h.mu.Unlock()
	e, ok := h.links[key]
	return ok && !e.fetchedAt.IsZero()
}

// Fetches returns the requests the speaker made for a speech link, oldest
// first; nil for unknown links.
func (h *Host) Fetches(link string) []Fetch {
	h.mu.Lock()
	defer h.mu.Unlock()
	e, ok := h.links[keyOf(link)]
	if !ok || len(e.fetches) == 0 {
		return nil
	}
	return append([]Fetch(nil), e.fetches...)
}

func keyOf(link string) string {
	if i := strings.Index(link, "/a/"); i >= 0 {
		return link[i+3:]
	}
	return link
}

// sweep drops expired links; caller holds h.mu.
func (h *Host) sweep() {
	now := h.now()
	for k, e := range h.links {
		if now.After(e.expires) {
			delete(h.links, k)
		}
	}
}

// ServeHTTP answers GET/HEAD /a/<key>.
func (h *Host) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/a/")
	now := h.now()
	h.mu.Lock()
	e, ok := h.links[key]
	if ok && now.After(e.expires) {
		delete(h.links, key)
		ok = false
	}
	if ok && e.fetchedAt.IsZero() {
		e.fetchedAt = now
		if e.file != "" && now.Add(speechAfterFetch).Before(e.expires) {
			e.expires = now.Add(speechAfterFetch)
		}
	}
	if ok && len(e.fetches) < maxFetches {
		rng := r.Header.Get("Range")
		e.fetches = append(e.fetches, Fetch{At: now, Whole: rng == "" || strings.HasPrefix(rng, "bytes=0-")})
		if h.Log != nil && e.file != "" {
			h.Log.Info("fetch", "link", key[:6], "n", len(e.fetches), "range", rng, "ua", r.UserAgent())
		}
	}
	var file string
	var resolve func() (string, error)
	if ok {
		file, resolve = e.file, e.resolve
	}
	h.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store") // 404s too: Cloudflare would cache them for 4 h
	if !ok {
		http.NotFound(w, r)
		return
	}
	if resolve != nil {
		u, err := resolve()
		if h.Log != nil { // one line per song fetch: did the speaker come, did the link resolve
			h.Log.Info("song fetch", "link", key[:6], "range", r.Header.Get("Range"), "ua", r.UserAgent(), "ok", err == nil && u != "")
		}
		if err != nil || u == "" {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, u, http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "audio/mpeg")
	http.ServeFile(w, r, file)
}
