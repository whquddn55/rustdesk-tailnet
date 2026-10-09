// Command rustdesk-tailnet-api is a small RustDesk API server (login, device
// registry, "Accessible devices" group tab, personal address book) that only
// listens on and answers Tailscale addresses.
//
// Usage:
//
//	rustdesk-tailnet-api serve
//	rustdesk-tailnet-api user add|list|del|passwd|set-group|disable|enable ...
//	rustdesk-tailnet-api group add|list|del ...
//	rustdesk-tailnet-api device list|del ...
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
