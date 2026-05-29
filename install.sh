#!/bin/sh
# lazy-http installer — downloads the latest prebuilt binary from GitHub Releases.
#
#   curl -fsSL https://raw.githubusercontent.com/wingedsheep/lazyhttp/main/install.sh | sh
#
# Override the install location with LAZYHTTP_INSTALL_DIR=/path sh -c "$(curl ...)".
set -eu

REPO="wingedsheep/lazyhttp"
BIN="lazyhttp"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)

case "$arch" in
  x86_64 | amd64) arch="amd64" ;;
  arm64 | aarch64) arch="arm64" ;;
  *) echo "lazy-http: unsupported architecture: $arch" >&2; exit 1 ;;
esac

case "$os" in
  darwin | linux) ;;
  *) echo "lazy-http: unsupported OS: $os (use 'go install' instead)" >&2; exit 1 ;;
esac

asset="${BIN}_${os}_${arch}.tar.gz"
url="https://github.com/${REPO}/releases/latest/download/${asset}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading ${url}"
if ! curl -fsSL "$url" -o "$tmp/$asset"; then
  echo "lazy-http: download failed — is there a published release yet?" >&2
  exit 1
fi
tar -xzf "$tmp/$asset" -C "$tmp"

# Pick an install dir: explicit override, else /usr/local/bin if writable,
# else ~/.local/bin (no sudo needed).
dir="${LAZYHTTP_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ]; then
    dir="/usr/local/bin"
  else
    dir="$HOME/.local/bin"
  fi
fi
mkdir -p "$dir"
cp "$tmp/$BIN" "$dir/$BIN"
chmod 0755 "$dir/$BIN"

echo "Installed ${BIN} to ${dir}/${BIN}"

case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "Note: ${dir} is not on your PATH. Add this to your shell rc:"
     echo "  export PATH=\"\$PATH:${dir}\"" ;;
esac
