#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/render-testbench-model-config.sh [--env-file <path>]

Loads the canonical non-secret testbench model posture and renders the exact
root-owned configuration consumed by testbench release-gate runners. Output
contains model identifiers and thinking levels only; credentials are forbidden.
USAGE
}

fail() {
  printf 'render-testbench-model-config: %s\n' "$*" >&2
  exit 1
}

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${ROOT_DIR}/.env"
while (($# > 0)); do
  case "$1" in
    --env-file) (($# >= 2)) || fail '--env-file requires a value'; ENV_FILE="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
  esac
done
[[ -f "${ENV_FILE}" && ! -L "${ENV_FILE}" ]] || fail "model environment file is missing or unsafe: ${ENV_FILE}"

# shellcheck source=scripts/lib-testbench-e2e.sh
source "${ROOT_DIR}/scripts/lib-testbench-e2e.sh"
SWARM_TESTBENCH_ENV_FILE="${ENV_FILE}"
export SWARM_TESTBENCH_ENV_FILE
swarm_testbench_load_env "${ROOT_DIR}" || exit 1
swarm_testbench_validate_env || exit 1

cat <<EOF
SWARM_RELEASE_GATE_PROVIDER=${SWARM_TESTBENCH_PROVIDER}
SWARM_RELEASE_GATE_MODEL=${SWARM_TESTBENCH_MODEL}
SWARM_RELEASE_GATE_THINKING=${SWARM_TESTBENCH_THINKING}
SWARM_RELEASE_GATE_ACTION_MODEL=${SWARM_TESTBENCH_ACTION_MODEL}
SWARM_RELEASE_GATE_ACTION_THINKING=${SWARM_TESTBENCH_ACTION_THINKING}
SWARM_RELEASE_GATE_PLAN_MODEL=${SWARM_TESTBENCH_PLAN_MODEL}
SWARM_RELEASE_GATE_PLAN_THINKING=${SWARM_TESTBENCH_PLAN_THINKING}
SWARM_RELEASE_GATE_CODER_MODEL=${SWARM_TESTBENCH_CODER_MODEL}
SWARM_RELEASE_GATE_CODER_THINKING=${SWARM_TESTBENCH_CODER_THINKING}
SWARM_RELEASE_GATE_DESIGNER_MODEL=${SWARM_TESTBENCH_DESIGNER_MODEL}
SWARM_RELEASE_GATE_DESIGNER_THINKING=${SWARM_TESTBENCH_DESIGNER_THINKING}
EOF
