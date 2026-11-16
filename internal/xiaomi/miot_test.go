package xiaomi_test

import (
	"context"
	"testing"
)

func TestActionIsSignedWithXiaomiioToken(t *testing.T) {
	f, sp := newSpeaker(t)
	if err := sp.Action(context.Background(), 7, 3, []any{"主人，测试一下。"}); err != nil {
		t.Fatalf("Action: %v", err)
	}
	if len(f.Actions) != 1 {
		t.Fatalf("actions = %v", f.Actions)
	}
	a := f.Actions[0]
	if a["did"] != "123456" || a["siid"] != float64(7) || a["aiid"] != float64(3) {
		t.Fatalf("action = %v", a)
	}
	if in, _ := a["in"].([]any); len(in) != 1 || in[0] != "主人，测试一下。" {
		t.Fatalf("in = %v", a["in"])
	}
}

func TestConversationsNewestFirst(t *testing.T) {
	f, sp := newSpeaker(t)
	f.Records = []map[string]any{
		{"time": 1794650002000, "query": "雪儿放歌", "requestId": "r2", "answers": []any{map[string]any{"type": "TTS"}}},
		{"time": 1794650001000, "query": "现在几点", "requestId": "r1", "answers": []any{}},
	}
	recs, err := sp.Conversations(context.Background(), 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0].Query != "雪儿放歌" || recs[0].RequestID != "r2" || recs[0].TimeMS != 1794650002000 {
		t.Fatalf("records = %+v", recs)
	}
}

func TestDoRunsTheTextDirective(t *testing.T) {
	f, sp := newSpeaker(t)
	if err := sp.Do(context.Background(), "明天八点叫我", true); err != nil {
		t.Fatalf("Do: %v", err)
	}
	a := f.Actions[0]
	if a["siid"] != float64(7) || a["aiid"] != float64(4) {
		t.Fatalf("action = %v", a)
	}
	if in, _ := a["in"].([]any); len(in) != 2 || in[0] != "明天八点叫我" || in[1] != true {
		t.Fatalf("in = %v", a["in"])
	}
}

func TestDoNotDisturbRoundTrip(t *testing.T) {
	_, sp := newSpeaker(t)
	ctx := context.Background()
	if on, err := sp.DoNotDisturb(ctx); err != nil || on {
		t.Fatalf("initial = %v %v", on, err)
	}
	if err := sp.SetDoNotDisturb(ctx, true); err != nil {
		t.Fatalf("set: %v", err)
	}
	if on, err := sp.DoNotDisturb(ctx); err != nil || !on {
		t.Fatalf("after set = %v %v", on, err)
	}
}
