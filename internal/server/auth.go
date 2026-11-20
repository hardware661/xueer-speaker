package server

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Auth is how the panel knows its owner.
type Auth string

const (
	// AuthPassword (default): the panel has its own password, set on first
	// visit with a one-time code printed in the service log.
	AuthPassword Auth = "password"
	// AuthHeader: a reverse proxy in front has logged the user in and sets
	// X-Owner-User; it must strip that header from every other request.
	AuthHeader Auth = "header"
)

const (
	sessionCookie = "xs_session"
	sessionTTL    = 30 * 24 * time.Hour
	minPassword   = 8
	maxFailures   = 5
	lockout       = 5 * time.Minute
	pbkdfRounds   = 200_000
)

// gate holds the password-mode state; sessions live in the store so a
// restart does not log the owner out.
type gate struct {
	mu        sync.Mutex
	setupCode string
	failures  int
	lockedTo  time.Time
}

func (s *Server) initGate() {
	if s.cfg.Auth != AuthPassword || s.store.Setting("panel_password", "") != "" {
		return
	}
	n, _ := rand.Int(rand.Reader, big.NewInt(1_000_000))
	s.gate.setupCode = fmt.Sprintf("%06d", n.Int64())
	s.cfg.Log.Warn("panel has no password yet: open the panel and enter this code to set one", "setup_code", s.gate.setupCode)
}

func hashPassword(pw string, salt []byte) string {
	key, _ := pbkdf2.Key(sha256.New, pw, salt, pbkdfRounds, 32)
	return "pbkdf2-sha256$" + fmt.Sprint(pbkdfRounds) + "$" + hex.EncodeToString(salt) + "$" + hex.EncodeToString(key)
}

func checkPassword(pw, stored string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	salt, err := hex.DecodeString(parts[2])
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hashPassword(pw, salt)), []byte(stored)) == 1
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// sessionKey is what the store keeps for a session: a hash, so the
// database alone does not log anyone in.
func sessionKey(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return "session:" + hex.EncodeToString(sum[:])
}

func (s *Server) newSession(w http.ResponseWriter, r *http.Request) bool {
	tok := randomHex(32)
	if err := s.store.SetSetting(sessionKey(tok), fmt.Sprint(time.Now().Add(sessionTTL).Unix())); err != nil {
		s.authStoreError(w, err)
		return false
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", MaxAge: int(sessionTTL.Seconds()),
	})
	return true
}

func (s *Server) authStoreError(w http.ResponseWriter, err error) {
	s.cfg.Log.Error("persist panel authentication", "err", err)
	writeErr(w, http.StatusInternalServerError, "auth_store", "登录状态保存失败，请稍后再试")
}

func (s *Server) loggedIn(r *http.Request) bool {
	if s.cfg.Auth == AuthHeader {
		return r.Header.Get("X-Owner-User") != ""
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return false
	}
	var until int64
	if _, err := fmt.Sscan(s.store.Setting(sessionKey(c.Value), "0"), &until); err != nil {
		return false
	}
	return time.Now().Unix() < until
}

// owner admits the panel's owner: a session cookie in password mode, the
// proxy's header in header mode.
func (s *Server) owner(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.loggedIn(r) {
			writeErr(w, http.StatusUnauthorized, "login_required", "需要先登录面板")
			return
		}
		next(w, r)
	}
}

// apiAuth tells the page what to show: the setup form, the login form or
// the panel.
func (s *Server) apiAuth(w http.ResponseWriter, r *http.Request) {
	setup := s.cfg.Auth == AuthPassword && s.store.Setting("panel_password", "") == ""
	writeJSON(w, http.StatusOK, map[string]any{"mode": s.cfg.Auth, "setup": setup, "logged_in": !setup && s.loggedIn(r)})
}

func (s *Server) apiSetup(w http.ResponseWriter, r *http.Request) {
	var b struct{ Code, Password string }
	if !readJSON(w, r, &b) {
		return
	}
	s.gate.mu.Lock()
	defer s.gate.mu.Unlock()
	if s.store.Setting("panel_password", "") != "" {
		writeErr(w, http.StatusConflict, "already_set", "面板密码已经设过了，直接登录")
		return
	}
	if s.gate.setupCode == "" || subtle.ConstantTimeCompare([]byte(b.Code), []byte(s.gate.setupCode)) != 1 {
		writeErr(w, http.StatusForbidden, "bad_code", "初始化码不对，看服务日志里的 setup_code")
		return
	}
	if utf8.RuneCountInString(b.Password) < minPassword {
		writeErr(w, http.StatusBadRequest, "short_password", fmt.Sprintf("密码至少 %d 位", minPassword))
		return
	}
	salt := make([]byte, 16)
	rand.Read(salt)
	if err := s.store.SetSetting("panel_password", hashPassword(b.Password, salt)); err != nil {
		s.authStoreError(w, err)
		return
	}
	s.gate.setupCode = ""
	if s.newSession(w, r) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) apiLogin(w http.ResponseWriter, r *http.Request) {
	var b struct{ Password string }
	if !readJSON(w, r, &b) {
		return
	}
	s.gate.mu.Lock()
	if time.Now().Before(s.gate.lockedTo) {
		s.gate.mu.Unlock()
		writeErr(w, http.StatusTooManyRequests, "locked", "试错太多次，过几分钟再试")
		return
	}
	s.gate.mu.Unlock()
	stored := s.store.Setting("panel_password", "")
	if stored == "" || !checkPassword(b.Password, stored) {
		s.gate.mu.Lock()
		if s.gate.failures++; s.gate.failures >= maxFailures {
			s.gate.failures, s.gate.lockedTo = 0, time.Now().Add(lockout)
		}
		s.gate.mu.Unlock()
		writeErr(w, http.StatusUnauthorized, "bad_password", "密码不对")
		return
	}
	s.gate.mu.Lock()
	s.gate.failures = 0
	s.gate.mu.Unlock()
	if s.newSession(w, r) {
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) apiLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.store.SetSetting(sessionKey(c.Value), "0"); err != nil {
			s.authStoreError(w, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}
