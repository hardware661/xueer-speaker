// Package xiaomitest is a fake Xiaomi cloud (account, MiNA, MIoT) for
// tests, behaving like the real endpoints did on 2026-11-14.
package xiaomitest

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/hardware661/xueer-speaker/internal/xiaomi"
)

// Cloud stands in for account.xiaomi.com, api2.mina.mi.com and
// api.io.mi.com behind one test server. Behaviour mirrors what the real
// endpoints did on 2026-11-14 (see login.go).
type Cloud struct {
	Srv *httptest.Server

	Mu            sync.Mutex
	PassToken     string // the only passToken serviceLogin accepts
	DeviceID      string // passToken is bound to this deviceId
	ServiceTokens map[string]string
	StsCalls      int
	Ubus          []url.Values // every /remote/ubus form, in order
	ExpireNext    bool         // the next MiNA call answers 401
	UbusReply     string       // "" = success with status playing, volume 30
	Actions       []map[string]any
	Records       []map[string]any // newest first, like the real endpoint

	Props map[string]any // "siid-piid" → value (MIoT properties)

	// Password login (login.go): one account, an SMS code when needOTP.
	User, Password, OTP string
	NeedOTP             bool
	OTPSent             int
}

// New starts a fake cloud; t.Cleanup stops it.
func New(t testing.TB) *Cloud {
	f := &Cloud{PassToken: "V1:good", DeviceID: "an_fixed", ServiceTokens: map[string]string{},
		User: "speaker@example.com", Password: "secret", OTP: "123456", NeedOTP: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/pass/serviceLogin", f.serviceLogin)
	mux.HandleFunc("/sts", f.sts)
	mux.HandleFunc("/pass/serviceLoginAuth2", f.auth2)
	mux.HandleFunc("/identity/authStart", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	mux.HandleFunc("/identity/list", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `&&&START&&&{"code":2,"flag":4}`) })
	mux.HandleFunc("/identity/auth/sendPhoneTicket", f.sendTicket)
	mux.HandleFunc("/identity/auth/verifyPhone", f.verifyPhone)
	mux.HandleFunc("/identity/result/check", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", f.Srv.URL+"/pass/serviceLoginAuth2/end?sid=micoapi")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/pass/serviceLoginAuth2/end", f.auth2End)
	mux.HandleFunc("/admin/v2/device_list", f.deviceList)
	mux.HandleFunc("/remote/ubus", f.remoteUbus)
	mux.HandleFunc("/miotspec/action", f.miotAction)
	mux.HandleFunc("/miotspec/prop/get", f.miotProps)
	mux.HandleFunc("/miotspec/prop/set", f.miotProps)
	mux.HandleFunc("/device_profile/v2/conversation", f.conversation)
	f.Srv = httptest.NewServer(mux)
	t.Cleanup(f.Srv.Close)
	return f
}

// Endpoints point a session at the fake.
func (f *Cloud) Endpoints() xiaomi.Endpoints {
	return xiaomi.Endpoints{Account: f.Srv.URL, MiNA: f.Srv.URL, MiIO: f.Srv.URL, Profile: f.Srv.URL}
}

func cookie(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

func (f *Cloud) serviceLogin(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	f.Mu.Lock()
	ok := cookie(r, "passToken") == f.PassToken && cookie(r, "deviceId") == f.DeviceID
	f.Mu.Unlock()
	if !ok {
		fmt.Fprintf(w, `&&&START&&&{"code":70016,"description":"登录验证失败","qs":"%%3Fsid","sid":"%s","_sign":"sign","callback":"https://sts"}`, sid)
		return
	}
	nonce := "n-" + sid
	loc := f.Srv.URL + "/sts?sid=" + sid + "&nonce=" + nonce
	fmt.Fprintf(w, `&&&START&&&{"code":0,"userId":2225000001,"nonce":"%s","ssecurity":"%s","location":"%s","passToken":"%s"}`,
		nonce, SSecurityFor(sid), loc, f.PassToken)
}

// auth2 checks the password (MD5, upper hex, as the app sends it). With
// needOTP it answers a notificationUrl; otherwise it logs in right away.
func (f *Cloud) auth2(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	sum := md5.Sum([]byte(f.Password))
	if r.PostForm.Get("user") != f.User || r.PostForm.Get("hash") != strings.ToUpper(hex.EncodeToString(sum[:])) {
		fmt.Fprint(w, `&&&START&&&{"code":70016,"desc":"密码错误"}`)
		return
	}
	if r.PostForm.Get("_sign") == "" || r.PostForm.Get("qs") == "" {
		fmt.Fprint(w, `&&&START&&&{"code":-1,"desc":"missing sign"}`)
		return
	}
	if f.NeedOTP {
		fmt.Fprint(w, `&&&START&&&{"code":0,"notificationUrl":"/identity/authStart?sid=micoapi&context=CTX"}`)
		return
	}
	f.issuePassToken(w, r)
	fmt.Fprintf(w, `&&&START&&&{"code":0,"userId":2225000001,"nonce":"n-pw","ssecurity":"%s","location":"%s/sts?sid=micoapi&nonce=n-pw"}`,
		SSecurityFor("micoapi"), f.Srv.URL)
}

func (f *Cloud) sendTicket(w http.ResponseWriter, r *http.Request) {
	f.Mu.Lock()
	f.OTPSent++
	f.Mu.Unlock()
	fmt.Fprint(w, `&&&START&&&{"code":0}`)
}

func (f *Cloud) verifyPhone(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		fmt.Fprint(w, `&&&START&&&{"code":0}`)
		return
	}
	r.ParseForm()
	if r.PostForm.Get("ticket") != f.OTP || r.PostForm.Get("trust") != "true" {
		fmt.Fprint(w, `&&&START&&&{"code":70014,"tips":"验证码错误"}`)
		return
	}
	fmt.Fprintf(w, `&&&START&&&{"code":0,"location":"%s/identity/result/check?x=1"}`, f.Srv.URL)
}

// auth2End hands out the new passToken, bound to the deviceId the login
// used, and points at sts — which 401s without clientSign, like the real one.
func (f *Cloud) auth2End(w http.ResponseWriter, r *http.Request) {
	f.issuePassToken(w, r)
	w.Header().Set("extension-pragma", fmt.Sprintf(`{"ssecurity":"%s"}`, SSecurityFor("micoapi")))
	w.Header().Set("Location", f.Srv.URL+"/sts?sid=micoapi&nonce=n-end")
	w.WriteHeader(http.StatusFound)
}

func (f *Cloud) issuePassToken(w http.ResponseWriter, r *http.Request) {
	f.Mu.Lock()
	f.PassToken, f.DeviceID = "V1:fresh", cookie(r, "deviceId")
	f.Mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "passToken", Value: "V1:fresh", Path: "/"})
	http.SetCookie(w, &http.Cookie{Name: "userId", Value: "2225000001", Path: "/"})
}

func (f *Cloud) sts(w http.ResponseWriter, r *http.Request) {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	f.StsCalls++
	q := r.URL.Query()
	sum := sha1.Sum([]byte("nonce=" + q.Get("nonce") + "&" + SSecurityFor(q.Get("sid"))))
	if q.Get("clientSign") != base64.StdEncoding.EncodeToString(sum[:]) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	tok := fmt.Sprintf("st-%s-%d", q.Get("sid"), f.StsCalls)
	f.ServiceTokens[q.Get("sid")] = tok
	http.SetCookie(w, &http.Cookie{Name: "serviceToken", Value: tok})
	fmt.Fprint(w, "ok")
}

func (f *Cloud) minaAuthorized(w http.ResponseWriter, r *http.Request) bool {
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if f.ExpireNext {
		f.ExpireNext = false
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	if tok := cookie(r, "serviceToken"); tok == "" || tok != f.ServiceTokens["micoapi"] {
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	return true
}

func (f *Cloud) deviceList(w http.ResponseWriter, r *http.Request) {
	if !f.minaAuthorized(w, r) {
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": []map[string]any{
		{"deviceID": "mina-dev-1", "miotDID": "123456", "name": "Xiaomi 智能音箱 Pro", "alias": "Xiaomi 智能音箱 Pro",
			"hardware": "OH2P", "romVersion": "1.60.3", "presence": "online", "serialNumber": "SN1", "mac": "AA:BB"},
	}})
}

func (f *Cloud) remoteUbus(w http.ResponseWriter, r *http.Request) {
	if !f.minaAuthorized(w, r) {
		return
	}
	r.ParseForm()
	f.Mu.Lock()
	f.Ubus = append(f.Ubus, r.PostForm)
	f.Mu.Unlock()
	if f.UbusReply != "" {
		fmt.Fprint(w, f.UbusReply)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"code": 0,
		"info": `{"status":1,"volume":30,"play_song_detail":{"position":12000,"duration":200000}}`}})
}

// miotSigned checks the xiaomiio token and signature exactly as MiService
// builds them and returns the decoded params.
func (f *Cloud) miotSigned(w http.ResponseWriter, r *http.Request, uri string) (json.RawMessage, bool) {
	r.ParseForm()
	if cookie(r, "serviceToken") == "" || cookie(r, "serviceToken") != f.ServiceTokens["xiaomiio"] {
		w.WriteHeader(http.StatusUnauthorized)
		return nil, false
	}
	nonce, data := r.PostForm.Get("_nonce"), r.PostForm.Get("data")
	ss, _ := base64.StdEncoding.DecodeString(SSecurityFor("xiaomiio"))
	nb, _ := base64.StdEncoding.DecodeString(nonce)
	h := sha256.Sum256(append(ss, nb...))
	mac := hmac.New(sha256.New, h[:])
	mac.Write([]byte(strings.Join([]string{uri, base64.StdEncoding.EncodeToString(h[:]), nonce, "data=" + data}, "&")))
	if r.PostForm.Get("signature") != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
		fmt.Fprint(w, `{"code":-8,"message":"bad sign"}`)
		return nil, false
	}
	var body struct{ Params json.RawMessage }
	json.Unmarshal([]byte(data), &body)
	return body.Params, true
}

// miotProps serves prop/get and prop/set on f.Props.
func (f *Cloud) miotProps(w http.ResponseWriter, r *http.Request) {
	params, ok := f.miotSigned(w, r, r.URL.Path)
	if !ok {
		return
	}
	var ps []struct {
		Siid, Piid int
		Value      any
	}
	json.Unmarshal(params, &ps)
	f.Mu.Lock()
	defer f.Mu.Unlock()
	if f.Props == nil {
		f.Props = map[string]any{}
	}
	out := []map[string]any{}
	for _, p := range ps {
		k := fmt.Sprintf("%d-%d", p.Siid, p.Piid)
		if strings.HasSuffix(r.URL.Path, "/set") {
			f.Props[k] = p.Value
			out = append(out, map[string]any{"siid": p.Siid, "piid": p.Piid, "code": 0})
			continue
		}
		out = append(out, map[string]any{"siid": p.Siid, "piid": p.Piid, "code": 0, "value": f.Props[k]})
	}
	json.NewEncoder(w).Encode(map[string]any{"code": 0, "result": out})
}

// miotAction checks the xiaomiio signature exactly as MiService builds it.
func (f *Cloud) miotAction(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	if cookie(r, "serviceToken") == "" || cookie(r, "serviceToken") != f.ServiceTokens["xiaomiio"] {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	nonce, data := r.PostForm.Get("_nonce"), r.PostForm.Get("data")
	ss, _ := base64.StdEncoding.DecodeString(SSecurityFor("xiaomiio"))
	nb, _ := base64.StdEncoding.DecodeString(nonce)
	h := sha256.Sum256(append(ss, nb...))
	mac := hmac.New(sha256.New, h[:])
	mac.Write([]byte(strings.Join([]string{"/miotspec/action", base64.StdEncoding.EncodeToString(h[:]), nonce, "data=" + data}, "&")))
	if r.PostForm.Get("signature") != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
		fmt.Fprint(w, `{"code":-8,"message":"bad sign"}`)
		return
	}
	var body struct{ Params map[string]any }
	json.Unmarshal([]byte(data), &body)
	f.Mu.Lock()
	f.Actions = append(f.Actions, body.Params)
	f.Mu.Unlock()
	fmt.Fprint(w, `{"code":0,"result":{"code":0}}`)
}

func (f *Cloud) conversation(w http.ResponseWriter, r *http.Request) {
	if !f.minaAuthorized(w, r) {
		return
	}
	data, _ := json.Marshal(map[string]any{"records": f.Records})
	json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": string(data)})
}

// SSecurityFor is the fake ssecurity for sid.
func SSecurityFor(sid string) string { return base64.StdEncoding.EncodeToString([]byte("ss-" + sid)) }
