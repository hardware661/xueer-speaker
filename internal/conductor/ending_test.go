package conductor

import (
	"testing"

	"github.com/hardware661/xueer-speaker/internal/xiaomi"
)

func st(state xiaomi.State, posS, durS int64) xiaomi.Status {
	return xiaomi.Status{State: state, PositionMS: posS * 1000, DurationMS: durS * 1000}
}

// Songloft's rules (competitors §3.3): only the last 15 s count, and either a
// position that fell back to the start or a stopped player means "finished".
func TestSongEnded(t *testing.T) {
	cases := []struct {
		name      string
		prev, cur xiaomi.Status
		want      bool
	}{
		{"middle of the song", st(xiaomi.StatePlaying, 100, 200), st(xiaomi.StatePlaying, 101, 200), false},
		{"stopped far from the end is not an ending", st(xiaomi.StatePlaying, 100, 200), st(xiaomi.StateStopped, 100, 200), false},
		{"stopped in the last 15 s", st(xiaomi.StatePlaying, 190, 200), st(xiaomi.StateIdle, 0, 0), true},
		{"wrapped back to the start (speaker loops the url)", st(xiaomi.StatePlaying, 195, 200), st(xiaomi.StatePlaying, 1, 200), true},
		{"wrap without having passed 10 s is a restart, not an end", st(xiaomi.StatePlaying, 5, 200), st(xiaomi.StatePlaying, 1, 200), false},
		{"status lies (idle) but position still moves", st(xiaomi.StatePlaying, 120, 200), st(xiaomi.StateIdle, 121, 200), false},
	}
	for _, c := range cases {
		if got := songEnded(c.prev, c.cur); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
