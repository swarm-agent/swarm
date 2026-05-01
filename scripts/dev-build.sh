#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
TUI_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
if [[ "$(basename -- "${TUI_ROOT}")" == "swarmtui" ]] && [[ -d "${TUI_ROOT}/../scripts" ]]; then
  PROJECT_ROOT="$(cd -- "${TUI_ROOT}/.." && pwd)"
else
  PROJECT_ROOT="${TUI_ROOT}"
fi
HOST_ROOT="$(cd -- "${PROJECT_ROOT}/.." && pwd)"

LANE_LIB="${SCRIPT_DIR}/lib-lane.sh"
GO_LIB="${SCRIPT_DIR}/lib-go.sh"
if [[ ! -f "${LANE_LIB}" ]]; then
  LANE_LIB="${PROJECT_ROOT}/scripts/lib-lane.sh"
fi
if [[ ! -f "${GO_LIB}" ]]; then
  GO_LIB="${PROJECT_ROOT}/scripts/lib-go.sh"
fi
if [[ ! -f "${LANE_LIB}" ]]; then
  echo "missing lane resolver script (expected at ${SCRIPT_DIR}/lib-lane.sh or ${PROJECT_ROOT}/scripts/lib-lane.sh)" >&2
  exit 1
fi
if [[ ! -f "${GO_LIB}" ]]; then
  echo "missing go resolver script (expected at ${SCRIPT_DIR}/lib-go.sh or ${PROJECT_ROOT}/scripts/lib-go.sh)" >&2
  exit 1
fi
# shellcheck disable=SC1091
source "${LANE_LIB}"
# shellcheck disable=SC1091
source "${GO_LIB}"
lane="$(swarm_lane_default)"
swarm_lane_export_profile "${lane}" "${PROJECT_ROOT}"

OUT_DIR="${OUT_DIR:-${SWARM_BIN_DIR}}"
swarm_require_go "${PROJECT_ROOT}"

CACHE_ROOT="${GO_CACHE_ROOT:-${PROJECT_ROOT}/.cache/go}"
GOCACHE_DIR="${GOCACHE_DIR:-${CACHE_ROOT}/build}"
GOMODCACHE_DIR="${GOMODCACHE_DIR:-${CACHE_ROOT}/mod}"
GOPATH_DIR="${GOPATH_DIR:-${CACHE_ROOT}/path}"
mkdir -p "${OUT_DIR}" "${SWARM_TOOL_BIN_DIR}" "${GOCACHE_DIR}" "${GOMODCACHE_DIR}" "${GOPATH_DIR}"

echo "building swarmtui (lane=${SWARM_LANE}) with ${GO_BIN}..."
(
  cd "${TUI_ROOT}"
  GOCACHE="${GOCACHE_DIR}" \
  GOMODCACHE="${GOMODCACHE_DIR}" \
  GOPATH="${GOPATH_DIR}" \
  GOTOOLCHAIN="${GOTOOLCHAIN}" \
  "${GO_BIN}" build -trimpath -o "${OUT_DIR}/swarmtui" ./cmd/swarmtui
)
GO_BIN="${GO_BIN}" \
GO_CACHE_ROOT="${CACHE_ROOT}" \
GOCACHE_DIR="${GOCACHE_DIR}" \
GOMODCACHE_DIR="${GOMODCACHE_DIR}" \
GOPATH_DIR="${GOPATH_DIR}" \
SWARM_BUILD_TOOLS_SKIP_REBUILD=1 \
bash "${PROJECT_ROOT}/scripts/build-tools.sh"

echo "built ${OUT_DIR}/swarmtui"
echo "built launcher tools in ${SWARM_TOOL_BIN_DIR}"
