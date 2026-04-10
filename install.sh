#!/bin/sh
set -e

# Palbase CLI installer
# Usage: curl -fsSL https://raw.githubusercontent.com/seklabsnet/palbase-cli/main/install.sh | sh

REPO="seklabsnet/palbase-cli"
INSTALL_DIR="/usr/local/bin"

OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH"; exit 1 ;;
esac

case "$OS" in
  darwin|linux) ;;
  *) echo "Unsupported OS: $OS"; exit 1 ;;
esac

VERSION=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')

if [ -z "$VERSION" ]; then
  echo "Failed to get latest version"
  exit 1
fi

echo "Installing palbase ${VERSION} (${OS}/${ARCH})..."

URL="https://github.com/${REPO}/releases/download/${VERSION}/palbase_${OS}_${ARCH}.tar.gz"
TMP_DIR=$(mktemp -d)
trap 'rm -rf "$TMP_DIR"' EXIT

curl -fsSL "$URL" -o "${TMP_DIR}/palbase.tar.gz"
tar -xzf "${TMP_DIR}/palbase.tar.gz" -C "$TMP_DIR"

BINARY=$(find "$TMP_DIR" -name palbase -type f | head -1)
if [ -z "$BINARY" ]; then
  echo "Failed to find palbase binary in archive"
  exit 1
fi

if [ -w "$INSTALL_DIR" ]; then
  mv "$BINARY" "${INSTALL_DIR}/palbase"
else
  sudo mv "$BINARY" "${INSTALL_DIR}/palbase"
fi

chmod +x "${INSTALL_DIR}/palbase"

echo "✓ palbase ${VERSION} installed to ${INSTALL_DIR}/palbase"
echo ""
echo "Get started:"
echo "  palbase login"
echo "  palbase backend init my-app"
