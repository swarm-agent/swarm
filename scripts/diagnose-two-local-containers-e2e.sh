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
  --flow-proof                     Create/run Flow proofs for self, first local container, managed host, and first managed container targets.
  --flow-agent-name <name>          Flow agent profile. Default: swarm
  --flow-agent-mode <mode>          Flow agent mode/profile_mode. Default: primary
  --flow-provider <provider>        Flow proof model provider. Default: --ai-provider value
  --flow-model <model>              Flow proof model. Default: --ai-model value
  --flow-thinking <level>           Flow proof thinking level. Default: --ai-thinking value
  --flow-timeout-seconds <seconds>  Flow proof timeout per run. Default: 420
  --flow-cron-min-lead-seconds <n>  Minimum lead before near-term cron minute. Default: 10
  --timeout-seconds <seconds>      Target readiness timeout per container. Default: 180
  --help                          Show this help.

Environment equivalents:
  SWARM_PRIMARY_URL, SWARM_PRIMARY_SSH, SWARM_MANAGED_SSH,
  SWARM_MANAGED_SWARM_ID, SWARM_MANAGED_NAME, SWARM_SOURCE_WORKSPACE_PATH,
  SWARM_TWO_LOCAL_CONTAINER_PREFIX, SWARM_TWO_LOCAL_ARTIFACT_DIR,
  SWARM_FIREWORKS_KEY_PATH, SWARM_TWO_LOCAL_AI_PROOF, SWARM_TWO_LOCAL_AI_PROVIDER,
  SWARM_TWO_LOCAL_AI_MODEL, SWARM_TWO_LOCAL_AI_THINKING, SWARM_TWO_LOCAL_FLOW_PROOF,
  SWARM_TWO_LOCAL_FLOW_AGENT_NAME, SWARM_TWO_LOCAL_FLOW_AGENT_MODE,
  SWARM_TWO_LOCAL_FLOW_PROVIDER, SWARM_TWO_LOCAL_FLOW_MODEL, SWARM_TWO_LOCAL_FLOW_THINKING

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
FLOW_PROOF="${SWARM_TWO_LOCAL_FLOW_PROOF:-false}"
FLOW_AGENT_NAME="${SWARM_TWO_LOCAL_FLOW_AGENT_NAME:-swarm}"
FLOW_AGENT_MODE="${SWARM_TWO_LOCAL_FLOW_AGENT_MODE:-primary}"
FLOW_PROVIDER="${SWARM_TWO_LOCAL_FLOW_PROVIDER:-${AI_PROVIDER}}"
FLOW_MODEL="${SWARM_TWO_LOCAL_FLOW_MODEL:-${AI_MODEL}}"
FLOW_THINKING="${SWARM_TWO_LOCAL_FLOW_THINKING:-${AI_THINKING}}"
FLOW_TIMEOUT_SECONDS="${SWARM_TWO_LOCAL_FLOW_TIMEOUT_SECONDS:-420}"
FLOW_CRON_MIN_LEAD_SECONDS="${SWARM_TWO_LOCAL_FLOW_CRON_MIN_LEAD_SECONDS:-10}"
FROM_ZERO="false"
LINK_COMMAND=""
CLEANUP="false"
COOKIE_FILE=""
API_STATUS=""
API_BODY=""
CREATED_DEPLOYMENTS=()
CREATED_CHILDREN=()
CREATED_FLOWS=()

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
    --flow-proof) FLOW_PROOF="true"; shift ;;
    --flow-agent-name) FLOW_AGENT_NAME="${2:-}"; shift 2 ;;
    --flow-agent-mode) FLOW_AGENT_MODE="${2:-}"; shift 2 ;;
    --flow-provider) FLOW_PROVIDER="${2:-}"; shift 2 ;;
    --flow-model) FLOW_MODEL="${2:-}"; shift 2 ;;
    --flow-thinking) FLOW_THINKING="${2:-}"; shift 2 ;;
    --flow-timeout-seconds) FLOW_TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --flow-cron-min-lead-seconds) FLOW_CRON_MIN_LEAD_SECONDS="${2:-}"; shift 2 ;;
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
[[ "${FLOW_TIMEOUT_SECONDS}" =~ ^[0-9]+$ && "${FLOW_TIMEOUT_SECONDS}" -gt 0 ]] || fail "--flow-timeout-seconds must be a positive integer"
[[ "${FLOW_CRON_MIN_LEAD_SECONDS}" =~ ^[0-9]+$ && "${FLOW_CRON_MIN_LEAD_SECONDS}" -gt 0 ]] || fail "--flow-cron-min-lead-seconds must be a positive integer"
[[ -n "${FLOW_AGENT_NAME}" ]] || fail "--flow-agent-name is required"
[[ -n "${FLOW_AGENT_MODE}" ]] || fail "--flow-agent-mode is required"
[[ -n "${FLOW_PROVIDER}" ]] || fail "--flow-provider is required"
[[ -n "${FLOW_MODEL}" ]] || fail "--flow-model is required"
[[ -n "${FLOW_THINKING}" ]] || fail "--flow-thinking is required"
[[ -n "${PRIMARY_SSH}" ]] || fail "--primary-ssh is required"
[[ -n "${MANAGED_SSH}" ]] || fail "--managed-ssh is required"
[[ -n "${SOURCE_WORKSPACE_PATH}" ]] || fail "--source-workspace-path is required"
PRIMARY_URL="${PRIMARY_URL%/}"
if [[ -z "${ARTIFACT_DIR}" ]]; then
  ARTIFACT_DIR="${ROOT_DIR}/tmp/two-local-containers-diagnostics/$(date +%Y%m%d-%H%M%S)"
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
  for flow_id in "${CREATED_FLOWS[@]}"; do
    [[ -n "${flow_id}" ]] || continue
    api_json PUT "/v3/flows/${flow_id}" "$(jq -nc '{enabled:false,target:{},unassign_target:true}')" "${ARTIFACT_DIR}/cleanup_flow_${flow_id}_disable.json" 60 || true
    api_json DELETE "/v3/flows/${flow_id}" "" "${ARTIFACT_DIR}/cleanup_flow_${flow_id}_delete.json" 60 || true
  done
  if [[ "${#CREATED_DEPLOYMENTS[@]}" -gt 0 ]]; then
    printf '%s\n' "${CREATED_DEPLOYMENTS[@]}" | jq -R . | jq -s '{ids:.}' >"${ARTIFACT_DIR}/cleanup_delete_request.json"
    api_json POST "/v1/deploy/container/delete" "$(cat "${ARTIFACT_DIR}/cleanup_delete_request.json")" "${ARTIFACT_DIR}/cleanup_delete_response.json" 180
  fi
  set -e
}
trap cleanup_created EXIT

configure_flow_model_defaults() {
  if [[ "${FLOW_PROOF}" != "true" ]]; then
    return 0
  fi
  local body
  body="$(jq -nc \
    --arg provider "${FLOW_PROVIDER}" \
    --arg model "${FLOW_MODEL}" \
    --arg thinking "${FLOW_THINKING}" \
    '{provider:$provider,model:$model,thinking:$thinking}')"
  printf '%s\n' "${body}" >"${ARTIFACT_DIR}/flow_model_preference_request.json"
  log "configuring account model preference for Flow proof: ${FLOW_PROVIDER}/${FLOW_MODEL} thinking=${FLOW_THINKING}"
  api_json POST "/v1/model" "${body}" "${ARTIFACT_DIR}/flow_model_preference_response.json" 60
  [[ "$(json_get "${ARTIFACT_DIR}/flow_model_preference_response.json" '(.preference.provider // .provider // empty)')" == "${FLOW_PROVIDER}" ]] || fail "flow model preference provider was not applied"
  [[ "$(json_get "${ARTIFACT_DIR}/flow_model_preference_response.json" '(.preference.model // .model // empty)')" == "${FLOW_MODEL}" ]] || fail "flow model preference model was not applied"
  [[ "$(json_get "${ARTIFACT_DIR}/flow_model_preference_response.json" '(.preference.thinking // .thinking // empty)')" == "${FLOW_THINKING}" ]] || fail "flow model preference thinking was not applied"
}

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


flow_target_expr() {
  jq -c '{swarm_id:(.swarm_id // ""),kind:(.kind // ""),deployment_id:(.deployment_id // ""),name:(.name // "")} | with_entries(select(.value != ""))' "${1:-}"
}

flow_workspace_expr() {
  local workspace_path="${1:-}" runtime_workspace_path="${2:-}" binding_id="${3:-}"
  jq -nc \
    --arg workspace_path "${workspace_path}" \
    --arg host_workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg runtime_workspace_path "${runtime_workspace_path}" \
    --arg workspace_binding_id "${binding_id}" \
    --arg workspace_name "$(basename "${SOURCE_WORKSPACE_PATH}")" \
    '{workspace_path:$workspace_path,host_workspace_path:$host_workspace_path,workspace_name:$workspace_name,cwd:$workspace_path} + (if $runtime_workspace_path != "" then {runtime_workspace_path:$runtime_workspace_path} else {} end) + (if $workspace_binding_id != "" then {workspace_binding_id:$workspace_binding_id} else {} end)'
}

next_cron_schedule_json() {
  local now_epoch now_seconds due_epoch minute hour
  now_epoch="$(date -u +%s)"
  now_seconds="$(date -u +%S)"
  due_epoch=$((now_epoch - now_seconds + 60))
  if (( due_epoch - now_epoch < FLOW_CRON_MIN_LEAD_SECONDS )); then
    due_epoch=$((due_epoch + 60))
  fi
  minute="$(date -u -d "@${due_epoch}" +%M)"
  hour="$(date -u -d "@${due_epoch}" +%H)"
  jq -nc --arg cron "${minute} ${hour} * * *" --arg due_at "$(date -u -d "@${due_epoch}" +%Y-%m-%dT%H:%M:%SZ)" '{cadence:"daily",timezone:"UTC",cron:$cron,due_at:$due_at}'
}

run_flow_proof_run() {
  local label="${1:-}" target_json="${2:-}" workspace_json="${3:-}" run_kind="${4:-}" out_dir="${5:-}"
  local token flow_id prompt schedule_json enabled flow_body status session_id deadline latest_run_id=""
  mkdir -p -- "${out_dir}"
  token="TWO_LOCAL_FLOW_${label}_${run_kind}_OK"
  flow_id="flow-${label}-${run_kind}-$(date +%Y%m%d-%H%M%S)-${RANDOM}"
  prompt="Flow perimeter proof for ${label} ${run_kind}. Reply with exactly: ${token}"
  if [[ "${run_kind}" == "cron" ]]; then
    schedule_json="$(next_cron_schedule_json)"
    enabled="true"
    jq -r '.due_at' <<<"${schedule_json}" >"${out_dir}/cron_due_at.txt"
    schedule_json="$(jq -c 'del(.due_at)' <<<"${schedule_json}")"
  else
    schedule_json='{"cadence":"on_demand","timezone":"UTC"}'
    enabled="false"
  fi
  flow_body="$(jq -nc \
    --arg flow_id "${flow_id}" \
    --arg name "Two-local Flow proof ${label} ${run_kind}" \
    --argjson enabled "${enabled}" \
    --argjson target "${target_json}" \
    --arg agent_name "${FLOW_AGENT_NAME}" \
    --arg agent_mode "${FLOW_AGENT_MODE}" \
    --argjson workspace "${workspace_json}" \
    --argjson schedule "${schedule_json}" \
    --arg prompt "${prompt}" \
    '{flow_id:$flow_id,name:$name,enabled:$enabled,target:$target,agent:{profile_name:$agent_name,profile_mode:$agent_mode},workspace:$workspace,schedule:$schedule,catch_up_policy:{mode:"once"},intent:{prompt:$prompt,mode:"two_local_flow_proof"}}')"
  printf '%s\n' "${target_json}" >"${out_dir}/target.json"
  printf '%s\n' "${workspace_json}" >"${out_dir}/workspace.json"
  printf '%s\n' "${flow_body}" >"${out_dir}/flow_create_request.json"
  log "Flow proof ${label} ${run_kind}: creating ${flow_id}"
  api_json POST "/v3/flows" "${flow_body}" "${out_dir}/flow_create_response.json" 90
  [[ "$(json_get "${out_dir}/flow_create_response.json" '.ok // false')" == "true" ]] || fail "Flow proof ${label} ${run_kind} create ok=false"
  CREATED_FLOWS+=("${flow_id}")
  if [[ "${run_kind}" == "run_now" ]]; then
    api_json POST "/v3/flows/${flow_id}/run-now" "" "${out_dir}/flow_run_now_response.json" 90
    [[ "$(json_get "${out_dir}/flow_run_now_response.json" '.ok // false')" == "true" ]] || fail "Flow proof ${label} run-now ok=false"
    latest_run_id="$(jq -r '(.last_run.run_id // (.flow.last_run.run_id // "") // ((.run.reason // .result.ack.reason // "") | capture("run_now started (?<id>[^ ]+)")? | .id) // empty)' "${out_dir}/flow_run_now_response.json")"
  fi
  deadline=$((SECONDS + FLOW_TIMEOUT_SECONDS))
  while :; do
    api_json GET "/v3/flows/${flow_id}/status?limit=100" "" "${out_dir}/flow_status_poll.json" 30
    api_json GET "/v3/flows/${flow_id}/history?limit=100" "" "${out_dir}/flow_history_poll.json" 30
    if [[ -n "${latest_run_id}" ]]; then
      status="$(jq -r --arg run_id "${latest_run_id}" '[.history[]? | select((.run_id // "") == $run_id)] | last | .status // empty' "${out_dir}/flow_history_poll.json")"
      session_id="$(jq -r --arg run_id "${latest_run_id}" '[.history[]? | select((.run_id // "") == $run_id)] | last | .session_id // empty' "${out_dir}/flow_history_poll.json")"
    else
      status="$(jq -r '[.history[]?] | sort_by(.started_at // .scheduled_at // "") | last | .status // empty' "${out_dir}/flow_history_poll.json")"
      session_id="$(jq -r '[.history[]?] | sort_by(.started_at // .scheduled_at // "") | last | .session_id // empty' "${out_dir}/flow_history_poll.json")"
    fi
    if [[ "${status}" == "success" && -n "${session_id}" ]]; then
      cp -- "${out_dir}/flow_status_poll.json" "${out_dir}/flow_status.json"
      cp -- "${out_dir}/flow_history_poll.json" "${out_dir}/flow_history.json"
      printf '%s\n' "${session_id}" >"${out_dir}/flow_session_id.txt"
      break
    fi
    if [[ "${status}" == "failed" ]]; then
      cp -- "${out_dir}/flow_status_poll.json" "${out_dir}/flow_status.json"
      cp -- "${out_dir}/flow_history_poll.json" "${out_dir}/flow_history.json"
      fail "Flow proof ${label} ${run_kind} failed: $(jq -c '[.history[]? | select((.status // "") == "failed")] | last' "${out_dir}/flow_history.json")"
    fi
    [[ "${SECONDS}" -lt "${deadline}" ]] || { cp -- "${out_dir}/flow_status_poll.json" "${out_dir}/flow_status.json"; cp -- "${out_dir}/flow_history_poll.json" "${out_dir}/flow_history.json"; fail "Flow proof ${label} ${run_kind} timed out waiting for success"; }
    sleep 5
  done
  api_json GET "/v1/sessions/${session_id}" "" "${out_dir}/flow_session.json" 30 || true
  api_json GET "/v1/sessions/${session_id}/metadata" "" "${out_dir}/flow_session_metadata.json" 30 || true
  api_json GET "/v1/sessions/${session_id}/messages?limit=100" "" "${out_dir}/flow_messages.json" 30
  api_json GET "/v1/swarm/topology/session-route?session_id=$(urlencode "${session_id}")" "" "${out_dir}/flow_session_route.json" 30 || true
  if ! jq -e --arg token "${token}" '[.messages[]? | select(((.content // "") | contains($token)))] | length > 0' "${out_dir}/flow_messages.json" >/dev/null; then
    fail "Flow proof ${label} ${run_kind} completed but messages did not contain ${token}"
  fi
  if [[ "${run_kind}" == "cron" ]]; then
    api_json PUT "/v3/flows/${flow_id}" "$(jq -nc '{enabled:false}')" "${out_dir}/flow_disable_response.json" 60 || true
  fi
  jq -nc \
    --arg flow_id "${flow_id}" \
    --arg label "${label}" \
    --arg run_kind "${run_kind}" \
    --arg session_id "${session_id}" \
    --arg token "${token}" \
    '{ok:true,flow_id:$flow_id,label:$label,run_kind:$run_kind,session_id:$session_id,proof_token:$token}' \
    >"${out_dir}/summary.json"
}

run_flow_proof_target() {
  local label="${1:-}" target_json="${2:-}" workspace_json="${3:-}" out_dir="${4:-}"
  mkdir -p -- "${out_dir}"
  run_flow_proof_run "${label}" "${target_json}" "${workspace_json}" run_now "${out_dir}/run-now"
  run_flow_proof_run "${label}" "${target_json}" "${workspace_json}" cron "${out_dir}/cron"
  jq -s '{ok:true,runs:.}' "${out_dir}/run-now/summary.json" "${out_dir}/cron/summary.json" >"${out_dir}/summary.json"
}

run_flow_proofs() {
  if [[ "${FLOW_PROOF}" != "true" ]]; then
    return 0
  fi
  log "running Flow proofs across self, managed host, primary local container(s), and managed local container(s)"
  local flow_dir self_target self_workspace managed_target managed_workspace summary_file
  flow_dir="${ARTIFACT_DIR}/flow-proofs"
  mkdir -p -- "${flow_dir}"
  self_target='{"kind":"self"}'
  self_workspace="$(flow_workspace_expr "${SOURCE_WORKSPACE_PATH}" "" "")"
  run_flow_proof_target self "${self_target}" "${self_workspace}" "${flow_dir}/self"
  managed_target="$(flow_target_expr "${ARTIFACT_DIR}/managed_target.json")"
  local managed_deployment_id managed_runtime_path
  managed_deployment_id="$(json_get "${ARTIFACT_DIR}/managed_target.json" '.deployment_id // empty')"
  managed_binding_id="$(jq -r \
    --arg runtime "${MANAGED_SWARM_ID}" \
    --arg deployment "${managed_deployment_id}" \
    --arg source "${SOURCE_WORKSPACE_PATH}" \
    '[.workspace_bindings[]? | select((.source_workspace_path // "") == $source) | select((.destination_runtime_swarm_id // "") == $runtime or ($deployment != "" and (((.binding_id // "") == $deployment) or ((.binding_id // "") | contains(":" + $deployment + ":")))))] | last | .binding_id // empty' \
    "${ARTIFACT_DIR}/topology_after_create.json")"
  if [[ -n "${managed_binding_id}" ]]; then
    managed_runtime_path="$(jq -r --arg binding "${managed_binding_id}" '[.workspace_bindings[]? | select((.binding_id // "") == $binding)] | last | .destination_workspace_path // empty' "${ARTIFACT_DIR}/topology_after_create.json")"
    managed_workspace="$(flow_workspace_expr "${SOURCE_WORKSPACE_PATH}" "${managed_runtime_path}" "${managed_binding_id}")"
  else
    managed_workspace="$(flow_workspace_expr "${SOURCE_WORKSPACE_PATH}" "" "")"
  fi
  run_flow_proof_target managed_host "${managed_target}" "${managed_workspace}" "${flow_dir}/managed-host"
  # The first successful primary/managed container proof covers the two container target classes.
  # Extra containers are still created and readiness-checked; avoid multiplying expensive AI Flow runs.
  for summary_file in "${ARTIFACT_DIR}"/primary-*/summary.json; do
    [[ -f "${summary_file}" ]] || continue
    local item_dir label target_json workspace_json runtime_workspace_path binding_id
    item_dir="$(dirname "${summary_file}")"
    label="primary_container_$(basename "${item_dir}" | sed 's/^primary-//')"
    target_json="$(flow_target_expr "${item_dir}/target.json")"
    runtime_workspace_path="$(json_get "${summary_file}" '.runtime_workspace_path // empty')"
    binding_id="$(json_get "${summary_file}" '.binding_id // empty')"
    workspace_json="$(flow_workspace_expr "${SOURCE_WORKSPACE_PATH}" "${runtime_workspace_path}" "${binding_id}")"
    run_flow_proof_target "${label}" "${target_json}" "${workspace_json}" "${flow_dir}/${label}"
    break
  done
  for summary_file in "${ARTIFACT_DIR}"/managed-*/summary.json; do
    [[ -f "${summary_file}" ]] || continue
    local item_dir label target_json workspace_json runtime_workspace_path binding_id
    item_dir="$(dirname "${summary_file}")"
    label="managed_container_$(basename "${item_dir}" | sed 's/^managed-//')"
    target_json="$(flow_target_expr "${item_dir}/target.json")"
    runtime_workspace_path="$(json_get "${summary_file}" '.runtime_workspace_path // empty')"
    binding_id="$(json_get "${summary_file}" '.binding_id // empty')"
    workspace_json="$(flow_workspace_expr "${SOURCE_WORKSPACE_PATH}" "${runtime_workspace_path}" "${binding_id}")"
    run_flow_proof_target "${label}" "${target_json}" "${workspace_json}" "${flow_dir}/${label}"
    break
  done
  find "${flow_dir}" -mindepth 2 -maxdepth 2 -name summary.json -print0 | sort -z | xargs -0 jq -s '{ok:true,targets:.}' >"${flow_dir}/summary.json"
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
configure_flow_model_defaults
resolve_managed_target
api_json GET "/v1/swarm/topology" "" "${ARTIFACT_DIR}/topology_before.json" 30

for ((i = 1; i <= PRIMARY_COUNT; i++)); do
  create_container primary "${i}" ""
done
for ((i = 1; i <= MANAGED_COUNT; i++)); do
  create_container managed "${i}" "${MANAGED_SWARM_ID}"
done

capture_runtime_ps
run_flow_proofs
if [[ ! -f "${ARTIFACT_DIR}/flow-proofs/summary.json" ]]; then
  mkdir -p -- "${ARTIFACT_DIR}/flow-proofs"
  printf '{"ok":true,"skipped":true}\n' >"${ARTIFACT_DIR}/flow-proofs/summary.json"
fi

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
  --arg flow_proof "${FLOW_PROOF}" \
  --arg flow_provider "${FLOW_PROVIDER}" \
  --arg flow_model "${FLOW_MODEL}" \
  --arg flow_thinking "${FLOW_THINKING}" \
  --slurpfile flow_summary "${ARTIFACT_DIR}/flow-proofs/summary.json" \
  '{ok:true,primary_ssh:$primary_ssh,managed_ssh:$managed_ssh,managed_swarm_id:$managed_swarm_id,managed_name:$managed_name,source_workspace_path:$source_workspace_path,artifact_dir:$artifact_dir,ai_proof:($ai_proof == "true"),ai_provider:$ai_provider,ai_model:$ai_model,flow_proof:($flow_proof == "true"),flow_provider:$flow_provider,flow_model:$flow_model,flow_thinking:$flow_thinking,flow_proofs:($flow_summary[0] // null),containers:.}' \
  "${summary_items}" >"${ARTIFACT_DIR}/summary.json"

log "PASS two-local-containers diagnostic"
cat "${ARTIFACT_DIR}/summary.json"
