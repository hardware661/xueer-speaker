package xiaomi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Speaker is one speaker reached through MiNA (and MIoT for spec actions).
type Speaker struct {
	s   *Session
	dev Device
}

// Speaker finds a speaker by name, alias, MiNA deviceID, MIoT did or MAC.
func (s *Session) Speaker(ctx context.Context, key string) (*Speaker, error) {
	devs, err := s.Devices(ctx)
	if err != nil {
		return nil, err
	}
	for _, d := range devs {
		for _, v := range []string{d.Name, d.Alias, d.DeviceID, d.MiotDID, d.MAC} {
			if v != "" && v == key {
				return &Speaker{s: s, dev: d}, nil
			}
		}
	}
	return nil, fmt.Errorf("xiaomi: no speaker matches %q", key)
}

// Device is what the cloud said about this speaker.
func (sp *Speaker) Device() Device { return sp.dev }

// ubus calls a MiNA ubus method on the speaker and returns data.info.
// Both the outer and the device's inner code must be 0.
func (sp *Speaker) ubus(ctx context.Context, path, method string, message any) (string, error) {
	msg, err := json.Marshal(message)
	if err != nil {
		return "", err
	}
	form := url.Values{
		"deviceId":  {sp.dev.DeviceID},
		"path":      {path},
		"method":    {method},
		"message":   {string(msg)},
		"requestId": {requestID()},
	}
	body, err := sp.s.do(ctx, sidMiNA, func(t token) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, sp.s.ep.MiNA+"/remote/ubus",
			strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		sp.s.minaHeaders(req, t, &sp.dev)
		return req, nil
	})
	if err != nil {
		return "", err
	}
	var r struct {
		Code int `json:"code"`
		Data struct {
			Code int    `json:"code"`
			Info string `json:"info"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("xiaomi: ubus %s: %w", method, err)
	}
	if r.Code != 0 {
		return "", fmt.Errorf("xiaomi: ubus %s: cloud code %d", method, r.Code)
	}
	if r.Data.Code != 0 {
		return "", fmt.Errorf("xiaomi: ubus %s: speaker refused (code %d)", method, r.Data.Code)
	}
	return r.Data.Info, nil
}

// Default audio ids used by MiService and xiaomusic for player_play_music.
const (
	musicAudioID = "1582971365183456177"
	musicCPID    = "355454500"
)

// PlayMusic plays one URL with player_play_music (OH2P needs this one;
// player_play_url is silent on it). REPLACE_ALL replaces whatever plays.
func (sp *Speaker) PlayMusic(ctx context.Context, u string) error {
	music := map[string]any{
		"payload": map[string]any{
			"audio_type": "",
			"audio_items": []any{map[string]any{
				"item_id": map[string]any{
					"audio_id": musicAudioID,
					"cp":       map[string]any{"album_id": "-1", "episode_index": 0, "id": musicCPID, "name": "xiaowei"},
				},
				"stream": map[string]any{"url": u},
			}},
			"list_params": map[string]any{"listId": "-1", "loadmore_offset": 0, "origin": "xiaowei", "type": "MUSIC"},
		},
		"play_behavior": "REPLACE_ALL",
	}
	inner, err := json.Marshal(music)
	if err != nil {
		return err
	}
	_, err = sp.ubus(ctx, "mediaplayer", "player_play_music", map[string]any{
		"startaudioid": musicAudioID,
		"music":        string(inner), // a JSON string inside the JSON message
	})
	return err
}

// PlayURL is the generic player; kept for SPEAKER_PLAY_API=url.
func (sp *Speaker) PlayURL(ctx context.Context, u string) error {
	_, err := sp.ubus(ctx, "mediaplayer", "player_play_url", map[string]any{"url": u, "type": 1, "media": "app_ios"})
	return err
}

// PlayTone plays a url the way the firmware plays its own prompt tones
// (mphelper tone: player_play_url, type 1, no media).
// docs/research/speech-channel-2026-11.md candidate 1.
func (sp *Speaker) PlayTone(ctx context.Context, u string) error {
	_, err := sp.ubus(ctx, "mediaplayer", "player_play_url", map[string]any{"url": u, "type": 1})
	return err
}

// QPlay asks the firmware's short-sound player (qplayer) to play a url.
// Nobody has called it through the cloud before (candidate 4).
func (sp *Speaker) QPlay(ctx context.Context, u string) error {
	_, err := sp.ubus(ctx, "qplayer", "play", map[string]any{"play": u, "light": 0})
	return err
}

// TextToSpeech asks the speaker to say text in Xiao Ai's own voice (MiNA).
func (sp *Speaker) TextToSpeech(ctx context.Context, text string) error {
	_, err := sp.ubus(ctx, "mibrain", "text_to_speech", map[string]any{"text": text, "save": 0})
	return err
}

// SetLoop sets the player's loop mode: 0 single repeat, 1 list repeat,
// 3 random; 2 plays the list once and stops — set right after a push, the
// OH2P plays the clip once and hands the sound back to Bluetooth by itself
// (hass-xiaomi-miot PR #2970; real device 2026-11-15).
func (sp *Speaker) SetLoop(ctx context.Context, t int) error {
	_, err := sp.ubus(ctx, "mediaplayer", "player_set_loop", map[string]any{"media": "common", "type": t})
	return err
}

// Op is a playback control.
type Op string

const (
	OpPlay  Op = "play"
	OpPause Op = "pause"
	OpStop  Op = "stop"
)

// Control pauses, resumes or stops playback.
func (sp *Speaker) Control(ctx context.Context, op Op) error {
	_, err := sp.ubus(ctx, "mediaplayer", "player_play_operation", map[string]any{"action": string(op)})
	return err
}

// SetVolume sets the volume, clamped to 6–100 like migpt-next does.
func (sp *Speaker) SetVolume(ctx context.Context, v int) error {
	v = min(max(v, 6), 100)
	_, err := sp.ubus(ctx, "mediaplayer", "player_set_volume", map[string]any{"volume": v})
	return err
}

// State is the player state.
type State string

const (
	StateIdle    State = "idle"
	StatePlaying State = "playing"
	StatePaused  State = "paused"
	StateStopped State = "stopped"
	StateUnknown State = "unknown"
)

// Status is the player as the speaker reports it. Position and Duration are
// milliseconds; Status can lie (state 0 while position still moves), so
// callers judge by position too.
type Status struct {
	State      State
	Volume     int
	PositionMS int64
	DurationMS int64
	Raw        json.RawMessage
}

// Status reads player_get_play_status.
func (sp *Speaker) Status(ctx context.Context) (Status, error) {
	info, err := sp.ubus(ctx, "mediaplayer", "player_get_play_status", map[string]any{})
	if err != nil {
		return Status{}, err
	}
	var r struct {
		Status int `json:"status"`
		Volume int `json:"volume"`
		Detail struct {
			Position int64 `json:"position"`
			Duration int64 `json:"duration"`
		} `json:"play_song_detail"`
	}
	if err := json.Unmarshal([]byte(info), &r); err != nil {
		return Status{}, fmt.Errorf("xiaomi: status: %w", err)
	}
	st := map[int]State{0: StateIdle, 1: StatePlaying, 2: StatePaused, 3: StateStopped}[r.Status]
	if st == "" {
		st = StateUnknown
	}
	return Status{State: st, Volume: r.Volume, PositionMS: r.Detail.Position, DurationMS: r.Detail.Duration,
		Raw: json.RawMessage(info)}, nil
}

// BluetoothState is the speaker's Bluetooth side (ubus mibt status).
type BluetoothState struct {
	Connected bool // a device (the owner's PC) is connected
	Playing   bool // and it is making sound: the speaker plays it instead of its own player
}

// Bluetooth reads the Bluetooth link.
func (sp *Speaker) Bluetooth(ctx context.Context) (BluetoothState, error) {
	info, err := sp.ubus(ctx, "mibt", "status", map[string]any{})
	if err != nil {
		return BluetoothState{}, err
	}
	var r struct {
		Playing int `json:"playing"`
		Connect int `json:"connect"`
		Devices []struct {
			Connect string `json:"connect"`
		} `json:"devices_list"`
	}
	if err := json.Unmarshal([]byte(info), &r); err != nil {
		return BluetoothState{}, fmt.Errorf("xiaomi: mibt status: %w", err)
	}
	// The top-level connect stays 1 after the PC switches its Bluetooth off
	//; a device in the list that is connected is what counts.
	connected := r.Connect == 1
	if r.Devices != nil {
		connected = false
		for _, d := range r.Devices {
			connected = connected || d.Connect == "true"
		}
	}
	return BluetoothState{Connected: connected, Playing: connected && r.Playing == 1}, nil
}

// Raw calls any ubus method; for diagnostics only.
func (sp *Speaker) Raw(ctx context.Context, path, method string, message json.RawMessage) (string, error) {
	return sp.ubus(ctx, path, method, message)
}
