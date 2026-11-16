package xiaomi

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const uaMiIO = "iOS-14.4-6.0.103-iPhone12,3--D7744744F7AF32F0544445285880DD63E47D9BE9-8816080-84A3F44E137B71AE-iPhone"

// miio calls a MIoT endpoint with the xiaomiio token, signed the way
// MiService 3.0.2 signs it (plain data + HMAC-SHA256, no RC4).
func (sp *Speaker) miio(ctx context.Context, uri string, data any) (json.RawMessage, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	body, err := sp.s.do(ctx, sidMiIO, func(t token) (*http.Request, error) {
		form, err := signMiIO(uri, string(raw), t.ssecurity)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, sp.s.ep.MiIO+uri, strings.NewReader(form.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", uaMiIO)
		req.Header.Set("x-xiaomi-protocal-flag-cli", "PROTOCAL-HTTP2")
		req.Header.Set("Cookie", "userId="+sp.s.creds.UserID+"; serviceToken="+t.value+"; PassportDeviceId="+sp.s.creds.DeviceID)
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	var r struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("xiaomi: %s: %w", uri, err)
	}
	if r.Code != 0 {
		return nil, fmt.Errorf("xiaomi: %s: code %d %s", uri, r.Code, r.Message)
	}
	return r.Result, nil
}

func signMiIO(uri, data, ssecurity string) (url.Values, error) {
	ss, err := base64.StdEncoding.DecodeString(ssecurity)
	if err != nil {
		return nil, fmt.Errorf("xiaomi: bad ssecurity: %w", err)
	}
	nb := make([]byte, 12)
	if _, err := rand.Read(nb[:8]); err != nil {
		return nil, err
	}
	binary.BigEndian.PutUint32(nb[8:], uint32(time.Now().Unix()/60))
	nonce := base64.StdEncoding.EncodeToString(nb)
	h := sha256.Sum256(append(ss, nb...))
	snonce := base64.StdEncoding.EncodeToString(h[:])
	mac := hmac.New(sha256.New, h[:])
	mac.Write([]byte(strings.Join([]string{uri, snonce, nonce, "data=" + data}, "&")))
	return url.Values{"_nonce": {nonce}, "data": {data}, "signature": {base64.StdEncoding.EncodeToString(mac.Sum(nil))}}, nil
}

// Action runs a MIoT spec action (siid-aiid) on the speaker, e.g. 7-3 play
// text or 7-4 execute text directive on OH2P.
func (sp *Speaker) Action(ctx context.Context, siid, aiid int, in []any) error {
	if in == nil {
		in = []any{}
	}
	res, err := sp.miio(ctx, "/miotspec/action", map[string]any{"params": map[string]any{
		"did": sp.dev.MiotDID, "siid": siid, "aiid": aiid, "in": in,
	}})
	if err != nil {
		return err
	}
	var r struct {
		Code int `json:"code"`
	}
	if json.Unmarshal(res, &r) == nil && r.Code != 0 {
		return fmt.Errorf("xiaomi: action %d-%d: speaker code %d", siid, aiid, r.Code)
	}
	return nil
}

// Prop reads a MIoT spec property.
func (sp *Speaker) Prop(ctx context.Context, siid, piid int) (any, error) {
	res, err := sp.miio(ctx, "/miotspec/prop/get", map[string]any{"params": []any{map[string]any{
		"did": sp.dev.MiotDID, "siid": siid, "piid": piid,
	}}})
	if err != nil {
		return nil, err
	}
	var r []struct {
		Code  int `json:"code"`
		Value any `json:"value"`
	}
	if err := json.Unmarshal(res, &r); err != nil || len(r) == 0 {
		return nil, fmt.Errorf("xiaomi: prop %d-%d: bad result", siid, piid)
	}
	if r[0].Code != 0 {
		return nil, fmt.Errorf("xiaomi: prop %d-%d: code %d", siid, piid, r[0].Code)
	}
	return r[0].Value, nil
}

// Record is one exchange from the speaker's conversation log.
type Record struct {
	TimeMS    int64           `json:"time"`
	Query     string          `json:"query"`
	RequestID string          `json:"requestId"`
	Answers   json.RawMessage `json:"answers"`
}

// Conversations returns up to limit records, newest first, older than
// beforeMS when it is non-zero.
func (sp *Speaker) Conversations(ctx context.Context, limit int, beforeMS int64) ([]Record, error) {
	q := url.Values{"source": {"dialogu"}, "hardware": {sp.dev.Hardware}, "limit": {strconv.Itoa(limit)},
		"requestId": {requestID()}}
	if beforeMS > 0 {
		q.Set("timestamp", strconv.FormatInt(beforeMS, 10))
	}
	body, err := sp.s.do(ctx, sidMiNA, func(t token) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			sp.s.ep.Profile+"/device_profile/v2/conversation?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		sp.s.minaHeaders(req, t, &sp.dev)
		req.Header.Set("Referer", "https://userprofile.mina.mi.com/dialogue-note/index.html")
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	var r struct {
		Code int    `json:"code"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("xiaomi: conversation: %w", err)
	}
	if r.Code != 0 {
		return nil, fmt.Errorf("xiaomi: conversation code %d", r.Code)
	}
	var d struct {
		Records []Record `json:"records"`
	}
	if r.Data != "" {
		if err := json.Unmarshal([]byte(r.Data), &d); err != nil {
			return nil, fmt.Errorf("xiaomi: conversation data: %w", err)
		}
	}
	return d.Records, nil
}

// Do asks Xiao Ai to carry out a sentence as if the owner had said it;
// silent = do it without answering aloud. MiNA's mibrain ai_service first
// (same login as speaking; works on OH2P, 2026-11-16), MIoT 7-4
// execute-text-directive if that fails (MIoT answered HTTP 429 for our
// account on 11-14 and 11-16).
func (sp *Speaker) Do(ctx context.Context, text string, silent bool) error {
	msg := map[string]any{"nlp": 1, "nlp_text": text}
	if !silent {
		msg["tts"] = 1
	}
	_, err := sp.ubus(ctx, "mibrain", "ai_service", msg)
	if err == nil {
		return nil
	}
	if err2 := sp.Action(ctx, 7, 4, []any{text, silent}); err2 != nil {
		return fmt.Errorf("%w; MIoT: %v", err, err2)
	}
	return nil
}

// DoNotDisturb reads Xiao Ai's own do-not-disturb switch (no-disturb 9-1).
func (sp *Speaker) DoNotDisturb(ctx context.Context) (bool, error) {
	v, err := sp.Prop(ctx, 9, 1)
	if err != nil {
		return false, err
	}
	on, _ := v.(bool)
	return on, nil
}

// SetDoNotDisturb switches Xiao Ai's own do-not-disturb.
func (sp *Speaker) SetDoNotDisturb(ctx context.Context, on bool) error {
	res, err := sp.miio(ctx, "/miotspec/prop/set", map[string]any{"params": []any{map[string]any{
		"did": sp.dev.MiotDID, "siid": 9, "piid": 1, "value": on,
	}}})
	if err != nil {
		return err
	}
	var r []struct {
		Code int `json:"code"`
	}
	if json.Unmarshal(res, &r) == nil && len(r) > 0 && r[0].Code != 0 {
		return fmt.Errorf("xiaomi: set do-not-disturb: code %d", r[0].Code)
	}
	return nil
}
