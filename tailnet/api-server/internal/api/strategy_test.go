package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServerSettingsPorts(t *testing.T) {
	def := ServerSettings{Host: "srv.ts.net"}.options()
	if def["custom-rendezvous-server"] != "srv.ts.net" || def["relay-server"] != "srv.ts.net" ||
		def["api-server"] != "http://srv.ts.net:21114" {
		t.Fatalf("default ports: %v", def)
	}
	alt := ServerSettings{Host: "srv.ts.net", APIPort: 22114, IDPort: 22116, RelayPort: 22117}.options()
	if alt["custom-rendezvous-server"] != "srv.ts.net:22116" || alt["relay-server"] != "srv.ts.net:22117" ||
		alt["api-server"] != "http://srv.ts.net:22114" {
		t.Fatalf("test ports: %v", alt)
	}
}

func TestLatestBuildFromReleases(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"tag_name":"tailnet-b12","html_url":"https://x/b12","draft":true},
			{"tag_name":"tailnet-b11","html_url":"https://x/b11"},
			{"tag_name":"tailnet-latest","html_url":"https://x/old"},
			{"tag_name":"tailnet-b9","html_url":"https://x/b9"}]`))
	}))
	defer gh.Close()
	info, err := newReleases(gh.URL).latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Build != 11 || info.URL != "https://x/b11" {
		t.Fatalf("latest: %+v", info)
	}
	if info, _ := newReleases("").latest(context.Background()); info.Build != 0 {
		t.Fatalf("disabled check: %+v", info)
	}
}
