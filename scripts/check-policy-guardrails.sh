#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

has_failures=0

echo "[policy-check] scanning for banned silent-fallback patterns"
silent_fallback_hits="$({
  rg -n \
    --glob '!.git/**' \
    --glob '*.go' \
    --glob '*.sh' \
    --glob '!**/*_test.go' \
    --glob '!scripts/check-policy-guardrails.sh' \
    '(?i)silent fallback|silently fallback|fallback silently|best effort|ignore error|swallow error|discard error|noop on error'
} || true)"
if [[ -n "${silent_fallback_hits}" ]]; then
  has_failures=1
  echo "[policy-check] FAIL: potential silent fallback patterns found:"
  echo "${silent_fallback_hits}"
fi

echo "[policy-check] scanning runtime app command list for /quit and /rebuild"
missing_commands=()
if ! rg -q 'Command: "/quit"' internal/app/app.go; then
  missing_commands+=("/quit command suggestion")
fi
if ! rg -q 'Command: "/rebuild"' internal/app/app.go; then
  missing_commands+=("/rebuild command suggestion")
fi
if ! rg -q 'case "quit", "exit":' internal/app/app.go; then
  missing_commands+=("/quit command handler")
fi
if ! rg -q 'case "rebuild":' internal/app/app.go; then
  missing_commands+=("/rebuild command handler")
fi
if (( ${#missing_commands[@]} > 0 )); then
  has_failures=1
  echo "[policy-check] FAIL: command coverage missing:"
  printf '  - %s\n' "${missing_commands[@]}"
fi

echo "[policy-check] scanning for oversized source files (warning at >= 2000 lines)"
oversized_files="$(
  git ls-files '*.go' '*.sh' | while IFS= read -r file; do
    if [[ -z "${file}" ]]; then
      continue
    fi
    line_count="$(wc -l < "${file}" | tr -d '[:space:]')"
    if [[ "${line_count}" -ge 2000 ]]; then
      printf '%7d  %s\n' "${line_count}" "${file}"
    fi
  done | sort -nr
)"
if [[ -n "${oversized_files}" ]]; then
  echo "[policy-check] WARN: files at/over 2000 lines should be queued for refactor:"
  echo "${oversized_files}"
fi

echo "[policy-check] scanning test file placement (target: tests/)"

new_colocated_tests="$(
  {
    git diff --name-only --diff-filter=A HEAD || true
    git ls-files --others --exclude-standard || true
  } | rg '_test\.go$' | rg -v '^tests/' | sort -u || true
)"
if [[ -n "${new_colocated_tests}" ]]; then
  echo "[policy-check] WARN: new _test.go files outside tests/ detected:"
  echo "${new_colocated_tests}"
fi

if [[ "${has_failures}" -ne 0 ]]; then
  exit 1
fi

echo "[policy-check] PASS"
