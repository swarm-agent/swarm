#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/run-testbench-tui-launch-e2e.sh [--workspace <name-or-path>] [--timeout-seconds <n>]

Loads the ignored repository-root .env and runs the canonical TUI launch scenarios
sequentially against the configured testbench. The first failing scenario stops
the suite with its focused PTY, durable-message, and event evidence.
USAGE
}

fail() {
  printf 'run-testbench-tui-launch-e2e: %s\n' "$*" >&2
  exit 1
}

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib-testbench-e2e.sh
source "${ROOT_DIR}/scripts/lib-testbench-e2e.sh"
swarm_testbench_load_env "${ROOT_DIR}" || exit 1
swarm_testbench_validate_env || exit 1

WORKSPACE=""
TIMEOUT_SECONDS="180"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --workspace) [[ $# -ge 2 ]] || fail "--workspace requires a value"; WORKSPACE="$2"; shift 2 ;;
    --timeout-seconds) [[ $# -ge 2 ]] || fail "--timeout-seconds requires a value"; TIMEOUT_SECONDS="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ ]] || fail "--timeout-seconds must be an integer"
(( TIMEOUT_SECONDS >= 60 )) || fail "--timeout-seconds must be at least 60"
OVERALL_TIMEOUT_SECONDS=$((TIMEOUT_SECONDS + 40))

TUI_TEST="${ROOT_DIR}/tests/testbench_tui_pty_e2e.sh"
COMMON=(
  --primary-ssh "${SWARM_PRIMARY_SSH}"
  --api-url "http://127.0.0.1:${SWARM_TESTBENCH_REMOTE_API_PORT}"
  --provider "${SWARM_TESTBENCH_PROVIDER}"
  --thinking "${SWARM_TESTBENCH_THINKING:-low}"
  --skip-follow-up
  --timeout-seconds "${TIMEOUT_SECONDS}"
  --overall-timeout-seconds "${OVERALL_TIMEOUT_SECONDS}"
)
if [[ -n "${SWARM_TESTBENCH_MODEL:-}" ]]; then
  COMMON+=(--model "${SWARM_TESTBENCH_MODEL}")
fi
if [[ -n "${WORKSPACE}" ]]; then
  fail "--workspace is reserved until the TUI harness accepts an explicit workspace selector"
fi

run_scenario() {
  local name="$1" mode="$2" worktree="$3" marker="$4" command="$5"
  printf '\n== TUI launch scenario: %s ==\n' "${name}"
  "${TUI_TEST}" "${COMMON[@]}" \
    --launch-command "${command}" \
    --first-marker "${marker}" \
    --expected-mode "${mode}" \
    --expected-worktree "${worktree}"
}

mode_prompt() {
  local mode="$1" marker="$2"
  printf 'Read only your injected runtime session mode. If it is %s, reply exactly %s; otherwise reply exactly MODE_MISMATCH. Return text only. Do not call tools, create a plan, or inspect files.' "${mode}" "${marker}"
}

marker="TUI_CANONICAL_NEW_AUTO_OK"
run_scenario new-auto auto false "${marker}" "/new $(mode_prompt auto "${marker}")"
marker="TUI_CANONICAL_NEW_PLAN_OK"
run_scenario new-plan plan false "${marker}" "/new plan $(mode_prompt plan "${marker}")"
marker="TUI_CANONICAL_NEW_WORKTREE_OK"
run_scenario new-worktree auto true "${marker}" "/new worktree $(mode_prompt auto "${marker}")"
marker="TUI_CANONICAL_NEW_WP_OK"
run_scenario new-wp plan true "${marker}" "/new wp $(mode_prompt plan "${marker}")"
marker="TUI_CANONICAL_TASK_AUTO_OK"
run_scenario task-auto auto true "${marker}" "/task $(mode_prompt auto "${marker}")"
marker="TUI_CANONICAL_TASK_PLAN_OK"
run_scenario task-plan plan true "${marker}" "/task plan $(mode_prompt plan "${marker}")"

printf '\ncanonical TUI launch suite PASS (6 sequential scenarios)\n'
