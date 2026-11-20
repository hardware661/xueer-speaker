package schedule

import (
	"testing"
	"time"
)

var sh = time.FixedZone("CST", 8*3600)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, sh)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNextRespectsTimeAndWeekdays(t *testing.T) {
	// 2026-11-16 is a Monday.
	weekdays := Entry{Hour: 8, Minute: 0, Days: Weekdays}
	cases := []struct {
		e    Entry
		from string
		want string
	}{
		{weekdays, "2026-11-16 07:59", "2026-11-16 08:00"},
		{weekdays, "2026-11-16 08:00", "2026-11-17 08:00"}, // not the same minute twice
		{weekdays, "2026-11-20 09:00", "2026-11-23 08:00"}, // Friday after → Monday
		{Entry{Hour: 22, Minute: 30}, "2026-11-21 23:00", "2026-11-22 22:30"},
		{Entry{Hour: 7, Minute: 15, Days: []time.Weekday{time.Sunday}}, "2026-11-16 00:00", "2026-11-22 07:15"},
	}
	for _, c := range cases {
		got, ok := c.e.Next(at(c.from))
		if !ok || !got.Equal(at(c.want)) {
			t.Errorf("%+v from %s = %v, want %s", c.e, c.from, got, c.want)
		}
	}
}

func TestDisabledNeverFires(t *testing.T) {
	if _, ok := (Entry{Hour: 8, Disabled: true}).Next(at("2026-11-16 00:00")); ok {
		t.Fatal("disabled entry fired")
	}
}

func TestValidate(t *testing.T) {
	good := Entry{Hour: 8, Minute: 0, Action: ActionPlay, Arg: "liked"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, e := range []Entry{
		{Hour: 24, Action: ActionPlay},
		{Minute: 60, Action: ActionStop},
		{Action: "dance"},
		{Action: ActionSay},                                         // say needs text
		{Action: ActionSay, Arg: string(make([]rune, 81))},          // too long
		{Action: ActionPlay, Arg: "liked", Days: []time.Weekday{9}}, // bad weekday
	} {
		if e.Validate() == nil {
			t.Errorf("%+v should be invalid", e)
		}
	}
}

func TestDueFiresEachEntryOncePerMinute(t *testing.T) {
	entries := []Entry{{ID: 1, Hour: 8, Action: ActionPlay, Arg: "liked"}, {ID: 2, Hour: 9, Action: ActionStop}}
	r := NewRunner()
	if got := r.Due(entries, at("2026-11-16 07:59")); len(got) != 0 {
		t.Fatalf("early: %+v", got)
	}
	if got := r.Due(entries, at("2026-11-16 08:00").Add(20*time.Second)); len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("08:00: %+v", got)
	}
	if got := r.Due(entries, at("2026-11-16 08:00").Add(50*time.Second)); len(got) != 0 {
		t.Fatalf("same minute fired twice: %+v", got)
	}
	if got := r.Due(entries, at("2026-11-16 09:00").Add(time.Second)); len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("09:00: %+v", got)
	}
}

func TestDueCatchesUpOnlyWithinTwoMinutes(t *testing.T) {
	// A restart at 08:01 still plays the 08:00 alarm; one at 08:30 does not.
	entries := []Entry{{ID: 1, Hour: 8, Action: ActionPlay, Arg: "liked"}}
	if got := NewRunner().Due(entries, at("2026-11-16 08:01").Add(30*time.Second)); len(got) != 1 {
		t.Fatalf("08:01:30 restart missed the 08:00 entry: %+v", got)
	}
	if got := NewRunner().Due(entries, at("2026-11-16 08:30")); len(got) != 0 {
		t.Fatalf("08:30 restart replayed the 08:00 entry: %+v", got)
	}
}

// "just once" — fires on its date only, then never again.
func TestOnceFiresOnItsDateOnly(t *testing.T) {
	e := Entry{Hour: 8, Minute: 0, Date: "2026-11-17", Action: ActionPlay, Arg: "liked"}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	got, ok := e.Next(at("2026-11-16 09:00"))
	if !ok || !got.Equal(at("2026-11-17 08:00")) {
		t.Fatalf("next = %v %v", got, ok)
	}
	if _, ok := e.Next(at("2026-11-17 08:00")); ok {
		t.Fatal("a once entry fired again after its date")
	}
	for _, bad := range []Entry{
		{Hour: 8, Date: "11-17", Action: ActionStop},
		{Hour: 8, Date: "2026-11-17", Days: Weekdays, Action: ActionStop}, // once has no weekdays
	} {
		if bad.Validate() == nil {
			t.Errorf("%+v should be invalid", bad)
		}
	}
}

// a restart right after an entry fired (08:00, released at 08:01)
// must not fire it again; the runner starts from what already ran.
func TestRunnerRemembersAcrossRestarts(t *testing.T) {
	entries := []Entry{{ID: 1, Hour: 8, Action: ActionPlay, Arg: "liked"}}
	r := NewRunner()
	fired := r.Due(entries, at("2026-11-16 08:00").Add(10*time.Second))
	if len(fired) != 1 {
		t.Fatalf("fired %+v", fired)
	}
	saved := r.Fired()
	r2 := NewRunnerFrom(saved)
	if got := r2.Due(entries, at("2026-11-16 08:01")); len(got) != 0 {
		t.Fatalf("fired twice across a restart: %+v", got)
	}
}

func TestPendingIsNotRecordedUntilAccepted(t *testing.T) {
	entries := []Entry{{ID: 1, Hour: 8, Action: ActionStop}, {ID: 2, Hour: 8, Action: ActionStop}}
	now := at("2026-11-16 08:00").Add(15 * time.Second)
	r := NewRunner()
	pending := r.Pending(entries, now)
	if len(pending) != 2 || len(r.Fired()) != 0 {
		t.Fatalf("pending=%v fired=%v", pending, r.Fired())
	}
	r.MarkFired(pending[0])
	restarted := NewRunnerFrom(r.Fired())
	got := restarted.Pending(entries, now.Add(15*time.Second))
	if len(got) != 1 || got[0].Entry.ID != 2 {
		t.Fatalf("expected only the unaccepted action, got %v", got)
	}
}
