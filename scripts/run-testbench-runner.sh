#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/run-testbench-runner.sh [runner-name] [runner options...]

Loads the ignored repository-root .env, maps this clean worktree to its stable
slot in the bounded isolated container pool, deploys the exact HEAD when needed,
and runs a checked-in scripts/runners scenario through temporary loopback tunnels.
Default runner: basic-plan-auto.
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
  printf 'run-testbench-runner: artifact-v2-provider-proof is retired; managed candidate testing uses artifact-v3-multipart-e2e\n' >&2
  exit 2
fi

model_args=()
model_args+=(--action-model "${SWARM_TESTBENCH_ACTION_MODEL}" --action-thinking "${SWARM_TESTBENCH_ACTION_THINKING}")
model_args+=(--plan-model "${SWARM_TESTBENCH_PLAN_MODEL}" --plan-thinking "${SWARM_TESTBENCH_PLAN_THINKING}")
model_args+=(--coder-model "${SWARM_TESTBENCH_CODER_MODEL}" --coder-thinking "${SWARM_TESTBENCH_CODER_THINKING}")
model_args+=(--designer-model "${SWARM_TESTBENCH_DESIGNER_MODEL}" --designer-thinking "${SWARM_TESTBENCH_DESIGNER_THINKING}")
[[ -n "${SWARM_TESTBENCH_LINKED_WORKSPACE_PATH:-}" ]] && model_args+=(--linked-workspace-path "${SWARM_TESTBENCH_LINKED_WORKSPACE_PATH}")

# Fixed package path matching htmlcapture.SystemChromePath and the host AppArmor policy.
readonly SYSTEM_CHROME_PATH='/opt/google/chrome/chrome'
browser_args=()
if [[ "${RUNNER}" == "artifact-v3-multipart-e2e" ]]; then
  [[ -x "${SYSTEM_CHROME_PATH}" ]] || { printf 'run-testbench-runner: trusted system Chrome is unavailable at %s\n' "${SYSTEM_CHROME_PATH}" >&2; exit 1; }
  browser_args+=(--browser-executable "${SYSTEM_CHROME_PATH}")
fi

runner=("${ROOT_DIR}/scripts/run-runner-test.sh"
  "__SWARM_DESKTOP_URL__"
  "${SWARM_TESTBENCH_PROVIDER}"
  "${RUNNER}"
  "${model_args[@]}"
  "${browser_args[@]}"
  "$@")

exec "${ROOT_DIR}/scripts/testbench-e2e-tunnel.sh" run "${runner[@]}"
