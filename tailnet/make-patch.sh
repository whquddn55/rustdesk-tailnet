#!/usr/bin/env bash
# Regenerate tailnet/hbb_common.patch from the working tree of the submodule.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
sub="$root/libs/hbb_common"
git -C "$sub" add -N src/tailnet.rs
git -C "$sub" diff > "$root/tailnet/hbb_common.patch"
git -C "$sub" reset -q src/tailnet.rs
echo "tailnet: wrote tailnet/hbb_common.patch ($(grep -c '^diff --git' "$root/tailnet/hbb_common.patch") files)"
