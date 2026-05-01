#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
LANE_LIB="${SCRIPT_DIR}/lib-lane.sh"
GO_LIB="${SCRIPT_DIR}/lib-go.sh"

if [[ ! -f "${LANE_LIB}" ]]; then
  echo "missing lane resolver script at ${LANE_LIB}" >&2
  exit 1
fi
if [[ ! -f "${GO_LIB}" ]]; then
  echo "missing go resolver script at ${GO_LIB}" >&2
  exit 1
fi
# shellcheck disable=SC1091
source "${LANE_LIB}"
# shellcheck disable=SC1091
source "${GO_LIB}"

swarm_require_go "${ROOT_DIR}"

CACHE_ROOT="${GO_CACHE_ROOT:-${ROOT_DIR}/.cache/go}"
GOCACHE_DIR="${GOCACHE_DIR:-${CACHE_ROOT}/build}"
GOMODCACHE_DIR="${GOMODCACHE_DIR:-${CACHE_ROOT}/mod}"
GOPATH_DIR="${GOPATH_DIR:-${CACHE_ROOT}/path}"
OUT_DIR="${OUT_DIR:-${SWARM_TOOL_BIN_DIR:-$(swarm_lane_tool_bin_dir)}}"
SKIP_REBUILD="${SWARM_BUILD_TOOLS_SKIP_REBUILD:-0}"
mkdir -p "${OUT_DIR}" "${GOCACHE_DIR}" "${GOMODCACHE_DIR}" "${GOPATH_DIR}"

build_tool() {
  local name="$1"
  local pkg="$2"
  local invoked_name="${3:-}"
  if [[ "${SKIP_REBUILD}" == "1" && "${name}" == "rebuild" ]]; then
    return
  fi
  local args=(build -trimpath)
  if [[ -n "${invoked_name}" ]]; then
    args+=( -ldflags "-X main.defaultInvokedName=${invoked_name}" )
  fi
  args+=( -o "${OUT_DIR}/${name}" "${pkg}" )
  GOCACHE="${GOCACHE_DIR}" \
  GOMODCACHE="${GOMODCACHE_DIR}" \
  GOPATH="${GOPATH_DIR}" \
  GOTOOLCHAIN="${GOTOOLCHAIN}" \
  "${GO_BIN}" "${args[@]}"
}

build_tool swarm ./cmd/swarm
build_tool rebuild ./cmd/rebuild
build_tool swarmsetup ./cmd/swarmsetup
build_tool swarmdev ./cmd/swarm swarmdev

printf 'built launcher tools in %s\n' "${OUT_DIR}"
