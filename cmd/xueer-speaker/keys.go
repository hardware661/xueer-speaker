package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// keys is the bearer keys file (600, never in Git): AI clients over MCP, a
// bot or script on the internal port, the CLI, and the Windows tray.
type keys struct {
	MCP map[string]string `json:"mcp"` // bearer → source name shown on the panel
	Bot string            `json:"bot"`
	CLI string            `json:"cli"`
	PC  string            `json:"pc"` // the Windows tray; "" = no PC
}

func readKeys(path string) (keys, error) {
	var k keys
	b, err := os.ReadFile(path)
	if err != nil {
		return k, err
	}
	if err := json.Unmarshal(b, &k); err != nil {
		return k, fmt.Errorf("%s: %w", path, err)
	}
	if len(k.MCP) == 0 || k.Bot == "" || k.CLI == "" {
		return k, fmt.Errorf("%s: mcp, bot and cli keys are all required", path)
	}
	return k, nil
}

func newKey() string {
	b := make([]byte, 24)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// loadOrCreateKeys reads the keys file, or writes a fresh one on first start.
func loadOrCreateKeys(path string) (keys, error) {
	k, err := readKeys(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return k, err
	}
	k = keys{MCP: map[string]string{newKey(): "ai"}, Bot: newKey(), CLI: newKey(), PC: newKey()}
	b, _ := json.MarshalIndent(k, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return k, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return k, err
	}
	return k, os.Rename(tmp, path)
}
