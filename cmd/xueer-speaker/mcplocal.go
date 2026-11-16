package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hardware661/xueer-speaker/internal/server"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mcpLocal is a stdio MCP server for AI clients that cannot speak HTTP MCP
// (or should not hold the key in their own config): it forwards
// speaker_say / speaker_status to the service, which decides where the line
// plays. The key is read from a private file, never from args or env.
func mcpLocal(args []string) error {
	fs := flag.NewFlagSet("mcp-local", flag.ContinueOnError)
	url := fs.String("url", os.Getenv("XS_MCP_URL"), "服务的 MCP 地址，如 http://192.168.1.10:8100/mcp")
	keyFile := fs.String("key-file", os.Getenv("XS_MCP_KEY_FILE"), "钥匙文件（只许自己读，600）：整个文件就是钥匙，或 NAME=钥匙 的 env 文件")
	who := fs.String("as", "", "用 env 文件时取 XUEER_SPEAKER_MCP_<AS> 这一行（如 claude）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *url == "" || *keyFile == "" {
		return errors.New("mcp-local: 要 -url 和 -key-file")
	}
	var key string
	var err error
	if *who != "" {
		key, err = readKey(*keyFile, "XUEER_SPEAKER_MCP_"+strings.ToUpper(*who))
	} else {
		key, err = readKeyFile(*keyFile)
	}
	if err != nil {
		return err
	}
	remote := &remote{url: *url, key: key}

	srv := mcp.NewServer(&mcp.Implementation{Name: "xueer-speaker", Version: version}, &mcp.ServerOptions{
		Instructions: server.Instructions,
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "speaker_say",
		Description: server.SayDescription,
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in localSayIn) (*mcp.CallToolResult, localSayOut, error) {
		return nil, remote.say(ctx, in.Text), nil
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "speaker_status",
		Description: "音箱在不在线、小米登录状态、在放什么、音量。",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return remote.status(ctx)
	})
	ctx, stop := signalContext()
	defer stop()
	return srv.Run(ctx, &mcp.StdioTransport{})
}

type localSayIn struct {
	Text string `json:"text" jsonschema:"雪儿口吻的一句话，80 字以内，开头说是哪个项目"`
}

type localSayOut struct {
	ID  string `json:"id,omitempty"`
	Err string `json:"err,omitempty"`
}

type remote struct {
	url, key string
}

func (r *remote) session(ctx context.Context) (*mcp.ClientSession, error) {
	c := mcp.NewClient(&mcp.Implementation{Name: "xueer-speaker-local", Version: version}, nil)
	hc := &http.Client{Timeout: 30 * time.Second, Transport: bearerRT{r.key}}
	return c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: r.url, HTTPClient: hc, MaxRetries: -1}, nil)
}

// say queues the line on the server. It never returns an error: the AI is
// told not to retry or mention failures, so the outcome is informational.
func (r *remote) say(ctx context.Context, text string) localSayOut {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cs, err := r.session(ctx)
	if err != nil {
		return localSayOut{Err: err.Error()}
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "speaker_say", Arguments: map[string]any{"text": text}})
	if err != nil {
		return localSayOut{Err: err.Error()}
	}
	if res.IsError {
		return localSayOut{Err: toolText(res)}
	}
	out := localSayOut{}
	if m, ok := res.StructuredContent.(map[string]any); ok {
		out.ID, _ = m["id"].(string)
	}
	return out
}

func (r *remote) status(ctx context.Context) (*mcp.CallToolResult, any, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cs, err := r.session(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "speaker_status", Arguments: map[string]any{}})
	if err != nil {
		return nil, nil, err
	}
	return nil, res.StructuredContent, nil
}

func toolText(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok {
			return t.Text
		}
	}
	return "tool error"
}

type bearerRT struct{ key string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return http.DefaultTransport.RoundTrip(r)
}

// openPrivate opens a file only its owner can read.
func openPrivate(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !private(st) {
		f.Close()
		return nil, fmt.Errorf("%s: must be private to its owner (chmod 600)", path)
	}
	return f, nil
}

// readKeyFile reads a file that holds just the key.
func readKeyFile(path string) (string, error) {
	f, err := openPrivate(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 4096))
	if err != nil {
		return "", err
	}
	k := strings.TrimSpace(string(b))
	if k == "" || strings.ContainsAny(k, " \t\n=") {
		return "", fmt.Errorf("%s: not a single key", path)
	}
	return k, nil
}

// readKey reads NAME=value from a private env file.
func readKey(path, name string) (string, error) {
	f, err := openPrivate(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var found []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if ok && k == name {
			found = append(found, strings.Trim(strings.TrimSpace(v), `"'`))
		}
	}
	if len(found) != 1 || found[0] == "" || strings.ContainsAny(found[0], " \t") {
		return "", fmt.Errorf("%s: %s missing or ambiguous", path, name)
	}
	return found[0], nil
}

func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
}
