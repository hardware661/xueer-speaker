package xiaomi

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPasswordLoginWithSMSCode(t *testing.T) {
	f := newFakeCloud(t)
	ctx := context.Background()
	l := NewLogin(f.endpoints(), "")

	creds, err := l.Start(ctx, "speaker@example.com", "secret")
	if !errors.Is(err, ErrNeedCode) || creds != nil {
		t.Fatalf("Start = %v, %v; want ErrNeedCode", creds, err)
	}
	if f.otpSent != 1 {
		t.Fatalf("sms sent %d times", f.otpSent)
	}
	if _, err := l.Verify(ctx, "000000"); !errors.Is(err, ErrBadCode) {
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
	if _, err := NewSession(f.endpoints(), *creds).Devices(ctx); err != nil {
		t.Fatalf("Devices with new creds: %v", err)
	}
}

func TestPasswordLoginWithoutSMS(t *testing.T) {
	f := newFakeCloud(t)
	f.needOTP = false
	creds, err := NewLogin(f.endpoints(), "an_keepme").Start(context.Background(), "speaker@example.com", "secret")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if creds.DeviceID != "an_keepme" || creds.PassToken != "V1:fresh" {
		t.Fatalf("creds = %+v", creds)
	}
}

func TestPasswordLoginWrongPassword(t *testing.T) {
	f := newFakeCloud(t)
	_, err := NewLogin(f.endpoints(), "").Start(context.Background(), "speaker@example.com", "nope")
	if !errors.Is(err, ErrBadPassword) {
		t.Fatalf("err = %v, want ErrBadPassword", err)
	}
}

func TestVerifyWithoutStart(t *testing.T) {
	f := newFakeCloud(t)
	if _, err := NewLogin(f.endpoints(), "").Verify(context.Background(), "123456"); err == nil {
		t.Fatal("Verify before Start should fail")
	}
}
