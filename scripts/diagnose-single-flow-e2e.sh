#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/diagnose-single-flow-e2e.sh [options]

Focused live E2E diagnostic: create exactly one /v3/flows self-target Flow on a
single primary API, run it once with /run-now, and verify the proof
token appears in the resulting session messages.

No containers, no managed host, no cron, no unit tests.

Options:
  --primary-url <url>              Primary swarmd API URL. Default: http://127.0.0.1:7781
  --workspace <path>               Workspace path for the Flow. Default: current repository root.
  --artifact-dir <path>            Evidence directory. Default: tmp/single-flow-diagnostics/<timestamp>
  --flow-agent-name <name>         Saved agent profile_name. Default: swarm
  --flow-agent-mode <mode>         Saved agent profile_mode. Default: primary
  --flow-provider <provider>       Model provider to set before the Flow. Default: fireworks
  --flow-model <model>             Model to set before the Flow. Default: accounts/fireworks/models/kimi-k2p6
  --flow-thinking <level>          Thinking level to set before the Flow. Default: low
  --fireworks-key-path <path>      Fireworks API key file to seed if no active credential exists.
                                  If omitted, auto-detects exactly one key file under ${TMPDIR:-/tmp} matching *fireworks*.key.
  --skip-model-config              Do not POST /v1/model before creating the Flow.
  --timeout-seconds <seconds>      Wait timeout for Flow success. Default: 420
  --cleanup                        Disable/delete the created Flow on exit.
  --help                           Show this help.

Environment equivalents:
  SWARM_PRIMARY_URL, SWARM_SOURCE_WORKSPACE_PATH, SWARM_SINGLE_FLOW_ARTIFACT_DIR,
  SWARM_SINGLE_FLOW_AGENT_NAME, SWARM_SINGLE_FLOW_AGENT_MODE,
  SWARM_SINGLE_FLOW_PROVIDER, SWARM_SINGLE_FLOW_MODEL, SWARM_SINGLE_FLOW_THINKING,
  SWARM_FIREWORKS_KEY_PATH, SWARM_SINGLE_FLOW_TIMEOUT_SECONDS, SWARMD_TOKEN
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
ARTIFACT_DIR="${SWARM_SINGLE_FLOW_ARTIFACT_DIR:-}"
FLOW_AGENT_NAME="${SWARM_SINGLE_FLOW_AGENT_NAME:-swarm}"
FLOW_AGENT_MODE="${SWARM_SINGLE_FLOW_AGENT_MODE:-primary}"
FLOW_PROVIDER="${SWARM_SINGLE_FLOW_PROVIDER:-fireworks}"
FLOW_MODEL="${SWARM_SINGLE_FLOW_MODEL:-accounts/fireworks/models/kimi-k2p6}"
FLOW_THINKING="${SWARM_SINGLE_FLOW_THINKING:-low}"
FIREWORKS_KEY_PATH="${SWARM_FIREWORKS_KEY_PATH:-}"
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
    --workspace|--source-workspace-path) WORKSPACE_PATH="${2:-}"; shift 2 ;;
    --artifact-dir) ARTIFACT_DIR="${2:-}"; shift 2 ;;
    --flow-agent-name) FLOW_AGENT_NAME="${2:-}"; shift 2 ;;
    --flow-agent-mode) FLOW_AGENT_MODE="${2:-}"; shift 2 ;;
    --flow-provider) FLOW_PROVIDER="${2:-}"; shift 2 ;;
    --flow-model) FLOW_MODEL="${2:-}"; shift 2 ;;
    --flow-thinking) FLOW_THINKING="${2:-}"; shift 2 ;;
    --fireworks-key-path) FIREWORKS_KEY_PATH="${2:-}"; shift 2 ;;
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
  ARTIFACT_DIR="${ROOT_DIR}/.tmp/single-flow-diagnostics/$(date +%Y%m%d-%H%M%S)"
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
  body="$(jq -nc --arg api_key "${key}" '{provider:"fireworks",type:"api",label:"single-flow diagnostic fireworks",api_key:$api_key,active:true}')"
  api_json POST "/v1/auth/credentials" "${body}" "${ARTIFACT_DIR}/fireworks_credential_seed_response.json" 90
  jq -nc --arg key_path "${key_path}" '{ok:true,key_path:$key_path,response_file:"fireworks_credential_seed_response.json"}' >"${ARTIFACT_DIR}/fireworks_credential_seed.json"
}

flow_workspace_json() {
  local workspace_path="${1:-}"
  jq -nc \
    --arg workspace_path "${workspace_path}" \
    --arg workspace_name "$(basename "${workspace_path}")" \
    '{workspace_path:$workspace_path,host_workspace_path:$workspace_path,workspace_name:$workspace_name,cwd:$workspace_path}'
}

log "single-flow diagnostic: primary=${PRIMARY_URL} workspace=${WORKSPACE_PATH} artifacts=${ARTIFACT_DIR}"
api_json GET "/v1/auth/desktop/session" "" "${ARTIFACT_DIR}/desktop_session.json" 20
api_json GET "/readyz" "" "${ARTIFACT_DIR}/readyz.json" 20
seed_fireworks_credential_if_needed
configure_model_defaults

flow_id="flow-single-self-$(date +%Y%m%d-%H%M%S)-${RANDOM}"
token="single-flow-proof-${flow_id}"
prompt="Single Flow E2E proof. Reply with exactly: ${token}"
workspace_json="$(flow_workspace_json "${WORKSPACE_PATH}")"
flow_body="$(jq -nc \
  --arg flow_id "${flow_id}" \
  --arg name "Single Flow self proof" \
  --argjson workspace "${workspace_json}" \
  --arg agent_name "${FLOW_AGENT_NAME}" \
  --arg agent_mode "${FLOW_AGENT_MODE}" \
  --arg prompt "${prompt}" \
  '{flow_id:$flow_id,name:$name,enabled:false,target:{kind:"self"},agent:{profile_name:$agent_name,profile_mode:$agent_mode},workspace:$workspace,schedule:{cadence:"on_demand",timezone:"UTC"},catch_up_policy:{mode:"once"},intent:{prompt:$prompt,mode:"single_flow_e2e"}}')"
printf '%s\n' "${flow_body}" >"${ARTIFACT_DIR}/flow_create_request.json"

log "creating one self Flow: ${flow_id}"
api_json POST "/v3/flows" "${flow_body}" "${ARTIFACT_DIR}/flow_create_response.json" 90
[[ "$(json_get "${ARTIFACT_DIR}/flow_create_response.json" '.ok // false')" == "true" ]] || fail "flow create ok=false"
CREATED_FLOW_ID="${flow_id}"

log "running one Flow now: ${flow_id}"
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

api_json GET "/v1/sessions/${session_id}" "" "${ARTIFACT_DIR}/flow_session.json" 30 || true
api_json GET "/v1/sessions/${session_id}/metadata" "" "${ARTIFACT_DIR}/flow_session_metadata.json" 30 || true
api_json GET "/v1/sessions/${session_id}/messages?limit=100" "" "${ARTIFACT_DIR}/flow_messages.json" 30
api_json GET "/v1/swarm/topology/session-route?session_id=$(urlencode "${session_id}")" "" "${ARTIFACT_DIR}/flow_session_route.json" 30 || true

if ! jq -e --arg token "${token}" '[.messages[]? | select(((.content // "") | contains($token)))] | length > 0' "${ARTIFACT_DIR}/flow_messages.json" >/dev/null; then
  fail "flow ${flow_id} completed but messages did not contain ${token}"
fi

jq -nc \
  --arg flow_id "${flow_id}" \
  --arg run_id "${latest_run_id}" \
  --arg session_id "${session_id}" \
  --arg proof_token "${token}" \
  --arg artifact_dir "${ARTIFACT_DIR}" \
  '{ok:true,flow_id:$flow_id,run_id:$run_id,session_id:$session_id,proof_token:$proof_token,artifact_dir:$artifact_dir}' \
  >"${ARTIFACT_DIR}/summary.json"

log "PASS single-flow diagnostic: ${ARTIFACT_DIR}/summary.json"
