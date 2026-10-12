#!/bin/sh
# hbbs/hbbr run with host networking and listen on every interface.
# Accept their ports only from tailscale0 (and loopback); drop everything else.
# Idempotent. Run as root after `docker compose up`, and at boot (see README).
set -eu
cd "$(dirname "$0")"
. ./.env
H=${HBBS_PORT:-21116}
R=${HBBR_PORT:-21117}
for ipt in iptables ip6tables; do
  $ipt -N RUSTDESK-TAILNET 2>/dev/null || $ipt -F RUSTDESK-TAILNET
  $ipt -A RUSTDESK-TAILNET -i tailscale0 -j RETURN
  $ipt -A RUSTDESK-TAILNET -i lo -j RETURN
  $ipt -A RUSTDESK-TAILNET -j DROP
  for p in $((H - 1)) "$H" $((H + 2)) "$R" $((R + 2)); do
    $ipt -C INPUT -p tcp --dport "$p" -j RUSTDESK-TAILNET 2>/dev/null ||
      $ipt -I INPUT -p tcp --dport "$p" -j RUSTDESK-TAILNET
  done
  $ipt -C INPUT -p udp --dport "$H" -j RUSTDESK-TAILNET 2>/dev/null ||
    $ipt -I INPUT -p udp --dport "$H" -j RUSTDESK-TAILNET
done
echo "RustDesk ports $((H - 1))-$((H + 2)), $R, $((R + 2)) limited to tailscale0"
