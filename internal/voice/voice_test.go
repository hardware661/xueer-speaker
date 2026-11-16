package voice

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanKeepsPunctuationDropsEmoji(t *testing.T) {
	cases := map[string]string{
		"主人～雪儿做完啦😊，你快来看看嘛。":       "主人，雪儿做完啦，你快来看看嘛。",
		"  Husic 的登录修好了（耳朵竖起来）  ": "Husic 的登录修好了",
		"告警：CPU 90%！":             "告警：CPU 90%！",
	}
	for in, want := range cases {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTooLongIsRejectedNotTruncated(t *testing.T) {
	v := New(t.TempDir(), &fakeEngine{})
	long := ""
	for range 81 {
		long += "啊"
	}
	if _, err := v.Render(context.Background(), long); !errors.Is(err, ErrTooLong) {
		t.Fatalf("err = %v, want ErrTooLong", err)
	}
	if _, err := v.Render(context.Background(), "😊～"); !errors.Is(err, ErrEmpty) {
		t.Fatalf("emoji-only err = %v, want ErrEmpty", err)
	}
}

func TestMP3DurationFromFrames(t *testing.T) {
	d, err := MP3Duration("testdata/xiaoxiao.mp3")
	if err != nil {
		t.Fatal(err)
	}
	// edge-tts 24kHz 48kbit/s mono; mutagen reads 7.248 s for this file (the
	// owner's chosen sample); allow a frame or two of slack either way.
	if d < 7100*time.Millisecond || d > 7400*time.Millisecond {
		t.Fatalf("duration = %v", d)
	}
}

func TestRenderCachesByText(t *testing.T) {
	eng := &fakeEngine{}
	v := New(t.TempDir(), eng)
	ctx := context.Background()
	a, err := v.Render(ctx, "主人，测试。")
	if err != nil {
		t.Fatal(err)
	}
	b, err := v.Render(ctx, "主人，测试。")
	if err != nil {
		t.Fatal(err)
	}
	if eng.calls != 1 || a.Path != b.Path || a.Duration <= 0 {
		t.Fatalf("calls=%d a=%+v b=%+v", eng.calls, a, b)
	}
	if eng.lastText != "主人，测试。" || eng.lastVoice != DefaultVoice {
		t.Fatalf("engine got %q %q", eng.lastText, eng.lastVoice)
	}
}

func TestEngineFailureSurfaces(t *testing.T) {
	v := New(t.TempDir(), &fakeEngine{fail: true})
	if _, err := v.Render(context.Background(), "主人。"); err == nil {
		t.Fatal("want error")
	}
}

type fakeEngine struct {
	calls               int
	fail                bool
	lastText, lastVoice string
}

func (f *fakeEngine) Synthesize(_ context.Context, text, voice, out string) error {
	f.calls++
	f.lastText, f.lastVoice = text, voice
	if f.fail {
		return errors.New("edge down")
	}
	src, err := os.ReadFile(filepath.Join("testdata", "xiaoxiao.mp3"))
	if err != nil {
		return err
	}
	return os.WriteFile(out, src, 0o600)
}
