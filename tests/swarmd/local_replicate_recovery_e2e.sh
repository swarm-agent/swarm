#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"

usage() {
  cat <<'EOF'
Usage: ./tests/swarmd/local_replicate_recovery_e2e.sh [options]

Run Stage S4 local recovery checks against the real local /v1/swarm/replicate path.

If --host-root is not supplied, this runner first boots a fresh host+child pair by
calling tests/swarmd/local_replicate_e2e.sh and then reuses that exact host root.

This runner creates one real routed session, appends a seed message, and then drives:
  - S4-01 host restart, child still running
  - S4-02 child restart, host still running
  - S4-03 host and child both down, host comes back first
  - S4-04 both down, child already running before host returns

Each scenario verifies:
  - the host still serves the routed session and messages locally
  - the session still points at the same child swarm
  - a follow-up routed message POST succeeds after recovery when the child is reachable

Options:
  --scenario <s4-01|s4-02|s4-03|s4-04|all>  Scenario to run. Default: all
  --host-root <path>                         Reuse an existing isolated host root
  --host-install-artifact-root <path>       Install the bootstrap host runtime from a release-style dist tree
  --runtime <docker|podman>                 Runtime for bootstrap. Default: docker
  --workspace-path <path>                   Workspace path for bootstrap. Default: repo root
  --group-name <name>                       Group name for bootstrap. Default: s4-recovery-<timestamp>
  --replication-mode <bundle|copy>          Replication mode for bootstrap
  --readonly                                Bootstrap the child workspace read-only
  --sync-enabled                            Enable managed sync during bootstrap
  --sync-vault-password <value>             Vault password to send in the bootstrap replicate request
  --sync-vault-password-env <name>          Read the child vault password from an environment variable
  --sync-vault-password-file <path>         Read the child vault password from a local file
  --host-vault-password <value>             Enable or unlock the host vault during bootstrap/restart
  --host-vault-password-env <name>          Read the host vault password from an environment variable
  --host-vault-password-file <path>         Read the host vault password from a local file
  --bypass-permissions <true|false>         Host bypass_permissions for bootstrap. Default: true
  --skip-host-rebuild                       Reuse the current host binaries during bootstrap
  --skip-image-rebuild                      Reuse the current canonical child image during bootstrap
  --attach-timeout <seconds>                Restart/reconnect timeout. Default: 90
  --poll-interval <seconds>                 Poll interval. Default: 2
  --log-tail <lines>                        Log tail lines to retain. Default: 200
  --help                                    Show this help text

Artifacts:
  Results are written under:
    <host-root>/recovery-artifacts/<timestamp>/
EOF
}

log() {
  printf '%s\n' "$*"
}

fail() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

require_command() {
  local name="${1:-}"
  command -v "${name}" >/dev/null 2>&1 || fail "required command not found: ${name}"
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

reserve_isolated_ports() {
  local backend_port="${BOOTSTRAP_HOST_BACKEND_PORT}"
  local desktop_port="${BOOTSTRAP_HOST_DESKTOP_PORT}"
  local attempts=0
  while (( attempts < 200 )); do
    if port_is_available "${backend_port}" \
      && port_is_available "$((backend_port + 1))" \
      && port_is_available "$((backend_port + 2))" \
      && port_is_available "${desktop_port}"; then
      BOOTSTRAP_HOST_BACKEND_PORT="${backend_port}"
      BOOTSTRAP_HOST_DESKTOP_PORT="${desktop_port}"
      return 0
    fi
    backend_port=$((backend_port + 3))
    desktop_port=$((desktop_port + 1))
    attempts=$((attempts + 1))
  done
  return 1
}

trim() {
  local value="${1-}"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "${value}"
}

resolve_secret_value() {
  local value_var="${1:-}"
  local env_var="${2:-}"
  local file_var="${3:-}"
  local display_name="${4:-secret}"
  local required="${5:-false}"
  local value env_name file_path resolved

  value="${!value_var:-}"
  env_name="${!env_var:-}"
  file_path="${!file_var:-}"

  if [[ -n "${file_path}" ]]; then
    [[ -f "${file_path}" ]] || fail "${display_name} file does not exist: ${file_path}"
    [[ -s "${file_path}" ]] || fail "${display_name} file is empty: ${file_path}"
    resolved="$(cat -- "${file_path}")"
    printf -v "${value_var}" '%s' "${resolved}"
    value="${resolved}"
  elif [[ -n "${env_name}" ]]; then
    resolved="${!env_name:-}"
    [[ -n "${resolved}" ]] || fail "environment variable ${env_name} is empty or unset"
    printf -v "${value_var}" '%s' "${resolved}"
    value="${resolved}"
  fi

  if [[ "${required}" == "true" && -z "${value}" ]]; then
    fail "${display_name} is required"
  fi
}

ensure_host_vault_ready() {
  local vault_password="${1:-}"
  [[ -n "${vault_password}" ]] || fail "host vault password is required"
  local status_json enabled unlocked payload response
  status_json="$(api_get '/v1/vault')"
  write_artifact "host-vault-status-before.json" "${status_json}"
  enabled="$(printf '%s' "${status_json}" | jq -r '.enabled // false')"
  unlocked="$(printf '%s' "${status_json}" | jq -r '.unlocked // false')"
  case "${enabled}:${unlocked}" in
    false:*)
      payload="$(jq -nc --arg password "${vault_password}" '{password:$password}')"
      response="$(api_post '/v1/vault/enable' "${payload}")"
      write_artifact "host-vault-enable.json" "${response}"
      ;;
    true:false)
      payload="$(jq -nc --arg password "${vault_password}" '{password:$password}')"
      response="$(api_post '/v1/vault/unlock' "${payload}")"
      write_artifact "host-vault-unlock.json" "${response}"
      ;;
  esac
  status_json="$(api_get '/v1/vault')"
  write_artifact "host-vault-status-after.json" "${status_json}"
  enabled="$(printf '%s' "${status_json}" | jq -r '.enabled // false')"
  unlocked="$(printf '%s' "${status_json}" | jq -r '.unlocked // false')"
  [[ "${enabled}" == "true" && "${unlocked}" == "true" ]] || fail "host vault is not enabled and unlocked"
}

write_artifact() {
  local name="${1:-}"
  local content="${2-}"
  printf '%s' "${content}" >"${ARTIFACT_DIR}/${name}"
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

host_ready() {
  [[ "$(curl_http_code "${HOST_ADMIN_API_URL%/}/readyz")" == "200" ]]
}

wait_for_host_ready() {
  local start_ts
  start_ts="$(date +%s)"
  while :; do
    if host_ready; then
      return 0
    fi
    if (( "$(date +%s)" - start_ts >= ATTACH_TIMEOUT_SECONDS )); then
      return 1
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

wait_for_host_down() {
  local start_ts
  start_ts="$(date +%s)"
  while :; do
    if ! host_ready; then
      return 0
    fi
    if (( "$(date +%s)" - start_ts >= ATTACH_TIMEOUT_SECONDS )); then
      return 1
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

api_request() {
  local method="${1:-GET}"
  local path="${2:-}"
  local body="${3:-}"
  local url="${HOST_ADMIN_API_URL%/}${path}"
  local body_file
  body_file="$(mktemp)"
  local http_code
  local args=(
    -sS
    --connect-timeout 3
    --max-time 30
    -o "${body_file}"
    -w '%{http_code}'
    -H 'Accept: application/json'
    -X "${method}"
  )
  if [[ -n "${ATTACH_TOKEN:-}" ]]; then
    args+=(-H "Authorization: Bearer ${ATTACH_TOKEN}")
  fi
  if [[ -n "${HOST_DESKTOP_SESSION_COOKIE_FILE:-}" ]]; then
    args+=(
      -c "${HOST_DESKTOP_SESSION_COOKIE_FILE}"
      -b "${HOST_DESKTOP_SESSION_COOKIE_FILE}"
      -H "Origin: ${HOST_ADMIN_API_URL%/}"
      -H "Referer: ${HOST_ADMIN_API_URL%/}/"
      -H 'Sec-Fetch-Site: same-origin'
    )
  fi
  if [[ -n "${body}" ]]; then
    args+=(-H 'Content-Type: application/json' --data "${body}")
  fi
  if http_code="$(curl "${args[@]}" "${url}")"; then
    :
  else
    http_code="000"
  fi
  local response_body
  response_body="$(cat -- "${body_file}")"
  rm -f -- "${body_file}"
  if [[ "${http_code}" != 2* ]]; then
    printf 'request failed method=%s url=%s status=%s body=%s\n' "${method}" "${url}" "${http_code}" "${response_body}" >&2
    return 1
  fi
  printf '%s' "${response_body}"
}

api_get() {
  api_request GET "$1"
}

api_post() {
  api_request POST "$1" "${2:-}"
}

child_api_request_capture() {
  local method="${1:-GET}"
  local path="${2:-}"
  local body="${3:-}"
  local max_time="${4:-30}"
  local response
  [[ -n "${CONTAINER_NAME:-}" ]] || fail "child container name is not set"

  if [[ -n "${body}" ]]; then
    response="$(printf '%s' "${body}" | "${RUNTIME}" exec -i \
      --env SWARM_CHILD_API_METHOD="${method}" \
      --env SWARM_CHILD_API_PATH="${path}" \
      --env SWARM_CHILD_API_TIMEOUT="${max_time}" \
      "${CONTAINER_NAME}" sh -lc '
        cookie_file="$(mktemp)"
        response_file="$(mktemp)"
        request_body_file="$(mktemp)"
        trap "rm -f -- \"${cookie_file}\" \"${response_file}\" \"${request_body_file}\"" EXIT
        cat >"${request_body_file}"
        curl -fsS -c "${cookie_file}" -b "${cookie_file}" \
          -H "Origin: http://127.0.0.1:5555" \
          -H "Referer: http://127.0.0.1:5555/" \
          -H "Sec-Fetch-Site: same-origin" \
          "http://127.0.0.1:5555/v1/auth/desktop/session" >/dev/null
        http_code="$(curl -sS --connect-timeout 3 --max-time "${SWARM_CHILD_API_TIMEOUT}" \
          -c "${cookie_file}" -b "${cookie_file}" \
          -H "Origin: http://127.0.0.1:7781" \
          -H "Referer: http://127.0.0.1:7781/" \
          -H "Sec-Fetch-Site: same-origin" \
          -H "Accept: application/json" \
          -H "Content-Type: application/json" \
          -o "${response_file}" -w "%{http_code}" \
          -X "${SWARM_CHILD_API_METHOD}" \
          --data-binary "@${request_body_file}" \
          "http://127.0.0.1:7781${SWARM_CHILD_API_PATH}")"
        cat "${response_file}"
        printf "\n__SWARM_HTTP_CODE__=%s\n" "${http_code}"
      ')"
  else
    response="$("${RUNTIME}" exec \
      --env SWARM_CHILD_API_METHOD="${method}" \
      --env SWARM_CHILD_API_PATH="${path}" \
      --env SWARM_CHILD_API_TIMEOUT="${max_time}" \
      "${CONTAINER_NAME}" sh -lc '
        cookie_file="$(mktemp)"
        response_file="$(mktemp)"
        trap "rm -f -- \"${cookie_file}\" \"${response_file}\"" EXIT
        curl -fsS -c "${cookie_file}" -b "${cookie_file}" \
          -H "Origin: http://127.0.0.1:5555" \
          -H "Referer: http://127.0.0.1:5555/" \
          -H "Sec-Fetch-Site: same-origin" \
          "http://127.0.0.1:5555/v1/auth/desktop/session" >/dev/null
        http_code="$(curl -sS --connect-timeout 3 --max-time "${SWARM_CHILD_API_TIMEOUT}" \
          -c "${cookie_file}" -b "${cookie_file}" \
          -H "Origin: http://127.0.0.1:7781" \
          -H "Referer: http://127.0.0.1:7781/" \
          -H "Sec-Fetch-Site: same-origin" \
          -H "Accept: application/json" \
          -o "${response_file}" -w "%{http_code}" \
          -X "${SWARM_CHILD_API_METHOD}" \
          "http://127.0.0.1:7781${SWARM_CHILD_API_PATH}")"
        cat "${response_file}"
        printf "\n__SWARM_HTTP_CODE__=%s\n" "${http_code}"
      ')"
  fi
  JSON_REQUEST_STATUS="$(printf '%s' "${response}" | sed -n 's/^__SWARM_HTTP_CODE__=//p' | tail -n 1)"
  JSON_REQUEST_BODY="$(printf '%s' "${response}" | sed '/^__SWARM_HTTP_CODE__=/d')"
}

child_api_request() {
  local method="${1:-GET}"
  local path="${2:-}"
  local body="${3:-}"
  local max_time="${4:-30}"
  child_api_request_capture "${method}" "${path}" "${body}" "${max_time}" || return 1
  if [[ "${JSON_REQUEST_STATUS}" != 2* ]]; then
    fail "${method} child:${path} failed with status ${JSON_REQUEST_STATUS}: ${JSON_REQUEST_BODY}"
  fi
  printf '%s' "${JSON_REQUEST_BODY}"
}

child_api_get() {
  child_api_request GET "$1" "" "${2:-30}"
}

child_api_post() {
  child_api_request POST "$1" "${2:-}" "${3:-30}"
}

wait_for_child_vault_unlocked() {
  local start_ts status_json enabled unlocked unlock_required
  start_ts="$(date +%s)"
  while :; do
    child_api_request_capture GET '/v1/vault' '' 30 || true
    if [[ "${JSON_REQUEST_STATUS}" == 2* ]]; then
      status_json="${JSON_REQUEST_BODY}"
      enabled="$(printf '%s' "${status_json}" | jq -r '.enabled // false')"
      unlocked="$(printf '%s' "${status_json}" | jq -r '.unlocked // false')"
      unlock_required="$(printf '%s' "${status_json}" | jq -r '.unlock_required // false')"
      if [[ "${enabled}" == "true" && "${unlocked}" == "true" && "${unlock_required}" == "false" ]]; then
        printf '%s' "${status_json}"
        return 0
      fi
    fi
    if (( "$(date +%s)" - start_ts >= ATTACH_TIMEOUT_SECONDS )); then
      return 1
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

verify_vaulted_child_unlocked() {
  local label="${1:-vaulted-child}"
  local status_json credentials_json
  status_json="$(wait_for_child_vault_unlocked)" || fail "timed out waiting for host-driven child vault unlock"
  write_artifact "${label}-vault-status-unlocked.json" "${status_json}"
  credentials_json="$(child_api_get '/v1/auth/credentials?provider=fireworks&limit=50' 30)"
  write_artifact "${label}-credentials-after-host-unlock.json" "${credentials_json}"
  printf '%s' "${credentials_json}" | jq -e '.records | arrays' >/dev/null || fail "child credentials response after unlock was malformed"
}

fetch_attach_token() {
  if [[ -z "${HOST_DESKTOP_SESSION_COOKIE_FILE:-}" ]]; then
    HOST_DESKTOP_SESSION_COOKIE_FILE="$(mktemp)"
  fi
  curl -fsS \
    -c "${HOST_DESKTOP_SESSION_COOKIE_FILE}" \
    -b "${HOST_DESKTOP_SESSION_COOKIE_FILE}" \
    -H "Origin: ${HOST_ADMIN_API_URL%/}" \
    -H "Referer: ${HOST_ADMIN_API_URL%/}/" \
    -H 'Sec-Fetch-Site: same-origin' \
    "${HOST_ADMIN_API_URL%/}/v1/auth/desktop/session" >/dev/null || return 1
  ATTACH_TOKEN=""
  return 0
}

ensure_host_running() {
  if host_ready; then
    fetch_attach_token
    if [[ -n "${HOST_VAULT_PASSWORD}" ]]; then
      ensure_host_vault_ready "${HOST_VAULT_PASSWORD}"
    fi
    return
  fi
  log "Starting isolated host at ${HOST_ADMIN_API_URL}"
  if [[ -x "${HOST_START_SCRIPT}" ]]; then
    "${HOST_START_SCRIPT}" >/dev/null
  else
    (
      cd "${ROOT_DIR}"
      XDG_CONFIG_HOME="${HOST_XDG_CONFIG_HOME}" \
      XDG_DATA_HOME="${HOST_XDG_DATA_HOME}" \
      XDG_STATE_HOME="${HOST_XDG_STATE_HOME}" \
      XDG_CACHE_HOME="${HOST_XDG_CACHE_HOME}" \
      SWARM_LANE=main \
      ./swarmd/scripts/dev-up.sh
    )
  fi
  wait_for_host_ready || return 1
  fetch_attach_token
  if [[ -n "${HOST_VAULT_PASSWORD}" ]]; then
    ensure_host_vault_ready "${HOST_VAULT_PASSWORD}"
  fi
}

stop_host() {
  log "Stopping isolated host at ${HOST_ADMIN_API_URL}"
  if [[ -x "${HOST_STOP_SCRIPT}" ]]; then
    "${HOST_STOP_SCRIPT}" >/dev/null 2>&1 || true
  else
    (
      cd "${ROOT_DIR}"
      XDG_CONFIG_HOME="${HOST_XDG_CONFIG_HOME}" \
      XDG_DATA_HOME="${HOST_XDG_DATA_HOME}" \
      XDG_STATE_HOME="${HOST_XDG_STATE_HOME}" \
      XDG_CACHE_HOME="${HOST_XDG_CACHE_HOME}" \
      SWARM_LANE=main \
      ./swarmd/scripts/dev-down.sh
    ) >/dev/null 2>&1 || true
  fi
  wait_for_host_down
}

runtime_container_running() {
  local running
  running="$("${RUNTIME}" inspect -f '{{if .State.Running}}true{{else}}false{{end}}' "${CONTAINER_NAME}" 2>/dev/null || true)"
  [[ "${running}" == "true" ]]
}

wait_for_runtime_state() {
  local expected="${1:-}"
  local start_ts
  start_ts="$(date +%s)"
  while :; do
    if [[ "${expected}" == "running" ]]; then
      if runtime_container_running; then
        return 0
      fi
    else
      if ! runtime_container_running; then
        return 0
      fi
    fi
    if (( "$(date +%s)" - start_ts >= ATTACH_TIMEOUT_SECONDS )); then
      return 1
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

direct_child_start() {
  "${RUNTIME}" start "${CONTAINER_NAME}" >/dev/null
}

direct_child_stop() {
  "${RUNTIME}" stop "${CONTAINER_NAME}" >/dev/null
}

current_deployment_json() {
  local deployments_json
  deployments_json="$(api_get '/v1/deploy/container')" || return 1
  LAST_DEPLOYMENTS_JSON="${deployments_json}"
  printf '%s' "${deployments_json}" | jq -c --arg deployment_id "${DEPLOYMENT_ID}" '.deployments[] | select(.id == $deployment_id)' | head -n 1
}

wait_for_deployment_attached() {
  local start_ts
  start_ts="$(date +%s)"
  while :; do
    local deployment_json attach_status child_backend_url child_swarm_id
    deployment_json="$(current_deployment_json)" || return 1
    [[ -n "${deployment_json}" ]] || return 1
    LAST_DEPLOYMENT_JSON="${deployment_json}"
    attach_status="$(printf '%s' "${deployment_json}" | jq -r '.attach_status // empty')"
    child_backend_url="$(printf '%s' "${deployment_json}" | jq -r '.child_backend_url // empty')"
    child_swarm_id="$(printf '%s' "${deployment_json}" | jq -r '.child_swarm_id // empty')"
    CHILD_BACKEND_URL="${child_backend_url}"
    if [[ "${attach_status}" == "attached" && "${child_swarm_id}" == "${CHILD_SWARM_ID}" && -n "${child_backend_url}" ]]; then
      if [[ "$(curl_http_code "${child_backend_url%/}/readyz")" == "200" ]]; then
        return 0
      fi
    fi
    if (( "$(date +%s)" - start_ts >= ATTACH_TIMEOUT_SECONDS )); then
      return 1
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

create_routed_session() {
  local payload response
  payload="$(jq -nc \
    --arg title "s4-recovery-${RUN_ID}" \
    --arg workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg provider "codex" \
    --arg model "gpt-5.4" \
    --arg thinking "medium" \
    '{title:$title,workspace_path:$workspace_path,mode:"auto",preference:{provider:$provider,model:$model,thinking:$thinking}}')"
  response="$(api_post "/v1/sessions?swarm_id=${CHILD_SWARM_ID}" "${payload}")" || return 1
  write_artifact "session-create-response.json" "${response}"
  SESSION_ID="$(printf '%s' "${response}" | jq -r '.session.id // empty')"
  [[ -n "${SESSION_ID}" ]]
}

append_message_and_verify() {
  local label="${1:-}"
  local content="${2:-}"
  local payload response messages_json
  payload="$(jq -nc --arg role "user" --arg content "${content}" '{role:$role,content:$content}')"
  response="$(api_post "/v1/sessions/${SESSION_ID}/messages" "${payload}")" || return 1
  write_artifact "${label}-message-post.json" "${response}"
  messages_json="$(api_get "/v1/sessions/${SESSION_ID}/messages?limit=200")" || return 1
  write_artifact "${label}-messages.json" "${messages_json}"
  local found
  found="$(printf '%s' "${messages_json}" | jq -r --arg content "${content}" '[.messages[] | select(.content == $content)] | length')"
  [[ "${found}" != "0" ]]
}

verify_session_state() {
  local label="${1:-}"
  local session_json messages_json permissions_json sessions_json child_meta found_seed
  session_json="$(api_get "/v1/sessions/${SESSION_ID}")" || return 1
  messages_json="$(api_get "/v1/sessions/${SESSION_ID}/messages?limit=200")" || return 1
  permissions_json="$(api_get "/v1/sessions/${SESSION_ID}/permissions?limit=50")" || return 1
  sessions_json="$(api_get "/v1/sessions?limit=200&swarm_id=${CHILD_SWARM_ID}")" || return 1
  write_artifact "${label}-session.json" "${session_json}"
  write_artifact "${label}-session-messages.json" "${messages_json}"
  write_artifact "${label}-session-permissions.json" "${permissions_json}"
  write_artifact "${label}-sessions-list.json" "${sessions_json}"
  child_meta="$(printf '%s' "${session_json}" | jq -r '.session.metadata.swarm_routed_child_swarm_id // empty')"
  [[ "${child_meta}" == "${CHILD_SWARM_ID}" ]] || return 1
  found_seed="$(printf '%s' "${messages_json}" | jq -r --arg content "${SEED_MESSAGE_CONTENT}" '[.messages[] | select(.content == $content)] | length')"
  [[ "${found_seed}" != "0" ]] || return 1
  local session_list_found
  session_list_found="$(printf '%s' "${sessions_json}" | jq -r --arg session_id "${SESSION_ID}" '[.sessions[] | select(.id == $session_id)] | length')"
  [[ "${session_list_found}" != "0" ]]
}

record_runtime_state() {
  local label="${1:-}"
  local inspect_json
  inspect_json="$("${RUNTIME}" inspect "${CONTAINER_NAME}" 2>/dev/null || true)"
  write_artifact "${label}-runtime-inspect.json" "${inspect_json}"
}

scenario_s401() {
  verify_session_state "s4-01-before" || return 1
  runtime_container_running || return 1
  stop_host || return 1
  runtime_container_running || return 1
  ensure_host_running || return 1
  wait_for_deployment_attached || return 1
  verify_session_state "s4-01-after" || return 1
  append_message_and_verify "s4-01-follow-up" "s4-01 host restart follow-up ${RUN_ID}" || return 1
  SCENARIO_NOTE="host restarted on the same root, child stayed up, host session reads survived, and a routed follow-up message still reached the same child"
}

scenario_s402() {
  verify_session_state "s4-02-before" || return 1
  api_post '/v1/deploy/container/action' "$(jq -nc --arg id "${DEPLOYMENT_ID}" --arg action "stop" '{id:$id,action:$action}')" >/dev/null || return 1
  wait_for_runtime_state "stopped" || return 1
  api_post '/v1/deploy/container/action' "$(jq -nc --arg id "${DEPLOYMENT_ID}" --arg action "start" '{id:$id,action:$action}')" >/dev/null || return 1
  wait_for_runtime_state "running" || return 1
  wait_for_deployment_attached || return 1
  if [[ -n "${SYNC_VAULT_PASSWORD}" ]]; then
    verify_vaulted_child_unlocked "s4-02"
  fi
  verify_session_state "s4-02-after" || return 1
  append_message_and_verify "s4-02-follow-up" "s4-02 child restart follow-up ${RUN_ID}" || return 1
  if [[ -n "${SYNC_VAULT_PASSWORD}" ]]; then
    SCENARIO_NOTE="vaulted child restarted under the running host, the host re-unlocked it automatically, synced credentials stayed readable, and the same routed session still accepted a follow-up child write"
  else
    SCENARIO_NOTE="child restarted under the running host, reattached, and the same routed session still accepted a follow-up child write"
  fi
}

scenario_s403() {
  verify_session_state "s4-03-before" || return 1
  api_post '/v1/deploy/container/action' "$(jq -nc --arg id "${DEPLOYMENT_ID}" --arg action "stop" '{id:$id,action:$action}')" >/dev/null || return 1
  wait_for_runtime_state "stopped" || return 1
  stop_host || return 1
  ensure_host_running || return 1
  verify_session_state "s4-03-host-only" || return 1
  if wait_for_deployment_attached; then
    if [[ -n "${SYNC_VAULT_PASSWORD}" ]]; then
      verify_vaulted_child_unlocked "s4-03"
    fi
    append_message_and_verify "s4-03-follow-up" "s4-03 host-first recovery follow-up ${RUN_ID}" || return 1
    SCENARIO_NOTE="with both sides down, bringing the host back first auto-recovered the local child and restored routed writes"
    return 0
  fi
  current_deployment_json >/dev/null || true
  write_artifact "s4-03-deployment-after-failed-recovery.json" "${LAST_DEPLOYMENT_JSON:-}"
  SCENARIO_NOTE="host preserved the routed session locally, but did not auto-recover the stopped local child within the reconnect timeout"
  return 1
}

scenario_s404() {
  if host_ready; then
    stop_host || return 1
  fi
  if runtime_container_running; then
    direct_child_stop || return 1
  fi
  wait_for_runtime_state "stopped" || return 1
  direct_child_start || return 1
  wait_for_runtime_state "running" || return 1
  record_runtime_state "s4-04-child-running-before-host"
  ensure_host_running || return 1
  if ! wait_for_deployment_attached; then
    write_artifact "s4-04-deployment-after-host-return.json" "${LAST_DEPLOYMENT_JSON:-}"
    SCENARIO_NOTE="child was started before the host returned, but the host did not reconnect to it within the reconnect timeout"
    return 1
  fi
  if [[ -n "${SYNC_VAULT_PASSWORD}" ]]; then
    verify_vaulted_child_unlocked "s4-04"
  fi
  verify_session_state "s4-04-after" || return 1
  append_message_and_verify "s4-04-follow-up" "s4-04 child-first recovery follow-up ${RUN_ID}" || return 1
  SCENARIO_NOTE="child was already running before host return, the host reattached, and routed writes resumed on the same session"
}

run_scenario() {
  local id="${1:-}"
  local fn="${2:-}"
  local status note started_at finished_at
  started_at="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
  SCENARIO_NOTE=""
  if "${fn}"; then
    status="PASS"
  else
    status="FAIL"
    OVERALL_FAILURE=1
  fi
  note="${SCENARIO_NOTE}"
  finished_at="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
  jq -nc \
    --arg id "${id}" \
    --arg status "${status}" \
    --arg started_at "${started_at}" \
    --arg finished_at "${finished_at}" \
    --arg session_id "${SESSION_ID}" \
    --arg deployment_id "${DEPLOYMENT_ID}" \
    --arg child_swarm_id "${CHILD_SWARM_ID}" \
    --arg container_name "${CONTAINER_NAME}" \
    --arg note "${note}" \
    --arg child_backend_url "${CHILD_BACKEND_URL}" \
    '{id:$id,status:$status,started_at:$started_at,finished_at:$finished_at,session_id:$session_id,deployment_id:$deployment_id,child_swarm_id:$child_swarm_id,container_name:$container_name,child_backend_url:$child_backend_url,note:$note}' \
    >"${ARTIFACT_DIR}/${id}.json"
  SCENARIO_FILES+=("${ARTIFACT_DIR}/${id}.json")
}

capture_logs() {
  if [[ -f "${HOST_LOG_FILE}" ]]; then
    tail -n "${LOG_TAIL}" "${HOST_LOG_FILE}" >"${ARTIFACT_DIR}/host-log-tail.txt" || true
  fi
  "${RUNTIME}" logs --tail "${LOG_TAIL}" "${CONTAINER_NAME}" >"${ARTIFACT_DIR}/child-container-log-tail.txt" 2>&1 || true
}

bootstrap_if_needed() {
  if [[ -n "${HOST_ROOT}" ]]; then
    return 0
  fi
  reserve_isolated_ports || fail "failed to reserve isolated host ports for recovery bootstrap"
  HOST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/swarm-recovery-XXXXXX")"
  OWN_BOOTSTRAP_ENV="true"
  GROUP_NAME="${GROUP_NAME:-s4-recovery-$(date +%Y%m%d-%H%M%S)}"
  local args=(
    "./tests/swarmd/local_replicate_e2e.sh"
    "--host-root" "${HOST_ROOT}"
    "--host-port" "${BOOTSTRAP_HOST_BACKEND_PORT}"
    "--host-desktop-port" "${BOOTSTRAP_HOST_DESKTOP_PORT}"
    "--runtime" "${RUNTIME}"
    "--workspace-path" "${WORKSPACE_PATH}"
    "--group-name" "${GROUP_NAME}"
    "--bypass-permissions" "${BYPASS_PERMISSIONS}"
    "--poll-timeout" "${ATTACH_TIMEOUT_SECONDS}"
    "--poll-interval" "${POLL_INTERVAL_SECONDS}"
    "--log-tail" "${LOG_TAIL}"
  )
  if [[ -n "${HOST_INSTALL_ARTIFACT_ROOT}" ]]; then
    args+=("--host-install-artifact-root" "${HOST_INSTALL_ARTIFACT_ROOT}")
  fi
  if [[ -n "${REPLICATION_MODE}" ]]; then
    args+=("--replication-mode" "${REPLICATION_MODE}")
  fi
  if [[ "${WORKSPACE_WRITABLE}" == "false" ]]; then
    args+=("--readonly")
  fi
  if [[ "${SYNC_ENABLED}" == "true" ]]; then
    args+=("--sync-enabled")
  fi
  if [[ -n "${SYNC_VAULT_PASSWORD}" ]]; then
    args+=("--sync-vault-password" "${SYNC_VAULT_PASSWORD}")
  fi
  if [[ -n "${HOST_VAULT_PASSWORD}" ]]; then
    args+=("--host-vault-password" "${HOST_VAULT_PASSWORD}")
  fi
  if [[ "${REBUILD_HOST}" != "true" ]]; then
    args+=("--skip-host-rebuild")
  fi
  if [[ "${REBUILD_IMAGE}" != "true" ]]; then
    args+=("--skip-image-rebuild")
  fi
  (
    cd "${ROOT_DIR}"
    "${args[@]}"
  )
}

cleanup_owned_bootstrap() {
  if [[ "${OWN_BOOTSTRAP_ENV}" != "true" || -z "${HOST_ROOT}" || ! -d "${HOST_ROOT}" ]]; then
    return 0
  fi

  local cleanup_runtime="${RUNTIME}"
  local cleanup_container="${CONTAINER_NAME}"
  local cleanup_host_stop="${HOST_STOP_SCRIPT}"

  if [[ -z "${cleanup_runtime}" || -z "${cleanup_container}" || -z "${cleanup_host_stop}" ]]; then
    local host_summary="${HOST_ROOT}/host-summary.json"
    local replicate_summary="${HOST_ROOT}/artifacts/summary.json"
    if [[ -z "${cleanup_host_stop}" && -f "${host_summary}" ]]; then
      cleanup_host_stop="$(jq -r '.stop_script // empty' "${host_summary}")"
    fi
    if [[ -f "${replicate_summary}" ]]; then
      if [[ -z "${cleanup_runtime}" ]]; then
        cleanup_runtime="$(jq -r '.runtime // empty' "${replicate_summary}")"
      fi
      if [[ -z "${cleanup_container}" ]]; then
        cleanup_container="$(jq -r '.container_name // empty' "${replicate_summary}")"
      fi
    fi
  fi

  if [[ -n "${cleanup_host_stop}" && -x "${cleanup_host_stop}" ]]; then
    "${cleanup_host_stop}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${cleanup_runtime}" && -n "${cleanup_container}" ]] && command -v "${cleanup_runtime}" >/dev/null 2>&1; then
    "${cleanup_runtime}" rm -f "${cleanup_container}" >/dev/null 2>&1 || true
  fi
}

cleanup() {
  if [[ -n "${HOST_DESKTOP_SESSION_COOKIE_FILE:-}" && -f "${HOST_DESKTOP_SESSION_COOKIE_FILE}" ]]; then
    rm -f -- "${HOST_DESKTOP_SESSION_COOKIE_FILE}"
  fi
  if [[ -n "${CHILD_DESKTOP_SESSION_COOKIE_FILE:-}" && -f "${CHILD_DESKTOP_SESSION_COOKIE_FILE}" ]]; then
    rm -f -- "${CHILD_DESKTOP_SESSION_COOKIE_FILE}"
  fi
  cleanup_owned_bootstrap
}

load_context() {
  HOST_ROOT="$(cd "${HOST_ROOT}" && pwd)"
  HOST_SUMMARY_FILE="${HOST_ROOT}/host-summary.json"
  REPLICATE_SUMMARY_FILE="${HOST_ROOT}/artifacts/summary.json"
  [[ -f "${HOST_SUMMARY_FILE}" ]] || fail "missing host summary: ${HOST_SUMMARY_FILE}"
  [[ -f "${REPLICATE_SUMMARY_FILE}" ]] || fail "missing replicate summary: ${REPLICATE_SUMMARY_FILE}"

  HOST_ADMIN_API_URL="$(jq -r '.api_url // empty' "${HOST_SUMMARY_FILE}")"
  HOST_START_SCRIPT="$(jq -r '.start_script // empty' "${HOST_SUMMARY_FILE}")"
  HOST_STOP_SCRIPT="$(jq -r '.stop_script // empty' "${HOST_SUMMARY_FILE}")"
  HOST_LOG_FILE="$(jq -r '.log_file // empty' "${HOST_SUMMARY_FILE}")"
  RUNTIME="$(jq -r '.runtime // empty' "${REPLICATE_SUMMARY_FILE}")"
  SOURCE_WORKSPACE_PATH="$(jq -r '.source_workspace_path // empty' "${REPLICATE_SUMMARY_FILE}")"
  DEPLOYMENT_ID="$(jq -r '.deployment_id // empty' "${REPLICATE_SUMMARY_FILE}")"
  CONTAINER_NAME="$(jq -r '.container_name // empty' "${REPLICATE_SUMMARY_FILE}")"
  CHILD_SWARM_ID="$(jq -r '.child_swarm_id // empty' "${REPLICATE_SUMMARY_FILE}")"
  CHILD_BACKEND_URL="$(jq -r '.child_backend_url // empty' "${REPLICATE_SUMMARY_FILE}")"
  CHILD_DESKTOP_URL="$(jq -r '.child_desktop_url // empty' "${REPLICATE_SUMMARY_FILE}")"
  [[ -n "${HOST_ADMIN_API_URL}" ]] || fail "host api url missing from ${HOST_SUMMARY_FILE}"
  [[ -n "${RUNTIME}" ]] || fail "runtime missing from ${REPLICATE_SUMMARY_FILE}"
  [[ -n "${DEPLOYMENT_ID}" ]] || fail "deployment_id missing from ${REPLICATE_SUMMARY_FILE}"
  [[ -n "${CONTAINER_NAME}" ]] || fail "container_name missing from ${REPLICATE_SUMMARY_FILE}"
  [[ -n "${CHILD_SWARM_ID}" ]] || fail "child_swarm_id missing from ${REPLICATE_SUMMARY_FILE}"
  [[ -n "${SOURCE_WORKSPACE_PATH}" ]] || fail "source_workspace_path missing from ${REPLICATE_SUMMARY_FILE}"
  [[ -n "${CHILD_BACKEND_URL}" ]] || fail "child_backend_url missing from ${REPLICATE_SUMMARY_FILE}"
  [[ -n "${CHILD_DESKTOP_URL}" ]] || fail "child_desktop_url missing from ${REPLICATE_SUMMARY_FILE}"

  HOST_XDG_CONFIG_HOME="${HOST_ROOT}/xdg/config"
  HOST_XDG_DATA_HOME="${HOST_ROOT}/xdg/data"
  HOST_XDG_STATE_HOME="${HOST_ROOT}/xdg/state"
  HOST_XDG_CACHE_HOME="${HOST_ROOT}/xdg/cache"

  RUN_ID="$(date +%Y%m%d-%H%M%S)"
  ARTIFACT_DIR="${HOST_ROOT}/recovery-artifacts/${RUN_ID}"
  mkdir -p "${ARTIFACT_DIR}"

  write_artifact "host-summary.json" "$(cat -- "${HOST_SUMMARY_FILE}")"
  write_artifact "replicate-summary.json" "$(cat -- "${REPLICATE_SUMMARY_FILE}")"
}

write_final_summary() {
  capture_logs
  jq -s \
    --arg host_root "${HOST_ROOT}" \
    --arg host_api_url "${HOST_ADMIN_API_URL}" \
    --arg runtime "${RUNTIME}" \
    --arg session_id "${SESSION_ID}" \
    --arg deployment_id "${DEPLOYMENT_ID}" \
    --arg container_name "${CONTAINER_NAME}" \
    --arg child_swarm_id "${CHILD_SWARM_ID}" \
    '{host_root:$host_root,host_api_url:$host_api_url,runtime:$runtime,session_id:$session_id,deployment_id:$deployment_id,container_name:$container_name,child_swarm_id:$child_swarm_id,scenarios:.}' \
    "${SCENARIO_FILES[@]}" >"${ARTIFACT_DIR}/summary.json"
  log ""
  log "Recovery summary"
  cat "${ARTIFACT_DIR}/summary.json" | jq .
  log ""
  log "Host root: ${HOST_ROOT}"
  log "Artifacts: ${ARTIFACT_DIR}"
}

HOST_ROOT=""
HOST_INSTALL_ARTIFACT_ROOT=""
RUNTIME="docker"
WORKSPACE_PATH="${ROOT_DIR}"
GROUP_NAME=""
REPLICATION_MODE=""
WORKSPACE_WRITABLE="true"
SYNC_ENABLED="false"
SYNC_VAULT_PASSWORD=""
SYNC_VAULT_PASSWORD_ENV=""
SYNC_VAULT_PASSWORD_FILE=""
HOST_VAULT_PASSWORD=""
HOST_VAULT_PASSWORD_ENV=""
HOST_VAULT_PASSWORD_FILE=""
BYPASS_PERMISSIONS="true"
REBUILD_HOST="true"
REBUILD_IMAGE="true"
ATTACH_TIMEOUT_SECONDS="90"
POLL_INTERVAL_SECONDS="2"
LOG_TAIL="200"
SCENARIO="all"
BOOTSTRAP_HOST_BACKEND_PORT="7781"
BOOTSTRAP_HOST_DESKTOP_PORT="5555"
ATTACH_TOKEN=""
HOST_DESKTOP_SESSION_COOKIE_FILE=""
CHILD_DESKTOP_SESSION_COOKIE_FILE=""
JSON_REQUEST_STATUS=""
JSON_REQUEST_BODY=""
SESSION_ID=""
SEED_MESSAGE_CONTENT=""
RUN_ID=""
ARTIFACT_DIR=""
OVERALL_FAILURE=0
SCENARIO_FILES=()
HOST_SUMMARY_FILE=""
REPLICATE_SUMMARY_FILE=""
HOST_ADMIN_API_URL=""
HOST_START_SCRIPT=""
HOST_STOP_SCRIPT=""
HOST_LOG_FILE=""
HOST_XDG_CONFIG_HOME=""
HOST_XDG_DATA_HOME=""
HOST_XDG_STATE_HOME=""
HOST_XDG_CACHE_HOME=""
SOURCE_WORKSPACE_PATH=""
DEPLOYMENT_ID=""
CONTAINER_NAME=""
CHILD_SWARM_ID=""
CHILD_BACKEND_URL=""
CHILD_DESKTOP_URL=""
LAST_DEPLOYMENTS_JSON=""
LAST_DEPLOYMENT_JSON=""
SCENARIO_NOTE=""
OWN_BOOTSTRAP_ENV="false"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --scenario)
      SCENARIO="$(printf '%s' "${2:-}" | tr '[:upper:]' '[:lower:]')"
      shift 2
      ;;
    --host-root)
      HOST_ROOT="${2:-}"
      shift 2
      ;;
    --host-install-artifact-root)
      HOST_INSTALL_ARTIFACT_ROOT="${2:-}"
      shift 2
      ;;
    --runtime)
      RUNTIME="${2:-}"
      shift 2
      ;;
    --workspace-path)
      WORKSPACE_PATH="${2:-}"
      shift 2
      ;;
    --group-name)
      GROUP_NAME="${2:-}"
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
    --sync-vault-password)
      SYNC_VAULT_PASSWORD="${2:-}"
      shift 2
      ;;
    --sync-vault-password-env)
      SYNC_VAULT_PASSWORD_ENV="${2:-}"
      shift 2
      ;;
    --sync-vault-password-file)
      SYNC_VAULT_PASSWORD_FILE="${2:-}"
      shift 2
      ;;
    --host-vault-password)
      HOST_VAULT_PASSWORD="${2:-}"
      shift 2
      ;;
    --host-vault-password-env)
      HOST_VAULT_PASSWORD_ENV="${2:-}"
      shift 2
      ;;
    --host-vault-password-file)
      HOST_VAULT_PASSWORD_FILE="${2:-}"
      shift 2
      ;;
    --bypass-permissions)
      BYPASS_PERMISSIONS="${2:-}"
      shift 2
      ;;
    --skip-host-rebuild)
      REBUILD_HOST="false"
      shift
      ;;
    --skip-image-rebuild)
      REBUILD_IMAGE="false"
      shift
      ;;
    --attach-timeout)
      ATTACH_TIMEOUT_SECONDS="${2:-}"
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

[[ "${ATTACH_TIMEOUT_SECONDS}" =~ ^[0-9]+$ ]] || fail "--attach-timeout must be a positive integer"
[[ "${POLL_INTERVAL_SECONDS}" =~ ^[0-9]+$ ]] || fail "--poll-interval must be a positive integer"
[[ "${LOG_TAIL}" =~ ^[0-9]+$ ]] || fail "--log-tail must be a positive integer"
[[ "${BYPASS_PERMISSIONS}" == "true" || "${BYPASS_PERMISSIONS}" == "false" ]] || fail "--bypass-permissions must be true or false"
[[ "${SCENARIO}" == "all" || "${SCENARIO}" == "s4-01" || "${SCENARIO}" == "s4-02" || "${SCENARIO}" == "s4-03" || "${SCENARIO}" == "s4-04" ]] || fail "--scenario must be one of s4-01, s4-02, s4-03, s4-04, all"
if [[ -n "${SYNC_VAULT_PASSWORD}" && ( -n "${SYNC_VAULT_PASSWORD_ENV}" || -n "${SYNC_VAULT_PASSWORD_FILE}" ) ]]; then
  fail "only one of --sync-vault-password, --sync-vault-password-env, or --sync-vault-password-file may be provided"
fi
if [[ -n "${SYNC_VAULT_PASSWORD_ENV}" && -n "${SYNC_VAULT_PASSWORD_FILE}" ]]; then
  fail "only one of --sync-vault-password-env or --sync-vault-password-file may be provided"
fi
resolve_secret_value "SYNC_VAULT_PASSWORD" "SYNC_VAULT_PASSWORD_ENV" "SYNC_VAULT_PASSWORD_FILE" "sync vault password" "false"
if [[ -n "${HOST_VAULT_PASSWORD}" && ( -n "${HOST_VAULT_PASSWORD_ENV}" || -n "${HOST_VAULT_PASSWORD_FILE}" ) ]]; then
  fail "only one of --host-vault-password, --host-vault-password-env, or --host-vault-password-file may be provided"
fi
if [[ -n "${HOST_VAULT_PASSWORD_ENV}" && -n "${HOST_VAULT_PASSWORD_FILE}" ]]; then
  fail "only one of --host-vault-password-env or --host-vault-password-file may be provided"
fi
resolve_secret_value "HOST_VAULT_PASSWORD" "HOST_VAULT_PASSWORD_ENV" "HOST_VAULT_PASSWORD_FILE" "host vault password" "false"

WORKSPACE_PATH="$(cd "${WORKSPACE_PATH}" && pwd)"
[[ -d "${WORKSPACE_PATH}" ]] || fail "--workspace-path must point to an existing directory"

if [[ -n "${HOST_INSTALL_ARTIFACT_ROOT}" ]]; then
  HOST_INSTALL_ARTIFACT_ROOT="$(cd "${HOST_INSTALL_ARTIFACT_ROOT}" && pwd)"
  [[ -d "${HOST_INSTALL_ARTIFACT_ROOT}" ]] || fail "--host-install-artifact-root must point to an existing directory"
fi

require_command curl
require_command jq
trap cleanup EXIT

bootstrap_if_needed
load_context
require_command "${RUNTIME}"
ensure_host_running || fail "failed to start host ${HOST_ADMIN_API_URL}"
wait_for_deployment_attached || fail "deployment ${DEPLOYMENT_ID} did not attach on the reused host"
create_routed_session || fail "failed to create routed session on host ${HOST_ADMIN_API_URL}"
SEED_MESSAGE_CONTENT="s4 seed before recovery ${RUN_ID}"
append_message_and_verify "seed" "${SEED_MESSAGE_CONTENT}" || fail "failed to append initial routed seed message"
verify_session_state "seed-state" || fail "initial routed session state verification failed"

log "Running Stage S4 recovery checks"
log "host root: ${HOST_ROOT}"
log "host api: ${HOST_ADMIN_API_URL}"
log "runtime: ${RUNTIME}"
log "deployment: ${DEPLOYMENT_ID}"
log "container: ${CONTAINER_NAME}"
log "child swarm: ${CHILD_SWARM_ID}"
log "session: ${SESSION_ID}"
log "artifacts: ${ARTIFACT_DIR}"

case "${SCENARIO}" in
  all)
    run_scenario "S4-01" scenario_s401
    run_scenario "S4-02" scenario_s402
    run_scenario "S4-03" scenario_s403
    run_scenario "S4-04" scenario_s404
    ;;
  s4-01)
    run_scenario "S4-01" scenario_s401
    ;;
  s4-02)
    run_scenario "S4-02" scenario_s402
    ;;
  s4-03)
    run_scenario "S4-03" scenario_s403
    ;;
  s4-04)
    run_scenario "S4-04" scenario_s404
    ;;
esac

write_final_summary

if (( OVERALL_FAILURE != 0 )); then
  exit 1
fi
