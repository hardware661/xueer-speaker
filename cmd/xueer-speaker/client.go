package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// client calls a running serve's /admin on the internal port.
func client(args []string) error {
	if len(args) == 0 {
		return errors.New("client 要接 say / status / history / volume / stop")
	}
	base := envOr("XS_ADMIN_URL", "http://127.0.0.1:8101")
	keyFile := envOr("XS_CLI_KEY_FILE", envOr("XS_KEYS", filepath.Join(envOr("XS_DATA", "/data"), "keys.json")))
	k, err := readKeys(keyFile)
	if err != nil {
		return err
	}
	var method, path string
	var body any
	switch args[0] {
	case "say":
		fs := flag.NewFlagSet("say", flag.ContinueOnError)
		native := fs.Bool("native", false, "用小爱原声")
		vol := fs.Int("volume", 0, "音量")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		method, path = http.MethodPost, "/admin/say"
		body = map[string]any{"text": strings.Join(fs.Args(), " "), "native": *native, "volume": *vol}
	case "status":
		method, path = http.MethodGet, "/admin/status"
	case "history":
		method, path = http.MethodGet, "/admin/history"
	case "volume":
		if len(args) < 2 {
			return errors.New("volume 要一个数")
		}
		var v int
		if _, err := fmt.Sscan(args[1], &v); err != nil {
			return err
		}
		method, path, body = http.MethodPost, "/admin/volume", map[string]int{"volume": v}
	case "stop":
		method, path = http.MethodPost, "/admin/stop"
	default:
		return fmt.Errorf("client 不认识 %q", args[0])
	}
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+k.CLI)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if len(out) > 0 {
		var pretty bytes.Buffer
		if json.Indent(&pretty, out, "", "  ") == nil {
			out = pretty.Bytes()
		}
		fmt.Println(string(out))
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// check is the container healthcheck (the image has no shell or curl).
func check() error {
	resp, err := (&http.Client{Timeout: 4 * time.Second}).Get(envOr("XS_CHECK_URL", "http://127.0.0.1:8102/readyz"))
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("readyz HTTP %d", resp.StatusCode)
	}
	return nil
}
