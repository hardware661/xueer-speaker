// Package schedule is the speaker's own timetable: play music, stop it, or
// say a line at a time of day on chosen weekdays. It is the one place timed
// things live, so the panel can show and edit all of them. Times are the
// service's local time (TZ).
package schedule

import (
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode/utf8"
)

// Action is what an entry does.
type Action string

const (
	ActionPlay Action = "play" // Arg: "liked" or "playlist:<name>"
	ActionStop Action = "stop"
	ActionSay  Action = "say" // Arg: the line
)

// Weekdays is Monday to Friday.
var Weekdays = []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}

// Entry is one timed thing. Empty Days means every day.
type Entry struct {
	ID       int64          `json:"id"`
	Hour     int            `json:"hour"`
	Minute   int            `json:"minute"`
	Days     []time.Weekday `json:"days,omitempty"`
	Date     string         `json:"date,omitempty"` // "2006-01-02": just once, that day
	Action   Action         `json:"action"`
	Arg      string         `json:"arg,omitempty"`
	Disabled bool           `json:"disabled,omitempty"`
}

// Validate checks an entry from the panel.
func (e Entry) Validate() error {
	if e.Hour < 0 || e.Hour > 23 || e.Minute < 0 || e.Minute > 59 {
		return errors.New("时间不对")
	}
	if e.Date != "" {
		if _, err := time.Parse(time.DateOnly, e.Date); err != nil {
			return errors.New("日期不对")
		}
		if len(e.Days) > 0 {
			return errors.New("只一次的定时不选星期")
		}
	}
	for _, d := range e.Days {
		if d < time.Sunday || d > time.Saturday {
			return errors.New("星期不对")
		}
	}
	switch e.Action {
	case ActionPlay, ActionStop:
	case ActionSay:
		if n := utf8.RuneCountInString(e.Arg); n == 0 || n > 80 {
			return errors.New("要说的话 1～80 字")
		}
	default:
		return fmt.Errorf("不认识的动作 %q", e.Action)
	}
	return nil
}

func (e Entry) onDay(d time.Weekday) bool { return len(e.Days) == 0 || slices.Contains(e.Days, d) }

// Next is the first time strictly after from that the entry fires.
func (e Entry) Next(from time.Time) (time.Time, bool) {
	if e.Disabled {
		return time.Time{}, false
	}
	if e.Date != "" {
		d, err := time.ParseInLocation(time.DateOnly, e.Date, from.Location())
		if err != nil {
			return time.Time{}, false
		}
		t := time.Date(d.Year(), d.Month(), d.Day(), e.Hour, e.Minute, 0, 0, from.Location())
		return t, t.After(from)
	}
	day := time.Date(from.Year(), from.Month(), from.Day(), e.Hour, e.Minute, 0, 0, from.Location())
	for i := range 8 {
		t := day.AddDate(0, 0, i)
		if t.After(from) && e.onDay(t.Weekday()) {
			return t, true
		}
	}
	return time.Time{}, false
}

// catchUp is how late a missed minute may still fire (a restart at 08:01).
const catchUp = 2 * time.Minute

// Runner decides which entries are due; it remembers what already fired so
// a minute is never run twice. Not goroutine-safe: one ticker calls Due.
type Runner struct {
	fired map[int64]time.Time // entry → the slot it last ran
}

// NewRunner returns an empty runner.
func NewRunner() *Runner { return &Runner{fired: map[int64]time.Time{}} }

// NewRunnerFrom starts from what already ran (saved with Fired), so a
// restart never fires the same slot twice.
func NewRunnerFrom(fired map[int64]time.Time) *Runner {
	r := NewRunner()
	for id, t := range fired {
		r.fired[id] = t
	}
	return r
}

// Fired is a copy of what ran, for saving.
func (r *Runner) Fired() map[int64]time.Time {
	out := make(map[int64]time.Time, len(r.fired))
	for id, t := range r.fired {
		out[id] = t
	}
	return out
}

// Occurrence identifies one scheduled action and its deduplication slot.
type Occurrence struct {
	Entry Entry
	slot  time.Time
}

// Pending finds due actions without recording that they ran.
func (r *Runner) Pending(entries []Entry, now time.Time) []Occurrence {
	var out []Occurrence
	for _, e := range entries {
		slot, ok := e.Next(now.Add(-catchUp))
		if !ok || slot.After(now) || r.fired[e.ID].Equal(slot) {
			continue
		}
		out = append(out, Occurrence{Entry: e, slot: slot})
	}
	return out
}

func (r *Runner) MarkFired(occurrence Occurrence) {
	r.fired[occurrence.Entry.ID] = occurrence.slot
}

// Due returns and records the due actions in one step.
func (r *Runner) Due(entries []Entry, now time.Time) []Entry {
	var out []Entry
	for _, occurrence := range r.Pending(entries, now) {
		r.MarkFired(occurrence)
		out = append(out, occurrence.Entry)
	}
	return out
}
