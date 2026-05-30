#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/diagnose-local-container-session-api-e2e.sh [options]

Focused live E2E for the Sessions API routed-to-local-container path. Intended to
run on the testbench host. It creates a new local container through the primary
swarmd API, opens a routed session with swarm_id=<child>, sends a deterministic
prompt, runs the AI through Fireworks, and captures backend logs for the session
API/routing path.

Options:
  --primary-url <url>            Primary swarmd API URL. Default: http://127.0.0.1:7781
  --workspace <path>             Source workspace path. Default: current repository root.
  --container-name <name>        New local container name. Default: local-session-api-e2e-<timestamp>
  --artifact-dir <path>          Evidence directory. Default: .tmp/local-container-session-api-e2e/<timestamp>
  --provider <provider>          AI provider. Default: fireworks
  --model <model>                AI model. Default: accounts/fireworks/models/kimi-k2p6
  --thinking <level>             Thinking level. Default: low
  --fireworks-key-path <path>    Fireworks API key file to seed if no active credential exists.
                                 If omitted, auto-detects exactly one *fireworks*.key under ${TMPDIR:-/tmp}.
  --runtime <podman|docker>      Optional requested runtime passed to /v1/swarm/replicate.
  --timeout-seconds <seconds>    Wait timeout. Default: 420
  --cleanup                      Delete created diagnostic container at exit.
  --help                         Show this help.

Environment equivalents:
  SWARM_PRIMARY_URL, SWARM_SOURCE_WORKSPACE_PATH,
  SWARM_LOCAL_SESSION_API_CONTAINER_NAME, SWARM_LOCAL_SESSION_API_ARTIFACT_DIR,
  SWARM_LOCAL_SESSION_API_PROVIDER, SWARM_LOCAL_SESSION_API_MODEL,
  SWARM_LOCAL_SESSION_API_THINKING, SWARM_FIREWORKS_KEY_PATH,
  SWARM_LOCAL_SESSION_API_RUNTIME, SWARM_LOCAL_SESSION_API_TIMEOUT_SECONDS,
  SWARMD_TOKEN
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
CONTAINER_NAME="${SWARM_LOCAL_SESSION_API_CONTAINER_NAME:-local-session-api-e2e-$(date +%Y%m%d-%H%M%S)}"
ARTIFACT_DIR="${SWARM_LOCAL_SESSION_API_ARTIFACT_DIR:-}"
PROVIDER="${SWARM_LOCAL_SESSION_API_PROVIDER:-fireworks}"
MODEL="${SWARM_LOCAL_SESSION_API_MODEL:-accounts/fireworks/models/kimi-k2p6}"
THINKING="${SWARM_LOCAL_SESSION_API_THINKING:-low}"
FIREWORKS_KEY_PATH="${SWARM_FIREWORKS_KEY_PATH:-}"
RUNTIME="${SWARM_LOCAL_SESSION_API_RUNTIME:-}"
TIMEOUT_SECONDS="${SWARM_LOCAL_SESSION_API_TIMEOUT_SECONDS:-420}"
CLEANUP="false"
COOKIE_FILE=""
API_STATUS=""
API_BODY=""
DEPLOYMENT_ID=""
CHILD_SWARM_ID=""
RUNTIME_WORKSPACE_PATH=""
WORKSPACE_BINDING_ID=""
SESSION_ID=""
RUN_ID=""
PROOF_TOKEN="SESSION_API_LOCAL_CONTAINER_OK_${RANDOM}_$(date +%s)"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --primary-url) PRIMARY_URL="${2:-}"; shift 2 ;;
    --workspace|--source-workspace-path) WORKSPACE_PATH="${2:-}"; shift 2 ;;
    --container-name) CONTAINER_NAME="${2:-}"; shift 2 ;;
    --artifact-dir) ARTIFACT_DIR="${2:-}"; shift 2 ;;
    --provider) PROVIDER="${2:-}"; shift 2 ;;
    --model) MODEL="${2:-}"; shift 2 ;;
    --thinking) THINKING="${2:-}"; shift 2 ;;
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
[[ -d "${WORKSPACE_PATH}" ]] || fail "workspace path does not exist: ${WORKSPACE_PATH}"
[[ -n "${CONTAINER_NAME}" ]] || fail "--container-name is required"
[[ -n "${PROVIDER}" ]] || fail "--provider is required"
[[ -n "${MODEL}" ]] || fail "--model is required"
[[ -n "${THINKING}" ]] || fail "--thinking is required"
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ && "${TIMEOUT_SECONDS}" -gt 0 ]] || fail "--timeout-seconds must be a positive integer"
PRIMARY_URL="${PRIMARY_URL%/}"
if [[ -z "${ARTIFACT_DIR}" ]]; then
  ARTIFACT_DIR="${ROOT_DIR}/.tmp/local-container-session-api-e2e/$(date +%Y%m%d-%H%M%S)"
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

capture_backend_logs() {
  local label="${1:-logs}"
  local out="${ARTIFACT_DIR}/${label}.log"
  {
    printf '### capture=%s time=%s host=%s\n' "${label}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$(hostname -f 2>/dev/null || hostname 2>/dev/null || true)"
    printf '### journalctl swarmd tail\n'
    journalctl -u swarmd --no-pager -n 500 2>&1 || true
    printf '\n### journalctl swarmd grep session/routing/fireworks/container\n'
    journalctl -u swarmd --no-pager -n 2500 2>&1 | grep -Ei 'session|routed|peer|swarm_id|run stream|fireworks|credential|replicate|deploy|container|trusted principal|workspace_binding' || true
    if [[ -n "${CONTAINER_NAME}" ]]; then
      printf '\n### runtime logs for container name=%s\n' "${CONTAINER_NAME}"
      if command -v podman >/dev/null 2>&1; then
        podman logs --tail 500 "${CONTAINER_NAME}" 2>&1 || true
      fi
      if command -v docker >/dev/null 2>&1; then
        docker logs --tail 500 "${CONTAINER_NAME}" 2>&1 || true
      fi
    fi
  } >"${out}"
}

cleanup_created() {
  capture_backend_logs final || true
  if [[ "${CLEANUP}" != "true" ]]; then
    return 0
  fi
  set +e
  if [[ -n "${DEPLOYMENT_ID}" ]]; then
    api_json POST "/v1/deploy/container/delete" "$(jq -nc --arg id "${DEPLOYMENT_ID}" '{ids:[$id]}')" "${ARTIFACT_DIR}/cleanup_container_delete.json" 180 || true
  fi
  set -e
}
trap cleanup_created EXIT

seed_fireworks_credential_if_needed() {
  if [[ "${PROVIDER}" != "fireworks" ]]; then
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
  body="$(jq -nc --arg api_key "${key}" '{provider:"fireworks",type:"api",label:"local-container session api e2e fireworks",api_key:$api_key,active:true}')"
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

log "local-container session API E2E: primary=${PRIMARY_URL} workspace=${WORKSPACE_PATH} artifacts=${ARTIFACT_DIR}"
api_json GET "/v1/auth/desktop/session" "" "${ARTIFACT_DIR}/desktop_session.json" 20
api_json GET "/readyz" "" "${ARTIFACT_DIR}/readyz.json" 20
seed_fireworks_credential_if_needed
api_json POST "/v1/model" "$(jq -nc --arg provider "${PROVIDER}" --arg model "${MODEL}" --arg thinking "${THINKING}" '{provider:$provider,model:$model,thinking:$thinking}')" "${ARTIFACT_DIR}/model_config_response.json" 60
capture_backend_logs before

replicate_body="$(jq -nc \
  --arg mode local \
  --arg swarm_name "${CONTAINER_NAME}" \
  --arg source_workspace_path "${WORKSPACE_PATH}" \
  --arg runtime "${RUNTIME}" \
  '{mode:$mode,swarm_name:$swarm_name,sync:{enabled:true,mode:"managed"},workspaces:[{source_workspace_path:$source_workspace_path,replication_mode:"bundle",writable:true}]} + (if $runtime != "" then {runtime:$runtime} else {} end)')"
printf '%s\n' "${replicate_body}" >"${ARTIFACT_DIR}/replicate_request.json"
log "creating new local container through /v1/swarm/replicate: ${CONTAINER_NAME}"
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
capture_backend_logs after_replicate

session_body="$(jq -nc \
  --arg title "Local container Sessions API Fireworks E2E" \
  --arg workspace_path "${WORKSPACE_PATH}" \
  --arg workspace_name "$(basename "${WORKSPACE_PATH}")" \
  --arg workspace_binding_id "${WORKSPACE_BINDING_ID}" \
  --arg provider "${PROVIDER}" \
  --arg model "${MODEL}" \
  --arg thinking "${THINKING}" \
  '{title:$title,workspace_path:$workspace_path,workspace_name:$workspace_name,workspace_binding_id:$workspace_binding_id,mode:"auto",agent_name:"swarm",worktree_mode:"off",preference:{provider:$provider,model:$model,thinking:$thinking},metadata:{local_container_session_api_e2e:true}}')"
printf '%s\n' "${session_body}" >"${ARTIFACT_DIR}/session_create_request.json"
log "opening routed session through primary /v1/sessions?swarm_id=${CHILD_SWARM_ID}"
api_json POST "/v1/sessions?swarm_id=$(urlencode "${CHILD_SWARM_ID}")" "${session_body}" "${ARTIFACT_DIR}/session_create_response.json" 120
SESSION_ID="$(json_get "${ARTIFACT_DIR}/session_create_response.json" '.session.id // .id // empty')"
[[ -n "${SESSION_ID}" ]] || fail "session create response missing session id"
api_json GET "/v1/swarm/topology/session-route?session_id=$(urlencode "${SESSION_ID}")" "" "${ARTIFACT_DIR}/session_route_after_create.json" 30 || true

prompt="Local container Sessions API E2E through swarm target ${CHILD_SWARM_ID}. Reply with exactly: ${PROOF_TOKEN}"
printf '%s\n' "${prompt}" >"${ARTIFACT_DIR}/prompt.txt"
api_json POST "/v1/sessions/${SESSION_ID}/messages" "$(jq -nc --arg content "${prompt}" '{role:"user",content:$content}')" "${ARTIFACT_DIR}/user_message_response.json" 60
run_body="$(jq -nc --arg prompt "${prompt}" '{type:"run.start",prompt:$prompt,background:true,execution_context:{worktree_mode:"off"}}')"
printf '%s\n' "${run_body}" >"${ARTIFACT_DIR}/run_request.json"
log "starting Fireworks AI run through routed session ${SESSION_ID}"
api_json POST "/v1/sessions/${SESSION_ID}/run/stream" "${run_body}" "${ARTIFACT_DIR}/run_start_response.json" 120
RUN_ID="$(json_get "${ARTIFACT_DIR}/run_start_response.json" '.run_id // empty')"
[[ -n "${RUN_ID}" ]] || fail "run start response missing run_id"
capture_backend_logs after_run_start

deadline=$((SECONDS + TIMEOUT_SECONDS))
while :; do
  api_json GET "/v1/sessions/${SESSION_ID}" "" "${ARTIFACT_DIR}/session_poll.json" 30 || true
  api_json GET "/v1/sessions/${SESSION_ID}/messages?limit=100" "" "${ARTIFACT_DIR}/messages_poll.json" 30
  if jq -e --arg token "${PROOF_TOKEN}" '[.messages[]? | select((.role // "") == "assistant" and ((.content // "") | contains($token)))] | length > 0' "${ARTIFACT_DIR}/messages_poll.json" >/dev/null; then
    cp -- "${ARTIFACT_DIR}/messages_poll.json" "${ARTIFACT_DIR}/messages.json"
    break
  fi
  [[ "${SECONDS}" -lt "${deadline}" ]] || { cp -- "${ARTIFACT_DIR}/messages_poll.json" "${ARTIFACT_DIR}/messages.json"; fail "timed out waiting for assistant proof token ${PROOF_TOKEN}"; }
  sleep 5
done

api_json GET "/v1/sessions/${SESSION_ID}/metadata" "" "${ARTIFACT_DIR}/session_metadata.json" 30 || true
api_json GET "/v1/swarm/topology/session-route?session_id=$(urlencode "${SESSION_ID}")" "" "${ARTIFACT_DIR}/session_route.json" 30 || true
capture_backend_logs after_success

jq -nc \
  --arg artifact_dir "${ARTIFACT_DIR}" \
  --arg deployment_id "${DEPLOYMENT_ID}" \
  --arg child_swarm_id "${CHILD_SWARM_ID}" \
  --arg workspace_binding_id "${WORKSPACE_BINDING_ID}" \
  --arg runtime_workspace_path "${RUNTIME_WORKSPACE_PATH}" \
  --arg session_id "${SESSION_ID}" \
  --arg run_id "${RUN_ID}" \
  --arg provider "${PROVIDER}" \
  --arg model "${MODEL}" \
  --arg proof_token "${PROOF_TOKEN}" \
  '{ok:true,artifact_dir:$artifact_dir,deployment_id:$deployment_id,child_swarm_id:$child_swarm_id,workspace_binding_id:$workspace_binding_id,runtime_workspace_path:$runtime_workspace_path,session_id:$session_id,run_id:$run_id,provider:$provider,model:$model,proof_token:$proof_token,product_path:"primary /v1/swarm/replicate -> /v1/sessions?swarm_id=child -> /v1/sessions/{id}/run/stream -> local-container backend",backend_logs:["before.log","after_replicate.log","after_run_start.log","after_success.log","final.log"]}' \
  >"${ARTIFACT_DIR}/summary.json"

log "PASS local-container session API E2E: ${ARTIFACT_DIR}/summary.json"
