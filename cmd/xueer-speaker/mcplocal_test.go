package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeSpeaker is the server side of speaker_say.
func fakeSpeaker(t *testing.T, fail bool) (*httptest.Server, *[]string) {
	var calls []string
	srv := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	type in struct {
		Text string `json:"text"`
	}
	type out struct {
		ID string `json:"id"`
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "speaker_say"}, func(_ context.Context, _ *mcp.CallToolRequest, a in) (*mcp.CallToolResult, out, error) {
		calls = append(calls, a.Text)
		if fail {
			return nil, out{}, errors.New("too many lines")
		}
		return nil, out{ID: "S1"}, nil
	})
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	hs := httptest.NewServer(h)
	t.Cleanup(hs.Close)
	return hs, &calls
}

func TestLocalSayForwardsToTheServer(t *testing.T) {
	hs, calls := fakeSpeaker(t, false)
	id, err := (&remote{url: hs.URL, key: "k"}).Say(context.Background(), "", "主人，好了")
	if id != "S1" || err != nil || strings.Join(*calls, "|") != "主人，好了" {
		t.Fatalf("id %q err %v calls %v", id, err, *calls)
	}
}

func TestLocalSayReportsServerRefusal(t *testing.T) {
	hs, _ := fakeSpeaker(t, true)
	_, err := (&remote{url: hs.URL, key: "k"}).Say(context.Background(), "", "主人")
	if err == nil || !strings.Contains(err.Error(), "too many lines") {
		t.Fatalf("err %v", err)
	}
}

func TestReadKeyRefusesSharedFiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k.env")
	os.WriteFile(p, []byte("A=1\nXUEER_SPEAKER_MCP_CLAUDE=secret\n"), 0o644)
	if _, err := readKey(p, "XUEER_SPEAKER_MCP_CLAUDE"); err == nil {
		t.Fatal("world-readable key file must be refused")
	}
	os.Chmod(p, 0o600)
	if k, err := readKey(p, "XUEER_SPEAKER_MCP_CLAUDE"); err != nil || k != "secret" {
		t.Fatalf("%q %v", k, err)
	}
}

func TestReadKeyFileTakesAPlainKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mcp.key")
	os.WriteFile(p, []byte("abc123\n"), 0o600)
	if k, err := readKeyFile(p); err != nil || k != "abc123" {
		t.Fatalf("%q %v", k, err)
	}
	os.Chmod(p, 0o644)
	if _, err := readKeyFile(p); err == nil {
		t.Fatal("shared key file must be refused")
	}
}
