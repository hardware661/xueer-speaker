package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/hardware661/xueer-speaker/internal/announcer"
	"github.com/hardware661/xueer-speaker/internal/tools"
)

// Controls is what the speaker tool and the panel's 勿扰 switch do beyond
// speaking.
type Controls interface {
	Volume(ctx context.Context) (int, error)
	SetVolume(ctx context.Context, v int) error
	DND(ctx context.Context, on bool, until string) (tools.DNDState, error)
	DNDState() tools.DNDState
	Do(ctx context.Context, text string, silent bool) error
}

var errNoControls = errors.New("这台服务没有接音箱控制")

// mcpBackend adapts the server to tools.Backend.
type mcpBackend struct{ s *Server }

func (b mcpBackend) Say(ctx context.Context, source, text string) (string, error) {
	res, err := b.s.say.Say(ctx, announcer.Request{Text: text, Source: source, Native: b.s.Voice() == "native"})
	return res.ID, err
}
func (b mcpBackend) Status(ctx context.Context) any { return b.s.status(ctx) }
func (b mcpBackend) Volume(ctx context.Context) (int, error) {
	if b.s.cfg.Controls == nil {
		return 0, errNoControls
	}
	return b.s.cfg.Controls.Volume(ctx)
}
func (b mcpBackend) SetVolume(ctx context.Context, v int) error {
	if b.s.cfg.Controls == nil {
		return errNoControls
	}
	return b.s.cfg.Controls.SetVolume(ctx, v)
}
func (b mcpBackend) DND(ctx context.Context, on bool, until string) (tools.DNDState, error) {
	if b.s.cfg.Controls == nil {
		return tools.DNDState{}, errNoControls
	}
	return b.s.cfg.Controls.DND(ctx, on, until)
}
func (b mcpBackend) Do(ctx context.Context, text string, silent bool) error {
	if b.s.cfg.Controls == nil {
		return errNoControls
	}
	return b.s.cfg.Controls.Do(ctx, text, silent)
}

// apiDND is the panel's 勿扰 switch: {"on": true, "until": "08:00"}.
func (s *Server) apiDND(w http.ResponseWriter, r *http.Request) {
	var b struct {
		On    bool   `json:"on"`
		Until string `json:"until"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	if s.cfg.Controls == nil {
		writeErr(w, http.StatusServiceUnavailable, "no_controls", errNoControls.Error())
		return
	}
	st, err := s.cfg.Controls.DND(r.Context(), b.On, b.Until)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "dnd", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}
