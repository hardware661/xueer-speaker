package music

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeHusic answers the four whitelisted endpoints the way Husic does.
func fakeHusic(t *testing.T) (*httptest.Server, *[]string) {
	var streamed []string
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer svc-key" {
				http.Error(w, "forbidden", 403)
				return
			}
			h(w, r)
		}
	}
	song := func(id int, title string, srcs ...string) map[string]any {
		var copies []map[string]any
		for _, s := range srcs {
			copies = append(copies, map[string]any{"source": s, "id": s + "-" + title})
		}
		return map[string]any{"songId": id, "title": title, "artists": []string{"歌手"}, "duration": 200, "copies": copies}
	}
	mux.HandleFunc("GET /api/library", auth(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"likes":     []any{song(1, "晴天", "netease"), song(2, "只有油管", "youtube"), song(3, "七里香", "netease", "qq")},
			"playlists": []any{map[string]any{"id": 7, "name": "早上听", "count": 2}, map[string]any{"id": 8, "name": "运动", "count": 1}},
		})
	}))
	mux.HandleFunc("GET /api/playlists/{id}", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "7" {
			http.Error(w, `{"error":"no such playlist"}`, 404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"songs": []any{song(4, "早安", "netease"), song(5, "日出", "netease")}})
	}))
	mux.HandleFunc("POST /api/stream", auth(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req struct {
			Item struct {
				Title string `json:"title"`
			} `json:"item"`
			Sources []string `json:"sources"`
			NoLink  bool     `json:"noLink"`
		}
		json.Unmarshal(b, &req)
		if len(req.Sources) != 1 || req.Sources[0] != "netease" || !req.NoLink {
			http.Error(w, "speaker must ask netease-only without linking", 400)
			return
		}
		streamed = append(streamed, req.Item.Title)
		format := "mp3"
		if req.Item.Title == "七里香" {
			format = "flac"
		}
		json.NewEncoder(w).Encode(map[string]any{"url": "https://m701.music.126.net/" + req.Item.Title + ".mp3", "source": "netease", "format": format})
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &streamed
}

func TestLikedQueueShufflesPlaysOnlyNeteaseMP3(t *testing.T) {
	srv, _ := fakeHusic(t)
	c := NewClient(srv.URL, "svc-key")
	reverse := func(n int) []int { // deterministic: last playable first
		out := make([]int, n)
		for i := range out {
			out[i] = n - 1 - i
		}
		return out
	}
	q, err := c.Liked(context.Background(), reverse)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for {
		s, ok := q.Next(context.Background())
		if !ok {
			break
		}
		u, err := s.Resolve()
		if err != nil {
			got = append(got, "skip:"+s.Title)
			continue
		}
		if !strings.HasPrefix(u, "https://") {
			t.Fatalf("url %q", u)
		}
		got = append(got, s.Title)
		if s.Duration != 200*time.Second {
			t.Fatalf("duration %v", s.Duration)
		}
	}
	// "只有油管" has no netease copy: never queued. "七里香" resolves to flac: skipped at resolve time.
	if strings.Join(got, ",") != "skip:七里香,晴天" {
		t.Fatalf("got %v", got)
	}
}

func TestPlaylistByFuzzyName(t *testing.T) {
	srv, _ := fakeHusic(t)
	c := NewClient(srv.URL, "svc-key")
	q, err := c.Playlist(context.Background(), "早上", nil)
	if err != nil {
		t.Fatal(err)
	}
	s, ok := q.Next(context.Background())
	if !ok || (s.Title != "早安" && s.Title != "日出") {
		t.Fatalf("first = %+v %v", s, ok)
	}
	if _, err := c.Playlist(context.Background(), "睡觉", nil); err != ErrNoPlaylist {
		t.Fatalf("err = %v", err)
	}
}

func TestShuffleDefaultCoversEverySongOnce(t *testing.T) {
	order := Shuffle(5)
	seen := map[int]bool{}
	for _, i := range order {
		seen[i] = true
	}
	if len(order) != 5 || len(seen) != 5 {
		t.Fatalf("order %v", order)
	}
}

func TestWrongKeyIsAnError(t *testing.T) {
	srv, _ := fakeHusic(t)
	if _, err := NewClient(srv.URL, "bad").Liked(context.Background(), nil); err == nil {
		t.Fatal("want error")
	}
}

func TestPlaylistNames(t *testing.T) {
	srv, _ := fakeHusic(t)
	names, err := NewClient(srv.URL, "svc-key").Playlists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(names, ",") != "早上听,运动" {
		t.Fatalf("names %v", names)
	}
}

func TestCommandPostsToRemoteAndMapsNoPlayer(t *testing.T) {
	var got string
	open := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/remote/command" || r.Header.Get("Authorization") != "Bearer svc-key" {
			http.Error(w, "no", 403)
			return
		}
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		if !open {
			http.Error(w, `{"error":"no player open"}`, 409)
			return
		}
		w.WriteHeader(202)
		w.Write([]byte(`{"player":"exe-1"}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "svc-key")
	if err := c.Command(context.Background(), "play", "playlist:开车"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"action":"play"`) || !strings.Contains(got, `"what":"playlist:开车"`) {
		t.Fatalf("body %s", got)
	}
	open = false
	if err := c.Command(context.Background(), "next", ""); err != ErrNoPlayer {
		t.Fatalf("err %v", err)
	}
}

func TestNowPlaying(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/remote/state" || r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "no", 403)
			return
		}
		w.Write([]byte(`{"open":true,"title":"晴天","artist":"周杰伦","playing":true}`))
	}))
	defer srv.Close()
	st, err := NewClient(srv.URL, "k").NowPlaying(context.Background())
	if err != nil || !st.Open || st.Title != "晴天" || st.Artist != "周杰伦" || !st.Playing {
		t.Fatalf("%+v %v", st, err)
	}
}
