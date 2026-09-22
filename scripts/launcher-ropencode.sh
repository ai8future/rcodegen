#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

case "$ARCH" in
  x86_64)  ARCH="amd64" ;;
  aarch64) ARCH="arm64" ;;
esac

BINARY="${SCRIPT_DIR}/ropencode-${OS}-${ARCH}"

if [ ! -f "$BINARY" ]; then
  echo "error: no binary for ${OS}/${ARCH} (expected ${BINARY})" >&2
  exit 1
fi

exec "$BINARY" "$@"
