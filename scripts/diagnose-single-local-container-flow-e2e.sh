#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/diagnose-single-local-container-flow-e2e.sh [options]

SSH-only focused live E2E diagnostic: on one primary API, create exactly
one local child container, create one /v3/flows Flow targeting that child, run it
once with /run-now, and verify the proof token appears in the resulting session
messages.

No managed host, no cron, no unit tests, no caller-supplied target ids.

Options:
  --primary-url <url>              Primary swarmd API URL. Default: http://127.0.0.1:7781
  --workspace <path>               Source workspace path. Default: current repository root.
  --container-name <name>          Diagnostic child name. Default: single-local-flow-<timestamp>
  --artifact-dir <path>            Evidence directory. Default: tmp/single-local-container-flow-diagnostics/<timestamp>
  --flow-agent-name <name>         Saved agent profile_name. Default: swarm
  --flow-agent-mode <mode>         Saved agent profile_mode. Default: primary
  --flow-provider <provider>       Model provider to set before the Flow. Default: fireworks
  --flow-model <model>             Model to set before the Flow. Default: accounts/fireworks/models/kimi-k2p6
  --flow-thinking <level>          Thinking level to set before the Flow. Default: low
  --fireworks-key-path <path>      Fireworks API key file to seed if no active credential exists.
                                  If omitted, auto-detects exactly one key file under ${TMPDIR:-/tmp} matching *fireworks*.key.
  --runtime <podman|docker>        Optional requested runtime passed to replicate.
  --timeout-seconds <seconds>      Wait timeout for container/Flow success. Default: 420
  --cleanup                        Disable/delete the created Flow/container on exit.
  --help                           Show this help.

Environment equivalents:
  SWARM_PRIMARY_URL, SWARM_SOURCE_WORKSPACE_PATH, SWARM_SINGLE_LOCAL_FLOW_CONTAINER_NAME,
  SWARM_SINGLE_LOCAL_FLOW_ARTIFACT_DIR, SWARM_SINGLE_FLOW_AGENT_NAME,
  SWARM_SINGLE_FLOW_AGENT_MODE, SWARM_SINGLE_FLOW_PROVIDER, SWARM_SINGLE_FLOW_MODEL,
  SWARM_SINGLE_FLOW_THINKING, SWARM_FIREWORKS_KEY_PATH, SWARM_SINGLE_FLOW_TIMEOUT_SECONDS,
  SWARM_SINGLE_LOCAL_FLOW_RUNTIME, SWARMD_TOKEN
EOF
}

log() { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "${1:-}" >/dev/null 2>&1 || fail "required command not found: ${1:-}"; }
urlencode() { jq -rn --arg v "${1:-}" '$v|@uri'; }
json_get() { jq -r "${2:-.}" "${1:-}"; }

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
PRIMARY_URL="${SWARM_PRIMARY_URL:-http://127.0.0.1:7781}"
WORKSPACE_PATH="${SWARM_SOURCE_WORKSPACE_PATH:-${ROOT_DIR}}"
CONTAINER_NAME="${SWARM_SINGLE_LOCAL_FLOW_CONTAINER_NAME:-single-local-flow-$(date +%Y%m%d-%H%M%S)}"
ARTIFACT_DIR="${SWARM_SINGLE_LOCAL_FLOW_ARTIFACT_DIR:-}"
FLOW_AGENT_NAME="${SWARM_SINGLE_FLOW_AGENT_NAME:-swarm}"
FLOW_AGENT_MODE="${SWARM_SINGLE_FLOW_AGENT_MODE:-primary}"
FLOW_PROVIDER="${SWARM_SINGLE_FLOW_PROVIDER:-fireworks}"
FLOW_MODEL="${SWARM_SINGLE_FLOW_MODEL:-accounts/fireworks/models/kimi-k2p6}"
FLOW_THINKING="${SWARM_SINGLE_FLOW_THINKING:-low}"
FIREWORKS_KEY_PATH="${SWARM_FIREWORKS_KEY_PATH:-}"
RUNTIME="${SWARM_SINGLE_LOCAL_FLOW_RUNTIME:-}"
TIMEOUT_SECONDS="${SWARM_SINGLE_FLOW_TIMEOUT_SECONDS:-420}"
CLEANUP="false"
COOKIE_FILE=""
API_STATUS=""
API_BODY=""
FLOW_ID=""
DEPLOYMENT_ID=""
CHILD_SWARM_ID=""
RUNTIME_WORKSPACE_PATH=""
WORKSPACE_BINDING_ID=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --primary-url) PRIMARY_URL="${2:-}"; shift 2 ;;
    --workspace|--source-workspace-path) WORKSPACE_PATH="${2:-}"; shift 2 ;;
    --container-name) CONTAINER_NAME="${2:-}"; shift 2 ;;
    --artifact-dir) ARTIFACT_DIR="${2:-}"; shift 2 ;;
    --flow-agent-name) FLOW_AGENT_NAME="${2:-}"; shift 2 ;;
    --flow-agent-mode) FLOW_AGENT_MODE="${2:-}"; shift 2 ;;
    --flow-provider) FLOW_PROVIDER="${2:-}"; shift 2 ;;
    --flow-model) FLOW_MODEL="${2:-}"; shift 2 ;;
    --flow-thinking) FLOW_THINKING="${2:-}"; shift 2 ;;
    --fireworks-key-path) FIREWORKS_KEY_PATH="${2:-}"; shift 2 ;;
    --runtime) RUNTIME="${2:-}"; shift 2 ;;
    --timeout-seconds) TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --cleanup) CLEANUP="true"; shift ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

require_command curl
require_command jq
[[ -n "${PRIMARY_URL}" ]] || fail "--primary-url is required"
[[ -n "${WORKSPACE_PATH}" ]] || fail "--workspace is required"
[[ -n "${CONTAINER_NAME}" ]] || fail "--container-name is required"
[[ -n "${FLOW_AGENT_NAME}" ]] || fail "--flow-agent-name is required"
[[ -n "${FLOW_AGENT_MODE}" ]] || fail "--flow-agent-mode is required"
[[ -n "${FLOW_PROVIDER}" ]] || fail "--flow-provider is required"
[[ -n "${FLOW_MODEL}" ]] || fail "--flow-model is required"
[[ -n "${FLOW_THINKING}" ]] || fail "--flow-thinking is required"
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ && "${TIMEOUT_SECONDS}" -gt 0 ]] || fail "--timeout-seconds must be a positive integer"
PRIMARY_URL="${PRIMARY_URL%/}"
if [[ -z "${ARTIFACT_DIR}" ]]; then
  ARTIFACT_DIR="${ROOT_DIR}/.tmp/single-local-container-flow-diagnostics/$(date +%Y%m%d-%H%M%S)"
fi
mkdir -p -- "${ARTIFACT_DIR}"
COOKIE_FILE="${ARTIFACT_DIR}/primary.cookies"
: >"${COOKIE_FILE}"

api_request_capture() {
  local method="${1:-GET}" path="${2:-}" body="${3:-}" output_file="${4:-}" max_time="${5:-30}"
  local response_file payload_file http_code
  response_file="$(mktemp)"
  payload_file=""
  local args=(-sS --connect-timeout 3 --max-time "${max_time}" -o "${response_file}" -w '%{http_code}'
    -H 'Accept: application/json'
    -H "Origin: ${PRIMARY_URL}"
    -H "Referer: ${PRIMARY_URL}/"
    -H 'Sec-Fetch-Site: same-origin'
    -c "${COOKIE_FILE}" -b "${COOKIE_FILE}" -X "${method}")
  if [[ -n "${SWARMD_TOKEN:-}" ]]; then
    args+=(-H "Authorization: Bearer ${SWARMD_TOKEN}")
  fi
  if [[ -n "${body}" ]]; then
    payload_file="$(mktemp)"
    printf '%s' "${body}" >"${payload_file}"
    args+=(-H 'Content-Type: application/json' --data-binary "@${payload_file}")
  fi
  if http_code="$(curl "${args[@]}" "${PRIMARY_URL}${path}")"; then :; else http_code="000"; fi
  [[ -z "${output_file}" ]] || cp -- "${response_file}" "${output_file}"
  API_STATUS="${http_code}"
  API_BODY="$(cat -- "${response_file}")"
  rm -f -- "${response_file}"
  [[ -z "${payload_file}" ]] || rm -f -- "${payload_file}"
}

api_json() {
  local method="${1:-GET}" path="${2:-}" body="${3:-}" output_file="${4:-}" max_time="${5:-30}"
  api_request_capture "${method}" "${path}" "${body}" "${output_file}" "${max_time}"
  if [[ "${API_STATUS}" != 2* ]]; then
    fail "${method} ${path} failed with HTTP ${API_STATUS}: ${API_BODY}"
  fi
  [[ -z "${output_file}" ]] || jq empty "${output_file}" >/dev/null
}

cleanup_created() {
  if [[ "${CLEANUP}" != "true" ]]; then
    return 0
  fi
  set +e
  if [[ -n "${FLOW_ID}" ]]; then
    api_json PUT "/v3/flows/${FLOW_ID}" "$(jq -nc '{enabled:false,target:{},unassign_target:true}')" "${ARTIFACT_DIR}/cleanup_flow_disable.json" 60 || true
    api_json DELETE "/v3/flows/${FLOW_ID}" "" "${ARTIFACT_DIR}/cleanup_flow_delete.json" 60 || true
  fi
  if [[ -n "${DEPLOYMENT_ID}" ]]; then
    api_json POST "/v1/deploy/container/delete" "$(jq -nc --arg id "${DEPLOYMENT_ID}" '{ids:[$id]}')" "${ARTIFACT_DIR}/cleanup_container_delete.json" 180 || true
  fi
  set -e
}
trap cleanup_created EXIT

configure_model_defaults() {
  local body
  body="$(jq -nc --arg provider "${FLOW_PROVIDER}" --arg model "${FLOW_MODEL}" --arg thinking "${FLOW_THINKING}" '{provider:$provider,model:$model,thinking:$thinking}')"
  printf '%s\n' "${body}" >"${ARTIFACT_DIR}/model_config_request.json"
  api_json POST "/v1/model" "${body}" "${ARTIFACT_DIR}/model_config_response.json" 60
}

seed_fireworks_credential_if_needed() {
  if [[ "${FLOW_PROVIDER}" != "fireworks" ]]; then
    printf '{"skipped":true,"reason":"provider is not fireworks"}\n' >"${ARTIFACT_DIR}/fireworks_credential_seed.json"
    return 0
  fi
  api_json GET "/v1/auth/credentials?provider=fireworks&limit=20" "" "${ARTIFACT_DIR}/fireworks_credentials_before.json" 30
  if jq -e '[.credentials[]? | select((.active // false) == true)] | length > 0' "${ARTIFACT_DIR}/fireworks_credentials_before.json" >/dev/null; then
    printf '{"skipped":true,"reason":"active fireworks credential already exists"}\n' >"${ARTIFACT_DIR}/fireworks_credential_seed.json"
    return 0
  fi
  local key_path="${FIREWORKS_KEY_PATH}"
  if [[ -z "${key_path}" ]]; then
    mapfile -t candidates < <(find "${TMPDIR:-/tmp}" -maxdepth 1 -type f -iname '*fireworks*.key' 2>/dev/null | sort)
    if [[ "${#candidates[@]}" -eq 1 ]]; then
      key_path="${candidates[0]}"
    else
      fail "no active fireworks credential and auto-detect found ${#candidates[@]} key files; pass --fireworks-key-path or set SWARM_FIREWORKS_KEY_PATH"
    fi
  fi
  [[ -s "${key_path}" ]] || fail "fireworks key file is missing or empty: ${key_path}"
  local key body
  key="$(tr -d '\r\n' <"${key_path}")"
  [[ -n "${key}" ]] || fail "fireworks key file is empty after trimming: ${key_path}"
  body="$(jq -nc --arg api_key "${key}" '{provider:"fireworks",type:"api",label:"single-local-container-flow diagnostic fireworks",api_key:$api_key,active:true}')"
  api_json POST "/v1/auth/credentials" "${body}" "${ARTIFACT_DIR}/fireworks_credential_seed_response.json" 90
  jq -nc --arg key_path "${key_path}" '{ok:true,key_path:$key_path,response_file:"fireworks_credential_seed_response.json"}' >"${ARTIFACT_DIR}/fireworks_credential_seed.json"
}

wait_for_child_target() {
  local encoded_child deadline
  encoded_child="$(urlencode "${CHILD_SWARM_ID}")"
  deadline=$((SECONDS + TIMEOUT_SECONDS))
  while :; do
    api_json GET "/v1/swarm/targets?swarm_id=${encoded_child}" "" "${ARTIFACT_DIR}/target_poll.json" 30
    if jq -e --arg id "${CHILD_SWARM_ID}" '.targets[]? | select((.swarm_id // "") == $id and (.online // false) == true and (.selectable // false) == true)' "${ARTIFACT_DIR}/target_poll.json" >/dev/null; then
      jq -c --arg id "${CHILD_SWARM_ID}" '.targets[]? | select((.swarm_id // "") == $id)' "${ARTIFACT_DIR}/target_poll.json" | head -n 1 | jq '.' >"${ARTIFACT_DIR}/target.json"
      return 0
    fi
    [[ "${SECONDS}" -lt "${deadline}" ]] || fail "timed out waiting for child target ${CHILD_SWARM_ID} online/selectable"
    sleep 3
  done
}

flow_target_json() {
  jq -c '{swarm_id:(.swarm_id // ""),kind:(.kind // ""),deployment_id:(.deployment_id // ""),name:(.name // "")} | with_entries(select(.value != ""))' "${1:-}"
}

flow_workspace_json() {
  jq -nc \
    --arg workspace_path "${WORKSPACE_PATH}" \
    --arg host_workspace_path "${WORKSPACE_PATH}" \
    --arg runtime_workspace_path "${RUNTIME_WORKSPACE_PATH}" \
    --arg workspace_binding_id "${WORKSPACE_BINDING_ID}" \
    --arg workspace_name "$(basename "${WORKSPACE_PATH}")" \
    '{workspace_path:$workspace_path,host_workspace_path:$host_workspace_path,runtime_workspace_path:$runtime_workspace_path,workspace_binding_id:$workspace_binding_id,workspace_name:$workspace_name,cwd:$workspace_path}'
}

log "single-local-container-flow diagnostic: primary=${PRIMARY_URL} workspace=${WORKSPACE_PATH} artifacts=${ARTIFACT_DIR}"
api_json GET "/v1/auth/desktop/session" "" "${ARTIFACT_DIR}/desktop_session.json" 20
api_json GET "/readyz" "" "${ARTIFACT_DIR}/readyz.json" 20
seed_fireworks_credential_if_needed
configure_model_defaults

replicate_body="$(jq -nc \
  --arg mode local \
  --arg swarm_name "${CONTAINER_NAME}" \
  --arg source_workspace_path "${WORKSPACE_PATH}" \
  --arg runtime "${RUNTIME}" \
  '{mode:$mode,swarm_name:$swarm_name,sync:{enabled:true,mode:"managed"},workspaces:[{source_workspace_path:$source_workspace_path,replication_mode:"bundle",writable:true}]} + (if $runtime != "" then {runtime:$runtime} else {} end)')"
printf '%s\n' "${replicate_body}" >"${ARTIFACT_DIR}/replicate_request.json"
log "creating one primary local container: ${CONTAINER_NAME}"
api_json POST "/v1/swarm/replicate" "${replicate_body}" "${ARTIFACT_DIR}/replicate_response.json" 300
[[ "$(json_get "${ARTIFACT_DIR}/replicate_response.json" '.ok // false')" == "true" ]] || fail "replicate ok=false"
DEPLOYMENT_ID="$(json_get "${ARTIFACT_DIR}/replicate_response.json" '.swarm.deployment_id // empty')"
CHILD_SWARM_ID="$(json_get "${ARTIFACT_DIR}/replicate_response.json" '.swarm.id // empty')"
RUNTIME_WORKSPACE_PATH="$(json_get "${ARTIFACT_DIR}/replicate_response.json" '.workspaces[0].binding.destination_workspace_path // empty')"
WORKSPACE_BINDING_ID="$(json_get "${ARTIFACT_DIR}/replicate_response.json" '.workspaces[0].binding.binding_id // empty')"
[[ -n "${DEPLOYMENT_ID}" ]] || fail "replicate missing deployment_id"
[[ -n "${CHILD_SWARM_ID}" ]] || fail "replicate missing child swarm id"
[[ -n "${RUNTIME_WORKSPACE_PATH}" ]] || fail "replicate missing runtime workspace path"
[[ -n "${WORKSPACE_BINDING_ID}" ]] || fail "replicate missing workspace binding id"
wait_for_child_target

FLOW_ID="flow-single-local-container-$(date +%Y%m%d-%H%M%S)-${RANDOM}"
token="SINGLE_LOCAL_CONTAINER_FLOW_OK_${RANDOM}"
prompt="Single primary local-container Flow E2E proof. Reply with exactly: ${token}"
target_json="$(flow_target_json "${ARTIFACT_DIR}/target.json")"
workspace_json="$(flow_workspace_json)"
flow_body="$(jq -nc \
  --arg flow_id "${FLOW_ID}" \
  --arg name "Single primary local-container Flow proof" \
  --argjson target "${target_json}" \
  --argjson workspace "${workspace_json}" \
  --arg agent_name "${FLOW_AGENT_NAME}" \
  --arg agent_mode "${FLOW_AGENT_MODE}" \
  --arg prompt "${prompt}" \
  '{flow_id:$flow_id,name:$name,enabled:false,target:$target,agent:{profile_name:$agent_name,profile_mode:$agent_mode},workspace:$workspace,schedule:{cadence:"on_demand",timezone:"UTC"},catch_up_policy:{mode:"once"},intent:{prompt:$prompt,mode:"single_local_container_flow_e2e"}}')"
printf '%s\n' "${target_json}" >"${ARTIFACT_DIR}/flow_target.json"
printf '%s\n' "${workspace_json}" >"${ARTIFACT_DIR}/flow_workspace.json"
printf '%s\n' "${flow_body}" >"${ARTIFACT_DIR}/flow_create_request.json"

log "creating one local-container Flow: ${FLOW_ID} target=${CHILD_SWARM_ID}"
api_json POST "/v3/flows" "${flow_body}" "${ARTIFACT_DIR}/flow_create_response.json" 90
[[ "$(json_get "${ARTIFACT_DIR}/flow_create_response.json" '.ok // false')" == "true" ]] || fail "flow create ok=false"

log "running one local-container Flow now: ${FLOW_ID}"
api_json POST "/v3/flows/${FLOW_ID}/run-now" "" "${ARTIFACT_DIR}/flow_run_now_response.json" 90
[[ "$(json_get "${ARTIFACT_DIR}/flow_run_now_response.json" '.ok // false')" == "true" ]] || fail "flow run-now ok=false"
latest_run_id="$(jq -r '(.last_run.run_id // (.flow.last_run.run_id // "") // ((.run.reason // .result.ack.reason // "") | capture("run_now started (?<id>[^ ]+)")? | .id) // empty)' "${ARTIFACT_DIR}/flow_run_now_response.json")"
printf '%s\n' "${latest_run_id}" >"${ARTIFACT_DIR}/flow_run_id.txt"

deadline=$((SECONDS + TIMEOUT_SECONDS))
status=""
session_id=""
while :; do
  api_json GET "/v3/flows/${FLOW_ID}/status?limit=100" "" "${ARTIFACT_DIR}/flow_status_poll.json" 30
  api_json GET "/v3/flows/${FLOW_ID}/history?limit=100" "" "${ARTIFACT_DIR}/flow_history_poll.json" 30
  if [[ -n "${latest_run_id}" ]]; then
    status="$(jq -r --arg run_id "${latest_run_id}" '[.history[]? | select((.run_id // "") == $run_id)] | last | .status // empty' "${ARTIFACT_DIR}/flow_history_poll.json")"
    session_id="$(jq -r --arg run_id "${latest_run_id}" '[.history[]? | select((.run_id // "") == $run_id)] | last | .session_id // empty' "${ARTIFACT_DIR}/flow_history_poll.json")"
  else
    status="$(jq -r '[.history[]?] | sort_by(.started_at // .scheduled_at // "") | last | .status // empty' "${ARTIFACT_DIR}/flow_history_poll.json")"
    session_id="$(jq -r '[.history[]?] | sort_by(.started_at // .scheduled_at // "") | last | .session_id // empty' "${ARTIFACT_DIR}/flow_history_poll.json")"
  fi
  if [[ "${status}" == "success" && -n "${session_id}" ]]; then
    cp -- "${ARTIFACT_DIR}/flow_status_poll.json" "${ARTIFACT_DIR}/flow_status.json"
    cp -- "${ARTIFACT_DIR}/flow_history_poll.json" "${ARTIFACT_DIR}/flow_history.json"
    printf '%s\n' "${session_id}" >"${ARTIFACT_DIR}/flow_session_id.txt"
    break
  fi
  if [[ "${status}" == "failed" ]]; then
    cp -- "${ARTIFACT_DIR}/flow_status_poll.json" "${ARTIFACT_DIR}/flow_status.json"
    cp -- "${ARTIFACT_DIR}/flow_history_poll.json" "${ARTIFACT_DIR}/flow_history.json"
    fail "flow ${FLOW_ID} failed: $(jq -c '[.history[]? | select((.status // "") == "failed")] | last' "${ARTIFACT_DIR}/flow_history.json")"
  fi
  [[ "${SECONDS}" -lt "${deadline}" ]] || { cp -- "${ARTIFACT_DIR}/flow_status_poll.json" "${ARTIFACT_DIR}/flow_status.json"; cp -- "${ARTIFACT_DIR}/flow_history_poll.json" "${ARTIFACT_DIR}/flow_history.json"; fail "timed out waiting for flow ${FLOW_ID} success"; }
  sleep 5
done

api_json GET "/v1/sessions/${session_id}" "" "${ARTIFACT_DIR}/flow_session.json" 30 || true
api_json GET "/v1/sessions/${session_id}/metadata" "" "${ARTIFACT_DIR}/flow_session_metadata.json" 30 || true
api_json GET "/v1/sessions/${session_id}/messages?limit=100" "" "${ARTIFACT_DIR}/flow_messages.json" 30
api_json GET "/v1/swarm/topology/session-route?session_id=$(urlencode "${session_id}")" "" "${ARTIFACT_DIR}/flow_session_route.json" 30 || true

if ! jq -e --arg token "${token}" '[.messages[]? | select(((.content // "") | contains($token)))] | length > 0' "${ARTIFACT_DIR}/flow_messages.json" >/dev/null; then
  fail "flow ${FLOW_ID} completed but messages did not contain ${token}"
fi

jq -nc \
  --arg flow_id "${FLOW_ID}" \
  --arg run_id "${latest_run_id}" \
  --arg session_id "${session_id}" \
  --arg deployment_id "${DEPLOYMENT_ID}" \
  --arg child_swarm_id "${CHILD_SWARM_ID}" \
  --arg proof_token "${token}" \
  --arg artifact_dir "${ARTIFACT_DIR}" \
  '{ok:true,flow_id:$flow_id,run_id:$run_id,session_id:$session_id,deployment_id:$deployment_id,child_swarm_id:$child_swarm_id,proof_token:$proof_token,artifact_dir:$artifact_dir}' \
  >"${ARTIFACT_DIR}/summary.json"

log "PASS single-local-container-flow diagnostic: ${ARTIFACT_DIR}/summary.json"
