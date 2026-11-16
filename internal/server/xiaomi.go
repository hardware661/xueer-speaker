package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Xiaomi is the account behind the speaker, as the panel manages it.
type Xiaomi interface {
	LoginState() string // none / ok / renewing / login-required / risk-control
	// Login sends the password; needCode = Xiaomi sent a code to verify.
	Login(ctx context.Context, user, password string) (needCode bool, err error)
	Verify(ctx context.Context, code string) error
	Logout() error
	Speakers(ctx context.Context) ([]SpeakerInfo, error)
	Choose(ctx context.Context, id string) error
	Chosen() string
}

// SpeakerInfo is one speaker on the account.
type SpeakerInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Model  string `json:"model"`
	Online bool   `json:"online"`
}

// Errors a Xiaomi implementation returns for the panel to explain.
var (
	ErrXiaomiPassword = errors.New("账号或密码不对")
	ErrXiaomiCode     = errors.New("验证码不对")
	ErrXiaomiLimited  = errors.New("小米暂时限流，过两小时再试")
)

func (s *Server) apiXiaomi(w http.ResponseWriter, r *http.Request) {
	mi := s.cfg.Xiaomi
	out := map[string]any{"login": mi.LoginState(), "speakers": []SpeakerInfo{}, "chosen": mi.Chosen()}
	if mi.LoginState() == "ok" {
		if list, err := mi.Speakers(r.Context()); err == nil && list != nil {
			out["speakers"] = list
		} else if err != nil {
			out["error"] = err.Error()
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func xiaomiErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrXiaomiPassword), errors.Is(err, ErrXiaomiCode):
		writeErr(w, http.StatusBadRequest, "xiaomi_login", err.Error())
	case errors.Is(err, ErrXiaomiLimited):
		writeErr(w, http.StatusTooManyRequests, "xiaomi_limited", err.Error())
	default:
		writeErr(w, http.StatusBadGateway, "xiaomi", err.Error())
	}
}

func (s *Server) apiXiaomiLogin(w http.ResponseWriter, r *http.Request) {
	var b struct{ User, Password string }
	if !readJSON(w, r, &b) {
		return
	}
	if strings.TrimSpace(b.User) == "" || b.Password == "" {
		writeErr(w, http.StatusBadRequest, "xiaomi_login", "账号和密码都要填")
		return
	}
	need, err := s.cfg.Xiaomi.Login(r.Context(), strings.TrimSpace(b.User), b.Password)
	if err != nil {
		xiaomiErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"need_code": need})
}

func (s *Server) apiXiaomiVerify(w http.ResponseWriter, r *http.Request) {
	var b struct{ Code string }
	if !readJSON(w, r, &b) {
		return
	}
	if err := s.cfg.Xiaomi.Verify(r.Context(), b.Code); err != nil {
		xiaomiErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiXiaomiLogout(w http.ResponseWriter, r *http.Request) {
	if err := s.cfg.Xiaomi.Logout(); err != nil {
		writeErr(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) apiXiaomiSpeaker(w http.ResponseWriter, r *http.Request) {
	var b struct{ ID string }
	if !readJSON(w, r, &b) {
		return
	}
	if err := s.cfg.Xiaomi.Choose(r.Context(), b.ID); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_speaker", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiConnect is what the 接入 page shows: the MCP address and a key for AI
// clients, the internal say endpoint, and the tray's server address and key.
func (s *Server) apiConnect(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	origin := scheme + "://" + r.Host
	var mcpKey string
	for k, src := range s.cfg.MCPTokens {
		if mcpKey == "" || src == "ai" {
			mcpKey = k
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"origin": origin, "mcp_url": origin + "/mcp", "mcp_key": mcpKey,
		"pc_key": s.cfg.PCKey, "say_key": s.cfg.BotKey, "base": s.store.Setting("base", ""),
	})
}

// learnBase remembers where the speaker can reach us the first time the
// owner opens the panel, unless it was configured. A panel opened on
// localhost says nothing about that, so it is not learnt.
func (s *Server) learnBase(r *http.Request) {
	if s.store.Setting("base", "") != "" {
		return
	}
	host := r.Host
	if h := strings.Split(host, ":")[0]; h == "" || h == "localhost" || h == "127.0.0.1" || h == "[::1]" {
		return
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	s.store.SetSetting("base", fmt.Sprintf("%s://%s", scheme, host))
}
