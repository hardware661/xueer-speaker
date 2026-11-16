package conductor

import (
	"testing"
	"time"

	"github.com/hardware661/xueer-speaker/internal/audio"
)

func TestPlayStart(t *testing.T) {
	t0 := time.Date(2026, 11, 15, 0, 42, 0, 0, time.UTC)
	at := func(s float64, whole bool) audio.Fetch {
		return audio.Fetch{At: t0.Add(time.Duration(s * float64(time.Second))), Whole: whole}
	}
	cases := []struct {
		name string
		f    []audio.Fetch
		want float64 // -1 = not started
	}{
		{"short line: first fetch plays", []audio.Fetch{at(2.4, true)}, 2.4},
		{"probing: whole then tail", []audio.Fetch{at(3.4, true), at(5.6, false)}, -1},
		{"long line 00:42: whole, tail, start", []audio.Fetch{at(3.3, true), at(6.4, false), at(8.5, true)}, 8.5},
	}
	for _, c := range cases {
		got := playStart(c.f)
		if c.want < 0 {
			if !got.IsZero() {
				t.Errorf("%s: started at %v", c.name, got.Sub(t0))
			}
			continue
		}
		if want := t0.Add(time.Duration(c.want * float64(time.Second))); !got.Equal(want) {
			t.Errorf("%s: %v, want %v", c.name, got.Sub(t0), want.Sub(t0))
		}
	}
}
