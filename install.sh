#!/bin/sh
# michaku installer — downloads a release binary from GitHub Releases,
# verifies its SHA-256 checksum, and installs it into ~/.local/bin.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/nishimoto265/michaku/main/install.sh | sh
#   MICHAKU_VERSION=v0.1.0 sh install.sh        # pin a version
#   MICHAKU_INSTALL_DIR=/opt/bin sh install.sh  # change install location
set -eu

REPO="nishimoto265/michaku"
INSTALL_DIR="${MICHAKU_INSTALL_DIR:-$HOME/.local/bin}"
VERSION="${MICHAKU_VERSION:-${1:-latest}}"

os="$(uname -s)"
case "$os" in
  Linux)  OS="linux" ;;
  Darwin) OS="darwin" ;;
  *) echo "install.sh: unsupported OS: $os — download a release manually from https://github.com/$REPO/releases" >&2; exit 1 ;;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64)  ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "install.sh: unsupported architecture: $arch" >&2; exit 1 ;;
esac

if [ "$VERSION" = "latest" ]; then
  VERSION="$(curl -fsSI "https://github.com/$REPO/releases/latest" \
    | sed -n 's/^[Ll]ocation: .*\/tag\/\([^[:space:]]*\).*/\1/p' | tr -d '\r')"
  if [ -z "$VERSION" ]; then
    echo "install.sh: could not resolve the latest version" >&2
    exit 1
  fi
fi
NUMVER="${VERSION#v}"

ARCHIVE="michaku_${NUMVER}_${OS}_${ARCH}.tar.gz"
BASE="https://github.com/$REPO/releases/download/$VERSION"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Downloading michaku $VERSION ($OS/$ARCH)..."
curl -fsSL "$BASE/$ARCHIVE" -o "$tmp/$ARCHIVE"
curl -fsSL "$BASE/checksums.txt" -o "$tmp/checksums.txt"

echo "Verifying checksum..."
( cd "$tmp" \
  && grep "  ${ARCHIVE}$" checksums.txt > michaku.sha256 \
  && if command -v sha256sum >/dev/null 2>&1; then
       sha256sum -c michaku.sha256
     else
       shasum -a 256 -c michaku.sha256
     fi )

tar -xzf "$tmp/$ARCHIVE" -C "$tmp"
mkdir -p "$INSTALL_DIR"
install -m 0755 "$tmp/michaku" "$INSTALL_DIR/michaku"

echo "Installed: $INSTALL_DIR/michaku ($VERSION)"
case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) echo "Note: $INSTALL_DIR is not on your PATH. Add it, e.g.:  export PATH=\"$INSTALL_DIR:\$PATH\"" ;;
esac
