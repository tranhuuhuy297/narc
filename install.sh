#!/bin/sh
# narc installer: curl -fsSL https://raw.githubusercontent.com/tranhuuhuy297/narc/main/install.sh | sh
#
# Environment:
#   NARC_VERSION      version to install (default: latest), e.g. v0.1.0
#   NARC_INSTALL_DIR  where to put the binary (default: /usr/local/bin if writable, else ~/.local/bin)
set -eu

REPO="tranhuuhuy297/narc"
VERSION="${NARC_VERSION:-latest}"

say() { printf 'narc-install: %s\n' "$*" >&2; }
die() { say "error: $*"; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "'$1' is required"; }

need uname
need tar
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1"; }
  fetch_to() { curl -fsSL -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO- "$1"; }
  fetch_to() { wget -qO "$2" "$1"; }
else
  die "curl or wget is required"
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux|darwin) ;;
  mingw*|msys*|cygwin*) os=windows ;;
  *) die "unsupported OS: $os" ;;
esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "unsupported architecture: $arch" ;;
esac

if [ "$VERSION" = "latest" ]; then
  VERSION=$(fetch "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n1)
  [ -n "$VERSION" ] || die "could not determine the latest release"
fi
num=${VERSION#v}
ext=tar.gz
[ "$os" = windows ] && ext=zip
asset="narc_${num}_${os}_${arch}.${ext}"
base="https://github.com/$REPO/releases/download/$VERSION"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
say "downloading $asset ($VERSION)"
fetch_to "$base/$asset" "$tmp/$asset" || die "download failed: $base/$asset"
if fetch_to "$base/checksums.txt" "$tmp/checksums.txt" 2>/dev/null; then
  want=$(grep " $asset\$" "$tmp/checksums.txt" | awk '{print $1}')
  if command -v sha256sum >/dev/null 2>&1; then got=$(sha256sum "$tmp/$asset" | awk '{print $1}')
  else got=$(shasum -a 256 "$tmp/$asset" | awk '{print $1}'); fi
  [ -n "$want" ] && [ "$want" = "$got" ] || die "checksum mismatch for $asset"
fi

if [ "$ext" = zip ]; then (cd "$tmp" && unzip -q "$asset"); else tar -xzf "$tmp/$asset" -C "$tmp"; fi

dir="${NARC_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ]; then dir=/usr/local/bin; else dir="$HOME/.local/bin"; fi
fi
mkdir -p "$dir"
bin=narc
[ "$os" = windows ] && bin=narc.exe
mv "$tmp/$bin" "$dir/$bin"
chmod +x "$dir/$bin"
say "installed $dir/$bin"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) say "add $dir to your PATH" ;;
esac
