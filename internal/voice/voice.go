// Package voice turns a line of text into an mp3 in Xiaoxiao's voice and
// knows how long it plays. It does not know about speakers or queues.
//
// Text keeps its commas and full stops (the owner chose those pauses,
// 2026-11-14); emoji, "～" and parenthesised stage directions go. Lines over
// MaxRunes are refused, never cut: a half sentence read aloud is worse than
// asking the caller to say it shorter.
package voice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// DefaultVoice is the owner's pick: Xiaoxiao at normal speed.
const DefaultVoice = "zh-CN-XiaoxiaoNeural"

// MaxRunes is the longest line spoken.
const MaxRunes = 80

var (
	ErrTooLong = fmt.Errorf("voice: longer than %d characters, say it shorter", MaxRunes)
	ErrEmpty   = errors.New("voice: nothing left to say after cleaning")
)

// Engine synthesizes text into an mp3 file at out.
type Engine interface {
	Synthesize(ctx context.Context, text, voice, out string) error
}

// Clip is a rendered line.
type Clip struct {
	Path     string
	Duration time.Duration
	Text     string
}

// Voice renders lines with an Engine and caches them under dir.
type Voice struct {
	dir    string
	engine Engine
	voice  string
}

// New returns a Voice caching into dir.
func New(dir string, e Engine) *Voice {
	return &Voice{dir: dir, engine: e, voice: DefaultVoice}
}

var (
	stageDirection = regexp.MustCompile(`[（(][^（）()]{0,20}[）)]`)
	tildes         = regexp.MustCompile(`[～~]+`)
	spaces         = regexp.MustCompile(`\s+`)
)

// Clean prepares text for reading aloud.
func Clean(s string) string {
	s = stageDirection.ReplaceAllString(s, "")
	s = tildes.ReplaceAllString(s, "，")
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 0x1F000, r >= 0x2600 && r <= 0x27BF, r == 0xFE0F, r == 0x200D:
			return -1
		case unicode.IsControl(r):
			return ' '
		}
		return r
	}, s)
	s = spaces.ReplaceAllString(strings.TrimSpace(s), " ")
	for _, dup := range []string{"，，", "，。", "，！", "，？"} {
		for strings.Contains(s, dup) {
			s = strings.ReplaceAll(s, dup, dup[len("，"):])
		}
	}
	s = strings.TrimLeft(s, "，")
	return strings.TrimRight(s, "，")
}

// Render returns the clip for text, synthesizing it on a cache miss.
func (v *Voice) Render(ctx context.Context, text string) (Clip, error) {
	text = Clean(text)
	if text == "" {
		return Clip{}, ErrEmpty
	}
	if utf8.RuneCountInString(text) > MaxRunes {
		return Clip{}, ErrTooLong
	}
	sum := sha256.Sum256([]byte(v.voice + "\x00" + text))
	path := filepath.Join(v.dir, hex.EncodeToString(sum[:16])+".mp3")
	if _, err := os.Stat(path); err != nil {
		tmp := path + ".part"
		if err := v.engine.Synthesize(ctx, text, v.voice, tmp); err != nil {
			os.Remove(tmp)
			return Clip{}, fmt.Errorf("voice: %w", err)
		}
		if err := os.Rename(tmp, path); err != nil {
			return Clip{}, err
		}
	} else {
		now := time.Now()
		os.Chtimes(path, now, now) // keeps it out of the next Prune
	}
	d, err := MP3Duration(path)
	if err != nil {
		os.Remove(path)
		return Clip{}, err
	}
	return Clip{Path: path, Duration: d, Text: text}, nil
}

// Prune deletes cached clips not used for maxAge.
func (v *Voice) Prune(maxAge time.Duration) {
	entries, _ := os.ReadDir(v.dir)
	cut := time.Now().Add(-maxAge)
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.ModTime().Before(cut) {
			os.Remove(filepath.Join(v.dir, e.Name()))
		}
	}
}

// EdgeCLI runs the upstream edge-tts command (ADR 0001).
type EdgeCLI struct {
	Bin string // path to edge-tts
}

// Synthesize implements Engine.
func (e EdgeCLI) Synthesize(ctx context.Context, text, voice, out string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.Bin, "--voice", voice, "--text", text, "--write-media", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(b))
		if len(msg) > 200 {
			msg = msg[len(msg)-200:]
		}
		return fmt.Errorf("edge-tts: %w: %s", err, msg)
	}
	return nil
}
