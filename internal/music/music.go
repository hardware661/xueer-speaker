// Package music reads the owner's Husic library (liked songs, playlists) with
// a read-only service key and turns it into a shuffled queue of songs the
// speaker can play: NetEase copies only, mp3 only, link resolved at play time
// (NetEase links expire in about 20 minutes).
package music

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
)

var (
	ErrNoPlaylist = errors.New("music: no playlist with that name")
	ErrEmpty      = errors.New("music: nothing playable there")
	ErrUnplayable = errors.New("music: no NetEase mp3 for this song")
	ErrNoPlayer   = errors.New("music: no Husic open on the owner's devices")
)

// Command asks the owner's open Husic (desktop app or browser) to play
// "liked" / "playlist:<name>", or next / pause / resume / stop.
func (c *Client) Command(ctx context.Context, action, what string) error {
	body, _ := json.Marshal(map[string]string{"action": action, "what": what})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/api/remote/command", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("husic: %w", err)
	}
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusAccepted, http.StatusOK:
		return nil
	case http.StatusConflict:
		return ErrNoPlayer
	}
	return fmt.Errorf("husic: remote command: HTTP %d", resp.StatusCode)
}

// Client talks to Husic over the container network.
type Client struct {
	base string
	key  string
	http *http.Client
}

// NewClient returns a client for Husic at base with the service key.
func NewClient(base, key string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), key: key, http: &http.Client{Timeout: 15 * time.Second}}
}

// item mirrors Husic's api.Item.
type item struct {
	SongID   int64    `json:"songId,omitempty"`
	Title    string   `json:"title"`
	Artists  []string `json:"artists"`
	Album    string   `json:"album"`
	Duration int      `json:"duration"`
	Copies   []struct {
		Source string `json:"source"`
		ID     string `json:"id"`
	} `json:"copies"`
}

func (it item) hasNetease() bool {
	for _, c := range it.Copies {
		if c.Source == "netease" {
			return true
		}
	}
	return false
}

func (c *Client) call(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("husic: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNoPlaylist
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("husic: %s %s: HTTP %d", method, path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
}

// Song is one queued song.
type Song struct {
	Title    string
	Artist   string
	Duration time.Duration
	Resolve  func() (string, error) // fresh direct NetEase mp3 link
}

// Queue hands out songs in shuffled order, each once.
type Queue struct {
	songs []Song
	i     int
}

// Next returns the next song; false when the round is over.
func (q *Queue) Next(context.Context) (Song, bool) {
	if q.i >= len(q.songs) {
		return Song{}, false
	}
	s := q.songs[q.i]
	q.i++
	return s, true
}

// NewQueue is a queue of songs in the given order.
func NewQueue(songs []Song) *Queue { return &Queue{songs: songs} }

// Len is the number of songs in the round.
func (q *Queue) Len() int { return len(q.songs) }

// Shuffle returns a random permutation of 0..n-1.
func Shuffle(n int) []int { return rand.Perm(n) }

// Liked queues the owner's liked songs. order may be nil (= Shuffle).
func (c *Client) Liked(ctx context.Context, order func(int) []int) (*Queue, error) {
	var lib struct {
		Likes []item `json:"likes"`
	}
	if err := c.call(ctx, http.MethodGet, "/api/library", nil, &lib); err != nil {
		return nil, err
	}
	return c.queue(lib.Likes, order)
}

// Playlists lists the owner's playlist names, in Husic's order.
func (c *Client) Playlists(ctx context.Context) ([]string, error) {
	var lib struct {
		Playlists []struct {
			Name string `json:"name"`
		} `json:"playlists"`
	}
	if err := c.call(ctx, http.MethodGet, "/api/library", nil, &lib); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(lib.Playlists))
	for _, p := range lib.Playlists {
		names = append(names, p.Name)
	}
	return names, nil
}

// Playlist queues the playlist whose name best matches name.
func (c *Client) Playlist(ctx context.Context, name string, order func(int) []int) (*Queue, error) {
	var lib struct {
		Playlists []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"playlists"`
	}
	if err := c.call(ctx, http.MethodGet, "/api/library", nil, &lib); err != nil {
		return nil, err
	}
	var id int64
	best := 0
	for _, p := range lib.Playlists {
		if s := nameScore(p.Name, name); s > best {
			best, id = s, p.ID
		}
	}
	if id == 0 {
		return nil, ErrNoPlaylist
	}
	var pl struct {
		Songs []item `json:"songs"`
	}
	if err := c.call(ctx, http.MethodGet, fmt.Sprintf("/api/playlists/%d", id), nil, &pl); err != nil {
		return nil, err
	}
	return c.queue(pl.Songs, order)
}

// nameScore: exact > contains > shares characters; 0 = no match.
func nameScore(have, want string) int {
	h, w := strings.ToLower(have), strings.ToLower(want)
	switch {
	case h == w:
		return 1000
	case strings.Contains(h, w) || strings.Contains(w, h):
		return 500
	}
	n := 0
	for _, r := range w {
		if strings.ContainsRune(h, r) {
			n++
		}
	}
	if n*2 < len([]rune(w)) { // fewer than half the characters: not this one
		return 0
	}
	return n
}

func (c *Client) queue(items []item, order func(int) []int) (*Queue, error) {
	var ok []item
	for _, it := range items {
		if it.hasNetease() {
			ok = append(ok, it)
		}
	}
	if len(ok) == 0 {
		return nil, ErrEmpty
	}
	if order == nil {
		order = Shuffle
	}
	q := &Queue{}
	for _, i := range order(len(ok)) {
		it := ok[i]
		q.songs = append(q.songs, Song{
			Title:    it.Title,
			Artist:   strings.Join(it.Artists, "、"),
			Duration: time.Duration(it.Duration) * time.Second,
			Resolve:  c.resolver(it),
		})
	}
	return q, nil
}

func (c *Client) resolver(it item) func() (string, error) {
	return func() (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var out struct {
			URL    string `json:"url"`
			Source string `json:"source"`
			Format string `json:"format"`
		}
		err := c.call(ctx, http.MethodPost, "/api/stream", map[string]any{
			"item": it, "sources": []string{"netease"}, "noLink": true,
		}, &out)
		if err != nil {
			return "", err
		}
		if out.Source != "netease" || !strings.HasPrefix(out.URL, "https://") || !strings.EqualFold(out.Format, "mp3") {
			return "", ErrUnplayable
		}
		return out.URL, nil
	}
}

// NowPlaying is what the owner's open Husic is playing.
type NowPlaying struct {
	Open    bool   `json:"open"`
	Title   string `json:"title"`
	Artist  string `json:"artist"`
	Playing bool   `json:"playing"`
}

// NowPlaying reads the open Husic's state (what the speaker's panel shows
// while the music plays on the PC).
func (c *Client) NowPlaying(ctx context.Context) (NowPlaying, error) {
	var st NowPlaying
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/remote/state", nil)
	if err != nil {
		return st, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := c.http.Do(req)
	if err != nil {
		return st, fmt.Errorf("husic: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return st, fmt.Errorf("husic: remote state: HTTP %d", resp.StatusCode)
	}
	return st, json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&st)
}
