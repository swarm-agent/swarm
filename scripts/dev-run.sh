#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
TUI_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

LANE_LIB="${SCRIPT_DIR}/lib-lane.sh"
if [[ ! -f "${LANE_LIB}" ]]; then
  LANE_LIB="${TUI_ROOT}/scripts/lib-lane.sh"
fi
if [[ ! -f "${LANE_LIB}" ]]; then
  echo "missing lane resolver script (expected at ${SCRIPT_DIR}/lib-lane.sh or ${TUI_ROOT}/scripts/lib-lane.sh)" >&2
  exit 1
fi
# shellcheck disable=SC1091
source "${LANE_LIB}"
lane="$(swarm_lane_default)"
swarm_lane_export_profile "${lane}" "${TUI_ROOT}"

if [[ "${SWARMTUI_FORCE_BUILD:-0}" == "1" ]]; then
  "${SCRIPT_DIR}/dev-build.sh"
fi

if [[ -x "${SWARM_BIN_DIR}/swarmtui" ]]; then
  exec "${SWARM_BIN_DIR}/swarmtui" "$@"
fi

if [[ "${SWARM_LANE}" == "main" ]]; then
  echo "swarm main lane binary not found at ${SWARM_BIN_DIR}/swarmtui" >&2
  echo "run ./rebuild from this worktree, then launch again" >&2
  exit 1
fi

"${SCRIPT_DIR}/dev-build.sh"

if [[ ! -x "${SWARM_BIN_DIR}/swarmtui" ]]; then
  echo "swarmtui binary missing at ${SWARM_BIN_DIR}/swarmtui after build" >&2
  exit 1
fi

exec "${SWARM_BIN_DIR}/swarmtui" "$@"
