package api

import "testing"

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
