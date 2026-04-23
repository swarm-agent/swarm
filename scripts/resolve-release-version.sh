#!/bin/sh
set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
ROOT_DIR="$(CDPATH= cd -- "${SCRIPT_DIR}/.." && pwd)"
INITIAL_STABLE_VERSION="${INITIAL_STABLE_VERSION:-v0.1.0}"

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

is_stable_tag() {
  printf '%s\n' "${1:-}" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'
}

next_patch_tag() {
  version="${1#v}"
  major="${version%%.*}"
  rest="${version#*.}"
  minor="${rest%%.*}"
  patch="${rest##*.}"
  next_patch=$((patch + 1))
  printf 'v%s.%s.%s\n' "${major}" "${minor}" "${next_patch}"
}

require_cmd git
require_cmd grep
require_cmd head

if ! is_stable_tag "${INITIAL_STABLE_VERSION}"; then
  echo "invalid INITIAL_STABLE_VERSION: ${INITIAL_STABLE_VERSION} (expected vX.Y.Z)" >&2
  exit 1
fi

exact_tag="$(git -C "${ROOT_DIR}" describe --tags --exact-match 2>/dev/null || true)"
if is_stable_tag "${exact_tag}"; then
  printf '%s\n' "${exact_tag}"
  exit 0
fi

latest_tag="$({
  git -C "${ROOT_DIR}" tag --list 'v*' --sort=-v:refname |
    grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' |
    head -n 1
} || true)"
if [ -z "${latest_tag}" ]; then
  printf '%s\n' "${INITIAL_STABLE_VERSION}"
  exit 0
fi

next_patch_tag "${latest_tag}"
