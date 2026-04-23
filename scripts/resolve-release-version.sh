#!/bin/sh
set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
ROOT_DIR="$(CDPATH= cd -- "${SCRIPT_DIR}/.." && pwd)"

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

require_cmd git
require_cmd grep

tag="$(git -C "${ROOT_DIR}" describe --tags --exact-match 2>/dev/null || true)"
if [ -z "${tag}" ]; then
  echo "release version resolution failed: HEAD must have an exact stable semver tag vX.Y.Z" >&2
  exit 1
fi
if ! printf '%s\n' "${tag}" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "release version resolution failed: exact tag is not a stable semver tag vX.Y.Z: ${tag}" >&2
  exit 1
fi

printf '%s\n' "${tag}"
