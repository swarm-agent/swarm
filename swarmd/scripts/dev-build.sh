#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/lib-dev.sh"

mkdir -p "${BIN_DIR}"

echo "building swarmd binaries (lane=${SWARM_LANE}) with ${GO_BIN:-auto}..."
(
  cd "${SWARMD_ROOT}"
  run_go build -trimpath -o "${BIN_DIR}/swarmd" ./cmd/swarmd
  run_go build -trimpath -o "${BIN_DIR}/swarmctl" ./cmd/swarmctl
  run_go build -trimpath -o "${BIN_DIR}/swarm-fff-search" ./cmd/swarm-fff-search
)

echo "built:"
echo "  ${BIN_DIR}/swarmd"
echo "  ${BIN_DIR}/swarmctl"
echo "  ${BIN_DIR}/swarm-fff-search"

if [[ "${SWARMD_BUILD_HARD_RESTART:-1}" != "0" ]]; then
  echo "hard restarting swarmd to load rebuilt binaries (lane=${SWARM_LANE})..."
  "${SCRIPT_DIR}/dev-down.sh" >/dev/null 2>&1 || true
  "${SCRIPT_DIR}/dev-up.sh"
fi
