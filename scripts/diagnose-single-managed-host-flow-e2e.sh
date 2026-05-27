#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/diagnose-single-managed-host-flow-e2e.sh [options]

SSH-only focused live E2E diagnostic: from a primary/testbench checkout, create
exactly one /v3/flows Flow targeting one already-linked managed host, run it once
with /run-now, and verify the proof token appears in the resulting session messages.

No containers, no cron, no unit tests.

Options:
  --primary-url <url>              Primary swarmd API URL on this SSH host. Default: http://127.0.0.1:7781
  --managed-swarm-id <id>          Managed host swarm_id. Optional if --managed-name resolves it.
  --managed-name <name>            Managed host display name. If omitted, uses a single online non-self/non-local target.
  --workspace <path>               Source workspace path on the primary/controller. Default: current repository root.
  --artifact-dir <path>            Evidence directory. Default: tmp/single-managed-host-flow-diagnostics/<timestamp>
  --flow-agent-name <name>         Saved agent profile_name. Default: swarm
  --flow-agent-mode <mode>         Saved agent profile_mode. Default: primary
  --flow-provider <provider>       Model provider to set before the Flow. Default: fireworks
  --flow-model <model>             Model to set before the Flow. Default: accounts/fireworks/models/minimax-m2p5
  --flow-thinking <level>          Thinking level to set before the Flow. Default: low
  --skip-model-config              Do not POST /v1/model before creating the Flow.
  --timeout-seconds <seconds>      Wait timeout for Flow success. Default: 420
  --cleanup                        Disable/delete the created Flow on exit.
  --help                           Show this help.

Environment equivalents:
  SWARM_PRIMARY_URL, SWARM_MANAGED_SWARM_ID, SWARM_MANAGED_NAME,
  SWARM_SOURCE_WORKSPACE_PATH, SWARM_SINGLE_MANAGED_HOST_FLOW_ARTIFACT_DIR,
  SWARM_SINGLE_FLOW_AGENT_NAME, SWARM_SINGLE_FLOW_AGENT_MODE,
  SWARM_SINGLE_FLOW_PROVIDER, SWARM_SINGLE_FLOW_MODEL, SWARM_SINGLE_FLOW_THINKING,
  SWARM_SINGLE_FLOW_TIMEOUT_SECONDS, SWARMD_TOKEN
EOF
}

log() { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "${1:-}" >/dev/null 2>&1 || fail "required command not found: ${1:-}"; }
urlencode() { jq -rn --arg v "${1:-}" '$v|@uri'; }
json_get() { jq -r "${2:-.}" "${1:-}"; }

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
PRIMARY_URL="${SWARM_PRIMARY_URL:-http://127.0.0.1:7781}"
MANAGED_SWARM_ID="${SWARM_MANAGED_SWARM_ID:-}"
MANAGED_NAME="${SWARM_MANAGED_NAME:-}"
WORKSPACE_PATH="${SWARM_SOURCE_WORKSPACE_PATH:-${ROOT_DIR}}"
ARTIFACT_DIR="${SWARM_SINGLE_MANAGED_HOST_FLOW_ARTIFACT_DIR:-}"
FLOW_AGENT_NAME="${SWARM_SINGLE_FLOW_AGENT_NAME:-swarm}"
FLOW_AGENT_MODE="${SWARM_SINGLE_FLOW_AGENT_MODE:-primary}"
FLOW_PROVIDER="${SWARM_SINGLE_FLOW_PROVIDER:-fireworks}"
FLOW_MODEL="${SWARM_SINGLE_FLOW_MODEL:-accounts/fireworks/models/minimax-m2p5}"
FLOW_THINKING="${SWARM_SINGLE_FLOW_THINKING:-low}"
TIMEOUT_SECONDS="${SWARM_SINGLE_FLOW_TIMEOUT_SECONDS:-420}"
CONFIGURE_MODEL="true"
CLEANUP="false"
COOKIE_FILE=""
API_STATUS=""
API_BODY=""
CREATED_FLOW_ID=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --primary-url) PRIMARY_URL="${2:-}"; shift 2 ;;
    --managed-swarm-id) MANAGED_SWARM_ID="${2:-}"; shift 2 ;;
    --managed-name) MANAGED_NAME="${2:-}"; shift 2 ;;
    --workspace|--source-workspace-path) WORKSPACE_PATH="${2:-}"; shift 2 ;;
    --artifact-dir) ARTIFACT_DIR="${2:-}"; shift 2 ;;
    --flow-agent-name) FLOW_AGENT_NAME="${2:-}"; shift 2 ;;
    --flow-agent-mode) FLOW_AGENT_MODE="${2:-}"; shift 2 ;;
    --flow-provider) FLOW_PROVIDER="${2:-}"; shift 2 ;;
    --flow-model) FLOW_MODEL="${2:-}"; shift 2 ;;
    --flow-thinking) FLOW_THINKING="${2:-}"; shift 2 ;;
    --skip-model-config) CONFIGURE_MODEL="false"; shift ;;
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
[[ -n "${FLOW_AGENT_NAME}" ]] || fail "--flow-agent-name is required"
[[ -n "${FLOW_AGENT_MODE}" ]] || fail "--flow-agent-mode is required"
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ && "${TIMEOUT_SECONDS}" -gt 0 ]] || fail "--timeout-seconds must be a positive integer"
if [[ "${CONFIGURE_MODEL}" == "true" ]]; then
  [[ -n "${FLOW_PROVIDER}" ]] || fail "--flow-provider is required unless --skip-model-config is set"
  [[ -n "${FLOW_MODEL}" ]] || fail "--flow-model is required unless --skip-model-config is set"
  [[ -n "${FLOW_THINKING}" ]] || fail "--flow-thinking is required unless --skip-model-config is set"
fi
PRIMARY_URL="${PRIMARY_URL%/}"
if [[ -z "${ARTIFACT_DIR}" ]]; then
  ARTIFACT_DIR="${ROOT_DIR}/tmp/single-managed-host-flow-diagnostics/$(date +%Y%m%d-%H%M%S)"
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
  if [[ "${CLEANUP}" != "true" || -z "${CREATED_FLOW_ID}" ]]; then
    return 0
  fi
  set +e
  api_json PUT "/v3/flows/${CREATED_FLOW_ID}" "$(jq -nc '{enabled:false,target:{},unassign_target:true}')" "${ARTIFACT_DIR}/cleanup_flow_disable.json" 60 || true
  api_json DELETE "/v3/flows/${CREATED_FLOW_ID}" "" "${ARTIFACT_DIR}/cleanup_flow_delete.json" 60 || true
  set -e
}
trap cleanup_created EXIT

configure_model_defaults() {
  if [[ "${CONFIGURE_MODEL}" != "true" ]]; then
    printf '{"skipped":true}\n' >"${ARTIFACT_DIR}/model_config.json"
    return 0
  fi
  local body
  body="$(jq -nc --arg provider "${FLOW_PROVIDER}" --arg model "${FLOW_MODEL}" --arg thinking "${FLOW_THINKING}" '{provider:$provider,model:$model,thinking:$thinking}')"
  printf '%s\n' "${body}" >"${ARTIFACT_DIR}/model_config_request.json"
  api_json POST "/v1/model" "${body}" "${ARTIFACT_DIR}/model_config_response.json" 60
}

resolve_managed_target() {
  api_json GET "/v1/swarm/targets" "" "${ARTIFACT_DIR}/targets.json" 30
  if [[ -n "${MANAGED_SWARM_ID}" ]]; then
    jq -c --arg id "${MANAGED_SWARM_ID}" '.targets[]? | select((.swarm_id // "") == $id)' "${ARTIFACT_DIR}/targets.json" | head -n 1 | jq '.' >"${ARTIFACT_DIR}/managed_target.json"
  elif [[ -n "${MANAGED_NAME}" ]]; then
    jq -c --arg name "${MANAGED_NAME}" '.targets[]? | select((.name // "") == $name)' "${ARTIFACT_DIR}/targets.json" | head -n 1 | jq '.' >"${ARTIFACT_DIR}/managed_target.json"
  else
    local count
    count="$(jq -r '[.targets[]? | select((.online // false) == true and (.selectable // false) == true and ((.kind // "") != "self") and ((.kind // "") != "local"))] | length' "${ARTIFACT_DIR}/targets.json")"
    [[ "${count}" == "1" ]] || fail "managed target not specified and auto-detect found ${count}; pass --managed-swarm-id or --managed-name"
    jq -c '.targets[]? | select((.online // false) == true and (.selectable // false) == true and ((.kind // "") != "self") and ((.kind // "") != "local"))' "${ARTIFACT_DIR}/targets.json" | head -n 1 | jq '.' >"${ARTIFACT_DIR}/managed_target.json"
  fi
  [[ -s "${ARTIFACT_DIR}/managed_target.json" ]] || fail "managed target not found"
  MANAGED_SWARM_ID="$(json_get "${ARTIFACT_DIR}/managed_target.json" '.swarm_id // empty')"
  MANAGED_NAME="$(json_get "${ARTIFACT_DIR}/managed_target.json" '.name // empty')"
  [[ -n "${MANAGED_SWARM_ID}" ]] || fail "managed target missing swarm_id"
  [[ "$(json_get "${ARTIFACT_DIR}/managed_target.json" '.online // false')" == "true" ]] || fail "managed target ${MANAGED_SWARM_ID} is not online"
  [[ "$(json_get "${ARTIFACT_DIR}/managed_target.json" '.selectable // false')" == "true" ]] || fail "managed target ${MANAGED_SWARM_ID} is not selectable"
}

flow_target_json() {
  jq -c '{swarm_id:(.swarm_id // ""),kind:(.kind // ""),deployment_id:(.deployment_id // ""),name:(.name // "")} | with_entries(select(.value != ""))' "${1:-}"
}

resolve_workspace_json() {
  api_json GET "/v1/swarm/topology" "" "${ARTIFACT_DIR}/topology.json" 30
  local deployment_id binding_id runtime_path
  deployment_id="$(json_get "${ARTIFACT_DIR}/managed_target.json" '.deployment_id // empty')"
  binding_id="$(jq -r \
    --arg runtime "${MANAGED_SWARM_ID}" \
    --arg deployment "${deployment_id}" \
    --arg source "${WORKSPACE_PATH}" \
    '[.workspace_bindings[]? | select((.source_workspace_path // "") == $source) | select((.destination_runtime_swarm_id // "") == $runtime or ($deployment != "" and (((.binding_id // "") == $deployment) or ((.binding_id // "") | contains(":" + $deployment + ":")))))] | last | .binding_id // empty' \
    "${ARTIFACT_DIR}/topology.json")"
  [[ -n "${binding_id}" ]] || fail "no workspace binding for managed target ${MANAGED_SWARM_ID} and source ${WORKSPACE_PATH}"
  runtime_path="$(jq -r --arg binding "${binding_id}" '[.workspace_bindings[]? | select((.binding_id // "") == $binding)] | last | .destination_workspace_path // empty' "${ARTIFACT_DIR}/topology.json")"
  [[ -n "${runtime_path}" ]] || fail "workspace binding ${binding_id} missing destination_workspace_path"
  jq -nc \
    --arg workspace_path "${WORKSPACE_PATH}" \
    --arg host_workspace_path "${WORKSPACE_PATH}" \
    --arg runtime_workspace_path "${runtime_path}" \
    --arg workspace_binding_id "${binding_id}" \
    --arg workspace_name "$(basename "${WORKSPACE_PATH}")" \
    '{workspace_path:$workspace_path,host_workspace_path:$host_workspace_path,runtime_workspace_path:$runtime_workspace_path,workspace_binding_id:$workspace_binding_id,workspace_name:$workspace_name,cwd:$workspace_path}'
}

log "single-managed-host-flow diagnostic: primary=${PRIMARY_URL} workspace=${WORKSPACE_PATH} artifacts=${ARTIFACT_DIR}"
api_json GET "/v1/auth/desktop/session" "" "${ARTIFACT_DIR}/desktop_session.json" 20
api_json GET "/readyz" "" "${ARTIFACT_DIR}/readyz.json" 20
configure_model_defaults
resolve_managed_target

target_json="$(flow_target_json "${ARTIFACT_DIR}/managed_target.json")"
workspace_json="$(resolve_workspace_json)"
flow_id="flow-single-managed-host-$(date +%Y%m%d-%H%M%S)-${RANDOM}"
token="single-managed-host-flow-proof-${flow_id}"
prompt="Single managed-host Flow E2E proof. Reply with exactly: ${token}"
flow_body="$(jq -nc \
  --arg flow_id "${flow_id}" \
  --arg name "Single managed-host Flow proof" \
  --argjson target "${target_json}" \
  --argjson workspace "${workspace_json}" \
  --arg agent_name "${FLOW_AGENT_NAME}" \
  --arg agent_mode "${FLOW_AGENT_MODE}" \
  --arg prompt "${prompt}" \
  '{flow_id:$flow_id,name:$name,enabled:false,target:$target,agent:{profile_name:$agent_name,profile_mode:$agent_mode},workspace:$workspace,schedule:{cadence:"on_demand",timezone:"UTC"},catch_up_policy:{mode:"once"},intent:{prompt:$prompt,mode:"single_managed_host_flow_e2e"}}')"
printf '%s\n' "${target_json}" >"${ARTIFACT_DIR}/target.json"
printf '%s\n' "${workspace_json}" >"${ARTIFACT_DIR}/workspace.json"
printf '%s\n' "${flow_body}" >"${ARTIFACT_DIR}/flow_create_request.json"

log "creating one managed-host Flow: ${flow_id} target=${MANAGED_NAME:-${MANAGED_SWARM_ID}}"
api_json POST "/v3/flows" "${flow_body}" "${ARTIFACT_DIR}/flow_create_response.json" 90
[[ "$(json_get "${ARTIFACT_DIR}/flow_create_response.json" '.ok // false')" == "true" ]] || fail "flow create ok=false"
CREATED_FLOW_ID="${flow_id}"

log "running one managed-host Flow now: ${flow_id}"
api_json POST "/v3/flows/${flow_id}/run-now" "" "${ARTIFACT_DIR}/flow_run_now_response.json" 90
[[ "$(json_get "${ARTIFACT_DIR}/flow_run_now_response.json" '.ok // false')" == "true" ]] || fail "flow run-now ok=false"
latest_run_id="$(jq -r '(.last_run.run_id // (.flow.last_run.run_id // "") // ((.run.reason // .result.ack.reason // "") | capture("run_now started (?<id>[^ ]+)")? | .id) // empty)' "${ARTIFACT_DIR}/flow_run_now_response.json")"
printf '%s\n' "${latest_run_id}" >"${ARTIFACT_DIR}/flow_run_id.txt"

deadline=$((SECONDS + TIMEOUT_SECONDS))
status=""
session_id=""
while :; do
  api_json GET "/v3/flows/${flow_id}/status?limit=100" "" "${ARTIFACT_DIR}/flow_status_poll.json" 30
  api_json GET "/v3/flows/${flow_id}/history?limit=100" "" "${ARTIFACT_DIR}/flow_history_poll.json" 30
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
    fail "flow ${flow_id} failed: $(jq -c '[.history[]? | select((.status // "") == "failed")] | last' "${ARTIFACT_DIR}/flow_history.json")"
  fi
  [[ "${SECONDS}" -lt "${deadline}" ]] || { cp -- "${ARTIFACT_DIR}/flow_status_poll.json" "${ARTIFACT_DIR}/flow_status.json"; cp -- "${ARTIFACT_DIR}/flow_history_poll.json" "${ARTIFACT_DIR}/flow_history.json"; fail "timed out waiting for flow ${flow_id} success"; }
  sleep 5
done

api_json GET "/v1/sessions/${session_id}/messages?limit=100" "" "${ARTIFACT_DIR}/flow_messages.json" 30
api_json GET "/v1/swarm/topology/session-route?session_id=$(urlencode "${session_id}")" "" "${ARTIFACT_DIR}/flow_session_route.json" 30 || true

if ! jq -e --arg token "${token}" '[.messages[]? | select(((.content // "") | contains($token)))] | length > 0' "${ARTIFACT_DIR}/flow_messages.json" >/dev/null; then
  fail "flow ${flow_id} completed but messages did not contain ${token}"
fi

jq -nc \
  --arg flow_id "${flow_id}" \
  --arg run_id "${latest_run_id}" \
  --arg session_id "${session_id}" \
  --arg target_swarm_id "${MANAGED_SWARM_ID}" \
  --arg proof_token "${token}" \
  --arg artifact_dir "${ARTIFACT_DIR}" \
  '{ok:true,flow_id:$flow_id,run_id:$run_id,session_id:$session_id,target_swarm_id:$target_swarm_id,proof_token:$proof_token,artifact_dir:$artifact_dir}' \
  >"${ARTIFACT_DIR}/summary.json"

log "PASS single-managed-host-flow diagnostic: ${ARTIFACT_DIR}/summary.json"
