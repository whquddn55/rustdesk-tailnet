package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"rustdesk-tailnet-api/internal/netguard"
	"rustdesk-tailnet-api/internal/store"
)

type env struct {
	t   *testing.T
	st  *store.Store
	srv *Server
	now time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{t: t, st: st, now: time.Unix(1_800_000_000, 0)}
	st.SetClock(func() time.Time { return e.now })
	e.srv = New(Config{Store: st, Guard: netguard.Policy{}, TokenTTL: 24 * time.Hour})
	return e
}

const tsIP = "100.101.1.1:40000"

func (e *env) do(method, path, token, remote string, body any) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		buf, _ := json.Marshal(b)
		rd = bytes.NewReader(buf)
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = remote
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func (e *env) mustUser(name, pw, group string) {
	e.t.Helper()
	if _, err := e.st.AddUser(e.t.Context(), store.NewUser{Name: name, Password: pw, Group: group, DisplayName: strings.ToUpper(name)}); err != nil {
		e.t.Fatal(err)
	}
}

// login mimics the Flutter client (LoginRequest.toJson) and returns the token.
func (e *env) login(user, pw, id, uuid, remote string) string {
	e.t.Helper()
	rec := e.do("POST", "/api/login", "", remote, map[string]any{
		"username": user, "password": pw, "id": id, "uuid": uuid, "autoLogin": true, "type": "account",
		"deviceInfo": map[string]string{"os": "windows", "type": "client", "name": "pc"},
	})
	if rec.Code != 200 {
		e.t.Fatalf("login %s: %d %s", user, rec.Code, rec.Body)
	}
	resp := decode[map[string]any](e.t, rec)
	if resp["type"] != "access_token" || resp["access_token"] == "" {
		e.t.Fatalf("bad login response %v", resp)
	}
	return resp["access_token"].(string)
}

// registerDevice mimics sync.rs: heartbeat, sysinfo if asked, heartbeat again.
func (e *env) registerDevice(id, uuid, host, osName, osUser, remote string) {
	e.t.Helper()
	hb := map[string]any{"id": id, "uuid": uuid, "ver": 1004000, "modified_at": 0}
	rec := e.do("POST", "/api/heartbeat", "", remote, hb)
	if rec.Code != 200 {
		e.t.Fatalf("heartbeat: %d %s", rec.Code, rec.Body)
	}
	if _, ok := decode[map[string]any](e.t, rec)["sysinfo"]; !ok {
		e.t.Fatalf("first heartbeat should request sysinfo, got %s", rec.Body)
	}
	rec = e.do("POST", "/api/sysinfo", "", remote, map[string]any{
		"id": id, "uuid": uuid, "version": "1.4.0", "hostname": host, "os": osName, "username": osUser,
		"cpu": "x, 3GHz, 8/4 cores", "memory": "16GB",
	})
	if rec.Body.String() != "SYSINFO_UPDATED" {
		e.t.Fatalf("sysinfo reply %q", rec.Body)
	}
	rec = e.do("POST", "/api/heartbeat", "", remote, hb)
	if _, ok := decode[map[string]any](e.t, rec)["sysinfo"]; ok {
		e.t.Fatalf("heartbeat after sysinfo should not request it again: %s", rec.Body)
	}
}

func peerIDs(p paged[peerPayload]) []string {
	var ids []string
	for _, x := range p.Data {
		ids = append(ids, x.ID)
	}
	sort.Strings(ids)
	return ids
}

func userNames(p paged[userPayload]) []string {
	var ns []string
	for _, x := range p.Data {
		ns = append(ns, x.Name)
	}
	sort.Strings(ns)
	return ns
}

func TestGroupVisibilityFlow(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	for _, g := range []string{"family", "office"} {
		if _, err := e.st.AddGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	e.mustUser("alice", "pw-a", "family")
	e.mustUser("bob", "pw-b", "family")
	e.mustUser("carol", "pw-c", "office")

	// Devices report heartbeat/sysinfo before and independently of login.
	e.registerDevice("111111111", "dXVpZC1h", "alice-pc", "windows / Windows 11 Pro - 10.0.26100", "Alice", "100.101.1.1:40000")
	e.registerDevice("222222222", "dXVpZC1i", "bob-mac", "macos / macOS 15.1", "bob", "100.101.1.2:40000")
	e.registerDevice("333333333", "dXVpZC1j", "carol-linux", "ubuntu / Linux 24.04 Ubuntu", "carol", "[fd7a:115c:a1e0::3]:40000")

	ta := e.login("alice", "pw-a", "111111111", "dXVpZC1h", "100.101.1.1:40000")
	tb := e.login("bob", "pw-b", "222222222", "dXVpZC1i", "100.101.1.2:40000")
	tc := e.login("carol", "pw-c", "333333333", "dXVpZC1j", "[fd7a:115c:a1e0::3]:40000")

	// Same query string the client sends (group_model.dart:233-238).
	q := "?current=1&pageSize=100&accessible=&status=1"

	// alice sees both family devices, not carol's.
	rec := e.do("GET", "/api/peers"+q, ta, tsIP, nil)
	if rec.Code != 200 {
		t.Fatalf("peers: %d %s", rec.Code, rec.Body)
	}
	pa := decode[paged[peerPayload]](t, rec)
	if got := peerIDs(pa); strings.Join(got, ",") != "111111111,222222222" || pa.Total != 2 {
		t.Fatalf("alice peers = %v total %d", got, pa.Total)
	}
	for _, p := range pa.Data {
		switch p.ID {
		case "111111111":
			if p.UserName != "alice" || p.Info.DeviceName != "alice-pc" || p.Info.Username != "Alice" || !strings.HasPrefix(p.Info.OS, "windows") || p.DeviceGroupName != "family" {
				t.Errorf("alice device payload %+v", p)
			}
		case "222222222":
			if p.UserName != "bob" || p.Info.DeviceName != "bob-mac" {
				t.Errorf("bob device payload %+v", p)
			}
		}
	}

	// bob sees the same set.
	pb := decode[paged[peerPayload]](t, e.do("GET", "/api/peers"+q, tb, tsIP, nil))
	if got := peerIDs(pb); strings.Join(got, ",") != "111111111,222222222" {
		t.Fatalf("bob peers = %v", got)
	}

	// carol (other group) sees only her own.
	pc := decode[paged[peerPayload]](t, e.do("GET", "/api/peers"+q, tc, tsIP, nil))
	if got := peerIDs(pc); strings.Join(got, ",") != "333333333" {
		t.Fatalf("carol peers = %v", got)
	}

	// users
	ua := decode[paged[userPayload]](t, e.do("GET", "/api/users"+q, ta, tsIP, nil))
	if got := userNames(ua); strings.Join(got, ",") != "alice,bob" {
		t.Fatalf("alice users = %v", got)
	}
	uc := decode[paged[userPayload]](t, e.do("GET", "/api/users"+q, tc, tsIP, nil))
	if got := userNames(uc); strings.Join(got, ",") != "carol" {
		t.Fatalf("carol users = %v", got)
	}

	// device groups
	dg := decode[paged[deviceGroupPayload]](t, e.do("GET", "/api/device-group/accessible?current=1&pageSize=100", ta, tsIP, nil))
	if dg.Total != 1 || dg.Data[0].Name != "family" {
		t.Fatalf("device groups = %+v", dg)
	}

	// pagination
	p2 := decode[paged[peerPayload]](t, e.do("GET", "/api/peers?current=2&pageSize=1", ta, tsIP, nil))
	if p2.Total != 2 || len(p2.Data) != 1 || p2.Data[0].ID != "222222222" {
		t.Fatalf("page 2 = %+v", p2)
	}
	p3 := decode[paged[peerPayload]](t, e.do("GET", "/api/peers?current=3&pageSize=1", ta, tsIP, nil))
	if p3.Total != 2 || len(p3.Data) != 0 {
		t.Fatalf("page 3 = %+v", p3)
	}

	// Moving carol into family makes her device visible to alice.
	if err := e.st.SetUserGroup(ctx, "carol", "family"); err != nil {
		t.Fatal(err)
	}
	pa = decode[paged[peerPayload]](t, e.do("GET", "/api/peers"+q, ta, tsIP, nil))
	if got := peerIDs(pa); len(got) != 3 {
		t.Fatalf("after set-group alice peers = %v", got)
	}

	// Logout unbinds the device: bob's device disappears from alice's list.
	rec = e.do("POST", "/api/logout", tb, tsIP, map[string]string{"id": "222222222", "uuid": "dXVpZC1i"})
	if rec.Code != 200 {
		t.Fatalf("logout %d", rec.Code)
	}
	pa = decode[paged[peerPayload]](t, e.do("GET", "/api/peers"+q, ta, tsIP, nil))
	if got := peerIDs(pa); strings.Join(got, ",") != "111111111,333333333" {
		t.Fatalf("after bob logout alice peers = %v", got)
	}
	// bob's token is revoked.
	if rec := e.do("POST", "/api/currentUser", tb, tsIP, map[string]string{"id": "222222222", "uuid": "dXVpZC1i"}); rec.Code != 401 {
		t.Fatalf("revoked token currentUser = %d", rec.Code)
	}

	// The last IP of a device is the Tailscale peer address.
	devs, _ := e.st.ListDevices(ctx)
	for _, d := range devs {
		if d.RustdeskID == "333333333" && d.LastIP != "fd7a:115c:a1e0::3" {
			t.Errorf("last ip %q", d.LastIP)
		}
	}
}

func TestNonTailscaleRemoteIs403(t *testing.T) {
	e := newEnv(t)
	for _, remote := range []string{"192.168.1.20:5000", "8.8.8.8:5000", "127.0.0.1:5000", "10.0.0.2:5000", "[2001:db8::1]:5000"} {
		for _, path := range []string{"/api/login-options", "/api/heartbeat", "/api/peers", "/api/nope"} {
			method := "GET"
			if path == "/api/heartbeat" {
				method = "POST"
			}
			req := httptest.NewRequest(method, path, strings.NewReader(`{"id":"1","uuid":"u"}`))
			req.RemoteAddr = remote
			req.Header.Set("X-Forwarded-For", "100.101.1.1")
			req.Header.Set("X-Real-IP", "100.101.1.1")
			rec := httptest.NewRecorder()
			e.srv.ServeHTTP(rec, req)
			if rec.Code != 403 {
				t.Errorf("%s %s from %s: %d", method, path, remote, rec.Code)
			}
		}
	}
	// Nothing was written for the rejected heartbeat.
	if devs, _ := e.st.ListDevices(t.Context()); len(devs) != 0 {
		t.Fatalf("rejected requests created devices: %+v", devs)
	}
}

func TestAuthAndLoginErrors(t *testing.T) {
	e := newEnv(t)
	e.mustUser("alice", "pw-a", "")

	// Wrong password -> non-200 with {"error"} (user_model.dart:209-211).
	rec := e.do("POST", "/api/login", "", tsIP, map[string]any{"username": "alice", "password": "nope", "type": "account"})
	if rec.Code != 401 || decode[map[string]any](t, rec)["error"] == nil {
		t.Fatalf("bad password: %d %s", rec.Code, rec.Body)
	}
	rec = e.do("POST", "/api/login", "", tsIP, map[string]any{"username": "ghost", "password": "x"})
	if rec.Code != 401 {
		t.Fatalf("unknown user: %d", rec.Code)
	}

	for _, p := range []string{"/api/users", "/api/peers", "/api/device-group/accessible", "/api/ab"} {
		if rec := e.do("GET", p, "", tsIP, nil); rec.Code != 401 {
			t.Errorf("%s without token: %d", p, rec.Code)
		}
		if rec := e.do("GET", p, "bogus", tsIP, nil); rec.Code != 401 {
			t.Errorf("%s bogus token: %d", p, rec.Code)
		}
	}

	// User without group sees only themselves and their own device.
	tok := e.login("alice", "pw-a", "123", "u-1", tsIP)
	us := decode[paged[userPayload]](t, e.do("GET", "/api/users", tok, tsIP, nil))
	if userNames(us)[0] != "alice" || us.Total != 1 {
		t.Fatalf("users %+v", us)
	}
	ps := decode[paged[peerPayload]](t, e.do("GET", "/api/peers", tok, tsIP, nil))
	if ps.Total != 1 || ps.Data[0].ID != "123" || ps.Data[0].DeviceGroupName != "" {
		t.Fatalf("peers %+v", ps)
	}
	dg := decode[paged[deviceGroupPayload]](t, e.do("GET", "/api/device-group/accessible", tok, tsIP, nil))
	if dg.Total != 0 || dg.Data == nil {
		t.Fatalf("device groups %+v (data must be [] not null)", dg)
	}

	// Sliding expiry: still valid at 20h (renewed past half-life), then expires after a full idle TTL.
	e.now = e.now.Add(20 * time.Hour)
	if rec := e.do("POST", "/api/currentUser", tok, tsIP, nil); rec.Code != 200 {
		t.Fatalf("token at 20h: %d", rec.Code)
	}
	e.now = e.now.Add(23 * time.Hour)
	if rec := e.do("POST", "/api/currentUser", tok, tsIP, nil); rec.Code != 200 {
		t.Fatalf("token renewed should be valid: %d", rec.Code)
	}
	e.now = e.now.Add(25 * time.Hour)
	if rec := e.do("POST", "/api/currentUser", tok, tsIP, nil); rec.Code != 401 {
		t.Fatalf("expired token: %d", rec.Code)
	}

	// Disabled user cannot log in and existing tokens die.
	tok = e.login("alice", "pw-a", "123", "u-1", tsIP)
	if err := e.st.SetUserStatus(t.Context(), "alice", 0); err != nil {
		t.Fatal(err)
	}
	if rec := e.do("GET", "/api/peers", tok, tsIP, nil); rec.Code != 401 {
		t.Fatalf("disabled user token: %d", rec.Code)
	}
	if rec := e.do("POST", "/api/login", "", tsIP, map[string]any{"username": "alice", "password": "pw-a"}); rec.Code != 401 {
		t.Fatalf("disabled login: %d", rec.Code)
	}
}

func TestHeartbeatSysinfoEdgeCases(t *testing.T) {
	e := newEnv(t)
	if rec := e.do("POST", "/api/sysinfo", "", tsIP, `{}`); rec.Body.String() != "ID_NOT_FOUND" {
		t.Fatalf("sysinfo without id: %q", rec.Body)
	}
	if rec := e.do("POST", "/api/heartbeat", "", tsIP, `{"id":"1"}`); rec.Code != 400 {
		t.Fatalf("heartbeat without uuid: %d", rec.Code)
	}
	// Heartbeat with active connections (sync.rs:240-242) is accepted.
	rec := e.do("POST", "/api/heartbeat", "", tsIP, `{"id":"9","uuid":"u9","ver":1004000,"conns":[1,2],"modified_at":0}`)
	if rec.Code != 200 {
		t.Fatalf("heartbeat with conns: %d %s", rec.Code, rec.Body)
	}
	// Unbound devices are not visible to anyone.
	e.mustUser("x", "pw", "")
	tok := e.login("x", "pw", "", "", tsIP) // older client without id/uuid
	ps := decode[paged[peerPayload]](t, e.do("GET", "/api/peers", tok, tsIP, nil))
	if ps.Total != 0 {
		t.Fatalf("unbound device leaked: %+v", ps)
	}
}

func TestAddressBookLegacyMode(t *testing.T) {
	e := newEnv(t)
	e.mustUser("alice", "pw", "")
	e.mustUser("bob", "pw", "")
	ta := e.login("alice", "pw", "1", "ua", tsIP)
	tb := e.login("bob", "pw", "2", "ub", tsIP)

	// 404 -> client switches to legacy mode (ab_model.dart:271-275).
	if rec := e.do("POST", "/api/ab/personal", ta, tsIP, nil); rec.Code != 404 {
		t.Fatalf("ab/personal: %d", rec.Code)
	}
	// Empty book: "data" is a JSON string that decodes to an object.
	ab := decode[legacyAbResponse](t, e.do("GET", "/api/ab", ta, tsIP, nil))
	var inner map[string]any
	if err := json.Unmarshal([]byte(ab.Data), &inner); err != nil {
		t.Fatalf("empty ab data %q: %v", ab.Data, err)
	}

	book := `{"tags":["home"],"peers":[{"id":"222","alias":"bob pc","tags":["home"]}],"tag_colors":"{\"home\":4278190335}"}`
	rec := e.do("POST", "/api/ab", ta, tsIP, map[string]string{"data": book})
	if rec.Code != 200 || rec.Body.Len() != 0 {
		t.Fatalf("ab push: %d %q", rec.Code, rec.Body)
	}
	ab = decode[legacyAbResponse](t, e.do("GET", "/api/ab", ta, tsIP, nil))
	if ab.Data != book {
		t.Fatalf("ab roundtrip %q", ab.Data)
	}
	// Sciter UI path.
	ab = decode[legacyAbResponse](t, e.do("POST", "/api/ab/get", ta, tsIP, nil))
	if ab.Data != book {
		t.Fatalf("ab/get %q", ab.Data)
	}
	// Address books are per user.
	ab = decode[legacyAbResponse](t, e.do("GET", "/api/ab", tb, tsIP, nil))
	if ab.Data == book {
		t.Fatalf("bob sees alice's address book")
	}
	if rec := e.do("POST", "/api/ab", ta, tsIP, map[string]string{"data": "not json"}); rec.Code != 400 {
		t.Fatalf("invalid ab: %d", rec.Code)
	}
}

func TestHarmlessEndpoints(t *testing.T) {
	e := newEnv(t)
	rec := e.do("GET", "/api/login-options", "", tsIP, nil)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("login-options: %d %q", rec.Code, rec.Body)
	}
	for _, typ := range []string{"conn", "file", "alarm"} {
		rec := e.do("POST", "/api/audit/"+typ, "", tsIP, `{"id":"1","action":"new"}`)
		if rec.Code != 200 {
			t.Errorf("audit/%s: %d", typ, rec.Code)
		}
		if body := strings.TrimSpace(rec.Body.String()); body != "" {
			t.Errorf("audit/%s: body %q, the client retries anything but an empty body", typ, body)
		}
	}
	rec = e.do("PUT", "/api/audit/conn", "", tsIP, `{"note":"x"}`)
	if rec.Code != 200 {
		t.Errorf("audit PUT: %d", rec.Code)
	}
	rec = e.do("POST", "/api/record?type=part&file=a.webm&offset=0&length=3", "", tsIP, "abc")
	if rec.Code != 200 {
		t.Errorf("record: %d", rec.Code)
	}
	if _, hasErr := decode[map[string]any](t, rec)["error"]; hasErr {
		t.Errorf("record returned error")
	}
	if rec := e.do("GET", "/api/unknown", "", tsIP, nil); rec.Code != 404 {
		t.Errorf("unknown: %d", rec.Code)
	}
}

func TestHeartbeatPushesServerSettingsToLoggedInDevices(t *testing.T) {
	e := newEnv(t)
	keyFile := filepath.Join(t.TempDir(), "id_ed25519.pub")
	if err := os.WriteFile(keyFile, []byte("SERVERKEY=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.srv.cfg = ServerSettings{Host: "thuthi-scale.example.ts.net", KeyFile: keyFile}
	if _, err := e.st.AddGroup(t.Context(), "home"); err != nil {
		t.Fatal(err)
	}
	e.mustUser("alice", "pw-alice", "home")

	type strategy struct {
		ConfigOptions map[string]string `json:"config_options"`
	}
	type reply struct {
		ModifiedAt int64     `json:"modified_at"`
		Strategy   *strategy `json:"strategy"`
	}
	beat := func(uuid string, modifiedAt int64) reply {
		return decode[reply](t, e.do("POST", "/api/heartbeat", "", tsIP, map[string]any{
			"id": "111", "uuid": uuid, "modified_at": modifiedAt,
		}))
	}

	if r := beat("dev-1", 0); r.Strategy != nil {
		t.Fatalf("device not logged in got a strategy: %+v", r)
	}

	e.login("alice", "pw-alice", "111", "dev-1", tsIP)
	r := beat("dev-1", 0)
	if r.Strategy == nil {
		t.Fatal("logged-in device got no strategy")
	}
	want := map[string]string{
		"custom-rendezvous-server": "thuthi-scale.example.ts.net",
		"relay-server":             "thuthi-scale.example.ts.net",
		"api-server":               "http://thuthi-scale.example.ts.net:21114",
		"key":                      "SERVERKEY=",
	}
	for k, v := range want {
		if r.Strategy.ConfigOptions[k] != v {
			t.Errorf("%s = %q, want %q", k, r.Strategy.ConfigOptions[k], v)
		}
	}

	if again := beat("dev-1", r.ModifiedAt); again.Strategy != nil || again.ModifiedAt != r.ModifiedAt {
		t.Fatalf("unchanged settings were resent: %+v", again)
	}

	if err := os.WriteFile(keyFile, []byte("NEWKEY=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if changed := beat("dev-1", r.ModifiedAt); changed.Strategy == nil || changed.Strategy.ConfigOptions["key"] != "NEWKEY=" {
		t.Fatalf("new key was not pushed: %+v", changed)
	}
}
