#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"

# shellcheck disable=SC1091
source "${ROOT_DIR}/scripts/lib-lane.sh"

usage() {
  cat <<'EOF'
Usage: ./tests/swarmd/local_replicate_e2e.sh [options]

Run the real local /v1/swarm/replicate flow end to end against a fresh or reused
isolated host swarm:
  1. create or reuse an isolated temp XDG root for the host swarm
  2. write a swarm-enabled startup config for that isolated host
  3. optionally rebuild the host binaries and canonical child image
  4. start the isolated host swarm and fetch an attach token
  5. seed the source workspace with /v1/workspace/add
  6. create or select the current swarm group
  7. call /v1/swarm/replicate with the chosen runtime and workspace
  8. wait for child attach/finalize
  9. verify deployment state, workspace replication link, group membership, and child current group

Options:
  --runtime <podman|docker>           Explicit local container runtime. Default: host recommendation
  --swarm-name <name>                 Child swarm name. Default: local-replicate-child-<timestamp>
  --workspace-path <path>             Source workspace path. Default: repo root
  --workspace-name <name>             Workspace display name. Default: basename of workspace path
  --replication-mode <bundle|copy>    Explicit replication mode. Default: handler default
  --readonly                          Mark replicated workspace read-only
  --sync-enabled                      Enable managed sync in the replicate request
  --sync-mode <managed>               Explicit sync mode. Default when enabled: managed
  --sync-modules <csv>                Explicit sync modules. Default when enabled: credentials
  --sync-vault-password <value>       Vault password to send in the replicate request
  --sync-vault-password-env <name>    Read the vault password from an environment variable
  --verify-sync-state                 After attach, create a host credential plus a custom agent/tool and wait for the child to read them
  --verify-sync-crud-flow             After attach, prove real Fireworks credential CRUD plus synced agent/tool routed execution on the child
  --sync-verify-timeout <seconds>     Managed sync propagation wait timeout. Default: 45
  --proof-provider <id>               Provider for the routed proof. Default: fireworks
  --proof-model <id>                  Model for the routed proof. Default: accounts/fireworks/models/minimax-m2p5
  --proof-thinking <level>            Thinking level for the routed proof. Default: medium
  --proof-provider-key-env <name>     Read the routed proof provider key from an environment variable
  --proof-provider-key-file <path>    Read the routed proof provider key from a local file
  --proof-timeout <seconds>           Routed proof wait timeout. Default: 90
  --group-id <id>                     Existing target group id
  --group-name <name>                 Existing target group name, or name to create
  --group-network-name <name>         Explicit target group network name
  --host-swarm-name <name>            Host swarm name. Default: Replicate Test Host
  --host-advertise-host <host>        Host bind/advertise host. Default: 127.0.0.1
  --host-port <port>                  Host backend/API port. Default: 7781
  --host-desktop-port <port>          Host desktop port. Default: 5555
  --host-root <path>                  Reuse a specific isolated host root instead of mktemp
  --bypass-permissions <true|false>   Host startup bypass_permissions value. Default: true
  --skip-host-rebuild                 Reuse the current host binaries instead of rebuilding first
  --skip-image-rebuild                Reuse the current canonical child image instead of rebuilding first
  --poll-timeout <seconds>            Attach wait timeout. Default: 120
  --poll-interval <seconds>           Attach poll interval. Default: 2
  --log-tail <lines>                  Host/container log tail size. Default: 200
  --help                              Show this help text

Artifacts:
  The harness keeps its host root and artifacts directory on disk and prints
  both paths at the end so config, DB, logs, and final state remain inspectable.

Notes:
  - This runner uses /v1/swarm/replicate.
  - It does not use /v1/deploy/container/create.
  - It does not save attach tokens or vault passwords into the artifacts.
EOF
}

log() {
  printf '%s\n' "$*"
}

fail() {
  capture_failure_context "$*"
  printf 'error: %s\n' "$*" >&2
  exit 1
}

require_command() {
  local name="${1:-}"
  command -v "${name}" >/dev/null 2>&1 || fail "required command not found: ${name}"
}

trim() {
  local value="${1-}"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "${value}"
}

write_artifact() {
  local name="${1:-}"
  local content="${2-}"
  printf '%s' "${content}" >"${ARTIFACT_DIR}/${name}"
}

safe_write_artifact() {
  local name="${1:-}"
  local content="${2-}"
  if [[ -z "${ARTIFACT_DIR:-}" || ! -d "${ARTIFACT_DIR}" ]]; then
    return 0
  fi
  write_artifact "${name}" "${content}"
}

set_step() {
  CURRENT_STEP="${1:-}"
  safe_write_artifact "current-step.txt" "${CURRENT_STEP}"
}

cleanup_ephemeral_secrets() {
  if [[ -n "${PROOF_PROVIDER_KEY_TMP_FILE:-}" && -f "${PROOF_PROVIDER_KEY_TMP_FILE}" ]]; then
    rm -f -- "${PROOF_PROVIDER_KEY_TMP_FILE}"
  fi
}

trap cleanup_ephemeral_secrets EXIT

json_request() {
  json_request_capture "${1:-}" "${2:-}" "${3:-GET}" "${4:-}" "${5:-}" "${6:-30}"
  if [[ "${JSON_REQUEST_STATUS}" != 2* ]]; then
    fail "${3:-GET} ${1:-}${4:-} failed with status ${JSON_REQUEST_STATUS}: ${JSON_REQUEST_BODY}"
  fi
  printf '%s' "${JSON_REQUEST_BODY}"
}

json_request_capture() {
  local base_url="${1:-}"
  local token="${2:-}"
  local method="${3:-GET}"
  local path="${4:-}"
  local body="${5:-}"
  local max_time="${6:-30}"
  local url="${base_url%/}${path}"
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
    -X "${method}"
  )
  if [[ -n "${token}" ]]; then
    args+=(-H "Authorization: Bearer ${token}")
  fi
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
  local response_body
  response_body="$(cat -- "${response_file}")"
  rm -f -- "${response_file}"
  if [[ -n "${payload_file}" ]]; then
    rm -f -- "${payload_file}"
  fi
  JSON_REQUEST_STATUS="${http_code}"
  JSON_REQUEST_BODY="${response_body}"
}

json_get() {
  json_request "${1:-}" "${2:-}" GET "${3:-}"
}

json_post() {
  json_request "${1:-}" "${2:-}" POST "${3:-}" "${4:-}"
}

json_put() {
  json_request "${1:-}" "${2:-}" PUT "${3:-}" "${4:-}"
}

json_delete() {
  json_request "${1:-}" "${2:-}" DELETE "${3:-}" "${4:-}"
}

curl_http_code() {
  local url="${1:-}"
  local body_file
  body_file="$(mktemp)"
  local http_code
  if http_code="$(curl -sS --connect-timeout 2 --max-time 8 -o "${body_file}" -w '%{http_code}' "${url}" 2>/dev/null)"; then
    :
  else
    http_code="000"
  fi
  rm -f -- "${body_file}"
  printf '%s' "${http_code}"
}

api_request() {
  local method="${1:-GET}"
  local path="${2:-}"
  local body="${3:-}"
  json_request "${HOST_ADMIN_API_URL}" "${ATTACH_TOKEN}" "${method}" "${path}" "${body}"
}

api_request_capture() {
  local method="${1:-GET}"
  local path="${2:-}"
  local body="${3:-}"
  local max_time="${4:-30}"
  json_request_capture "${HOST_ADMIN_API_URL}" "${ATTACH_TOKEN}" "${method}" "${path}" "${body}" "${max_time}"
}

api_get() {
  api_request GET "$1"
}

api_post() {
  api_request POST "$1" "${2:-}"
}

api_put() {
  api_request PUT "$1" "${2:-}"
}

api_delete() {
  api_request DELETE "$1" "${2:-}"
}

child_api_request() {
  local method="${1:-GET}"
  local path="${2:-}"
  local body="${3:-}"
  json_request "${CHILD_API_URL}" "${CHILD_ATTACH_TOKEN}" "${method}" "${path}" "${body}"
}

child_api_request_capture() {
  local method="${1:-GET}"
  local path="${2:-}"
  local body="${3:-}"
  local max_time="${4:-30}"
  json_request_capture "${CHILD_API_URL}" "${CHILD_ATTACH_TOKEN}" "${method}" "${path}" "${body}" "${max_time}"
}

child_api_get() {
  child_api_request GET "$1"
}

prepare_proof_provider_key_file() {
  if [[ -n "${PROOF_PROVIDER_KEY_FILE:-}" ]]; then
    [[ -f "${PROOF_PROVIDER_KEY_FILE}" ]] || fail "proof provider key file does not exist: ${PROOF_PROVIDER_KEY_FILE}"
    [[ -s "${PROOF_PROVIDER_KEY_FILE}" ]] || fail "proof provider key file is empty: ${PROOF_PROVIDER_KEY_FILE}"
    return 0
  fi
  if [[ -n "${PROOF_PROVIDER_KEY_ENV:-}" ]]; then
    local key_value
    key_value="${!PROOF_PROVIDER_KEY_ENV:-}"
    [[ -n "${key_value}" ]] || fail "environment variable ${PROOF_PROVIDER_KEY_ENV} is empty or unset"
    PROOF_PROVIDER_KEY_TMP_FILE="$(mktemp)"
    chmod 0600 "${PROOF_PROVIDER_KEY_TMP_FILE}"
    printf '%s' "${key_value}" >"${PROOF_PROVIDER_KEY_TMP_FILE}"
    PROOF_PROVIDER_KEY_FILE="${PROOF_PROVIDER_KEY_TMP_FILE}"
    return 0
  fi
  fail "--verify-sync-crud-flow requires --proof-provider-key-file or --proof-provider-key-env"
}

normalize_sync_modules() {
  local raw="${1:-}"
  local part
  local parts=()
  local normalized=()
  SYNC_MODULES=()
  if [[ -z "${raw}" ]]; then
    return 0
  fi
  IFS=',' read -r -a parts <<<"${raw}"
  for part in "${parts[@]}"; do
    part="$(trim "${part}")"
    case "${part}" in
      credentials|agents|custom_tools)
        ;;
      "")
        continue
        ;;
      *)
        fail "--sync-modules entry must be one of credentials, agents, or custom_tools (got ${part})"
        ;;
    esac
    local already_present="false"
    local current
    for current in "${normalized[@]:-}"; do
      if [[ "${current}" == "${part}" ]]; then
        already_present="true"
        break
      fi
    done
    if [[ "${already_present}" == "true" ]]; then
      continue
    fi
    normalized+=("${part}")
  done
  if (( ${#normalized[@]} == 0 )); then
    fail "--sync-modules resolved to an empty set"
  fi
  SYNC_MODULES=("${normalized[@]}")
}

sync_module_enabled() {
  local wanted="${1:-}"
  local current
  for current in "${SYNC_MODULES[@]:-}"; do
    if [[ "${current}" == "${wanted}" ]]; then
      return 0
    fi
  done
  return 1
}

sync_modules_json() {
  if (( ${#SYNC_MODULES[@]} == 0 )); then
    printf '[]'
    return 0
  fi
  printf '%s\n' "${SYNC_MODULES[@]}" | jq -R . | jq -c -s .
}

create_routed_host_session() {
  local session_title="${1:-}"
  local agent_name="${2:-}"
  [[ -n "${CHILD_SWARM_ID:-}" ]] || fail "child swarm id is not set for routed session create"
  [[ -n "${TARGET_WORKSPACE_PATH:-}" ]] || fail "target workspace path is not set for routed session create"
  local payload
  payload="$(build_routed_session_payload "${session_title}" "${agent_name}")"
  api_post "/v1/sessions?swarm_id=${CHILD_SWARM_ID}" "${payload}"
}

start_background_routed_run() {
  local session_id="${1:-}"
  local prompt="${2:-}"
  local agent_name="${3:-}"
  local tool_name="${4:-}"
  [[ -n "${session_id}" ]] || fail "session id is required for routed run start"
  local payload
  payload="$(build_routed_run_payload "${prompt}" "${agent_name}" "${tool_name}" "true")"
  api_post "/v1/sessions/${session_id}/run/stream" "${payload}"
}

wait_for_pending_permission() {
  local session_id="${1:-}"
  local tool_name="${2:-}"
  [[ -n "${session_id}" ]] || fail "session id is required for pending permission wait"
  [[ -n "${tool_name}" ]] || fail "tool name is required for pending permission wait"
  local start_ts permission_json match
  start_ts="$(date +%s)"
  while :; do
    permission_json="$(api_get "/v1/sessions/${session_id}/permissions?limit=200")"
    match="$(printf '%s' "${permission_json}" | jq -c --arg tool_name "${tool_name}" '[.permissions[]? | select((.status // "") == "pending" and (.tool_name // "") == $tool_name)] | sort_by(.created_at // 0) | last // empty')"
    if [[ -n "${match}" && "${match}" != "null" ]]; then
      printf '%s' "${match}"
      return 0
    fi
    if (( "$(date +%s)" - start_ts >= PROOF_TIMEOUT_SECONDS )); then
      fail "timed out waiting for pending permission ${tool_name} on session ${session_id}"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

resolve_session_permission() {
  local session_id="${1:-}"
  local permission_id="${2:-}"
  local action="${3:-approve}"
  local reason="${4:-ok}"
  [[ -n "${session_id}" ]] || fail "session id is required for permission resolve"
  [[ -n "${permission_id}" ]] || fail "permission id is required for permission resolve"
  local payload
  payload="$(jq -nc --arg action "${action}" --arg reason "${reason}" '{action:$action,reason:$reason}')"
  api_post "/v1/sessions/${session_id}/permissions/${permission_id}/resolve" "${payload}"
}

wait_for_assistant_message_content() {
  local session_id="${1:-}"
  local want_content="${2:-}"
  local artifact_prefix="${3:-session-message}"
  [[ -n "${session_id}" ]] || fail "session id is required for assistant message wait"
  [[ -n "${want_content}" ]] || fail "wanted assistant message content is required"
  local start_ts
  start_ts="$(date +%s)"
  while :; do
    local session_json messages_json permissions_json
    session_json="$(api_get "/v1/sessions/${session_id}")"
    messages_json="$(api_get "/v1/sessions/${session_id}/messages?limit=200")"
    permissions_json="$(api_get "/v1/sessions/${session_id}/permissions?limit=50")"
    if printf '%s' "${messages_json}" | jq -e '.messages[]? | select((.role // "") == "assistant" and (.content // "") == "WRONG_AGENT")' >/dev/null 2>&1; then
      write_artifact "${artifact_prefix}-session.wrong-agent.json" "${session_json}"
      write_artifact "${artifact_prefix}-messages.wrong-agent.json" "${messages_json}"
      write_artifact "${artifact_prefix}-permissions.wrong-agent.json" "${permissions_json}"
      capture_session_snapshot "${session_id}" "${artifact_prefix}-wrong-agent" 15
      maybe_capture_routed_run_diagnostic
      fail "assistant replied WRONG_AGENT while waiting for ${want_content}"
    fi
    if printf '%s' "${messages_json}" | jq -e --arg want "${want_content}" '.messages[]? | select((.role // "") == "assistant" and ((.content // "") | contains($want)))' >/dev/null 2>&1; then
      write_artifact "${artifact_prefix}-session.json" "${session_json}"
      write_artifact "${artifact_prefix}-messages.json" "${messages_json}"
      write_artifact "${artifact_prefix}-permissions.json" "${permissions_json}"
      capture_session_snapshot "${session_id}" "${artifact_prefix}" 15
      return 0
    fi
    if printf '%s' "${messages_json}" | jq -e '.messages[]? | select((.role // "") == "assistant" and ((.content // "") | contains("[TOOL_CALL]")))' >/dev/null 2>&1; then
      write_artifact "${artifact_prefix}-session.pseudo-tool-call.json" "${session_json}"
      write_artifact "${artifact_prefix}-messages.pseudo-tool-call.json" "${messages_json}"
      write_artifact "${artifact_prefix}-permissions.pseudo-tool-call.json" "${permissions_json}"
      capture_session_snapshot "${session_id}" "${artifact_prefix}-pseudo-tool-call" 15
      maybe_capture_routed_run_diagnostic
      fail "assistant emitted pseudo tool-call text while waiting for ${want_content}"
    fi
    if printf '%s' "${session_json}" | jq -e '.session.lifecycle.phase == "completed"' >/dev/null 2>&1; then
      write_artifact "${artifact_prefix}-session.completed-without-success.json" "${session_json}"
      write_artifact "${artifact_prefix}-messages.completed-without-success.json" "${messages_json}"
      write_artifact "${artifact_prefix}-permissions.completed-without-success.json" "${permissions_json}"
      capture_session_snapshot "${session_id}" "${artifact_prefix}-completed-without-success" 15
      maybe_capture_routed_run_diagnostic
      fail "routed run completed without assistant message containing ${want_content}"
    fi
    if (( "$(date +%s)" - start_ts >= PROOF_TIMEOUT_SECONDS )); then
      write_artifact "${artifact_prefix}-session.timeout.json" "${session_json}"
      write_artifact "${artifact_prefix}-messages.timeout.json" "${messages_json}"
      write_artifact "${artifact_prefix}-permissions.timeout.json" "${permissions_json}"
      capture_session_snapshot "${session_id}" "${artifact_prefix}-timeout" 15
      maybe_capture_routed_run_diagnostic
      fail "timed out waiting for assistant message containing ${want_content}"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

wait_for_child_credentials_exact() {
  local provider="${1:-}"
  local expected_ids_json="${2:-[]}"
  local active_id="${3:-}"
  local artifact_prefix="${4:-child-credentials}"
  [[ -n "${provider}" ]] || fail "provider is required for child credential wait"
  local start_ts
  start_ts="$(date +%s)"
  while :; do
    local credentials_json present_ids_json active_seen
    credentials_json="$(child_api_get "/v1/auth/credentials?provider=${provider}&limit=50")"
    present_ids_json="$(printf '%s' "${credentials_json}" | jq -c '[.records[]? | .id] | sort')"
    active_seen="$(printf '%s' "${credentials_json}" | jq -r '[.records[]? | select(.active == true) | .id] | if length == 1 then .[0] else "" end')"
    if [[ "${present_ids_json}" == "$(printf '%s' "${expected_ids_json}" | jq -c 'sort')" && "${active_seen}" == "${active_id}" ]]; then
      write_artifact "${artifact_prefix}.json" "${credentials_json}"
      return 0
    fi
    if (( "$(date +%s)" - start_ts >= SYNC_VERIFY_TIMEOUT_SECONDS )); then
      write_artifact "${artifact_prefix}.timeout.json" "${credentials_json}"
      fail "timed out waiting for child credentials provider=${provider} ids=${expected_ids_json} active=${active_id} (saw ids=${present_ids_json} active=${active_seen:-<empty>})"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

wait_for_child_probe_state() {
  local want_agent_present="${1:-true}"
  local want_tool_present="${2:-true}"
  local expected_prompt="${3:-}"
  local expected_command="${4:-}"
  local expected_active_primary="${5:-}"
  local artifact_prefix="${6:-child-sync-probe}"
  local start_ts
  start_ts="$(date +%s)"
  while :; do
    local child_agents_json child_tools_json
    child_agents_json="$(child_api_get '/v2/agents?limit=200')"
    child_tools_json="$(child_api_get '/v2/custom-tools?limit=200')"

    local agent_seen tool_seen active_primary
    agent_seen="$(printf '%s' "${child_agents_json}" | jq -r --arg name "${SYNC_PROBE_AGENT_NAME}" --arg prompt "${expected_prompt}" '[.state.profiles[]? | select(.name == $name and (.prompt // "") == $prompt)] | length')"
    tool_seen="$(printf '%s' "${child_tools_json}" | jq -r --arg name "${SYNC_PROBE_TOOL_NAME}" --arg command "${expected_command}" '[.custom_tools[]? | select(.name == $name and (.command // "") == $command)] | length')"
    active_primary="$(printf '%s' "${child_agents_json}" | jq -r '.state.active_primary // empty')"

    local agent_ok tool_ok active_ok
    agent_ok="false"
    tool_ok="false"
    active_ok="false"
    if [[ "${want_agent_present}" == "true" && "${agent_seen}" == "1" ]]; then
      agent_ok="true"
    elif [[ "${want_agent_present}" == "false" && "${agent_seen}" == "0" ]]; then
      agent_ok="true"
    fi
    if [[ "${want_tool_present}" == "true" && "${tool_seen}" == "1" ]]; then
      tool_ok="true"
    elif [[ "${want_tool_present}" == "false" && "${tool_seen}" == "0" ]]; then
      tool_ok="true"
    fi
    if [[ -z "${expected_active_primary}" ]]; then
      if [[ "${want_agent_present}" == "false" && "${active_primary}" != "${SYNC_PROBE_AGENT_NAME}" ]]; then
        active_ok="true"
      elif [[ "${want_agent_present}" == "true" ]]; then
        active_ok="true"
      fi
    elif [[ "${active_primary}" == "${expected_active_primary}" ]]; then
      active_ok="true"
    fi

    if [[ "${agent_ok}" == "true" && "${tool_ok}" == "true" && "${active_ok}" == "true" ]]; then
      write_artifact "${artifact_prefix}-agents.json" "${child_agents_json}"
      write_artifact "${artifact_prefix}-tools.json" "${child_tools_json}"
      return 0
    fi

    if (( "$(date +%s)" - start_ts >= SYNC_VERIFY_TIMEOUT_SECONDS )); then
      write_artifact "${artifact_prefix}-agents.timeout.json" "${child_agents_json}"
      write_artifact "${artifact_prefix}-tools.timeout.json" "${child_tools_json}"
      fail "timed out waiting for child probe state agent_present=${want_agent_present} tool_present=${want_tool_present} active_primary=${expected_active_primary:-<any>} (saw agent=${agent_seen} tool=${tool_seen} active_primary=${active_primary:-<empty>})"
    fi

    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

port_is_available() {
  local port="${1:-0}"
  if command -v ss >/dev/null 2>&1; then
    if ss -ltn "( sport = :${port} )" 2>/dev/null | awk 'NR > 1 { found = 1 } END { exit(found ? 0 : 1) }'; then
      return 1
    fi
    return 0
  fi
  if command -v lsof >/dev/null 2>&1; then
    if lsof -nP -iTCP:"${port}" -sTCP:LISTEN >/dev/null 2>&1; then
      return 1
    fi
    return 0
  fi
  fail "unable to check local port availability because neither ss nor lsof is installed"
}

write_host_startup_config() {
  mkdir -p "$(dirname -- "${HOST_STARTUP_CONFIG}")"
  cat >"${HOST_STARTUP_CONFIG}" <<EOF
startup_mode = box
host = ${HOST_BIND_HOST}
port = ${HOST_BACKEND_PORT}
advertise_host = ${HOST_ADVERTISE_HOST}
advertise_port = ${HOST_BACKEND_PORT}
desktop_port = ${HOST_DESKTOP_PORT}
bypass_permissions = ${BYPASS_PERMISSIONS}
retain_tool_output_history = false
swarm_name = ${HOST_SWARM_NAME}
swarm_mode = true
child = false
mode = lan
tailscale_url =
local_transport_port = 7790
peer_transport_port = 7791
parent_swarm_id =
pairing_state =
deploy_container_enabled = false
deploy_container_host_driven = false
deploy_container_sync_enabled = false
deploy_container_sync_mode =
deploy_container_sync_owner_swarm_id =
deploy_container_sync_credential_url =
deploy_container_deployment_id =
deploy_container_host_api_base_url =
deploy_container_host_desktop_url =
deploy_container_local_transport_socket_path =
deploy_container_bootstrap_secret =
deploy_container_verification_code =
remote_deploy_enabled = false
remote_deploy_session_id =
remote_deploy_session_token =
remote_deploy_host_api_base_url =
remote_deploy_host_desktop_url =
remote_deploy_invite_token =
EOF
}

write_host_control_files() {
  cat >"${HOST_ROOT}/start-host.sh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
cd "${ROOT_DIR}"
XDG_CONFIG_HOME="${HOST_XDG_CONFIG_HOME}" \\
XDG_DATA_HOME="${HOST_XDG_DATA_HOME}" \\
XDG_STATE_HOME="${HOST_XDG_STATE_HOME}" \\
XDG_CACHE_HOME="${HOST_XDG_CACHE_HOME}" \\
SWARM_LANE=main \\
./swarmd/scripts/dev-up.sh
EOF
  chmod 0755 "${HOST_ROOT}/start-host.sh"

  cat >"${HOST_ROOT}/stop-host.sh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
cd "${ROOT_DIR}"
XDG_CONFIG_HOME="${HOST_XDG_CONFIG_HOME}" \\
XDG_DATA_HOME="${HOST_XDG_DATA_HOME}" \\
XDG_STATE_HOME="${HOST_XDG_STATE_HOME}" \\
XDG_CACHE_HOME="${HOST_XDG_CACHE_HOME}" \\
SWARM_LANE=main \\
./swarmd/scripts/dev-down.sh
EOF
  chmod 0755 "${HOST_ROOT}/stop-host.sh"

  jq -nc \
    --arg host_root "${HOST_ROOT}" \
    --arg host_swarm_name "${HOST_SWARM_NAME}" \
    --arg config_path "${HOST_STARTUP_CONFIG}" \
    --arg state_root "${STATE_ROOT}" \
    --arg data_dir "${DATA_DIR}" \
    --arg log_file "${LOG_FILE}" \
    --arg api_url "${SWARMD_URL}" \
    --arg desktop_url "${HOST_DESKTOP_URL}" \
    --arg start_script "${HOST_ROOT}/start-host.sh" \
    --arg stop_script "${HOST_ROOT}/stop-host.sh" \
    '{host_root:$host_root,host_swarm_name:$host_swarm_name,config_path:$config_path,state_root:$state_root,data_dir:$data_dir,log_file:$log_file,api_url:$api_url,desktop_url:$desktop_url,start_script:$start_script,stop_script:$stop_script}' \
    >"${HOST_ROOT}/host-summary.json"
}

install_host_desktop_assets() {
  local source_dir="${ROOT_DIR}/web/dist"
  local target_dir="${HOST_XDG_DATA_HOME}/swarm/share"
  if [[ ! -f "${source_dir}/index.html" ]]; then
    return 0
  fi
  rm -rf "${target_dir}"
  mkdir -p "${target_dir}"
  cp -R "${source_dir}/." "${target_dir}/"
}

prepare_isolated_host() {
  HOST_ROOT="${HOST_ROOT_OVERRIDE:-$(mktemp -d "${TMPDIR:-/tmp}/swarm-replicate-XXXXXX")}"
  HOST_XDG_CONFIG_HOME="${HOST_ROOT}/xdg/config"
  HOST_XDG_DATA_HOME="${HOST_ROOT}/xdg/data"
  HOST_XDG_STATE_HOME="${HOST_ROOT}/xdg/state"
  HOST_XDG_CACHE_HOME="${HOST_ROOT}/xdg/cache"

  mkdir -p "${HOST_XDG_CONFIG_HOME}" "${HOST_XDG_DATA_HOME}" "${HOST_XDG_STATE_HOME}" "${HOST_XDG_CACHE_HOME}"

  export XDG_CONFIG_HOME="${HOST_XDG_CONFIG_HOME}"
  export XDG_DATA_HOME="${HOST_XDG_DATA_HOME}"
  export XDG_STATE_HOME="${HOST_XDG_STATE_HOME}"
  export XDG_CACHE_HOME="${HOST_XDG_CACHE_HOME}"

  HOST_ADVERTISE_HOST="${HOST_ADVERTISE_HOST_OVERRIDE:-127.0.0.1}"
  HOST_BIND_HOST="${HOST_ADVERTISE_HOST}"

  port_is_available "${HOST_BACKEND_PORT}" || fail "requested host backend port ${HOST_BACKEND_PORT} is already in use"
  port_is_available "${HOST_DESKTOP_PORT}" || fail "requested host desktop port ${HOST_DESKTOP_PORT} is already in use"

  HOST_STARTUP_CONFIG="${HOST_XDG_CONFIG_HOME}/swarm/swarm.conf"
  write_host_startup_config

  swarm_lane_export_profile main "${ROOT_DIR}"
  HOST_ADMIN_API_URL="${SWARMD_URL}"
  HOST_DESKTOP_URL="http://${HOST_ADVERTISE_HOST}:${HOST_DESKTOP_PORT}"

  ARTIFACT_DIR="${HOST_ROOT}/artifacts"
  mkdir -p "${ARTIFACT_DIR}"

  write_host_control_files
  install_host_desktop_assets
  write_artifact "host-startup-config.txt" "$(cat -- "${HOST_STARTUP_CONFIG}")"
}

ensure_host_running() {
  local ready_code
  ready_code="$(curl_http_code "${HOST_ADMIN_API_URL%/}/readyz")"
  if [[ "${ready_code}" == "200" ]]; then
    return 0
  fi
  log "Starting isolated replicate host ${HOST_SWARM_NAME} at ${HOST_ADMIN_API_URL}"
  (
    cd "${ROOT_DIR}"
    XDG_CONFIG_HOME="${HOST_XDG_CONFIG_HOME}" \
    XDG_DATA_HOME="${HOST_XDG_DATA_HOME}" \
    XDG_STATE_HOME="${HOST_XDG_STATE_HOME}" \
    XDG_CACHE_HOME="${HOST_XDG_CACHE_HOME}" \
    SWARM_LANE=main \
    ./swarmd/scripts/dev-up.sh
  )
  ready_code="$(curl_http_code "${HOST_ADMIN_API_URL%/}/readyz")"
  [[ "${ready_code}" == "200" ]] || fail "isolated replicate host did not become ready at ${HOST_ADMIN_API_URL}"
}

fetch_attach_token() {
  local response
  response="$(curl -fsS \
    -H "Origin: ${HOST_ADMIN_API_URL%/}" \
    -H "Referer: ${HOST_ADMIN_API_URL%/}/" \
    -H 'Sec-Fetch-Site: same-origin' \
    "${HOST_ADMIN_API_URL%/}/v1/auth/attach/token")"
  ATTACH_TOKEN="$(printf '%s' "${response}" | jq -r '.token // empty')"
  [[ -n "${ATTACH_TOKEN}" ]] || fail "failed to fetch attach token from ${HOST_ADMIN_API_URL%/}/v1/auth/attach/token"
}

resolve_runtime() {
  if [[ -n "${RUNTIME}" ]]; then
    return 0
  fi
  local runtime_status_json
  runtime_status_json="$(api_get '/v1/deploy/container/runtime')"
  write_artifact "runtime-status.json" "${runtime_status_json}"
  RUNTIME="$(printf '%s' "${runtime_status_json}" | jq -r '.runtime.recommended // empty')"
  if [[ -z "${RUNTIME}" ]]; then
    RUNTIME="$(printf '%s' "${runtime_status_json}" | jq -r '.runtime.available[0] // empty')"
  fi
  [[ -n "${RUNTIME}" ]] || fail "no supported local container runtime is available"
}

maybe_rebuild_image() {
  if [[ "${REBUILD_IMAGE}" != "true" ]]; then
    return 0
  fi
  log "Rebuilding canonical child image with runtime ${RUNTIME}"
  (
    cd "${ROOT_DIR}"
    XDG_CONFIG_HOME="${HOST_XDG_CONFIG_HOME}" \
    XDG_DATA_HOME="${HOST_XDG_DATA_HOME}" \
    XDG_STATE_HOME="${HOST_XDG_STATE_HOME}" \
    XDG_CACHE_HOME="${HOST_XDG_CACHE_HOME}" \
    SWARM_LANE=main \
    BUILD_RUNTIME="${RUNTIME}" \
    ./scripts/rebuild-container.sh --image-only
  )
}

maybe_rebuild_host() {
  if [[ "${REBUILD_HOST}" != "true" ]]; then
    return 0
  fi
  log "Rebuilding isolated host binaries"
  (
    cd "${ROOT_DIR}"
    XDG_CONFIG_HOME="${HOST_XDG_CONFIG_HOME}" \
    XDG_DATA_HOME="${HOST_XDG_DATA_HOME}" \
    XDG_STATE_HOME="${HOST_XDG_STATE_HOME}" \
    XDG_CACHE_HOME="${HOST_XDG_CACHE_HOME}" \
    SWARM_LANE=main \
    SWARMD_BUILD_HARD_RESTART=0 \
    ./swarmd/scripts/dev-build.sh
  )
  install_host_desktop_assets
}

fetch_groups_json() {
  api_get '/v1/swarm/groups'
}

set_current_group() {
  local target_group_id="${1:-}"
  local payload
  payload="$(jq -nc --arg group_id "${target_group_id}" '{group_id:$group_id}')"
  api_post '/v1/swarm/groups/current' "${payload}" >/dev/null
}

ensure_target_group() {
  local groups_json
  groups_json="$(fetch_groups_json)"
  write_artifact "groups-before.json" "${groups_json}"

  if [[ -n "${GROUP_ID}" ]]; then
    TARGET_GROUP_ID="${GROUP_ID}"
  elif [[ -n "${GROUP_NAME}" ]]; then
    TARGET_GROUP_ID="$(printf '%s' "${groups_json}" | jq -r --arg group_name "${GROUP_NAME}" '.groups[] | select(.group.name == $group_name) | .group.id' | head -n 1)"
  else
    GROUP_NAME="replicate-test-group-$(date +%Y%m%d-%H%M%S)"
  fi

  if [[ -n "${TARGET_GROUP_ID:-}" ]]; then
    TARGET_GROUP_NAME="$(printf '%s' "${groups_json}" | jq -r --arg group_id "${TARGET_GROUP_ID}" '.groups[] | select(.group.id == $group_id) | .group.name // empty' | head -n 1)"
    TARGET_GROUP_NETWORK_NAME="$(printf '%s' "${groups_json}" | jq -r --arg group_id "${TARGET_GROUP_ID}" '.groups[] | select(.group.id == $group_id) | .group.networkName // empty' | head -n 1)"
    [[ -n "${TARGET_GROUP_NAME}" ]] || TARGET_GROUP_NAME="${GROUP_NAME:-${TARGET_GROUP_ID}}"
    if [[ -n "${GROUP_NETWORK_NAME}" && "${TARGET_GROUP_NETWORK_NAME}" != "${GROUP_NETWORK_NAME}" ]]; then
      local upsert_payload
      upsert_payload="$(jq -nc \
        --arg group_id "${TARGET_GROUP_ID}" \
        --arg name "${TARGET_GROUP_NAME}" \
        --arg network_name "${GROUP_NETWORK_NAME}" \
        '{group_id:$group_id,name:$name,network_name:$network_name,set_current:true}')"
      local upsert_response
      upsert_response="$(api_post '/v1/swarm/groups/upsert' "${upsert_payload}")"
      write_artifact "group-upsert-existing.json" "${upsert_response}"
      TARGET_GROUP_NETWORK_NAME="$(printf '%s' "${upsert_response}" | jq -r '.group.networkName // empty')"
    else
      set_current_group "${TARGET_GROUP_ID}"
    fi
  else
    [[ -n "${GROUP_NAME}" ]] || fail "group name is required when no --group-id is supplied"
    local create_payload
    create_payload="$(jq -nc \
      --arg name "${GROUP_NAME}" \
      --arg network_name "${GROUP_NETWORK_NAME}" \
      '{name:$name,network_name:$network_name,set_current:true}')"
    local create_response
    create_response="$(api_post '/v1/swarm/groups/upsert' "${create_payload}")"
    write_artifact "group-upsert-created.json" "${create_response}"
    TARGET_GROUP_ID="$(printf '%s' "${create_response}" | jq -r '.group.id // empty')"
    TARGET_GROUP_NAME="$(printf '%s' "${create_response}" | jq -r '.group.name // empty')"
    TARGET_GROUP_NETWORK_NAME="$(printf '%s' "${create_response}" | jq -r '.group.networkName // empty')"
  fi

  [[ -n "${TARGET_GROUP_ID:-}" ]] || fail "failed to resolve a target group id"
  TARGET_GROUP_NAME="$(trim "${TARGET_GROUP_NAME:-}")"
  TARGET_GROUP_NETWORK_NAME="$(trim "${TARGET_GROUP_NETWORK_NAME:-}")"
  if [[ -z "${TARGET_GROUP_NETWORK_NAME}" ]]; then
    TARGET_GROUP_NETWORK_NAME="$(trim "${GROUP_NETWORK_NAME:-}")"
  fi
}

wait_for_target_group_in_host_state() {
  local start_ts
  start_ts="$(date +%s)"
  while :; do
    local host_state_json groups_json
    host_state_json="$(api_get '/v1/swarm/state')"
    groups_json="$(fetch_groups_json)"
    write_artifact "host-state-before-replicate.json" "${host_state_json}"
    write_artifact "groups-before-replicate.json" "${groups_json}"

    local state_swarm_id state_group_present state_host_membership groups_group_present groups_host_swarm_id
    state_swarm_id="$(printf '%s' "${host_state_json}" | jq -r '.state.node.swarm_id // empty')"
    state_group_present="$(printf '%s' "${host_state_json}" | jq -r --arg group_id "${TARGET_GROUP_ID}" '[.state.groups[] | select(.group.id == $group_id)] | length')"
    state_host_membership="$(printf '%s' "${host_state_json}" | jq -r --arg group_id "${TARGET_GROUP_ID}" --arg swarm_id "${state_swarm_id}" '[.state.groups[] | select(.group.id == $group_id) | .members[] | select(.swarm_id == $swarm_id and .membership_role == "host")] | length')"
    groups_group_present="$(printf '%s' "${groups_json}" | jq -r --arg group_id "${TARGET_GROUP_ID}" '[.groups[] | select(.group.id == $group_id)] | length')"
    groups_host_swarm_id="$(printf '%s' "${groups_json}" | jq -r --arg group_id "${TARGET_GROUP_ID}" '.groups[] | select(.group.id == $group_id) | .group.host_swarm_id // empty' | head -n 1)"

    if [[ "${state_group_present}" == "1" && "${state_host_membership}" == "1" && "${groups_group_present}" == "1" && "${groups_host_swarm_id}" == "${state_swarm_id}" ]]; then
      return 0
    fi

    if (( "$(date +%s)" - start_ts >= 15 )); then
      fail "target group ${TARGET_GROUP_ID} is not consistent between /v1/swarm/state and /v1/swarm/groups"
    fi

    sleep 1
  done
}

seed_source_workspace() {
  local payload response
  payload="$(jq -nc \
    --arg path "${SOURCE_WORKSPACE_PATH}" \
    --arg name "${WORKSPACE_NAME}" \
    '{path:$path,name:$name,make_current:true}')"
  response="$(api_post '/v1/workspace/add' "${payload}")"
  write_artifact "workspace-add-response.json" "${response}"
}

redacted_replicate_payload() {
  jq -nc \
    --arg mode "local" \
    --arg swarm_name "${SWARM_NAME}" \
    --arg runtime "${RUNTIME}" \
    --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg replication_mode "${REPLICATION_MODE}" \
    --arg sync_mode "${SYNC_MODE}" \
    --argjson sync_modules "$(sync_modules_json)" \
    --argjson writable "${WORKSPACE_WRITABLE}" \
    --argjson sync_enabled "${SYNC_ENABLED}" '
      {
        mode: $mode,
        swarm_name: $swarm_name,
        sync: (
          {enabled: $sync_enabled}
          + (if $sync_enabled and $sync_mode != "" then {mode: $sync_mode} else {} end)
          + (if $sync_enabled and ($sync_modules | length) > 0 then {modules: $sync_modules} else {} end)
          + (if $sync_enabled then {vault_password: "<redacted>"} else {} end)
        ),
        workspaces: [
          (
            {source_workspace_path: $source_workspace_path, writable: $writable}
            + (if $replication_mode != "" then {replication_mode: $replication_mode} else {} end)
          )
        ]
      }
      + (if $runtime != "" then {runtime: $runtime} else {} end)
    '
}

replicate_payload() {
  jq -nc \
    --arg mode "local" \
    --arg swarm_name "${SWARM_NAME}" \
    --arg runtime "${RUNTIME}" \
    --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg replication_mode "${REPLICATION_MODE}" \
    --arg sync_mode "${SYNC_MODE}" \
    --arg vault_password "${SYNC_VAULT_PASSWORD}" \
    --argjson sync_modules "$(sync_modules_json)" \
    --argjson writable "${WORKSPACE_WRITABLE}" \
    --argjson sync_enabled "${SYNC_ENABLED}" '
      {
        mode: $mode,
        swarm_name: $swarm_name,
        sync: (
          {enabled: $sync_enabled}
          + (if $sync_enabled and $sync_mode != "" then {mode: $sync_mode} else {} end)
          + (if $sync_enabled and ($sync_modules | length) > 0 then {modules: $sync_modules} else {} end)
          + (if $sync_enabled and $vault_password != "" then {vault_password: $vault_password} else {} end)
        ),
        workspaces: [
          (
            {source_workspace_path: $source_workspace_path, writable: $writable}
            + (if $replication_mode != "" then {replication_mode: $replication_mode} else {} end)
          )
        ]
      }
      + (if $runtime != "" then {runtime: $runtime} else {} end)
    '
}

run_replicate() {
  local payload response
  payload="$(replicate_payload)"
  write_artifact "replicate-request.redacted.json" "$(redacted_replicate_payload)"
  response="$(api_post '/v1/swarm/replicate' "${payload}")"
  write_artifact "replicate-response.json" "${response}"
  REPLICATE_RESPONSE_JSON="${response}"
  DEPLOYMENT_ID="$(printf '%s' "${response}" | jq -r '.swarm.deployment_id // empty')"
  CHILD_SWARM_ID_FROM_RESPONSE="$(printf '%s' "${response}" | jq -r '.swarm.id // empty')"
  REPLICATE_GROUP_ID="$(printf '%s' "${response}" | jq -r '.swarm.group_id // empty')"
  TARGET_WORKSPACE_PATH="$(printf '%s' "${response}" | jq -r '.workspaces[0].link.target_workspace_path // empty')"
  [[ -n "${DEPLOYMENT_ID}" ]] || fail "replicate response was missing swarm.deployment_id"
  [[ -n "${CHILD_SWARM_ID_FROM_RESPONSE}" ]] || fail "replicate response was missing swarm.id"
}

wait_for_attach() {
  local start_ts
  start_ts="$(date +%s)"
  while :; do
    local deployments_json
    deployments_json="$(api_get '/v1/deploy/container')"
    write_artifact "deployments-poll.json" "${deployments_json}"
    local deployment_json
    deployment_json="$(printf '%s' "${deployments_json}" | jq -c --arg deployment_id "${DEPLOYMENT_ID}" '.deployments[] | select(.id == $deployment_id)' | head -n 1)"
    [[ -n "${deployment_json}" ]] || fail "deployment ${DEPLOYMENT_ID} disappeared while waiting for attach"
    local attach_status
    attach_status="$(printf '%s' "${deployment_json}" | jq -r '.attach_status // empty')"
    local last_error
    last_error="$(printf '%s' "${deployment_json}" | jq -r '.last_attach_error // empty')"
    log "Attach poll deployment=${DEPLOYMENT_ID} attach_status=${attach_status:-<empty>} error=${last_error:-<empty>}"
    case "${attach_status}" in
      attached)
        DEPLOYMENT_JSON="${deployment_json}"
        return 0
        ;;
      rejected|failed)
        fail "attach failed for ${DEPLOYMENT_ID}: ${last_error:-unknown error}"
        ;;
    esac
    if (( "$(date +%s)" - start_ts >= POLL_TIMEOUT_SECONDS )); then
      fail "timed out waiting for deployment ${DEPLOYMENT_ID} to attach after ${POLL_TIMEOUT_SECONDS}s"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

probe_base_url() {
  local base_url="${1:-}"
  local label="${2:-probe}"
  local ready_code token_code root_code root_kind root_content_type
  local headers_file body_file
  headers_file="$(mktemp)"
  body_file="$(mktemp)"
  ready_code="$(curl_http_code "${base_url%/}/readyz")"
  token_code="$(curl_http_code "${base_url%/}/v1/auth/attach/token")"
  if root_code="$(curl -sS --connect-timeout 2 --max-time 8 -D "${headers_file}" -o "${body_file}" -w '%{http_code}' "${base_url%/}/" 2>/dev/null)"; then
    :
  else
    root_code="000"
  fi
  root_content_type="$(sed -n 's/^[Cc]ontent-[Tt]ype:[[:space:]]*//p' "${headers_file}" | tr -d '\r' | tail -n 1)"
  if grep -qiE '<!doctype html|<html' "${body_file}"; then
    root_kind="html"
  elif grep -qi '^\s*{' "${body_file}"; then
    root_kind="json"
  else
    root_kind="other"
  fi
  jq -nc \
    --arg base_url "${base_url}" \
    --arg ready_code "${ready_code}" \
    --arg token_code "${token_code}" \
    --arg root_code "${root_code}" \
    --arg root_content_type "${root_content_type}" \
    --arg root_kind "${root_kind}" \
    '{base_url:$base_url,ready_code:$ready_code,token_code:$token_code,root_code:$root_code,root_content_type:$root_content_type,root_kind:$root_kind}' >"${ARTIFACT_DIR}/${label}.json"
  cat "${ARTIFACT_DIR}/${label}.json"
  rm -f -- "${headers_file}" "${body_file}"
}

capture_logs() {
  if [[ -f "${LOG_FILE}" ]]; then
    tail -n "${LOG_TAIL}" "${LOG_FILE}" >"${ARTIFACT_DIR}/host-log-tail.txt" || true
  fi
  if [[ -n "${CONTAINER_NAME:-}" ]]; then
    "${RUNTIME}" logs --tail "${LOG_TAIL}" "${CONTAINER_NAME}" >"${ARTIFACT_DIR}/child-container-log-tail.txt" 2>&1 || true
  fi
}

capture_session_snapshot() {
  local session_id="${1:-}"
  local artifact_prefix="${2:-session-snapshot}"
  local max_time="${3:-10}"
  [[ -n "${session_id}" ]] || return 0
  [[ -n "${ARTIFACT_DIR:-}" && -d "${ARTIFACT_DIR}" ]] || return 0

  api_request_capture GET "/v1/sessions/${session_id}" "" "${max_time}"
  safe_write_artifact "${artifact_prefix}-session.http.txt" "${JSON_REQUEST_STATUS}"
  safe_write_artifact "${artifact_prefix}-session.json" "${JSON_REQUEST_BODY}"

  api_request_capture GET "/v1/sessions/${session_id}/messages?limit=200" "" "${max_time}"
  safe_write_artifact "${artifact_prefix}-messages.http.txt" "${JSON_REQUEST_STATUS}"
  safe_write_artifact "${artifact_prefix}-messages.json" "${JSON_REQUEST_BODY}"

  api_request_capture GET "/v1/sessions/${session_id}/permissions?limit=50" "" "${max_time}"
  safe_write_artifact "${artifact_prefix}-permissions.http.txt" "${JSON_REQUEST_STATUS}"
  safe_write_artifact "${artifact_prefix}-permissions.json" "${JSON_REQUEST_BODY}"

  api_request_capture GET "/v1/sessions/${session_id}/usage?limit=10" "" "${max_time}"
  safe_write_artifact "${artifact_prefix}-usage.http.txt" "${JSON_REQUEST_STATUS}"
  safe_write_artifact "${artifact_prefix}-usage.json" "${JSON_REQUEST_BODY}"

  api_request_capture GET "/v1/sessions/${session_id}/preference" "" "${max_time}"
  safe_write_artifact "${artifact_prefix}-preference.http.txt" "${JSON_REQUEST_STATUS}"
  safe_write_artifact "${artifact_prefix}-preference.json" "${JSON_REQUEST_BODY}"
}

build_routed_session_payload() {
  local session_title="${1:-}"
  local agent_name="${2:-}"
  jq -nc \
    --arg title "${session_title}" \
    --arg workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg host_workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg runtime_workspace_path "${TARGET_WORKSPACE_PATH}" \
    --arg workspace_name "${WORKSPACE_NAME}" \
    --arg mode "auto" \
    --arg agent_name "${agent_name}" \
    --arg provider "${PROOF_PROVIDER}" \
    --arg model "${PROOF_MODEL}" \
    --arg thinking "${PROOF_THINKING}" \
    '{title:$title,workspace_path:$workspace_path,host_workspace_path:$host_workspace_path,runtime_workspace_path:$runtime_workspace_path,workspace_name:$workspace_name,mode:$mode,agent_name:$agent_name,preference:{provider:$provider,model:$model,thinking:$thinking}}'
}

build_routed_run_payload() {
  local prompt="${1:-}"
  local agent_name="${2:-}"
  local tool_name="${3:-}"
  local background="${4:-false}"
  jq -nc \
    --arg prompt "${prompt}" \
    --arg agent_name "${agent_name}" \
    --arg tool_name "${tool_name}" \
    --argjson background "${background}" \
    '{prompt:$prompt,agent_name:$agent_name,tool_scope:{allow_tools:[$tool_name]}} + (if $background then {type:"run.start",background:true} else {} end)'
}

maybe_capture_routed_run_diagnostic() {
  if [[ "${PROOF_DIAGNOSTIC_CAPTURED}" == "true" ]]; then
    return 0
  fi
  [[ "${VERIFY_SYNC_CRUD_FLOW}" == "true" ]] || return 0
  [[ -n "${PROOF_RUN_PROMPT:-}" ]] || return 0
  [[ -n "${SYNC_PROBE_AGENT_NAME:-}" ]] || return 0
  [[ -n "${SYNC_PROBE_TOOL_CONTRACT_NAME:-}" ]] || return 0
  [[ -n "${CHILD_SWARM_ID:-}" ]] || return 0
  [[ -n "${TARGET_WORKSPACE_PATH:-}" ]] || return 0

  PROOF_DIAGNOSTIC_CAPTURED="true"
  log "Capturing synchronous routed proof diagnostic"

  local diag_session_title diag_session_payload
  diag_session_title="sync-proof-diagnostic-$(date +%s)"
  diag_session_payload="$(build_routed_session_payload "${diag_session_title}" "${SYNC_PROBE_AGENT_NAME}")"
  api_request_capture POST "/v1/sessions?swarm_id=${CHILD_SWARM_ID}" "${diag_session_payload}" 30
  safe_write_artifact "proof-diagnostic-session-create.http.txt" "${JSON_REQUEST_STATUS}"
  safe_write_artifact "proof-diagnostic-session-create.json" "${JSON_REQUEST_BODY}"
  if [[ "${JSON_REQUEST_STATUS}" != 2* ]]; then
    return 0
  fi
  DIAGNOSTIC_PROOF_SESSION_ID="$(printf '%s' "${JSON_REQUEST_BODY}" | jq -r '.session.id // empty' 2>/dev/null || true)"
  [[ -n "${DIAGNOSTIC_PROOF_SESSION_ID}" ]] || return 0

  local diag_run_payload diag_timeout
  diag_run_payload="$(build_routed_run_payload "${PROOF_RUN_PROMPT}" "${SYNC_PROBE_AGENT_NAME}" "${SYNC_PROBE_TOOL_CONTRACT_NAME}" "false")"
  diag_timeout="$((PROOF_TIMEOUT_SECONDS + 15))"
  api_request_capture POST "/v1/sessions/${DIAGNOSTIC_PROOF_SESSION_ID}/run" "${diag_run_payload}" "${diag_timeout}"
  safe_write_artifact "proof-diagnostic-run.http.txt" "${JSON_REQUEST_STATUS}"
  safe_write_artifact "proof-diagnostic-run.json" "${JSON_REQUEST_BODY}"
  capture_session_snapshot "${DIAGNOSTIC_PROOF_SESSION_ID}" "proof-diagnostic-session" 15
}

capture_failure_context() {
  local reason="${1:-}"
  if [[ "${FAILURE_CONTEXT_CAPTURED}" == "true" ]]; then
    return 0
  fi
  FAILURE_CONTEXT_CAPTURED="true"
  [[ -n "${ARTIFACT_DIR:-}" && -d "${ARTIFACT_DIR}" ]] || return 0

  safe_write_artifact "failure-message.txt" "${reason}"
  safe_write_artifact "failure-step.txt" "${CURRENT_STEP:-}"
  if command -v jq >/dev/null 2>&1; then
    safe_write_artifact "failure-context.json" "$(jq -nc \
      --arg reason "${reason}" \
      --arg step "${CURRENT_STEP:-}" \
      --arg deployment_id "${DEPLOYMENT_ID:-}" \
      --arg proof_session_id "${PROOF_SESSION_ID:-}" \
      --arg diagnostic_proof_session_id "${DIAGNOSTIC_PROOF_SESSION_ID:-}" \
      --arg child_swarm_id "${CHILD_SWARM_ID:-}" \
      --arg container_name "${CONTAINER_NAME:-}" \
      '{reason:$reason,step:$step,deployment_id:$deployment_id,proof_session_id:$proof_session_id,diagnostic_proof_session_id:$diagnostic_proof_session_id,child_swarm_id:$child_swarm_id,container_name:$container_name}')"
  fi
  if [[ -n "${PROOF_SESSION_ID:-}" ]]; then
    capture_session_snapshot "${PROOF_SESSION_ID}" "failure-proof-session" 15 || true
  fi
  if [[ -n "${DIAGNOSTIC_PROOF_SESSION_ID:-}" ]]; then
    capture_session_snapshot "${DIAGNOSTIC_PROOF_SESSION_ID}" "failure-proof-diagnostic-session" 15 || true
  fi
  capture_logs || true
}

ensure_child_attach_token() {
  if [[ -n "${CHILD_ATTACH_TOKEN:-}" ]]; then
    return 0
  fi
  [[ -n "${CONTAINER_NAME:-}" ]] || fail "child container name is not set"
  local child_token_response
  child_token_response="$("${RUNTIME}" exec "${CONTAINER_NAME}" curl -fsS 'http://127.0.0.1:7781/v1/auth/attach/token')"
  CHILD_ATTACH_TOKEN="$(printf '%s' "${child_token_response}" | jq -r '.token // empty')"
  [[ -n "${CHILD_ATTACH_TOKEN}" ]] || fail "failed to fetch child attach token from inside container ${CONTAINER_NAME}"
}

exercise_sync_state() {
  [[ "${VERIFY_SYNC_STATE}" == "true" ]] || return 0
  [[ "${VERIFY_SYNC_CRUD_FLOW}" == "true" ]] && return 0
  [[ "${SYNC_ENABLED}" == "true" ]] || fail "--verify-sync-state requires --sync-enabled"
  sync_module_enabled "credentials" || fail "--verify-sync-state requires credentials in --sync-modules"
  sync_module_enabled "agents" || fail "--verify-sync-state requires agents in --sync-modules"
  sync_module_enabled "custom_tools" || fail "--verify-sync-state requires custom_tools in --sync-modules"

  ensure_child_attach_token

  local probe_suffix
  probe_suffix="$(printf '%s' "${DEPLOYMENT_ID}" | tr -cd '[:alnum:]' | tr '[:upper:]' '[:lower:]')"
  probe_suffix="${probe_suffix:0:18}"
  if [[ -z "${probe_suffix}" ]]; then
    probe_suffix="$(date +%s)"
  fi

  SYNC_PROBE_CREDENTIAL_PROVIDER="syncprobe"
  SYNC_PROBE_CREDENTIAL_ID="sync-probe-${probe_suffix}"
  SYNC_PROBE_TOOL_NAME="sync-probe-tool-${probe_suffix}"
  SYNC_PROBE_TOOL_COMMAND="git status --short"
  SYNC_PROBE_AGENT_NAME="sync-probe-agent-${probe_suffix}"
  SYNC_PROBE_AGENT_PROMPT="Sync probe agent ${probe_suffix}"

  log "Creating post-attach managed sync probe state on the host"

  local host_credential_payload host_credential_response
  host_credential_payload="$(jq -nc \
    --arg id "${SYNC_PROBE_CREDENTIAL_ID}" \
    --arg provider "${SYNC_PROBE_CREDENTIAL_PROVIDER}" \
    --arg api_key "sync-probe-api-key-${probe_suffix}" \
    '{id:$id,provider:$provider,type:"api",label:"sync-probe",api_key:$api_key,active:true}')"
  host_credential_response="$(api_post '/v1/auth/credentials' "${host_credential_payload}")"
  write_artifact "host-sync-probe-credential.json" "${host_credential_response}"

  local host_tool_payload host_tool_response
  host_tool_payload="$(jq -nc \
    --arg name "${SYNC_PROBE_TOOL_NAME}" \
    --arg command "${SYNC_PROBE_TOOL_COMMAND}" \
    '{name:$name,kind:"fixed_bash",description:"Local sync probe tool",command:$command}')"
  host_tool_response="$(api_put "/v2/custom-tools/${SYNC_PROBE_TOOL_NAME}" "${host_tool_payload}")"
  write_artifact "host-sync-probe-tool.json" "${host_tool_response}"

  local host_agent_payload host_agent_response
  host_agent_payload="$(jq -nc \
    --arg prompt "${SYNC_PROBE_AGENT_PROMPT}" \
    --arg tool_name "${SYNC_PROBE_TOOL_NAME}" \
    '{mode:"primary",description:"Local sync probe agent",prompt:$prompt,enabled:true,exit_plan_mode_enabled:true,assign_custom_tools:[$tool_name]}')"
  host_agent_response="$(api_put "/v2/agents/${SYNC_PROBE_AGENT_NAME}" "${host_agent_payload}")"
  write_artifact "host-sync-probe-agent.json" "${host_agent_response}"

  local active_primary_payload active_primary_response
  active_primary_payload="$(jq -nc --arg name "${SYNC_PROBE_AGENT_NAME}" '{name:$name}')"
  active_primary_response="$(api_put '/v2/agents/active/primary' "${active_primary_payload}")"
  write_artifact "host-sync-probe-active-primary.json" "${active_primary_response}"

  local start_ts
  start_ts="$(date +%s)"
  while :; do
    local child_credentials_json child_agents_json child_tools_json
    child_credentials_json="$(child_api_get "/v1/auth/credentials?provider=${SYNC_PROBE_CREDENTIAL_PROVIDER}&limit=50")"
    child_agents_json="$(child_api_get '/v2/agents?limit=200')"
    child_tools_json="$(child_api_get '/v2/custom-tools?limit=200')"

    local credential_seen tool_seen agent_seen active_primary
    credential_seen="$(printf '%s' "${child_credentials_json}" | jq -r --arg id "${SYNC_PROBE_CREDENTIAL_ID}" '[.records[]? | select(.id == $id and .active == true)] | length')"
    tool_seen="$(printf '%s' "${child_tools_json}" | jq -r --arg name "${SYNC_PROBE_TOOL_NAME}" --arg command "${SYNC_PROBE_TOOL_COMMAND}" '[.custom_tools[]? | select(.name == $name and .command == $command)] | length')"
    agent_seen="$(printf '%s' "${child_agents_json}" | jq -r --arg name "${SYNC_PROBE_AGENT_NAME}" --arg prompt "${SYNC_PROBE_AGENT_PROMPT}" '[.state.profiles[]? | select(.name == $name and .mode == "primary" and .prompt == $prompt)] | length')"
    active_primary="$(printf '%s' "${child_agents_json}" | jq -r '.state.active_primary // empty')"

    if [[ "${credential_seen}" == "1" && "${tool_seen}" == "1" && "${agent_seen}" == "1" && "${active_primary}" == "${SYNC_PROBE_AGENT_NAME}" ]]; then
      write_artifact "child-sync-probe-credentials.json" "${child_credentials_json}"
      write_artifact "child-sync-probe-agents.json" "${child_agents_json}"
      write_artifact "child-sync-probe-tools.json" "${child_tools_json}"
      return 0
    fi

    if (( "$(date +%s)" - start_ts >= SYNC_VERIFY_TIMEOUT_SECONDS )); then
      write_artifact "child-sync-probe-credentials.timeout.json" "${child_credentials_json}"
      write_artifact "child-sync-probe-agents.timeout.json" "${child_agents_json}"
      write_artifact "child-sync-probe-tools.timeout.json" "${child_tools_json}"
      fail "timed out waiting for child managed sync state (credential=${credential_seen} tool=${tool_seen} agent=${agent_seen} active_primary=${active_primary:-<empty>})"
    fi

    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

exercise_sync_crud_flow() {
  [[ "${VERIFY_SYNC_CRUD_FLOW}" == "true" ]] || return 0
  [[ "${SYNC_ENABLED}" == "true" ]] || fail "--verify-sync-crud-flow requires --sync-enabled"
  sync_module_enabled "credentials" || fail "--verify-sync-crud-flow requires credentials in --sync-modules"
  sync_module_enabled "agents" || fail "--verify-sync-crud-flow requires agents in --sync-modules"
  sync_module_enabled "custom_tools" || fail "--verify-sync-crud-flow requires custom_tools in --sync-modules"
  [[ -n "${TARGET_WORKSPACE_PATH:-}" ]] || fail "--verify-sync-crud-flow requires a replicated target workspace path"

  ensure_child_attach_token
  prepare_proof_provider_key_file

  local probe_suffix
  probe_suffix="$(printf '%s' "${DEPLOYMENT_ID}" | tr -cd '[:alnum:]' | tr '[:upper:]' '[:lower:]')"
  probe_suffix="${probe_suffix:0:18}"
  if [[ -z "${probe_suffix}" ]]; then
    probe_suffix="$(date +%s)"
  fi

  PROOF_PRIMARY_CREDENTIAL_ID="fw-proof-primary-${probe_suffix}"
  PROOF_SECONDARY_CREDENTIAL_ID="fw-proof-secondary-${probe_suffix}"
  PROOF_SUCCESS_TOKEN="sync-agent-success-${probe_suffix}"
  SYNC_PROBE_TOOL_NAME="sync-probe-tool-${probe_suffix}"
  SYNC_PROBE_TOOL_CONTRACT_NAME="${SYNC_PROBE_TOOL_NAME}"
  SYNC_PROBE_TOOL_COMMAND="printf 'sync-agent-create-%s\n' '${probe_suffix}'"
  SYNC_PROBE_TOOL_COMMAND_FINAL="printf '%s\n' '${PROOF_SUCCESS_TOKEN}'"
  SYNC_PROBE_AGENT_NAME="sync-probe-agent-${probe_suffix}"
  SYNC_PROBE_AGENT_PROMPT="You are sync probe agent ${probe_suffix}. When asked to prove your identity, call your assigned custom tool exactly once and reply with only its output."
  SYNC_PROBE_AGENT_PROMPT_FINAL="You are sync probe agent ${probe_suffix}. If the user asks whether you are the right agent, prove it by calling your assigned custom tool exactly once and reply with only the tool output."

  log "Running post-attach managed sync CRUD plus routed execution proof"

  set_step "proof:create-primary-credential"
  local credential_primary_payload credential_primary_response
  credential_primary_payload="$(jq -nc \
    --arg id "${PROOF_PRIMARY_CREDENTIAL_ID}" \
    --arg provider "${PROOF_PROVIDER}" \
    --arg label "fw-sync-proof-primary" \
    --argjson active true \
    --rawfile api_key "${PROOF_PROVIDER_KEY_FILE}" \
    '{id:$id,provider:$provider,type:"api",label:$label,api_key:($api_key | sub("\r?\n$";"")),active:$active}')"
  credential_primary_response="$(api_post '/v1/auth/credentials' "${credential_primary_payload}")"
  write_artifact "host-proof-credential-primary.json" "${credential_primary_response}"

  local host_tool_payload host_tool_response
  host_tool_payload="$(jq -nc \
    --arg name "${SYNC_PROBE_TOOL_NAME}" \
    --arg command "${SYNC_PROBE_TOOL_COMMAND}" \
    '{name:$name,kind:"fixed_bash",description:"Local sync CRUD proof tool",command:$command}')"
  host_tool_response="$(api_put "/v2/custom-tools/${SYNC_PROBE_TOOL_NAME}" "${host_tool_payload}")"
  write_artifact "host-proof-tool-create.json" "${host_tool_response}"

  local host_agent_payload host_agent_response
  host_agent_payload="$(jq -nc \
    --arg prompt "${SYNC_PROBE_AGENT_PROMPT}" \
    --arg tool_contract_name "${SYNC_PROBE_TOOL_CONTRACT_NAME}" \
    --arg tool_name "${SYNC_PROBE_TOOL_NAME}" \
    '{mode:"primary",description:"Local sync CRUD proof agent",prompt:$prompt,enabled:true,exit_plan_mode_enabled:true,tool_contract:{inherit_policy:false,tools:({read:{enabled:false},search:{enabled:false},list:{enabled:false},websearch:{enabled:false},webfetch:{enabled:false},webdownload:{enabled:false},write:{enabled:false},edit:{enabled:false},bash:{enabled:false},task:{enabled:false},ask_user:{enabled:false},exit_plan_mode:{enabled:false},plan_manage:{enabled:false},skill_use:{enabled:false},manage_agent:{enabled:false},manage_skill:{enabled:false},manage_theme:{enabled:false},manage_worktree:{enabled:false},manage_todos:{enabled:false},git_status:{enabled:false},git_diff:{enabled:false},git_add:{enabled:false},git_commit:{enabled:false}} + {($tool_contract_name):{enabled:true}})},assign_custom_tools:[$tool_name]}')"
  host_agent_response="$(api_put "/v2/agents/${SYNC_PROBE_AGENT_NAME}" "${host_agent_payload}")"
  write_artifact "host-proof-agent-create.json" "${host_agent_response}"

  set_step "proof:activate-primary-agent"
  local active_primary_payload active_primary_response
  active_primary_payload="$(jq -nc --arg name "${SYNC_PROBE_AGENT_NAME}" '{name:$name}')"
  active_primary_response="$(api_put '/v2/agents/active/primary' "${active_primary_payload}")"
  write_artifact "host-proof-agent-activate.json" "${active_primary_response}"

  set_step "proof:wait-child-create-state"
  wait_for_child_credentials_exact "${PROOF_PROVIDER}" "$(jq -nc --arg id "${PROOF_PRIMARY_CREDENTIAL_ID}" '[$id]')" "${PROOF_PRIMARY_CREDENTIAL_ID}" "child-proof-credentials-create"
  wait_for_child_probe_state "true" "true" "${SYNC_PROBE_AGENT_PROMPT}" "${SYNC_PROBE_TOOL_COMMAND}" "${SYNC_PROBE_AGENT_NAME}" "child-proof-state-create"

  set_step "proof:create-secondary-credential"
  local credential_secondary_payload credential_secondary_response
  credential_secondary_payload="$(jq -nc \
    --arg id "${PROOF_SECONDARY_CREDENTIAL_ID}" \
    --arg provider "${PROOF_PROVIDER}" \
    --arg label "fw-sync-proof-secondary" \
    --argjson active false \
    --rawfile api_key "${PROOF_PROVIDER_KEY_FILE}" \
    '{id:$id,provider:$provider,type:"api",label:$label,api_key:($api_key | sub("\r?\n$";"")),active:$active}')"
  credential_secondary_response="$(api_post '/v1/auth/credentials' "${credential_secondary_payload}")"
  write_artifact "host-proof-credential-secondary.json" "${credential_secondary_response}"

  set_step "proof:wait-child-two-credentials"
  wait_for_child_credentials_exact "${PROOF_PROVIDER}" "$(jq -nc --arg a "${PROOF_PRIMARY_CREDENTIAL_ID}" --arg b "${PROOF_SECONDARY_CREDENTIAL_ID}" '[$a,$b]')" "${PROOF_PRIMARY_CREDENTIAL_ID}" "child-proof-credentials-two"

  set_step "proof:activate-secondary-credential"
  local activate_secondary_payload activate_secondary_response
  activate_secondary_payload="$(jq -nc --arg provider "${PROOF_PROVIDER}" --arg id "${PROOF_SECONDARY_CREDENTIAL_ID}" '{provider:$provider,id:$id}')"
  activate_secondary_response="$(api_post '/v1/auth/credentials/active' "${activate_secondary_payload}")"
  write_artifact "host-proof-credential-secondary-activate.json" "${activate_secondary_response}"

  set_step "proof:wait-child-active-secondary"
  wait_for_child_credentials_exact "${PROOF_PROVIDER}" "$(jq -nc --arg a "${PROOF_PRIMARY_CREDENTIAL_ID}" --arg b "${PROOF_SECONDARY_CREDENTIAL_ID}" '[$a,$b]')" "${PROOF_SECONDARY_CREDENTIAL_ID}" "child-proof-credentials-active-secondary"

  set_step "proof:delete-primary-credential"
  local delete_primary_payload delete_primary_response
  delete_primary_payload="$(jq -nc --arg provider "${PROOF_PROVIDER}" --arg id "${PROOF_PRIMARY_CREDENTIAL_ID}" '{provider:$provider,id:$id}')"
  delete_primary_response="$(api_post '/v1/auth/credentials/delete' "${delete_primary_payload}")"
  write_artifact "host-proof-credential-primary-delete.json" "${delete_primary_response}"

  set_step "proof:wait-child-delete-primary"
  wait_for_child_credentials_exact "${PROOF_PROVIDER}" "$(jq -nc --arg id "${PROOF_SECONDARY_CREDENTIAL_ID}" '[$id]')" "${PROOF_SECONDARY_CREDENTIAL_ID}" "child-proof-credentials-delete-primary"

  set_step "proof:update-tool"
  local host_tool_update_payload host_tool_update_response
  host_tool_update_payload="$(jq -nc \
    --arg name "${SYNC_PROBE_TOOL_NAME}" \
    --arg command "${SYNC_PROBE_TOOL_COMMAND_FINAL}" \
    '{name:$name,kind:"fixed_bash",description:"Local sync CRUD proof tool",command:$command}')"
  host_tool_update_response="$(api_put "/v2/custom-tools/${SYNC_PROBE_TOOL_NAME}" "${host_tool_update_payload}")"
  write_artifact "host-proof-tool-update.json" "${host_tool_update_response}"

  local host_agent_update_payload host_agent_update_response
  host_agent_update_payload="$(jq -nc \
    --arg prompt "${SYNC_PROBE_AGENT_PROMPT_FINAL}" \
    --arg tool_contract_name "${SYNC_PROBE_TOOL_CONTRACT_NAME}" \
    --arg tool_name "${SYNC_PROBE_TOOL_NAME}" \
    '{mode:"primary",description:"Local sync CRUD proof agent",prompt:$prompt,enabled:true,exit_plan_mode_enabled:true,tool_contract:{inherit_policy:false,tools:({read:{enabled:false},search:{enabled:false},list:{enabled:false},websearch:{enabled:false},webfetch:{enabled:false},webdownload:{enabled:false},write:{enabled:false},edit:{enabled:false},bash:{enabled:false},task:{enabled:false},ask_user:{enabled:false},exit_plan_mode:{enabled:false},plan_manage:{enabled:false},skill_use:{enabled:false},manage_agent:{enabled:false},manage_skill:{enabled:false},manage_theme:{enabled:false},manage_worktree:{enabled:false},manage_todos:{enabled:false},git_status:{enabled:false},git_diff:{enabled:false},git_add:{enabled:false},git_commit:{enabled:false}} + {($tool_contract_name):{enabled:true}})},assign_custom_tools:[$tool_name]}')"
  host_agent_update_response="$(api_put "/v2/agents/${SYNC_PROBE_AGENT_NAME}" "${host_agent_update_payload}")"
  write_artifact "host-proof-agent-update.json" "${host_agent_update_response}"

  set_step "proof:wait-child-update-state"
  wait_for_child_probe_state "true" "true" "${SYNC_PROBE_AGENT_PROMPT_FINAL}" "${SYNC_PROBE_TOOL_COMMAND_FINAL}" "${SYNC_PROBE_AGENT_NAME}" "child-proof-state-update"

  set_step "proof:create-routed-session"
  local routed_session_create_response
  routed_session_create_response="$(create_routed_host_session "sync-proof-${probe_suffix}" "${SYNC_PROBE_AGENT_NAME}")"
  write_artifact "proof-routed-session-create.json" "${routed_session_create_response}"
  PROOF_SESSION_ID="$(printf '%s' "${routed_session_create_response}" | jq -r '.session.id // empty')"
  [[ -n "${PROOF_SESSION_ID}" ]] || fail "routed proof session create returned no session id"

  local routed_run_prompt routed_run_start_response
  routed_run_prompt="Call your assigned custom tool now to prove you are ${SYNC_PROBE_AGENT_NAME}. Reply with only the tool output."
  PROOF_RUN_PROMPT="${routed_run_prompt}"
  set_step "proof:start-background-run"
  routed_run_start_response="$(start_background_routed_run "${PROOF_SESSION_ID}" "${routed_run_prompt}" "${SYNC_PROBE_AGENT_NAME}" "${SYNC_PROBE_TOOL_CONTRACT_NAME}")"
  write_artifact "proof-routed-run-start.json" "${routed_run_start_response}"

  set_step "proof:approve-routed-custom-tool"
  local routed_permission_json routed_permission_id routed_permission_resolve
  routed_permission_json="$(wait_for_pending_permission "${PROOF_SESSION_ID}" "${SYNC_PROBE_TOOL_NAME}")"
  write_artifact "proof-routed-run-success-permission-pending.json" "${routed_permission_json}"
  routed_permission_id="$(printf '%s' "${routed_permission_json}" | jq -r '.id // empty')"
  [[ -n "${routed_permission_id}" ]] || fail "pending routed custom-tool permission returned no id"
  routed_permission_resolve="$(resolve_session_permission "${PROOF_SESSION_ID}" "${routed_permission_id}" "approve" "ok")"
  write_artifact "proof-routed-run-success-permission-resolve.json" "${routed_permission_resolve}"

  set_step "proof:wait-routed-success"
  wait_for_assistant_message_content "${PROOF_SESSION_ID}" "${PROOF_SUCCESS_TOKEN}" "proof-routed-run-success"

  set_step "proof:cleanup-host-state"
  local delete_agent_response delete_tool_response delete_secondary_payload delete_secondary_response
  delete_agent_response="$(api_delete "/v2/agents/${SYNC_PROBE_AGENT_NAME}")"
  write_artifact "host-proof-agent-delete.json" "${delete_agent_response}"
  delete_tool_response="$(api_delete "/v2/custom-tools/${SYNC_PROBE_TOOL_NAME}")"
  write_artifact "host-proof-tool-delete.json" "${delete_tool_response}"
  delete_secondary_payload="$(jq -nc --arg provider "${PROOF_PROVIDER}" --arg id "${PROOF_SECONDARY_CREDENTIAL_ID}" '{provider:$provider,id:$id}')"
  delete_secondary_response="$(api_post '/v1/auth/credentials/delete' "${delete_secondary_payload}")"
  write_artifact "host-proof-credential-secondary-delete.json" "${delete_secondary_response}"

  set_step "proof:wait-child-delete-state"
  wait_for_child_credentials_exact "${PROOF_PROVIDER}" '[]' "" "child-proof-credentials-delete-all"
  wait_for_child_probe_state "false" "false" "${SYNC_PROBE_AGENT_PROMPT_FINAL}" "${SYNC_PROBE_TOOL_COMMAND_FINAL}" "" "child-proof-state-delete"
  set_step "proof:complete"
}

verify_workspace_link() {
  local workspaces_json
  workspaces_json="$(api_get '/v1/workspace/list?limit=1000')"
  write_artifact "workspace-list-final.json" "${workspaces_json}"

  WORKSPACE_ENTRY_JSON="$(printf '%s' "${workspaces_json}" | jq -c --arg path "${SOURCE_WORKSPACE_PATH}" '.workspaces[] | select(.path == $path)' | head -n 1)"
  [[ -n "${WORKSPACE_ENTRY_JSON}" ]] || fail "workspace list is missing source workspace ${SOURCE_WORKSPACE_PATH}"

  WORKSPACE_LINK_JSON="$(printf '%s' "${WORKSPACE_ENTRY_JSON}" | jq -c --arg child_swarm_id "${CHILD_SWARM_ID}" '.replication_links[]? | select(.target_swarm_id == $child_swarm_id)' | head -n 1)"
  [[ -n "${WORKSPACE_LINK_JSON}" ]] || fail "workspace ${SOURCE_WORKSPACE_PATH} is missing a replication link for child swarm ${CHILD_SWARM_ID}"

  local link_target_kind link_target_workspace_path link_writable link_replication_mode link_sync_enabled link_sync_mode link_sync_modules
  link_target_kind="$(printf '%s' "${WORKSPACE_LINK_JSON}" | jq -r '.target_kind // empty')"
  link_target_workspace_path="$(printf '%s' "${WORKSPACE_LINK_JSON}" | jq -r '.target_workspace_path // empty')"
  link_writable="$(printf '%s' "${WORKSPACE_LINK_JSON}" | jq -r '.writable')"
  link_replication_mode="$(printf '%s' "${WORKSPACE_LINK_JSON}" | jq -r '.replication_mode // empty')"
  link_sync_enabled="$(printf '%s' "${WORKSPACE_LINK_JSON}" | jq -r '.sync.enabled')"
  link_sync_mode="$(printf '%s' "${WORKSPACE_LINK_JSON}" | jq -r '.sync.mode // empty')"
  link_sync_modules="$(printf '%s' "${WORKSPACE_LINK_JSON}" | jq -cr '.sync.modules // []')"

  [[ "${link_target_kind}" == "local" ]] || fail "workspace replication link target_kind=${link_target_kind}, expected local"
  [[ "${link_writable}" == "${WORKSPACE_WRITABLE}" ]] || fail "workspace replication link writable=${link_writable}, expected ${WORKSPACE_WRITABLE}"
  if [[ -n "${TARGET_WORKSPACE_PATH}" ]]; then
    [[ "${link_target_workspace_path}" == "${TARGET_WORKSPACE_PATH}" ]] || fail "workspace replication link target_workspace_path=${link_target_workspace_path}, expected ${TARGET_WORKSPACE_PATH}"
  fi
  if [[ -n "${REPLICATION_MODE}" ]]; then
    [[ "${link_replication_mode}" == "${REPLICATION_MODE}" ]] || fail "workspace replication link replication_mode=${link_replication_mode}, expected ${REPLICATION_MODE}"
  fi
  if [[ "${SYNC_ENABLED}" == "true" ]]; then
    [[ "${link_sync_enabled}" == "true" ]] || fail "workspace replication link sync.enabled=${link_sync_enabled}, expected true"
    if [[ -n "${SYNC_MODE}" ]]; then
      [[ "${link_sync_mode}" == "${SYNC_MODE}" ]] || fail "workspace replication link sync.mode=${link_sync_mode}, expected ${SYNC_MODE}"
    fi
    [[ "${link_sync_modules}" == "$(sync_modules_json)" ]] || fail "workspace replication link sync.modules=${link_sync_modules}, expected $(sync_modules_json)"
  else
    [[ "${link_sync_enabled}" == "false" ]] || fail "workspace replication link sync.enabled=${link_sync_enabled}, expected false"
  fi
}

verify_final_state() {
  local host_state_json deployments_json containers_json groups_json
  host_state_json="$(api_get '/v1/swarm/state')"
  deployments_json="$(api_get '/v1/deploy/container')"
  containers_json="$(api_get '/v1/swarm/containers/local')"
  groups_json="$(fetch_groups_json)"

  write_artifact "host-state-final.json" "${host_state_json}"
  write_artifact "deployments-final.json" "${deployments_json}"
  write_artifact "local-containers-final.json" "${containers_json}"
  write_artifact "groups-final.json" "${groups_json}"

  if [[ -z "${DEPLOYMENT_JSON:-}" ]]; then
    DEPLOYMENT_JSON="$(printf '%s' "${deployments_json}" | jq -c --arg deployment_id "${DEPLOYMENT_ID}" '.deployments[] | select(.id == $deployment_id)' | head -n 1)"
  fi
  [[ -n "${DEPLOYMENT_JSON}" ]] || fail "final deployment state missing for ${DEPLOYMENT_ID}"

  local persisted_group_id attach_status backend_host_port desktop_host_port child_backend_url child_desktop_url
  persisted_group_id="$(printf '%s' "${DEPLOYMENT_JSON}" | jq -r '.group_id // empty')"
  attach_status="$(printf '%s' "${DEPLOYMENT_JSON}" | jq -r '.attach_status // empty')"
  backend_host_port="$(printf '%s' "${DEPLOYMENT_JSON}" | jq -r '.backend_host_port // 0')"
  desktop_host_port="$(printf '%s' "${DEPLOYMENT_JSON}" | jq -r '.desktop_host_port // 0')"
  child_backend_url="$(printf '%s' "${DEPLOYMENT_JSON}" | jq -r '.child_backend_url // empty')"
  child_desktop_url="$(printf '%s' "${DEPLOYMENT_JSON}" | jq -r '.child_desktop_url // empty')"
  CHILD_API_URL="${child_backend_url}"
  CHILD_SWARM_ID="$(printf '%s' "${DEPLOYMENT_JSON}" | jq -r '.child_swarm_id // empty')"
  CONTAINER_NAME="$(printf '%s' "${DEPLOYMENT_JSON}" | jq -r '.container_name // empty')"

  [[ "${attach_status}" == "attached" ]] || fail "deployment ${DEPLOYMENT_ID} did not finish attached (attach_status=${attach_status})"
  [[ "${persisted_group_id}" == "${TARGET_GROUP_ID}" ]] || fail "deployment ${DEPLOYMENT_ID} saved group_id=${persisted_group_id}, expected ${TARGET_GROUP_ID}"
  [[ "${REPLICATE_GROUP_ID}" == "${TARGET_GROUP_ID}" ]] || fail "replicate response group_id=${REPLICATE_GROUP_ID}, expected ${TARGET_GROUP_ID}"
  [[ "${CHILD_SWARM_ID}" == "${CHILD_SWARM_ID_FROM_RESPONSE}" ]] || fail "deployment child_swarm_id=${CHILD_SWARM_ID}, expected ${CHILD_SWARM_ID_FROM_RESPONSE}"
  [[ -n "${CONTAINER_NAME}" ]] || fail "deployment ${DEPLOYMENT_ID} is missing container_name"

  LOCAL_CONTAINER_JSON="$(printf '%s' "${containers_json}" | jq -c --arg container_name "${CONTAINER_NAME}" '.containers[] | select(.container_name == $container_name or .id == $container_name)' | head -n 1)"
  [[ -n "${LOCAL_CONTAINER_JSON}" ]] || fail "local container record missing for ${CONTAINER_NAME}"

  local local_host_port local_network_name local_status
  local_host_port="$(printf '%s' "${LOCAL_CONTAINER_JSON}" | jq -r '.host_port // 0')"
  local_network_name="$(printf '%s' "${LOCAL_CONTAINER_JSON}" | jq -r '.network_name // empty')"
  local_status="$(printf '%s' "${LOCAL_CONTAINER_JSON}" | jq -r '.status // empty')"
  [[ "${local_host_port}" == "${backend_host_port}" ]] || fail "local container host_port=${local_host_port} does not match deployment backend_host_port=${backend_host_port}"
  [[ "${local_status}" == "running" || "${local_status}" == "attached" ]] || fail "local container status for ${CONTAINER_NAME} is ${local_status}"
  if [[ -n "${TARGET_GROUP_NETWORK_NAME}" ]]; then
    [[ "${local_network_name}" == "${TARGET_GROUP_NETWORK_NAME}" ]] || fail "local container network_name=${local_network_name} does not match target group network ${TARGET_GROUP_NETWORK_NAME}"
  fi

  local child_member_count
  child_member_count="$(printf '%s' "${groups_json}" | jq -r --arg group_id "${TARGET_GROUP_ID}" --arg child_swarm_id "${CHILD_SWARM_ID}" '[.groups[] | select(.group.id == $group_id) | .members[] | select((.swarm_id // .swarmID // "") == $child_swarm_id)] | length')"
  [[ "${child_member_count}" == "1" ]] || fail "group ${TARGET_GROUP_ID} is missing child swarm ${CHILD_SWARM_ID}"

  BACKEND_PROBE_JSON="$(probe_base_url "${child_backend_url}" "child-backend-probe")"
  DESKTOP_PROBE_JSON="$(probe_base_url "${child_desktop_url}" "child-desktop-probe")"

  local backend_ready backend_root_kind backend_root_code desktop_ready desktop_root_kind
  backend_ready="$(printf '%s' "${BACKEND_PROBE_JSON}" | jq -r '.ready_code')"
  backend_root_kind="$(printf '%s' "${BACKEND_PROBE_JSON}" | jq -r '.root_kind')"
  backend_root_code="$(printf '%s' "${BACKEND_PROBE_JSON}" | jq -r '.root_code')"
  desktop_ready="$(printf '%s' "${DESKTOP_PROBE_JSON}" | jq -r '.ready_code')"
  desktop_root_kind="$(printf '%s' "${DESKTOP_PROBE_JSON}" | jq -r '.root_kind')"

  [[ "${backend_ready}" == "200" ]] || fail "backend probe ${child_backend_url} did not return readyz=200 (got ${backend_ready})"
  [[ "${desktop_ready}" == "200" ]] || fail "desktop probe ${child_desktop_url} did not return readyz=200 (got ${desktop_ready})"
  [[ "${backend_root_kind}" != "html" ]] || fail "backend probe ${child_backend_url} still served HTML at /"
  [[ "${backend_root_code}" == "401" || "${backend_root_code}" == "404" ]] || fail "backend probe ${child_backend_url} returned unexpected / status ${backend_root_code}; expected 401 or 404 on the API listener"
  [[ "${desktop_root_kind}" == "html" ]] || fail "desktop probe ${child_desktop_url} did not serve HTML at / (kind=${desktop_root_kind})"

  local child_state_json child_current_group
  ensure_child_attach_token
  child_state_json="$(child_api_get '/v1/swarm/state')"
  write_artifact "child-state-final.json" "${child_state_json}"
  child_current_group="$(printf '%s' "${child_state_json}" | jq -r '.state.current_group_id // empty')"
  [[ "${child_current_group}" == "${TARGET_GROUP_ID}" ]] || fail "child current_group_id=${child_current_group} does not match target group ${TARGET_GROUP_ID}"

  verify_workspace_link
  exercise_sync_state
  exercise_sync_crud_flow
  capture_logs

  SUMMARY_JSON="$(jq -nc \
    --arg runtime "${RUNTIME}" \
    --arg host_root "${HOST_ROOT}" \
    --arg host_swarm_name "${HOST_SWARM_NAME}" \
    --arg host_api_url "${HOST_ADMIN_API_URL}" \
    --arg host_desktop_url "${HOST_DESKTOP_URL}" \
    --arg host_log_file "${LOG_FILE}" \
    --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg workspace_name "${WORKSPACE_NAME}" \
    --arg replication_mode "${REPLICATION_MODE}" \
    --arg group_id "${TARGET_GROUP_ID}" \
    --arg group_name "${TARGET_GROUP_NAME}" \
    --arg group_network_name "${TARGET_GROUP_NETWORK_NAME}" \
    --arg deployment_id "${DEPLOYMENT_ID}" \
    --arg container_name "${CONTAINER_NAME}" \
    --arg child_swarm_id "${CHILD_SWARM_ID}" \
    --arg child_backend_url "${child_backend_url}" \
    --arg child_desktop_url "${child_desktop_url}" \
    --arg target_workspace_path "${TARGET_WORKSPACE_PATH}" \
    --argjson writable "${WORKSPACE_WRITABLE}" \
    --argjson sync_enabled "${SYNC_ENABLED}" \
    --arg sync_mode "${SYNC_MODE}" \
    --argjson sync_modules "$(sync_modules_json)" \
    --argjson verify_sync_state "${VERIFY_SYNC_STATE}" \
    --argjson verify_sync_crud_flow "${VERIFY_SYNC_CRUD_FLOW}" \
    --arg proof_provider "${PROOF_PROVIDER}" \
    --arg proof_model "${PROOF_MODEL}" \
    --arg proof_thinking "${PROOF_THINKING}" \
    --arg proof_primary_credential_id "${PROOF_PRIMARY_CREDENTIAL_ID:-}" \
    --arg proof_secondary_credential_id "${PROOF_SECONDARY_CREDENTIAL_ID:-}" \
    --arg proof_session_id "${PROOF_SESSION_ID:-}" \
    --arg proof_success_token "${PROOF_SUCCESS_TOKEN:-}" \
    --arg sync_probe_credential_provider "${SYNC_PROBE_CREDENTIAL_PROVIDER:-}" \
    --arg sync_probe_credential_id "${SYNC_PROBE_CREDENTIAL_ID:-}" \
    --arg sync_probe_tool_name "${SYNC_PROBE_TOOL_NAME:-}" \
    --arg sync_probe_agent_name "${SYNC_PROBE_AGENT_NAME:-}" \
    --argjson backend_probe "${BACKEND_PROBE_JSON}" \
    --argjson desktop_probe "${DESKTOP_PROBE_JSON}" \
    '{runtime:$runtime,host_root:$host_root,host_swarm_name:$host_swarm_name,host_api_url:$host_api_url,host_desktop_url:$host_desktop_url,host_log_file:$host_log_file,source_workspace_path:$source_workspace_path,workspace_name:$workspace_name,replication_mode:$replication_mode,writable:$writable,sync_enabled:$sync_enabled,sync_mode:$sync_mode,sync_modules:$sync_modules,verify_sync_state:$verify_sync_state,verify_sync_crud_flow:$verify_sync_crud_flow,group_id:$group_id,group_name:$group_name,group_network_name:$group_network_name,deployment_id:$deployment_id,container_name:$container_name,child_swarm_id:$child_swarm_id,child_backend_url:$child_backend_url,child_desktop_url:$child_desktop_url,target_workspace_path:$target_workspace_path,proof:{provider:$proof_provider,model:$proof_model,thinking:$proof_thinking,primary_credential_id:$proof_primary_credential_id,secondary_credential_id:$proof_secondary_credential_id,session_id:$proof_session_id,success_token:$proof_success_token},sync_probe:{credential_provider:$sync_probe_credential_provider,credential_id:$sync_probe_credential_id,tool_name:$sync_probe_tool_name,agent_name:$sync_probe_agent_name},backend_probe:$backend_probe,desktop_probe:$desktop_probe}')"
  write_artifact "summary.json" "${SUMMARY_JSON}"

  log ""
  log "Verification summary"
  printf '%s\n' "${SUMMARY_JSON}" | jq .
  log ""
  log "Host root: ${HOST_ROOT}"
  log "Host config: ${HOST_STARTUP_CONFIG}"
  log "Host log: ${LOG_FILE}"
  log "Host start script: ${HOST_ROOT}/start-host.sh"
  log "Host stop script: ${HOST_ROOT}/stop-host.sh"
  log "Artifacts: ${ARTIFACT_DIR}"
  if [[ -f "${ARTIFACT_DIR}/host-log-tail.txt" ]]; then
    log ""
    log "== host log tail (${LOG_FILE})"
    cat "${ARTIFACT_DIR}/host-log-tail.txt"
  fi
  if [[ -f "${ARTIFACT_DIR}/child-container-log-tail.txt" ]]; then
    log ""
    log "== child container log tail (${CONTAINER_NAME})"
    cat "${ARTIFACT_DIR}/child-container-log-tail.txt"
  fi
}

RUNTIME="${RUNTIME:-}"
SWARM_NAME=""
SOURCE_WORKSPACE_PATH="${ROOT_DIR}"
WORKSPACE_NAME=""
REPLICATION_MODE=""
WORKSPACE_WRITABLE="true"
SYNC_ENABLED="false"
SYNC_MODE=""
SYNC_MODULES_RAW=""
SYNC_MODULES=()
SYNC_VAULT_PASSWORD=""
SYNC_VAULT_PASSWORD_ENV=""
VERIFY_SYNC_STATE="false"
VERIFY_SYNC_CRUD_FLOW="false"
SYNC_VERIFY_TIMEOUT_SECONDS="45"
PROOF_PROVIDER="fireworks"
PROOF_MODEL="accounts/fireworks/models/minimax-m2p5"
PROOF_THINKING="medium"
PROOF_PROVIDER_KEY_ENV=""
PROOF_PROVIDER_KEY_FILE=""
PROOF_PROVIDER_KEY_TMP_FILE=""
PROOF_TIMEOUT_SECONDS="90"
GROUP_ID=""
GROUP_NAME=""
GROUP_NETWORK_NAME=""
HOST_SWARM_NAME="${HOST_SWARM_NAME:-Replicate Test Host}"
HOST_ADVERTISE_HOST_OVERRIDE="${HOST_ADVERTISE_HOST:-}"
HOST_BACKEND_PORT="7781"
HOST_DESKTOP_PORT="5555"
HOST_ROOT_OVERRIDE="${HOST_ROOT:-}"
BYPASS_PERMISSIONS="true"
REBUILD_HOST="true"
REBUILD_IMAGE="true"
POLL_TIMEOUT_SECONDS="120"
POLL_INTERVAL_SECONDS="2"
LOG_TAIL="200"
CHILD_API_URL=""
CHILD_ATTACH_TOKEN=""
JSON_REQUEST_STATUS=""
JSON_REQUEST_BODY=""
CURRENT_STEP=""
FAILURE_CONTEXT_CAPTURED="false"
PROOF_PRIMARY_CREDENTIAL_ID=""
PROOF_SECONDARY_CREDENTIAL_ID=""
PROOF_SESSION_ID=""
DIAGNOSTIC_PROOF_SESSION_ID=""
PROOF_DIAGNOSTIC_CAPTURED="false"
PROOF_RUN_PROMPT=""
PROOF_SUCCESS_TOKEN=""
SYNC_PROBE_CREDENTIAL_PROVIDER=""
SYNC_PROBE_CREDENTIAL_ID=""
SYNC_PROBE_TOOL_NAME=""
SYNC_PROBE_TOOL_CONTRACT_NAME=""
SYNC_PROBE_TOOL_COMMAND=""
SYNC_PROBE_TOOL_COMMAND_FINAL=""
SYNC_PROBE_AGENT_NAME=""
SYNC_PROBE_AGENT_PROMPT=""
SYNC_PROBE_AGENT_PROMPT_FINAL=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --runtime)
      RUNTIME="${2:-}"
      shift 2
      ;;
    --swarm-name)
      SWARM_NAME="${2:-}"
      shift 2
      ;;
    --workspace-path)
      SOURCE_WORKSPACE_PATH="${2:-}"
      shift 2
      ;;
    --workspace-name)
      WORKSPACE_NAME="${2:-}"
      shift 2
      ;;
    --replication-mode)
      REPLICATION_MODE="${2:-}"
      shift 2
      ;;
    --readonly)
      WORKSPACE_WRITABLE="false"
      shift
      ;;
    --sync-enabled)
      SYNC_ENABLED="true"
      shift
      ;;
    --sync-mode)
      SYNC_MODE="${2:-}"
      shift 2
      ;;
    --sync-modules)
      SYNC_MODULES_RAW="${2:-}"
      shift 2
      ;;
    --sync-vault-password)
      SYNC_VAULT_PASSWORD="${2:-}"
      shift 2
      ;;
    --sync-vault-password-env)
      SYNC_VAULT_PASSWORD_ENV="${2:-}"
      shift 2
      ;;
    --verify-sync-state)
      VERIFY_SYNC_STATE="true"
      shift
      ;;
    --verify-sync-crud-flow)
      VERIFY_SYNC_CRUD_FLOW="true"
      shift
      ;;
    --sync-verify-timeout)
      SYNC_VERIFY_TIMEOUT_SECONDS="${2:-}"
      shift 2
      ;;
    --proof-provider)
      PROOF_PROVIDER="${2:-}"
      shift 2
      ;;
    --proof-model)
      PROOF_MODEL="${2:-}"
      shift 2
      ;;
    --proof-thinking)
      PROOF_THINKING="${2:-}"
      shift 2
      ;;
    --proof-provider-key-env)
      PROOF_PROVIDER_KEY_ENV="${2:-}"
      shift 2
      ;;
    --proof-provider-key-file)
      PROOF_PROVIDER_KEY_FILE="${2:-}"
      shift 2
      ;;
    --proof-timeout)
      PROOF_TIMEOUT_SECONDS="${2:-}"
      shift 2
      ;;
    --group-id)
      GROUP_ID="${2:-}"
      shift 2
      ;;
    --group-name)
      GROUP_NAME="${2:-}"
      shift 2
      ;;
    --group-network-name)
      GROUP_NETWORK_NAME="${2:-}"
      shift 2
      ;;
    --host-swarm-name)
      HOST_SWARM_NAME="${2:-}"
      shift 2
      ;;
    --host-advertise-host)
      HOST_ADVERTISE_HOST_OVERRIDE="${2:-}"
      shift 2
      ;;
    --host-port)
      HOST_BACKEND_PORT="${2:-}"
      shift 2
      ;;
    --host-desktop-port)
      HOST_DESKTOP_PORT="${2:-}"
      shift 2
      ;;
    --host-root)
      HOST_ROOT_OVERRIDE="${2:-}"
      shift 2
      ;;
    --bypass-permissions)
      BYPASS_PERMISSIONS="${2:-}"
      shift 2
      ;;
    --skip-image-rebuild)
      REBUILD_IMAGE="false"
      shift
      ;;
    --skip-host-rebuild)
      REBUILD_HOST="false"
      shift
      ;;
    --poll-timeout)
      POLL_TIMEOUT_SECONDS="${2:-}"
      shift 2
      ;;
    --poll-interval)
      POLL_INTERVAL_SECONDS="${2:-}"
      shift 2
      ;;
    --log-tail)
      LOG_TAIL="${2:-}"
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
done

[[ "${POLL_TIMEOUT_SECONDS}" =~ ^[0-9]+$ ]] || fail "--poll-timeout must be a positive integer"
[[ "${POLL_INTERVAL_SECONDS}" =~ ^[0-9]+$ ]] || fail "--poll-interval must be a positive integer"
[[ "${SYNC_VERIFY_TIMEOUT_SECONDS}" =~ ^[0-9]+$ ]] || fail "--sync-verify-timeout must be a positive integer"
[[ "${PROOF_TIMEOUT_SECONDS}" =~ ^[0-9]+$ ]] || fail "--proof-timeout must be a positive integer"
[[ "${LOG_TAIL}" =~ ^[0-9]+$ ]] || fail "--log-tail must be a positive integer"
[[ "${HOST_BACKEND_PORT}" =~ ^[0-9]+$ ]] || fail "--host-port must be a positive integer"
[[ "${HOST_DESKTOP_PORT}" =~ ^[0-9]+$ ]] || fail "--host-desktop-port must be a positive integer"
if [[ "${BYPASS_PERMISSIONS}" != "true" && "${BYPASS_PERMISSIONS}" != "false" ]]; then
  fail "--bypass-permissions must be true or false"
fi

SOURCE_WORKSPACE_PATH="$(cd "${SOURCE_WORKSPACE_PATH}" && pwd)"
[[ -d "${SOURCE_WORKSPACE_PATH}" ]] || fail "--workspace-path must point to an existing directory"

if [[ -z "${WORKSPACE_NAME}" ]]; then
  WORKSPACE_NAME="$(basename "${SOURCE_WORKSPACE_PATH}")"
fi
WORKSPACE_NAME="$(trim "${WORKSPACE_NAME}")"
[[ -n "${WORKSPACE_NAME}" ]] || fail "workspace name resolved empty"

if [[ -z "${SWARM_NAME}" ]]; then
  SWARM_NAME="local-replicate-child-$(date +%Y%m%d-%H%M%S)"
fi

if [[ -n "${REPLICATION_MODE}" ]]; then
  case "${REPLICATION_MODE}" in
    bundle|copy) ;;
    *) fail "--replication-mode must be bundle or copy" ;;
  esac
fi

if [[ "${SYNC_ENABLED}" != "true" && "${SYNC_ENABLED}" != "false" ]]; then
  fail "sync enabled flag resolved to unexpected value: ${SYNC_ENABLED}"
fi
if [[ "${WORKSPACE_WRITABLE}" != "true" && "${WORKSPACE_WRITABLE}" != "false" ]]; then
  fail "workspace writable flag resolved to unexpected value: ${WORKSPACE_WRITABLE}"
fi

if [[ -n "${SYNC_VAULT_PASSWORD}" && -n "${SYNC_VAULT_PASSWORD_ENV}" ]]; then
  fail "only one of --sync-vault-password or --sync-vault-password-env may be provided"
fi
if [[ -n "${SYNC_VAULT_PASSWORD_ENV}" ]]; then
  SYNC_VAULT_PASSWORD="${!SYNC_VAULT_PASSWORD_ENV:-}"
  [[ -n "${SYNC_VAULT_PASSWORD}" ]] || fail "environment variable ${SYNC_VAULT_PASSWORD_ENV} is empty or unset"
fi
if [[ "${SYNC_ENABLED}" == "true" && -z "${SYNC_MODE}" ]]; then
  SYNC_MODE="managed"
fi
if [[ "${VERIFY_SYNC_STATE}" == "true" && "${SYNC_ENABLED}" != "true" ]]; then
  fail "--verify-sync-state requires --sync-enabled"
fi
if [[ "${VERIFY_SYNC_CRUD_FLOW}" == "true" && "${SYNC_ENABLED}" != "true" ]]; then
  fail "--verify-sync-crud-flow requires --sync-enabled"
fi
if [[ "${VERIFY_SYNC_STATE}" == "true" && -z "${SYNC_MODULES_RAW}" ]]; then
  SYNC_MODULES_RAW="credentials,agents,custom_tools"
fi
if [[ "${VERIFY_SYNC_CRUD_FLOW}" == "true" && -z "${SYNC_MODULES_RAW}" ]]; then
  SYNC_MODULES_RAW="credentials,agents,custom_tools"
fi
if [[ "${SYNC_ENABLED}" == "true" ]]; then
  if [[ -z "${SYNC_MODULES_RAW}" ]]; then
    SYNC_MODULES_RAW="credentials"
  fi
  normalize_sync_modules "${SYNC_MODULES_RAW}"
else
  if [[ -n "${SYNC_MODULES_RAW}" ]]; then
    fail "--sync-modules requires --sync-enabled"
  fi
  SYNC_MODULES=()
fi
if [[ -n "${PROOF_PROVIDER_KEY_ENV}" && -n "${PROOF_PROVIDER_KEY_FILE}" ]]; then
  fail "only one of --proof-provider-key-env or --proof-provider-key-file may be provided"
fi
if [[ "${VERIFY_SYNC_CRUD_FLOW}" == "true" ]]; then
  prepare_proof_provider_key_file
fi

require_command curl
require_command jq

prepare_isolated_host
maybe_rebuild_host
ensure_host_running
fetch_attach_token

HOST_STATE_INITIAL="$(api_get '/v1/swarm/state')"
write_artifact "host-state-initial.json" "${HOST_STATE_INITIAL}"
HOST_ROLE="$(printf '%s' "${HOST_STATE_INITIAL}" | jq -r '.state.node.role // empty')"
HOST_SWARM_ID="$(printf '%s' "${HOST_STATE_INITIAL}" | jq -r '.state.node.swarm_id // empty')"
[[ "${HOST_ROLE}" == "master" ]] || fail "isolated replicate host is not a master swarm (role=${HOST_ROLE})"
[[ -n "${HOST_SWARM_ID}" ]] || fail "isolated replicate host is missing a local swarm id"

resolve_runtime
require_command "${RUNTIME}"
maybe_rebuild_image
seed_source_workspace
ensure_target_group
wait_for_target_group_in_host_state

log "Running /v1/swarm/replicate end-to-end verification"
log "host swarm name: ${HOST_SWARM_NAME}"
log "host api: ${HOST_ADMIN_API_URL}"
log "host desktop: ${HOST_DESKTOP_URL}"
log "host root: ${HOST_ROOT}"
log "host log: ${LOG_FILE}"
log "bypass permissions: ${BYPASS_PERMISSIONS}"
log "runtime: ${RUNTIME}"
log "source workspace: ${SOURCE_WORKSPACE_PATH}"
log "replication mode: ${REPLICATION_MODE:-<default>}"
log "writable: ${WORKSPACE_WRITABLE}"
log "sync enabled: ${SYNC_ENABLED}"
log "sync mode: ${SYNC_MODE:-<default>}"
log "sync modules: $(IFS=, ; printf '%s' "${SYNC_MODULES[*]:-}")"
log "verify sync state: ${VERIFY_SYNC_STATE}"
log "verify sync CRUD flow: ${VERIFY_SYNC_CRUD_FLOW}"
log "proof provider: ${PROOF_PROVIDER}"
log "proof model: ${PROOF_MODEL}"
log "proof thinking: ${PROOF_THINKING}"
log "swarm name: ${SWARM_NAME}"
log "target group: ${TARGET_GROUP_ID} (${TARGET_GROUP_NAME:-<unnamed>})"
log "target group network: ${TARGET_GROUP_NETWORK_NAME:-<unset>}"
log "artifacts: ${ARTIFACT_DIR}"

set_step "replicate:run"
run_replicate
set_step "replicate:wait-attach"
wait_for_attach
set_step "replicate:verify-final-state"
verify_final_state
