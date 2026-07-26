#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

printf 'reviewed header\n' >"${TMP_DIR}/fff.h"
printf 'reviewed library\n' >"${TMP_DIR}/libfff_c.so"
header_sha256="$(sha256sum "${TMP_DIR}/fff.h" | awk '{print $1}')"
library_sha256="$(sha256sum "${TMP_DIR}/libfff_c.so" | awk '{print $1}')"
printf 'v-test %s %s\n' "$header_sha256" "$library_sha256" >"${TMP_DIR}/manifest"

bash "${SCRIPT_DIR}/verify-fff-release.sh" \
  "${TMP_DIR}/manifest" v-test "${TMP_DIR}/fff.h" "${TMP_DIR}/libfff_c.so" >/dev/null

printf 'tampered\n' >>"${TMP_DIR}/fff.h"
if bash "${SCRIPT_DIR}/verify-fff-release.sh" \
  "${TMP_DIR}/manifest" v-test "${TMP_DIR}/fff.h" "${TMP_DIR}/libfff_c.so" >/dev/null 2>&1; then
  echo 'tampered FFF header was accepted' >&2
  exit 1
fi

if bash "${SCRIPT_DIR}/verify-fff-release.sh" --require-tag \
  "${TMP_DIR}/manifest" v-drift >/dev/null 2>&1; then
  echo 'unreviewed FFF version was accepted before download' >&2
  exit 1
fi

echo 'FFF release verification regression checks passed'
