#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
GO_LIB="${ROOT_DIR}/scripts/lib-go.sh"
PNPM_LIB="${ROOT_DIR}/scripts/lib-pnpm.sh"
cd "${ROOT_DIR}"

if [[ ! -f "${GO_LIB}" ]]; then
  echo "missing go resolver script at ${GO_LIB}" >&2
  exit 1
fi
if [[ ! -f "${PNPM_LIB}" ]]; then
  echo "missing pnpm resolver script at ${PNPM_LIB}" >&2
  exit 1
fi
# shellcheck disable=SC1091
source "${GO_LIB}"
# shellcheck disable=SC1091
source "${PNPM_LIB}"
swarm_require_go "${ROOT_DIR}"

CACHE_ROOT="${GO_CACHE_ROOT:-${ROOT_DIR}/.cache/go}"
GOCACHE_DIR="${GOCACHE_DIR:-${CACHE_ROOT}/build}"
GOMODCACHE_DIR="${GOMODCACHE_DIR:-${CACHE_ROOT}/mod}"
GOPATH_DIR="${GOPATH_DIR:-${CACHE_ROOT}/path}"
VULN_BIN_DIR="${ROOT_DIR}/.tools/bin"
GOBIN_DIR="${GOBIN_DIR:-${VULN_BIN_DIR}}"
mkdir -p "${GOCACHE_DIR}" "${GOMODCACHE_DIR}" "${GOPATH_DIR}" "${VULN_BIN_DIR}"

run_go() {
  GOCACHE="${GOCACHE_DIR}" \
  GOMODCACHE="${GOMODCACHE_DIR}" \
  GOPATH="${GOPATH_DIR}" \
  GOBIN="${GOBIN_DIR}" \
  GOTOOLCHAIN="${GOTOOLCHAIN}" \
  "${GO_BIN}" "$@"
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
    return 1
  fi
  if [[ ! -x "${govuln_bin}" ]]; then
    echo "[vuln-check] FAIL: govulncheck install did not produce ${govuln_bin}" >&2
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
    echo "[vuln-check] FAIL: govulncheck failed for ${label}" >&2
    return 1
  fi
}

run_pnpm_audit() {
  echo "[vuln-check] running pnpm audit (web lockfile, all deps)"
  if ! (
    cd "${ROOT_DIR}/web"
    if [[ ! -f "pnpm-lock.yaml" ]]; then
      echo "[vuln-check] FAIL: missing web/pnpm-lock.yaml" >&2
      exit 1
    fi
    swarm_pnpm audit --audit-level=low
  ); then
    echo "[vuln-check] FAIL: pnpm audit reported web dependency vulnerabilities" >&2
    return 1
  fi
}

if ! command -v pnpm > /dev/null 2>&1 && ! command -v corepack > /dev/null 2>&1; then
  echo "[vuln-check] FAIL: missing required command: pnpm or corepack" >&2
  exit 1
fi
GOVULN_BIN="$(ensure_govulncheck)"
run_govuln_module "${ROOT_DIR}" "root module" "${GOVULN_BIN}"
run_govuln_module "${ROOT_DIR}/swarmd" "swarmd module" "${GOVULN_BIN}"
run_pnpm_audit

echo "[vuln-check] PASS"
