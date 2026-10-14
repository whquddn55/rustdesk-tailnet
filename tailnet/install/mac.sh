#!/bin/sh
# Install or update rustdesk-tailnet on macOS (Apple Silicon).
#   curl -fsSL https://raw.githubusercontent.com/whquddn55/rustdesk-tailnet/tailnet/tailnet/install/mac.sh | sh
# A file downloaded with curl carries no quarantine flag, so Gatekeeper does not
# reject the self-signed app as "damaged".
set -eu
REPO=whquddn55/rustdesk-tailnet
API=https://api.github.com/repos/$REPO/releases
TAG=${1:-$(curl -fsSL "$API?per_page=30" | grep -o '"tag_name": *"tailnet-b[0-9]*"' | head -1 | grep -o 'tailnet-b[0-9]*')}
[ -n "$TAG" ] || { echo "no tailnet-b release found" >&2; exit 1; }
URL=$(curl -fsSL "$API/tags/$TAG" | grep -o '"browser_download_url": *"[^"]*\.dmg"' | head -1 | grep -o 'https://[^"]*')
echo "rustdesk-tailnet $TAG"
tmp=$(mktemp -d)
trap 'hdiutil detach -quiet "$tmp/mnt" 2>/dev/null || true; rm -rf "$tmp"' EXIT
curl -fL --progress-bar -o "$tmp/rustdesk.dmg" "$URL"
mkdir "$tmp/mnt"
hdiutil attach -quiet -nobrowse -readonly -mountpoint "$tmp/mnt" "$tmp/rustdesk.dmg"
osascript -e 'quit app "RustDesk"' 2>/dev/null || true
sleep 1
APP=/Applications/RustDesk.app
SUDO=
[ -w /Applications ] && { [ ! -e "$APP" ] || [ -w "$APP" ]; } || SUDO=sudo
$SUDO rm -rf "$APP"
$SUDO ditto "$tmp/mnt/RustDesk.app" "$APP"
$SUDO xattr -dr com.apple.quarantine "$APP" 2>/dev/null || true
echo "installed $APP ($TAG). Open RustDesk to finish (service, permissions on first install)."
