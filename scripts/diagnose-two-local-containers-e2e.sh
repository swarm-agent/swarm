#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/diagnose-two-local-containers-e2e.sh [options]

Focused live E2E diagnostic: create two local containers on the primary host and
two local containers on an already linked managed host, then verify all four child
targets become online/selectable with workspace bindings.

Required runtime inputs:
  --primary-ssh <alias> or SWARM_PRIMARY_SSH
  --managed-ssh <alias> or SWARM_MANAGED_SSH
  --primary-url defaults to http://127.0.0.1:7781 when running on the primary host

Options:
  --primary-url <url>              Primary swarmd API URL. Default: http://127.0.0.1:7781
  --primary-ssh <alias>            Primary SSH alias used for rebuild/key checks. Use local/self when running on primary. Required.
  --managed-ssh <alias>            Managed host SSH alias. Required.
  --managed-swarm-id <id>          Managed host swarm_id. Optional if --managed-name resolves it.
  --managed-name <name>            Managed host display name. If omitted, tries --managed-ssh, then a single online non-self target.
  --source-workspace-path <path>   Source workspace path on the primary/controller. Default: current directory.
  --container-prefix <name>        Container name prefix. Default: two-local-diag-<timestamp>
  --primary-count <n>              Primary-host local containers to create. Default: 2
  --managed-count <n>              Managed-host local containers to create. Default: 2
  --runtime <podman|docker>        Optional requested runtime passed to replicate.
  --artifact-dir <path>            Evidence directory. Default: tmp/two-local-containers-diagnostics/<timestamp>
  --from-zero                      Rebuild both hosts from zero before running (destructive remote DB reset).
  --link-command <command>         Command to run after --from-zero if managed target is absent.
  --fireworks-key-path <path>      Fireworks key path checked on primary SSH host. Enables the key check.
  --check-fireworks-key            Require --fireworks-key-path/SWARM_FIREWORKS_KEY_PATH to exist and be non-empty.
  --skip-fireworks-key-check       Do not check for the Fireworks key.
  --cleanup                        Delete created diagnostic containers at exit.
  --ai-proof                       For each created child, open a routed session, run AI, and require proof token.
  --ai-provider <provider>         AI provider for proof. Default: fireworks
  --ai-model <model>               AI model for proof. Default: accounts/fireworks/models/minimax-m2p5
  --ai-thinking <level>            AI thinking level. Default: low
  --ai-timeout-seconds <seconds>   AI proof timeout per container. Default: 240
  --timeout-seconds <seconds>      Target readiness timeout per container. Default: 180
  --help                          Show this help.

Environment equivalents:
  SWARM_PRIMARY_URL, SWARM_PRIMARY_SSH, SWARM_MANAGED_SSH,
  SWARM_MANAGED_SWARM_ID, SWARM_MANAGED_NAME, SWARM_SOURCE_WORKSPACE_PATH,
  SWARM_TWO_LOCAL_CONTAINER_PREFIX, SWARM_TWO_LOCAL_ARTIFACT_DIR,
  SWARM_FIREWORKS_KEY_PATH, SWARM_TWO_LOCAL_AI_PROOF, SWARM_TWO_LOCAL_AI_PROVIDER,
  SWARM_TWO_LOCAL_AI_MODEL, SWARM_TWO_LOCAL_AI_THINKING

No unit tests are run by this harness.
EOF
}

log() { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "${1:-}" >/dev/null 2>&1 || fail "required command not found: ${1:-}"; }
urlencode() { jq -rn --arg v "${1:-}" '$v|@uri'; }
json_get() { jq -r "${2:-.}" "${1:-}"; }
is_local_target() { [[ -z "${1:-}" || "${1:-}" == "local" || "${1:-}" == "localhost" || "${1:-}" == "self" ]]; }

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
PRIMARY_URL="${SWARM_PRIMARY_URL:-http://127.0.0.1:7781}"
PRIMARY_SSH="${SWARM_PRIMARY_SSH:-}"
MANAGED_SSH="${SWARM_MANAGED_SSH:-}"
MANAGED_SWARM_ID="${SWARM_MANAGED_SWARM_ID:-}"
MANAGED_NAME="${SWARM_MANAGED_NAME:-}"
SOURCE_WORKSPACE_PATH="${SWARM_SOURCE_WORKSPACE_PATH:-${ROOT_DIR}}"
CONTAINER_PREFIX="${SWARM_TWO_LOCAL_CONTAINER_PREFIX:-two-local-diag-$(date +%Y%m%d-%H%M%S)}"
PRIMARY_COUNT="${SWARM_TWO_LOCAL_PRIMARY_COUNT:-2}"
MANAGED_COUNT="${SWARM_TWO_LOCAL_MANAGED_COUNT:-2}"
RUNTIME="${SWARM_TWO_LOCAL_RUNTIME:-}"
ARTIFACT_DIR="${SWARM_TWO_LOCAL_ARTIFACT_DIR:-}"
TIMEOUT_SECONDS="${SWARM_TWO_LOCAL_TIMEOUT_SECONDS:-180}"
FIREWORKS_KEY_PATH="${SWARM_FIREWORKS_KEY_PATH:-}"
CHECK_FIREWORKS_KEY="${SWARM_FIREWORKS_KEY_CHECK:-false}"
[[ -n "${FIREWORKS_KEY_PATH}" ]] && CHECK_FIREWORKS_KEY="true"
AI_PROOF="${SWARM_TWO_LOCAL_AI_PROOF:-false}"
AI_PROVIDER="${SWARM_TWO_LOCAL_AI_PROVIDER:-fireworks}"
AI_MODEL="${SWARM_TWO_LOCAL_AI_MODEL:-accounts/fireworks/models/minimax-m2p5}"
AI_THINKING="${SWARM_TWO_LOCAL_AI_THINKING:-low}"
AI_TIMEOUT_SECONDS="${SWARM_TWO_LOCAL_AI_TIMEOUT_SECONDS:-240}"
FROM_ZERO="false"
LINK_COMMAND=""
CLEANUP="false"
COOKIE_FILE=""
API_STATUS=""
API_BODY=""
CREATED_DEPLOYMENTS=()
CREATED_CHILDREN=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --primary-url) PRIMARY_URL="${2:-}"; shift 2 ;;
    --primary-ssh) PRIMARY_SSH="${2:-}"; shift 2 ;;
    --managed-ssh) MANAGED_SSH="${2:-}"; shift 2 ;;
    --managed-swarm-id) MANAGED_SWARM_ID="${2:-}"; shift 2 ;;
    --managed-name) MANAGED_NAME="${2:-}"; shift 2 ;;
    --source-workspace-path|--workspace) SOURCE_WORKSPACE_PATH="${2:-}"; shift 2 ;;
    --container-prefix) CONTAINER_PREFIX="${2:-}"; shift 2 ;;
    --primary-count) PRIMARY_COUNT="${2:-}"; shift 2 ;;
    --managed-count) MANAGED_COUNT="${2:-}"; shift 2 ;;
    --runtime) RUNTIME="${2:-}"; shift 2 ;;
    --artifact-dir) ARTIFACT_DIR="${2:-}"; shift 2 ;;
    --timeout-seconds) TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --fireworks-key-path) FIREWORKS_KEY_PATH="${2:-}"; CHECK_FIREWORKS_KEY="true"; shift 2 ;;
    --check-fireworks-key) CHECK_FIREWORKS_KEY="true"; shift ;;
    --skip-fireworks-key-check) CHECK_FIREWORKS_KEY="false"; shift ;;
    --from-zero) FROM_ZERO="true"; shift ;;
    --link-command) LINK_COMMAND="${2:-}"; shift 2 ;;
    --cleanup) CLEANUP="true"; shift ;;
    --ai-proof) AI_PROOF="true"; shift ;;
    --ai-provider) AI_PROVIDER="${2:-}"; shift 2 ;;
    --ai-model) AI_MODEL="${2:-}"; shift 2 ;;
    --ai-thinking) AI_THINKING="${2:-}"; shift 2 ;;
    --ai-timeout-seconds) AI_TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

require_command curl
require_command jq
require_command ssh
[[ "${PRIMARY_COUNT}" =~ ^[0-9]+$ && "${PRIMARY_COUNT}" -gt 0 ]] || fail "--primary-count must be a positive integer"
[[ "${MANAGED_COUNT}" =~ ^[0-9]+$ && "${MANAGED_COUNT}" -gt 0 ]] || fail "--managed-count must be a positive integer"
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ && "${TIMEOUT_SECONDS}" -gt 0 ]] || fail "--timeout-seconds must be a positive integer"
[[ "${AI_TIMEOUT_SECONDS}" =~ ^[0-9]+$ && "${AI_TIMEOUT_SECONDS}" -gt 0 ]] || fail "--ai-timeout-seconds must be a positive integer"
[[ -n "${PRIMARY_SSH}" ]] || fail "--primary-ssh is required"
[[ -n "${MANAGED_SSH}" ]] || fail "--managed-ssh is required"
[[ -n "${SOURCE_WORKSPACE_PATH}" ]] || fail "--source-workspace-path is required"
PRIMARY_URL="${PRIMARY_URL%/}"
if [[ -z "${ARTIFACT_DIR}" ]]; then
  ARTIFACT_DIR="${ROOT_DIR}/.tmp/two-local-containers-diagnostics/$(date +%Y%m%d-%H%M%S)"
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
  if [[ "${#CREATED_DEPLOYMENTS[@]}" -gt 0 ]]; then
    printf '%s\n' "${CREATED_DEPLOYMENTS[@]}" | jq -R . | jq -s '{ids:.}' >"${ARTIFACT_DIR}/cleanup_delete_request.json"
    api_json POST "/v1/deploy/container/delete" "$(cat "${ARTIFACT_DIR}/cleanup_delete_request.json")" "${ARTIFACT_DIR}/cleanup_delete_response.json" 180
  fi
  set -e
}
trap cleanup_created EXIT

check_fireworks_key() {
  if [[ "${CHECK_FIREWORKS_KEY}" != "true" ]]; then
    printf '{"skipped":true}\n' >"${ARTIFACT_DIR}/fireworks_key_check.json"
    return 0
  fi
  [[ -n "${FIREWORKS_KEY_PATH}" ]] || fail "Fireworks key check requested but no --fireworks-key-path/SWARM_FIREWORKS_KEY_PATH was provided"
  log "checking Fireworks key on ${PRIMARY_SSH}:${FIREWORKS_KEY_PATH}"
  local check_script
  check_script='set -euo pipefail
key_path="${1:-}"
if [[ -n "${key_path}" && -s "${key_path}" ]]; then
  stat -c '\''{"ok":true,"path":"%n","size":%s,"mode":"%a"}'\'' "${key_path}"
  exit 0
fi
printf '\''{"ok":false,"path":"%s","error":"missing_or_empty"}\n'\'' "${key_path}"
exit 1'
  if is_local_target "${PRIMARY_SSH}"; then
    bash -s -- "${FIREWORKS_KEY_PATH}" >"${ARTIFACT_DIR}/fireworks_key_check.json" <<<"${check_script}"
  else
    ssh "${PRIMARY_SSH}" 'bash -s' -- "${FIREWORKS_KEY_PATH}" >"${ARTIFACT_DIR}/fireworks_key_check.json" <<<"${check_script}"
  fi
}

maybe_from_zero() {
  if [[ "${FROM_ZERO}" != "true" ]]; then
    return 0
  fi
  log "from-zero rebuild: ${PRIMARY_SSH}"
  "${ROOT_DIR}/scripts/ssh-fast-test.sh" "${PRIMARY_SSH}" --from-zero
  log "from-zero rebuild: ${MANAGED_SSH}"
  "${ROOT_DIR}/scripts/ssh-fast-test.sh" "${MANAGED_SSH}" --from-zero
}

resolve_managed_target() {
  local targets_file="${ARTIFACT_DIR}/targets_before.json"
  api_json GET "/v1/swarm/targets" "" "${targets_file}" 30
  if [[ -n "${MANAGED_SWARM_ID}" ]]; then
    jq -c --arg id "${MANAGED_SWARM_ID}" '.targets[]? | select((.swarm_id // "") == $id)' "${targets_file}" | head -n 1 | jq '.' >"${ARTIFACT_DIR}/managed_target.json"
  elif [[ -n "${MANAGED_NAME}" ]]; then
    jq -c --arg name "${MANAGED_NAME}" '.targets[]? | select((.name // "") == $name)' "${targets_file}" | head -n 1 | jq '.' >"${ARTIFACT_DIR}/managed_target.json"
  else
    jq -c --arg name "${MANAGED_SSH}" '.targets[]? | select((.name // "") == $name)' "${targets_file}" | head -n 1 | jq '.' >"${ARTIFACT_DIR}/managed_target.json"
    if [[ ! -s "${ARTIFACT_DIR}/managed_target.json" ]]; then
      local count
      count="$(jq -r '[.targets[]? | select((.online // false) == true and (.selectable // false) == true and ((.kind // "") != "self") and ((.kind // "") != "local"))] | length' "${targets_file}")"
      if [[ "${count}" == "1" ]]; then
        jq -c '.targets[]? | select((.online // false) == true and (.selectable // false) == true and ((.kind // "") != "self") and ((.kind // "") != "local"))' "${targets_file}" | head -n 1 | jq '.' >"${ARTIFACT_DIR}/managed_target.json"
      fi
    fi
  fi
  if [[ ! -s "${ARTIFACT_DIR}/managed_target.json" && -n "${LINK_COMMAND}" ]]; then
    log "managed target not found; running link command"
    bash -lc "${LINK_COMMAND}" | tee "${ARTIFACT_DIR}/link_command.log"
    api_json GET "/v1/swarm/targets" "" "${targets_file}" 30
    resolve_managed_target
    return 0
  fi
  [[ -s "${ARTIFACT_DIR}/managed_target.json" ]] || fail "managed target not found; pass --managed-swarm-id/--managed-name, or provide --link-command after --from-zero"
  MANAGED_SWARM_ID="$(json_get "${ARTIFACT_DIR}/managed_target.json" '.swarm_id // empty')"
  MANAGED_NAME="$(json_get "${ARTIFACT_DIR}/managed_target.json" '.name // empty')"
  [[ -n "${MANAGED_SWARM_ID}" ]] || fail "managed target missing swarm_id"
  [[ "$(json_get "${ARTIFACT_DIR}/managed_target.json" '.online // false')" == "true" ]] || fail "managed target ${MANAGED_SWARM_ID} is not online"
  [[ "$(json_get "${ARTIFACT_DIR}/managed_target.json" '.selectable // false')" == "true" ]] || fail "managed target ${MANAGED_SWARM_ID} is not selectable"
  log "managed target: name=${MANAGED_NAME:-<empty>} swarm_id=${MANAGED_SWARM_ID}"
}

create_container() {
  local location="${1:-}" index="${2:-}" target_host_swarm_id="${3:-}"
  local name="${CONTAINER_PREFIX}-${location}-${index}"
  local out_dir="${ARTIFACT_DIR}/${location}-${index}"
  mkdir -p -- "${out_dir}"
  local body
  body="$(jq -nc \
    --arg mode local \
    --arg swarm_name "${name}" \
    --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg target_host_swarm_id "${target_host_swarm_id}" \
    --arg runtime "${RUNTIME}" \
    '{mode:$mode,swarm_name:$swarm_name,sync:{enabled:true,mode:"managed"},workspaces:[{source_workspace_path:$source_workspace_path,replication_mode:"bundle",writable:true}]} + (if $target_host_swarm_id != "" then {target_host_swarm_id:$target_host_swarm_id} else {} end) + (if $runtime != "" then {runtime:$runtime} else {} end)')"
  printf '%s\n' "${body}" >"${out_dir}/replicate_request.json"
  log "creating ${location} container ${index}: ${name}"
  api_json POST "/v1/swarm/replicate" "${body}" "${out_dir}/replicate_response.json" 300
  [[ "$(json_get "${out_dir}/replicate_response.json" '.ok // false')" == "true" ]] || fail "replicate ok=false for ${name}"
  local deployment_id child_swarm_id runtime_workspace_path binding_id destination_host_swarm_id destination_runtime_swarm_id
  deployment_id="$(json_get "${out_dir}/replicate_response.json" '.swarm.deployment_id // empty')"
  child_swarm_id="$(json_get "${out_dir}/replicate_response.json" '.swarm.id // empty')"
  runtime_workspace_path="$(json_get "${out_dir}/replicate_response.json" '.workspaces[0].binding.destination_workspace_path // empty')"
  binding_id="$(json_get "${out_dir}/replicate_response.json" '.workspaces[0].binding.binding_id // empty')"
  destination_host_swarm_id="$(json_get "${out_dir}/replicate_response.json" '.workspaces[0].binding.destination_host_swarm_id // empty')"
  destination_runtime_swarm_id="$(json_get "${out_dir}/replicate_response.json" '.workspaces[0].binding.destination_runtime_swarm_id // empty')"
  [[ -n "${deployment_id}" ]] || fail "${name} missing deployment_id"
  [[ -n "${child_swarm_id}" ]] || fail "${name} missing child swarm id"
  [[ -n "${runtime_workspace_path}" ]] || fail "${name} missing runtime workspace path"
  [[ -n "${binding_id}" ]] || fail "${name} missing workspace binding id"
  [[ "${destination_runtime_swarm_id}" == "${child_swarm_id}" ]] || fail "${name} binding runtime swarm mismatch"
  if [[ "${location}" == "managed" ]]; then
    [[ "${destination_host_swarm_id}" == "${MANAGED_SWARM_ID}" ]] || fail "${name} destination_host_swarm_id=${destination_host_swarm_id}, expected ${MANAGED_SWARM_ID}"
  fi
  CREATED_DEPLOYMENTS+=("${deployment_id}")
  CREATED_CHILDREN+=("${child_swarm_id}")
  wait_for_child_target "${child_swarm_id}" "${out_dir}"
  jq -nc \
    --arg name "${name}" \
    --arg location "${location}" \
    --arg deployment_id "${deployment_id}" \
    --arg child_swarm_id "${child_swarm_id}" \
    --arg runtime_workspace_path "${runtime_workspace_path}" \
    --arg binding_id "${binding_id}" \
    '{name:$name,location:$location,deployment_id:$deployment_id,child_swarm_id:$child_swarm_id,runtime_workspace_path:$runtime_workspace_path,binding_id:$binding_id}' \
    >"${out_dir}/summary.json"
  if [[ "${AI_PROOF}" == "true" ]]; then
    run_ai_proof "${location}" "${index}" "${name}" "${child_swarm_id}" "${runtime_workspace_path}" "${out_dir}"
  fi
}

run_ai_proof() {
  local location="${1:-}" index="${2:-}" name="${3:-}" child_swarm_id="${4:-}" runtime_workspace_path="${5:-}" out_dir="${6:-}"
  local token session_body session_id prompt run_body run_id deadline encoded_session
  token="TWO_LOCAL_AI_${location}_${index}_OK"
  prompt="Two local container AI proof for ${name}. Reply with exactly: ${token}"
  session_body="$(jq -nc \
    --arg title "Two local AI proof ${name}" \
    --arg workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg host_workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg runtime_workspace_path "${runtime_workspace_path}" \
    --arg workspace_name "$(basename "${SOURCE_WORKSPACE_PATH}")" \
    --arg provider "${AI_PROVIDER}" \
    --arg model "${AI_MODEL}" \
    --arg thinking "${AI_THINKING}" \
    '{title:$title,workspace_path:$workspace_path,host_workspace_path:$host_workspace_path,runtime_workspace_path:$runtime_workspace_path,workspace_name:$workspace_name,mode:"auto",agent_name:"swarm",preference:{provider:$provider,model:$model,thinking:$thinking},metadata:{two_local_container_ai_proof:true}}')"
  printf '%s\n' "${session_body}" >"${out_dir}/ai_session_create_request.json"
  log "AI proof ${location} container ${index}: opening session on ${child_swarm_id}"
  api_json POST "/v1/sessions?swarm_id=$(urlencode "${child_swarm_id}")" "${session_body}" "${out_dir}/ai_session_create_response.json" 90
  session_id="$(json_get "${out_dir}/ai_session_create_response.json" '.session.id // empty')"
  [[ -n "${session_id}" ]] || fail "AI proof ${name} did not return session id"
  encoded_session="$(urlencode "${session_id}")"
  api_json GET "/v1/swarm/topology/session-route?session_id=${encoded_session}" "" "${out_dir}/ai_session_route_after_create.json" 30 || true
  api_json POST "/v1/sessions/${session_id}/messages" "$(jq -nc --arg content "${prompt}" '{role:"user",content:$content}')" "${out_dir}/ai_user_message_response.json" 60
  run_body="$(jq -nc --arg prompt "${prompt}" '{type:"run.start",prompt:$prompt,background:true,execution_context:{worktree_mode:"off"}}')"
  printf '%s\n' "${run_body}" >"${out_dir}/ai_run_request.json"
  api_json POST "/v1/sessions/${session_id}/run/stream" "${run_body}" "${out_dir}/ai_run_start_response.json" 90
  run_id="$(json_get "${out_dir}/ai_run_start_response.json" '.run_id // empty')"
  [[ -n "${run_id}" ]] || fail "AI proof ${name} did not return run_id"
  deadline=$((SECONDS + AI_TIMEOUT_SECONDS))
  while :; do
    api_json GET "/v1/sessions/${session_id}" "" "${out_dir}/ai_session_poll.json" 30 || true
    api_json GET "/v1/sessions/${session_id}/messages?limit=100" "" "${out_dir}/ai_messages_poll.json" 30
    if jq -e --arg token "${token}" '[.messages[]? | select((.role // "") == "assistant" and ((.content // "") | contains($token)))] | length > 0' "${out_dir}/ai_messages_poll.json" >/dev/null; then
      cp -- "${out_dir}/ai_messages_poll.json" "${out_dir}/ai_messages.json"
      break
    fi
    [[ "${SECONDS}" -lt "${deadline}" ]] || { cp -- "${out_dir}/ai_messages_poll.json" "${out_dir}/ai_messages.json"; fail "AI proof ${name} timed out waiting for ${token}"; }
    sleep 5
  done
  api_json GET "/v1/sessions/${session_id}/metadata" "" "${out_dir}/ai_session_metadata.json" 30 || true
  api_json GET "/v1/swarm/topology/session-route?session_id=${encoded_session}" "" "${out_dir}/ai_session_route.json" 30 || true
  jq -nc \
    --arg session_id "${session_id}" \
    --arg run_id "${run_id}" \
    --arg token "${token}" \
    --arg provider "${AI_PROVIDER}" \
    --arg model "${AI_MODEL}" \
    '{ok:true,session_id:$session_id,run_id:$run_id,proof_token:$token,provider:$provider,model:$model}' \
    >"${out_dir}/ai_summary.json"
  jq -s '.[0] + {ai: .[1]}' "${out_dir}/summary.json" "${out_dir}/ai_summary.json" >"${out_dir}/summary.with-ai.json"
  mv "${out_dir}/summary.with-ai.json" "${out_dir}/summary.json"
}

wait_for_child_target() {
  local child_swarm_id="${1:-}" out_dir="${2:-}"
  local encoded_child deadline
  encoded_child="$(urlencode "${child_swarm_id}")"
  deadline=$((SECONDS + TIMEOUT_SECONDS))
  while :; do
    api_json GET "/v1/swarm/targets?swarm_id=${encoded_child}" "" "${out_dir}/target_poll.json" 30
    if jq -e --arg id "${child_swarm_id}" '.targets[]? | select((.swarm_id // "") == $id and (.online // false) == true and (.selectable // false) == true)' "${out_dir}/target_poll.json" >/dev/null; then
      jq -c --arg id "${child_swarm_id}" '.targets[]? | select((.swarm_id // "") == $id)' "${out_dir}/target_poll.json" | head -n 1 | jq '.' >"${out_dir}/target.json"
      return 0
    fi
    [[ "${SECONDS}" -lt "${deadline}" ]] || fail "timed out waiting for child target ${child_swarm_id} online/selectable"
    sleep 3
  done
}


capture_runtime_ps() {
  api_json GET "/v1/deploy/container" "" "${ARTIFACT_DIR}/deployments_after_create.json" 30 || true
  api_json GET "/v1/swarm/targets" "" "${ARTIFACT_DIR}/targets_after_create.json" 30 || true
  api_json GET "/v1/swarm/topology" "" "${ARTIFACT_DIR}/topology_after_create.json" 30 || true
  if is_local_target "${PRIMARY_SSH}"; then
    bash -s >"${ARTIFACT_DIR}/primary_runtime_ps.txt" 2>&1 <<'REMOTE_PS' || true
set -euo pipefail
if command -v podman >/dev/null 2>&1; then podman ps -a --format '{{.ID}} {{.Names}} {{.Status}}'; elif command -v docker >/dev/null 2>&1; then docker ps -a --format '{{.ID}} {{.Names}} {{.Status}}'; else echo 'no runtime'; fi
REMOTE_PS
  else
    ssh "${PRIMARY_SSH}" 'bash -s' >"${ARTIFACT_DIR}/primary_runtime_ps.txt" 2>&1 <<'REMOTE_PS' || true
set -euo pipefail
if command -v podman >/dev/null 2>&1; then podman ps -a --format '{{.ID}} {{.Names}} {{.Status}}'; elif command -v docker >/dev/null 2>&1; then docker ps -a --format '{{.ID}} {{.Names}} {{.Status}}'; else echo 'no runtime'; fi
REMOTE_PS
  fi
  if is_local_target "${MANAGED_SSH}"; then
    bash -s >"${ARTIFACT_DIR}/managed_runtime_ps.txt" 2>&1 <<'REMOTE_PS' || true
set -euo pipefail
if command -v podman >/dev/null 2>&1; then podman ps -a --format '{{.ID}} {{.Names}} {{.Status}}'; elif command -v docker >/dev/null 2>&1; then docker ps -a --format '{{.ID}} {{.Names}} {{.Status}}'; else echo 'no runtime'; fi
REMOTE_PS
  else
    ssh "${MANAGED_SSH}" 'bash -s' >"${ARTIFACT_DIR}/managed_runtime_ps.txt" 2>&1 <<'REMOTE_PS' || true
set -euo pipefail
if command -v podman >/dev/null 2>&1; then podman ps -a --format '{{.ID}} {{.Names}} {{.Status}}'; elif command -v docker >/dev/null 2>&1; then docker ps -a --format '{{.ID}} {{.Names}} {{.Status}}'; else echo 'no runtime'; fi
REMOTE_PS
  fi
}

maybe_from_zero
check_fireworks_key
api_json GET "/v1/auth/desktop/session" "" "${ARTIFACT_DIR}/desktop_session.json" 20
api_json GET "/readyz" "" "${ARTIFACT_DIR}/readyz.json" 20
resolve_managed_target
api_json GET "/v1/swarm/topology" "" "${ARTIFACT_DIR}/topology_before.json" 30

for ((i = 1; i <= PRIMARY_COUNT; i++)); do
  create_container primary "${i}" ""
done
for ((i = 1; i <= MANAGED_COUNT; i++)); do
  create_container managed "${i}" "${MANAGED_SWARM_ID}"
done

capture_runtime_ps
summary_items="${ARTIFACT_DIR}/summary_items.json"
: >"${summary_items}"
for item in "${ARTIFACT_DIR}"/primary-*/summary.json "${ARTIFACT_DIR}"/managed-*/summary.json; do
  [[ -f "${item}" ]] || continue
  cat "${item}" >>"${summary_items}"
  printf '\n' >>"${summary_items}"
done
jq -s \
  --arg primary_ssh "${PRIMARY_SSH}" \
  --arg managed_ssh "${MANAGED_SSH}" \
  --arg managed_swarm_id "${MANAGED_SWARM_ID}" \
  --arg managed_name "${MANAGED_NAME}" \
  --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
  --arg artifact_dir "${ARTIFACT_DIR}" \
  --arg ai_proof "${AI_PROOF}" \
  --arg ai_provider "${AI_PROVIDER}" \
  --arg ai_model "${AI_MODEL}" \
  '{ok:true,primary_ssh:$primary_ssh,managed_ssh:$managed_ssh,managed_swarm_id:$managed_swarm_id,managed_name:$managed_name,source_workspace_path:$source_workspace_path,artifact_dir:$artifact_dir,ai_proof:($ai_proof == "true"),ai_provider:$ai_provider,ai_model:$ai_model,containers:.}' \
  "${summary_items}" >"${ARTIFACT_DIR}/summary.json"

log "PASS two-local-containers diagnostic"
cat "${ARTIFACT_DIR}/summary.json"
