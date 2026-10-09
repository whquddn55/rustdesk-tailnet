// Package netguard enforces that the API is only reachable over Tailscale.
//
// Two layers:
//   - ValidateListenAddr refuses to start unless LISTEN_ADDR is a literal IP
//     inside the Tailscale ranges (CGNAT 100.64.0.0/10 or the Tailscale ULA
//     fd7a:115c:a1e0::/48). There is deliberately no 0.0.0.0 default.
//   - Middleware rejects any request whose TCP peer address (r.RemoteAddr)
//     is outside those ranges with 403. Forwarding headers such as
//     X-Forwarded-For are never consulted.
//
// Loopback can be allowed for local testing only, via an explicit opt-in.
package netguard

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
)

var (
	// TailscaleV4 is the CGNAT range Tailscale assigns IPv4 addresses from.
	TailscaleV4 = netip.MustParsePrefix("100.64.0.0/10")
	// TailscaleV6 is the ULA range Tailscale assigns IPv6 addresses from.
	TailscaleV6 = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// Policy decides which peer addresses are acceptable.
type Policy struct {
	// AllowLoopback additionally accepts 127.0.0.0/8 and ::1. Testing only.
	AllowLoopback bool
}

// IsTailscale reports whether ip is inside a Tailscale range.
func IsTailscale(ip netip.Addr) bool {
	ip = ip.Unmap()
	return TailscaleV4.Contains(ip) || TailscaleV6.Contains(ip)
}

// Allowed reports whether ip may talk to the API under this policy.
func (p Policy) Allowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	if IsTailscale(ip) {
		return true
	}
	return p.AllowLoopback && ip.IsLoopback()
}

// ValidateListenAddr checks that addr is "IP:port" with a literal IP that is
// allowed by the policy. Hostnames, empty hosts and wildcard addresses are
// rejected so the server can never bind to every interface by accident.
func (p Policy) ValidateListenAddr(addr string) error {
	if addr == "" {
		return fmt.Errorf("LISTEN_ADDR is not set; set it to this machine's Tailscale IP and port, e.g. 100.101.102.103:21114")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("LISTEN_ADDR %q is not host:port: %v", addr, err)
	}
	if port == "" {
		return fmt.Errorf("LISTEN_ADDR %q has no port", addr)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("LISTEN_ADDR host %q must be a literal Tailscale IP (hostnames are not accepted)", host)
	}
	if ip.IsUnspecified() {
		return fmt.Errorf("LISTEN_ADDR %q would listen on all interfaces; use the Tailscale IP instead", addr)
	}
	if !p.Allowed(ip) {
		return fmt.Errorf("LISTEN_ADDR %q is outside the Tailscale ranges %s / %s", addr, TailscaleV4, TailscaleV6)
	}
	return nil
}

// RemoteIP parses r.RemoteAddr (the TCP peer, set by net/http).
func RemoteIP(r *http.Request) (netip.Addr, bool) {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	return ap.Addr().Unmap(), true
}

// Middleware returns 403 for any request not coming from an allowed address.
func (p Policy) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, ok := RemoteIP(r)
		if !ok || !p.Allowed(ip) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "forbidden: Tailscale access only"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
