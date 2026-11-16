package xiaomi

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
)

// fakeCloud stands in for account.xiaomi.com, api2.mina.mi.com and
// api.io.mi.com behind one test server. Behaviour mirrors what the real
// endpoints did on 2026-11-14 (see login.go).
type fakeCloud struct {
	t   *testing.T
	srv *httptest.Server

	mu            sync.Mutex
	passToken     string // the only passToken serviceLogin accepts
	deviceID      string // passToken is bound to this deviceId
	serviceTokens map[string]string
	stsCalls      int
	ubus          []url.Values // every /remote/ubus form, in order
	expireNext    bool         // the next MiNA call answers 401
	ubusReply     string       // "" = success with status playing, volume 30
	actions       []map[string]any
	records       []map[string]any // newest first, like the real endpoint

	props map[string]any // "siid-piid" → value (MIoT properties)

	// Password login (login.go): one account, an SMS code when needOTP.
	user, password, otp string
	needOTP             bool
	otpSent             int
}

func newFakeCloud(t *testing.T) *fakeCloud {
	f := &fakeCloud{t: t, passToken: "V1:good", deviceID: "an_fixed", serviceTokens: map[string]string{},
		user: "speaker@example.com", password: "secret", otp: "123456", needOTP: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/pass/serviceLogin", f.serviceLogin)
	mux.HandleFunc("/sts", f.sts)
	mux.HandleFunc("/pass/serviceLoginAuth2", f.auth2)
	mux.HandleFunc("/identity/authStart", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	mux.HandleFunc("/identity/list", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `&&&START&&&{"code":2,"flag":4}`) })
	mux.HandleFunc("/identity/auth/sendPhoneTicket", f.sendTicket)
	mux.HandleFunc("/identity/auth/verifyPhone", f.verifyPhone)
	mux.HandleFunc("/identity/result/check", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", f.srv.URL+"/pass/serviceLoginAuth2/end?sid=micoapi")
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/pass/serviceLoginAuth2/end", f.auth2End)
	mux.HandleFunc("/admin/v2/device_list", f.deviceList)
	mux.HandleFunc("/remote/ubus", f.remoteUbus)
	mux.HandleFunc("/miotspec/action", f.miotAction)
	mux.HandleFunc("/miotspec/prop/get", f.miotProps)
	mux.HandleFunc("/miotspec/prop/set", f.miotProps)
	mux.HandleFunc("/device_profile/v2/conversation", f.conversation)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCloud) endpoints() Endpoints {
	return Endpoints{Account: f.srv.URL, MiNA: f.srv.URL, MiIO: f.srv.URL, Profile: f.srv.URL}
}

func cookie(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

func (f *fakeCloud) serviceLogin(w http.ResponseWriter, r *http.Request) {
	sid := r.URL.Query().Get("sid")
	f.mu.Lock()
	ok := cookie(r, "passToken") == f.passToken && cookie(r, "deviceId") == f.deviceID
	f.mu.Unlock()
	if !ok {
		fmt.Fprintf(w, `&&&START&&&{"code":70016,"description":"登录验证失败","qs":"%%3Fsid","sid":"%s","_sign":"sign","callback":"https://sts"}`, sid)
		return
	}
	nonce := "n-" + sid
	loc := f.srv.URL + "/sts?sid=" + sid + "&nonce=" + nonce
	fmt.Fprintf(w, `&&&START&&&{"code":0,"userId":2225000001,"nonce":"%s","ssecurity":"%s","location":"%s","passToken":"%s"}`,
		nonce, ssecurityFor(sid), loc, f.passToken)
}

// auth2 checks the password (MD5, upper hex, as the app sends it). With
// needOTP it answers a notificationUrl; otherwise it logs in right away.
func (f *fakeCloud) auth2(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	sum := md5.Sum([]byte(f.password))
	if r.PostForm.Get("user") != f.user || r.PostForm.Get("hash") != strings.ToUpper(hex.EncodeToString(sum[:])) {
		fmt.Fprint(w, `&&&START&&&{"code":70016,"desc":"密码错误"}`)
		return
	}
	if r.PostForm.Get("_sign") == "" || r.PostForm.Get("qs") == "" {
		fmt.Fprint(w, `&&&START&&&{"code":-1,"desc":"missing sign"}`)
		return
	}
	if f.needOTP {
		fmt.Fprint(w, `&&&START&&&{"code":0,"notificationUrl":"/identity/authStart?sid=micoapi&context=CTX"}`)
		return
	}
	f.issuePassToken(w, r)
	fmt.Fprintf(w, `&&&START&&&{"code":0,"userId":2225000001,"nonce":"n-pw","ssecurity":"%s","location":"%s/sts?sid=micoapi&nonce=n-pw"}`,
		ssecurityFor("micoapi"), f.srv.URL)
}

func (f *fakeCloud) sendTicket(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.otpSent++
	f.mu.Unlock()
	fmt.Fprint(w, `&&&START&&&{"code":0}`)
}

func (f *fakeCloud) verifyPhone(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		fmt.Fprint(w, `&&&START&&&{"code":0}`)
		return
	}
	r.ParseForm()
	if r.PostForm.Get("ticket") != f.otp || r.PostForm.Get("trust") != "true" {
		fmt.Fprint(w, `&&&START&&&{"code":70014,"tips":"验证码错误"}`)
		return
	}
	fmt.Fprintf(w, `&&&START&&&{"code":0,"location":"%s/identity/result/check?x=1"}`, f.srv.URL)
}

// auth2End hands out the new passToken, bound to the deviceId the login
// used, and points at sts — which 401s without clientSign, like the real one.
func (f *fakeCloud) auth2End(w http.ResponseWriter, r *http.Request) {
	f.issuePassToken(w, r)
	w.Header().Set("extension-pragma", fmt.Sprintf(`{"ssecurity":"%s"}`, ssecurityFor("micoapi")))
	w.Header().Set("Location", f.srv.URL+"/sts?sid=micoapi&nonce=n-end")
	w.WriteHeader(http.StatusFound)
}

func (f *fakeCloud) issuePassToken(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.passToken, f.deviceID = "V1:fresh", cookie(r, "deviceId")
	f.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "passToken", Value: "V1:fresh", Path: "/"})
	http.SetCookie(w, &http.Cookie{Name: "userId", Value: "2225000001", Path: "/"})
}

func (f *fakeCloud) sts(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stsCalls++
	q := r.URL.Query()
	sum := sha1.Sum([]byte("nonce=" + q.Get("nonce") + "&" + ssecurityFor(q.Get("sid"))))
	if q.Get("clientSign") != base64.StdEncoding.EncodeToString(sum[:]) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	tok := fmt.Sprintf("st-%s-%d", q.Get("sid"), f.stsCalls)
	f.serviceTokens[q.Get("sid")] = tok
	http.SetCookie(w, &http.Cookie{Name: "serviceToken", Value: tok})
	fmt.Fprint(w, "ok")
}

func (f *fakeCloud) minaAuthorized(w http.ResponseWriter, r *http.Request) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.expireNext {
		f.expireNext = false
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	if tok := cookie(r, "serviceToken"); tok == "" || tok != f.serviceTokens["micoapi"] {
		w.WriteHeader(http.StatusUnauthorized)
		return false
	}
	return true
}

func (f *fakeCloud) deviceList(w http.ResponseWriter, r *http.Request) {
	if !f.minaAuthorized(w, r) {
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": []map[string]any{
		{"deviceID": "mina-dev-1", "miotDID": "123456", "name": "Xiaomi 智能音箱 Pro", "alias": "Xiaomi 智能音箱 Pro",
			"hardware": "OH2P", "romVersion": "1.60.3", "presence": "online", "serialNumber": "SN1", "mac": "AA:BB"},
	}})
}

func (f *fakeCloud) remoteUbus(w http.ResponseWriter, r *http.Request) {
	if !f.minaAuthorized(w, r) {
		return
	}
	r.ParseForm()
	f.mu.Lock()
	f.ubus = append(f.ubus, r.PostForm)
	f.mu.Unlock()
	if f.ubusReply != "" {
		fmt.Fprint(w, f.ubusReply)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"code": 0,
		"info": `{"status":1,"volume":30,"play_song_detail":{"position":12000,"duration":200000}}`}})
}

// miotSigned checks the xiaomiio token and signature exactly as MiService
// builds them and returns the decoded params.
func (f *fakeCloud) miotSigned(w http.ResponseWriter, r *http.Request, uri string) (json.RawMessage, bool) {
	r.ParseForm()
	if cookie(r, "serviceToken") == "" || cookie(r, "serviceToken") != f.serviceTokens["xiaomiio"] {
		w.WriteHeader(http.StatusUnauthorized)
		return nil, false
	}
	nonce, data := r.PostForm.Get("_nonce"), r.PostForm.Get("data")
	ss, _ := base64.StdEncoding.DecodeString(ssecurityFor("xiaomiio"))
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

// miotProps serves prop/get and prop/set on f.props.
func (f *fakeCloud) miotProps(w http.ResponseWriter, r *http.Request) {
	params, ok := f.miotSigned(w, r, r.URL.Path)
	if !ok {
		return
	}
	var ps []struct {
		Siid, Piid int
		Value      any
	}
	json.Unmarshal(params, &ps)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.props == nil {
		f.props = map[string]any{}
	}
	out := []map[string]any{}
	for _, p := range ps {
		k := fmt.Sprintf("%d-%d", p.Siid, p.Piid)
		if strings.HasSuffix(r.URL.Path, "/set") {
			f.props[k] = p.Value
			out = append(out, map[string]any{"siid": p.Siid, "piid": p.Piid, "code": 0})
			continue
		}
		out = append(out, map[string]any{"siid": p.Siid, "piid": p.Piid, "code": 0, "value": f.props[k]})
	}
	json.NewEncoder(w).Encode(map[string]any{"code": 0, "result": out})
}

// miotAction checks the xiaomiio signature exactly as MiService builds it.
func (f *fakeCloud) miotAction(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	if cookie(r, "serviceToken") == "" || cookie(r, "serviceToken") != f.serviceTokens["xiaomiio"] {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	nonce, data := r.PostForm.Get("_nonce"), r.PostForm.Get("data")
	ss, _ := base64.StdEncoding.DecodeString(ssecurityFor("xiaomiio"))
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
	f.mu.Lock()
	f.actions = append(f.actions, body.Params)
	f.mu.Unlock()
	fmt.Fprint(w, `{"code":0,"result":{"code":0}}`)
}

func (f *fakeCloud) conversation(w http.ResponseWriter, r *http.Request) {
	if !f.minaAuthorized(w, r) {
		return
	}
	data, _ := json.Marshal(map[string]any{"records": f.records})
	json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": string(data)})
}

func ssecurityFor(sid string) string { return base64.StdEncoding.EncodeToString([]byte("ss-" + sid)) }
