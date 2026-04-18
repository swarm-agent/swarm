#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"

# shellcheck disable=SC1091
source "${ROOT_DIR}/scripts/lib-lane.sh"

usage() {
  cat <<'EOF'
Usage: ./tests/swarmd/remote_deploy_e2e.sh [options]

Run the real remote SSH deploy path against one or more SSH targets with an isolated host:
  1. create or reuse an isolated temp XDG root for the host swarm
  2. write a swarm-enabled tailscale startup config for that isolated host
  3. optionally rebuild the host binaries
  4. start the isolated host swarm and ensure a Tailscale callback URL exists
  5. seed the source workspace with /v1/workspace/add
  6. create or select the current swarm group
  7. create and start remote deploy sessions for one or more SSH targets
  8. print and poll the remote Tailscale auth URLs
  9. auto-approve attach once each child enrolls back to the host

This harness covers the current supported remote path:
  - SSH is bootstrap only
  - Tailscale is the live child<->host transport

It does not automate the child Tailscale browser login in manual-auth mode.
It prints the auth URLs and waits for the user to complete them.

Options:
  --ssh-target <target>                 SSH alias/host to deploy to. Repeatable. Required.
  --launches-per-target <count>         Number of sessions to launch per SSH target. Default: 1
  --remote-runtime <docker|podman>      Remote runtime. Default: docker
  --session-prefix <prefix>             Remote child/session name prefix. Default: ssh-remote-e2e
  --workspace-path <path>               Source workspace path. Default: repo root
  --workspace-name <name>               Workspace display name. Default: basename of workspace path
  --group-id <id>                       Existing target group id
  --group-name <name>                   Existing target group name, or name to create
  --host-swarm-name <name>              Host swarm name. Default: Remote Deploy Test Host
  --host-root <path>                    Reuse a specific isolated host root instead of mktemp
  --host-backend-port <port>            Host backend/API port. Default: 17781
  --host-desktop-port <port>            Host desktop port. Default: 15555
  --host-peer-port <port>               Host peer transport port. Default: 17791
  --host-tailscale-url <url>            Override host tailscale callback URL
  --manage-tailscale-serve <true|false> Manage `tailscale serve` for the host callback URL. Default: true
  --sync-enabled <true|false>            Enable managed Swarm Sync for remote children. Default: true
  --sync-vault-password-env <name>       Environment variable containing the launch-only vault password
  --host-vault-password-env <name>       Environment variable containing the host vault password for sync proof
  --tailscale-auth-mode <manual|key>    Remote child Tailscale auth mode. Default: manual
  --tailscale-auth-key-env <name>       Environment variable containing the launch-only Tailscale auth key
  --skip-host-rebuild                   Reuse the current host binaries instead of rebuilding first
  --poll-timeout <seconds>              Attach/auth wait timeout. Default: 600
  --poll-interval <seconds>             Poll interval. Default: 5
  --remote-start-timeout <seconds>      Timeout for POST /v1/deploy/remote/session/start. Default: 600
  --auto-approve <true|false>           Auto-approve host attach once enrolled. Default: true
  --prove-routed-ai                     After attach, run the real routed AI/permission proof on each child
  --proof-auth-source <child|host-sync> Route proof creds from the child directly or via host sync. Default: child
  --proof-provider <id>                 Provider for routed AI proof. Default: fireworks
  --proof-model <id>                    Model for routed AI proof. Default: accounts/fireworks/models/kimi-k2p5
  --proof-thinking <low|medium|high>    Thinking level for routed AI proof. Default: high
  --proof-provider-key-env <name>       Environment variable containing the provider API key for child seeding
  --proof-bash-command <cmd>            Command for the bash approval proof. Default: pwd
  --teardown-only                       Reuse an existing --host-root, clean remote sessions, stop the host, and exit
  --no-wait                             Start remote sessions and print auth URLs, but do not wait for attach
  --teardown                            Stop the isolated host and remove remote child state at the end
  --help                                Show this help text

Artifacts:
  The harness keeps its host root and artifacts directory on disk and prints
  both paths at the end so config, DB, logs, and remote session state remain inspectable.

Notes:
  - This harness uses /v1/deploy/remote/session/*
  - It does not create a second remote transport path
  - Auth-key mode is launch-only: the raw key is sent only on remote session start and is not saved by Swarm
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
  local max_time_seconds="${4:-60}"
  local url="${HOST_ADMIN_API_URL%/}${path}"
  local body_file
  body_file="$(mktemp)"
  local request_body_file=""
  local http_code
  local args=(
    -sS
    --connect-timeout 3
    --max-time "${max_time_seconds}"
    -o "${body_file}"
    -w '%{http_code}'
    -H "Authorization: Bearer ${ATTACH_TOKEN}"
    -H 'Accept: application/json'
    -X "${method}"
  )
  if [[ -n "${body}" ]]; then
    request_body_file="$(mktemp)"
    printf '%s' "${body}" >"${request_body_file}"
    args+=(-H 'Content-Type: application/json' --data-binary "@${request_body_file}")
  fi
  if http_code="$(curl "${args[@]}" "${url}")"; then
    :
  else
    http_code="000"
  fi
  local response_body
  response_body="$(cat -- "${body_file}")"
  rm -f -- "${body_file}"
  if [[ -n "${request_body_file}" ]]; then
    rm -f -- "${request_body_file}"
  fi
  if [[ "${http_code}" != 2* ]]; then
    fail "${method} ${url} failed with status ${http_code}: ${response_body}"
  fi
  printf '%s' "${response_body}"
}

api_get() {
  api_request GET "$1"
}

api_post() {
  api_request POST "$1" "${2:-}"
}

api_post_with_timeout() {
  local path="${1:-}"
  local body="${2:-}"
  local max_time_seconds="${3:-60}"
  api_request POST "${path}" "${body}" "${max_time_seconds}"
}

fetch_attach_token_for_base() {
  local base_url="${1:-}"
  local response
  response="$(curl -fsS \
    -H "Origin: ${base_url%/}" \
    -H "Referer: ${base_url%/}/" \
    -H 'Sec-Fetch-Site: same-origin' \
    "${base_url%/}/v1/auth/attach/token")"
  printf '%s' "${response}" | jq -r '.token // empty'
}

json_request_with_bearer() {
  local base_url="${1:-}"
  local token="${2:-}"
  local method="${3:-GET}"
  local path="${4:-}"
  local body="${5:-}"
  local max_time_seconds="${6:-60}"
  local url="${base_url%/}${path}"
  local body_file
  body_file="$(mktemp)"
  local request_body_file=""
  local http_code
  local args=(
    -sS
    --connect-timeout 3
    --max-time "${max_time_seconds}"
    -o "${body_file}"
    -w '%{http_code}'
    -H "Authorization: Bearer ${token}"
    -H 'Accept: application/json'
    -X "${method}"
  )
  if [[ -n "${body}" ]]; then
    request_body_file="$(mktemp)"
    printf '%s' "${body}" >"${request_body_file}"
    args+=(-H 'Content-Type: application/json' --data-binary "@${request_body_file}")
  fi
  if http_code="$(curl "${args[@]}" "${url}")"; then
    :
  else
    http_code="000"
  fi
  local response_body
  response_body="$(cat -- "${body_file}")"
  rm -f -- "${body_file}"
  if [[ -n "${request_body_file}" ]]; then
    rm -f -- "${request_body_file}"
  fi
  if [[ "${http_code}" != 2* ]]; then
    fail "${method} ${url} failed with status ${http_code}: ${response_body}"
  fi
  printf '%s' "${response_body}"
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

seed_host_provider_key() {
  local provider="${1:-}"
  local api_key="${2:-}"
  [[ -n "${provider}" ]] || fail "provider is required for host provider seeding"
  [[ -n "${api_key}" ]] || fail "provider api key is required for host provider seeding"
  local payload response
  payload="$(jq -nc --arg provider "${provider}" --arg api_key "${api_key}" '{provider:$provider,type:"api",api_key:$api_key,active:true}')"
  response="$(api_post '/v1/auth/credentials' "${payload}")"
  printf '%s' "${response}"
}

wait_for_remote_child_synced_provider() {
  local child_base_url="${1:-}"
  local provider="${2:-}"
  local want_storage_mode="${3:-}"
  [[ -n "${child_base_url}" ]] || fail "child base url is required for synced credential wait"
  [[ -n "${provider}" ]] || fail "provider is required for synced credential wait"
  local child_token start_ts credentials_json match
  child_token="$(fetch_attach_token_for_base "${child_base_url}")"
  [[ -n "${child_token}" ]] || fail "failed to fetch child attach token from ${child_base_url}"
  start_ts="$(date +%s)"
  while :; do
    credentials_json="$(json_request_with_bearer "${child_base_url}" "${child_token}" GET "/v1/auth/credentials?provider=${provider}&limit=200" "" 60)"
    if [[ -n "${want_storage_mode}" ]]; then
      match="$(printf '%s' "${credentials_json}" | jq -c --arg provider "${provider}" --arg storage_mode "${want_storage_mode}" '.records[]? | select((.provider // "") == $provider and (.storage_mode // "") == $storage_mode) | .id' | head -n 1)"
    else
      match="$(printf '%s' "${credentials_json}" | jq -c --arg provider "${provider}" '.records[]? | select((.provider // "") == $provider) | .id' | head -n 1)"
    fi
    if [[ -n "${match}" && "${match}" != "null" ]]; then
      printf '%s' "${credentials_json}"
      return 0
    fi
    if (( "$(date +%s)" - start_ts >= POLL_TIMEOUT_SECONDS )); then
      fail "timed out waiting for synced provider ${provider} on remote child ${child_base_url}"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

prepare_host_routed_ai_prereqs() {
  if [[ "${PROVE_ROUTED_AI}" != "true" || "${PROOF_AUTH_SOURCE}" != "host-sync" ]]; then
    return 0
  fi
  local host_vault_password provider_key
  host_vault_password="${!HOST_VAULT_PASSWORD_ENV:-}"
  provider_key="${!PROOF_PROVIDER_KEY_ENV:-}"
  ensure_host_vault_ready "${host_vault_password}"
  write_artifact "host-auth-seed.json" "$(seed_host_provider_key "${PROOF_PROVIDER}" "${provider_key}")"
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
  local backend_port="${HOST_BACKEND_PORT}"
  local desktop_port="${HOST_DESKTOP_PORT}"
  local peer_port="${HOST_PEER_PORT}"
  local attempts=0
  while (( attempts < 200 )); do
    if port_is_available "${backend_port}" \
      && port_is_available "${desktop_port}" \
      && port_is_available "${peer_port}"; then
      HOST_BACKEND_PORT="${backend_port}"
      HOST_DESKTOP_PORT="${desktop_port}"
      HOST_PEER_PORT="${peer_port}"
      return 0
    fi
    backend_port=$((backend_port + 11))
    desktop_port=$((desktop_port + 11))
    peer_port=$((peer_port + 11))
    attempts=$((attempts + 1))
  done
  return 1
}

write_host_startup_config() {
  mkdir -p "$(dirname -- "${HOST_STARTUP_CONFIG}")"
  cat >"${HOST_STARTUP_CONFIG}" <<EOF
startup_mode = box
host = 127.0.0.1
port = ${HOST_BACKEND_PORT}
advertise_host =
advertise_port = ${HOST_BACKEND_PORT}
desktop_port = ${HOST_DESKTOP_PORT}
bypass_permissions = false
retain_tool_output_history = false
swarm_name = ${HOST_SWARM_NAME}
swarm_mode = true
child = false
mode = tailscale
tailscale_url = ${HOST_TAILSCALE_URL}
peer_transport_port = ${HOST_PEER_PORT}
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
}

prepare_isolated_host() {
  if [[ -z "${HOST_ROOT_OVERRIDE}" ]]; then
    HOST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/swarm-remote-XXXXXX")"
  else
    HOST_ROOT="${HOST_ROOT_OVERRIDE}"
    mkdir -p "${HOST_ROOT}"
  fi

  HOST_XDG_CONFIG_HOME="${HOST_ROOT}/xdg/config"
  HOST_XDG_DATA_HOME="${HOST_ROOT}/xdg/data"
  HOST_XDG_STATE_HOME="${HOST_ROOT}/xdg/state"
  HOST_XDG_CACHE_HOME="${HOST_ROOT}/xdg/cache"
  mkdir -p "${HOST_XDG_CONFIG_HOME}" "${HOST_XDG_DATA_HOME}" "${HOST_XDG_STATE_HOME}" "${HOST_XDG_CACHE_HOME}"

  export XDG_CONFIG_HOME="${HOST_XDG_CONFIG_HOME}"
  export XDG_DATA_HOME="${HOST_XDG_DATA_HOME}"
  export XDG_STATE_HOME="${HOST_XDG_STATE_HOME}"
  export XDG_CACHE_HOME="${HOST_XDG_CACHE_HOME}"

  reserve_isolated_ports || fail "unable to reserve isolated host ports"

  HOST_STARTUP_CONFIG="${HOST_XDG_CONFIG_HOME}/swarm/swarm.conf"
  HOST_ADMIN_API_URL="http://127.0.0.1:${HOST_BACKEND_PORT}"
  HOST_DESKTOP_URL="http://127.0.0.1:${HOST_DESKTOP_PORT}"
  ARTIFACT_DIR="${HOST_ROOT}/artifacts"
  mkdir -p "${ARTIFACT_DIR}"

  write_host_startup_config
  write_host_control_files
  write_artifact "host-startup-config.txt" "$(cat -- "${HOST_STARTUP_CONFIG}")"
}

resolve_host_tailscale_url() {
  if [[ -n "${HOST_TAILSCALE_URL_OVERRIDE}" ]]; then
    HOST_TAILSCALE_URL="$(trim "${HOST_TAILSCALE_URL_OVERRIDE}")"
    [[ -n "${HOST_TAILSCALE_URL}" ]] || fail "--host-tailscale-url resolved empty"
    return 0
  fi
  local dns_name
  dns_name="$(tailscale status --json | jq -r '.Self.DNSName // empty' | sed 's/\.$//')"
  [[ -n "${dns_name}" ]] || fail "tailscale status did not report a DNS name; log in to Tailscale first"
  HOST_TAILSCALE_URL="https://${dns_name}"
}

ensure_tailscale_serve() {
  if [[ "${MANAGE_TAILSCALE_SERVE}" != "true" ]]; then
    return 0
  fi
  local serve_json
  serve_json="$(tailscale serve status --json 2>/dev/null || printf '{}')"
  if [[ "${serve_json}" != "{}" && "${serve_json}" != "" ]]; then
    local serve_host current_proxy
    serve_host="${HOST_TAILSCALE_URL#https://}"
    serve_host="${serve_host#http://}"
    serve_host="${serve_host%/}"
    current_proxy="$(printf '%s' "${serve_json}" | jq -r --arg host "${serve_host}:443" '.Web[$host].Handlers["/"].Proxy // empty')"
    if [[ "${current_proxy}" == "http://127.0.0.1:${HOST_BACKEND_PORT}" ]]; then
      log "Reusing existing tailscale serve config for ${HOST_TAILSCALE_URL}"
      write_artifact "tailscale-serve-status.json" "${serve_json}"
      return 0
    fi
    fail "tailscale serve already has a non-empty config; rerun with --manage-tailscale-serve false and a known --host-tailscale-url, or reset serve first"
  fi
  log "Configuring tailscale serve for ${HOST_TAILSCALE_URL} -> ${HOST_ADMIN_API_URL}"
  tailscale serve --bg "http://127.0.0.1:${HOST_BACKEND_PORT}" >/dev/null
  CREATED_TAILSCALE_SERVE="true"
  write_artifact "tailscale-serve-status.json" "$(tailscale serve status --json 2>/dev/null || printf '{}')"
}

reset_tailscale_serve_if_needed() {
  if [[ "${CREATED_TAILSCALE_SERVE}" == "true" ]]; then
    tailscale serve reset >/dev/null 2>&1 || true
  fi
}

host_ready() {
  [[ "$(curl_http_code "${HOST_ADMIN_API_URL%/}/readyz")" == "200" ]]
}

ensure_host_running() {
  if host_ready; then
    return 0
  fi
  log "Starting isolated remote-deploy host ${HOST_SWARM_NAME} at ${HOST_ADMIN_API_URL}"
  (
    cd "${ROOT_DIR}"
    XDG_CONFIG_HOME="${HOST_XDG_CONFIG_HOME}" \
    XDG_DATA_HOME="${HOST_XDG_DATA_HOME}" \
    XDG_STATE_HOME="${HOST_XDG_STATE_HOME}" \
    XDG_CACHE_HOME="${HOST_XDG_CACHE_HOME}" \
    SWARM_LANE=main \
    ./swarmd/scripts/dev-up.sh
  )
  host_ready || fail "isolated host did not become ready at ${HOST_ADMIN_API_URL}"
}

stop_host() {
  if ! host_ready; then
    return 0
  fi
  log "Stopping isolated remote-deploy host ${HOST_ADMIN_API_URL}"
  "${HOST_ROOT}/stop-host.sh" >/dev/null 2>&1 || true
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
    GROUP_NAME="remote-deploy-test-group-$(date +%Y%m%d-%H%M%S)"
  fi

  if [[ -n "${TARGET_GROUP_ID:-}" ]]; then
    TARGET_GROUP_NAME="$(printf '%s' "${groups_json}" | jq -r --arg group_id "${TARGET_GROUP_ID}" '.groups[] | select(.group.id == $group_id) | .group.name // empty' | head -n 1)"
    [[ -n "${TARGET_GROUP_NAME}" ]] || TARGET_GROUP_NAME="${GROUP_NAME:-${TARGET_GROUP_ID}}"
    set_current_group "${TARGET_GROUP_ID}"
  else
    [[ -n "${GROUP_NAME}" ]] || fail "group name is required when no --group-id is supplied"
    local create_payload create_response
    create_payload="$(jq -nc --arg name "${GROUP_NAME}" '{name:$name,set_current:true}')"
    create_response="$(api_post '/v1/swarm/groups/upsert' "${create_payload}")"
    write_artifact "group-upsert-created.json" "${create_response}"
    TARGET_GROUP_ID="$(printf '%s' "${create_response}" | jq -r '.group.id // empty')"
    TARGET_GROUP_NAME="$(printf '%s' "${create_response}" | jq -r '.group.name // empty')"
  fi

  [[ -n "${TARGET_GROUP_ID:-}" ]] || fail "failed to resolve a target group id"
}

expand_targets() {
  EXPANDED_TARGETS=()
  local target copy_idx
  for target in "${SSH_TARGETS[@]}"; do
    copy_idx=1
    while (( copy_idx <= LAUNCHES_PER_TARGET )); do
      EXPANDED_TARGETS+=("${target}")
      copy_idx=$((copy_idx + 1))
    done
  done
}

create_remote_sessions() {
  SESSION_IDS=()
  SESSION_NAMES=()
  SESSION_TARGETS=()
  local idx=0
  local target session_name create_payload create_response session_id start_payload start_response tailscale_auth_key sync_vault_password
  tailscale_auth_key=""
  sync_vault_password=""
  if [[ "${TAILSCALE_AUTH_MODE}" == "key" ]]; then
    tailscale_auth_key="${!TAILSCALE_AUTH_KEY_ENV:-}"
  fi
  if [[ -n "${SYNC_VAULT_PASSWORD_ENV}" ]]; then
    sync_vault_password="${!SYNC_VAULT_PASSWORD_ENV:-}"
  fi
  for target in "${EXPANDED_TARGETS[@]}"; do
    idx=$((idx + 1))
    session_name="${SESSION_PREFIX}-$(printf '%02d' "${idx}")"
    create_payload="$(jq -nc \
      --arg name "${session_name}" \
      --arg ssh_session_target "${target}" \
      --arg group_id "${TARGET_GROUP_ID}" \
      --arg group_name "${TARGET_GROUP_NAME}" \
      --arg remote_runtime "${REMOTE_RUNTIME}" \
      --argjson sync_enabled "${SYNC_ENABLED}" \
      --arg source_path "${SOURCE_WORKSPACE_PATH}" \
      --arg workspace_path "${SOURCE_WORKSPACE_PATH}" \
      --arg workspace_name "${WORKSPACE_NAME}" \
      --arg target_path "/workspaces" \
      '{name:$name,ssh_session_target:$ssh_session_target,group_id:$group_id,group_name:$group_name,remote_runtime:$remote_runtime,sync_enabled:$sync_enabled,payloads:[{source_path:$source_path,workspace_path:$workspace_path,workspace_name:$workspace_name,target_path:$target_path,mode:"rw"}]}')"
    create_response="$(api_post '/v1/deploy/remote/session/create' "${create_payload}")"
    write_artifact "remote-session-$(printf '%02d' "${idx}")-create.json" "${create_response}"
    session_id="$(printf '%s' "${create_response}" | jq -r '.session.id // empty')"
    [[ -n "${session_id}" ]] || fail "remote session create for ${target} returned no session id"

    if [[ "${TAILSCALE_AUTH_MODE}" == "key" ]]; then
      start_payload="$(jq -nc --arg session_id "${session_id}" --arg tailscale_auth_key "${tailscale_auth_key}" --arg sync_vault_password "${sync_vault_password}" '{session_id:$session_id,tailscale_auth_key:$tailscale_auth_key} + (if $sync_vault_password != "" then {sync_vault_password:$sync_vault_password} else {} end)')"
    else
      start_payload="$(jq -nc --arg session_id "${session_id}" --arg sync_vault_password "${sync_vault_password}" '{session_id:$session_id} + (if $sync_vault_password != "" then {sync_vault_password:$sync_vault_password} else {} end)')"
    fi
    start_response="$(api_post_with_timeout '/v1/deploy/remote/session/start' "${start_payload}" "${REMOTE_START_MAX_TIME_SECONDS}")"
    write_artifact "remote-session-$(printf '%02d' "${idx}")-start.json" "${start_response}"

    SESSION_IDS+=("${session_id}")
    SESSION_NAMES+=("${session_name}")
    SESSION_TARGETS+=("${target}")
  done
}

refresh_remote_sessions() {
  REMOTE_SESSIONS_JSON="$(api_get '/v1/deploy/remote/session')"
  write_artifact "remote-sessions-latest.json" "${REMOTE_SESSIONS_JSON}"
}

session_json_by_id() {
  local session_id="${1:-}"
  printf '%s' "${REMOTE_SESSIONS_JSON}" | jq -c --arg session_id "${session_id}" '.sessions[] | select(.id == $session_id)' | head -n 1
}

populate_session_arrays_from_remote_sessions() {
  SESSION_IDS=()
  SESSION_NAMES=()
  SESSION_TARGETS=()
  local encoded row
  while IFS= read -r encoded; do
    [[ -n "${encoded}" ]] || continue
    row="$(printf '%s' "${encoded}" | base64 -d)"
    SESSION_IDS+=("$(printf '%s' "${row}" | jq -r '.id // empty')")
    SESSION_NAMES+=("$(printf '%s' "${row}" | jq -r '.name // empty')")
    SESSION_TARGETS+=("$(printf '%s' "${row}" | jq -r '.ssh_session_target // empty')")
  done < <(printf '%s' "${REMOTE_SESSIONS_JSON}" | jq -r '.sessions[]? | @base64')
}

seed_remote_child_provider_key() {
  local child_base_url="${1:-}"
  local provider="${2:-}"
  local api_key="${3:-}"
  [[ -n "${child_base_url}" ]] || fail "child base url is required for provider seeding"
  [[ -n "${provider}" ]] || fail "provider is required for provider seeding"
  [[ -n "${api_key}" ]] || fail "provider api key is required for provider seeding"
  local child_token payload response
  child_token="$(fetch_attach_token_for_base "${child_base_url}")"
  [[ -n "${child_token}" ]] || fail "failed to fetch child attach token from ${child_base_url}"
  payload="$(jq -nc --arg provider "${provider}" --arg api_key "${api_key}" '{provider:$provider,type:"api",api_key:$api_key,active:true}')"
  response="$(json_request_with_bearer "${child_base_url}" "${child_token}" POST '/v1/auth/credentials' "${payload}" 60)"
  printf '%s' "${response}"
}

create_routed_host_session() {
  local child_swarm_id="${1:-}"
  local runtime_workspace_path="${2:-}"
  [[ -n "${child_swarm_id}" ]] || fail "child swarm id is required for routed session create"
  local body response
  body="$(jq -nc \
    --arg title "remote-proof-${child_swarm_id}" \
    --arg workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg host_workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg runtime_workspace_path "${runtime_workspace_path}" \
    --arg workspace_name "${WORKSPACE_NAME}" \
    --arg provider "${PROOF_PROVIDER}" \
    --arg model "${PROOF_MODEL}" \
    --arg thinking "${PROOF_THINKING}" \
    '{title:$title,workspace_path:$workspace_path,host_workspace_path:$host_workspace_path,runtime_workspace_path:$runtime_workspace_path,workspace_name:$workspace_name,mode:"plan",preference:{provider:$provider,model:$model,thinking:$thinking}}')"
  response="$(api_post "/v1/sessions?swarm_id=${child_swarm_id}" "${body}")"
  printf '%s' "${response}"
}

start_routed_session_run() {
  local session_id="${1:-}"
  local prompt="${2:-}"
  [[ -n "${session_id}" ]] || fail "session id is required for routed run start"
  local body response
  body="$(jq -nc --arg prompt "${prompt}" '{type:"run.start",prompt:$prompt,background:true}')"
  response="$(api_post "/v1/sessions/${session_id}/run/stream" "${body}")"
  printf '%s' "${response}"
}

wait_for_pending_permission() {
  local session_id="${1:-}"
  local tool_name="${2:-}"
  local start_ts permission_json
  start_ts="$(date +%s)"
  while :; do
    permission_json="$(api_get "/v1/sessions/${session_id}/permissions?limit=200")"
    local match
    match="$(printf '%s' "${permission_json}" | jq -c --arg tool_name "${tool_name}" '[.permissions[]? | select((.status // "") == "pending" and (.tool_name // "") == $tool_name)] | sort_by(.created_at // 0) | last // empty')"
    if [[ -n "${match}" && "${match}" != "null" ]]; then
      printf '%s' "${match}"
      return 0
    fi
    if (( "$(date +%s)" - start_ts >= POLL_TIMEOUT_SECONDS )); then
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
  local payload response
  payload="$(jq -nc --arg action "${action}" --arg reason "${reason}" '{action:$action,reason:$reason}')"
  response="$(api_post "/v1/sessions/${session_id}/permissions/${permission_id}/resolve" "${payload}")"
  printf '%s' "${response}"
}

wait_for_session_mode() {
  local session_id="${1:-}"
  local want_mode="${2:-}"
  local start_ts session_json current_mode
  start_ts="$(date +%s)"
  while :; do
    session_json="$(api_get "/v1/sessions/${session_id}")"
    current_mode="$(printf '%s' "${session_json}" | jq -r '.session.mode // empty')"
    if [[ "${current_mode}" == "${want_mode}" ]]; then
      printf '%s' "${session_json}"
      return 0
    fi
    if (( "$(date +%s)" - start_ts >= POLL_TIMEOUT_SECONDS )); then
      fail "timed out waiting for session ${session_id} mode ${want_mode}"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

wait_for_message_content() {
  local session_id="${1:-}"
  local want_content="${2:-}"
  local start_ts messages_json
  start_ts="$(date +%s)"
  while :; do
    messages_json="$(api_get "/v1/sessions/${session_id}/messages?limit=200")"
    if printf '%s' "${messages_json}" | jq -e --arg want "${want_content}" '.messages[]? | select((.role // "") == "assistant" and ((.content // "") | contains($want)))' >/dev/null 2>&1; then
      printf '%s' "${messages_json}"
      return 0
    fi
    if (( "$(date +%s)" - start_ts >= POLL_TIMEOUT_SECONDS )); then
      fail "timed out waiting for session ${session_id} message ${want_content}"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

run_routed_ai_proof_for_session() {
  local remote_session_json="${1:-}"
  local session_id="${2:-}"
  [[ -n "${remote_session_json}" ]] || fail "remote session json is required for routed AI proof"
  [[ -n "${session_id}" ]] || fail "remote deploy session id is required for routed AI proof"

  local child_swarm_id child_base_url runtime_workspace_path provider_key want_storage_mode
  child_swarm_id="$(printf '%s' "${remote_session_json}" | jq -r '.child_swarm_id // empty')"
  child_base_url="$(printf '%s' "${remote_session_json}" | jq -r '.remote_tailnet_url // empty')"
  runtime_workspace_path="$(printf '%s' "${remote_session_json}" | jq -r '.preflight.payloads[0].target_path // empty')"
  if [[ -z "${runtime_workspace_path}" || "${runtime_workspace_path}" == "null" ]]; then
    runtime_workspace_path="/workspaces/${WORKSPACE_NAME}"
  fi
  provider_key="${!PROOF_PROVIDER_KEY_ENV:-}"
  want_storage_mode=""

  [[ -n "${child_swarm_id}" ]] || fail "remote session ${session_id} is missing child_swarm_id for routed AI proof"
  [[ -n "${child_base_url}" ]] || fail "remote session ${session_id} is missing remote_tailnet_url for routed AI proof"
  [[ -n "${provider_key}" ]] || fail "environment variable ${PROOF_PROVIDER_KEY_ENV} is required for routed AI proof"

  case "${PROOF_AUTH_SOURCE}" in
    child)
      write_artifact "remote-session-${session_id}-child-auth-seed.json" "$(seed_remote_child_provider_key "${child_base_url}" "${PROOF_PROVIDER}" "${provider_key}")"
      ;;
    host-sync)
      if [[ -n "${HOST_VAULT_PASSWORD_ENV}" ]]; then
        want_storage_mode="pebble/vault"
      fi
      write_artifact "remote-session-${session_id}-child-auth-synced.json" "$(wait_for_remote_child_synced_provider "${child_base_url}" "${PROOF_PROVIDER}" "${want_storage_mode}")"
      ;;
    *)
      fail "unsupported proof auth source: ${PROOF_AUTH_SOURCE}"
      ;;
  esac

  local routed_create_json routed_session_id
  routed_create_json="$(create_routed_host_session "${child_swarm_id}" "${runtime_workspace_path}")"
  write_artifact "remote-session-${session_id}-routed-session-create.json" "${routed_create_json}"
  routed_session_id="$(printf '%s' "${routed_create_json}" | jq -r '.session.id // empty')"
  [[ -n "${routed_session_id}" ]] || fail "routed session create for remote session ${session_id} returned no session id"

  write_artifact "remote-session-${session_id}-run-exit-plan-start.json" "$(start_routed_session_run "${routed_session_id}" "Exit plan mode. After approval, reply with exactly: I got out.")"
  local exit_permission_json exit_permission_id
  exit_permission_json="$(wait_for_pending_permission "${routed_session_id}" "exit_plan_mode")"
  write_artifact "remote-session-${session_id}-permission-exit-plan-pending.json" "${exit_permission_json}"
  exit_permission_id="$(printf '%s' "${exit_permission_json}" | jq -r '.id // empty')"
  [[ -n "${exit_permission_id}" ]] || fail "pending exit_plan_mode permission for session ${routed_session_id} returned no id"
  write_artifact "remote-session-${session_id}-permission-exit-plan-resolve.json" "$(resolve_session_permission "${routed_session_id}" "${exit_permission_id}" "approve" "ok")"
  write_artifact "remote-session-${session_id}-session-auto.json" "$(wait_for_session_mode "${routed_session_id}" "auto")"
  write_artifact "remote-session-${session_id}-message-i-got-out.json" "$(wait_for_message_content "${routed_session_id}" "I got out.")"

  write_artifact "remote-session-${session_id}-run-bash-start.json" "$(start_routed_session_run "${routed_session_id}" "Run exactly this bash command and return only its output: ${PROOF_BASH_COMMAND}")"
  local bash_permission_json bash_permission_id
  bash_permission_json="$(wait_for_pending_permission "${routed_session_id}" "bash")"
  write_artifact "remote-session-${session_id}-permission-bash-pending.json" "${bash_permission_json}"
  bash_permission_id="$(printf '%s' "${bash_permission_json}" | jq -r '.id // empty')"
  [[ -n "${bash_permission_id}" ]] || fail "pending bash permission for session ${routed_session_id} returned no id"
  write_artifact "remote-session-${session_id}-permission-bash-resolve.json" "$(resolve_session_permission "${routed_session_id}" "${bash_permission_id}" "approve" "ok")"
  write_artifact "remote-session-${session_id}-message-bash-output.json" "$(wait_for_message_content "${routed_session_id}" "${runtime_workspace_path}")"
}

approve_remote_session() {
  local session_id="${1:-}"
  local response
  response="$(api_post "/v1/deploy/remote/session/${session_id}/approve")"
  write_artifact "remote-session-${session_id}-approve.json" "${response}"
}

wait_for_remote_attach() {
  local start_ts
  start_ts="$(date +%s)"
  declare -gA LAST_PRINTED_AUTH_URLS=()
  while :; do
    refresh_remote_sessions
    local pending_count=0
    local idx session_id session_name target session_json status auth_url enrollment_id last_error
    for idx in "${!SESSION_IDS[@]}"; do
      session_id="${SESSION_IDS[$idx]}"
      session_name="${SESSION_NAMES[$idx]}"
      target="${SESSION_TARGETS[$idx]}"
      session_json="$(session_json_by_id "${session_id}")"
      [[ -n "${session_json}" ]] || fail "remote session ${session_id} disappeared from list"
      status="$(printf '%s' "${session_json}" | jq -r '.status // empty')"
      auth_url="$(printf '%s' "${session_json}" | jq -r '.remote_auth_url // empty')"
      enrollment_id="$(printf '%s' "${session_json}" | jq -r '.enrollment_id // empty')"
      last_error="$(printf '%s' "${session_json}" | jq -r '.last_error // empty')"

      if [[ -n "${auth_url}" && "${LAST_PRINTED_AUTH_URLS[${session_id}]:-}" != "${auth_url}" ]]; then
        LAST_PRINTED_AUTH_URLS["${session_id}"]="${auth_url}"
        log "Remote auth required for ${session_name} (${target}): ${auth_url}"
      fi

      case "${status}" in
        attached)
          ;;
        failed)
          fail "remote session ${session_name} (${target}) failed: ${last_error:-unknown error}"
          ;;
        *)
          pending_count=$((pending_count + 1))
          if [[ -n "${enrollment_id}" && "${AUTO_APPROVE}" == "true" ]]; then
            log "Approving remote session ${session_name} (${target}) enrollment ${enrollment_id}"
            approve_remote_session "${session_id}"
            refresh_remote_sessions
            session_json="$(session_json_by_id "${session_id}")"
            [[ -n "${session_json}" ]] || fail "remote session ${session_id} disappeared after approve"
            status="$(printf '%s' "${session_json}" | jq -r '.status // empty')"
            if [[ "${status}" == "attached" ]]; then
              pending_count=$((pending_count - 1))
            fi
          fi
          ;;
      esac
    done

    if (( pending_count == 0 )); then
      return 0
    fi
    if (( "$(date +%s)" - start_ts >= POLL_TIMEOUT_SECONDS )); then
      fail "timed out waiting for remote sessions to attach after ${POLL_TIMEOUT_SECONDS}s"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

write_summary() {
  refresh_remote_sessions
  local summary_json
  summary_json="$(jq -nc \
    --arg host_root "${HOST_ROOT}" \
    --arg host_api_url "${HOST_ADMIN_API_URL}" \
    --arg host_desktop_url "${HOST_DESKTOP_URL}" \
    --arg host_tailscale_url "${HOST_TAILSCALE_URL}" \
    --arg group_id "${TARGET_GROUP_ID}" \
    --arg group_name "${TARGET_GROUP_NAME}" \
    --arg workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg workspace_name "${WORKSPACE_NAME}" \
    --argjson sessions "${REMOTE_SESSIONS_JSON}" \
    '{host_root:$host_root,host_api_url:$host_api_url,host_desktop_url:$host_desktop_url,host_tailscale_url:$host_tailscale_url,group_id:$group_id,group_name:$group_name,workspace_path:$workspace_path,workspace_name:$workspace_name,sessions:($sessions.sessions // [])}')"
  write_artifact "summary.json" "${summary_json}"
}

cleanup_remote_sessions() {
  if [[ -z "${REMOTE_SESSIONS_JSON:-}" ]]; then
    refresh_remote_sessions || true
  fi
  if (( ${#SESSION_IDS[@]} == 0 )); then
    populate_session_arrays_from_remote_sessions
  fi
  local idx session_id target session_json remote_root systemd_unit remote_runtime image_ref
  for idx in "${!SESSION_IDS[@]}"; do
    session_id="${SESSION_IDS[$idx]}"
    target="${SESSION_TARGETS[$idx]}"
    session_json="$(session_json_by_id "${session_id}")"
    [[ -n "${session_json}" ]] || continue
    remote_root="$(printf '%s' "${session_json}" | jq -r '.preflight.remote_root // empty')"
    systemd_unit="$(printf '%s' "${session_json}" | jq -r '.preflight.systemd_unit // empty')"
    remote_runtime="$(printf '%s' "${session_json}" | jq -r '.remote_runtime // "docker"')"
    image_ref="$(printf '%s' "${session_json}" | jq -r '.image_ref // empty')"
    log "Cleaning remote session ${session_id} on ${target}"
    ssh "${target}" "bash -lc $(printf '%q' "set -euo pipefail
if [ -n '${systemd_unit}' ]; then
  unit_path=\$(systemctl show -p FragmentPath --value '${systemd_unit}' 2>/dev/null || true)
  sudo systemctl disable --now '${systemd_unit}' >/dev/null 2>&1 || true
  if [ -n \"\${unit_path}\" ]; then
    sudo rm -f \"\${unit_path}\" >/dev/null 2>&1 || true
  fi
  sudo systemctl daemon-reload >/dev/null 2>&1 || true
fi
if [ '${remote_runtime}' = 'podman' ]; then
  podman rm -f swarm-remote-child >/dev/null 2>&1 || true
  if [ -n '${image_ref}' ]; then
    podman image rm '${image_ref}' >/dev/null 2>&1 || true
  fi
else
  sudo docker rm -f swarm-remote-child >/dev/null 2>&1 || true
  if [ -n '${image_ref}' ]; then
    sudo docker image rm '${image_ref}' >/dev/null 2>&1 || true
  fi
fi
if [ -n '${remote_root}' ]; then
  rm -rf '${remote_root}'
fi
")" >/dev/null 2>&1 || true
  done
}

print_next_steps() {
  local idx session_id session_name target session_json status auth_url tailnet_url child_swarm_id
  refresh_remote_sessions
  log ""
  log "Remote deploy harness summary:"
  log "  host root: ${HOST_ROOT}"
  log "  artifacts: ${ARTIFACT_DIR}"
  log "  host api: ${HOST_ADMIN_API_URL}"
  log "  host desktop: ${HOST_DESKTOP_URL}"
  log "  host tailscale url: ${HOST_TAILSCALE_URL}"
  for idx in "${!SESSION_IDS[@]}"; do
    session_id="${SESSION_IDS[$idx]}"
    session_name="${SESSION_NAMES[$idx]}"
    target="${SESSION_TARGETS[$idx]}"
    session_json="$(session_json_by_id "${session_id}")"
    [[ -n "${session_json}" ]] || continue
    status="$(printf '%s' "${session_json}" | jq -r '.status // empty')"
    auth_url="$(printf '%s' "${session_json}" | jq -r '.remote_auth_url // empty')"
    tailnet_url="$(printf '%s' "${session_json}" | jq -r '.remote_tailnet_url // empty')"
    child_swarm_id="$(printf '%s' "${session_json}" | jq -r '.child_swarm_id // empty')"
    log "  - ${session_name} (${target})"
    log "    session_id: ${session_id}"
    log "    status: ${status:-<empty>}"
    [[ -n "${child_swarm_id}" ]] && log "    child_swarm_id: ${child_swarm_id}"
    [[ -n "${tailnet_url}" ]] && log "    remote_tailnet_url: ${tailnet_url}"
    [[ -n "${auth_url}" ]] && log "    remote_auth_url: ${auth_url}"
  done
  return 0
}

REBUILD_HOST="true"
WAIT_FOR_ATTACH="true"
AUTO_APPROVE="true"
MANAGE_TAILSCALE_SERVE="true"
CREATED_TAILSCALE_SERVE="false"
SYNC_ENABLED="true"
SYNC_VAULT_PASSWORD_ENV=""
HOST_VAULT_PASSWORD_ENV=""
TAILSCALE_AUTH_MODE="manual"
TAILSCALE_AUTH_KEY_ENV=""
TEARDOWN="false"
TEARDOWN_ONLY="false"
PROVE_ROUTED_AI="false"

HOST_ROOT_OVERRIDE=""
HOST_SWARM_NAME="Remote Deploy Test Host"
HOST_BACKEND_PORT=17781
HOST_DESKTOP_PORT=15555
HOST_PEER_PORT=17791
HOST_TAILSCALE_URL_OVERRIDE=""
HOST_TAILSCALE_URL=""

SOURCE_WORKSPACE_PATH="${ROOT_DIR}"
WORKSPACE_NAME="$(basename "${SOURCE_WORKSPACE_PATH}")"
GROUP_ID=""
GROUP_NAME=""
REMOTE_RUNTIME="docker"
SESSION_PREFIX="ssh-remote-e2e"
LAUNCHES_PER_TARGET=1
POLL_TIMEOUT_SECONDS=600
POLL_INTERVAL_SECONDS=5
REMOTE_START_MAX_TIME_SECONDS=600
PROOF_PROVIDER="fireworks"
PROOF_MODEL="accounts/fireworks/models/kimi-k2p5"
PROOF_THINKING="high"
PROOF_AUTH_SOURCE="child"
PROOF_PROVIDER_KEY_ENV=""
PROOF_BASH_COMMAND="pwd"

SSH_TARGETS=()
SESSION_IDS=()
SESSION_NAMES=()
SESSION_TARGETS=()
EXPANDED_TARGETS=()

while (($# > 0)); do
  case "$1" in
    --ssh-target)
      shift
      [[ $# -gt 0 ]] || fail "--ssh-target requires a value"
      SSH_TARGETS+=("$1")
      ;;
    --launches-per-target)
      shift
      [[ $# -gt 0 ]] || fail "--launches-per-target requires a value"
      LAUNCHES_PER_TARGET="$1"
      ;;
    --remote-runtime)
      shift
      [[ $# -gt 0 ]] || fail "--remote-runtime requires a value"
      REMOTE_RUNTIME="$1"
      ;;
    --session-prefix)
      shift
      [[ $# -gt 0 ]] || fail "--session-prefix requires a value"
      SESSION_PREFIX="$1"
      ;;
    --workspace-path)
      shift
      [[ $# -gt 0 ]] || fail "--workspace-path requires a value"
      SOURCE_WORKSPACE_PATH="$1"
      ;;
    --workspace-name)
      shift
      [[ $# -gt 0 ]] || fail "--workspace-name requires a value"
      WORKSPACE_NAME="$1"
      ;;
    --group-id)
      shift
      [[ $# -gt 0 ]] || fail "--group-id requires a value"
      GROUP_ID="$1"
      ;;
    --group-name)
      shift
      [[ $# -gt 0 ]] || fail "--group-name requires a value"
      GROUP_NAME="$1"
      ;;
    --host-swarm-name)
      shift
      [[ $# -gt 0 ]] || fail "--host-swarm-name requires a value"
      HOST_SWARM_NAME="$1"
      ;;
    --host-root)
      shift
      [[ $# -gt 0 ]] || fail "--host-root requires a value"
      HOST_ROOT_OVERRIDE="$1"
      ;;
    --host-backend-port)
      shift
      [[ $# -gt 0 ]] || fail "--host-backend-port requires a value"
      HOST_BACKEND_PORT="$1"
      ;;
    --host-desktop-port)
      shift
      [[ $# -gt 0 ]] || fail "--host-desktop-port requires a value"
      HOST_DESKTOP_PORT="$1"
      ;;
    --host-peer-port)
      shift
      [[ $# -gt 0 ]] || fail "--host-peer-port requires a value"
      HOST_PEER_PORT="$1"
      ;;
    --host-tailscale-url)
      shift
      [[ $# -gt 0 ]] || fail "--host-tailscale-url requires a value"
      HOST_TAILSCALE_URL_OVERRIDE="$1"
      ;;
    --manage-tailscale-serve)
      shift
      [[ $# -gt 0 ]] || fail "--manage-tailscale-serve requires a value"
      MANAGE_TAILSCALE_SERVE="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
      ;;
    --sync-enabled)
      shift
      [[ $# -gt 0 ]] || fail "--sync-enabled requires a value"
      SYNC_ENABLED="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
      ;;
    --sync-vault-password-env)
      shift
      [[ $# -gt 0 ]] || fail "--sync-vault-password-env requires a value"
      SYNC_VAULT_PASSWORD_ENV="$1"
      ;;
    --host-vault-password-env)
      shift
      [[ $# -gt 0 ]] || fail "--host-vault-password-env requires a value"
      HOST_VAULT_PASSWORD_ENV="$1"
      ;;
    --tailscale-auth-mode)
      shift
      [[ $# -gt 0 ]] || fail "--tailscale-auth-mode requires a value"
      TAILSCALE_AUTH_MODE="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
      ;;
    --tailscale-auth-key-env)
      shift
      [[ $# -gt 0 ]] || fail "--tailscale-auth-key-env requires a value"
      TAILSCALE_AUTH_KEY_ENV="$1"
      ;;
    --skip-host-rebuild)
      REBUILD_HOST="false"
      ;;
    --poll-timeout)
      shift
      [[ $# -gt 0 ]] || fail "--poll-timeout requires a value"
      POLL_TIMEOUT_SECONDS="$1"
      ;;
    --poll-interval)
      shift
      [[ $# -gt 0 ]] || fail "--poll-interval requires a value"
      POLL_INTERVAL_SECONDS="$1"
      ;;
    --remote-start-timeout)
      shift
      [[ $# -gt 0 ]] || fail "--remote-start-timeout requires a value"
      REMOTE_START_MAX_TIME_SECONDS="$1"
      ;;
    --auto-approve)
      shift
      [[ $# -gt 0 ]] || fail "--auto-approve requires a value"
      AUTO_APPROVE="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
      ;;
    --prove-routed-ai)
      PROVE_ROUTED_AI="true"
      ;;
    --proof-auth-source)
      shift
      [[ $# -gt 0 ]] || fail "--proof-auth-source requires a value"
      PROOF_AUTH_SOURCE="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
      ;;
    --proof-provider)
      shift
      [[ $# -gt 0 ]] || fail "--proof-provider requires a value"
      PROOF_PROVIDER="$1"
      ;;
    --proof-model)
      shift
      [[ $# -gt 0 ]] || fail "--proof-model requires a value"
      PROOF_MODEL="$1"
      ;;
    --proof-thinking)
      shift
      [[ $# -gt 0 ]] || fail "--proof-thinking requires a value"
      PROOF_THINKING="$1"
      ;;
    --proof-provider-key-env)
      shift
      [[ $# -gt 0 ]] || fail "--proof-provider-key-env requires a value"
      PROOF_PROVIDER_KEY_ENV="$1"
      ;;
    --proof-bash-command)
      shift
      [[ $# -gt 0 ]] || fail "--proof-bash-command requires a value"
      PROOF_BASH_COMMAND="$1"
      ;;
    --teardown-only)
      TEARDOWN_ONLY="true"
      ;;
    --no-wait)
      WAIT_FOR_ATTACH="false"
      ;;
    --teardown)
      TEARDOWN="true"
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
  shift
done

require_command jq
require_command curl
require_command ssh
require_command tailscale

(( ${#SSH_TARGETS[@]} > 0 )) || fail "at least one --ssh-target is required"
[[ "${LAUNCHES_PER_TARGET}" =~ ^[0-9]+$ ]] || fail "--launches-per-target must be a positive integer"
(( LAUNCHES_PER_TARGET > 0 )) || fail "--launches-per-target must be greater than zero"
[[ "${POLL_TIMEOUT_SECONDS}" =~ ^[0-9]+$ ]] || fail "--poll-timeout must be a positive integer"
(( POLL_TIMEOUT_SECONDS > 0 )) || fail "--poll-timeout must be greater than zero"
[[ "${POLL_INTERVAL_SECONDS}" =~ ^[0-9]+$ ]] || fail "--poll-interval must be a positive integer"
(( POLL_INTERVAL_SECONDS > 0 )) || fail "--poll-interval must be greater than zero"
[[ "${REMOTE_START_MAX_TIME_SECONDS}" =~ ^[0-9]+$ ]] || fail "--remote-start-timeout must be a positive integer"
(( REMOTE_START_MAX_TIME_SECONDS > 0 )) || fail "--remote-start-timeout must be greater than zero"
[[ -d "${SOURCE_WORKSPACE_PATH}" ]] || fail "--workspace-path must point to an existing directory"
[[ -n "${WORKSPACE_NAME}" ]] || fail "--workspace-name resolved empty"

case "${REMOTE_RUNTIME}" in
  docker|podman) ;;
  *) fail "--remote-runtime must be docker or podman" ;;
esac

case "${TAILSCALE_AUTH_MODE}" in
  manual) ;;
  key) ;;
  *) fail "--tailscale-auth-mode must be manual or key" ;;
esac

case "${SYNC_ENABLED}" in
  true|false) ;;
  *) fail "--sync-enabled must be true or false" ;;
esac

if [[ "${SYNC_ENABLED}" == "true" && -n "${SYNC_VAULT_PASSWORD_ENV}" ]]; then
  [[ -n "${!SYNC_VAULT_PASSWORD_ENV:-}" ]] || fail "environment variable ${SYNC_VAULT_PASSWORD_ENV} is required for --sync-vault-password-env"
fi

if [[ -n "${HOST_VAULT_PASSWORD_ENV}" ]]; then
  [[ -n "${!HOST_VAULT_PASSWORD_ENV:-}" ]] || fail "environment variable ${HOST_VAULT_PASSWORD_ENV} is required for --host-vault-password-env"
fi

if [[ "${TAILSCALE_AUTH_MODE}" == "key" ]]; then
  [[ -n "${TAILSCALE_AUTH_KEY_ENV}" ]] || fail "--tailscale-auth-key-env is required with --tailscale-auth-mode key"
  [[ -n "${!TAILSCALE_AUTH_KEY_ENV:-}" ]] || fail "environment variable ${TAILSCALE_AUTH_KEY_ENV} is required for --tailscale-auth-mode key"
fi

case "${MANAGE_TAILSCALE_SERVE}" in
  true|false) ;;
  *) fail "--manage-tailscale-serve must be true or false" ;;
esac

case "${AUTO_APPROVE}" in
  true|false) ;;
  *) fail "--auto-approve must be true or false" ;;
esac

case "${PROVE_ROUTED_AI}" in
  true|false) ;;
  *) fail "--prove-routed-ai flag resolved to invalid value" ;;
esac

if [[ "${PROVE_ROUTED_AI}" == "true" ]]; then
  [[ -n "${PROOF_PROVIDER_KEY_ENV}" ]] || fail "--proof-provider-key-env is required with --prove-routed-ai"
  [[ -n "${!PROOF_PROVIDER_KEY_ENV:-}" ]] || fail "environment variable ${PROOF_PROVIDER_KEY_ENV} is required for --prove-routed-ai"
fi

case "${PROOF_AUTH_SOURCE}" in
  child|host-sync) ;;
  *) fail "--proof-auth-source must be child or host-sync" ;;
esac

if [[ "${PROOF_AUTH_SOURCE}" == "host-sync" ]]; then
  [[ "${PROVE_ROUTED_AI}" == "true" ]] || fail "--proof-auth-source host-sync requires --prove-routed-ai"
  [[ "${SYNC_ENABLED}" == "true" ]] || fail "--proof-auth-source host-sync requires --sync-enabled true"
  [[ -n "${HOST_VAULT_PASSWORD_ENV}" ]] || fail "--proof-auth-source host-sync requires --host-vault-password-env"
  if [[ -z "${SYNC_VAULT_PASSWORD_ENV}" ]]; then
    SYNC_VAULT_PASSWORD_ENV="${HOST_VAULT_PASSWORD_ENV}"
  fi
fi

case "${TEARDOWN_ONLY}" in
  true|false) ;;
  *) fail "--teardown-only flag resolved to invalid value" ;;
esac

if [[ "${TEARDOWN_ONLY}" == "true" && -z "${HOST_ROOT_OVERRIDE}" ]]; then
  fail "--teardown-only requires --host-root"
fi

prepare_isolated_host
resolve_host_tailscale_url
write_host_startup_config
write_artifact "host-startup-config.txt" "$(cat -- "${HOST_STARTUP_CONFIG}")"

maybe_rebuild_host
ensure_host_running
ensure_tailscale_serve
fetch_attach_token
prepare_host_routed_ai_prereqs
if [[ "${TEARDOWN_ONLY}" == "true" ]]; then
  refresh_remote_sessions
  cleanup_remote_sessions
  stop_host
  reset_tailscale_serve_if_needed
  exit 0
fi
seed_source_workspace
ensure_target_group
expand_targets
create_remote_sessions
write_summary
print_next_steps

if [[ "${WAIT_FOR_ATTACH}" == "true" ]]; then
  wait_for_remote_attach
  if [[ "${PROVE_ROUTED_AI}" == "true" ]]; then
    refresh_remote_sessions
    idx=""
    session_id=""
    session_json=""
    for idx in "${!SESSION_IDS[@]}"; do
      session_id="${SESSION_IDS[$idx]}"
      session_json="$(session_json_by_id "${session_id}")"
      [[ -n "${session_json}" ]] || fail "remote session ${session_id} disappeared before routed AI proof"
      run_routed_ai_proof_for_session "${session_json}" "${session_id}"
    done
  fi
  write_summary
  print_next_steps
fi

if [[ "${TEARDOWN}" == "true" ]]; then
  cleanup_remote_sessions
  stop_host
  reset_tailscale_serve_if_needed
fi
