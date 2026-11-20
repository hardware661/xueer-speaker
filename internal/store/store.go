// Package store keeps what the panel shows and what must survive a restart:
// lines spoken, sentences heard, and the panel's switches. Logs
// stay 30 days and are not backed up: they say what the owner was doing.
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	_ "modernc.org/sqlite"

	"github.com/hardware661/xueer-speaker/internal/schedule"
)

// ErrNotFound is returned for a missing schedule entry.
var ErrNotFound = errors.New("store: not found")

// Say is one line handed to the speaker.
type Say struct {
	ID     string    `json:"id"`
	At     time.Time `json:"at"`
	Source string    `json:"source"`
	Text   string    `json:"text"`
	Voice  string    `json:"voice"` // edge / native / "" while queued
	Err    string    `json:"err,omitempty"`
}

// Heard is one sentence the owner said to Xiao Ai and what we did about it.
type Heard struct {
	At     time.Time `json:"at"`
	Query  string    `json:"query"`
	Action string    `json:"action"` // "" = not ours
}

// Store is safe for concurrent use.
type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS says (id TEXT PRIMARY KEY, at INTEGER NOT NULL, source TEXT NOT NULL,
  text TEXT NOT NULL, voice TEXT NOT NULL DEFAULT '', err TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS says_at ON says(at);
CREATE TABLE IF NOT EXISTS heard (at INTEGER NOT NULL, query TEXT NOT NULL, action TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS heard_at ON heard(at);
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS schedules (id INTEGER PRIMARY KEY AUTOINCREMENT, entry TEXT NOT NULL);`

// Open opens (and creates) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// AddSay records a queued line.
func (s *Store) AddSay(x Say) error {
	_, err := s.db.Exec(`INSERT INTO says (id, at, source, text) VALUES (?, ?, ?, ?)`, x.ID, x.At.UnixMilli(), x.Source, x.Text)
	return err
}

// FinishSay records how a line went.
// MarkOrphans gives every line still without a result (queued when the
// service stopped) the error text; it returns how many there were.
func (s *Store) MarkOrphans(errText string) (int64, error) {
	r, err := s.db.Exec(`UPDATE says SET err = ? WHERE COALESCE(voice, '') = '' AND COALESCE(err, '') = ''`, errText)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

func (s *Store) FinishSay(id, voice, errText string) error {
	_, err := s.db.Exec(`UPDATE says SET voice = ?, err = ? WHERE id = ?`, voice, errText, id)
	return err
}

// Says returns up to n lines, newest first.
func (s *Store) Says(n int) ([]Say, error) {
	rows, err := s.db.Query(`SELECT id, at, source, text, voice, err FROM says ORDER BY at DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Say
	for rows.Next() {
		var x Say
		var at int64
		if err := rows.Scan(&x.ID, &at, &x.Source, &x.Text, &x.Voice, &x.Err); err != nil {
			return nil, err
		}
		x.At = time.UnixMilli(at)
		out = append(out, x)
	}
	return out, rows.Err()
}

// AddHeard records a sentence from the conversation log.
func (s *Store) AddHeard(h Heard) error {
	_, err := s.db.Exec(`INSERT INTO heard (at, query, action) VALUES (?, ?, ?)`, h.At.UnixMilli(), h.Query, h.Action)
	return err
}

// Heard returns up to n sentences, newest first.
func (s *Store) Heard(n int) ([]Heard, error) {
	rows, err := s.db.Query(`SELECT at, query, action FROM heard ORDER BY at DESC LIMIT ?`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Heard
	for rows.Next() {
		var h Heard
		var at int64
		if err := rows.Scan(&at, &h.Query, &h.Action); err != nil {
			return nil, err
		}
		h.At = time.UnixMilli(at)
		out = append(out, h)
	}
	return out, rows.Err()
}

// Setting returns a switch's value, or def when unset or unreadable.
func (s *Store) Setting(key, def string) string {
	var v string
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return def
	}
	return v
}

// SetSetting stores a switch.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// Prune deletes log rows older than before.
func (s *Store) Prune(before time.Time) error {
	ms := before.UnixMilli()
	if _, err := s.db.Exec(`DELETE FROM says WHERE at < ?`, ms); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM heard WHERE at < ?`, ms)
	return err
}

// Schedules returns every entry, earliest time of day first.
func (s *Store) Schedules() ([]schedule.Entry, error) {
	rows, err := s.db.Query(`SELECT id, entry FROM schedules`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []schedule.Entry{}
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var e schedule.Entry
		if err := json.Unmarshal([]byte(raw), &e); err != nil {
			return nil, err
		}
		e.ID = id
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Hour*60+out[i].Minute < out[j].Hour*60+out[j].Minute
	})
	return out, rows.Err()
}

// AddSchedule stores a new entry and returns its id.
func (s *Store) AddSchedule(e schedule.Entry) (int64, error) {
	e.ID = 0
	raw, _ := json.Marshal(e)
	res, err := s.db.Exec(`INSERT INTO schedules (entry) VALUES (?)`, string(raw))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateSchedule replaces an entry.
func (s *Store) UpdateSchedule(e schedule.Entry) error {
	id := e.ID
	e.ID = 0
	raw, _ := json.Marshal(e)
	res, err := s.db.Exec(`UPDATE schedules SET entry = ? WHERE id = ?`, string(raw), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DisableCompletedSchedule disables a one-time entry only if it has not
// changed since the runner took its snapshot.
func (s *Store) DisableCompletedSchedule(e schedule.Entry) error {
	id := e.ID
	e.ID = 0
	before, err := json.Marshal(e)
	if err != nil {
		return err
	}
	e.Disabled = true
	after, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE schedules SET entry = ? WHERE id = ? AND entry = ?`, string(after), id, string(before))
	return err
}

// DeleteSchedule removes an entry.
func (s *Store) DeleteSchedule(id int64) error {
	res, err := s.db.Exec(`DELETE FROM schedules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
