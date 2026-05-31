#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"

usage() {
  cat <<'EOF'
Usage: ./tests/swarmd/managed_host_launch_gate_e2e.sh [options]

Managed-host-only Launch Gate harness. It drives the primary swarmd API and
never uses the remote deploy or remote shell product paths. For real testbench
runs, execute this script on SwarmTarget1 over SSH (or on SwarmTarget1 itself); do
not run it from the developer workstation.

Implemented checkpoints:
  0  Testbench sanity: primary ready, managed target online/selectable, topology
     binding present, mirror endpoint healthy, optional managed-container cleanup,
     and non-destructive target picker select/restore.
  1  Harness extension: create repeatable evidence directory, matrix status, and
     harness metadata.
  2  Managed host AI: open/message/run through primary managed-host APIs, then
     prove primary mirror and primary topology route. Cold managed DB proof is
     checkpoint 3 after the managed daemon is stopped.
  4  Managed container create: create a child container hosted on the managed
     host through primary /v1/swarm/replicate, prove no primary-local fallback,
     and capture exact primary topology/router/mirror identities.
  5  Managed container CRUD: update settings, stop/start, delete, prove topology
     cleanup, then recreate through the primary API.
  6  Managed container AI: create a managed-host child container, open/message/run
     a session through primary routed session APIs, verify proof token, topology,
     mirror, and route cleanup.
  7  Flow A: create/run a primary self-target flow and verify status/history,
     session report, and assistant proof token on primary.
  8  Flow B: create/run a primary local-container flow, verify route/report/history,
     then disable/unassign before deleting the container and verify cleanup.
  9  Flow C: create/run a managed-host main flow via primary flow APIs and verify
     managed execution report/history/session evidence returned to primary.
  10 Flow D: create/run a managed-host container flow, verify attribution/report,
     then disable/unassign before deleting the managed container and verify cleanup.

Options:
  --scenario <0|1|2|4|5|6|7|8|9|10|0-1|0-2|0-4|0-5|0-6|7-10|0-10|all>    Default: 0-1
  --primary-url <url>                      Primary swarmd URL. Or SWARM_PRIMARY_URL.
                                           Default for SSH testbench: http://127.0.0.1:7781
  --managed-swarm-id <id>                  Managed host swarm_id. Or SWARM_MANAGED_SWARM_ID.
  --managed-name <name>                    Managed host name if id is unknown. Or SWARM_MANAGED_NAME.
  --source-workspace-path <path>           Primary source workspace path to require. Or SWARM_SOURCE_WORKSPACE_PATH.
  --managed-workspace-path <path>          Managed-host runtime workspace path. Or SWARM_MANAGED_WORKSPACE_PATH.
                                           Defaults to --source-workspace-path.
  --provider <provider>                    Model provider for checkpoint 2. Or SWARM_PROVIDER.
  --model <model>                          Model for checkpoint 2. Or SWARM_MODEL.
  --thinking <level>                       Thinking setting for checkpoint 2. Or SWARM_THINKING. Default: low.
  --prompt <text>                          Prompt for checkpoint 2. Or SWARM_LAUNCH_GATE_PROMPT.
  --container-name <name>                  Child container/swarm name for checkpoints 4/5. Or SWARM_LAUNCH_GATE_CONTAINER_NAME.
  --cp5-container-name <name>              Child container/swarm name for checkpoint 5. Or SWARM_LAUNCH_GATE_CP5_CONTAINER_NAME.
  --cp5-recreate-container-name <name>     Recreated child name for checkpoint 5. Or SWARM_LAUNCH_GATE_CP5_RECREATE_CONTAINER_NAME.
  --cp6-container-name <name>              Child container/swarm name for checkpoint 6. Or SWARM_LAUNCH_GATE_CP6_CONTAINER_NAME.
  --cp6-prompt <text>                      Prompt for checkpoint 6. Or SWARM_LAUNCH_GATE_CP6_PROMPT.
  --flow-agent-name <name>                 Agent profile used by checkpoints 7-10. Default: swarm.
  --flow-agent-mode <mode>                 Agent profile mode used by checkpoints 7-10. Default: primary.
  --cp8-container-name <name>              Primary local child name for checkpoint 8.
  --cp10-container-name <name>             Managed-host child name for checkpoint 10.
  --flow-timeout-seconds <seconds>         Flow run wait timeout for checkpoints 7-10. Default: 360.
  --runtime <podman|docker>                Container runtime for checkpoints 4/5. Optional; managed host default is used when empty.
  --evidence-dir <path>                    Evidence directory. Default: tmp launch-gate directory.
  --cleanup-existing-managed-containers    Delete managed-host containers seen in primary topology via primary API.
  --allow-existing-managed-containers      Do not fail checkpoint 0 when managed containers already exist.
  --allow-existing-managed-session-routes  Do not fail checkpoint 0 when managed session routes already exist.
  --skip-target-picker                     Skip select managed target + restore original target proof.
  --allow-local-workstation-run            Allow running from a non-SSH/testbench context.
  --help                                   Show help.

Environment shortcuts:
  SWARM_PRIMARY_URL, SWARM_MANAGED_SWARM_ID, SWARM_MANAGED_NAME,
  SWARM_SOURCE_WORKSPACE_PATH, SWARM_MANAGED_WORKSPACE_PATH,
  SWARM_PROVIDER, SWARM_MODEL, SWARM_THINKING, SWARM_LAUNCH_GATE_PROMPT,
  SWARM_LAUNCH_GATE_CONTAINER_NAME, SWARM_LAUNCH_GATE_CP5_CONTAINER_NAME,
  SWARM_LAUNCH_GATE_CP5_RECREATE_CONTAINER_NAME, SWARM_LAUNCH_GATE_CP6_CONTAINER_NAME,
  SWARM_LAUNCH_GATE_CP6_PROMPT, SWARM_LAUNCH_GATE_RUNTIME,
  SWARM_LAUNCH_GATE_FLOW_AGENT_NAME, SWARM_LAUNCH_GATE_FLOW_AGENT_MODE,
  SWARM_LAUNCH_GATE_CP8_CONTAINER_NAME, SWARM_LAUNCH_GATE_CP10_CONTAINER_NAME,
  SWARM_LAUNCH_GATE_FLOW_TIMEOUT_SECONDS,
  SWARM_LAUNCH_GATE_EVIDENCE_DIR, SWARM_LAUNCH_GATE_ALLOW_LOCAL

Artifacts:
  <evidence-dir>/matrix_status.json
  <evidence-dir>/checkpoint-0/*.json
  <evidence-dir>/checkpoint-1/*.json
  <evidence-dir>/checkpoint-2/*.json
  <evidence-dir>/checkpoint-4/*.json
  <evidence-dir>/checkpoint-5/*.json
  <evidence-dir>/checkpoint-6/*.json
  <evidence-dir>/checkpoint-7/*.json
  <evidence-dir>/checkpoint-8/*.json
  <evidence-dir>/checkpoint-9/*.json
  <evidence-dir>/checkpoint-10/*.json
EOF
}

log() { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "${1:-}" >/dev/null 2>&1 || fail "required command not found: ${1:-}"; }

urlencode() {
  jq -rn --arg v "${1:-}" '$v|@uri'
}

json_file_get() {
  local file="${1:-}"
  local query="${2:-}"
  [[ -f "${file}" ]] || fail "missing json file: ${file}"
  jq -r "${query}" "${file}"
}

require_testbench_execution() {
  if [[ "${ALLOW_LOCAL_WORKSTATION_RUN}" == "true" ]]; then
    return 0
  fi
  local host
  host="$(hostname -s 2>/dev/null || hostname 2>/dev/null || true)"
  case "${host}" in
    SwarmTarget1|SwarmTarget1-*) return 0 ;;
  esac
  fail "Launch Gate harness must run on the SSH testbench host, not the local workstation. Use: ssh SwarmTarget1 'cd ${SWARM_GO_ROOT:-/path/to/swarm-go} && ./tests/swarmd/managed_host_launch_gate_e2e.sh ...'"
}

init_evidence() {
  if [[ -z "${EVIDENCE_DIR}" ]]; then
    EVIDENCE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/swarm-managed-launch-gate-XXXXXX")"
  fi
  mkdir -p -- "${EVIDENCE_DIR}"
  STATUS_FILE="${EVIDENCE_DIR}/matrix_status.json"
  if [[ ! -f "${STATUS_FILE}" ]]; then
    jq -nc \
      --arg created_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
      --arg primary_url "${PRIMARY_URL}" \
      --arg managed_swarm_id "${MANAGED_SWARM_ID}" \
      --arg managed_name "${MANAGED_NAME}" \
      --arg runner_host "$(hostname -f 2>/dev/null || hostname 2>/dev/null || true)" \
      '{created_at:$created_at, primary_url:$primary_url, managed_swarm_id:$managed_swarm_id, managed_name:$managed_name, runner_host:$runner_host, checkpoints:[]}' \
      >"${STATUS_FILE}"
  fi
}

record_checkpoint() {
  local id="${1:-}"
  local status="${2:-}"
  local summary="${3:-}"
  local artifact_dir="${4:-}"
  local tmp
  tmp="$(mktemp)"
  jq \
    --arg id "${id}" \
    --arg status "${status}" \
    --arg summary "${summary}" \
    --arg artifact_dir "${artifact_dir}" \
    --arg updated_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '.checkpoints = ((.checkpoints // []) | map(select(.id != $id)) + [{id:$id,status:$status,summary:$summary,artifact_dir:$artifact_dir,updated_at:$updated_at}])' \
    "${STATUS_FILE}" >"${tmp}"
  mv -- "${tmp}" "${STATUS_FILE}"
}

start_desktop_session() {
  COOKIE_FILE="${EVIDENCE_DIR}/primary.cookies"
  : >"${COOKIE_FILE}"
  curl -fsS --connect-timeout 3 --max-time 20 \
    -c "${COOKIE_FILE}" \
    -b "${COOKIE_FILE}" \
    -H "Origin: ${PRIMARY_URL%/}" \
    -H "Referer: ${PRIMARY_URL%/}/" \
    -H 'Sec-Fetch-Site: same-origin' \
    "${PRIMARY_URL%/}/v1/auth/desktop/session" >/dev/null
}

api_request_capture() {
  local method="${1:-GET}"
  local path="${2:-}"
  local body="${3:-}"
  local output_file="${4:-}"
  local max_time="${5:-30}"
  local url="${PRIMARY_URL%/}${path}"
  local response_file payload_file http_code
  response_file="$(mktemp)"
  payload_file=""
  local args=(
    -sS
    --connect-timeout 3
    --max-time "${max_time}"
    -o "${response_file}"
    -w '%{http_code}'
    -H 'Accept: application/json'
    -H "Origin: ${PRIMARY_URL%/}"
    -H "Referer: ${PRIMARY_URL%/}/"
    -H 'Sec-Fetch-Site: same-origin'
    -c "${COOKIE_FILE}"
    -b "${COOKIE_FILE}"
    -X "${method}"
  )
  if [[ -n "${body}" ]]; then
    payload_file="$(mktemp)"
    printf '%s' "${body}" >"${payload_file}"
    args+=(-H 'Content-Type: application/json' --data-binary "@${payload_file}")
  fi
  if http_code="$(curl "${args[@]}" "${url}")"; then
    :
  else
    http_code="000"
  fi
  if [[ -n "${output_file}" ]]; then
    cp -- "${response_file}" "${output_file}"
  fi
  API_STATUS="${http_code}"
  API_BODY="$(cat -- "${response_file}")"
  rm -f -- "${response_file}"
  [[ -z "${payload_file}" ]] || rm -f -- "${payload_file}"
}

api_json() {
  local method="${1:-GET}"
  local path="${2:-}"
  local body="${3:-}"
  local output_file="${4:-}"
  local max_time="${5:-30}"
  api_request_capture "${method}" "${path}" "${body}" "${output_file}" "${max_time}"
  if [[ "${API_STATUS}" != 2* ]]; then
    fail "${method} ${path} failed with HTTP ${API_STATUS}: ${API_BODY}"
  fi
  if [[ -n "${output_file}" ]]; then
    jq empty "${output_file}" >/dev/null
  fi
}

readyz_capture() {
  local output_file="${1:-}"
  local code body_file
  body_file="$(mktemp)"
  if code="$(curl -sS --connect-timeout 3 --max-time 20 -o "${body_file}" -w '%{http_code}' "${PRIMARY_URL%/}/readyz")"; then
    :
  else
    code="000"
  fi
  jq -nc --arg http_code "${code}" --rawfile body "${body_file}" '{http_code:$http_code, body:$body}' >"${output_file}"
  rm -f -- "${body_file}"
  [[ "${code}" == "200" ]] || fail "primary readyz failed with HTTP ${code}"
}

resolve_managed_target() {
  local targets_file="${1:-}"
  local target_file="${2:-}"
  if [[ -n "${MANAGED_SWARM_ID}" ]]; then
    jq -c --arg id "${MANAGED_SWARM_ID}" '.targets[]? | select((.swarm_id // "") == $id)' "${targets_file}" | head -n 1 | jq '.' >"${target_file}"
  elif [[ -n "${MANAGED_NAME}" ]]; then
    jq -c --arg name "${MANAGED_NAME}" '.targets[]? | select((.name // "") == $name)' "${targets_file}" | head -n 1 | jq '.' >"${target_file}"
  else
    fail "managed target selector required: pass --managed-swarm-id or --managed-name"
  fi
  if [[ ! -s "${target_file}" ]] || ! jq empty "${target_file}" >/dev/null 2>&1; then
    fail "managed target was not found in /v1/swarm/targets"
  fi
  MANAGED_SWARM_ID="$(jq -r '.swarm_id // empty' "${target_file}")"
  MANAGED_NAME="$(jq -r '.name // empty' "${target_file}")"
  [[ -n "${MANAGED_SWARM_ID}" ]] || fail "managed target has empty swarm_id"
}

assert_checkpoint0_topology() {
  local cp_dir="${1:-}"
  local topology_file="${cp_dir}/topology.json"
  local target_file="${cp_dir}/managed_target.json"
  local online selectable backend runtime_count binding_count container_count attachment_count route_count source_binding_count

  online="$(jq -r '.online // false' "${target_file}")"
  selectable="$(jq -r '.selectable // false' "${target_file}")"
  backend="$(jq -r '.backend_url // empty' "${target_file}")"
  [[ "${online}" == "true" ]] || fail "managed target ${MANAGED_SWARM_ID} is not online"
  [[ "${selectable}" == "true" ]] || fail "managed target ${MANAGED_SWARM_ID} is not selectable"
  [[ -n "${backend}" ]] || fail "managed target ${MANAGED_SWARM_ID} has no backend_url"
  [[ "$(jq -r '.path_id // empty' "${topology_file}")" == "swarm.topology.snapshot.v1" ]] || fail "topology snapshot path_id mismatch"

  runtime_count="$(jq -r --arg id "${MANAGED_SWARM_ID}" '[.runtimes[]? | select((.swarm_id // "") == $id)] | length' "${topology_file}")"
  binding_count="$(jq -r --arg id "${MANAGED_SWARM_ID}" '[.workspace_bindings[]? | select((.destination_runtime_swarm_id // "") == $id or (.destination_host_swarm_id // "") == $id)] | length' "${topology_file}")"
  container_count="$(jq -r --arg id "${MANAGED_SWARM_ID}" '[.host_containers[]? | select((.host_swarm_id // "") == $id)] | length' "${topology_file}")"
  attachment_count="$(jq -r --arg id "${MANAGED_SWARM_ID}" '[.attachments[]? as $a | .host_containers[]? | select((.host_swarm_id // "") == $id and (.host_container_id // "") == ($a.host_container_id // ""))] | length' "${topology_file}")"
  route_count="$(jq -r --arg id "${MANAGED_SWARM_ID}" '[.session_routes[]? | select((.host_swarm_id // "") == $id or (.runtime_swarm_id // "") == $id)] | length' "${topology_file}")"

  [[ "${runtime_count}" -ge 1 ]] || fail "topology has no runtime for managed target ${MANAGED_SWARM_ID}"
  [[ "${binding_count}" -ge 1 ]] || fail "topology has no workspace binding for managed target ${MANAGED_SWARM_ID}"

  if [[ -n "${SOURCE_WORKSPACE_PATH}" ]]; then
    source_binding_count="$(jq -r --arg id "${MANAGED_SWARM_ID}" --arg source "${SOURCE_WORKSPACE_PATH}" '[.workspace_bindings[]? | select((.source_workspace_path // "") == $source and ((.destination_runtime_swarm_id // "") == $id or (.destination_host_swarm_id // "") == $id))] | length' "${topology_file}")"
    [[ "${source_binding_count}" -ge 1 ]] || fail "topology has no binding for source workspace ${SOURCE_WORKSPACE_PATH} on ${MANAGED_SWARM_ID}"
  fi

  if [[ "${container_count}" -gt 0 && "${ALLOW_EXISTING_MANAGED_CONTAINERS}" != "true" ]]; then
    fail "managed host has ${container_count} existing topology host container(s); rerun with cleanup or explicit allow"
  fi
  if [[ "${route_count}" -gt 0 && "${ALLOW_EXISTING_MANAGED_SESSION_ROUTES}" != "true" ]]; then
    fail "managed host has ${route_count} existing session route(s); clean baseline required or explicit allow"
  fi

  jq -nc \
    --arg managed_swarm_id "${MANAGED_SWARM_ID}" \
    --arg managed_name "${MANAGED_NAME}" \
    --argjson runtime_count "${runtime_count}" \
    --argjson binding_count "${binding_count}" \
    --argjson container_count "${container_count}" \
    --argjson attachment_count "${attachment_count}" \
    --argjson session_route_count "${route_count}" \
    '{managed_swarm_id:$managed_swarm_id,managed_name:$managed_name,runtime_count:$runtime_count,binding_count:$binding_count,container_count:$container_count,attachment_count:$attachment_count,session_route_count:$session_route_count}' \
    >"${cp_dir}/checkpoint_0_counts.json"
}

cleanup_managed_containers_if_requested() {
  local cp_dir="${1:-}"
  local topology_file="${cp_dir}/topology.json"
  local ids_file="${cp_dir}/managed_container_ids.json"
  local deployment_ids_file="${cp_dir}/managed_deployment_ids.json"
  jq --arg id "${MANAGED_SWARM_ID}" '[.attachments[]? as $a | .host_containers[]? | select((.host_swarm_id // "") == $id and (.host_container_id // "") == ($a.host_container_id // "")) | ($a.deployment_id // .container_name // .container_id // .runtime_container_ref // .host_container_id)] | map(select(. != null and . != ""))' "${topology_file}" >"${ids_file}"
  jq --arg id "${MANAGED_SWARM_ID}" '[.attachments[]? as $a | .host_containers[]? | select((.host_swarm_id // "") == $id and (.host_container_id // "") == ($a.host_container_id // "")) | $a.deployment_id] | map(select(. != null and . != ""))' "${topology_file}" >"${deployment_ids_file}"
  local count
  count="$(jq -r 'length' "${ids_file}")"
  if [[ "${count}" -eq 0 ]]; then
    return 0
  fi
  if [[ "${CLEANUP_EXISTING_MANAGED_CONTAINERS}" != "true" ]]; then
    return 0
  fi
  log "checkpoint 0: deleting ${count} existing managed-host container(s) through primary managed-host API"
  local body deploy_body deploy_count
  deploy_count="$(jq -r 'length' "${deployment_ids_file}")"
  if [[ "${deploy_count}" -gt 0 ]]; then
    deploy_body="$(jq -nc --slurpfile ids "${deployment_ids_file}" '{ids:$ids[0]}')"
    api_json POST "/v1/deploy/container/delete" "${deploy_body}" "${cp_dir}/cleanup_managed_deployments.json" 120
  else
    body="$(jq -nc --arg managed_swarm_id "${MANAGED_SWARM_ID}" --slurpfile ids "${ids_file}" '{managed_swarm_id:$managed_swarm_id, ids:$ids[0]}')"
    api_json POST "/v1/swarm/managed-host/container/delete" "${body}" "${cp_dir}/cleanup_managed_containers.json" 120
  fi
  api_json GET "/v1/swarm/topology" "" "${cp_dir}/topology_after_cleanup.json" 30
  cp -- "${cp_dir}/topology_after_cleanup.json" "${topology_file}"
}

verify_target_picker() {
  local cp_dir="${1:-}"
  if [[ "${VERIFY_TARGET_PICKER}" != "true" ]]; then
    jq -nc '{skipped:true, reason:"--skip-target-picker"}' >"${cp_dir}/target_picker.json"
    return 0
  fi
  api_json GET "/v1/swarm/target/current" "" "${cp_dir}/target_current_before.json" 30
  local original_id
  original_id="$(jq -r '.target.swarm_id // empty' "${cp_dir}/target_current_before.json")"
  api_json POST "/v1/swarm/target/select" "$(jq -nc --arg swarm_id "${MANAGED_SWARM_ID}" '{swarm_id:$swarm_id}')" "${cp_dir}/target_select_managed.json" 30
  api_json GET "/v1/swarm/target/current" "" "${cp_dir}/target_current_managed.json" 30
  [[ "$(jq -r '.target.swarm_id // empty' "${cp_dir}/target_current_managed.json")" == "${MANAGED_SWARM_ID}" ]] || fail "target picker did not select managed target ${MANAGED_SWARM_ID}"
  if [[ -n "${original_id}" && "${original_id}" != "${MANAGED_SWARM_ID}" ]]; then
    api_json POST "/v1/swarm/target/select" "$(jq -nc --arg swarm_id "${original_id}" '{swarm_id:$swarm_id}')" "${cp_dir}/target_restore_original.json" 30
  fi
  jq -nc --arg selected "${MANAGED_SWARM_ID}" --arg restored "${original_id}" '{selected_managed_swarm_id:$selected, restored_swarm_id:$restored}' >"${cp_dir}/target_picker.json"
}

checkpoint_0() {
  [[ -n "${PRIMARY_URL}" ]] || fail "--primary-url or SWARM_PRIMARY_URL is required for checkpoint 0"
  local cp_dir="${EVIDENCE_DIR}/checkpoint-0"
  mkdir -p -- "${cp_dir}"
  log "== checkpoint 0: testbench sanity =="
  readyz_capture "${cp_dir}/readyz.json"
  start_desktop_session
  api_json GET "/v1/swarm/targets" "" "${cp_dir}/targets.json" 30
  resolve_managed_target "${cp_dir}/targets.json" "${cp_dir}/managed_target.json"
  api_json GET "/v1/swarm/topology" "" "${cp_dir}/topology.json" 30
  cleanup_managed_containers_if_requested "${cp_dir}"
  local managed_query
  managed_query="$(urlencode "${MANAGED_SWARM_ID}")"
  api_json GET "/v1/swarm/mirror/resources?managed_swarm_id=${managed_query}" "" "${cp_dir}/mirror_resources.json" 30
  [[ "$(jq -r '.ok // false' "${cp_dir}/mirror_resources.json")" == "true" ]] || fail "mirror resources endpoint did not return ok=true"
  assert_checkpoint0_topology "${cp_dir}"
  verify_target_picker "${cp_dir}"
  record_checkpoint "0" "PASS" "primary sees managed target online/selectable with topology binding, mirror endpoint healthy, and target picker proof captured" "${cp_dir}"
}

checkpoint_1() {
  local cp_dir="${EVIDENCE_DIR}/checkpoint-1"
  mkdir -p -- "${cp_dir}"
  log "== checkpoint 1: harness extension =="
  jq -nc \
    --arg harness "tests/swarmd/managed_host_launch_gate_e2e.sh" \
    --arg evidence_dir "${EVIDENCE_DIR}" \
    --arg root_dir "${ROOT_DIR}" \
    --arg runner_host "$(hostname -f 2>/dev/null || hostname 2>/dev/null || true)" \
    --arg created_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{harness:$harness,evidence_dir:$evidence_dir,root_dir:$root_dir,runner_host:$runner_host,created_at:$created_at,implemented_checkpoints:["0","1","2","4","5","6","7","8","9","10"],product_path:"primary swarmd API to managed-host swarmd API only",execution_scope:"ssh testbench host only"}' \
    >"${cp_dir}/harness_metadata.json"
  jq -nc '{checkpoint_0:"implemented",checkpoint_1:"implemented",checkpoint_2:"implemented_provider_key_required",checkpoint_3:"completed_external_cold_db_proof",checkpoint_4:"implemented",checkpoint_5:"implemented",checkpoint_6:"implemented_provider_key_required",checkpoint_7:"implemented_provider_key_required",checkpoint_8:"implemented_provider_key_required",checkpoint_9:"implemented_provider_key_required",checkpoint_10:"implemented_provider_key_required",checkpoint_11:"pending"}' \
    >"${cp_dir}/matrix_template.json"
  record_checkpoint "1" "PASS" "repeatable evidence directory and matrix status created; checkpoints 0-2 and 4-10 implemented" "${cp_dir}"
}

ensure_checkpoint2_inputs() {
  [[ -n "${MANAGED_SWARM_ID}" ]] || fail "managed target must be resolved before checkpoint 2"
  [[ -n "${SOURCE_WORKSPACE_PATH}" ]] || fail "--source-workspace-path is required for checkpoint 2"
  if [[ -z "${MANAGED_WORKSPACE_PATH}" ]]; then
    MANAGED_WORKSPACE_PATH="${SOURCE_WORKSPACE_PATH}"
  fi
  [[ -n "${PROVIDER}" ]] || fail "--provider or SWARM_PROVIDER is required for checkpoint 2"
  [[ -n "${MODEL}" ]] || fail "--model or SWARM_MODEL is required for checkpoint 2"
  if [[ -z "${CHECKPOINT2_PROMPT}" ]]; then
    CHECKPOINT2_PROMPT="Launch Gate checkpoint 2. Reply with exactly: LAUNCH_GATE_CP2_OK"
  fi
}

assert_checkpoint2_route() {
  local cp_dir="${1:-}"
  local session_id="${2:-}"
  local session_query
  session_query="$(urlencode "${session_id}")"
  api_json GET "/v1/swarm/topology/session-route?session_id=${session_query}" "" "${cp_dir}/primary_topology_session_route.json" 30
  api_json GET "/v1/swarm/topology" "" "${cp_dir}/primary_topology_after_session.json" 30
  local route_count metadata_host metadata_runtime
  route_count="$(jq -r '.route | if . == null then 0 else 1 end' "${cp_dir}/primary_topology_session_route.json")"
  metadata_host="$(jq -r '.metadata.swarm_managed_host_swarm_id // empty' "${cp_dir}/primary_session_metadata.json")"
  metadata_runtime="$(jq -r '.metadata.swarm_managed_host_runtime_workspace_path // empty' "${cp_dir}/primary_session_metadata.json")"
  if [[ "${route_count}" -eq 1 ]]; then
    [[ "$(jq -r '.route.runtime_swarm_id // empty' "${cp_dir}/primary_topology_session_route.json")" == "${MANAGED_SWARM_ID}" ]] || fail "checkpoint 2 route runtime_swarm_id mismatch"
    [[ "$(jq -r '.route.backend_url // empty' "${cp_dir}/primary_topology_session_route.json")" != "" ]] || fail "checkpoint 2 route backend_url missing"
  else
    [[ "${metadata_host}" == "${MANAGED_SWARM_ID}" ]] || fail "checkpoint 2 topology route missing and primary metadata managed host mismatch"
    jq -nc --arg session_id "${session_id}" --arg runtime_swarm_id "${MANAGED_SWARM_ID}" --arg runtime_workspace_path "${MANAGED_WORKSPACE_PATH}" --arg source "session_metadata_fallback" '{ok:true,source:$source,route:{session_id:$session_id,runtime_swarm_id:$runtime_swarm_id,runtime_workspace_path:$runtime_workspace_path}}' >"${cp_dir}/primary_topology_session_route_fallback.json"
  fi
  [[ "${metadata_host}" == "${MANAGED_SWARM_ID}" ]] || fail "checkpoint 2 primary metadata managed host mismatch"
  [[ "${metadata_runtime}" == "${MANAGED_WORKSPACE_PATH}" ]] || fail "checkpoint 2 primary metadata runtime workspace path mismatch"
}

assert_checkpoint2_messages() {
  local cp_dir="${1:-}"
  local session_id="${2:-}"
  local user_count assistant_count assistant_match
  api_json GET "/v1/sessions/${session_id}/messages?limit=100" "" "${cp_dir}/primary_messages.json" 30
  user_count="$(jq -r '[.messages[]? | select((.role // "") == "user")] | length' "${cp_dir}/primary_messages.json")"
  assistant_count="$(jq -r '[.messages[]? | select((.role // "") == "assistant")] | length' "${cp_dir}/primary_messages.json")"
  assistant_match="$(jq -r '[.messages[]? | select((.role // "") == "assistant" and ((.content // "") | contains("LAUNCH_GATE_CP2_OK")))] | length' "${cp_dir}/primary_messages.json")"
  [[ "${user_count}" -ge 1 ]] || fail "checkpoint 2 primary mirror has no user message"
  [[ "${assistant_count}" -ge 1 ]] || fail "checkpoint 2 primary mirror has no assistant message"
  [[ "${assistant_match}" -ge 1 ]] || fail "checkpoint 2 assistant response did not contain LAUNCH_GATE_CP2_OK"
}

ensure_checkpoint4_inputs() {
  [[ -n "${MANAGED_SWARM_ID}" ]] || fail "managed target must be resolved before checkpoint 4"
  [[ -n "${SOURCE_WORKSPACE_PATH}" ]] || fail "--source-workspace-path is required for checkpoint 4"
  if [[ -z "${CHECKPOINT4_CONTAINER_NAME}" ]]; then
    CHECKPOINT4_CONTAINER_NAME="launch-gate-cp4-$(date +%Y%m%d-%H%M%S)"
  fi
}

ensure_checkpoint5_inputs() {
  [[ -n "${MANAGED_SWARM_ID}" ]] || fail "managed target must be resolved before checkpoint 5"
  [[ -n "${SOURCE_WORKSPACE_PATH}" ]] || fail "--source-workspace-path is required for checkpoint 5"
  if [[ -z "${CHECKPOINT5_CONTAINER_NAME}" ]]; then
    CHECKPOINT5_CONTAINER_NAME="launch-gate-cp5-$(date +%Y%m%d-%H%M%S)"
  fi
  if [[ -z "${CHECKPOINT5_RECREATE_CONTAINER_NAME}" ]]; then
    CHECKPOINT5_RECREATE_CONTAINER_NAME="${CHECKPOINT5_CONTAINER_NAME}-recreate"
  fi
}

ensure_checkpoint6_inputs() {
  [[ -n "${MANAGED_SWARM_ID}" ]] || fail "managed target must be resolved before checkpoint 6"
  [[ -n "${SOURCE_WORKSPACE_PATH}" ]] || fail "--source-workspace-path is required for checkpoint 6"
  [[ -n "${PROVIDER}" ]] || fail "--provider or SWARM_PROVIDER is required for checkpoint 6"
  [[ -n "${MODEL}" ]] || fail "--model or SWARM_MODEL is required for checkpoint 6"
  if [[ -z "${CHECKPOINT6_CONTAINER_NAME}" ]]; then
    CHECKPOINT6_CONTAINER_NAME="launch-gate-cp6-$(date +%Y%m%d-%H%M%S)"
  fi
  if [[ -z "${CHECKPOINT6_PROMPT}" ]]; then
    CHECKPOINT6_PROMPT="Launch Gate checkpoint 6. Reply with exactly: LAUNCH_GATE_CP6_OK"
  fi
  if [[ -z "${CHECKPOINT2_PROMPT}" ]]; then
    CHECKPOINT2_PROMPT="Launch Gate checkpoint 2. Reply with exactly: LAUNCH_GATE_CP2_OK"
  fi
}

checkpoint4_topology_count() {
  local file="${1:-}"
  shift || true
  local jq_args=("$@")
  local last_index=$(( ${#jq_args[@]} - 1 ))
  (( last_index >= 0 )) || fail "checkpoint4_topology_count requires a jq filter"
  local filter="${jq_args[${last_index}]}"
  unset 'jq_args[${last_index}]'
  jq -r "${jq_args[@]}" "[${filter}] | length" "${file}"
}

assert_checkpoint4_create() {
  local cp_dir="${1:-}"
  local deployment_id="${2:-}"
  local child_swarm_id="${3:-}"
  local host_container_id="${4:-}"
  local attachment_id="${5:-}"
  local container_name="${6:-}"
  local runtime_workspace_path="${7:-}"

  [[ -n "${deployment_id}" ]] || fail "checkpoint 4 missing deployment_id"
  [[ -n "${child_swarm_id}" ]] || fail "checkpoint 4 missing child_swarm_id"
  [[ -n "${host_container_id}" ]] || fail "checkpoint 4 missing host_container_id"
  [[ -n "${attachment_id}" ]] || fail "checkpoint 4 missing attachment_id"
  [[ -n "${container_name}" ]] || fail "checkpoint 4 missing container_name"
  [[ -n "${runtime_workspace_path}" ]] || fail "checkpoint 4 missing runtime workspace path"

  local topology_file="${cp_dir}/primary_topology_after_create.json"
  local deployments_file="${cp_dir}/primary_deployments_after_create.json"
  local host_containers_file="${cp_dir}/primary_topology_host_containers.json"
  local runtime_owner_file="${cp_dir}/primary_topology_runtime_owner.json"
  local bindings_file="${cp_dir}/primary_topology_workspace_bindings.json"
  local mirror_file="${cp_dir}/mirror_resources_after_create.json"

  [[ "$(jq -r '.path_id // empty' "${topology_file}")" == "swarm.topology.snapshot.v1" ]] || fail "checkpoint 4 topology snapshot path_id mismatch"
  [[ "$(jq -r '.path_id // empty' "${host_containers_file}")" == "swarm.topology.host_containers.v1" ]] || fail "checkpoint 4 host-containers path_id mismatch"
  [[ "$(jq -r '.path_id // empty' "${runtime_owner_file}")" == "swarm.topology.runtime_owner.v1" ]] || fail "checkpoint 4 runtime-owner path_id mismatch"
  [[ "$(jq -r '.path_id // empty' "${bindings_file}")" == "swarm.topology.workspace_bindings.v1" ]] || fail "checkpoint 4 workspace-bindings path_id mismatch"

  [[ "$(checkpoint4_topology_count "${topology_file}" --arg id "${child_swarm_id}" '.runtimes[]? | select(.swarm_id == $id)')" == "1" ]] || fail "checkpoint 4 topology runtime count is not exactly one for ${child_swarm_id}"
  [[ "$(checkpoint4_topology_count "${topology_file}" --arg id "${host_container_id}" '.host_containers[]? | select(.host_container_id == $id)')" == "1" ]] || fail "checkpoint 4 topology host container count is not exactly one for ${host_container_id}"
  [[ "$(checkpoint4_topology_count "${topology_file}" --arg id "${attachment_id}" '.attachments[]? | select(.attachment_id == $id)')" == "1" ]] || fail "checkpoint 4 topology attachment count is not exactly one for ${attachment_id}"
  [[ "$(checkpoint4_topology_count "${topology_file}" --arg id "${child_swarm_id}" --arg source "${SOURCE_WORKSPACE_PATH}" --arg runtime_path "${runtime_workspace_path}" --arg managed_id "${MANAGED_SWARM_ID}" '.workspace_bindings[]? | select(.source_workspace_path == $source and .destination_runtime_swarm_id == $id and .destination_host_swarm_id == $managed_id and .destination_workspace_path == $runtime_path)')" == "1" ]] || fail "checkpoint 4 topology workspace binding did not uniquely identify managed-host child runtime"

  [[ "$(jq -r --arg id "${deployment_id}" '[.deployments[]? | select(.id == $id)] | length' "${deployments_file}")" == "1" ]] || fail "checkpoint 4 primary deployment mirror missing ${deployment_id}"
  [[ "$(jq -r --arg id "${deployment_id}" '.deployments[]? | select(.id == $id) | .host_swarm_id // empty' "${deployments_file}" | head -n 1)" == "${MANAGED_SWARM_ID}" ]] || fail "checkpoint 4 deployment host_swarm_id is not managed target"
  [[ "$(jq -r --arg id "${deployment_id}" '.deployments[]? | select(.id == $id) | .group_id // empty' "${deployments_file}" | head -n 1)" == "" ]] || fail "checkpoint 4 deployment has primary group_id; managed-host create must not use primary local group"
  [[ "$(jq -r --arg id "${deployment_id}" '.deployments[]? | select(.id == $id) | .attach_status // empty' "${deployments_file}" | head -n 1)" == "attached" ]] || fail "checkpoint 4 deployment is not attached"

  [[ "$(jq -r --arg id "${host_container_id}" '[.host_containers[]? | select(.host_container_id == $id)] | length' "${host_containers_file}")" == "1" ]] || fail "checkpoint 4 host-containers endpoint missing managed host container"
  [[ "$(jq -r '.host_container.host_container_id // empty' "${runtime_owner_file}")" == "${host_container_id}" ]] || fail "checkpoint 4 runtime-owner host_container_id mismatch"
  [[ "$(jq -r '.host_container.host_swarm_id // empty' "${runtime_owner_file}")" == "${MANAGED_SWARM_ID}" ]] || fail "checkpoint 4 runtime-owner host_swarm_id mismatch"
  [[ "$(jq -r '.attachment.attachment_id // empty' "${runtime_owner_file}")" == "${attachment_id}" ]] || fail "checkpoint 4 runtime-owner attachment_id mismatch"
  [[ "$(jq -r '.attachment.runtime_swarm_id // empty' "${runtime_owner_file}")" == "${child_swarm_id}" ]] || fail "checkpoint 4 runtime-owner runtime_swarm_id mismatch"
  [[ "$(jq -r --arg id "${child_swarm_id}" --arg source "${SOURCE_WORKSPACE_PATH}" --arg runtime_path "${runtime_workspace_path}" --arg managed_id "${MANAGED_SWARM_ID}" '[.bindings[]? | select(.source_workspace_path == $source and .destination_runtime_swarm_id == $id and .destination_host_swarm_id == $managed_id and .destination_workspace_path == $runtime_path)] | length' "${bindings_file}")" == "1" ]] || fail "checkpoint 4 workspace-bindings endpoint missing managed child binding"

  [[ "$(jq -r --arg id "${deployment_id}" '[.resources[]? | select(.kind == "deployment" and .id == $id)] | length' "${mirror_file}")" -ge 1 ]] || fail "checkpoint 4 mirror resources missing managed deployment ${deployment_id}"
  [[ "$(jq -r --arg name "${container_name}" '[.resources[]? | select(.kind == "container") | .resource | select((.container_name // "") == $name or (.id // "") == $name)] | length' "${mirror_file}")" -ge 1 ]] || fail "checkpoint 4 mirror resources missing managed runtime container ${container_name}"
}

checkpoint_4() {
  local cp_dir="${EVIDENCE_DIR}/checkpoint-4"
  mkdir -p -- "${cp_dir}"
  log "== checkpoint 4: managed container create via primary API =="
  if [[ -z "${MANAGED_SWARM_ID}" ]]; then
    api_json GET "/v1/swarm/targets" "" "${cp_dir}/targets.json" 30
    resolve_managed_target "${cp_dir}/targets.json" "${cp_dir}/managed_target.json"
  fi
  ensure_checkpoint4_inputs

  local before_container_count before_route_count
  api_json GET "/v1/swarm/topology" "" "${cp_dir}/primary_topology_before_create.json" 30
  before_container_count="$(jq -r --arg id "${MANAGED_SWARM_ID}" '[.host_containers[]? | select(.host_swarm_id == $id)] | length' "${cp_dir}/primary_topology_before_create.json")"
  before_route_count="$(jq -r --arg id "${MANAGED_SWARM_ID}" '[.session_routes[]? | select(.host_swarm_id == $id or .runtime_swarm_id == $id)] | length' "${cp_dir}/primary_topology_before_create.json")"
  jq -nc --argjson managed_container_count "${before_container_count}" --argjson managed_session_route_count "${before_route_count}" '{managed_container_count:$managed_container_count,managed_session_route_count:$managed_session_route_count}' >"${cp_dir}/baseline_counts.json"
  [[ "${before_container_count}" == "0" ]] || fail "checkpoint 4 requires clean managed container baseline; found ${before_container_count}"

  local payload deployment_id child_swarm_id runtime_workspace_path host_container_id attachment_id container_name child_backend_url managed_query host_query runtime_query source_query
  payload="$(jq -nc \
    --arg mode "local" \
    --arg swarm_name "${CHECKPOINT4_CONTAINER_NAME}" \
    --arg target_host_swarm_id "${MANAGED_SWARM_ID}" \
    --arg runtime "${CHECKPOINT4_RUNTIME}" \
    --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
    '{mode:$mode,swarm_name:$swarm_name,target_host_swarm_id:$target_host_swarm_id,sync:{enabled:true,mode:"managed"},workspaces:[{source_workspace_path:$source_workspace_path,replication_mode:"bundle",writable:true}]} + (if $runtime != "" then {runtime:$runtime} else {} end)')"
  printf '%s' "${payload}" | jq 'del(.sync.vault_password)' >"${cp_dir}/replicate_request.redacted.json"
  api_json POST "/v1/swarm/replicate" "${payload}" "${cp_dir}/replicate_response.json" 240
  [[ "$(jq -r '.ok // false' "${cp_dir}/replicate_response.json")" == "true" ]] || fail "checkpoint 4 replicate response ok=false"

  deployment_id="$(jq -r '.swarm.deployment_id // empty' "${cp_dir}/replicate_response.json")"
  child_swarm_id="$(jq -r '.swarm.id // empty' "${cp_dir}/replicate_response.json")"
  runtime_workspace_path="$(jq -r '.workspaces[0].binding.destination_workspace_path // empty' "${cp_dir}/replicate_response.json")"
  [[ "$(jq -r '.swarm.mode // empty' "${cp_dir}/replicate_response.json")" == "local" ]] || fail "checkpoint 4 replicate response did not use local container mode"
  [[ "$(jq -r '.swarm.group_id // empty' "${cp_dir}/replicate_response.json")" == "" ]] || fail "checkpoint 4 response has primary group_id; managed-host create must not use primary local group"
  [[ "$(jq -r '.workspaces[0].binding.destination_host_swarm_id // empty' "${cp_dir}/replicate_response.json")" == "${MANAGED_SWARM_ID}" ]] || fail "checkpoint 4 binding destination_host_swarm_id mismatch"
  [[ "$(jq -r '.workspaces[0].binding.destination_runtime_swarm_id // empty' "${cp_dir}/replicate_response.json")" == "${child_swarm_id}" ]] || fail "checkpoint 4 binding destination_runtime_swarm_id mismatch"

  api_json GET "/v1/deploy/container" "" "${cp_dir}/primary_deployments_after_create.json" 30
  host_container_id="$(jq -r --arg id "${deployment_id}" '.deployments[]? | select(.id == $id) | .host_container_id // empty' "${cp_dir}/primary_deployments_after_create.json" | head -n 1)"
  attachment_id="$(jq -r --arg id "${deployment_id}" '.deployments[]? | select(.id == $id) | .attachment_id // empty' "${cp_dir}/primary_deployments_after_create.json" | head -n 1)"
  container_name="$(jq -r --arg id "${deployment_id}" '.deployments[]? | select(.id == $id) | .container_name // empty' "${cp_dir}/primary_deployments_after_create.json" | head -n 1)"
  child_backend_url="$(jq -r --arg id "${deployment_id}" '.deployments[]? | select(.id == $id) | .child_backend_url // empty' "${cp_dir}/primary_deployments_after_create.json" | head -n 1)"

  host_query="$(urlencode "${MANAGED_SWARM_ID}")"
  runtime_query="$(urlencode "${child_swarm_id}")"
  source_query="$(urlencode "${SOURCE_WORKSPACE_PATH}")"
  managed_query="$(urlencode "${MANAGED_SWARM_ID}")"
  api_json GET "/v1/swarm/topology" "" "${cp_dir}/primary_topology_after_create.json" 30
  api_json GET "/v1/swarm/topology/host-containers?host_swarm_id=${host_query}" "" "${cp_dir}/primary_topology_host_containers.json" 30
  api_json GET "/v1/swarm/topology/runtime-owner?runtime_swarm_id=${runtime_query}" "" "${cp_dir}/primary_topology_runtime_owner.json" 30
  api_json GET "/v1/swarm/topology/workspace-bindings?source_workspace_path=${source_query}" "" "${cp_dir}/primary_topology_workspace_bindings.json" 30
  local mirror_deadline=$((SECONDS + 45))
  while :; do
    api_json GET "/v1/swarm/mirror/resources?managed_swarm_id=${managed_query}&resources=container,deployment,target" "" "${cp_dir}/mirror_resources_after_create.json" 30
    if jq -e --arg id "${deployment_id}" '[.resources[]? | select(.kind == "deployment" and .id == $id)] | length > 0' "${cp_dir}/mirror_resources_after_create.json" >/dev/null; then
      break
    fi
    [[ "${SECONDS}" -lt "${mirror_deadline}" ]] || fail "checkpoint 4 timed out waiting for managed deployment mirror resource ${deployment_id}"
    sleep 3
  done

  assert_checkpoint4_create "${cp_dir}" "${deployment_id}" "${child_swarm_id}" "${host_container_id}" "${attachment_id}" "${container_name}" "${runtime_workspace_path}"

  if [[ -n "${child_backend_url}" ]]; then
    api_json GET "/v1/sessions?swarm_id=$(urlencode "${child_swarm_id}")" "" "${cp_dir}/primary_child_route_session_list_probe.json" 30
  fi

  jq -nc \
    --arg deployment_id "${deployment_id}" \
    --arg child_swarm_id "${child_swarm_id}" \
    --arg managed_swarm_id "${MANAGED_SWARM_ID}" \
    --arg host_container_id "${host_container_id}" \
    --arg attachment_id "${attachment_id}" \
    --arg container_name "${container_name}" \
    --arg runtime_workspace_path "${runtime_workspace_path}" \
    --arg child_backend_url "${child_backend_url}" \
    '{deployment_id:$deployment_id,child_swarm_id:$child_swarm_id,managed_swarm_id:$managed_swarm_id,host_container_id:$host_container_id,attachment_id:$attachment_id,container_name:$container_name,runtime_workspace_path:$runtime_workspace_path,child_backend_url:$child_backend_url,product_path:"primary /v1/swarm/replicate -> managed host /v1/deploy/container/create",fallback_allowed:false}' \
    >"${cp_dir}/checkpoint_4_summary.json"
  cleanup_checkpoint6_container "${cp_dir}" "${deployment_id}" "${child_swarm_id}" "${host_container_id}" "${attachment_id}"

  record_checkpoint "4" "PASS" "managed-host container created through primary API; primary topology/router/mirror identify managed host, child runtime, host container, attachment, and workspace binding without local fallback; container cleaned up" "${cp_dir}"
}


assert_checkpoint5_deleted_absent() {
  local cp_dir="${1:-}"
  local deployment_id="${2:-}"
  local child_swarm_id="${3:-}"
  local host_container_id="${4:-}"
  local attachment_id="${5:-}"
  local source_query="$(urlencode "${SOURCE_WORKSPACE_PATH}")"
  api_json GET "/v1/swarm/topology" "" "${cp_dir}/primary_topology_after_delete.json" 30
  api_json GET "/v1/deploy/container" "" "${cp_dir}/primary_deployments_after_delete.json" 30
  api_json GET "/v1/swarm/topology/workspace-bindings?source_workspace_path=${source_query}" "" "${cp_dir}/primary_workspace_bindings_after_delete.json" 30

  [[ "$(jq -r --arg id "${deployment_id}" '[.deployments[]? | select(.id == $id)] | length' "${cp_dir}/primary_deployments_after_delete.json")" == "0" ]] || fail "checkpoint 5 delete left primary deployment mirror ${deployment_id}"
  [[ "$(jq -r --arg id "${child_swarm_id}" '[.runtimes[]? | select(.swarm_id == $id)] | length' "${cp_dir}/primary_topology_after_delete.json")" == "0" ]] || fail "checkpoint 5 delete left child runtime ${child_swarm_id}"
  [[ "$(jq -r --arg id "${host_container_id}" '[.host_containers[]? | select(.host_container_id == $id)] | length' "${cp_dir}/primary_topology_after_delete.json")" == "0" ]] || fail "checkpoint 5 delete left host container ${host_container_id}"
  [[ "$(jq -r --arg id "${attachment_id}" '[.attachments[]? | select(.attachment_id == $id)] | length' "${cp_dir}/primary_topology_after_delete.json")" == "0" ]] || fail "checkpoint 5 delete left attachment ${attachment_id}"
  [[ "$(jq -r --arg id "${child_swarm_id}" '[.workspace_bindings[]? | select(.destination_runtime_swarm_id == $id)] | length' "${cp_dir}/primary_topology_after_delete.json")" == "0" ]] || fail "checkpoint 5 delete left workspace binding for ${child_swarm_id}"
  [[ "$(jq -r --arg id "${child_swarm_id}" '[.bindings[]? | select(.destination_runtime_swarm_id == $id)] | length' "${cp_dir}/primary_workspace_bindings_after_delete.json")" == "0" ]] || fail "checkpoint 5 workspace-bindings endpoint still returns ${child_swarm_id}"
}

checkpoint_5() {
  local cp_dir="${EVIDENCE_DIR}/checkpoint-5"
  mkdir -p -- "${cp_dir}"
  log "== checkpoint 5: managed container CRUD via primary API =="
  if [[ -z "${MANAGED_SWARM_ID}" ]]; then
    api_json GET "/v1/swarm/targets" "" "${cp_dir}/targets.json" 30
    resolve_managed_target "${cp_dir}/targets.json" "${cp_dir}/managed_target.json"
  fi
  ensure_checkpoint5_inputs

  local before_container_count payload deployment_id child_swarm_id runtime_workspace_path host_container_id attachment_id container_name managed_query host_query runtime_query source_query
  api_json GET "/v1/swarm/topology" "" "${cp_dir}/primary_topology_before_crud.json" 30
  before_container_count="$(jq -r --arg id "${MANAGED_SWARM_ID}" '[.host_containers[]? | select(.host_swarm_id == $id)] | length' "${cp_dir}/primary_topology_before_crud.json")"
  jq -nc --argjson managed_container_count "${before_container_count}" '{managed_container_count:$managed_container_count}' >"${cp_dir}/baseline_counts.json"
  [[ "${before_container_count}" == "0" ]] || fail "checkpoint 5 requires clean managed container baseline; found ${before_container_count}"

  payload="$(jq -nc \
    --arg mode "local" \
    --arg swarm_name "${CHECKPOINT5_CONTAINER_NAME}" \
    --arg target_host_swarm_id "${MANAGED_SWARM_ID}" \
    --arg runtime "${CHECKPOINT4_RUNTIME}" \
    --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
    '{mode:$mode,swarm_name:$swarm_name,target_host_swarm_id:$target_host_swarm_id,sync:{enabled:true,mode:"managed"},workspaces:[{source_workspace_path:$source_workspace_path,replication_mode:"bundle",writable:true}]} + (if $runtime != "" then {runtime:$runtime} else {} end)')"
  printf '%s' "${payload}" | jq 'del(.sync.vault_password)' >"${cp_dir}/replicate_request.redacted.json"
  api_json POST "/v1/swarm/replicate" "${payload}" "${cp_dir}/replicate_response.json" 240
  [[ "$(jq -r '.ok // false' "${cp_dir}/replicate_response.json")" == "true" ]] || fail "checkpoint 5 replicate response ok=false"

  deployment_id="$(jq -r '.swarm.deployment_id // empty' "${cp_dir}/replicate_response.json")"
  child_swarm_id="$(jq -r '.swarm.id // empty' "${cp_dir}/replicate_response.json")"
  runtime_workspace_path="$(jq -r '.workspaces[0].binding.destination_workspace_path // empty' "${cp_dir}/replicate_response.json")"
  api_json GET "/v1/deploy/container" "" "${cp_dir}/primary_deployments_after_create.json" 30
  host_container_id="$(jq -r --arg id "${deployment_id}" '.deployments[]? | select(.id == $id) | .host_container_id // empty' "${cp_dir}/primary_deployments_after_create.json" | head -n 1)"
  attachment_id="$(jq -r --arg id "${deployment_id}" '.deployments[]? | select(.id == $id) | .attachment_id // empty' "${cp_dir}/primary_deployments_after_create.json" | head -n 1)"
  container_name="$(jq -r --arg id "${deployment_id}" '.deployments[]? | select(.id == $id) | .container_name // empty' "${cp_dir}/primary_deployments_after_create.json" | head -n 1)"

  host_query="$(urlencode "${MANAGED_SWARM_ID}")"
  runtime_query="$(urlencode "${child_swarm_id}")"
  source_query="$(urlencode "${SOURCE_WORKSPACE_PATH}")"
  managed_query="$(urlencode "${MANAGED_SWARM_ID}")"
  api_json GET "/v1/swarm/topology" "" "${cp_dir}/primary_topology_after_create.json" 30
  api_json GET "/v1/swarm/topology/host-containers?host_swarm_id=${host_query}" "" "${cp_dir}/primary_topology_host_containers_after_create.json" 30
  api_json GET "/v1/swarm/topology/runtime-owner?runtime_swarm_id=${runtime_query}" "" "${cp_dir}/primary_topology_runtime_owner_after_create.json" 30
  api_json GET "/v1/swarm/topology/workspace-bindings?source_workspace_path=${source_query}" "" "${cp_dir}/primary_topology_workspace_bindings_after_create.json" 30
  local mirror_deadline=$((SECONDS + 45))
  while :; do
    api_json GET "/v1/swarm/mirror/resources?managed_swarm_id=${managed_query}&resources=container,deployment,target" "" "${cp_dir}/mirror_resources_after_create.json" 30
    if jq -e --arg id "${deployment_id}" '[.resources[]? | select(.kind == "deployment" and .id == $id)] | length > 0' "${cp_dir}/mirror_resources_after_create.json" >/dev/null; then
      break
    fi
    [[ "${SECONDS}" -lt "${mirror_deadline}" ]] || fail "checkpoint 5 timed out waiting for managed deployment mirror resource ${deployment_id}"
    sleep 3
  done
  cp -- "${cp_dir}/primary_topology_host_containers_after_create.json" "${cp_dir}/primary_topology_host_containers.json"
  cp -- "${cp_dir}/primary_topology_runtime_owner_after_create.json" "${cp_dir}/primary_topology_runtime_owner.json"
  cp -- "${cp_dir}/primary_topology_workspace_bindings_after_create.json" "${cp_dir}/primary_topology_workspace_bindings.json"
  cp -- "${cp_dir}/mirror_resources_after_create.json" "${cp_dir}/mirror_resources_after_create_for_assert.json"
  mv -- "${cp_dir}/mirror_resources_after_create_for_assert.json" "${cp_dir}/mirror_resources_after_create.json"
  assert_checkpoint4_create "${cp_dir}" "${deployment_id}" "${child_swarm_id}" "${host_container_id}" "${attachment_id}" "${container_name}" "${runtime_workspace_path}"

  api_json POST "/v1/deploy/container/settings" "$(jq -nc --arg id "${deployment_id}" '{id:$id,bypass_permissions:true,sync_modules:["permissions"]}')" "${cp_dir}/settings_response.json" 120
  [[ "$(jq -r '.ok // false' "${cp_dir}/settings_response.json")" == "true" ]] || fail "checkpoint 5 settings update ok=false"
  [[ "$(jq -r '.deployment.host_swarm_id // empty' "${cp_dir}/settings_response.json")" == "${MANAGED_SWARM_ID}" ]] || fail "checkpoint 5 settings response host_swarm_id mismatch"
  [[ "$(jq -r '.deployment.bypass_permissions // false' "${cp_dir}/settings_response.json")" == "true" ]] || fail "checkpoint 5 settings did not update mirrored bypass_permissions"

  api_json POST "/v1/deploy/container/action" "$(jq -nc --arg id "${deployment_id}" '{id:$id,action:"stop"}')" "${cp_dir}/action_stop_response.json" 120
  [[ "$(jq -r '.ok // false' "${cp_dir}/action_stop_response.json")" == "true" ]] || fail "checkpoint 5 stop action ok=false"
  [[ "$(jq -r '.deployment.host_swarm_id // empty' "${cp_dir}/action_stop_response.json")" == "${MANAGED_SWARM_ID}" ]] || fail "checkpoint 5 stop response host_swarm_id mismatch"
  api_json POST "/v1/deploy/container/action" "$(jq -nc --arg id "${deployment_id}" '{id:$id,action:"start"}')" "${cp_dir}/action_start_response.json" 180
  [[ "$(jq -r '.ok // false' "${cp_dir}/action_start_response.json")" == "true" ]] || fail "checkpoint 5 start action ok=false"
  [[ "$(jq -r '.deployment.host_swarm_id // empty' "${cp_dir}/action_start_response.json")" == "${MANAGED_SWARM_ID}" ]] || fail "checkpoint 5 start response host_swarm_id mismatch"

  api_json GET "/v1/swarm/topology" "" "${cp_dir}/primary_topology_after_update_actions.json" 30
  [[ "$(checkpoint4_topology_count "${cp_dir}/primary_topology_after_update_actions.json" --arg id "${child_swarm_id}" '.runtimes[]? | select(.swarm_id == $id)')" == "1" ]] || fail "checkpoint 5 update/actions lost child runtime route"
  [[ "$(checkpoint4_topology_count "${cp_dir}/primary_topology_after_update_actions.json" --arg id "${host_container_id}" '.host_containers[]? | select(.host_container_id == $id)')" == "1" ]] || fail "checkpoint 5 update/actions lost host container route"

  api_json POST "/v1/deploy/container/delete" "$(jq -nc --arg id "${deployment_id}" '{ids:[$id]}')" "${cp_dir}/delete_response.json" 180
  [[ "$(jq -r '.ok // false' "${cp_dir}/delete_response.json")" == "true" ]] || fail "checkpoint 5 delete ok=false"
  [[ "$(jq -r --arg id "${deployment_id}" '[.result.deleted[]? | select(. == $id)] | length' "${cp_dir}/delete_response.json")" == "1" ]] || fail "checkpoint 5 delete response did not include ${deployment_id}"
  assert_checkpoint5_deleted_absent "${cp_dir}" "${deployment_id}" "${child_swarm_id}" "${host_container_id}" "${attachment_id}"

  local recreate_payload recreate_deployment_id recreate_child_swarm_id recreate_runtime_workspace_path recreate_host_container_id recreate_attachment_id recreate_container_name
  recreate_payload="$(jq -nc \
    --arg mode "local" \
    --arg swarm_name "${CHECKPOINT5_RECREATE_CONTAINER_NAME}" \
    --arg target_host_swarm_id "${MANAGED_SWARM_ID}" \
    --arg runtime "${CHECKPOINT4_RUNTIME}" \
    --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
    '{mode:$mode,swarm_name:$swarm_name,target_host_swarm_id:$target_host_swarm_id,sync:{enabled:true,mode:"managed"},workspaces:[{source_workspace_path:$source_workspace_path,replication_mode:"bundle",writable:true}]} + (if $runtime != "" then {runtime:$runtime} else {} end)')"
  printf '%s' "${recreate_payload}" | jq 'del(.sync.vault_password)' >"${cp_dir}/recreate_request.redacted.json"
  api_json POST "/v1/swarm/replicate" "${recreate_payload}" "${cp_dir}/recreate_response.json" 240
  [[ "$(jq -r '.ok // false' "${cp_dir}/recreate_response.json")" == "true" ]] || fail "checkpoint 5 recreate response ok=false"
  recreate_deployment_id="$(jq -r '.swarm.deployment_id // empty' "${cp_dir}/recreate_response.json")"
  recreate_child_swarm_id="$(jq -r '.swarm.id // empty' "${cp_dir}/recreate_response.json")"
  recreate_runtime_workspace_path="$(jq -r '.workspaces[0].binding.destination_workspace_path // empty' "${cp_dir}/recreate_response.json")"
  [[ -n "${recreate_deployment_id}" && "${recreate_deployment_id}" != "${deployment_id}" ]] || fail "checkpoint 5 recreate did not produce a new deployment id"
  api_json GET "/v1/deploy/container" "" "${cp_dir}/primary_deployments_after_recreate.json" 30
  recreate_host_container_id="$(jq -r --arg id "${recreate_deployment_id}" '.deployments[]? | select(.id == $id) | .host_container_id // empty' "${cp_dir}/primary_deployments_after_recreate.json" | head -n 1)"
  recreate_attachment_id="$(jq -r --arg id "${recreate_deployment_id}" '.deployments[]? | select(.id == $id) | .attachment_id // empty' "${cp_dir}/primary_deployments_after_recreate.json" | head -n 1)"
  recreate_container_name="$(jq -r --arg id "${recreate_deployment_id}" '.deployments[]? | select(.id == $id) | .container_name // empty' "${cp_dir}/primary_deployments_after_recreate.json" | head -n 1)"
  runtime_query="$(urlencode "${recreate_child_swarm_id}")"
  api_json GET "/v1/swarm/topology" "" "${cp_dir}/primary_topology_after_recreate.json" 30
  api_json GET "/v1/swarm/topology/host-containers?host_swarm_id=${host_query}" "" "${cp_dir}/primary_topology_host_containers.json" 30
  api_json GET "/v1/swarm/topology/runtime-owner?runtime_swarm_id=${runtime_query}" "" "${cp_dir}/primary_topology_runtime_owner.json" 30
  api_json GET "/v1/swarm/topology/workspace-bindings?source_workspace_path=${source_query}" "" "${cp_dir}/primary_topology_workspace_bindings.json" 30
  mirror_deadline=$((SECONDS + 45))
  while :; do
    api_json GET "/v1/swarm/mirror/resources?managed_swarm_id=${managed_query}&resources=container,deployment,target" "" "${cp_dir}/mirror_resources_after_create.json" 30
    if jq -e --arg id "${recreate_deployment_id}" '[.resources[]? | select(.kind == "deployment" and .id == $id)] | length > 0' "${cp_dir}/mirror_resources_after_create.json" >/dev/null; then
      break
    fi
    [[ "${SECONDS}" -lt "${mirror_deadline}" ]] || fail "checkpoint 5 timed out waiting for recreated managed deployment mirror resource ${recreate_deployment_id}"
    sleep 3
  done
  cp -- "${cp_dir}/primary_topology_after_recreate.json" "${cp_dir}/primary_topology_after_create.json"
  cp -- "${cp_dir}/primary_deployments_after_recreate.json" "${cp_dir}/primary_deployments_after_create.json"
  assert_checkpoint4_create "${cp_dir}" "${recreate_deployment_id}" "${recreate_child_swarm_id}" "${recreate_host_container_id}" "${recreate_attachment_id}" "${recreate_container_name}" "${recreate_runtime_workspace_path}"

  jq -nc \
    --arg deployment_id "${deployment_id}" \
    --arg child_swarm_id "${child_swarm_id}" \
    --arg recreate_deployment_id "${recreate_deployment_id}" \
    --arg recreate_child_swarm_id "${recreate_child_swarm_id}" \
    --arg managed_swarm_id "${MANAGED_SWARM_ID}" \
    '{deployment_id:$deployment_id,child_swarm_id:$child_swarm_id,recreate_deployment_id:$recreate_deployment_id,recreate_child_swarm_id:$recreate_child_swarm_id,managed_swarm_id:$managed_swarm_id,product_path:"primary /v1/deploy/container/settings|action|delete plus /v1/swarm/replicate -> managed host deploy service",fallback_allowed:false}' \
    >"${cp_dir}/checkpoint_5_summary.json"
  cleanup_checkpoint6_container "${cp_dir}" "${recreate_deployment_id}" "${recreate_child_swarm_id}" "${recreate_host_container_id}" "${recreate_attachment_id}"

  record_checkpoint "5" "PASS" "managed-host container settings update, stop/start, delete topology cleanup, and recreate all succeeded through primary API without primary-local fallback; recreated container cleaned up" "${cp_dir}"
}

assert_checkpoint6_route() {
  local cp_dir="${1:-}"
  local session_id="${2:-}"
  local child_swarm_id="${3:-}"
  local host_container_id="${4:-}"
  local runtime_workspace_path="${5:-}"
  local session_query
  session_query="$(urlencode "${session_id}")"
  api_json GET "/v1/swarm/topology/session-route?session_id=${session_query}" "" "${cp_dir}/primary_topology_session_route.json" 30
  api_json GET "/v1/swarm/topology" "" "${cp_dir}/primary_topology_after_session.json" 30

  if [[ "$(jq -r '.route | if . == null then 0 else 1 end' "${cp_dir}/primary_topology_session_route.json")" == "1" ]]; then
    [[ "$(jq -r '.route.runtime_swarm_id // empty' "${cp_dir}/primary_topology_session_route.json")" == "${child_swarm_id}" ]] || fail "checkpoint 6 route runtime_swarm_id mismatch"
    [[ "$(jq -r '.route.host_swarm_id // empty' "${cp_dir}/primary_topology_session_route.json")" == "${MANAGED_SWARM_ID}" ]] || fail "checkpoint 6 route host_swarm_id mismatch"
    [[ "$(jq -r '.route.host_container_id // empty' "${cp_dir}/primary_topology_session_route.json")" == "${host_container_id}" ]] || fail "checkpoint 6 route host_container_id mismatch"
    [[ "$(jq -r '.route.runtime_workspace_path // empty' "${cp_dir}/primary_topology_session_route.json")" == "${runtime_workspace_path}" ]] || fail "checkpoint 6 route runtime workspace path mismatch"
    [[ "$(jq -r '.route.backend_url // empty' "${cp_dir}/primary_topology_session_route.json")" != "" ]] || fail "checkpoint 6 route backend_url missing"
  else
    [[ "$(jq -r --arg id "${session_id}" '(.session.id // empty) == $id' "${cp_dir}/primary_session.json")" == "true" ]] || fail "checkpoint 6 primary session missing ${session_id}"
    [[ "$(jq -r --arg runtime "${child_swarm_id}" '(.session.metadata.swarm_routed_child_swarm_id // empty) == $runtime' "${cp_dir}/primary_session.json")" == "true" ]] || fail "checkpoint 6 primary session metadata child mismatch"
    [[ "$(jq -r --arg runtime_path "${runtime_workspace_path}" '(.session.metadata.swarm_routed_runtime_workspace_path // empty) == $runtime_path' "${cp_dir}/primary_session.json")" == "true" ]] || fail "checkpoint 6 primary session metadata runtime workspace path mismatch"
    jq -nc --arg session_id "${session_id}" --arg runtime_swarm_id "${child_swarm_id}" --arg host_swarm_id "${MANAGED_SWARM_ID}" --arg host_container_id "${host_container_id}" --arg runtime_workspace_path "${runtime_workspace_path}" --arg source "session_metadata_fallback" '{ok:true,source:$source,route:{session_id:$session_id,runtime_swarm_id:$runtime_swarm_id,host_swarm_id:$host_swarm_id,host_container_id:$host_container_id,runtime_workspace_path:$runtime_workspace_path}}' >"${cp_dir}/primary_topology_session_route_fallback.json"
  fi
  if [[ "$(jq -r --arg id "${session_id}" --arg runtime "${child_swarm_id}" '[.session_routes[]? | select(.session_id == $id and .runtime_swarm_id == $runtime)] | length' "${cp_dir}/primary_topology_after_session.json")" != "1" ]]; then
    [[ -f "${cp_dir}/primary_topology_session_route_fallback.json" ]] || fail "checkpoint 6 topology snapshot missing session route"
  fi

  [[ "$(jq -r '.metadata.swarm_routed_child_swarm_id // empty' "${cp_dir}/primary_session_metadata.json")" == "${child_swarm_id}" ]] || fail "checkpoint 6 primary metadata child swarm mismatch"
  [[ "$(jq -r '.metadata.swarm_routed_host_workspace_path // empty' "${cp_dir}/primary_session_metadata.json")" == "${SOURCE_WORKSPACE_PATH}" ]] || fail "checkpoint 6 primary metadata host workspace path mismatch"
  [[ "$(jq -r '.metadata.swarm_routed_runtime_workspace_path // empty' "${cp_dir}/primary_session_metadata.json")" == "${runtime_workspace_path}" ]] || fail "checkpoint 6 primary metadata runtime workspace path mismatch"
}

assert_checkpoint6_messages() {
  local cp_dir="${1:-}"
  local session_id="${2:-}"
  api_json GET "/v1/sessions/${session_id}/messages?limit=100" "" "${cp_dir}/primary_messages.json" 30
  [[ "$(jq -r '[.messages[]? | select((.role // "") == "user")] | length' "${cp_dir}/primary_messages.json")" -ge 1 ]] || fail "checkpoint 6 primary mirror has no user message"
  [[ "$(jq -r '[.messages[]? | select((.role // "") == "assistant")] | length' "${cp_dir}/primary_messages.json")" -ge 1 ]] || fail "checkpoint 6 primary mirror has no assistant message"
  [[ "$(jq -r '[.messages[]? | select((.role // "") == "assistant" and ((.content // "") | contains("LAUNCH_GATE_CP6_OK")))] | length' "${cp_dir}/primary_messages.json")" -ge 1 ]] || fail "checkpoint 6 assistant response did not contain LAUNCH_GATE_CP6_OK"
}

cleanup_checkpoint6_container() {
  local cp_dir="${1:-}"
  local deployment_id="${2:-}"
  local child_swarm_id="${3:-}"
  local host_container_id="${4:-}"
  local attachment_id="${5:-}"
  [[ -n "${deployment_id}" ]] || return 0
  api_json POST "/v1/deploy/container/delete" "$(jq -nc --arg id "${deployment_id}" '{ids:[$id]}')" "${cp_dir}/delete_response.json" 180
  [[ "$(jq -r '.ok // false' "${cp_dir}/delete_response.json")" == "true" ]] || fail "checkpoint 6 delete ok=false"
  assert_checkpoint5_deleted_absent "${cp_dir}" "${deployment_id}" "${child_swarm_id}" "${host_container_id}" "${attachment_id}"
}

checkpoint_6() {
  local cp_dir="${EVIDENCE_DIR}/checkpoint-6"
  mkdir -p -- "${cp_dir}"
  log "== checkpoint 6: managed container AI via primary routed session =="
  if [[ -z "${MANAGED_SWARM_ID}" ]]; then
    api_json GET "/v1/swarm/targets" "" "${cp_dir}/targets.json" 30
    resolve_managed_target "${cp_dir}/targets.json" "${cp_dir}/managed_target.json"
  fi
  ensure_checkpoint6_inputs

  local before_container_count payload deployment_id child_swarm_id runtime_workspace_path host_container_id attachment_id container_name child_backend_url managed_query host_query runtime_query source_query
  api_json GET "/v1/swarm/topology" "" "${cp_dir}/primary_topology_before_create.json" 30
  before_container_count="$(jq -r --arg id "${MANAGED_SWARM_ID}" '[.host_containers[]? | select(.host_swarm_id == $id)] | length' "${cp_dir}/primary_topology_before_create.json")"
  jq -nc --argjson managed_container_count "${before_container_count}" '{managed_container_count:$managed_container_count}' >"${cp_dir}/baseline_counts.json"
  [[ "${before_container_count}" == "0" ]] || fail "checkpoint 6 requires clean managed container baseline; found ${before_container_count}"

  payload="$(jq -nc \
    --arg mode "local" \
    --arg swarm_name "${CHECKPOINT6_CONTAINER_NAME}" \
    --arg target_host_swarm_id "${MANAGED_SWARM_ID}" \
    --arg runtime "${CHECKPOINT4_RUNTIME}" \
    --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
    '{mode:$mode,swarm_name:$swarm_name,target_host_swarm_id:$target_host_swarm_id,sync:{enabled:true,mode:"managed"},workspaces:[{source_workspace_path:$source_workspace_path,replication_mode:"bundle",writable:true}]} + (if $runtime != "" then {runtime:$runtime} else {} end)')"
  printf '%s' "${payload}" | jq 'del(.sync.vault_password)' >"${cp_dir}/replicate_request.redacted.json"
  api_json POST "/v1/swarm/replicate" "${payload}" "${cp_dir}/replicate_response.json" 240
  [[ "$(jq -r '.ok // false' "${cp_dir}/replicate_response.json")" == "true" ]] || fail "checkpoint 6 replicate response ok=false"

  deployment_id="$(jq -r '.swarm.deployment_id // empty' "${cp_dir}/replicate_response.json")"
  child_swarm_id="$(jq -r '.swarm.id // empty' "${cp_dir}/replicate_response.json")"
  runtime_workspace_path="$(jq -r '.workspaces[0].binding.destination_workspace_path // empty' "${cp_dir}/replicate_response.json")"
  api_json GET "/v1/deploy/container" "" "${cp_dir}/primary_deployments_after_create.json" 30
  host_container_id="$(jq -r --arg id "${deployment_id}" '.deployments[]? | select(.id == $id) | .host_container_id // empty' "${cp_dir}/primary_deployments_after_create.json" | head -n 1)"
  attachment_id="$(jq -r --arg id "${deployment_id}" '.deployments[]? | select(.id == $id) | .attachment_id // empty' "${cp_dir}/primary_deployments_after_create.json" | head -n 1)"
  container_name="$(jq -r --arg id "${deployment_id}" '.deployments[]? | select(.id == $id) | .container_name // empty' "${cp_dir}/primary_deployments_after_create.json" | head -n 1)"
  child_backend_url="$(jq -r --arg id "${deployment_id}" '.deployments[]? | select(.id == $id) | .child_backend_url // empty' "${cp_dir}/primary_deployments_after_create.json" | head -n 1)"

  host_query="$(urlencode "${MANAGED_SWARM_ID}")"
  runtime_query="$(urlencode "${child_swarm_id}")"
  source_query="$(urlencode "${SOURCE_WORKSPACE_PATH}")"
  managed_query="$(urlencode "${MANAGED_SWARM_ID}")"
  api_json GET "/v1/swarm/topology" "" "${cp_dir}/primary_topology_after_create.json" 30
  api_json GET "/v1/swarm/topology/host-containers?host_swarm_id=${host_query}" "" "${cp_dir}/primary_topology_host_containers.json" 30
  api_json GET "/v1/swarm/topology/runtime-owner?runtime_swarm_id=${runtime_query}" "" "${cp_dir}/primary_topology_runtime_owner.json" 30
  api_json GET "/v1/swarm/topology/workspace-bindings?source_workspace_path=${source_query}" "" "${cp_dir}/primary_topology_workspace_bindings.json" 30
  local mirror_deadline=$((SECONDS + 45))
  while :; do
    api_json GET "/v1/swarm/targets?swarm_id=${runtime_query}" "" "${cp_dir}/targets_after_create_poll.json" 30
    if jq -e --arg id "${child_swarm_id}" '[.targets[]? | select(.swarm_id == $id and .kind == "mirrored")] | length > 0' "${cp_dir}/targets_after_create_poll.json" >/dev/null; then
      cp -- "${cp_dir}/targets_after_create_poll.json" "${cp_dir}/targets_after_create.json"
      break
    fi
    [[ "${SECONDS}" -lt "${mirror_deadline}" ]] || fail "checkpoint 6 timed out waiting for mirrored child target ${child_swarm_id}"
    sleep 3
  done
  while :; do
    api_json GET "/v1/swarm/mirror/resources?managed_swarm_id=${managed_query}&resources=container,deployment,target" "" "${cp_dir}/mirror_resources_after_create.json" 30
    if jq -e --arg id "${deployment_id}" '[.resources[]? | select(.kind == "deployment" and .id == $id)] | length > 0' "${cp_dir}/mirror_resources_after_create.json" >/dev/null; then
      break
    fi
    [[ "${SECONDS}" -lt "${mirror_deadline}" ]] || fail "checkpoint 6 timed out waiting for managed deployment mirror resource ${deployment_id}"
    sleep 3
  done
  assert_checkpoint4_create "${cp_dir}" "${deployment_id}" "${child_swarm_id}" "${host_container_id}" "${attachment_id}" "${container_name}" "${runtime_workspace_path}"

  local open_body session_id run_body run_id deadline now
  open_body="$(jq -nc \
    --arg title "Launch Gate checkpoint 6" \
    --arg workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg host_workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg runtime_workspace_path "${runtime_workspace_path}" \
    --arg workspace_name "$(basename "${SOURCE_WORKSPACE_PATH}")" \
    --arg mode "auto" \
    --arg agent_name "swarm" \
    --arg provider "${PROVIDER}" \
    --arg model "${MODEL}" \
    --arg thinking "${THINKING}" \
    '{title:$title,workspace_path:$workspace_path,host_workspace_path:$host_workspace_path,runtime_workspace_path:$runtime_workspace_path,workspace_name:$workspace_name,mode:$mode,agent_name:$agent_name,preference:{provider:$provider,model:$model,thinking:$thinking},metadata:{launch_gate_checkpoint:"6"}}')"
  api_json POST "/v1/sessions?swarm_id=$(urlencode "${child_swarm_id}")" "${open_body}" "${cp_dir}/open_session.json" 60
  session_id="$(jq -r '.session.id // empty' "${cp_dir}/open_session.json")"
  [[ -n "${session_id}" ]] || fail "checkpoint 6 open did not return session id"
  printf '%s\n' "${session_id}" >"${cp_dir}/session_id.txt"

  api_json POST "/v1/sessions/${session_id}/messages" \
    "$(jq -nc --arg content "${CHECKPOINT6_PROMPT}" '{role:"user",content:$content}')" \
    "${cp_dir}/user_message.json" 60

  run_body="$(jq -nc --arg prompt "${CHECKPOINT6_PROMPT}" --arg workspace_path "${runtime_workspace_path}" '{type:"run.start",prompt:$prompt,background:true,execution_context:{workspace_path:$workspace_path,worktree_mode:"off"}}')"
  api_json POST "/v1/sessions/${session_id}/run/stream" "${run_body}" "${cp_dir}/run_start.json" 60
  run_id="$(jq -r '.run_id // empty' "${cp_dir}/run_start.json")"
  [[ -n "${run_id}" ]] || fail "checkpoint 6 run start did not return run_id"
  printf '%s\n' "${run_id}" >"${cp_dir}/run_id.txt"

  deadline=$((SECONDS + 240))
  while :; do
    api_json GET "/v1/sessions/${session_id}" "" "${cp_dir}/primary_session.json" 30
    api_json GET "/v1/sessions/${session_id}/messages?limit=100" "" "${cp_dir}/primary_messages_poll.json" 30
    if jq -e '[.messages[]? | select((.role // "") == "assistant" and ((.content // "") | contains("LAUNCH_GATE_CP6_OK")))] | length > 0' "${cp_dir}/primary_messages_poll.json" >/dev/null; then
      cp -- "${cp_dir}/primary_messages_poll.json" "${cp_dir}/primary_messages.json"
      break
    fi
    now=${SECONDS}
    if [[ "${now}" -ge "${deadline}" ]]; then
      cp -- "${cp_dir}/primary_messages_poll.json" "${cp_dir}/primary_messages.json"
      fail "checkpoint 6 timed out waiting for assistant proof token"
    fi
    sleep 5
  done

  api_json GET "/v1/sessions/${session_id}/metadata" "" "${cp_dir}/primary_session_metadata.json" 30
  assert_checkpoint6_route "${cp_dir}" "${session_id}" "${child_swarm_id}" "${host_container_id}" "${runtime_workspace_path}"
  assert_checkpoint6_messages "${cp_dir}" "${session_id}"
  api_json GET "/v1/swarm/mirror/resources?managed_swarm_id=${managed_query}&resources=container,deployment,target" "" "${cp_dir}/mirror_resources_after_run.json" 30

  cleanup_checkpoint6_container "${cp_dir}" "${deployment_id}" "${child_swarm_id}" "${host_container_id}" "${attachment_id}"
  api_json GET "/v1/swarm/topology/session-route?session_id=$(urlencode "${session_id}")" "" "${cp_dir}/primary_topology_session_route_after_delete.json" 30
  [[ "$(jq -r '.route | if . == null then 0 else 1 end' "${cp_dir}/primary_topology_session_route_after_delete.json")" == "0" ]] || fail "checkpoint 6 delete left routed session route ${session_id}"

  jq -nc \
    --arg deployment_id "${deployment_id}" \
    --arg child_swarm_id "${child_swarm_id}" \
    --arg managed_swarm_id "${MANAGED_SWARM_ID}" \
    --arg host_container_id "${host_container_id}" \
    --arg session_id "${session_id}" \
    --arg run_id "${run_id}" \
    --arg runtime_workspace_path "${runtime_workspace_path}" \
    '{deployment_id:$deployment_id,child_swarm_id:$child_swarm_id,managed_swarm_id:$managed_swarm_id,host_container_id:$host_container_id,session_id:$session_id,run_id:$run_id,runtime_workspace_path:$runtime_workspace_path,proof_token:"LAUNCH_GATE_CP6_OK",product_path:"primary /v1/sessions?swarm_id=child -> primary routed session proxy -> managed-host child container",fallback_allowed:false}' \
    >"${cp_dir}/checkpoint_6_summary.json"
  record_checkpoint "6" "PASS" "managed-host container session opened, messaged, and run through primary routed session APIs; assistant response mirrored, topology route verified, and delete cleaned route" "${cp_dir}"
}


ensure_flow_inputs() {
  [[ -n "${SOURCE_WORKSPACE_PATH}" ]] || fail "--source-workspace-path is required for flow checkpoints"
  if [[ -z "${MANAGED_WORKSPACE_PATH}" ]]; then
    MANAGED_WORKSPACE_PATH="${SOURCE_WORKSPACE_PATH}"
  fi
  if [[ -z "${CHECKPOINT_FLOW_AGENT_NAME}" ]]; then
    CHECKPOINT_FLOW_AGENT_NAME="swarm"
  fi
  if [[ -z "${CHECKPOINT_FLOW_AGENT_MODE}" ]]; then
    CHECKPOINT_FLOW_AGENT_MODE="primary"
  fi
}

flow_prompt_for_checkpoint() {
  local checkpoint="${1:-}"
  case "${checkpoint}" in
    7) printf '%s' "${CHECKPOINT7_PROMPT:-Launch Gate checkpoint 7 flow run. Reply with exactly: LAUNCH_GATE_CP7_OK}" ;;
    8) printf '%s' "${CHECKPOINT8_PROMPT:-Launch Gate checkpoint 8 container flow run. Reply with exactly: LAUNCH_GATE_CP8_OK}" ;;
    9) printf '%s' "${CHECKPOINT9_PROMPT:-Launch Gate checkpoint 9 managed-host flow run. Reply with exactly: LAUNCH_GATE_CP9_OK}" ;;
    10) printf '%s' "${CHECKPOINT10_PROMPT:-Launch Gate checkpoint 10 managed-container flow run. Reply with exactly: LAUNCH_GATE_CP10_OK}" ;;
    *) fail "unknown flow checkpoint prompt: ${checkpoint}" ;;
  esac
}

flow_token_for_checkpoint() {
  local checkpoint="${1:-}"
  case "${checkpoint}" in
    7) printf '%s' "LAUNCH_GATE_CP7_OK" ;;
    8) printf '%s' "LAUNCH_GATE_CP8_OK" ;;
    9) printf '%s' "LAUNCH_GATE_CP9_OK" ;;
    10) printf '%s' "LAUNCH_GATE_CP10_OK" ;;
    *) fail "unknown flow checkpoint token: ${checkpoint}" ;;
  esac
}

capture_flow_agent_state() {
  local cp_dir="${1:-}"
  api_json GET "/v2/agents?limit=200" "" "${cp_dir}/agents_state.json" 30
  jq -e --arg name "${CHECKPOINT_FLOW_AGENT_NAME}" '.state.profiles[]? | select((.name // "") == $name)' "${cp_dir}/agents_state.json" >"${cp_dir}/flow_agent_profile.json" \
    || fail "flow agent profile ${CHECKPOINT_FLOW_AGENT_NAME} was not found"
  local enabled mode
  enabled="$(jq -r '.enabled // true' "${cp_dir}/flow_agent_profile.json")"
  mode="$(jq -r '.mode // empty' "${cp_dir}/flow_agent_profile.json")"
  [[ "${enabled}" == "true" ]] || fail "flow agent profile ${CHECKPOINT_FLOW_AGENT_NAME} is disabled"
  [[ -n "${mode}" ]] || fail "flow agent profile ${CHECKPOINT_FLOW_AGENT_NAME} has empty mode"
}

flow_target_from_targets() {
  local targets_file="${1:-}"
  local selector="${2:-}"
  local output_file="${3:-}"
  case "${selector}" in
    self)
      jq -c '.targets[]? | select(((.relationship // "") == "self") or ((.kind // "") == "self"))' "${targets_file}" | head -n 1 | jq '.' >"${output_file}"
      ;;
    managed)
      jq -c --arg id "${MANAGED_SWARM_ID}" '.targets[]? | select((.swarm_id // "") == $id)' "${targets_file}" | head -n 1 | jq '.' >"${output_file}"
      ;;
    *)
      jq -c --arg id "${selector}" '.targets[]? | select((.swarm_id // "") == $id)' "${targets_file}" | head -n 1 | jq '.' >"${output_file}"
      ;;
  esac
  if [[ ! -s "${output_file}" ]] || ! jq empty "${output_file}" >/dev/null 2>&1; then
    fail "flow target ${selector} was not found in ${targets_file}"
  fi
}

flow_selection_from_target() {
  local target_file="${1:-}"
  local output_file="${2:-}"
  jq '{swarm_id:(.swarm_id // ""),kind:(.kind // ""),deployment_id:(.deployment_id // ""),name:(.name // "")}' "${target_file}" >"${output_file}"
  [[ -n "$(jq -r '.swarm_id // empty' "${output_file}")" ]] || fail "flow target selection has empty swarm_id"
}

flow_payload() {
  local flow_id="${1:-}"
  local name="${2:-}"
  local target_file="${3:-}"
  local prompt="${4:-}"
  local workspace_path="${5:-}"
  local host_workspace_path="${6:-}"
  local runtime_workspace_path="${7:-}"
  jq -nc \
    --arg flow_id "${flow_id}" \
    --arg name "${name}" \
    --slurpfile target "${target_file}" \
    --arg agent_name "${CHECKPOINT_FLOW_AGENT_NAME}" \
    --arg agent_mode "${CHECKPOINT_FLOW_AGENT_MODE}" \
    --arg workspace_path "${workspace_path}" \
    --arg host_workspace_path "${host_workspace_path}" \
    --arg runtime_workspace_path "${runtime_workspace_path}" \
    --arg prompt "${prompt}" \
    '{flow_id:$flow_id,name:$name,enabled:true,target:$target[0],agent:{profile_name:$agent_name,profile_mode:$agent_mode},workspace:{workspace_path:$workspace_path,host_workspace_path:$host_workspace_path,runtime_workspace_path:$runtime_workspace_path},schedule:{cadence:"on_demand",timezone:"UTC"},catch_up_policy:{mode:"once"},intent:{prompt:$prompt,mode:"one_shot_background"}}'
}

poll_flow_run_success() {
  local cp_dir="${1:-}"
  local flow_id="${2:-}"
  local token="${3:-}"
  local expected_swarm_id="${4:-}"
  local deadline=$((SECONDS + FLOW_RUN_TIMEOUT_SECONDS))
  local status session_id run_id now
  while :; do
    api_json GET "/v3/flows/${flow_id}/status?limit=100" "" "${cp_dir}/flow_status_poll.json" 30
    api_json GET "/v3/flows/${flow_id}/history?limit=100" "" "${cp_dir}/flow_history_poll.json" 30
    status="$(jq -r '[.history[]? | select((.status // "") == "success" or (.status // "") == "failed")] | sort_by(.started_at // .scheduled_at // "") | last | .status // empty' "${cp_dir}/flow_history_poll.json")"
    session_id="$(jq -r '[.history[]? | select((.status // "") == "success" or (.status // "") == "failed")] | sort_by(.started_at // .scheduled_at // "") | last | .session_id // empty' "${cp_dir}/flow_history_poll.json")"
    run_id="$(jq -r '[.history[]? | select((.status // "") == "success" or (.status // "") == "failed")] | sort_by(.started_at // .scheduled_at // "") | last | .run_id // empty' "${cp_dir}/flow_history_poll.json")"
    if [[ "${status}" == "success" && -n "${session_id}" ]]; then
      cp -- "${cp_dir}/flow_status_poll.json" "${cp_dir}/flow_status.json"
      cp -- "${cp_dir}/flow_history_poll.json" "${cp_dir}/flow_history.json"
      printf '%s\n' "${session_id}" >"${cp_dir}/flow_session_id.txt"
      printf '%s\n' "${run_id}" >"${cp_dir}/flow_run_id.txt"
      break
    fi
    if [[ "${status}" == "failed" ]]; then
      cp -- "${cp_dir}/flow_status_poll.json" "${cp_dir}/flow_status.json"
      cp -- "${cp_dir}/flow_history_poll.json" "${cp_dir}/flow_history.json"
      fail "flow ${flow_id} failed: $(jq -c '[.history[]? | select((.status // "") == "failed")] | last' "${cp_dir}/flow_history.json")"
    fi
    now=${SECONDS}
    if [[ "${now}" -ge "${deadline}" ]]; then
      cp -- "${cp_dir}/flow_status_poll.json" "${cp_dir}/flow_status.json"
      cp -- "${cp_dir}/flow_history_poll.json" "${cp_dir}/flow_history.json"
      fail "timed out waiting for flow ${flow_id} success"
    fi
    sleep 5
  done

  api_json GET "/v1/sessions/${session_id}" "" "${cp_dir}/flow_session.json" 30
  api_json GET "/v1/sessions/${session_id}/metadata" "" "${cp_dir}/flow_session_metadata.json" 30
  api_json GET "/v1/sessions/${session_id}/messages?limit=100" "" "${cp_dir}/flow_messages.json" 30
  [[ "$(jq -r --arg token "${token}" '[.messages[]? | select((.role // "") == "assistant" and ((.content // "") | contains($token)))] | length' "${cp_dir}/flow_messages.json")" -ge 1 ]] \
    || fail "flow ${flow_id} assistant messages did not contain ${token}"
  if [[ -n "${expected_swarm_id}" ]]; then
    api_json GET "/v1/swarm/topology/session-route?session_id=$(urlencode "${session_id}")" "" "${cp_dir}/flow_session_route.json" 30
    if [[ "$(jq -r '.route | if . == null then 0 else 1 end' "${cp_dir}/flow_session_route.json")" == "1" ]]; then
      local route_runtime route_child
      route_runtime="$(jq -r '.route.runtime_swarm_id // empty' "${cp_dir}/flow_session_route.json")"
      route_child="$(jq -r '.route.child_swarm_id // empty' "${cp_dir}/flow_session_route.json")"
      [[ "${route_runtime}" == "${expected_swarm_id}" || "${route_child}" == "${expected_swarm_id}" ]] \
        || fail "flow ${flow_id} session route did not point at ${expected_swarm_id}"
    else
      [[ "$(jq -r --arg id "${expected_swarm_id}" '(.metadata.target_swarm_id // .metadata.swarm_target_swarm_id // .metadata.swarm_routed_child_swarm_id // empty) == $id' "${cp_dir}/flow_session_metadata.json")" == "true" ]] \
        || fail "flow ${flow_id} had no topology route and metadata did not point at ${expected_swarm_id}"
    fi
  fi
}

create_run_verify_flow() {
  local cp_dir="${1:-}"
  local checkpoint="${2:-}"
  local target_selection_file="${3:-}"
  local workspace_path="${4:-}"
  local host_workspace_path="${5:-}"
  local runtime_workspace_path="${6:-}"
  local expected_swarm_id="${7:-}"
  local flow_id="launch-gate-cp${checkpoint}-$(date +%Y%m%d-%H%M%S)"
  local name="Launch Gate CP${checkpoint} flow"
  local prompt token payload
  prompt="$(flow_prompt_for_checkpoint "${checkpoint}")"
  token="$(flow_token_for_checkpoint "${checkpoint}")"
  payload="$(flow_payload "${flow_id}" "${name}" "${target_selection_file}" "${prompt}" "${workspace_path}" "${host_workspace_path}" "${runtime_workspace_path}")"
  printf '%s' "${payload}" >"${cp_dir}/flow_create_request.json"
  api_json POST "/v3/flows" "${payload}" "${cp_dir}/flow_create_response.json" 60
  [[ "$(jq -r '.ok // false' "${cp_dir}/flow_create_response.json")" == "true" ]] || fail "checkpoint ${checkpoint} flow create ok=false"
  api_json POST "/v3/flows/${flow_id}/run-now" "" "${cp_dir}/flow_run_now_response.json" 60
  [[ "$(jq -r '.ok // false' "${cp_dir}/flow_run_now_response.json")" == "true" ]] || fail "checkpoint ${checkpoint} flow run-now ok=false"
  poll_flow_run_success "${cp_dir}" "${flow_id}" "${token}" "${expected_swarm_id}"
  printf '%s\n' "${flow_id}" >"${cp_dir}/flow_id.txt"
  jq -nc --arg flow_id "${flow_id}" --arg token "${token}" --arg expected_swarm_id "${expected_swarm_id}" '{flow_id:$flow_id,proof_token:$token,expected_target_swarm_id:$expected_swarm_id}' >"${cp_dir}/flow_run_summary.json"
}

wait_for_target_by_swarm_id() {
  local cp_dir="${1:-}"
  local swarm_id="${2:-}"
  local output_file="${3:-}"
  local deadline=$((SECONDS + 60))
  while :; do
    api_json GET "/v1/swarm/targets?swarm_id=$(urlencode "${swarm_id}")" "" "${cp_dir}/targets_${swarm_id}_poll.json" 30
    if jq -e --arg id "${swarm_id}" '.targets[]? | select((.swarm_id // "") == $id)' "${cp_dir}/targets_${swarm_id}_poll.json" >/dev/null; then
      jq -c --arg id "${swarm_id}" '.targets[]? | select((.swarm_id // "") == $id)' "${cp_dir}/targets_${swarm_id}_poll.json" | head -n 1 | jq '.' >"${output_file}"
      return 0
    fi
    [[ "${SECONDS}" -lt "${deadline}" ]] || fail "timed out waiting for target ${swarm_id}"
    sleep 3
  done
}

create_flow_container() {
  local cp_dir="${1:-}"
  local checkpoint="${2:-}"
  local container_name="${3:-}"
  local target_host_swarm_id="${4:-}"
  local payload
  payload="$(jq -nc \
    --arg mode "local" \
    --arg swarm_name "${container_name}" \
    --arg target_host_swarm_id "${target_host_swarm_id}" \
    --arg runtime "${CHECKPOINT4_RUNTIME}" \
    --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
    '{mode:$mode,swarm_name:$swarm_name,sync:{enabled:true,mode:"managed"},workspaces:[{source_workspace_path:$source_workspace_path,replication_mode:"bundle",writable:true}]} + (if $target_host_swarm_id != "" then {target_host_swarm_id:$target_host_swarm_id} else {} end) + (if $runtime != "" then {runtime:$runtime} else {} end)')"
  printf '%s' "${payload}" | jq 'del(.sync.vault_password)' >"${cp_dir}/replicate_request.redacted.json"
  api_json POST "/v1/swarm/replicate" "${payload}" "${cp_dir}/replicate_response.json" 240
  [[ "$(jq -r '.ok // false' "${cp_dir}/replicate_response.json")" == "true" ]] || fail "checkpoint ${checkpoint} replicate response ok=false"
  FLOW_DEPLOYMENT_ID="$(jq -r '.swarm.deployment_id // empty' "${cp_dir}/replicate_response.json")"
  FLOW_CHILD_SWARM_ID="$(jq -r '.swarm.id // empty' "${cp_dir}/replicate_response.json")"
  FLOW_RUNTIME_WORKSPACE_PATH="$(jq -r '.workspaces[0].binding.destination_workspace_path // empty' "${cp_dir}/replicate_response.json")"
  [[ -n "${FLOW_DEPLOYMENT_ID}" ]] || fail "checkpoint ${checkpoint} missing deployment id"
  [[ -n "${FLOW_CHILD_SWARM_ID}" ]] || fail "checkpoint ${checkpoint} missing child swarm id"
  [[ -n "${FLOW_RUNTIME_WORKSPACE_PATH}" ]] || fail "checkpoint ${checkpoint} missing runtime workspace path"
  api_json GET "/v1/deploy/container" "" "${cp_dir}/deployments_after_create.json" 30
  FLOW_HOST_CONTAINER_ID="$(jq -r --arg id "${FLOW_DEPLOYMENT_ID}" '.deployments[]? | select(.id == $id) | .host_container_id // empty' "${cp_dir}/deployments_after_create.json" | head -n 1)"
  FLOW_ATTACHMENT_ID="$(jq -r --arg id "${FLOW_DEPLOYMENT_ID}" '.deployments[]? | select(.id == $id) | .attachment_id // empty' "${cp_dir}/deployments_after_create.json" | head -n 1)"
  [[ -n "${FLOW_HOST_CONTAINER_ID}" ]] || fail "checkpoint ${checkpoint} missing host container id"
  [[ -n "${FLOW_ATTACHMENT_ID}" ]] || fail "checkpoint ${checkpoint} missing attachment id"
  api_json GET "/v1/swarm/topology" "" "${cp_dir}/topology_after_container_create.json" 30
  wait_for_target_by_swarm_id "${cp_dir}" "${FLOW_CHILD_SWARM_ID}" "${cp_dir}/container_target.json"
  flow_selection_from_target "${cp_dir}/container_target.json" "${cp_dir}/container_flow_target_selection.json"
}

deactivate_flow_before_container_delete() {
  local cp_dir="${1:-}"
  local flow_id="${2:-}"
  api_json PUT "/v3/flows/${flow_id}" "$(jq -nc '{enabled:false,target:{},unassign_target:true}')" "${cp_dir}/flow_deactivate_response.json" 60
  [[ "$(jq -r '.ok // false' "${cp_dir}/flow_deactivate_response.json")" == "true" ]] || fail "flow ${flow_id} deactivate ok=false"
  api_json GET "/v3/flows/${flow_id}" "" "${cp_dir}/flow_after_deactivate.json" 30
  [[ "$(jq -r '.definition.enabled // true' "${cp_dir}/flow_after_deactivate.json")" == "false" ]] || fail "flow ${flow_id} was not disabled before container delete"
  [[ "$(jq -r '[(.definition.target.swarm_id // ""),(.definition.target.kind // ""),(.definition.target.deployment_id // ""),(.definition.target.name // "")] | map(select(. != "")) | length' "${cp_dir}/flow_after_deactivate.json")" == "0" ]] \
    || fail "flow ${flow_id} target was not cleared before container delete"
}

delete_flow_container_and_verify() {
  local cp_dir="${1:-}"
  local checkpoint="${2:-}"
  local flow_id="${3:-}"
  local deployment_id="${4:-}"
  local child_swarm_id="${5:-}"
  local host_container_id="${6:-}"
  local attachment_id="${7:-}"
  api_json POST "/v1/deploy/container/delete" "$(jq -nc --arg id "${deployment_id}" '{ids:[$id]}')" "${cp_dir}/delete_response.json" 180
  [[ "$(jq -r '.ok // false' "${cp_dir}/delete_response.json")" == "true" ]] || fail "checkpoint ${checkpoint} container delete ok=false"
  assert_checkpoint5_deleted_absent "${cp_dir}" "${deployment_id}" "${child_swarm_id}" "${host_container_id}" "${attachment_id}"
  api_json GET "/v3/flows/${flow_id}" "" "${cp_dir}/flow_after_container_delete.json" 30
  [[ "$(jq -r '.definition.enabled // true' "${cp_dir}/flow_after_container_delete.json")" == "false" ]] || fail "flow ${flow_id} was not disabled after container delete"
  [[ "$(jq -r '[(.definition.target.swarm_id // ""),(.definition.target.kind // ""),(.definition.target.deployment_id // ""),(.definition.target.name // "")] | map(select(. != "")) | length' "${cp_dir}/flow_after_container_delete.json")" == "0" ]] \
    || fail "flow ${flow_id} target was not cleared after container delete"
}

checkpoint_7() {
  local cp_dir="${EVIDENCE_DIR}/checkpoint-7"
  mkdir -p -- "${cp_dir}"
  log "== checkpoint 7: Flow A primary main flow =="
  ensure_flow_inputs
  capture_flow_agent_state "${cp_dir}"
  api_json GET "/v1/swarm/targets" "" "${cp_dir}/targets.json" 30
  flow_target_from_targets "${cp_dir}/targets.json" self "${cp_dir}/self_target.json"
  flow_selection_from_target "${cp_dir}/self_target.json" "${cp_dir}/flow_target_selection.json"
  local self_swarm_id
  self_swarm_id="$(jq -r '.swarm_id // empty' "${cp_dir}/self_target.json")"
  create_run_verify_flow "${cp_dir}" 7 "${cp_dir}/flow_target_selection.json" "${SOURCE_WORKSPACE_PATH}" "${SOURCE_WORKSPACE_PATH}" "${SOURCE_WORKSPACE_PATH}" "${self_swarm_id}"
  record_checkpoint "7" "PASS" "primary main flow created and run; primary status/history/report session and assistant proof token verified" "${cp_dir}"
}

checkpoint_8() {
  local cp_dir="${EVIDENCE_DIR}/checkpoint-8"
  mkdir -p -- "${cp_dir}"
  log "== checkpoint 8: Flow B primary local container flow =="
  ensure_flow_inputs
  capture_flow_agent_state "${cp_dir}"
  local container_name flow_id
  container_name="${CHECKPOINT8_CONTAINER_NAME:-launch-gate-cp8-$(date +%Y%m%d-%H%M%S)}"
  create_flow_container "${cp_dir}" 8 "${container_name}" ""
  create_run_verify_flow "${cp_dir}" 8 "${cp_dir}/container_flow_target_selection.json" "${SOURCE_WORKSPACE_PATH}" "${SOURCE_WORKSPACE_PATH}" "${FLOW_RUNTIME_WORKSPACE_PATH}" "${FLOW_CHILD_SWARM_ID}"
  flow_id="$(cat "${cp_dir}/flow_id.txt")"
  deactivate_flow_before_container_delete "${cp_dir}" "${flow_id}"
  delete_flow_container_and_verify "${cp_dir}" 8 "${flow_id}" "${FLOW_DEPLOYMENT_ID}" "${FLOW_CHILD_SWARM_ID}" "${FLOW_HOST_CONTAINER_ID}" "${FLOW_ATTACHMENT_ID}"
  jq -nc --arg flow_id "${flow_id}" --arg deployment_id "${FLOW_DEPLOYMENT_ID}" --arg child_swarm_id "${FLOW_CHILD_SWARM_ID}" '{flow_id:$flow_id,deployment_id:$deployment_id,child_swarm_id:$child_swarm_id,deactivation:"flow disabled and target cleared before API container delete; state verified after delete"}' >"${cp_dir}/checkpoint_8_summary.json"
  record_checkpoint "8" "PASS" "primary local container flow created/run with route/report/history proof; flow deactivated and target cleared before container delete, then verified after delete" "${cp_dir}"
}

checkpoint_9() {
  local cp_dir="${EVIDENCE_DIR}/checkpoint-9"
  mkdir -p -- "${cp_dir}"
  log "== checkpoint 9: Flow C managed host main flow =="
  if [[ -z "${MANAGED_SWARM_ID}" ]]; then
    api_json GET "/v1/swarm/targets" "" "${cp_dir}/targets_for_resolve.json" 30
    resolve_managed_target "${cp_dir}/targets_for_resolve.json" "${cp_dir}/managed_target_resolved.json"
  fi
  ensure_flow_inputs
  capture_flow_agent_state "${cp_dir}"
  api_json GET "/v1/swarm/targets" "" "${cp_dir}/targets.json" 30
  flow_target_from_targets "${cp_dir}/targets.json" managed "${cp_dir}/managed_target.json"
  flow_selection_from_target "${cp_dir}/managed_target.json" "${cp_dir}/flow_target_selection.json"
  create_run_verify_flow "${cp_dir}" 9 "${cp_dir}/flow_target_selection.json" "${SOURCE_WORKSPACE_PATH}" "${SOURCE_WORKSPACE_PATH}" "${MANAGED_WORKSPACE_PATH}" "${MANAGED_SWARM_ID}"
  record_checkpoint "9" "PASS" "managed-host main flow created from primary, assignment executed on managed host, and report/history/session proof returned to primary" "${cp_dir}"
}

checkpoint_10() {
  local cp_dir="${EVIDENCE_DIR}/checkpoint-10"
  mkdir -p -- "${cp_dir}"
  log "== checkpoint 10: Flow D managed-host container flow =="
  if [[ -z "${MANAGED_SWARM_ID}" ]]; then
    api_json GET "/v1/swarm/targets" "" "${cp_dir}/targets_for_resolve.json" 30
    resolve_managed_target "${cp_dir}/targets_for_resolve.json" "${cp_dir}/managed_target_resolved.json"
  fi
  ensure_flow_inputs
  capture_flow_agent_state "${cp_dir}"
  local container_name flow_id managed_query
  container_name="${CHECKPOINT10_CONTAINER_NAME:-launch-gate-cp10-$(date +%Y%m%d-%H%M%S)}"
  create_flow_container "${cp_dir}" 10 "${container_name}" "${MANAGED_SWARM_ID}"
  managed_query="$(urlencode "${MANAGED_SWARM_ID}")"
  api_json GET "/v1/swarm/mirror/resources?managed_swarm_id=${managed_query}&resources=container,deployment,target" "" "${cp_dir}/mirror_resources_after_container_create.json" 30
  create_run_verify_flow "${cp_dir}" 10 "${cp_dir}/container_flow_target_selection.json" "${SOURCE_WORKSPACE_PATH}" "${SOURCE_WORKSPACE_PATH}" "${FLOW_RUNTIME_WORKSPACE_PATH}" "${FLOW_CHILD_SWARM_ID}"
  flow_id="$(cat "${cp_dir}/flow_id.txt")"
  deactivate_flow_before_container_delete "${cp_dir}" "${flow_id}"
  delete_flow_container_and_verify "${cp_dir}" 10 "${flow_id}" "${FLOW_DEPLOYMENT_ID}" "${FLOW_CHILD_SWARM_ID}" "${FLOW_HOST_CONTAINER_ID}" "${FLOW_ATTACHMENT_ID}"
  api_json GET "/v1/swarm/mirror/resources?managed_swarm_id=${managed_query}&resources=container,deployment,target" "" "${cp_dir}/mirror_resources_after_delete.json" 30
  jq -nc --arg flow_id "${flow_id}" --arg deployment_id "${FLOW_DEPLOYMENT_ID}" --arg child_swarm_id "${FLOW_CHILD_SWARM_ID}" --arg managed_swarm_id "${MANAGED_SWARM_ID}" '{flow_id:$flow_id,deployment_id:$deployment_id,child_swarm_id:$child_swarm_id,managed_swarm_id:$managed_swarm_id,deactivation:"flow disabled and target cleared before managed-host API container delete; state verified after delete"}' >"${cp_dir}/checkpoint_10_summary.json"
  record_checkpoint "10" "PASS" "managed-host container flow created/run with primary-visible attribution/report/history; flow deactivated and target cleared before managed container delete, then verified after delete" "${cp_dir}"
}
checkpoint_2() {
  local cp_dir="${EVIDENCE_DIR}/checkpoint-2"
  mkdir -p -- "${cp_dir}"
  log "== checkpoint 2: managed host AI via primary API =="
  if [[ -z "${MANAGED_SWARM_ID}" ]]; then
    api_json GET "/v1/swarm/targets" "" "${cp_dir}/targets.json" 30
    resolve_managed_target "${cp_dir}/targets.json" "${cp_dir}/managed_target.json"
  fi
  ensure_checkpoint2_inputs

  local open_body session_id run_body run_id deadline now managed_query
  open_body="$(jq -nc \
    --arg target_swarm_id "${MANAGED_SWARM_ID}" \
    --arg title "Launch Gate checkpoint 2" \
    --arg workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg host_workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg runtime_workspace_path "${MANAGED_WORKSPACE_PATH}" \
    --arg workspace_name "$(basename "${SOURCE_WORKSPACE_PATH}")" \
    --arg mode "auto" \
    --arg agent_name "swarm" \
    --arg provider "${PROVIDER}" \
    --arg model "${MODEL}" \
    --arg thinking "${THINKING}" \
    '{target_swarm_id:$target_swarm_id,title:$title,workspace_path:$workspace_path,host_workspace_path:$host_workspace_path,runtime_workspace_path:$runtime_workspace_path,workspace_name:$workspace_name,mode:$mode,agent_name:$agent_name,preference:{provider:$provider,model:$model,thinking:$thinking},metadata:{launch_gate_checkpoint:"2"}}')"
  api_json POST "/v1/swarm/managed-hosts/sessions/open" "${open_body}" "${cp_dir}/open_session.json" 60
  session_id="$(jq -r '.session.id // empty' "${cp_dir}/open_session.json")"
  [[ -n "${session_id}" ]] || fail "checkpoint 2 open did not return session id"
  printf '%s\n' "${session_id}" >"${cp_dir}/session_id.txt"

  api_json POST "/v1/swarm/managed-hosts/sessions/message" \
    "$(jq -nc --arg target_swarm_id "${MANAGED_SWARM_ID}" --arg session_id "${session_id}" --arg content "${CHECKPOINT2_PROMPT}" '{target_swarm_id:$target_swarm_id,session_id:$session_id,role:"user",content:$content,metadata:{launch_gate_checkpoint:"2"}}')" \
    "${cp_dir}/user_message.json" 60

  run_body="$(jq -nc --arg session_id "${session_id}" --arg prompt "${CHECKPOINT2_PROMPT}" '{session_id:$session_id,type:"run.start",prompt:$prompt,background:true}')"
  api_json POST "/v1/sessions/${session_id}/run/stream?swarm_id=$(urlencode "${MANAGED_SWARM_ID}")" "${run_body}" "${cp_dir}/run_start.json" 60
  run_id="$(jq -r '.run_id // empty' "${cp_dir}/run_start.json")"
  [[ -n "${run_id}" ]] || fail "checkpoint 2 run start did not return run_id"
  printf '%s\n' "${run_id}" >"${cp_dir}/run_id.txt"

  deadline=$((SECONDS + 240))
  while :; do
    api_json GET "/v1/sessions/${session_id}" "" "${cp_dir}/primary_session.json" 30
    api_json GET "/v1/sessions/${session_id}/messages?limit=100" "" "${cp_dir}/primary_messages_poll.json" 30
    if jq -e '[.messages[]? | select((.role // "") == "assistant" and ((.content // "") | contains("LAUNCH_GATE_CP2_OK")))] | length > 0' "${cp_dir}/primary_messages_poll.json" >/dev/null; then
      cp -- "${cp_dir}/primary_messages_poll.json" "${cp_dir}/primary_messages.json"
      break
    fi
    now=${SECONDS}
    if [[ "${now}" -ge "${deadline}" ]]; then
      cp -- "${cp_dir}/primary_messages_poll.json" "${cp_dir}/primary_messages.json"
      fail "checkpoint 2 timed out waiting for assistant proof token"
    fi
    sleep 5
  done

  api_json GET "/v1/sessions/${session_id}/metadata" "" "${cp_dir}/primary_session_metadata.json" 30
  assert_checkpoint2_route "${cp_dir}" "${session_id}"
  assert_checkpoint2_messages "${cp_dir}" "${session_id}"

  managed_query="$(urlencode "${MANAGED_SWARM_ID}")"
  api_json GET "/v1/swarm/mirror/resources?managed_swarm_id=${managed_query}" "" "${cp_dir}/mirror_resources_after_run.json" 30
  jq -nc \
    --arg session_id "${session_id}" \
    --arg run_id "${run_id}" \
    --arg managed_swarm_id "${MANAGED_SWARM_ID}" \
    --arg managed_workspace_path "${MANAGED_WORKSPACE_PATH}" \
    '{session_id:$session_id,run_id:$run_id,managed_swarm_id:$managed_swarm_id,managed_workspace_path:$managed_workspace_path,proof_token:"LAUNCH_GATE_CP2_OK"}' \
    >"${cp_dir}/checkpoint_2_summary.json"
  record_checkpoint "2" "PASS" "managed-host session opened, messaged, run through primary API; assistant response mirrored and primary topology route verified" "${cp_dir}"
}

SCENARIO="0-1"
PRIMARY_URL="${SWARM_PRIMARY_URL:-http://127.0.0.1:7781}"
MANAGED_SWARM_ID="${SWARM_MANAGED_SWARM_ID:-}"
MANAGED_NAME="${SWARM_MANAGED_NAME:-}"
SOURCE_WORKSPACE_PATH="${SWARM_SOURCE_WORKSPACE_PATH:-}"
MANAGED_WORKSPACE_PATH="${SWARM_MANAGED_WORKSPACE_PATH:-}"
PROVIDER="${SWARM_PROVIDER:-}"
MODEL="${SWARM_MODEL:-}"
THINKING="${SWARM_THINKING:-low}"
CHECKPOINT2_PROMPT="${SWARM_LAUNCH_GATE_PROMPT:-}"
CHECKPOINT4_CONTAINER_NAME="${SWARM_LAUNCH_GATE_CONTAINER_NAME:-}"
CHECKPOINT5_CONTAINER_NAME="${SWARM_LAUNCH_GATE_CP5_CONTAINER_NAME:-${SWARM_LAUNCH_GATE_CONTAINER_NAME:-}}"
CHECKPOINT5_RECREATE_CONTAINER_NAME="${SWARM_LAUNCH_GATE_CP5_RECREATE_CONTAINER_NAME:-}"
CHECKPOINT6_CONTAINER_NAME="${SWARM_LAUNCH_GATE_CP6_CONTAINER_NAME:-${SWARM_LAUNCH_GATE_CONTAINER_NAME:-}}"
CHECKPOINT6_PROMPT="${SWARM_LAUNCH_GATE_CP6_PROMPT:-}"
CHECKPOINT7_PROMPT="${SWARM_LAUNCH_GATE_CP7_PROMPT:-}"
CHECKPOINT8_PROMPT="${SWARM_LAUNCH_GATE_CP8_PROMPT:-}"
CHECKPOINT9_PROMPT="${SWARM_LAUNCH_GATE_CP9_PROMPT:-}"
CHECKPOINT10_PROMPT="${SWARM_LAUNCH_GATE_CP10_PROMPT:-}"
CHECKPOINT8_CONTAINER_NAME="${SWARM_LAUNCH_GATE_CP8_CONTAINER_NAME:-}"
CHECKPOINT10_CONTAINER_NAME="${SWARM_LAUNCH_GATE_CP10_CONTAINER_NAME:-}"
CHECKPOINT_FLOW_AGENT_NAME="${SWARM_LAUNCH_GATE_FLOW_AGENT_NAME:-swarm}"
CHECKPOINT_FLOW_AGENT_MODE="${SWARM_LAUNCH_GATE_FLOW_AGENT_MODE:-primary}"
FLOW_RUN_TIMEOUT_SECONDS="${SWARM_LAUNCH_GATE_FLOW_TIMEOUT_SECONDS:-360}"
CHECKPOINT4_RUNTIME="${SWARM_LAUNCH_GATE_RUNTIME:-}"
EVIDENCE_DIR="${SWARM_LAUNCH_GATE_EVIDENCE_DIR:-}"
STATUS_FILE=""
COOKIE_FILE=""
API_STATUS=""
API_BODY=""
CLEANUP_EXISTING_MANAGED_CONTAINERS="false"
ALLOW_EXISTING_MANAGED_CONTAINERS="false"
ALLOW_EXISTING_MANAGED_SESSION_ROUTES="false"
VERIFY_TARGET_PICKER="true"
ALLOW_LOCAL_WORKSTATION_RUN="${SWARM_LAUNCH_GATE_ALLOW_LOCAL:-false}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --scenario) SCENARIO="${2:-}"; shift 2 ;;
    --primary-url) PRIMARY_URL="${2:-}"; shift 2 ;;
    --managed-swarm-id) MANAGED_SWARM_ID="${2:-}"; shift 2 ;;
    --managed-name) MANAGED_NAME="${2:-}"; shift 2 ;;
    --source-workspace-path) SOURCE_WORKSPACE_PATH="${2:-}"; shift 2 ;;
    --managed-workspace-path) MANAGED_WORKSPACE_PATH="${2:-}"; shift 2 ;;
    --provider) PROVIDER="${2:-}"; shift 2 ;;
    --model) MODEL="${2:-}"; shift 2 ;;
    --thinking) THINKING="${2:-}"; shift 2 ;;
    --prompt) CHECKPOINT2_PROMPT="${2:-}"; shift 2 ;;
    --container-name) CHECKPOINT4_CONTAINER_NAME="${2:-}"; CHECKPOINT5_CONTAINER_NAME="${2:-}"; CHECKPOINT6_CONTAINER_NAME="${2:-}"; shift 2 ;;
    --cp5-container-name) CHECKPOINT5_CONTAINER_NAME="${2:-}"; shift 2 ;;
    --cp5-recreate-container-name) CHECKPOINT5_RECREATE_CONTAINER_NAME="${2:-}"; shift 2 ;;
    --cp6-container-name) CHECKPOINT6_CONTAINER_NAME="${2:-}"; shift 2 ;;
    --cp6-prompt) CHECKPOINT6_PROMPT="${2:-}"; shift 2 ;;
    --cp7-prompt) CHECKPOINT7_PROMPT="${2:-}"; shift 2 ;;
    --cp8-prompt) CHECKPOINT8_PROMPT="${2:-}"; shift 2 ;;
    --cp9-prompt) CHECKPOINT9_PROMPT="${2:-}"; shift 2 ;;
    --cp10-prompt) CHECKPOINT10_PROMPT="${2:-}"; shift 2 ;;
    --cp8-container-name) CHECKPOINT8_CONTAINER_NAME="${2:-}"; shift 2 ;;
    --cp10-container-name) CHECKPOINT10_CONTAINER_NAME="${2:-}"; shift 2 ;;
    --flow-agent-name) CHECKPOINT_FLOW_AGENT_NAME="${2:-}"; shift 2 ;;
    --flow-agent-mode) CHECKPOINT_FLOW_AGENT_MODE="${2:-}"; shift 2 ;;
    --flow-timeout-seconds) FLOW_RUN_TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --runtime) CHECKPOINT4_RUNTIME="${2:-}"; shift 2 ;;
    --evidence-dir) EVIDENCE_DIR="${2:-}"; shift 2 ;;
    --cleanup-existing-managed-containers) CLEANUP_EXISTING_MANAGED_CONTAINERS="true"; shift ;;
    --allow-existing-managed-containers) ALLOW_EXISTING_MANAGED_CONTAINERS="true"; shift ;;
    --allow-existing-managed-session-routes) ALLOW_EXISTING_MANAGED_SESSION_ROUTES="true"; shift ;;
    --skip-target-picker) VERIFY_TARGET_PICKER="false"; shift ;;
    --allow-local-workstation-run) ALLOW_LOCAL_WORKSTATION_RUN="true"; shift ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done

PRIMARY_URL="${PRIMARY_URL%/}"
require_testbench_execution
require_command curl
require_command jq
init_evidence

case "${SCENARIO}" in
  0|checkpoint-0) checkpoint_0 ;;
  1|checkpoint-1) checkpoint_1 ;;
  2|checkpoint-2) start_desktop_session; checkpoint_2 ;;
  4|checkpoint-4) start_desktop_session; checkpoint_4 ;;
  5|checkpoint-5) start_desktop_session; checkpoint_5 ;;
  6|checkpoint-6) start_desktop_session; checkpoint_6 ;;
  7|checkpoint-7) start_desktop_session; checkpoint_7 ;;
  8|checkpoint-8) start_desktop_session; checkpoint_8 ;;
  9|checkpoint-9) start_desktop_session; checkpoint_9 ;;
  10|checkpoint-10) start_desktop_session; checkpoint_10 ;;
  0-1) checkpoint_0; checkpoint_1 ;;
  0-2) checkpoint_0; checkpoint_1; checkpoint_2 ;;
  0-4) checkpoint_0; checkpoint_1; checkpoint_2; checkpoint_4 ;;
  0-5) checkpoint_0; checkpoint_1; checkpoint_2; checkpoint_4; checkpoint_5 ;;
  0-6) checkpoint_0; checkpoint_1; checkpoint_2; checkpoint_4; checkpoint_5; checkpoint_6 ;;
  7-10) start_desktop_session; checkpoint_7; checkpoint_8; checkpoint_9; checkpoint_10 ;;
  0-10|all) checkpoint_0; checkpoint_1; checkpoint_2; checkpoint_4; checkpoint_5; checkpoint_6; checkpoint_7; checkpoint_8; checkpoint_9; checkpoint_10 ;;
  *) fail "unknown scenario: ${SCENARIO}" ;;
esac

log "evidence: ${EVIDENCE_DIR}"
log "status: ${STATUS_FILE}"
