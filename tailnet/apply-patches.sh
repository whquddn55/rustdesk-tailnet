#!/usr/bin/env bash
# Apply the tailnet-only patch to the hbb_common submodule (idempotent).
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
patch="$root/tailnet/hbb_common.patch"
sub="$root/libs/hbb_common"
if git -C "$sub" apply --reverse --check "$patch" 2>/dev/null; then
  echo "tailnet: hbb_common patch already applied"
else
  git -C "$sub" apply "$patch"
  echo "tailnet: hbb_common patch applied"
fi

# CI build number -> Flutter build number (Android versionCode, macOS CFBundleVersion,
# Windows file version). Above upstream's own build number so updates install in place.
if [ -n "${TAILNET_BUILD_NUMBER:-}" ]; then
  build=$((1000 + TAILNET_BUILD_NUMBER))
  sed -i.bak -E "s/^version: ([0-9.]+)\+[0-9]+/version: \1+$build/" "$root/flutter/pubspec.yaml"
  rm -f "$root/flutter/pubspec.yaml.bak"
  echo "tailnet: build number $build ($(grep '^version:' "$root/flutter/pubspec.yaml"))"
fi
