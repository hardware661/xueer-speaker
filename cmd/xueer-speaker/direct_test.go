package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hardware661/xueer-speaker/internal/tools"
	"github.com/hardware661/xueer-speaker/internal/xiaomi/xiaomitest"
)

// newDirect logs in to a fake cloud the way a user would (account,
// password, SMS code piped in) and returns the backend.
func newDirect(t *testing.T) (*direct, *xiaomitest.Cloud) {
	t.Helper()
	f := xiaomitest.New(t)
	old := endpoints
	endpoints = f.Endpoints()
	t.Cleanup(func() { endpoints = old })
	t.Setenv("XS_HOME", t.TempDir())
	d, err := openDirect(&bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	in := strings.NewReader("speaker@example.com\nsecret\n123456\n")
	if err := d.login(in); err != nil {
		t.Fatalf("login: %v", err)
	}
	if d.state().Speaker != "mina-dev-1" {
		t.Fatalf("speaker not chosen: %+v", d.state())
	}
	return d, f
}

func lastUbus(f *xiaomitest.Cloud) (path, method, message string) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if len(f.Ubus) == 0 {
		return "", "", ""
	}
	u := f.Ubus[len(f.Ubus)-1]
	return u.Get("path"), u.Get("method"), u.Get("message")
}

func TestDirectLoginSayAndLimits(t *testing.T) {
	d, f := newDirect(t)
	ctx := context.Background()
	if _, err := d.Say(ctx, "", "做好了～"); err != nil {
		t.Fatalf("say: %v", err)
	}
	if p, m, msg := lastUbus(f); p != "mibrain" || m != "text_to_speech" || !strings.Contains(msg, "做好了") {
		t.Fatalf("ubus = %s %s %s", p, m, msg)
	}
	if _, err := d.Say(ctx, "", "做好了～"); err == nil {
		t.Fatal("same line twice within 10 s")
	}
	// A fresh process (the next CLI call) still has the login and speaker.
	d2, err := openDirect(&bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d2.Say(ctx, "", "第二句。"); err != nil {
		t.Fatalf("say after reopen: %v", err)
	}
}

func TestDirectDNDKeepsLinesThenSaysHowMany(t *testing.T) {
	d, f := newDirect(t)
	ctx := context.Background()
	r, err := d.DND(ctx, true, "2h")
	if err != nil || !r.On {
		t.Fatalf("dnd on: %+v %v", r, err)
	}
	if f.Props["9-1"] != true {
		t.Fatalf("xiao ai's own dnd = %v", f.Props["9-1"])
	}
	n := len(f.Ubus)
	if id, err := d.Say(ctx, "", "半夜做完了。"); err != nil || id != "quiet" {
		t.Fatalf("say while quiet = %q %v", id, err)
	}
	if len(f.Ubus) != n {
		t.Fatal("spoke while quiet")
	}
	st := d.status(ctx)
	if !st.DND || st.DNDUntil == "" {
		t.Fatalf("status = %+v", st)
	}
	r, err = d.DND(ctx, false, "")
	if err != nil || r.Held != 1 {
		t.Fatalf("dnd off = %+v %v", r, err)
	}
	if _, _, msg := lastUbus(f); !strings.Contains(msg, "攒了 1 句") {
		t.Fatalf("report = %s", msg)
	}
	if f.Props["9-1"] != false {
		t.Fatal("xiao ai's own dnd still on")
	}
}

func TestDirectDoVolumeStatus(t *testing.T) {
	d, f := newDirect(t)
	ctx := context.Background()
	if err := d.Do(ctx, "明天八点叫我", true); err != nil {
		t.Fatalf("do: %v", err)
	}
	if a := f.Actions[len(f.Actions)-1]; a["aiid"] != float64(4) {
		t.Fatalf("action = %v", a)
	}
	if err := d.Do(ctx, "帮我买牛奶", false); err == nil {
		t.Fatal("unsafe do accepted")
	}
	if v, err := d.Volume(ctx); err != nil || v != 30 {
		t.Fatalf("volume = %d %v", v, err)
	}
	if err := d.SetVolume(ctx, 45); err != nil {
		t.Fatal(err)
	}
	if _, m, msg := lastUbus(f); m != "player_set_volume" || !strings.Contains(msg, "45") {
		t.Fatalf("set volume = %s %s", m, msg)
	}
	b, _ := json.Marshal(d.status(ctx))
	if !strings.Contains(string(b), `"login":"ok"`) || !strings.Contains(string(b), `"online":true`) {
		t.Fatalf("status = %s", b)
	}
}

func TestDirectNotLoggedIn(t *testing.T) {
	t.Setenv("XS_HOME", t.TempDir())
	d, _ := openDirect(&bytes.Buffer{})
	if _, err := d.Say(context.Background(), "", "你好"); err == nil || !strings.Contains(err.Error(), "login") {
		t.Fatalf("err = %v", err)
	}
}

// The serverless stdio MCP serves the same two tools as the service.
func TestDirectMCPHasTheTwoTools(t *testing.T) {
	d, _ := newDirect(t)
	srv := mcp.NewServer(&mcp.Implementation{Name: "xueer-speaker", Version: "t"}, nil)
	tools.Register(srv, d, tools.Persona{}, func(*mcp.CallToolRequest) string { return "" })
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	go srv.Run(ctx, st)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	lt, _ := cs.ListTools(ctx, nil)
	var names []string
	for _, tl := range lt.Tools {
		names = append(names, tl.Name)
	}
	if len(names) != 2 {
		t.Fatalf("tools = %v", names)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "speaker", Arguments: map[string]any{"action": "volume", "volume": 20}})
	if err != nil || res.IsError {
		t.Fatalf("volume: %v %+v", err, res)
	}
	res, _ = cs.CallTool(ctx, &mcp.CallToolParams{Name: "speaker", Arguments: map[string]any{"action": "status"}})
	b, _ := json.Marshal(res.StructuredContent)
	if !strings.Contains(string(b), `"online":true`) {
		t.Fatalf("status = %s", b)
	}
}
