package control

import (
	"errors"
	"testing"
	"time"
)

func TestCheckDirectiveRefusesMoneyAndMessages(t *testing.T) {
	for _, ok := range []string{"明天八点叫我", "十分钟后提醒我关火", "把客厅灯关了", "现在几点"} {
		if err := CheckDirective(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"帮我买一箱牛奶", "下单", "给妈妈打电话", "发短信给他", "发微信说我到了", "支付宝付款", "转账一百块", ""} {
		if err := CheckDirective(bad); !errors.Is(err, ErrRefused) {
			t.Errorf("%q not refused: %v", bad, err)
		}
	}
}

func TestParseUntil(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	now := time.Date(2026, 11, 16, 23, 30, 0, 0, loc)
	cases := map[string]time.Time{
		"08:00": time.Date(2026, 11, 17, 8, 0, 0, 0, loc), // tomorrow morning
		"23:45": time.Date(2026, 11, 16, 23, 45, 0, 0, loc),
		"":      now.Add(MaxDND),
		"2h":    now.Add(2 * time.Hour),
		"30h":   now.Add(MaxDND), // capped
	}
	for in, want := range cases {
		got, err := ParseUntil(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("ParseUntil(%q) = %v %v, want %v", in, got, err, want)
		}
	}
	if _, err := ParseUntil("8点", now); err == nil {
		t.Error("bad time accepted")
	}
}

func TestQuietHoldsLinesAndReportsOnce(t *testing.T) {
	now := time.Date(2026, 11, 16, 23, 0, 0, 0, time.UTC)
	q := &Quiet{Now: func() time.Time { return now }}
	if q.On() {
		t.Fatal("on at start")
	}
	q.Start(now.Add(8 * time.Hour))
	if !q.On() {
		t.Fatal("not on")
	}
	q.Hold()
	q.Hold()
	if held := q.Stop(); held != 2 {
		t.Fatalf("held = %d", held)
	}
	if q.On() || q.Stop() != 0 {
		t.Fatal("stop twice")
	}
	q.Start(now.Add(time.Hour))
	q.Hold()
	now = now.Add(2 * time.Hour)
	if q.On() {
		t.Fatal("still on after until")
	}
	if held, expired := q.Expired(); !expired || held != 1 {
		t.Fatalf("expired = %d %v", held, expired)
	}
	if _, expired := q.Expired(); expired {
		t.Fatal("expired reported twice")
	}
}
