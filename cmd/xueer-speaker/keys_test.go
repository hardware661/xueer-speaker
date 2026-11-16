package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateKeysMakesThemOnce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keys.json")
	k1, err := loadOrCreateKeys(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(k1.MCP) != 1 || k1.Bot == "" || k1.CLI == "" || k1.PC == "" || len(k1.Bot) < 32 {
		t.Fatalf("keys = %+v", k1)
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", st.Mode())
	}
	k2, err := loadOrCreateKeys(p)
	if err != nil {
		t.Fatal(err)
	}
	if k2.Bot != k1.Bot || k2.PC != k1.PC {
		t.Fatal("keys changed on the second start")
	}
}

func TestLoadOrCreateKeysKeepsAnExistingFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "keys.json")
	os.WriteFile(p, []byte(`{"mcp":{"a":"claude"},"bot":"b","cli":"c"}`), 0o600)
	k, err := loadOrCreateKeys(p)
	if err != nil || k.Bot != "b" || k.MCP["a"] != "claude" || k.PC != "" {
		t.Fatalf("%+v %v", k, err)
	}
}

func TestEnvFillsUnsetFlags(t *testing.T) {
	t.Setenv("XS_BASE", "http://192.168.1.5:8100")
	t.Setenv("XS_PUBLIC", ":9")
	fs := newServeFlags()
	if err := parseWithEnv(fs.set, []string{"-public=:7"}); err != nil {
		t.Fatal(err)
	}
	if *fs.base != "http://192.168.1.5:8100" || *fs.public != ":7" {
		t.Fatalf("base %q public %q", *fs.base, *fs.public)
	}
}
