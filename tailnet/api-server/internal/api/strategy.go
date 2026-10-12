package api

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
)

// ServerSettings are pushed to every logged-in device through the heartbeat
// "strategy" reply, which the client stores as its own options
// (sync.rs:268-272 handle_config_options). Devices of the same account share
// one ID server, relay and key without per-device setup.
type ServerSettings struct {
	// Host is the ID / relay / API server host, e.g. a MagicDNS name.
	Host string
	// KeyFile is hbbs' public key (id_ed25519.pub); read on each heartbeat so
	// a regenerated key reaches the devices without a restart.
	KeyFile string
	// Ports as seen by the devices; zero means the RustDesk default. Non-default
	// ports let a test instance run beside an existing server on the same host.
	APIPort, IDPort, RelayPort int
}

func hostPort(host string, port, def int) string {
	if port == 0 || port == def {
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func (c ServerSettings) options() map[string]string {
	if c.Host == "" {
		return nil
	}
	opts := map[string]string{
		"custom-rendezvous-server": hostPort(c.Host, c.IDPort, 21116),
		"relay-server":             hostPort(c.Host, c.RelayPort, 21117),
		"api-server":               "http://" + net.JoinHostPort(c.Host, strconv.Itoa(cmp.Or(c.APIPort, 21114))),
	}
	if c.KeyFile != "" {
		if b, err := os.ReadFile(c.KeyFile); err == nil {
			if key := strings.TrimSpace(string(b)); key != "" {
				opts["key"] = key
			}
		}
	}
	return opts
}

// version is the strategy's "modified_at": the client echoes it back and we
// only resend the strategy when it differs.
func version(opts map[string]string) int64 {
	keys := make([]string, 0, len(opts))
	for k := range opts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k + "=" + opts[k] + "\n"))
	}
	return int64(binary.BigEndian.Uint64(h.Sum(nil)[:8]) >> 1)
}
