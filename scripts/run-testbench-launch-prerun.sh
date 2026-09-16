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
  installed-new-user      installed root-created human account and resumable TUI
  installed-existing-user installed root-selected human account and preservation
  installed-normal-user   installed sudo-assisted human account and preservation
  desktop       real Desktop /new, /task, worktree, Plan-to-Auto lifecycle gate
  tui           real TUI /new, /task, worktree, and Plan launch gate
  plan-auto     API Plan-to-Auto two-checkpoint lifecycle runner
  task-routing  API /task Auto/Plan routing from current, explicit saved, and existing sessions
  task-program  live same/linked-repo Task Programs across both parent modes
  provider-sync signed sync/realtime repair plus one real provider response
                (uses the configured testbench provider/model; despite the
                legacy underlying script name, Fireworks is not hard-required)
  omarchy-install exact candidate install/start/CLI proof on a clean Omarchy VM overlay
                (explicit opt-in; requires --omarchy-guest)

Options:
  --attach-only <url>        Reuse this exact loopback Desktop root; no .env or deployment
  --suite attach-inspect     Bounded authenticated read-only connection proof
  --suite workspace-routing  Owned fixture/provider stage (requires SWARM_ATTACH_FIXTURE_PARENT)
  --suite workspace-workers  Owned two-worker stage (requires SWARM_ATTACH_FIXTURE_PARENT)
  --suite workspace-safety   Focused local identity/CAS/rooted-I/O/reopen proofs
  --suite workspace-browser  Hermetic picker/reconnect fixture (not full-app proof)
  --wall-seconds <n>         Suite wall deadline (1..600; default: 600)
  --stall-seconds <n>        No-output deadline (1..wall; default: 120)
  --output-bytes <n>         Per-suite log cap (1024..4194304; default: 1048576)
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
  --installed-onboarding-runner <path>
                             Reviewed Python installed proof runner (required for installed suites)
  --candidate-archive <path> Exact release archive for installed suites / omarchy-install
  --candidate-checksum <path> Matching SHA-256 sidecar for installed suites / omarchy-install
  --omarchy-guest <user@host> Clean official-ISO Omarchy VM SSH target
  --omarchy-port <n>         Optional Omarchy VM SSH port
  --omarchy-identity <path>  Optional Omarchy VM SSH identity
  --evidence-dir <path>      Preserve aggregate logs at this ignored path
  --headful                  Show the Desktop Playwright browser
  -h, --help                 Show this help

Attach-only requires explicit suites and rejects legacy suites that change shared
settings, allocate tunnels, or select ambient sessions. Local safety/browser and
read-only inspect suites are admitted. Routing requires an explicit reviewed
SWARM_ATTACH_FIXTURE_PARENT for unique disposable repositories; worker admission
uses exact task permission matching and terminal child verification. Later isolated scenarios must be explicitly reviewed
in this same manifest. Authentication stays in process memory. Build and lane
ownership require separate read-only broker evidence; HTTP 200 is not proof.

Outside attach-only, the ignored .env remains the authority for the SSH alias, container loopback
ports, provider/per-role model posture, and optional linked workspace path. Before
non-dry execution this entrypoint checks that the broker-owned isolated container runs
exact current HEAD. It never deploys or restarts host swarm.service, commits,
pushes, or mutates production. Provider-sync still performs its own candidate
checkout evidence after the shared container preflight.
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

DEFAULT_SUITES=(critical onboarding installed-new-user installed-existing-user installed-normal-user desktop tui plan-auto task-routing task-program provider-sync)
ALL_SUITES=("${DEFAULT_SUITES[@]}" omarchy-install attach-inspect workspace-routing workspace-workers workspace-safety workspace-browser)
ATTACH_URL=""
WALL_SECONDS=600
STALL_SECONDS=120
OUTPUT_BYTES=1048576
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
CANDIDATE_ARCHIVE="${SWARM_INSTALL_CANDIDATE_ARCHIVE:-}"
CANDIDATE_CHECKSUM="${SWARM_INSTALL_CANDIDATE_CHECKSUM:-}"
INSTALLED_ONBOARDING_RUNNER=""
OMARCHY_GUEST=""
OMARCHY_PORT=""
OMARCHY_IDENTITY=""
EVIDENCE_DIR=""
SELECTED=()
SKIPPED=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --attach-only) [[ $# -ge 2 ]] || fail "--attach-only requires a URL"; ATTACH_URL="$2"; shift 2 ;;
    --wall-seconds) [[ $# -ge 2 ]] || fail "--wall-seconds requires a value"; WALL_SECONDS="$2"; shift 2 ;;
    --stall-seconds) [[ $# -ge 2 ]] || fail "--stall-seconds requires a value"; STALL_SECONDS="$2"; shift 2 ;;
    --output-bytes) [[ $# -ge 2 ]] || fail "--output-bytes requires a value"; OUTPUT_BYTES="$2"; shift 2 ;;
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
    --installed-onboarding-runner) [[ $# -ge 2 ]] || fail "--installed-onboarding-runner requires a path"; INSTALLED_ONBOARDING_RUNNER="$2"; shift 2 ;;
    --candidate-archive) [[ $# -ge 2 ]] || fail "--candidate-archive requires a value"; CANDIDATE_ARCHIVE="$2"; shift 2 ;;
    --candidate-checksum) [[ $# -ge 2 ]] || fail "--candidate-checksum requires a value"; CANDIDATE_CHECKSUM="$2"; shift 2 ;;
    --omarchy-guest) [[ $# -ge 2 ]] || fail "--omarchy-guest requires a value"; OMARCHY_GUEST="$2"; shift 2 ;;
    --omarchy-port) [[ $# -ge 2 ]] || fail "--omarchy-port requires a value"; OMARCHY_PORT="$2"; shift 2 ;;
    --omarchy-identity) [[ $# -ge 2 ]] || fail "--omarchy-identity requires a value"; OMARCHY_IDENTITY="$2"; shift 2 ;;
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

if [[ -n "${ATTACH_URL}" && ${#SELECTED[@]} == 0 ]]; then fail "attach-only requires explicit --suite selections"; fi
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

[[ "${WALL_SECONDS}" =~ ^[1-9][0-9]*$ && "${WALL_SECONDS}" -le 600 ]] || fail "invalid wall deadline"
[[ "${STALL_SECONDS}" =~ ^[1-9][0-9]*$ && "${STALL_SECONDS}" -le "${WALL_SECONDS}" ]] || fail "invalid stall deadline"
[[ "${OUTPUT_BYTES}" =~ ^[1-9][0-9]*$ && "${OUTPUT_BYTES}" -ge 1024 && "${OUTPUT_BYTES}" -le 4194304 ]] || fail "invalid output cap"
export SWARM_LAUNCH_WALL_SECONDS="${WALL_SECONDS}" SWARM_LAUNCH_STALL_SECONDS="${STALL_SECONDS}" SWARM_LAUNCH_OUTPUT_BYTES="${OUTPUT_BYTES}"
if [[ -n "${ATTACH_URL}" ]]; then
  node --input-type=module -e 'import {pathToFileURL} from "node:url"; const {desktopOrigin}=await import(pathToFileURL(process.argv[2])); desktopOrigin(process.argv[3])' validate-only "${ROOT_DIR}/scripts/testbench-attach.mjs" "${ATTACH_URL}"
  for suite in "${SUITES[@]}"; do
    case "${suite}" in
      workspace-routing) [[ -n "${SWARM_ATTACH_FIXTURE_PARENT:-}" ]] || fail "workspace-routing is not attach-safe without explicit SWARM_ATTACH_FIXTURE_PARENT; no connection or mutation attempted" ;;
      workspace-workers) [[ -n "${SWARM_ATTACH_FIXTURE_PARENT:-}" ]] || fail "workspace-workers is not attach-safe without explicit SWARM_ATTACH_FIXTURE_PARENT; no connection or mutation attempted" ;;
      critical|attach-inspect|workspace-safety|workspace-browser) ;; *) fail "suite ${suite} is not attach-safe; no connection or mutation attempted" ;; esac
  done
  [[ -z "${WORKSPACE}${WORKSPACE_PATH}${LINKED_WORKSPACE_PATH}${REMOTE_REPO}${CANDIDATE_ARCHIVE}${CANDIDATE_CHECKSUM}${OMARCHY_GUEST}" ]] || fail "attach-only rejects legacy workspace/deployment overrides"
  export SWARM_TESTBENCH_ATTACH_ONLY=1 SWARM_DESKTOP_URL="${ATTACH_URL}" SWARM_RUNNER_API_URL="${ATTACH_URL%/}" SWARM_PRIMARY_API_URL="${ATTACH_URL%/}"
else
  for suite in attach-inspect workspace-routing workspace-workers workspace-safety workspace-browser; do
    contains "${suite}" "${SUITES[@]}" && fail "${suite} requires --attach-only"
  done
  NEEDS_TESTBENCH=false
  for suite in "${SUITES[@]}"; do
    case "$suite" in desktop|tui|plan-auto|task-routing|task-program|provider-sync) NEEDS_TESTBENCH=true ;; esac
  done
  if [[ "$NEEDS_TESTBENCH" == true ]]; then
    swarm_testbench_load_env "${ROOT_DIR}" || exit 1
    swarm_testbench_validate_env || exit 1
  fi
fi
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
    attach-inspect)
      built=(node "${ROOT_DIR}/scripts/testbench-attach.mjs" "${ATTACH_URL}")
      ;;
    workspace-routing)
      built=(node "${ROOT_DIR}/scripts/runners/workspace-launch.mjs" "${RUN_DIR:-${TMPDIR}}/workspace-routing.json")
      ;;
    workspace-workers)
      built=(node "${ROOT_DIR}/scripts/runners/workspace-launch.mjs" "${RUN_DIR:-${TMPDIR}}/workspace-workers.json" regular)
      ;;
    workspace-safety)
      built=(env GOMAXPROCS=2 go -C "${ROOT_DIR}/swarmd" test -p 2 ./internal/run ./internal/tool -run '^(TestMultiWorkspaceIdentityTransitions|TestWorkspaceLaunchLaterCohortDenied|TestWorkspaceLaunchPostOpenSubstitution|TestWorkspaceLaunchAcknowledgedMutationProcessExit|TestWorkspaceTargetFilesystemAuthority|TestWorkspaceTargetSearchSelection)$' -count=2 -timeout=90s)
      ;;
    workspace-browser)
      built=(bash -c 'cd "$1" && exec env SWARM_TEST_BROWSER_CHANNEL="${SWARM_TEST_BROWSER_CHANNEL:-chrome}" SWARM_TEST_SCREENSHOT_DIR="$2" node --import tsx --test --test-concurrency=1 src/features/desktop/git/workspace-launch.browser.spec.ts src/features/desktop/chat/components/session-attachments.browser.spec.ts' workspace-browser "${ROOT_DIR}/web" "${RUN_DIR:-${TMPDIR}}")
      ;;
    critical)
      built=(env GOMAXPROCS=2 GOFLAGS=-p=2 "${ROOT_DIR}/scripts/run-critical-tests.sh" all)
      ;;
    onboarding)
      built=("${ROOT_DIR}/tests/swarmd/identity_bootstrap_e2e.sh")
      if [[ -n "${RUN_DIR:-}" ]]; then built+=("${RUN_DIR}/onboarding"); fi
      ;;
    installed-new-user|installed-existing-user|installed-normal-user)
      [[ -n "$INSTALLED_ONBOARDING_RUNNER" && -f "$INSTALLED_ONBOARDING_RUNNER" && ! -L "$INSTALLED_ONBOARDING_RUNNER" ]] || fail "installed suites require --installed-onboarding-runner (reviewed regular Python file)"
      [[ -n "$CANDIDATE_ARCHIVE" && -n "$CANDIDATE_CHECKSUM" ]] || fail "installed suites require --candidate-archive and --candidate-checksum"
      built=(python3 "$INSTALLED_ONBOARDING_RUNNER" --archive "$CANDIDATE_ARCHIVE" --checksum "$CANDIDATE_CHECKSUM" --case "${suite#installed-}")
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
    omarchy-install)
      [[ -n "${CANDIDATE_ARCHIVE}" ]] || fail "omarchy-install requires --candidate-archive"
      [[ -n "${CANDIDATE_CHECKSUM}" ]] || fail "omarchy-install requires --candidate-checksum"
      [[ -n "${OMARCHY_GUEST}" ]] || fail "omarchy-install requires --omarchy-guest"
      built=("${ROOT_DIR}/scripts/test-install-omarchy-vm.sh" --archive "${CANDIDATE_ARCHIVE}" --checksum "${CANDIDATE_CHECKSUM}" --guest "${OMARCHY_GUEST}")
      if [[ -n "${OMARCHY_PORT}" ]]; then built+=(--port "${OMARCHY_PORT}"); fi
      if [[ -n "${OMARCHY_IDENTITY}" ]]; then built+=(--identity "${OMARCHY_IDENTITY}"); fi
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


if [[ -n "${EVIDENCE_DIR}" ]]; then
  [[ "${EVIDENCE_DIR}" != /* && "${EVIDENCE_DIR}" != ".." && "${EVIDENCE_DIR}" != ../* && "${EVIDENCE_DIR}" != */../* && "${EVIDENCE_DIR}" != */.. ]] || fail "--evidence-dir must be a clean workspace-relative path"
  RUN_DIR="${ROOT_DIR}/${EVIDENCE_DIR}"
  resolved_evidence="$(realpath -m -- "${RUN_DIR}")"
  [[ "${resolved_evidence}" == "${ROOT_DIR}/"* ]] || fail "evidence directory escapes workspace through symlink"
  git -C "${ROOT_DIR}" check-ignore -q -- "${EVIDENCE_DIR}/evidence-probe" || fail "evidence directory must be ignored"
  [[ ! -e "${RUN_DIR}" ]] || fail "evidence directory already exists; choose a fresh run directory"
  mkdir -m 700 -p -- "${RUN_DIR}"
else
  RUN_DIR="$(mktemp -d "${TMPDIR:?TMPDIR must be set}/swarm-launch-prerun.XXXXXX")"
fi
umask 077
if [[ -n "${ATTACH_URL}" ]]; then
  printf '\n== Preflight: read-only attach to existing Desktop ==\n'
  node "${ROOT_DIR}/scripts/testbench-attach.mjs" "${ATTACH_URL}" >"${RUN_DIR}/connection.json"
  # Pin identity once for every child; credentials are never exported or persisted.
  SWARM_ATTACH_EXPECTED_RUNTIME="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["runtime_id"])' "${RUN_DIR}/connection.json")"
  SWARM_ATTACH_EXPECTED_SETTINGS="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["settings_sha256"])' "${RUN_DIR}/connection.json")"
  export SWARM_ATTACH_EXPECTED_RUNTIME SWARM_ATTACH_EXPECTED_SETTINGS
elif [[ "${NEEDS_TESTBENCH:-false}" == true ]]; then
  printf '\n== Preflight: check exact isolated-container candidate ==\n'
  "${ROOT_DIR}/scripts/testbench-e2e-tunnel.sh" check
fi
printf 'launch-prerun: evidence=%s\n' "${RUN_DIR}"

swarm_launch_prerun_lane_cleanup_seconds() {
  case "$1" in
    workspace-routing|workspace-workers) printf '25\n' ;;
    installed-new-user|installed-existing-user|installed-normal-user) printf '25\n' ;;
    *) printf '0.5\n' ;;
  esac
}

swarm_launch_prerun_lane_command() {
  suite_command "$1" "$2"
}

swarm_launch_prerun_run_parallel "${RUN_DIR}" "${JOBS}" "${SUITES[@]}"
