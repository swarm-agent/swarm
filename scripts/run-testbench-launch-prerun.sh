#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/run-testbench-launch-prerun.sh [options]

Runs the canonical pre-launch product proof against the configured testbench.
Connectivity is checked once, then dependency-independent suites run with bounded
parallelism. Every selected suite is allowed to finish so the aggregate reports
all failures instead of hiding later results behind the first failure.

Default suites:
  critical      local deterministic atlas-driven critical test gate
  onboarding    isolated local onboarding/bootstrap persistence gate
  desktop       real Desktop /new, /task, worktree, Plan-to-Auto lifecycle gate
  tui           real TUI /new, /task, worktree, and Plan launch gate
  plan-auto     API Plan-to-Auto two-checkpoint lifecycle runner
  task-routing  API /task Auto/Plan routing from current, explicit saved, and existing sessions
  task-program  live same/linked-repo Task Programs across both parent modes
  provider-sync signed sync/realtime repair plus one real provider response
                (uses the configured testbench provider/model; despite the
                legacy underlying script name, Fireworks is not hard-required)

Options:
  --jobs <n>                 Parallel suite limit (default: 4; maximum: 8)
  --suite <name>             Run only this suite; repeat to select several
  --skip-suite <name>        Exclude one default suite; repeatable
  --list-suites              Print suite names and exit
  --dry-run                  Print the selected commands without executing them
  --workspace <name-or-path> Desktop workspace selector
  --workspace-path <path>    Runner workspace path override
  --linked-workspace-path <path>
                             Second bound repository for task-program
  --desktop-timeout-ms <ms>  Desktop lifecycle wait budget (default: 900000)
  --tui-timeout-seconds <n>  TUI per-scenario wait budget (default: 180)
  --runner-timeout-ms <ms>   API runner wait budget (default: 600000)
  --expected-commit <sha>    Candidate commit override for provider-sync (default: local HEAD)
  --remote-repo <path>       Candidate checkout override (default: discovered testbench checkout)
  --evidence-dir <path>      Preserve aggregate logs at this ignored path
  --headful                  Show the Desktop Playwright browser
  -h, --help                 Show this help

The ignored .env remains the authority for the SSH alias, loopback ports,
provider/per-role model posture, and optional linked workspace path. Provider-sync derives
candidate authority from local HEAD plus the uniquely discovered testbench Swarm
checkout unless explicit overrides are supplied. This entrypoint does not rebuild
or deploy testbench, commit, push, or mutate production.
USAGE
}

fail() {
  printf 'run-testbench-launch-prerun: %s\n' "$*" >&2
  exit 1
}

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib-testbench-e2e.sh
source "${ROOT_DIR}/scripts/lib-testbench-e2e.sh"
# shellcheck source=scripts/lib-launch-prerun.sh
source "${ROOT_DIR}/scripts/lib-launch-prerun.sh"

DEFAULT_SUITES=(critical onboarding desktop tui plan-auto task-routing task-program provider-sync)
ALL_SUITES=("${DEFAULT_SUITES[@]}")
JOBS=4
DRY_RUN="false"
LIST_SUITES="false"
HEADFUL="false"
WORKSPACE=""
WORKSPACE_PATH=""
LINKED_WORKSPACE_PATH=""
DESKTOP_TIMEOUT_MS="900000"
TUI_TIMEOUT_SECONDS="180"
RUNNER_TIMEOUT_MS="600000"
EXPECTED_COMMIT="${SWARM_EXPECTED_COMMIT:-}"
REMOTE_REPO="${SWARM_REMOTE_REPO:-}"
EVIDENCE_DIR=""
SELECTED=()
SKIPPED=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --jobs) [[ $# -ge 2 ]] || fail "--jobs requires a value"; JOBS="$2"; shift 2 ;;
    --suite) [[ $# -ge 2 ]] || fail "--suite requires a value"; SELECTED+=("$2"); shift 2 ;;
    --skip-suite) [[ $# -ge 2 ]] || fail "--skip-suite requires a value"; SKIPPED+=("$2"); shift 2 ;;
    --list-suites) LIST_SUITES="true"; shift ;;
    --dry-run) DRY_RUN="true"; shift ;;
    --workspace) [[ $# -ge 2 ]] || fail "--workspace requires a value"; WORKSPACE="$2"; shift 2 ;;
    --workspace-path) [[ $# -ge 2 ]] || fail "--workspace-path requires a value"; WORKSPACE_PATH="$2"; shift 2 ;;
    --linked-workspace-path) [[ $# -ge 2 ]] || fail "--linked-workspace-path requires a value"; LINKED_WORKSPACE_PATH="$2"; shift 2 ;;
    --desktop-timeout-ms) [[ $# -ge 2 ]] || fail "--desktop-timeout-ms requires a value"; DESKTOP_TIMEOUT_MS="$2"; shift 2 ;;
    --tui-timeout-seconds) [[ $# -ge 2 ]] || fail "--tui-timeout-seconds requires a value"; TUI_TIMEOUT_SECONDS="$2"; shift 2 ;;
    --runner-timeout-ms) [[ $# -ge 2 ]] || fail "--runner-timeout-ms requires a value"; RUNNER_TIMEOUT_MS="$2"; shift 2 ;;
    --expected-commit) [[ $# -ge 2 ]] || fail "--expected-commit requires a value"; EXPECTED_COMMIT="$2"; shift 2 ;;
    --remote-repo) [[ $# -ge 2 ]] || fail "--remote-repo requires a value"; REMOTE_REPO="$2"; shift 2 ;;
    --evidence-dir) [[ $# -ge 2 ]] || fail "--evidence-dir requires a value"; EVIDENCE_DIR="$2"; shift 2 ;;
    --headful) HEADFUL="true"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

if [[ "${LIST_SUITES}" == "true" ]]; then
  printf '%s\n' "${ALL_SUITES[@]}"
  exit 0
fi

suite_known() {
  local wanted="$1" item
  for item in "${ALL_SUITES[@]}"; do [[ "${item}" == "${wanted}" ]] && return 0; done
  return 1
}

contains() {
  local wanted="$1"
  shift
  local item
  for item in "$@"; do [[ "${item}" == "${wanted}" ]] && return 0; done
  return 1
}

for suite in "${SELECTED[@]}" "${SKIPPED[@]}"; do
  [[ -n "${suite}" ]] || continue
  suite_known "${suite}" || fail "unknown suite ${suite}; use --list-suites"
done

if (( ${#SELECTED[@]} == 0 )); then
  SELECTED=("${DEFAULT_SUITES[@]}")
fi
SUITES=()
for suite in "${SELECTED[@]}"; do
  contains "${suite}" "${SKIPPED[@]}" && continue
  contains "${suite}" "${SUITES[@]}" || SUITES+=("${suite}")
done
(( ${#SUITES[@]} > 0 )) || fail "no suites remain after selection"
swarm_launch_prerun_validate_jobs "${JOBS}" || exit 2
[[ "${DESKTOP_TIMEOUT_MS}" =~ ^[0-9]+$ && "${DESKTOP_TIMEOUT_MS}" -ge 30000 ]] || fail "--desktop-timeout-ms must be at least 30000"
[[ "${TUI_TIMEOUT_SECONDS}" =~ ^[0-9]+$ && "${TUI_TIMEOUT_SECONDS}" -ge 60 ]] || fail "--tui-timeout-seconds must be at least 60"
[[ "${RUNNER_TIMEOUT_MS}" =~ ^[0-9]+$ && "${RUNNER_TIMEOUT_MS}" -ge 30000 && "${RUNNER_TIMEOUT_MS}" -le 600000 ]] || fail "--runner-timeout-ms must be between 30000 and 600000"

swarm_testbench_load_env "${ROOT_DIR}" || exit 1
swarm_testbench_validate_env || exit 1
LINKED_WORKSPACE_PATH="${LINKED_WORKSPACE_PATH:-${SWARM_TESTBENCH_LINKED_WORKSPACE_PATH:-}}"

if contains provider-sync "${SUITES[@]}"; then
  if [[ -z "${EXPECTED_COMMIT}" ]]; then
    EXPECTED_COMMIT="$(git -C "${ROOT_DIR}" rev-parse HEAD 2>/dev/null)" || fail "provider-sync could not resolve the local candidate commit; pass --expected-commit"
  fi
  if [[ -z "${REMOTE_REPO}" && "${DRY_RUN}" != "true" ]]; then
    REMOTE_REPO="$(swarm_testbench_discover_candidate_repo "${SWARM_PRIMARY_SSH}")" || fail "provider-sync could not discover the candidate checkout; pass --remote-repo"
  fi
fi

runner_args() {
  local -n out_ref="$1"
  out_ref=(--timeout-ms "${RUNNER_TIMEOUT_MS}")
  if [[ -n "${WORKSPACE_PATH}" ]]; then
    out_ref+=(--workspace-path "${WORKSPACE_PATH}")
  fi
}

suite_command() {
  local suite="$1"
  local -n output_ref="$2"
  local -a built=() args=()
  case "${suite}" in
    critical)
      built=("${ROOT_DIR}/scripts/run-critical-tests.sh" all)
      ;;
    onboarding)
      built=("${ROOT_DIR}/tests/swarmd/identity_bootstrap_e2e.sh")
      if [[ -n "${RUN_DIR:-}" ]]; then built+=("${RUN_DIR}/onboarding"); fi
      ;;
    desktop)
      built=("${ROOT_DIR}/scripts/run-testbench-desktop-e2e.sh" --timeout-ms "${DESKTOP_TIMEOUT_MS}")
      if [[ -n "${WORKSPACE}" ]]; then built+=(--workspace "${WORKSPACE}"); fi
      if [[ "${HEADFUL}" == "true" ]]; then built+=(--headful); fi
      ;;
    tui)
      built=("${ROOT_DIR}/scripts/run-testbench-tui-launch-e2e.sh" --timeout-seconds "${TUI_TIMEOUT_SECONDS}")
      ;;
    plan-auto)
      runner_args args
      built=("${ROOT_DIR}/scripts/run-testbench-runner.sh" basic-plan-auto "${args[@]}")
      ;;
    task-routing)
      runner_args args
      built=("${ROOT_DIR}/scripts/run-testbench-runner.sh" task-routing "${args[@]}")
      if [[ -n "${LINKED_WORKSPACE_PATH}" ]]; then built+=(--linked-workspace-path "${LINKED_WORKSPACE_PATH}"); fi
      ;;
    task-program)
      runner_args args
      built=("${ROOT_DIR}/scripts/run-testbench-runner.sh" task-program-worktrees "${args[@]}")
      if [[ -n "${LINKED_WORKSPACE_PATH}" ]]; then built+=(--linked-workspace-path "${LINKED_WORKSPACE_PATH}"); fi
      ;;
    provider-sync)
      built=(env SWARM_EXPECTED_COMMIT="${EXPECTED_COMMIT}" SWARM_REMOTE_REPO="${REMOTE_REPO}" SWARM_LIVE_STREAM_PROVIDER="${SWARM_TESTBENCH_PROVIDER}" SWARM_LIVE_STREAM_MODEL="${SWARM_TESTBENCH_ACTION_MODEL}" "${ROOT_DIR}/scripts/v3-sync-fireworks-e2e-testbench.sh" "${SWARM_PRIMARY_SSH}")
      ;;
    *) fail "unsupported suite ${suite}" ;;
  esac
  output_ref=("${built[@]}")
}

print_command() {
  local -a command=("$@")
  printf '  '
  printf '%q ' "${command[@]}"
  printf '\n'
}

printf 'canonical launch pre-run suites (%s, jobs=%s):\n' "${#SUITES[@]}" "${JOBS}"
for suite in "${SUITES[@]}"; do
  command=()
  suite_command "${suite}" command
  printf -- '- %s\n' "${suite}"
  if [[ "${DRY_RUN}" == "true" ]]; then print_command "${command[@]}"; fi
done
if [[ "${DRY_RUN}" == "true" ]]; then exit 0; fi

printf '\n== Preflight: testbench connectivity and configuration ==\n'
"${ROOT_DIR}/scripts/testbench-e2e-tunnel.sh" check

if [[ -n "${EVIDENCE_DIR}" ]]; then
  [[ "${EVIDENCE_DIR}" != /* && "${EVIDENCE_DIR}" != ".." && "${EVIDENCE_DIR}" != ../* && "${EVIDENCE_DIR}" != */../* && "${EVIDENCE_DIR}" != */.. ]] || fail "--evidence-dir must be a clean workspace-relative path"
  RUN_DIR="${ROOT_DIR}/${EVIDENCE_DIR}"
  mkdir -p -- "${RUN_DIR}"
else
  RUN_DIR="$(mktemp -d "${TMPDIR:?TMPDIR must be set}/swarm-launch-prerun.XXXXXX")"
fi
cleanup_children() {
  local job
  for job in $(jobs -pr 2>/dev/null || true); do
    kill "${job}" 2>/dev/null || true
  done
  wait 2>/dev/null || true
}
trap cleanup_children EXIT INT TERM
printf 'launch-prerun: evidence=%s\n' "${RUN_DIR}"

swarm_launch_prerun_run_lane() {
  local lane="$1" _log_path="$2"
  local -a command=()
  suite_command "${lane}" command
  printf 'command:'
  printf ' %q' "${command[@]}"
  printf '\n'
  "${command[@]}"
}

swarm_launch_prerun_run_parallel "${RUN_DIR}" "${JOBS}" "${SUITES[@]}"
