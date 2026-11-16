package xiaomi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// HealthNoAccount: nobody has logged in yet.
const HealthNoAccount Health = -1

// Account is the Xiaomi login the service runs on: it loads the saved
// credentials, runs a password login from the panel and saves the result.
// Session is nil until there is a login.
type Account struct {
	ep   Endpoints
	path string

	mu      sync.Mutex
	session *Session
	creds   Credentials
	login   *Login
}

// NewAccount loads the credentials saved at path, if any.
func NewAccount(ep Endpoints, path string) (*Account, error) {
	a := &Account{ep: ep, path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &a.creds); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if a.creds.PassToken != "" {
		a.session = NewSession(ep, a.creds)
	}
	return a, nil
}

// Session is the live session, or nil before the first login.
func (a *Account) Session() *Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.session
}

// Health is the session's login state.
func (a *Account) Health() Health {
	if s := a.Session(); s != nil {
		return s.Health()
	}
	return HealthNoAccount
}

// Login starts a password login. ErrNeedCode means a code was sent: call
// Verify with it. The deviceId of an earlier login is kept.
func (a *Account) Login(ctx context.Context, user, password string) error {
	a.mu.Lock()
	l := NewLogin(a.ep, a.creds.DeviceID)
	a.login = l
	a.mu.Unlock()
	c, err := l.Start(ctx, user, password)
	if err != nil {
		return err
	}
	return a.adopt(c)
}

// Verify finishes the login with the code Xiaomi sent.
func (a *Account) Verify(ctx context.Context, code string) error {
	a.mu.Lock()
	l := a.login
	a.mu.Unlock()
	if l == nil {
		return errors.New("xiaomi: log in with the password first")
	}
	c, err := l.Verify(ctx, code)
	if err != nil {
		return err
	}
	return a.adopt(c)
}

// adopt saves new credentials and switches to a session built on them.
func (a *Account) adopt(c *Credentials) error {
	if err := a.save(*c); err != nil {
		return err
	}
	a.mu.Lock()
	a.creds, a.session, a.login = *c, NewSession(a.ep, *c), nil
	a.mu.Unlock()
	return nil
}

func (a *Account) save(c Credentials) error {
	b, _ := json.Marshal(struct {
		Credentials
		SavedAt int64 `json:"savedAt"`
	}{c, time.Now().Unix()})
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.path)
}

// Logout forgets the login (the deviceId with it).
func (a *Account) Logout() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.session, a.creds, a.login = nil, Credentials{}, nil
	if err := os.Remove(a.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
