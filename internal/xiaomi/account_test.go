package xiaomi_test

import (
	"context"
	"errors"
	"github.com/hardware661/xueer-speaker/internal/xiaomi"
	"github.com/hardware661/xueer-speaker/internal/xiaomi/xiaomitest"
	"os"
	"path/filepath"
	"testing"
)

func TestAccountLogsInSavesAndReloads(t *testing.T) {
	f := xiaomitest.New(t)
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "xiaomi.json")
	a, err := xiaomi.NewAccount(f.Endpoints(), p)
	if err != nil {
		t.Fatal(err)
	}
	if a.Session() != nil || a.Health() != xiaomi.HealthNoAccount {
		t.Fatal("fresh account must have no session")
	}
	if err := a.Login(ctx, "speaker@example.com", "secret"); !errors.Is(err, xiaomi.ErrNeedCode) {
		t.Fatalf("Login = %v", err)
	}
	if err := a.Verify(ctx, "123456"); err != nil {
		t.Fatalf("Verify = %v", err)
	}
	if a.Session() == nil || a.Health() != xiaomi.HealthOK {
		t.Fatal("no session after login")
	}
	if st, _ := os.Stat(p); st == nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("saved file: %v", st)
	}
	b, err := xiaomi.NewAccount(f.Endpoints(), p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Session().Devices(ctx); err != nil {
		t.Fatalf("reloaded session: %v", err)
	}
	if err := b.Logout(); err != nil || b.Session() != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("logout must remove the file")
	}
}

func TestAccountReloginKeepsTheDeviceID(t *testing.T) {
	f := xiaomitest.New(t)
	f.NeedOTP = false
	p := filepath.Join(t.TempDir(), "xiaomi.json")
	os.WriteFile(p, []byte(`{"userId":"1","passToken":"V1:old","deviceId":"an_keep"}`), 0o600)
	a, err := xiaomi.NewAccount(f.Endpoints(), p)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Login(context.Background(), "speaker@example.com", "secret"); err != nil {
		t.Fatal(err)
	}
	if f.DeviceID != "an_keep" {
		t.Fatalf("relogin used deviceId %q", f.DeviceID)
	}
}
