#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

# shellcheck disable=SC1091
source "${SCRIPT_DIR}/lib-lane.sh"

lane="$(swarm_lane_default)"
swarm_lane_export_profile "${lane}" "${ROOT_DIR}"

bash "${ROOT_DIR}/scripts/build-tools.sh"
exec "${SWARM_TOOL_BIN_DIR}/rebuild" "${SWARM_LANE}" "$@"
