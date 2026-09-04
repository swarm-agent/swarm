#!/usr/bin/env bash
# Requirement: the dependency gate must tolerate bounded transient registry
# failures without treating scanner unavailability as a clean audit.
# Threat: a flaky advisory endpoint either blocks every protected push or is
# ignored in a way that lets a vulnerability result pass.
# Authority: scripts/check-vulns.sh run_pnpm_audit.
# Layer: a shell-level fake pnpm proves retry, report, and fail-closed behavior
# without contacting a registry or exercising unrelated Go/Trivy scanners.
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck disable=SC1091
source "${ROOT_DIR}/scripts/check-vulns.sh"

TEST_ROOT="$(mktemp -d "${TMPDIR:?TMPDIR must be set}/swarm-pnpm-audit-test.XXXXXX")"
trap 'rm -rf "${TEST_ROOT}"' EXIT
mkdir -p "${TEST_ROOT}/web"
printf '%s\n' '{"packageManager":"pnpm@11.13.1"}' >"${TEST_ROOT}/web/package.json"
printf '%s\n' 'lockfileVersion: "9.0"' >"${TEST_ROOT}/web/pnpm-lock.yaml"

export ROOT_DIR="${TEST_ROOT}"
export ATTEMPT_FILE="${TEST_ROOT}/attempts"
export FAKE_MODE=""

swarm_pnpm() {
  if [[ "$1" == "--version" ]]; then
    printf '%s\n' '11.13.1'
    return 0
  fi
  local attempts=0
  [[ -f "${ATTEMPT_FILE}" ]] && attempts="$(cat "${ATTEMPT_FILE}")"
  attempts=$((attempts + 1))
  printf '%s\n' "${attempts}" >"${ATTEMPT_FILE}"
  case "${FAKE_MODE}" in
    transient)
      if (( attempts < 3 )); then
        printf '%s\n' '{"error":{"code":"pnpm","message":"fetch failed"}}'
        return 1
      fi
      printf '%s\n' '{"advisories":{},"metadata":{"vulnerabilities":{"low":0}}}'
      return 0
      ;;
    vulnerability)
      printf '%s\n' '{"advisories":{"GHSA-test":{"severity":"high"}},"metadata":{"vulnerabilities":{"high":1}}}'
      return 1
      ;;
    unavailable)
      printf '%s\n' '{"error":{"code":"pnpm","message":"fetch failed"}}'
      return 1
      ;;
    stderr_unavailable)
      printf '%s\n' 'request timed out' >&2
      return 1
      ;;
    *)
      return 99
      ;;
  esac
}

sleep() { :; }

FAKE_MODE=transient
run_pnpm_audit >/dev/null
[[ "$(cat "${ATTEMPT_FILE}")" == "3" ]] || { echo "expected transient audit to succeed on attempt 3" >&2; exit 1; }

rm -f "${ATTEMPT_FILE}"
FAKE_MODE=vulnerability
if run_pnpm_audit >/dev/null 2>&1; then
  echo "vulnerability report must remain fatal" >&2
  exit 1
fi
[[ "$(cat "${ATTEMPT_FILE}")" == "1" ]] || { echo "vulnerability report must not be retried" >&2; exit 1; }

rm -f "${ATTEMPT_FILE}"
FAKE_MODE=unavailable
if run_pnpm_audit >/dev/null 2>&1; then
  echo "registry unavailability must fail closed" >&2
  exit 1
fi
[[ "$(cat "${ATTEMPT_FILE}")" == "3" ]] || { echo "unavailable audit must stop after 3 attempts" >&2; exit 1; }

rm -f "${ATTEMPT_FILE}"
FAKE_MODE=stderr_unavailable
if run_pnpm_audit >/dev/null 2>"${TEST_ROOT}/stderr"; then
  echo "stderr-only registry failure must fail closed" >&2
  exit 1
fi
[[ "$(cat "${ATTEMPT_FILE}")" == "3" ]] || { echo "stderr-only failure must stop after 3 attempts" >&2; exit 1; }
grep -q 'request timed out' "${TEST_ROOT}/stderr" || { echo "scanner stderr must remain visible" >&2; exit 1; }

echo "check_vulns_pnpm_audit_test: PASS"
