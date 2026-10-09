#!/usr/bin/env bash
# Apply the tailnet-only patch to the hbb_common submodule (idempotent).
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
patch="$root/tailnet/hbb_common.patch"
sub="$root/libs/hbb_common"
if git -C "$sub" apply --reverse --check "$patch" 2>/dev/null; then
  echo "tailnet: hbb_common patch already applied"
  exit 0
fi
git -C "$sub" apply "$patch"
echo "tailnet: hbb_common patch applied"
