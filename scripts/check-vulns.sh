#!/usr/bin/env bash
set -uo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
GO_LIB="${ROOT_DIR}/scripts/lib-go.sh"
cd "${ROOT_DIR}"

if [[ ! -f "${GO_LIB}" ]]; then
  echo "missing go resolver script at ${GO_LIB}" >&2
  exit 1
fi
# shellcheck disable=SC1091
source "${GO_LIB}"
swarm_require_go "${ROOT_DIR}"

CACHE_ROOT="${GO_CACHE_ROOT:-${ROOT_DIR}/.cache/go}"
GOCACHE_DIR="${GOCACHE_DIR:-${CACHE_ROOT}/build}"
GOMODCACHE_DIR="${GOMODCACHE_DIR:-${CACHE_ROOT}/mod}"
GOPATH_DIR="${GOPATH_DIR:-${CACHE_ROOT}/path}"
VULN_BIN_DIR="${ROOT_DIR}/.tools/bin"
GOBIN_DIR="${GOBIN_DIR:-${VULN_BIN_DIR}}"
mkdir -p "${GOCACHE_DIR}" "${GOMODCACHE_DIR}" "${GOPATH_DIR}" "${VULN_BIN_DIR}"

fail_count=0

run_go() {
  GOCACHE="${GOCACHE_DIR}" \
  GOMODCACHE="${GOMODCACHE_DIR}" \
  GOPATH="${GOPATH_DIR}" \
  GOBIN="${GOBIN_DIR}" \
  GOTOOLCHAIN="${GOTOOLCHAIN}" \
  "${GO_BIN}" "$@"
}

need_cmd() {
  local cmd="$1"
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    echo "[vuln-check] FAIL: missing required command: ${cmd}" >&2
    fail_count=$((fail_count + 1))
    return 1
  fi
}

ensure_govulncheck() {
  local govuln_bin="${VULN_BIN_DIR}/govulncheck"
  if [[ -x "${govuln_bin}" ]]; then
    printf '%s\n' "${govuln_bin}"
    return 0
  fi
  if command -v govulncheck >/dev/null 2>&1; then
    command -v govulncheck
    return 0
  fi

  echo "[vuln-check] installing govulncheck into ${VULN_BIN_DIR}" >&2
  if ! run_go install golang.org/x/vuln/cmd/govulncheck@latest; then
    echo "[vuln-check] FAIL: unable to install govulncheck" >&2
    fail_count=$((fail_count + 1))
    return 1
  fi
  if [[ ! -x "${govuln_bin}" ]]; then
    echo "[vuln-check] FAIL: govulncheck install did not produce ${govuln_bin}" >&2
    fail_count=$((fail_count + 1))
    return 1
  fi
  printf '%s\n' "${govuln_bin}"
}

run_govuln_module() {
  local module_dir="$1"
  local label="$2"
  local govuln_bin="$3"
  echo "[vuln-check] running govulncheck (${label})"
  if ! (
    cd "${module_dir}"
    GOCACHE="${GOCACHE_DIR}" \
    GOMODCACHE="${GOMODCACHE_DIR}" \
    GOPATH="${GOPATH_DIR}" \
    GOTOOLCHAIN="${GOTOOLCHAIN}" \
    "${govuln_bin}" ./...
  ); then
    echo "[vuln-check] FAIL: govulncheck reported vulnerabilities in ${label}" >&2
    fail_count=$((fail_count + 1))
  fi
}

run_npm_audit() {
  echo "[vuln-check] running npm audit (web lockfile, all deps)"
  if ! (
    cd "${ROOT_DIR}/web"
    if [[ ! -f "package-lock.json" ]]; then
      echo "[vuln-check] FAIL: missing web/package-lock.json" >&2
      exit 1
    fi
    npm audit --package-lock-only --audit-level=low
  ); then
    echo "[vuln-check] FAIL: npm audit reported web dependency vulnerabilities" >&2
    fail_count=$((fail_count + 1))
  fi
}

need_cmd npm || true
GOVULN_BIN=""
if GOVULN_BIN="$(ensure_govulncheck)"; then
  run_govuln_module "${ROOT_DIR}" "root module" "${GOVULN_BIN}"
  run_govuln_module "${ROOT_DIR}/swarmd" "swarmd module" "${GOVULN_BIN}"
fi
run_npm_audit

if (( fail_count > 0 )); then
  echo "[vuln-check] CRITICAL: vulnerability scan failed; treat this as a commit blocker." >&2
  exit 1
fi

echo "[vuln-check] PASS"
