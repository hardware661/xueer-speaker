package tools

import (
	"context"
	"strings"
	"testing"
)

func TestPersonaShapesTheTexts(t *testing.T) {
	p := Persona{Name: "小白", Owner: "老板"}
	for _, d := range []string{p.SayDescription(), p.SpeakerDescription(), p.Instructions()} {
		if strings.Contains(d, "雪儿") || strings.Contains(d, "主人") {
			t.Fatalf("persona leaked: %s", d)
		}
	}
	if !strings.Contains(Persona{}.SayDescription(), "雪儿") || !strings.Contains(Persona{}.SayDescription(), "主人") {
		t.Fatal("default persona is 雪儿 calling 主人")
	}
}

type nopBackend struct{ Backend }

func TestRunValidatesBeforeTouchingTheSpeaker(t *testing.T) {
	ctx := context.Background()
	bad := 120
	if _, err := Run(ctx, nopBackend{}, SpeakerIn{Action: "volume", Volume: &bad}); err == nil {
		t.Fatal("volume 120 accepted")
	}
	if _, err := Run(ctx, nopBackend{}, SpeakerIn{Action: "dnd"}); err == nil {
		t.Fatal("dnd without on accepted")
	}
	if _, err := Run(ctx, nopBackend{}, SpeakerIn{Action: "do", Text: "给他打电话"}); err == nil {
		t.Fatal("unsafe do accepted")
	}
}
