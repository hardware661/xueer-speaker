package xiaomi_test

import (
	"context"
	"errors"
	"github.com/hardware661/xueer-speaker/internal/xiaomi"
	"github.com/hardware661/xueer-speaker/internal/xiaomi/xiaomitest"
	"strings"
	"testing"
)

func TestPasswordLoginWithSMSCode(t *testing.T) {
	f := xiaomitest.New(t)
	ctx := context.Background()
	l := xiaomi.NewLogin(f.Endpoints(), "")

	creds, err := l.Start(ctx, "speaker@example.com", "secret")
	if !errors.Is(err, xiaomi.ErrNeedCode) || creds != nil {
		t.Fatalf("Start = %v, %v; want xiaomi.ErrNeedCode", creds, err)
	}
	if f.OTPSent != 1 {
		t.Fatalf("sms sent %d times", f.OTPSent)
	}
	if _, err := l.Verify(ctx, "000000"); !errors.Is(err, xiaomi.ErrBadCode) {
		t.Fatalf("wrong code: %v", err)
	}
	creds, err = l.Verify(ctx, "123456")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if creds.PassToken != "V1:fresh" || creds.UserID != "2225000001" || !strings.HasPrefix(creds.DeviceID, "an_") {
		t.Fatalf("creds = %+v", creds)
	}
	// The passToken works for a session bound to the same deviceId.
	if _, err := xiaomi.NewSession(f.Endpoints(), *creds).Devices(ctx); err != nil {
		t.Fatalf("Devices with new creds: %v", err)
	}
}

func TestPasswordLoginWithoutSMS(t *testing.T) {
	f := xiaomitest.New(t)
	f.NeedOTP = false
	creds, err := xiaomi.NewLogin(f.Endpoints(), "an_keepme").Start(context.Background(), "speaker@example.com", "secret")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if creds.DeviceID != "an_keepme" || creds.PassToken != "V1:fresh" {
		t.Fatalf("creds = %+v", creds)
	}
}

func TestPasswordLoginWrongPassword(t *testing.T) {
	f := xiaomitest.New(t)
	_, err := xiaomi.NewLogin(f.Endpoints(), "").Start(context.Background(), "speaker@example.com", "nope")
	if !errors.Is(err, xiaomi.ErrBadPassword) {
		t.Fatalf("err = %v, want xiaomi.ErrBadPassword", err)
	}
}

func TestVerifyWithoutStart(t *testing.T) {
	f := xiaomitest.New(t)
	if _, err := xiaomi.NewLogin(f.Endpoints(), "").Verify(context.Background(), "123456"); err == nil {
		t.Fatal("Verify before Start should fail")
	}
}
