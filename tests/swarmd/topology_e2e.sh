#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"

usage() {
  cat <<'EOF'
Usage: ./tests/swarmd/topology_e2e.sh [options]

Run the real topology end-to-end bench against isolated local container swarms.
This is an orchestrator over the existing real harnesses; it does not seed fake
Pebble records or bypass runtime/API behavior.

Default scenarios:
  - local-basic: one real /v1/swarm/replicate child with full topology checks
  - local-cleanup: one real child deleted through APIs, proves no stale topology remains
  - local-multi: two real replicate children on one isolated host, proves counts/IDs
  - local-recovery: S4 restart recovery with topology assertions after each phase

Options:
  --scenario <local-basic|local-cleanup|local-multi|local-recovery|all>  Default: all
  --runtime <docker|podman>                                Runtime passed to local harnesses
  --workspace-path <path>                                  Source workspace. Default: repo root
  --host-root <path>                                       Reuse/create a specific isolated host root for local-basic/local-multi
  --replication-mode <bundle|copy>                         Replication mode forwarded to local harnesses
  --readonly                                               Replicate workspaces read-only
  --sync-enabled                                           Enable managed sync in local harnesses
  --host-vault-password-env <name>                         Forward host vault secret env var name
  --sync-vault-password-env <name>                         Forward child vault secret env var name
  --skip-host-rebuild                                      Reuse current host binaries
  --skip-image-rebuild                                     Reuse current child image
  --poll-timeout <seconds>                                 Attach timeout. Default: 120
  --poll-interval <seconds>                                Poll interval. Default: 2
  --log-tail <lines>                                       Log tail lines. Default: 200
  --help                                                   Show help

Artifacts:
  The bench writes a summary under <bench-root>/summary.json. Per-scenario
  artifacts remain under each underlying harness host root.
EOF
}

log() { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "${1:-}" >/dev/null 2>&1 || fail "required command not found: ${1:-}"; }

port_is_available() {
  local port="${1:-0}"
  if command -v ss >/dev/null 2>&1; then
    if ss -ltn "( sport = :${port} )" 2>/dev/null | awk 'NR > 1 { found = 1 } END { exit(found ? 0 : 1) }'; then
      return 1
    fi
    return 0
  fi
  fail "unable to check local port availability because ss is not installed"
}

reserve_port_pair() {
  local backend_port="${1:-7781}"
  local desktop_port="${2:-5555}"
  local attempts=0
  while (( attempts < 200 )); do
    if port_is_available "${backend_port}" \
      && port_is_available "$((backend_port + 1))" \
      && port_is_available "$((backend_port + 2))" \
      && port_is_available "${desktop_port}"; then
      RESERVED_BACKEND_PORT="${backend_port}"
      RESERVED_DESKTOP_PORT="${desktop_port}"
      return 0
    fi
    backend_port=$((backend_port + 3))
    desktop_port=$((desktop_port + 1))
    attempts=$((attempts + 1))
  done
  return 1
}

json_get() {
  local file="${1:-}"
  local query="${2:-}"
  [[ -f "${file}" ]] || fail "missing json file: ${file}"
  jq -r "${query}" "${file}"
}

run_local_replicate() {
  local host_root="${1:-}"
  local replicate_count="${2:-1}"
  shift 2
  local args=(
    "./tests/swarmd/local_replicate_e2e.sh"
    "--host-root" "${host_root}"
    "--host-port" "${RESERVED_BACKEND_PORT}"
    "--host-desktop-port" "${RESERVED_DESKTOP_PORT}"
    "--runtime" "${RUNTIME}"
    "--workspace-path" "${WORKSPACE_PATH}"
    "--replicate-count" "${replicate_count}"
    "--poll-timeout" "${POLL_TIMEOUT_SECONDS}"
    "--poll-interval" "${POLL_INTERVAL_SECONDS}"
    "--log-tail" "${LOG_TAIL}"
  )
  [[ -n "${REPLICATION_MODE}" ]] && args+=("--replication-mode" "${REPLICATION_MODE}")
  [[ "${WORKSPACE_WRITABLE}" == "false" ]] && args+=("--readonly")
  [[ "${SYNC_ENABLED}" == "true" ]] && args+=("--sync-enabled")
  [[ -n "${HOST_VAULT_PASSWORD_ENV}" ]] && args+=("--host-vault-password-env" "${HOST_VAULT_PASSWORD_ENV}")
  [[ -n "${SYNC_VAULT_PASSWORD_ENV}" ]] && args+=("--sync-vault-password-env" "${SYNC_VAULT_PASSWORD_ENV}")
  [[ "${REBUILD_HOST}" != "true" ]] && args+=("--skip-host-rebuild")
  [[ "${REBUILD_IMAGE}" != "true" ]] && args+=("--skip-image-rebuild")
  args+=("$@")
  (cd "${ROOT_DIR}" && "${args[@]}")
}

assert_multi_summary() {
  local summary_file="${1:-}"
  [[ -f "${summary_file}" ]] || fail "missing local-multi summary: ${summary_file}"
  local host_api child_count topology_json runtimes bindings host_containers attachments
  host_api="$(json_get "${summary_file}" '.host_api_url // empty')"
  child_count="$(json_get "${summary_file}" '.runs | map(.child_swarm_id) | length')"
  [[ -n "${host_api}" ]] || fail "local-multi summary is missing host_api_url"
  [[ "${child_count}" == "2" ]] || fail "local-multi expected 2 child_swarm_ids, got ${child_count}"
  topology_json="$(curl -sS --connect-timeout 3 --max-time 30 "${host_api%/}/v1/swarm/topology")"
  printf '%s' "${topology_json}" >"${BENCH_ROOT}/local-multi-topology.json"
  runtimes="$(printf '%s' "${topology_json}" | jq -r --slurpfile summary "${summary_file}" '[.runtimes[]? | select(.swarm_id as $id | ($summary[0].runs | map(.child_swarm_id) | index($id)))] | length')"
  bindings="$(printf '%s' "${topology_json}" | jq -r --slurpfile summary "${summary_file}" '[.workspace_bindings[]? | select(.destination_runtime_swarm_id as $id | ($summary[0].runs | map(.child_swarm_id) | index($id)))] | length')"
  host_containers="$(printf '%s' "${topology_json}" | jq -r '[.host_containers[]? | select(.host_container_id != "")] | length')"
  attachments="$(printf '%s' "${topology_json}" | jq -r --slurpfile summary "${summary_file}" '[.attachments[]? | select(.runtime_swarm_id as $id | ($summary[0].runs | map(.child_swarm_id) | index($id)))] | length')"
  [[ "${runtimes}" == "2" ]] || fail "local-multi topology runtime count=${runtimes}, expected 2"
  [[ "${bindings}" == "2" ]] || fail "local-multi topology binding count=${bindings}, expected 2"
  [[ "${attachments}" == "2" ]] || fail "local-multi topology attachment count=${attachments}, expected 2"
  [[ "${host_containers}" -ge 2 ]] || fail "local-multi topology host container count=${host_containers}, expected at least 2"
}

record_result() {
  local name="${1:-}"
  local status="${2:-}"
  local summary_file="${3:-}"
  jq -nc --arg name "${name}" --arg status "${status}" --arg summary_file "${summary_file}" '{name:$name,status:$status,summary_file:$summary_file}' >"${BENCH_ROOT}/${name}.json"
  RESULT_FILES+=("${BENCH_ROOT}/${name}.json")
}

scenario_local_basic() {
  log "== topology local-basic =="
  reserve_port_pair 7781 5555 || fail "unable to reserve ports for local-basic"
  local host_root="${HOST_ROOT_OVERRIDE:-$(mktemp -d "${TMPDIR:-/tmp}/swarm-topology-basic-XXXXXX")}"
  run_local_replicate "${host_root}" 1 --group-name "topology-basic-$(date +%Y%m%d-%H%M%S)"
  local summary_file="${host_root}/artifacts/summary.json"
  [[ -f "${summary_file}" ]] || fail "local-basic summary missing: ${summary_file}"
  record_result "local-basic" "PASS" "${summary_file}"
}

scenario_local_cleanup() {
  log "== topology local-cleanup =="
  reserve_port_pair 9781 7555 || fail "unable to reserve ports for local-cleanup"
  local host_root="$(mktemp -d "${TMPDIR:-/tmp}/swarm-topology-cleanup-XXXXXX")"
  run_local_replicate "${host_root}" 1 --group-name "topology-cleanup-$(date +%Y%m%d-%H%M%S)" --verify-topology-cleanup
  local summary_file="${host_root}/artifacts/summary.json"
  [[ -f "${summary_file}" ]] || fail "local-cleanup summary missing: ${summary_file}"
  [[ "$(json_get "${summary_file}" '.runs[0].verify_topology_cleanup // false')" == "true" ]] || fail "local-cleanup summary did not record verify_topology_cleanup=true"
  record_result "local-cleanup" "PASS" "${summary_file}"
}

scenario_local_multi() {
  log "== topology local-multi =="
  reserve_port_pair 8781 6555 || fail "unable to reserve ports for local-multi"
  local host_root
  if [[ -n "${HOST_ROOT_OVERRIDE}" && "${SCENARIO}" == "local-multi" ]]; then
    host_root="${HOST_ROOT_OVERRIDE}"
  else
    host_root="$(mktemp -d "${TMPDIR:-/tmp}/swarm-topology-multi-XXXXXX")"
  fi
  run_local_replicate "${host_root}" 2 --group-name "topology-multi-$(date +%Y%m%d-%H%M%S)"
  local summary_file="${host_root}/artifacts/summary.json"
  assert_multi_summary "${summary_file}"
  record_result "local-multi" "PASS" "${summary_file}"
}

scenario_local_recovery() {
  log "== topology local-recovery =="
  local recovery_root="$(mktemp -d "${TMPDIR:-/tmp}/swarm-topology-recovery-XXXXXX")"
  local args=(
    "./tests/swarmd/local_replicate_recovery_e2e.sh"
    "--host-root" "${recovery_root}"
    "--runtime" "${RUNTIME}"
    "--workspace-path" "${WORKSPACE_PATH}"
    "--group-name" "topology-recovery-$(date +%Y%m%d-%H%M%S)"
    "--attach-timeout" "${POLL_TIMEOUT_SECONDS}"
    "--poll-interval" "${POLL_INTERVAL_SECONDS}"
    "--log-tail" "${LOG_TAIL}"
  )
  [[ -n "${REPLICATION_MODE}" ]] && args+=("--replication-mode" "${REPLICATION_MODE}")
  [[ "${WORKSPACE_WRITABLE}" == "false" ]] && args+=("--readonly")
  [[ "${SYNC_ENABLED}" == "true" ]] && args+=("--sync-enabled")
  [[ -n "${HOST_VAULT_PASSWORD_ENV}" ]] && args+=("--host-vault-password-env" "${HOST_VAULT_PASSWORD_ENV}")
  [[ -n "${SYNC_VAULT_PASSWORD_ENV}" ]] && args+=("--sync-vault-password-env" "${SYNC_VAULT_PASSWORD_ENV}")
  [[ "${REBUILD_HOST}" != "true" ]] && args+=("--skip-host-rebuild")
  [[ "${REBUILD_IMAGE}" != "true" ]] && args+=("--skip-image-rebuild")
  (cd "${ROOT_DIR}" && "${args[@]}")
  local summary_file
  summary_file="$(find "${recovery_root}/recovery-artifacts" -name summary.json -type f | sort | tail -n 1)"
  [[ -n "${summary_file}" && -f "${summary_file}" ]] || fail "local-recovery summary missing under ${recovery_root}"
  record_result "local-recovery" "PASS" "${summary_file}"
}

write_summary() {
  jq -s --arg bench_root "${BENCH_ROOT}" --arg scenario "${SCENARIO}" '{bench_root:$bench_root,scenario:$scenario,results:.}' "${RESULT_FILES[@]}" >"${BENCH_ROOT}/summary.json"
  log ""
  log "Topology bench summary"
  jq . "${BENCH_ROOT}/summary.json"
}

SCENARIO="all"
RUNTIME="docker"
WORKSPACE_PATH="${ROOT_DIR}"
HOST_ROOT_OVERRIDE=""
REPLICATION_MODE=""
WORKSPACE_WRITABLE="true"
SYNC_ENABLED="false"
HOST_VAULT_PASSWORD_ENV=""
SYNC_VAULT_PASSWORD_ENV=""
REBUILD_HOST="true"
REBUILD_IMAGE="true"
POLL_TIMEOUT_SECONDS="120"
POLL_INTERVAL_SECONDS="2"
LOG_TAIL="200"
RESERVED_BACKEND_PORT=""
RESERVED_DESKTOP_PORT=""
BENCH_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/swarm-topology-bench-XXXXXX")"
RESULT_FILES=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --scenario) shift; [[ $# -gt 0 ]] || fail "--scenario requires a value"; SCENARIO="$1" ;;
    --runtime) shift; [[ $# -gt 0 ]] || fail "--runtime requires a value"; RUNTIME="$1" ;;
    --workspace-path) shift; [[ $# -gt 0 ]] || fail "--workspace-path requires a value"; WORKSPACE_PATH="$1" ;;
    --host-root) shift; [[ $# -gt 0 ]] || fail "--host-root requires a value"; HOST_ROOT_OVERRIDE="$1" ;;
    --replication-mode) shift; [[ $# -gt 0 ]] || fail "--replication-mode requires a value"; REPLICATION_MODE="$1" ;;
    --readonly) WORKSPACE_WRITABLE="false" ;;
    --sync-enabled) SYNC_ENABLED="true" ;;
    --host-vault-password-env) shift; [[ $# -gt 0 ]] || fail "--host-vault-password-env requires a value"; HOST_VAULT_PASSWORD_ENV="$1" ;;
    --sync-vault-password-env) shift; [[ $# -gt 0 ]] || fail "--sync-vault-password-env requires a value"; SYNC_VAULT_PASSWORD_ENV="$1" ;;
    --skip-host-rebuild) REBUILD_HOST="false" ;;
    --skip-image-rebuild) REBUILD_IMAGE="false" ;;
    --poll-timeout) shift; [[ $# -gt 0 ]] || fail "--poll-timeout requires a value"; POLL_TIMEOUT_SECONDS="$1" ;;
    --poll-interval) shift; [[ $# -gt 0 ]] || fail "--poll-interval requires a value"; POLL_INTERVAL_SECONDS="$1" ;;
    --log-tail) shift; [[ $# -gt 0 ]] || fail "--log-tail requires a value"; LOG_TAIL="$1" ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
  shift
done

case "${SCENARIO}" in
  local-basic|local-cleanup|local-multi|local-recovery|all) ;;
  *) fail "unknown scenario: ${SCENARIO}" ;;
esac
case "${RUNTIME}" in docker|podman) ;; *) fail "--runtime must be docker or podman" ;; esac
[[ -d "${WORKSPACE_PATH}" ]] || fail "workspace path does not exist: ${WORKSPACE_PATH}"
WORKSPACE_PATH="$(cd "${WORKSPACE_PATH}" && pwd)"
require_command jq
require_command curl
require_command ss

case "${SCENARIO}" in
  local-basic) scenario_local_basic ;;
  local-cleanup) scenario_local_cleanup ;;
  local-multi) scenario_local_multi ;;
  local-recovery) scenario_local_recovery ;;
  all)
    scenario_local_basic
    scenario_local_cleanup
    scenario_local_multi
    scenario_local_recovery
    ;;
esac

write_summary
log "Bench artifacts: ${BENCH_ROOT}"
