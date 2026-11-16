package xiaomi_test

import (
	"context"
	"errors"
	"github.com/hardware661/xueer-speaker/internal/xiaomi"
	"github.com/hardware661/xueer-speaker/internal/xiaomi/xiaomitest"
	"testing"
)

func TestLoginExchangesPassTokenForServiceToken(t *testing.T) {
	f := xiaomitest.New(t)
	s := xiaomi.NewSession(f.Endpoints(), xiaomi.Credentials{UserID: "2225000001", PassToken: "V1:good", DeviceID: "an_fixed"})

	devs, err := s.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(devs) != 1 || devs[0].Hardware != "OH2P" || devs[0].Name != "Xiaomi 智能音箱 Pro" {
		t.Fatalf("devices = %+v", devs)
	}
}

func TestWrongDeviceIDNeedsRelogin(t *testing.T) {
	f := xiaomitest.New(t)
	s := xiaomi.NewSession(f.Endpoints(), xiaomi.Credentials{UserID: "2225000001", PassToken: "V1:good", DeviceID: "an_other"})

	_, err := s.Devices(context.Background())
	if !errors.Is(err, xiaomi.ErrLoginRequired) {
		t.Fatalf("err = %v, want xiaomi.ErrLoginRequired", err)
	}
	if h := s.Health(); h != xiaomi.HealthLoginRequired {
		t.Fatalf("health = %v", h)
	}
}

func TestExpiredServiceTokenIsRefreshedOnceAndRequestReplayed(t *testing.T) {
	f := xiaomitest.New(t)
	s := xiaomi.NewSession(f.Endpoints(), xiaomi.Credentials{UserID: "2225000001", PassToken: "V1:good", DeviceID: "an_fixed"})
	ctx := context.Background()
	if _, err := s.Devices(ctx); err != nil {
		t.Fatal(err)
	}
	f.ExpireNext = true
	if _, err := s.Devices(ctx); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
	if f.StsCalls != 2 {
		t.Fatalf("sts calls = %d, want 2 (initial + one refresh)", f.StsCalls)
	}
}
