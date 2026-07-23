#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
GO_LIB="${SCRIPT_DIR}/lib-go.sh"
PNPM_LIB="${SCRIPT_DIR}/lib-pnpm.sh"
TOOL_VERSIONS="${SCRIPT_DIR}/security-tool-versions.sh"
cd "${ROOT_DIR}"

for required_file in "${GO_LIB}" "${PNPM_LIB}" "${TOOL_VERSIONS}"; do
  if [[ ! -f "${required_file}" ]]; then
    echo "[vuln-check] FAIL: missing required script: ${required_file}" >&2
    exit 1
  fi
done
# shellcheck disable=SC1091
source "${GO_LIB}"
# shellcheck disable=SC1091
source "${PNPM_LIB}"
# shellcheck disable=SC1091
source "${TOOL_VERSIONS}"
swarm_require_go "${ROOT_DIR}"

CACHE_ROOT="${GO_CACHE_ROOT:-${ROOT_DIR}/.cache/go}"
GOCACHE_DIR="${GOCACHE_DIR:-${CACHE_ROOT}/build}"
GOMODCACHE_DIR="${GOMODCACHE_DIR:-${CACHE_ROOT}/mod}"
GOPATH_DIR="${GOPATH_DIR:-${CACHE_ROOT}/path}"
SECURITY_BIN_DIR="${SECURITY_BIN_DIR:-${ROOT_DIR}/.tools/security/bin}"
mkdir -p "${GOCACHE_DIR}" "${GOMODCACHE_DIR}" "${GOPATH_DIR}" "${SECURITY_BIN_DIR}"

run_go() {
  GOCACHE="${GOCACHE_DIR}" \
  GOMODCACHE="${GOMODCACHE_DIR}" \
  GOPATH="${GOPATH_DIR}" \
  GOBIN="${SECURITY_BIN_DIR}" \
  GOTOOLCHAIN="${GOTOOLCHAIN}" \
  "${GO_BIN}" "$@"
}

installed_go_module_version() {
  local binary="$1"
  local module="$2"
  run_go version -m "${binary}" \
    | awk -v module="${module}" '$1 == "mod" && $2 == module { print $3; exit }'
}

ensure_govulncheck() {
  local govuln_bin="${SECURITY_BIN_DIR}/govulncheck"
  local installed_version=""
  if [[ -x "${govuln_bin}" ]]; then
    installed_version="$(installed_go_module_version "${govuln_bin}" "golang.org/x/vuln")"
  fi
  if [[ "${installed_version}" != "${GOVULNCHECK_VERSION}" ]]; then
    echo "[vuln-check] installing govulncheck@${GOVULNCHECK_VERSION}" >&2
    rm -f "${govuln_bin}"
    run_go install "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}"
    installed_version="$(installed_go_module_version "${govuln_bin}" "golang.org/x/vuln")"
  fi
  if [[ ! -x "${govuln_bin}" || "${installed_version}" != "${GOVULNCHECK_VERSION}" ]]; then
    echo "[vuln-check] FAIL: expected govulncheck ${GOVULNCHECK_VERSION}, got ${installed_version:-missing}" >&2
    return 1
  fi
  printf '%s\n' "${govuln_bin}"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{ print $1 }'
    return
  fi
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{ print $1 }'
    return
  fi
  echo "[vuln-check] FAIL: sha256sum or shasum is required" >&2
  return 1
}

ensure_trivy() {
  local trivy_bin="${SECURITY_BIN_DIR}/trivy"
  local installed_version=""
  if [[ -x "${trivy_bin}" ]]; then
    installed_version="$("${trivy_bin}" --version 2>/dev/null | awk 'NR == 1 { sub(/^Version: /, ""); print; exit }')"
  fi
  if [[ "${installed_version}" == "${TRIVY_VERSION}" ]]; then
    printf '%s\n' "${trivy_bin}"
    return 0
  fi

  local os arch artifact checksum archive actual_checksum
  case "$(uname -s)" in
    Linux) os="Linux" ;;
    Darwin) os="macOS" ;;
    *)
      echo "[vuln-check] FAIL: unsupported Trivy operating system: $(uname -s)" >&2
      return 1
      ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64) arch="64bit" ;;
    arm64|aarch64) arch="ARM64" ;;
    *)
      echo "[vuln-check] FAIL: unsupported Trivy architecture: $(uname -m)" >&2
      return 1
      ;;
  esac
  artifact="trivy_${TRIVY_VERSION}_${os}-${arch}.tar.gz"
  checksum="$(trivy_archive_checksum "${os}-${arch}")" || {
    echo "[vuln-check] FAIL: missing checksum for ${artifact}" >&2
    return 1
  }
  archive="$(mktemp "${TMPDIR:-/tmp}/swarm-trivy.XXXXXX.tar.gz")"
  echo "[vuln-check] installing Trivy ${TRIVY_VERSION} (${os}-${arch})" >&2
  curl -fL --retry 3 --proto '=https' --tlsv1.2 \
    "https://github.com/aquasecurity/trivy/releases/download/v${TRIVY_VERSION}/${artifact}" \
    -o "${archive}"
  actual_checksum="$(sha256_file "${archive}")"
  if [[ "${actual_checksum}" != "${checksum}" ]]; then
    rm -f "${archive}"
    echo "[vuln-check] FAIL: checksum mismatch for ${artifact}" >&2
    return 1
  fi
  tar -xzf "${archive}" -C "${SECURITY_BIN_DIR}" trivy
  rm -f "${archive}"
  chmod 0755 "${trivy_bin}"
  installed_version="$("${trivy_bin}" --version 2>/dev/null | awk 'NR == 1 { sub(/^Version: /, ""); print; exit }')"
  if [[ "${installed_version}" != "${TRIVY_VERSION}" ]]; then
    echo "[vuln-check] FAIL: expected Trivy ${TRIVY_VERSION}, got ${installed_version:-missing}" >&2
    return 1
  fi
  printf '%s\n' "${trivy_bin}"
}

run_govuln_module() {
  local module_dir="$1"
  local label="$2"
  local govuln_bin="$3"
  shift 3
  local -a package_patterns=("$@")
  echo "[vuln-check] running govulncheck (${label}, symbol reachability): ${package_patterns[*]}"
  (
    cd "${module_dir}"
    GOCACHE="${GOCACHE_DIR}" \
    GOMODCACHE="${GOMODCACHE_DIR}" \
    GOPATH="${GOPATH_DIR}" \
    GOTOOLCHAIN="${GOTOOLCHAIN}" \
    "${govuln_bin}" -mode=source -scan=symbol "${package_patterns[@]}"
  )
}

run_pnpm_audit() {
  echo "[vuln-check] running pnpm audit (web lockfile, production and development dependencies)"
  (
    cd "${ROOT_DIR}/web"
    [[ -f "package.json" ]] || { echo "[vuln-check] FAIL: missing web/package.json" >&2; exit 1; }
    [[ -f "pnpm-lock.yaml" ]] || { echo "[vuln-check] FAIL: missing web/pnpm-lock.yaml" >&2; exit 1; }
    local expected_pnpm actual_pnpm
    expected_pnpm="$(grep -o 'pnpm@[^"]*' package.json | cut -d@ -f2)"
    actual_pnpm="$(swarm_pnpm --version)"
    if [[ "${actual_pnpm}" != "${expected_pnpm}" ]]; then
      echo "[vuln-check] FAIL: expected pnpm ${expected_pnpm}, got ${actual_pnpm:-missing}" >&2
      exit 1
    fi
    swarm_pnpm audit --audit-level=low
  )
}

run_trivy_inventory() {
  local trivy_bin="$1"
  local target
  local -a targets=(
    "${ROOT_DIR}/go.mod"
    "${ROOT_DIR}/swarmd/go.mod"
    "${ROOT_DIR}/web/pnpm-lock.yaml"
  )
  echo "[vuln-check] running Trivy on tracked dependency manifests (both Go modules and web, including dev dependencies)"
  for target in "${targets[@]}"; do
    "${trivy_bin}" fs \
      --scanners vuln \
      --pkg-types library \
      --include-dev-deps \
      --detection-priority comprehensive \
      --severity UNKNOWN,LOW,MEDIUM,HIGH,CRITICAL \
      --exit-code 1 \
      --ignorefile "${ROOT_DIR}/.trivyignore.yaml" \
      "${target}"
  done
}

if ! command -v curl >/dev/null 2>&1; then
  echo "[vuln-check] FAIL: missing required command: curl" >&2
  exit 1
fi
if ! command -v pnpm >/dev/null 2>&1 && ! command -v corepack >/dev/null 2>&1; then
  echo "[vuln-check] FAIL: missing required command: pnpm or corepack" >&2
  exit 1
fi

GOVULN_BIN="$(ensure_govulncheck)"
TRIVY_BIN="$(ensure_trivy)"
echo "[vuln-check] govulncheck ${GOVULNCHECK_VERSION}; Trivy ${TRIVY_VERSION}; web $(grep -o 'pnpm@[^"]*' "${ROOT_DIR}/web/package.json")"
# The repository intentionally contains relocated test fixtures and ignored tmp
# programs inside both modules. Scan the canonical production source roots so
# those non-product trees cannot make vulnerability coverage machine-dependent.
run_govuln_module "${ROOT_DIR}" "root module" "${GOVULN_BIN}" ./cmd/... ./internal/... ./pkg/... ./theme/...
run_govuln_module "${ROOT_DIR}/swarmd" "swarmd module" "${GOVULN_BIN}" ./cmd/... ./internal/...
run_pnpm_audit
run_trivy_inventory "${TRIVY_BIN}"

echo "[vuln-check] PASS"
