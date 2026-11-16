// Package control holds the speaker's rules shared by the service and the
// serverless CLI: which sentences Xiao Ai may be told to carry out, how a
// do-not-disturb "until" is read, and the do-not-disturb hold itself.
package control

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrRefused: the sentence is not something an AI may make Xiao Ai do.
var ErrRefused = errors.New("这句不能让小爱执行：只用于闹钟、提醒、查信息和家里的设备，不能买东西、付款、打电话或发消息")

// refused are words that make a directive spend money or reach other
// people; an AI must never have Xiao Ai do those on the owner's behalf.
var refused = []string{"买", "下单", "付款", "支付", "转账", "充值", "打电话", "拨打", "呼叫", "发短信", "发消息", "发微信", "视频通话"}

// CheckDirective returns ErrRefused for an empty or unsafe sentence.
func CheckDirective(text string) error {
	t := strings.TrimSpace(text)
	if t == "" {
		return fmt.Errorf("%w（空的）", ErrRefused)
	}
	for _, w := range refused {
		if strings.Contains(t, w) {
			return ErrRefused
		}
	}
	return nil
}

// MaxDND is the longest do-not-disturb: an AI that forgets to switch it off
// does not silence the speaker for days.
const MaxDND = 12 * time.Hour

// ParseUntil reads "08:00" (the next time the clock shows it, at most a day
// away), "2h" / "90m" (from now, at most MaxDND) or "" (MaxDND).
func ParseUntil(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	var t time.Time
	switch {
	case s == "":
		t = now.Add(MaxDND)
	case strings.Contains(s, ":"):
		hm, err := time.Parse("15:04", s)
		if err != nil {
			return time.Time{}, fmt.Errorf("到几点要写成 08:00 这样: %w", err)
		}
		t = time.Date(now.Year(), now.Month(), now.Day(), hm.Hour(), hm.Minute(), 0, 0, now.Location())
		if !t.After(now) {
			t = t.AddDate(0, 0, 1)
		}
		return t, nil // the owner named the time: keep it
	default:
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return time.Time{}, fmt.Errorf("到几点要写成 08:00 或 2h 这样")
		}
		t = now.Add(d)
	}
	if t.After(now.Add(MaxDND)) {
		t = now.Add(MaxDND)
	}
	return t, nil
}

// Quiet is our own do-not-disturb: while it is on, lines are kept but not
// spoken. Safe for concurrent use.
type Quiet struct {
	Now func() time.Time

	mu    sync.Mutex
	until time.Time
	held  int
}

func (q *Quiet) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now()
}

// Start switches it on until t (a new Start moves the end, keeps the count).
func (q *Quiet) Start(t time.Time) {
	q.mu.Lock()
	q.until = t
	q.mu.Unlock()
}

// On reports whether lines are being held right now.
func (q *Quiet) On() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return !q.until.IsZero() && q.now().Before(q.until)
}

// Until is when it ends; zero when off.
func (q *Quiet) Until() time.Time {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.until.IsZero() || !q.now().Before(q.until) {
		return time.Time{}
	}
	return q.until
}

// Hold counts one line kept quiet.
func (q *Quiet) Hold() {
	q.mu.Lock()
	q.held++
	q.mu.Unlock()
}

// Stop switches it off and returns how many lines were held (0 if it was
// already off).
func (q *Quiet) Stop() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.until.IsZero() {
		return 0
	}
	n := q.held
	q.until, q.held = time.Time{}, 0
	return n
}

// Expired reports, once, that the end time passed by itself, with the
// number of lines held; the caller then switches Xiao Ai's own one off.
func (q *Quiet) Expired() (int, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.until.IsZero() || q.now().Before(q.until) {
		return 0, false
	}
	n := q.held
	q.until, q.held = time.Time{}, 0
	return n, true
}
