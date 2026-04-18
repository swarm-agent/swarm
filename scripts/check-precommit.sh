#!/usr/bin/env bash
set -euo pipefail

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
mkdir -p "${GOCACHE_DIR}" "${GOMODCACHE_DIR}" "${GOPATH_DIR}"

run_go() {
  GOCACHE="${GOCACHE_DIR}" \
  GOMODCACHE="${GOMODCACHE_DIR}" \
  GOPATH="${GOPATH_DIR}" \
  GOTOOLCHAIN="${GOTOOLCHAIN}" \
  "${GO_BIN}" "$@"
}

list_root_test_packages() {
  local package
  while IFS= read -r package; do
    case "${package}" in
      swarm-refactor/swarmtui/tests/internal|swarm-refactor/swarmtui/tests/internal/*)
        continue
        ;;
      swarm-refactor/swarmtui/tests/swarmd|swarm-refactor/swarmtui/tests/swarmd/*)
        continue
        ;;
    esac
    printf '%s\n' "${package}"
  done < <(run_go list ./...)
}

echo "[precommit] validating AGENTS.md policy sections"
if ! rg -q '^## 1\. Non-Negotiable Public Repo Rules$' "${ROOT_DIR}/AGENTS.md"; then
  echo "[precommit] FAIL: AGENTS.md missing '## 1. Non-Negotiable Public Repo Rules'" >&2
  exit 1
fi
if ! rg -q '^## 2\. Task Execution Policy$' "${ROOT_DIR}/AGENTS.md"; then
  echo "[precommit] FAIL: AGENTS.md missing '## 2. Task Execution Policy'" >&2
  exit 1
fi
if ! rg -q '^## 4\. Safe Throwaway / Scratch Locations$' "${ROOT_DIR}/AGENTS.md"; then
  echo "[precommit] FAIL: AGENTS.md missing '## 4. Safe Throwaway / Scratch Locations'" >&2
  exit 1
fi

"${SCRIPT_DIR}/check-hardcoded-paths.sh"
"${SCRIPT_DIR}/check-secrets.sh"
"${SCRIPT_DIR}/check-hidden-text.sh"
bash "${SCRIPT_DIR}/check-policy-guardrails.sh"
bash "${SCRIPT_DIR}/check-vulns.sh"

echo "[precommit] checking gofmt"
mapfile -t go_files < <(git ls-files '*.go')
if (( ${#go_files[@]} > 0 )); then
  mapfile -t unformatted < <("${GOFMT_BIN}" -l "${go_files[@]}")
  if (( ${#unformatted[@]} > 0 )); then
    echo "[precommit] FAIL: gofmt required for:"
    printf '  %s\n' "${unformatted[@]}"
    exit 1
  fi
fi

echo "[precommit] tests are opt-in (run only when explicitly requested)"
if [[ "${SWARM_PRECOMMIT_RUN_TESTS:-0}" == "1" ]]; then
  echo "[precommit] excluding relocated tests/internal/... and tests/swarmd/... packages from root go test"
  echo "[precommit] reason: those directories currently contain package-scope and cross-module tests that do not compile as standalone root-module packages"
  mapfile -t root_test_packages < <(list_root_test_packages)
  if (( ${#root_test_packages[@]} == 0 )); then
    echo "[precommit] FAIL: no root-module packages discovered for go test" >&2
    exit 1
  fi
  echo "[precommit] running go test (tui module)"
  run_go test "${root_test_packages[@]}"

  echo "[precommit] running go test (swarmd module)"
  (
    cd "${ROOT_DIR}/swarmd"
    run_go test ./...
  )
else
  echo "[precommit] skipping go test (set SWARM_PRECOMMIT_RUN_TESTS=1 to enable)"
fi

echo "[precommit] PASS"
