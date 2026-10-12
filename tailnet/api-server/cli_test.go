package main

import (
	"strings"
	"testing"
)

// The server must refuse to start unless LISTEN_ADDR is a Tailscale address.
func TestServerConfigRefusesNonTailscale(t *testing.T) {
	bad := []string{"", "0.0.0.0:21114", ":21114", "192.168.0.2:21114", "127.0.0.1:21114", "example.com:21114"}
	for _, addr := range bad {
		t.Setenv("LISTEN_ADDR", addr)
		t.Setenv("ALLOW_LOOPBACK", "")
		if _, _, _, err := serverConfig(); err == nil {
			t.Errorf("LISTEN_ADDR=%q accepted, want refusal", addr)
		}
		// serve() must fail the same way, before opening a socket or DB.
		t.Setenv("DB_PATH", t.TempDir()+"/x.db")
		if err := serve(nil); err == nil || !strings.Contains(err.Error(), "refusing to start") {
			t.Errorf("serve with LISTEN_ADDR=%q: err=%v", addr, err)
		}
	}

	t.Setenv("LISTEN_ADDR", "100.88.1.2:21114")
	if _, _, ttl, err := serverConfig(); err != nil || ttl <= 0 {
		t.Errorf("tailscale addr refused: %v", err)
	}

	t.Setenv("LISTEN_ADDR", "127.0.0.1:21114")
	t.Setenv("ALLOW_LOOPBACK", "1")
	if _, p, _, err := serverConfig(); err != nil || !p.AllowLoopback {
		t.Errorf("loopback with ALLOW_LOOPBACK=1 refused: %v", err)
	}

	t.Setenv("LISTEN_ADDR", "100.88.1.2:21114")
	t.Setenv("ALLOW_LOOPBACK", "")
	t.Setenv("TOKEN_TTL", "nonsense")
	if _, _, _, err := serverConfig(); err == nil {
		t.Errorf("bad TOKEN_TTL accepted")
	}
}
