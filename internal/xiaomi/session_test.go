package xiaomi

import (
	"context"
	"errors"
	"testing"
)

func TestLoginExchangesPassTokenForServiceToken(t *testing.T) {
	f := newFakeCloud(t)
	s := NewSession(f.endpoints(), Credentials{UserID: "2225000001", PassToken: "V1:good", DeviceID: "an_fixed"})

	devs, err := s.Devices(context.Background())
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	if len(devs) != 1 || devs[0].Hardware != "OH2P" || devs[0].Name != "Xiaomi 智能音箱 Pro" {
		t.Fatalf("devices = %+v", devs)
	}
}

func TestWrongDeviceIDNeedsRelogin(t *testing.T) {
	f := newFakeCloud(t)
	s := NewSession(f.endpoints(), Credentials{UserID: "2225000001", PassToken: "V1:good", DeviceID: "an_other"})

	_, err := s.Devices(context.Background())
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("err = %v, want ErrLoginRequired", err)
	}
	if h := s.Health(); h != HealthLoginRequired {
		t.Fatalf("health = %v", h)
	}
}

func TestExpiredServiceTokenIsRefreshedOnceAndRequestReplayed(t *testing.T) {
	f := newFakeCloud(t)
	s := NewSession(f.endpoints(), Credentials{UserID: "2225000001", PassToken: "V1:good", DeviceID: "an_fixed"})
	ctx := context.Background()
	if _, err := s.Devices(ctx); err != nil {
		t.Fatal(err)
	}
	f.expireNext = true
	if _, err := s.Devices(ctx); err != nil {
		t.Fatalf("after expiry: %v", err)
	}
	if f.stsCalls != 2 {
		t.Fatalf("sts calls = %d, want 2 (initial + one refresh)", f.stsCalls)
	}
}
