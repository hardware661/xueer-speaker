package xiaomi

import (
	"context"
	"testing"
)

func TestActionIsSignedWithXiaomiioToken(t *testing.T) {
	f, sp := newSpeaker(t)
	if err := sp.Action(context.Background(), 7, 3, []any{"主人，测试一下。"}); err != nil {
		t.Fatalf("Action: %v", err)
	}
	if len(f.actions) != 1 {
		t.Fatalf("actions = %v", f.actions)
	}
	a := f.actions[0]
	if a["did"] != "123456" || a["siid"] != float64(7) || a["aiid"] != float64(3) {
		t.Fatalf("action = %v", a)
	}
	if in, _ := a["in"].([]any); len(in) != 1 || in[0] != "主人，测试一下。" {
		t.Fatalf("in = %v", a["in"])
	}
}

func TestConversationsNewestFirst(t *testing.T) {
	f, sp := newSpeaker(t)
	f.records = []map[string]any{
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
