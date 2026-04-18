#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

DISALLOWED_ABS_PATH_PATTERN='(/home/|/Users/|/tmp/|/var/tmp/|/etc/|/opt/|/root/)'
has_failures=0

echo "[path-check] scanning non-test runtime code and scripts for hardcoded absolute paths"
runtime_hits="$(
  rg -n \
    --glob '!.git/**' \
    --glob '*.go' \
    --glob '*.sh' \
    --glob 'bin/swarm' \
    --glob 'bin/swarmdev' \
    --glob 'bin/swarmsetup' \
    --glob '!**/*_test.go' \
    --glob '!scripts/check-hardcoded-paths.sh' \
    --glob '!scripts/check-precommit.sh' \
    "${DISALLOWED_ABS_PATH_PATTERN}" \
    "${ROOT_DIR}" || true
)"
if [[ -n "${runtime_hits}" ]]; then
  has_failures=1
  echo "[path-check] FAIL: disallowed absolute path literals found:"
  echo "${runtime_hits}"
fi

echo "[path-check] scanning docs/scripts for legacy repo path tokens"
legacy_hits="$(
  rg -n \
    --glob '!.git/**' \
    --glob '*.md' \
    --glob '*.sh' \
    --glob 'bin/swarm' \
    --glob 'bin/swarmdev' \
    --glob 'bin/swarmsetup' \
    --glob '!scripts/check-hardcoded-paths.sh' \
    --glob '!scripts/check-precommit.sh' \
    'swarm-refactor' \
    "${ROOT_DIR}" || true
)"
if [[ -n "${legacy_hits}" ]]; then
  has_failures=1
  echo "[path-check] FAIL: legacy repo path token 'swarm-refactor' found:"
  echo "${legacy_hits}"
fi

echo "[path-check] scanning docs for machine-specific home paths"
home_hits="$(
  rg -n \
    --glob '!.git/**' \
    --glob '*.md' \
    '/home/[A-Za-z0-9._-]+/' \
    "${ROOT_DIR}" || true
)"
if [[ -n "${home_hits}" ]]; then
  has_failures=1
  echo "[path-check] FAIL: machine-specific home paths found in docs:"
  echo "${home_hits}"
fi

echo "[path-check] scanning repository for personal home-directory paths"
repo_home_hits="$(
  rg -n \
    --glob '!.git/**' \
    --glob '!scripts/check-hardcoded-paths.sh' \
    --glob '!scripts/check-precommit.sh' \
    '/home/[A-Za-z0-9._-]+/|/Users/[A-Za-z0-9._-]+/' \
    "${ROOT_DIR}" || true
)"
if [[ -n "${repo_home_hits}" ]]; then
  has_failures=1
  echo "[path-check] FAIL: personal home-directory paths found:"
  echo "${repo_home_hits}"
fi

if [[ "${has_failures}" -ne 0 ]]; then
  exit 1
fi

echo "[path-check] PASS"
