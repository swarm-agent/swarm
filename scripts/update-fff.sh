#!/usr/bin/env bash
set -euo pipefail

REPO="dmtrKovalenko/fff"
SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
ROOT_DIR="$(CDPATH= cd -- "${SCRIPT_DIR}/.." && pwd)"
MANIFEST="${SCRIPT_DIR}/fff-release-manifest.txt"
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
require_tool mktemp
require_tool install

VERIFY_SCRIPT="${SCRIPT_DIR}/verify-fff-release.sh"
if [ ! -f "$VERIFY_SCRIPT" ]; then
  echo "missing verifier: ${VERIFY_SCRIPT}" >&2
  exit 1
fi

if [ $# -ne 1 ] || [ -z "$1" ]; then
  echo "usage: $0 <reviewed-release-tag>" >&2
  echo "add the tag and reviewed SHA-256 digests to ${MANIFEST} before updating" >&2
  exit 1
fi

TAG="$1"
case "$TAG" in
  *[!A-Za-z0-9._-]* )
    echo "invalid FFF release tag: ${TAG}" >&2
    exit 1
    ;;
esac
bash "$VERIFY_SCRIPT" --require-tag "$MANIFEST" "$TAG"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT
HEADER_URL="https://raw.githubusercontent.com/${REPO}/${TAG}/${RAW_HEADER_PATH}"
ASSET_URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET_NAME}"

HEADER_DSTS=(
  "${ROOT_DIR}/internal/fff/include/fff.h"
  "${ROOT_DIR}/swarmd/internal/fff/include/fff.h"
)
LIB_DSTS=(
  "${ROOT_DIR}/internal/fff/lib/linux-amd64-gnu/libfff_c.so"
  "${ROOT_DIR}/swarmd/internal/fff/lib/linux-amd64-gnu/libfff_c.so"
)

printf 'Updating FFF to reviewed release %s\n' "$TAG"
curl -fsSL "$HEADER_URL" -o "${TMP_DIR}/fff.h"
curl -fsSL "$ASSET_URL" -o "${TMP_DIR}/${ASSET_NAME}"
bash "$VERIFY_SCRIPT" "$MANIFEST" "$TAG" "${TMP_DIR}/fff.h" "${TMP_DIR}/${ASSET_NAME}"

for dst in "${HEADER_DSTS[@]}"; do
  install -m 0644 "${TMP_DIR}/fff.h" "$dst"
  printf 'updated %s\n' "$dst"
done
for dst in "${LIB_DSTS[@]}"; do
  install -m 0755 "${TMP_DIR}/${ASSET_NAME}" "$dst"
  printf 'updated %s\n' "$dst"
done

if cmp -s "${ROOT_DIR}/internal/fff/fff.go" "${ROOT_DIR}/swarmd/internal/fff/fff.go"; then
  echo 'wrapper_sync=ok'
else
  echo 'wrapper_sync=DIFFERS (review internal/fff/fff.go vs swarmd/internal/fff/fff.go)' >&2
fi

echo 'Done. Review all vendored changes and run focused FFF smoke checks before committing.'
