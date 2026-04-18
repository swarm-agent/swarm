#!/usr/bin/env bash
set -euo pipefail

REPO="dmtrKovalenko/fff.nvim"
SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
ROOT_DIR="$(CDPATH= cd -- "${SCRIPT_DIR}/.." && pwd)"
ASSET_NAME="c-lib-x86_64-unknown-linux-gnu.so"
RAW_HEADER_PATH="crates/fff-c/include/fff.h"

require_tool() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "missing required tool: $1" >&2
    exit 1
  fi
}

require_tool curl
require_tool sha256sum
require_tool awk
require_tool sed
require_tool mktemp

resolve_tag() {
  if [ $# -gt 0 ] && [ -n "${1:-}" ]; then
    printf '%s\n' "$1"
    return 0
  fi

  local api tag
  api="https://api.github.com/repos/${REPO}/releases/latest"
  tag="$({ curl -fsSL "$api" | sed -n 's/^[[:space:]]*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1; } || true)"
  if [ -z "$tag" ]; then
    echo "failed to resolve latest FFF release tag from ${api}" >&2
    exit 1
  fi
  printf '%s\n' "$tag"
}

TAG="$(resolve_tag "${1:-}")"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

HEADER_URL="https://raw.githubusercontent.com/${REPO}/${TAG}/${RAW_HEADER_PATH}"
ASSET_URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET_NAME}"
CHECKSUM_URL="${ASSET_URL}.sha256"

HEADER_DSTS=(
  "${ROOT_DIR}/internal/fff/include/fff.h"
  "${ROOT_DIR}/swarmd/internal/fff/include/fff.h"
)
LIB_DSTS=(
  "${ROOT_DIR}/internal/fff/lib/linux-amd64-gnu/libfff_c.so"
  "${ROOT_DIR}/swarmd/internal/fff/lib/linux-amd64-gnu/libfff_c.so"
)

printf 'Updating FFF to %s\n' "$TAG"
printf 'Header: %s\n' "$HEADER_URL"
printf 'Asset:  %s\n' "$ASSET_URL"

curl -fsSL "$HEADER_URL" -o "${TMP_DIR}/fff.h"
curl -fsSL "$ASSET_URL" -o "${TMP_DIR}/${ASSET_NAME}"
curl -fsSL "$CHECKSUM_URL" -o "${TMP_DIR}/${ASSET_NAME}.sha256"

(
  cd "$TMP_DIR"
  sha256sum -c "${ASSET_NAME}.sha256"
)

for dst in "${HEADER_DSTS[@]}"; do
  install -m 0644 "${TMP_DIR}/fff.h" "$dst"
  printf 'updated %s\n' "$dst"
done

for dst in "${LIB_DSTS[@]}"; do
  install -m 0755 "${TMP_DIR}/${ASSET_NAME}" "$dst"
  printf 'updated %s\n' "$dst"
done

printf 'header_sha1=%s\n' "$(sha1sum "${TMP_DIR}/fff.h" | awk '{print $1}')"
printf 'lib_sha1=%s\n' "$(sha1sum "${TMP_DIR}/${ASSET_NAME}" | awk '{print $1}')"

if cmp -s "${ROOT_DIR}/internal/fff/fff.go" "${ROOT_DIR}/swarmd/internal/fff/fff.go"; then
  echo 'wrapper_sync=ok'
else
  echo 'wrapper_sync=DIFFERS (review internal/fff/fff.go vs swarmd/internal/fff/fff.go)' >&2
fi

echo 'Done. Next steps:'
echo '  1. Review wrapper/API changes if upstream header changed.'
echo '  2. Run gofmt on touched Go files if needed.'
echo '  3. Run manual smoke checks with cmd/fffprobe in the swarm repo.'
