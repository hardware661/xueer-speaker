package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/hardware661/xueer-speaker/internal/schedule"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "speaker.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSaysAreUpdatedAndListedNewestFirst(t *testing.T) {
	s := open(t)
	t0 := time.Date(2026, 11, 14, 23, 0, 0, 0, time.UTC)
	s.AddSay(Say{ID: "a", At: t0, Source: "claude", Text: "主人，好了。"})
	s.AddSay(Say{ID: "b", At: t0.Add(time.Minute), Source: "codex", Text: "主人，提交了。"})
	s.FinishSay("a", "edge", "")
	s.FinishSay("b", "native", "edge-tts: timeout")
	got, err := s.Says(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "b" || got[0].Voice != "native" || got[0].Err == "" || got[1].Voice != "edge" || !got[1].At.Equal(t0) {
		t.Fatalf("says = %+v", got)
	}
}

func TestHeardIsKept(t *testing.T) {
	s := open(t)
	s.AddHeard(Heard{At: time.Unix(100, 0), Query: "播放我的歌单", Action: "放「我喜欢」"})
	got, _ := s.Heard(5)
	if len(got) != 1 || got[0].Query != "播放我的歌单" || got[0].Action != "放「我喜欢」" {
		t.Fatalf("heard = %+v", got)
	}
}

func TestSettingsSurviveReopen(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(filepath.Join(dir, "x.db"))
	if v := s.Setting("listener", "on"); v != "on" {
		t.Fatalf("default = %q", v)
	}
	s.SetSetting("listener", "off")
	s.Close()
	s, _ = Open(filepath.Join(dir, "x.db"))
	defer s.Close()
	if v := s.Setting("listener", "on"); v != "off" {
		t.Fatalf("after reopen = %q", v)
	}
}

func TestPruneDropsOldRows(t *testing.T) {
	s := open(t)
	now := time.Date(2026, 12, 20, 0, 0, 0, 0, time.UTC)
	s.AddSay(Say{ID: "old", At: now.Add(-31 * 24 * time.Hour), Source: "x", Text: "旧"})
	s.AddSay(Say{ID: "new", At: now.Add(-time.Hour), Source: "x", Text: "新"})
	s.AddHeard(Heard{At: now.Add(-40 * 24 * time.Hour), Query: "旧"})
	s.Prune(now.Add(-30 * 24 * time.Hour))
	says, _ := s.Says(10)
	heard, _ := s.Heard(10)
	if len(says) != 1 || says[0].ID != "new" || len(heard) != 0 {
		t.Fatalf("says=%+v heard=%+v", says, heard)
	}
}

func TestSchedulesCRUD(t *testing.T) {
	s := open(t)
	id, err := s.AddSchedule(schedule.Entry{Hour: 8, Minute: 0, Days: schedule.Weekdays, Action: schedule.ActionPlay, Arg: "liked"})
	if err != nil || id == 0 {
		t.Fatal(id, err)
	}
	s.AddSchedule(schedule.Entry{Hour: 7, Minute: 30, Action: schedule.ActionSay, Arg: "主人，起床啦。"})
	got, _ := s.Schedules()
	if len(got) != 2 || got[0].Hour != 7 || got[1].ID != id || len(got[1].Days) != 5 {
		t.Fatalf("schedules = %+v", got)
	}
	e := got[1]
	e.Disabled, e.Minute = true, 5
	if err := s.UpdateSchedule(e); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSchedule(got[0].ID); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Schedules()
	if len(got) != 1 || !got[0].Disabled || got[0].Minute != 5 {
		t.Fatalf("after update/delete = %+v", got)
	}
	if err := s.UpdateSchedule(schedule.Entry{ID: 999, Action: schedule.ActionStop}); err != ErrNotFound {
		t.Fatalf("update missing = %v", err)
	}
}

// a line still queued when the service stopped never gets a
// result; on start it is marked so the panel does not show "排队中" forever.
func TestOrphanedQueuedLinesAreMarkedOnStart(t *testing.T) {
	s := open(t)
	t0 := time.Date(2026, 11, 15, 17, 33, 0, 0, time.UTC)
	s.AddSay(Say{ID: "lost", At: t0, Source: "claude", Text: "念到一半"})
	s.AddSay(Say{ID: "done", At: t0, Source: "claude", Text: "好了"})
	s.FinishSay("done", "pc", "")
	if n, err := s.MarkOrphans("服务重启了，这句没念成"); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	got, _ := s.Says(10)
	for _, x := range got {
		if x.ID == "lost" && x.Err != "服务重启了，这句没念成" || x.ID == "done" && x.Err != "" {
			t.Fatalf("says = %+v", got)
		}
	}
}
