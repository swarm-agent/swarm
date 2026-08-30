#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=scripts/lib-launch-prerun.sh
source "${ROOT_DIR}/scripts/lib-launch-prerun.sh"

fail() {
  printf 'lib_launch_prerun_test: %s\n' "$*" >&2
  exit 1
}

TEST_ROOT="$(mktemp -d "${TMPDIR:?TMPDIR must be set}/launch-prerun-test.XXXXXX")"
trap 'rm -rf -- "${TEST_ROOT}"' EXIT
EVENTS="${TEST_ROOT}/events.ndjson"
: >"${EVENTS}"

swarm_launch_prerun_run_lane() {
  local lane="$1" _log_path="$2"
  local start end
  start="$(date +%s%N)"
  printf '{"lane":"%s","event":"start","ns":%s}\n' "${lane}" "${start}" >>"${EVENTS}"
  case "${lane}" in
    slow-a|slow-b) sleep 0.25 ;;
    fail-c) sleep 0.05 ;;
    *) sleep 0.02 ;;
  esac
  end="$(date +%s%N)"
  printf '{"lane":"%s","event":"end","ns":%s}\n' "${lane}" "${end}" >>"${EVENTS}"
  [[ "${lane}" != "fail-c" ]]
}

set +e
swarm_launch_prerun_run_parallel "${TEST_ROOT}/run" 2 slow-a slow-b fail-c >"${TEST_ROOT}/stdout" 2>"${TEST_ROOT}/stderr"
status=$?
set -e
[[ "${status}" == "1" ]] || fail "aggregate returned ${status}, want 1"
[[ "$(wc -l <"${TEST_ROOT}/run/summary.tsv")" == "3" ]] || fail "summary does not contain every suite"
grep -q $'^fail-c\t1\t' "${TEST_ROOT}/run/summary.tsv" || fail "summary does not preserve failing suite status"
grep -q $'^slow-a\t0\t' "${TEST_ROOT}/run/summary.tsv" || fail "summary does not preserve slow-a success"
grep -q $'^slow-b\t0\t' "${TEST_ROOT}/run/summary.tsv" || fail "summary does not preserve slow-b success"

read -r first_end starts_before_first_end < <(python3 - "${EVENTS}" <<'PY'
import json
import sys

events = [json.loads(line) for line in open(sys.argv[1], encoding='utf-8')]
first_end = min(item['ns'] for item in events if item['event'] == 'end')
starts = sum(1 for item in events if item['event'] == 'start' and item['ns'] < first_end)
print(first_end, starts)
PY
)
[[ "${starts_before_first_end}" -ge 2 ]] || fail "scheduler did not overlap the first two suites"

set +e
swarm_launch_prerun_validate_jobs 0 >/dev/null 2>&1
zero_status=$?
swarm_launch_prerun_validate_jobs 9 >/dev/null 2>&1
nine_status=$?
set -e
[[ "${zero_status}" != "0" && "${nine_status}" != "0" ]] || fail "invalid job limits were accepted"

EXPECTED_SUITES=$'critical\nonboarding\ndesktop\ntui\nplan-auto\ntask-routing\ntask-program\nprovider-sync'
ACTUAL_SUITES="$("${ROOT_DIR}/scripts/run-testbench-launch-prerun.sh" --list-suites)"
[[ "${ACTUAL_SUITES}" == "${EXPECTED_SUITES}" ]] || fail "canonical eight-suite manifest changed unexpectedly"
critical_dry_run="$("${ROOT_DIR}/scripts/run-testbench-launch-prerun.sh" --dry-run --suite critical)" || fail "critical lane dry run failed"
grep -Fq 'scripts/run-critical-tests.sh all' <<<"${critical_dry_run}" || fail "critical lane does not run the complete deterministic gate"
if ! "${ROOT_DIR}/scripts/run-testbench-launch-prerun.sh" --dry-run --suite task-program >/dev/null 2>&1; then
  fail "task-program lane still requires a manually configured linked workspace despite runtime binding discovery"
fi
provider_sync_dry_run="$("${ROOT_DIR}/scripts/run-testbench-launch-prerun.sh" --dry-run --suite provider-sync)" || fail "provider-sync dry run still requires manually supplied candidate authority"
grep -Fq 'SWARM_LIVE_STREAM_PROVIDER=fireworks' <<<"${provider_sync_dry_run}" || fail "provider-sync is not Fireworks-only"
grep -Fq 'SWARM_LIVE_STREAM_MODEL=accounts/fireworks/models/deepseek-v4-flash-0731' <<<"${provider_sync_dry_run}" || fail "provider-sync does not use the configured Fireworks action model"
RUNNER_WRAPPER="${ROOT_DIR}/scripts/run-testbench-runner.sh"
grep -Fq 'SWARM_TESTBENCH_ACTION_MODEL' "${RUNNER_WRAPPER}" || fail "runner wrapper does not pass the configured Action model"
grep -Fq 'SWARM_TESTBENCH_PLAN_MODEL' "${RUNNER_WRAPPER}" || fail "runner wrapper does not pass the configured Plan model"
grep -Fq 'SWARM_TESTBENCH_CODER_MODEL' "${RUNNER_WRAPPER}" || fail "runner wrapper does not pass the configured Coder model"
grep -Fq 'SWARM_TESTBENCH_DESIGNER_MODEL' "${RUNNER_WRAPPER}" || fail "runner wrapper does not pass the configured Designer model"
BASIC_RUNNER="${ROOT_DIR}/scripts/runners/basic-plan-auto.mjs"
TASK_ROUTING_RUNNER="${ROOT_DIR}/scripts/runners/task-routing.mjs"
TASK_PROGRAM_RUNNER="${ROOT_DIR}/scripts/runners/task-program-worktrees.mjs"
for runner in "${BASIC_RUNNER}" "${TASK_ROUTING_RUNNER}"; do
  grep -Fq -- '--action-model and --plan-model are required' "${runner}" || fail "$(basename "${runner}") does not fail closed without explicit Action/Plan models"
  if grep -Fq 'recommendedAssignment' "${runner}"; then fail "$(basename "${runner}") still discovers model recommendations"; fi
done
grep -Fq -- '--coder-model is required' "${TASK_PROGRAM_RUNNER}" || fail "task-program runner does not fail closed without the configured Coder model"
if grep -Fq "includes('5.6-luna')" "${TASK_PROGRAM_RUNNER}"; then fail "task-program runner still discovers a model fallback"; fi
MODEL_RENDERER="${ROOT_DIR}/scripts/render-testbench-model-config.sh"
MODEL_SYNC="${ROOT_DIR}/scripts/sync-testbench-model-config.sh"
grep -Fq 'SWARM_RELEASE_GATE_PROVIDER=${SWARM_TESTBENCH_PROVIDER}' "${MODEL_RENDERER}" || fail "model renderer does not use the .env provider"
grep -Fq 'SWARM_RELEASE_GATE_ACTION_MODEL=${SWARM_TESTBENCH_ACTION_MODEL}' "${MODEL_RENDERER}" || fail "model renderer does not use the .env Action model"
grep -Fq 'SWARM_RELEASE_GATE_PLAN_MODEL=${SWARM_TESTBENCH_PLAN_MODEL}' "${MODEL_RENDERER}" || fail "model renderer does not use the .env Plan model"
grep -Fq 'gate-sync-models' "${MODEL_SYNC}" || fail "model sync does not invoke the fixed testbench action"
grep -Fq '/var/cache/swarm-testbench-model-config/pending.conf' "${MODEL_SYNC}" || fail "model sync does not use the fixed testbench pending path"
local_head="$(git -C "${ROOT_DIR}" rev-parse HEAD)"
grep -Fq "SWARM_EXPECTED_COMMIT=${local_head}" <<<"${provider_sync_dry_run}" || fail "provider-sync did not derive candidate authority from local HEAD"

ONBOARDING_GATE="${ROOT_DIR}/tests/swarmd/identity_bootstrap_e2e.sh"
if grep -Fq 'npm test -- --run' "${ONBOARDING_GATE}" ||
   grep -Fq 'npm run build' "${ONBOARDING_GATE}" ||
   grep -Fq "Test(Session|Desktop|Local|Auth|Protected|JWT|Onboarding)" "${ONBOARDING_GATE}"; then
  fail "onboarding lane reintroduced broad frontend or API package tests"
fi
grep -Fq 'go build -o' "${ONBOARDING_GATE}" || fail "onboarding lane no longer builds the isolated daemon"
grep -Fq '/v1/onboarding' "${ONBOARDING_GATE}" || fail "onboarding lane no longer exercises the onboarding API"
grep -Fq 'auth-session-after-restart' "${ONBOARDING_GATE}" || fail "onboarding lane no longer proves restart persistence"

DESKTOP_GATE="${ROOT_DIR}/scripts/run-testbench-desktop-e2e.sh"
grep -Fq 'PLAYWRIGHT_BROWSER_EXECUTABLE' "${DESKTOP_GATE}" || fail "Desktop lane no longer supports the installed system browser"
grep -Fq 'google-chrome-stable google-chrome chromium chromium-browser' "${DESKTOP_GATE}" || fail "Desktop lane browser fallback order changed unexpectedly"

TUI_GATE="${ROOT_DIR}/tests/testbench_tui_pty_e2e.sh"
grep -Fq "messages.snapshot.final" "${TUI_GATE}" || fail "TUI lane no longer captures its authoritative final timeline from durable messages"
if grep -Fq "feedPTY('/copy\\r', 'capture authoritative final chat snapshot')" "${TUI_GATE}"; then
  fail "TUI lane reintroduced clipboard-only final timeline evidence"
fi

printf 'lib_launch_prerun_test: PASS\n'
