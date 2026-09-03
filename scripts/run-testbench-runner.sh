#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/run-testbench-runner.sh [runner-name] [runner options...]

Loads the ignored repository-root .env and runs a checked-in scripts/runners
scenario against the configured SSH alias. Default runner: basic-plan-auto.
USAGE
}

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib-testbench-e2e.sh
source "${ROOT_DIR}/scripts/lib-testbench-e2e.sh"
swarm_testbench_load_env "${ROOT_DIR}" || exit 1
swarm_testbench_validate_env || exit 1

case "${1:-}" in
  -h|--help) usage; exit 0 ;;
esac

RUNNER="basic-plan-auto"
if [[ $# -gt 0 && "${1}" != --* ]]; then
  RUNNER="$1"
  shift
fi

if [[ "${RUNNER}" == "artifact-v2-provider-proof" ]]; then
  SWARM_TESTBENCH_PROVIDER="fireworks"
  SWARM_TESTBENCH_ACTION_MODEL="deepseek-v4-flash-0731"
  SWARM_TESTBENCH_ACTION_THINKING="high"
  SWARM_TESTBENCH_DESIGNER_MODEL="deepseek-v4-flash-0731"
  SWARM_TESTBENCH_DESIGNER_THINKING="off"
fi

model_args=()
model_args+=(--action-model "${SWARM_TESTBENCH_ACTION_MODEL}" --action-thinking "${SWARM_TESTBENCH_ACTION_THINKING}")
model_args+=(--plan-model "${SWARM_TESTBENCH_PLAN_MODEL}" --plan-thinking "${SWARM_TESTBENCH_PLAN_THINKING}")
model_args+=(--coder-model "${SWARM_TESTBENCH_CODER_MODEL}" --coder-thinking "${SWARM_TESTBENCH_CODER_THINKING}")
model_args+=(--designer-model "${SWARM_TESTBENCH_DESIGNER_MODEL}" --designer-thinking "${SWARM_TESTBENCH_DESIGNER_THINKING}")
[[ -n "${SWARM_TESTBENCH_LINKED_WORKSPACE_PATH:-}" ]] && model_args+=(--linked-workspace-path "${SWARM_TESTBENCH_LINKED_WORKSPACE_PATH}")

exec "${ROOT_DIR}/scripts/run-runner-test.sh" \
  "${SWARM_PRIMARY_SSH}" \
  "${SWARM_TESTBENCH_PROVIDER}" \
  "${RUNNER}" \
  "${model_args[@]}" \
  "$@"
