package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"rustdesk-tailnet-api/internal/store"
)

var storeUserFixture = store.User{Name: "alice", Status: 1}

// Contract test: extract the JSON keys the RustDesk Flutter client reads from
// its own source and check our responses carry every one of them (minus keys
// that are documented as optional). Point RUSTDESK_SRC at a rustdesk checkout;
// defaults to /tmp/rd-src/rustdesk and skips if missing.

func rustdeskSrc(t *testing.T) string {
	dir := os.Getenv("RUSTDESK_SRC")
	if dir == "" {
		dir = "/tmp/rd-src/rustdesk"
	}
	if _, err := os.Stat(filepath.Join(dir, "flutter/lib/common/hbbs/hbbs.dart")); err != nil {
		t.Skipf("rustdesk source not found at %s (set RUSTDESK_SRC)", dir)
	}
	return dir
}

func section(t *testing.T, src, from, to string) string {
	t.Helper()
	i := strings.Index(src, from)
	if i < 0 {
		t.Fatalf("marker %q not found in client source", from)
	}
	rest := src[i:]
	j := strings.Index(rest, to)
	if j < 0 {
		t.Fatalf("marker %q not found after %q", to, from)
	}
	return rest[:j]
}

func keys(re *regexp.Regexp, s string) []string {
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		seen[m[1]] = true
	}
	var out []string
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func jsonKeys(t *testing.T, v any) map[string]bool {
	t.Helper()
	b, _ := json.Marshal(v)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

func checkKeys(t *testing.T, what string, clientKeys []string, ours map[string]bool, optional ...string) {
	t.Helper()
	opt := map[string]bool{}
	for _, o := range optional {
		opt[o] = true
	}
	if len(clientKeys) == 0 {
		t.Fatalf("%s: no keys extracted from client source", what)
	}
	for _, k := range clientKeys {
		if !ours[k] && !opt[k] {
			t.Errorf("%s: client reads %q but our response lacks it", what, k)
		}
	}
	t.Logf("%s: client keys %v", what, clientKeys)
}

func TestContractWithClientSource(t *testing.T) {
	dir := rustdeskSrc(t)
	b, err := os.ReadFile(filepath.Join(dir, "flutter/lib/common/hbbs/hbbs.dart"))
	if err != nil {
		t.Fatal(err)
	}
	hbbs := string(b)
	jsonKey := regexp.MustCompile(`json\['(\w+)'\]`)
	infoKey := regexp.MustCompile(`p\.info\['(\w+)'\]`)

	// UserPayload.fromJson -> /api/currentUser, /api/users data[], login "user".
	user := toUserPayload(&storeUserFixture)
	checkKeys(t, "UserPayload", keys(jsonKey, section(t, hbbs, "UserPayload.fromJson", "Map<String, dynamic> toJson")),
		jsonKeys(t, user), "verifier" /* web-only, nullable */)

	// PeerPayload.fromJson + toPeer info[] -> /api/peers data[].
	peerSrc := section(t, hbbs, "PeerPayload.fromJson", "static String? _platform")
	peer := peerPayload{ID: "1"}
	checkKeys(t, "PeerPayload", keys(jsonKey, peerSrc), jsonKeys(t, peer))
	checkKeys(t, "PeerPayload.info", keys(infoKey, peerSrc), jsonKeys(t, peer.Info))

	// DeviceGroupPayload.fromJson -> /api/device-group/accessible data[].
	checkKeys(t, "DeviceGroupPayload", keys(jsonKey, section(t, hbbs, "DeviceGroupPayload.fromJson", "toGroupCacheJson")),
		jsonKeys(t, deviceGroupPayload{}))

	// LoginResponse.fromJson -> /api/login.
	checkKeys(t, "LoginResponse", keys(jsonKey, section(t, hbbs, "LoginResponse.fromJson", "class RequestException")),
		jsonKeys(t, loginResponse{}), "tfa_type", "secret" /* 2FA only, nullable */)

	// Paged envelope used by group_model.dart for users/peers/device groups.
	gm, err := os.ReadFile(filepath.Join(dir, "flutter/lib/models/group_model.dart"))
	if err != nil {
		t.Fatal(err)
	}
	envKey := regexp.MustCompile(`json\.containsKey\('(\w+)'\)`)
	checkKeys(t, "group paged envelope", keys(envKey, string(gm)), jsonKeys(t, paged[peerPayload]{}), "error")

	// Legacy address book (LegacyAb.pullAbImpl).
	ab, err := os.ReadFile(filepath.Join(dir, "flutter/lib/models/ab_model.dart"))
	if err != nil {
		t.Fatal(err)
	}
	legacy := section(t, string(ab), "class LegacyAb", "Future<bool> pushAb")
	checkKeys(t, "LegacyAb pull", keys(jsonKey, legacy), jsonKeys(t, legacyAbResponse{}), "error")

	// Rust side: account.rs UserPayload has two fields without #[serde(default)]:
	// name and info. Both must be present or serde fails.
	acc, err := os.ReadFile(filepath.Join(dir, "src/hbbs_http/account.rs"))
	if err != nil {
		t.Fatal(err)
	}
	rs := section(t, string(acc), "pub struct UserPayload", "}")
	required := requiredRustFields(rs)
	checkKeys(t, "Rust UserPayload (non-default fields)", required, jsonKeys(t, user))

	// sync.rs heartbeat reply keys and sysinfo reply strings.
	sy, err := os.ReadFile(filepath.Join(dir, "src/hbbs_http/sync.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`rsp.remove("sysinfo")`, `x == "SYSINFO_UPDATED"`, `x == "ID_NOT_FOUND"`, `url.replace("heartbeat", "sysinfo")`} {
		if !strings.Contains(string(sy), s) {
			t.Errorf("sync.rs no longer contains %s; re-check heartbeat/sysinfo protocol", s)
		}
	}
}

// requiredRustFields returns struct fields not preceded by #[serde(default)].
func requiredRustFields(body string) []string {
	var out []string
	lines := strings.Split(body, "\n")
	field := regexp.MustCompile(`^\s*pub (?:r#)?(\w+):`)
	for i, l := range lines {
		m := field.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if i > 0 && strings.Contains(lines[i-1], "serde(default") {
			continue
		}
		out = append(out, m[1])
	}
	return out
}
