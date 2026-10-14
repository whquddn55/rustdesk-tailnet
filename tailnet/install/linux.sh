#!/bin/sh
# Install or update rustdesk-tailnet on Linux x86_64 (deb or rpm).
#   curl -fsSL https://raw.githubusercontent.com/whquddn55/rustdesk-tailnet/tailnet/tailnet/install/linux.sh | sh
set -eu
REPO=whquddn55/rustdesk-tailnet
API=https://api.github.com/repos/$REPO/releases
TAG=${1:-$(curl -fsSL "$API?per_page=30" | grep -o '"tag_name": *"tailnet-b[0-9]*"' | head -1 | grep -o 'tailnet-b[0-9]*')}
[ -n "$TAG" ] || { echo "no tailnet-b release found" >&2; exit 1; }
if command -v apt-get >/dev/null 2>&1; then EXT='x86_64\.deb'; elif command -v dnf >/dev/null 2>&1; then EXT='x86_64\.rpm'; else echo "need apt or dnf" >&2; exit 1; fi
URL=$(curl -fsSL "$API/tags/$TAG" | grep -o "\"browser_download_url\": *\"[^\"]*$EXT\"" | grep -v suse | head -1 | grep -o 'https://[^"]*')
echo "rustdesk-tailnet $TAG"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
FILE="$tmp/$(basename "$URL")"
curl -fL --progress-bar -o "$FILE" "$URL"
chmod 644 "$FILE"
if command -v apt-get >/dev/null 2>&1; then sudo apt-get install -y "$FILE"; else sudo dnf install -y "$FILE"; fi
sudo systemctl enable --now rustdesk
echo "installed rustdesk-tailnet $TAG"
