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

model_args=()
[[ -n "${SWARM_TESTBENCH_MODEL}" ]] && model_args+=(--model "${SWARM_TESTBENCH_MODEL}")
[[ -n "${SWARM_TESTBENCH_THINKING}" ]] && model_args+=(--thinking "${SWARM_TESTBENCH_THINKING}")

exec "${ROOT_DIR}/scripts/run-runner-test.sh" \
  "${SWARM_PRIMARY_SSH}" \
  "${SWARM_TESTBENCH_PROVIDER}" \
  "${RUNNER}" \
  "${model_args[@]}" \
  "$@"
