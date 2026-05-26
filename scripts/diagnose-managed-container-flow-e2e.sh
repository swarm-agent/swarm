#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/diagnose-managed-container-flow-e2e.sh [options]

Focused live diagnostic for managed-host container Flows. It creates a fresh
managed-host child container through the primary API, creates a /v3/flows Flow
for that child, runs it now, and captures controller/target/container evidence.

Options:
  --primary-url <url>             Primary swarmd API URL. Default: http://127.0.0.1:7781
  --managed-swarm-id <id>         Managed host swarm_id. Optional if --managed-name is set.
  --managed-name <name>           Managed host display name to resolve from /v1/swarm/targets.
  --source-workspace-path <path>  Source workspace path on the primary/controller. Required.
  --container-name <name>         Diagnostic child name. Default: flow-diag-<timestamp>
  --agent-name <name>             Flow agent profile. Default: swarm
  --agent-mode <mode>             Flow agent mode. Default: primary
  --prompt <text>                 Flow prompt.
  --timeout-seconds <seconds>     Wait timeout for flow completion. Default: 360
  --evidence-dir <path>           Evidence output directory. Default: tmp/managed-container-flow-diagnostics/<timestamp>
  --managed-ssh-host <ssh-alias>  Optional SSH host for container logs/exec evidence.
  --runtime <podman|docker>       Runtime for SSH inspection. Default: auto-detect.
  --cleanup                       Disable/delete the diagnostic Flow/container after capture.
  --help                          Show help.

Environment equivalents:
  SWARM_PRIMARY_URL, SWARM_MANAGED_SWARM_ID, SWARM_MANAGED_NAME,
  SWARM_SOURCE_WORKSPACE_PATH, SWARM_FLOW_DIAG_CONTAINER_NAME,
  SWARM_FLOW_DIAG_EVIDENCE_DIR, SWARM_MANAGED_SSH_HOST
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
SOURCE_WORKSPACE_PATH="${SWARM_SOURCE_WORKSPACE_PATH:-}"
CONTAINER_NAME="${SWARM_FLOW_DIAG_CONTAINER_NAME:-flow-diag-$(date +%Y%m%d-%H%M%S)}"
AGENT_NAME="${SWARM_FLOW_DIAG_AGENT_NAME:-swarm}"
AGENT_MODE="${SWARM_FLOW_DIAG_AGENT_MODE:-primary}"
PROMPT="${SWARM_FLOW_DIAG_PROMPT:-Managed container Flow diagnostic. Reply with exactly: FLOW_DIAG_OK}"
TIMEOUT_SECONDS="${SWARM_FLOW_DIAG_TIMEOUT_SECONDS:-360}"
EVIDENCE_DIR="${SWARM_FLOW_DIAG_EVIDENCE_DIR:-}"
MANAGED_SSH_HOST="${SWARM_MANAGED_SSH_HOST:-}"
RUNTIME="${SWARM_FLOW_DIAG_RUNTIME:-}"
CLEANUP="false"
COOKIE_FILE=""
API_STATUS=""
API_BODY=""
FLOW_ID=""
DEPLOYMENT_ID=""
CHILD_SWARM_ID=""
HOST_CONTAINER_ID=""
ATTACHMENT_ID=""
RUNTIME_WORKSPACE_PATH=""
WORKSPACE_BINDING_ID=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --primary-url) PRIMARY_URL="$2"; shift 2 ;;
    --managed-swarm-id) MANAGED_SWARM_ID="$2"; shift 2 ;;
    --managed-name) MANAGED_NAME="$2"; shift 2 ;;
    --source-workspace-path) SOURCE_WORKSPACE_PATH="$2"; shift 2 ;;
    --container-name) CONTAINER_NAME="$2"; shift 2 ;;
    --agent-name) AGENT_NAME="$2"; shift 2 ;;
    --agent-mode) AGENT_MODE="$2"; shift 2 ;;
    --prompt) PROMPT="$2"; shift 2 ;;
    --timeout-seconds) TIMEOUT_SECONDS="$2"; shift 2 ;;
    --evidence-dir) EVIDENCE_DIR="$2"; shift 2 ;;
    --managed-ssh-host) MANAGED_SSH_HOST="$2"; shift 2 ;;
    --runtime) RUNTIME="$2"; shift 2 ;;
    --cleanup) CLEANUP="true"; shift ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

require_command curl
require_command jq
[[ -n "${SOURCE_WORKSPACE_PATH}" ]] || fail "--source-workspace-path is required"
[[ -n "${MANAGED_SWARM_ID}" || -n "${MANAGED_NAME}" ]] || fail "pass --managed-swarm-id or --managed-name"
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ ]] || fail "--timeout-seconds must be numeric"
PRIMARY_URL="${PRIMARY_URL%/}"
if [[ -z "${EVIDENCE_DIR}" ]]; then
  EVIDENCE_DIR="${ROOT_DIR}/tmp/managed-container-flow-diagnostics/$(date +%Y%m%d-%H%M%S)"
fi
mkdir -p -- "${EVIDENCE_DIR}"
COOKIE_FILE="${EVIDENCE_DIR}/primary.cookies"
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

safe_cleanup() {
  if [[ "${CLEANUP}" != "true" ]]; then
    return 0
  fi
  set +e
  if [[ -n "${FLOW_ID}" ]]; then
    api_json PUT "/v3/flows/${FLOW_ID}" "$(jq -nc '{enabled:false,target:{},unassign_target:true}')" "${EVIDENCE_DIR}/cleanup_flow_disable.json" 60
    api_json DELETE "/v3/flows/${FLOW_ID}" "" "${EVIDENCE_DIR}/cleanup_flow_delete.json" 60
  fi
  if [[ -n "${DEPLOYMENT_ID}" ]]; then
    api_json POST "/v1/deploy/container/delete" "$(jq -nc --arg id "${DEPLOYMENT_ID}" '{ids:[$id]}')" "${EVIDENCE_DIR}/cleanup_container_delete.json" 180
  fi
  set -e
}
trap safe_cleanup EXIT

log "evidence_dir=${EVIDENCE_DIR}"
api_json GET "/v1/auth/desktop/session" "" "${EVIDENCE_DIR}/desktop_session.json" 20
api_json GET "/readyz" "" "${EVIDENCE_DIR}/readyz.json" 20
api_json GET "/v1/swarm/targets" "" "${EVIDENCE_DIR}/targets_before.json" 30

if [[ -n "${MANAGED_SWARM_ID}" ]]; then
  jq -c --arg id "${MANAGED_SWARM_ID}" '.targets[]? | select((.swarm_id // "") == $id)' "${EVIDENCE_DIR}/targets_before.json" | head -n 1 | jq '.' >"${EVIDENCE_DIR}/managed_target.json"
else
  jq -c --arg name "${MANAGED_NAME}" '.targets[]? | select((.name // "") == $name)' "${EVIDENCE_DIR}/targets_before.json" | head -n 1 | jq '.' >"${EVIDENCE_DIR}/managed_target.json"
fi
[[ -s "${EVIDENCE_DIR}/managed_target.json" ]] || fail "managed target not found"
MANAGED_SWARM_ID="$(json_get "${EVIDENCE_DIR}/managed_target.json" '.swarm_id // empty')"
MANAGED_NAME="$(json_get "${EVIDENCE_DIR}/managed_target.json" '.name // empty')"
[[ "$(json_get "${EVIDENCE_DIR}/managed_target.json" '.online // false')" == "true" ]] || fail "managed target ${MANAGED_SWARM_ID} is not online"
[[ "$(json_get "${EVIDENCE_DIR}/managed_target.json" '.selectable // false')" == "true" ]] || fail "managed target ${MANAGED_SWARM_ID} is not selectable"

api_json GET "/v1/swarm/topology" "" "${EVIDENCE_DIR}/topology_before.json" 30

replicate_body="$(jq -nc \
  --arg mode local \
  --arg swarm_name "${CONTAINER_NAME}" \
  --arg target_host_swarm_id "${MANAGED_SWARM_ID}" \
  --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
  '{mode:$mode,swarm_name:$swarm_name,target_host_swarm_id:$target_host_swarm_id,sync:{enabled:true,mode:"managed"},workspaces:[{source_workspace_path:$source_workspace_path,replication_mode:"bundle",writable:true}]}')"
printf '%s\n' "${replicate_body}" >"${EVIDENCE_DIR}/replicate_request.json"
api_json POST "/v1/swarm/replicate" "${replicate_body}" "${EVIDENCE_DIR}/replicate_response.json" 240
[[ "$(json_get "${EVIDENCE_DIR}/replicate_response.json" '.ok // false')" == "true" ]] || fail "replicate ok=false"
DEPLOYMENT_ID="$(json_get "${EVIDENCE_DIR}/replicate_response.json" '.swarm.deployment_id // empty')"
CHILD_SWARM_ID="$(json_get "${EVIDENCE_DIR}/replicate_response.json" '.swarm.id // empty')"
RUNTIME_WORKSPACE_PATH="$(json_get "${EVIDENCE_DIR}/replicate_response.json" '.workspaces[0].binding.destination_workspace_path // empty')"
WORKSPACE_BINDING_ID="$(json_get "${EVIDENCE_DIR}/replicate_response.json" '.workspaces[0].binding.binding_id // empty')"
[[ -n "${DEPLOYMENT_ID}" ]] || fail "replicate missing deployment_id"
[[ -n "${CHILD_SWARM_ID}" ]] || fail "replicate missing child swarm id"
[[ -n "${RUNTIME_WORKSPACE_PATH}" ]] || fail "replicate missing destination workspace path"
log "created deployment=${DEPLOYMENT_ID} child=${CHILD_SWARM_ID} runtime_workspace=${RUNTIME_WORKSPACE_PATH}"

api_json GET "/v1/deploy/container" "" "${EVIDENCE_DIR}/deployments_after_create.json" 30
HOST_CONTAINER_ID="$(jq -r --arg id "${DEPLOYMENT_ID}" '.deployments[]? | select(.id == $id) | .host_container_id // empty' "${EVIDENCE_DIR}/deployments_after_create.json" | head -n 1)"
ATTACHMENT_ID="$(jq -r --arg id "${DEPLOYMENT_ID}" '.deployments[]? | select(.id == $id) | .attachment_id // empty' "${EVIDENCE_DIR}/deployments_after_create.json" | head -n 1)"

encoded_child="$(urlencode "${CHILD_SWARM_ID}")"
deadline=$((SECONDS + 90))
while :; do
  api_json GET "/v1/swarm/targets?swarm_id=${encoded_child}" "" "${EVIDENCE_DIR}/child_target_poll.json" 30
  if jq -e --arg id "${CHILD_SWARM_ID}" '.targets[]? | select((.swarm_id // "") == $id and (.online // false) == true and (.selectable // false) == true)' "${EVIDENCE_DIR}/child_target_poll.json" >/dev/null; then
    jq -c --arg id "${CHILD_SWARM_ID}" '.targets[]? | select((.swarm_id // "") == $id)' "${EVIDENCE_DIR}/child_target_poll.json" | head -n 1 | jq '.' >"${EVIDENCE_DIR}/child_target.json"
    break
  fi
  [[ "${SECONDS}" -lt "${deadline}" ]] || fail "timed out waiting for child target ${CHILD_SWARM_ID} online/selectable"
  sleep 3
done
api_json GET "/v1/swarm/topology" "" "${EVIDENCE_DIR}/topology_after_create.json" 30

workspace_name="$(basename "${SOURCE_WORKSPACE_PATH}")"
chat_session_body="$(jq -nc \
  --arg title "Managed container chat diagnostic ${CONTAINER_NAME}" \
  --arg workspace_path "${SOURCE_WORKSPACE_PATH}" \
  --arg host_workspace_path "${SOURCE_WORKSPACE_PATH}" \
  --arg runtime_workspace_path "${RUNTIME_WORKSPACE_PATH}" \
  --arg workspace_name "${workspace_name}" \
  --arg agent_name "${AGENT_NAME}" \
  '{title:$title,workspace_path:$workspace_path,host_workspace_path:$host_workspace_path,runtime_workspace_path:$runtime_workspace_path,workspace_name:$workspace_name,mode:"auto",agent_name:$agent_name,preference:{provider:"codex",model:"gpt-5.4",thinking:"medium"}}')"
printf '%s\n' "${chat_session_body}" >"${EVIDENCE_DIR}/chat_session_create_request.json"
api_json POST "/v1/sessions?swarm_id=${encoded_child}" "${chat_session_body}" "${EVIDENCE_DIR}/chat_session_create_response.json" 90
chat_session_id="$(json_get "${EVIDENCE_DIR}/chat_session_create_response.json" '.session.id // empty')"
[[ -n "${chat_session_id}" ]] || fail "chat session create did not return session.id"
chat_message_body="$(jq -nc '{role:"user",content:"Managed container route principal diagnostic message"}')"
printf '%s\n' "${chat_message_body}" >"${EVIDENCE_DIR}/chat_message_request.json"
api_json POST "/v1/sessions/${chat_session_id}/messages" "${chat_message_body}" "${EVIDENCE_DIR}/chat_message_response.json" 90
[[ "$(json_get "${EVIDENCE_DIR}/chat_message_response.json" '.ok // false')" == "true" ]] || fail "chat message ok=false"
api_json GET "/v1/sessions/${chat_session_id}/messages?limit=20" "" "${EVIDENCE_DIR}/chat_messages_after_send.json" 30 || true
api_json GET "/v1/swarm/topology/session-route?session_id=$(urlencode "${chat_session_id}")" "" "${EVIDENCE_DIR}/chat_session_route.json" 30 || true
log "chat diagnostic session=${chat_session_id} message send ok"

FLOW_ID="flow-diag-$(date +%Y%m%d-%H%M%S)"
flow_body="$(jq -nc \
  --arg flow_id "${FLOW_ID}" \
  --arg name "Managed container Flow diagnostic ${CONTAINER_NAME}" \
  --slurpfile target "${EVIDENCE_DIR}/child_target.json" \
  --arg agent_name "${AGENT_NAME}" \
  --arg agent_mode "${AGENT_MODE}" \
  --arg workspace_path "${SOURCE_WORKSPACE_PATH}" \
  --arg host_workspace_path "${SOURCE_WORKSPACE_PATH}" \
  --arg runtime_workspace_path "${RUNTIME_WORKSPACE_PATH}" \
  --arg workspace_binding_id "${WORKSPACE_BINDING_ID}" \
  --arg workspace_name "${workspace_name}" \
  --arg prompt "${PROMPT}" \
  '{flow_id:$flow_id,name:$name,enabled:true,target:{swarm_id:($target[0].swarm_id // ""),kind:($target[0].kind // ""),deployment_id:($target[0].deployment_id // ""),name:($target[0].name // "")},agent:{profile_name:$agent_name,profile_mode:$agent_mode},workspace:{workspace_path:$workspace_path,host_workspace_path:$host_workspace_path,runtime_workspace_path:$runtime_workspace_path,workspace_binding_id:$workspace_binding_id,workspace_name:$workspace_name,cwd:$workspace_path},schedule:{cadence:"on_demand",timezone:"UTC"},catch_up_policy:{mode:"once"},intent:{prompt:$prompt,mode:"diagnostic_managed_container_flow"}}')"
printf '%s\n' "${flow_body}" >"${EVIDENCE_DIR}/flow_create_request.json"
api_json POST "/v3/flows" "${flow_body}" "${EVIDENCE_DIR}/flow_create_response.json" 90
[[ "$(json_get "${EVIDENCE_DIR}/flow_create_response.json" '.ok // false')" == "true" ]] || fail "flow create ok=false"
api_json POST "/v3/flows/${FLOW_ID}/run-now" "" "${EVIDENCE_DIR}/flow_run_now_response.json" 90
[[ "$(json_get "${EVIDENCE_DIR}/flow_run_now_response.json" '.ok // false')" == "true" ]] || fail "flow run-now ok=false"

run_deadline=$((SECONDS + TIMEOUT_SECONDS))
while :; do
  api_json GET "/v3/flows/${FLOW_ID}/status?limit=100" "" "${EVIDENCE_DIR}/flow_status_poll.json" 30
  api_json GET "/v3/flows/${FLOW_ID}/history?limit=100" "" "${EVIDENCE_DIR}/flow_history_poll.json" 30
  status="$(jq -r '[.history[]? | select((.status // "") == "success" or (.status // "") == "failed")] | sort_by(.started_at // .scheduled_at // "") | last | .status // empty' "${EVIDENCE_DIR}/flow_history_poll.json")"
  session_id="$(jq -r '[.history[]? | select((.status // "") == "success" or (.status // "") == "failed")] | sort_by(.started_at // .scheduled_at // "") | last | .session_id // empty' "${EVIDENCE_DIR}/flow_history_poll.json")"
  if [[ "${status}" == "success" && -n "${session_id}" ]]; then
    cp -- "${EVIDENCE_DIR}/flow_status_poll.json" "${EVIDENCE_DIR}/flow_status.json"
    cp -- "${EVIDENCE_DIR}/flow_history_poll.json" "${EVIDENCE_DIR}/flow_history.json"
    printf '%s\n' "${session_id}" >"${EVIDENCE_DIR}/flow_session_id.txt"
    break
  fi
  if [[ "${status}" == "failed" ]]; then
    cp -- "${EVIDENCE_DIR}/flow_status_poll.json" "${EVIDENCE_DIR}/flow_status.json"
    cp -- "${EVIDENCE_DIR}/flow_history_poll.json" "${EVIDENCE_DIR}/flow_history.json"
    fail "flow ${FLOW_ID} failed: $(jq -c '[.history[]? | select((.status // "") == "failed")] | last' "${EVIDENCE_DIR}/flow_history.json")"
  fi
  [[ "${SECONDS}" -lt "${run_deadline}" ]] || { cp -- "${EVIDENCE_DIR}/flow_status_poll.json" "${EVIDENCE_DIR}/flow_status.json"; cp -- "${EVIDENCE_DIR}/flow_history_poll.json" "${EVIDENCE_DIR}/flow_history.json"; fail "timed out waiting for flow ${FLOW_ID} success"; }
  sleep 5
done

api_json GET "/v1/sessions/${session_id}" "" "${EVIDENCE_DIR}/flow_session.json" 30 || true
api_json GET "/v1/sessions/${session_id}/metadata" "" "${EVIDENCE_DIR}/flow_session_metadata.json" 30 || true
api_json GET "/v1/sessions/${session_id}/messages?limit=100" "" "${EVIDENCE_DIR}/flow_messages.json" 30 || true
api_json GET "/v1/swarm/topology/session-route?session_id=$(urlencode "${session_id}")" "" "${EVIDENCE_DIR}/flow_session_route.json" 30 || true
api_json GET "/v1/swarm/targets?swarm_id=${encoded_child}" "" "${EVIDENCE_DIR}/targets_after_flow.json" 30 || true
api_json GET "/v1/swarm/topology" "" "${EVIDENCE_DIR}/topology_after_flow.json" 30 || true

if [[ -n "${MANAGED_SSH_HOST}" ]]; then
  log "capturing managed host/container shell evidence from ${MANAGED_SSH_HOST}"
  container_name="$(jq -r --arg id "${DEPLOYMENT_ID}" '.deployments[]? | select(.id == $id) | .container_name // .name // empty' "${EVIDENCE_DIR}/deployments_after_create.json" | head -n 1)"
  [[ -n "${container_name}" ]] || container_name="${CONTAINER_NAME}"
  ssh "${MANAGED_SSH_HOST}" 'bash -s' -- "${container_name}" "${RUNTIME}" "${RUNTIME_WORKSPACE_PATH}" >"${EVIDENCE_DIR}/managed_container_shell_evidence.txt" 2>&1 <<'REMOTE_DIAG'
set -euo pipefail
container_name="${1:-}"
runtime="${2:-}"
runtime_workspace_path="${3:-}"
if [[ -z "${runtime_workspace_path}" && -n "${runtime}" && "${runtime}" != "podman" && "${runtime}" != "docker" ]]; then
  runtime_workspace_path="${runtime}"
  runtime=""
fi
if [[ -z "${runtime}" ]]; then
  if command -v podman >/dev/null 2>&1; then runtime=podman; elif command -v docker >/dev/null 2>&1; then runtime=docker; else echo "no podman/docker runtime found"; exit 0; fi
fi
echo "runtime=${runtime} container=${container_name} workspace=${runtime_workspace_path}"
"${runtime}" ps -a --format '{{.ID}} {{.Names}} {{.Status}}' | grep -F "${container_name}" || true
echo '--- logs tail ---'
"${runtime}" logs --tail 200 "${container_name}" || true
echo '--- exec workspace/readyz ---'
"${runtime}" exec "${container_name}" sh -lc 'set -eu; hostname; pwd; echo "workspace check:"; ls -la "$1" | sed -n "1,40p"; echo "readyz:"; (curl -fsS http://127.0.0.1:7781/readyz || wget -qO- http://127.0.0.1:7781/readyz || true)' sh "${runtime_workspace_path}" || true
REMOTE_DIAG
fi

jq -nc \
  --arg flow_id "${FLOW_ID}" \
  --arg deployment_id "${DEPLOYMENT_ID}" \
  --arg child_swarm_id "${CHILD_SWARM_ID}" \
  --arg managed_swarm_id "${MANAGED_SWARM_ID}" \
  --arg runtime_workspace_path "${RUNTIME_WORKSPACE_PATH}" \
  --arg workspace_binding_id "${WORKSPACE_BINDING_ID}" \
  --arg chat_session_id "${chat_session_id}" \
  --arg session_id "${session_id}" \
  --arg evidence_dir "${EVIDENCE_DIR}" \
  '{ok:true,flow_id:$flow_id,deployment_id:$deployment_id,child_swarm_id:$child_swarm_id,managed_swarm_id:$managed_swarm_id,runtime_workspace_path:$runtime_workspace_path,workspace_binding_id:$workspace_binding_id,chat_session_id:$chat_session_id,session_id:$session_id,evidence_dir:$evidence_dir}' \
  >"${EVIDENCE_DIR}/summary.json"

log "PASS managed-container Flow diagnostic"
cat "${EVIDENCE_DIR}/summary.json"
