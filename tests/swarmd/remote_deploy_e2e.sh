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
  2. write a swarm-enabled startup config for that isolated host
  3. optionally rebuild the host binaries
  4. start the isolated host swarm and ensure a callback URL exists
  5. seed the source workspace with /v1/workspace/add
  6. create or select the current swarm group
  7. create and start remote deploy sessions for one or more SSH targets
  8. print and poll the remote transport/auth state
  9. auto-approve attach once each child enrolls back to the host

This harness covers the current supported remote paths:
  - SSH is bootstrap only
  - Tailscale or LAN/WireGuard is the live child<->host transport

In Tailscale manual-auth mode it does not automate the child browser login.
It prints the auth URLs and waits for the user to complete them.

Options:
  --ssh-target <target>                 SSH alias/host to deploy to. Repeatable. Required.
  --launches-per-target <count>         Number of sessions to launch per SSH target. Default: 1
  --transport-mode <tailscale|lan>      Remote child transport after SSH bootstrap. Default: tailscale
  --remote-runtime <docker|podman>      Remote runtime. Default: docker
  --image-delivery-mode <registry|archive>
                                      Remote image delivery path. Default: registry
  --session-prefix <prefix>             Remote child/session name prefix. Default: ssh-remote-e2e
  --workspace-path <path>               Source workspace path. Default: repo root
  --workspace-name <name>               Workspace display name. Default: basename of workspace path
  --group-id <id>                       Existing target group id
  --group-name <name>                   Existing target group name, or name to create
  --host-swarm-name <name>              Host swarm name. Default: Remote Deploy Test Host
  --host-lane <main|dev>                Local lane for the isolated host swarm. Default: main
  --host-root <path>                    Reuse a specific isolated host root instead of mktemp
  --host-bind-host <host>               Host bind address for the isolated master. LAN mode defaults to the first private non-container address.
  --host-advertise-host <host>          Host advertised callback address. Default: host bind address for LAN mode.
  --remote-advertise-host <host>        Remote child advertised callback host/IP. LAN mode may infer this from the SSH target if omitted.
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
  --proof-bash-expected <text>          Expected assistant text after the bash proof. Default: runtime workspace path
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
  - Registry image delivery mirrors the desktop UI path. Archive delivery is
    still available for local-dev/current-worktree validation.
  - Auth-key mode is launch-only: the raw key is sent only on remote session start and is not saved by Swarm
EOF
}

log() {
  printf '%s\n' "$*"
}

now_ms() {
  date +%s%3N
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

write_timing_artifact() {
  local name="${1:-}"
  local phase="${2:-}"
  local duration_ms="${3:-0}"
  local extra_json="${4:-{}}"
  local payload
  payload="$(jq -nc --arg phase "${phase}" --arg duration_ms "${duration_ms}" --arg extra_json "${extra_json}" '($extra_json | fromjson? // {}) + {phase:$phase,duration_ms:($duration_ms | tonumber? // 0)}')"
  write_artifact "${name}" "${payload}"
  printf '%s\n' "${payload}" >>"${ARTIFACT_DIR}/timings.ndjson"
  log "timing ${phase}: ${duration_ms}ms"
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
  payload="$(SWARM_HARNESS_PROVIDER="${provider}" SWARM_HARNESS_API_KEY="${api_key}" jq -nc '{provider:env.SWARM_HARNESS_PROVIDER,type:"api",api_key:env.SWARM_HARNESS_API_KEY,active:true}')"
  response="$(api_post '/v1/auth/credentials' "${payload}")"
  printf '%s' "${response}"
}

wait_for_remote_child_synced_provider() {
  local remote_session_json="${1:-}"
  local provider="${2:-}"
  local want_storage_mode="${3:-}"
  [[ -n "${remote_session_json}" ]] || fail "remote session json is required for synced credential wait"
  [[ -n "${provider}" ]] || fail "provider is required for synced credential wait"
  local start_ts credentials_json match
  start_ts="$(date +%s)"
  while :; do
    credentials_json="$(remote_child_json_request_over_ssh "${remote_session_json}" GET "/v1/auth/credentials?provider=${provider}&limit=200" "" 60)"
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
      fail "timed out waiting for synced provider ${provider} on remote child"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

prepare_host_routed_ai_prereqs() {
  if [[ "${PROVE_ROUTED_AI}" != "true" || "${PROOF_AUTH_SOURCE}" != "host-sync" ]]; then
    return 0
  fi
  local host_vault_password provider_key seed_response
  host_vault_password="${!HOST_VAULT_PASSWORD_ENV:-}"
  provider_key="${!PROOF_PROVIDER_KEY_ENV:-}"
  ensure_host_vault_ready "${host_vault_password}"
  if ! seed_response="$(seed_host_provider_key "${PROOF_PROVIDER}" "${provider_key}")"; then
    fail "failed to seed provider ${PROOF_PROVIDER} on host"
  fi
  write_artifact "host-auth-seed.json" "${seed_response}"
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

is_private_ipv4() {
  local value="${1:-}"
  [[ "${value}" =~ ^10\. ]] && return 0
  [[ "${value}" =~ ^192\.168\. ]] && return 0
  [[ "${value}" =~ ^172\.(1[6-9]|2[0-9]|3[0-1])\. ]] && return 0
  return 1
}

detect_lan_bind_host() {
  local candidate iface cidr
  while read -r _ iface _ cidr _; do
    [[ -n "${iface:-}" && -n "${cidr:-}" ]] || continue
    case "${iface}" in
      lo|tailscale0|docker0|br-*|veth*|cni*|flannel*|virbr*|zt*|podman*)
        continue
        ;;
    esac
    candidate="${cidr%%/*}"
    if is_private_ipv4 "${candidate}"; then
      printf '%s' "${candidate}"
      return 0
    fi
  done < <(ip -o -4 addr show scope global)
  return 1
}

reserve_isolated_ports() {
  local backend_port="${HOST_BACKEND_PORT}"
  local desktop_port="${HOST_DESKTOP_PORT}"
  local peer_port="${HOST_PEER_PORT}"
  local lane_offset
  lane_offset="$(host_lane_port_offset)"
  local attempts=0
  while (( attempts < 200 )); do
    local effective_backend_port=$((backend_port + lane_offset))
    local effective_desktop_port=$((desktop_port + lane_offset))
    if (( effective_backend_port > 65535 || effective_desktop_port > 65535 )); then
      return 1
    fi
    if port_is_available "${effective_backend_port}" \
      && port_is_available "${effective_desktop_port}" \
      && port_is_available "${peer_port}"; then
      HOST_BACKEND_PORT="${backend_port}"
      HOST_DESKTOP_PORT="${desktop_port}"
      HOST_PEER_PORT="${peer_port}"
      HOST_EFFECTIVE_BACKEND_PORT="${effective_backend_port}"
      HOST_EFFECTIVE_DESKTOP_PORT="${effective_desktop_port}"
      return 0
    fi
    backend_port=$((backend_port + 11))
    desktop_port=$((desktop_port + 11))
    peer_port=$((peer_port + 11))
    attempts=$((attempts + 1))
  done
  return 1
}

host_lane_port_offset() {
  case "${HOST_LANE}" in
    dev) printf '1' ;;
    *) printf '0' ;;
  esac
}

write_host_startup_config() {
  mkdir -p "$(dirname -- "${HOST_STARTUP_CONFIG}")"
  cat >"${HOST_STARTUP_CONFIG}" <<EOF
startup_mode = box
dev_mode = false
host = ${HOST_BIND_HOST}
port = ${HOST_BACKEND_PORT}
advertise_host = ${HOST_ADVERTISE_HOST}
advertise_port = ${HOST_BACKEND_PORT}
desktop_port = ${HOST_DESKTOP_PORT}
bypass_permissions = false
retain_tool_output_history = false
swarm_name = ${HOST_SWARM_NAME}
swarm_mode = true
child = false
mode = ${TRANSPORT_MODE}
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
remote_deploy_host_api_base_url =
remote_deploy_host_desktop_url =
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
SWARM_LANE="${HOST_LANE}" \\
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
SWARM_LANE="${HOST_LANE}" \\
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
  HOST_ADMIN_API_URL="http://${HOST_BIND_HOST}:${HOST_EFFECTIVE_BACKEND_PORT}"
  HOST_DESKTOP_URL="http://${HOST_BIND_HOST}:${HOST_EFFECTIVE_DESKTOP_PORT}"
  if [[ "${TRANSPORT_MODE}" == "lan" ]]; then
    HOST_CALLBACK_URL="http://${HOST_ADVERTISE_HOST}:${HOST_EFFECTIVE_BACKEND_PORT}"
  fi
  ARTIFACT_DIR="${HOST_ROOT}/artifacts"
  mkdir -p "${ARTIFACT_DIR}"
  if [[ "${TEARDOWN_ONLY}" != "true" ]]; then
    : >"${ARTIFACT_DIR}/timings.ndjson"
  fi

  write_host_startup_config
  write_host_control_files
  write_artifact "host-startup-config.txt" "$(cat -- "${HOST_STARTUP_CONFIG}")"
}

resolve_host_transport() {
  case "${TRANSPORT_MODE}" in
    tailscale)
      HOST_BIND_HOST="127.0.0.1"
      HOST_ADVERTISE_HOST=""
      resolve_host_tailscale_url
      HOST_CALLBACK_URL="${HOST_TAILSCALE_URL}"
      ;;
    lan)
      HOST_BIND_HOST="$(trim "${HOST_BIND_HOST_OVERRIDE}")"
      if [[ -z "${HOST_BIND_HOST}" ]]; then
        HOST_BIND_HOST="$(detect_lan_bind_host || true)"
      fi
      [[ -n "${HOST_BIND_HOST}" ]] || fail "could not determine a LAN/WireGuard host bind address; pass --host-bind-host"
      HOST_ADVERTISE_HOST="$(trim "${HOST_ADVERTISE_HOST_OVERRIDE}")"
      [[ -n "${HOST_ADVERTISE_HOST}" ]] || HOST_ADVERTISE_HOST="${HOST_BIND_HOST}"
      HOST_TAILSCALE_URL=""
      HOST_CALLBACK_URL="http://${HOST_ADVERTISE_HOST}:${HOST_EFFECTIVE_BACKEND_PORT}"
      ;;
    *)
      fail "unsupported transport mode: ${TRANSPORT_MODE}"
      ;;
  esac
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
  if [[ "${TRANSPORT_MODE}" != "tailscale" ]]; then
    return 0
  fi
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
    if [[ "${current_proxy}" == "http://127.0.0.1:${HOST_EFFECTIVE_BACKEND_PORT}" ]]; then
      log "Reusing existing tailscale serve config for ${HOST_TAILSCALE_URL}"
      write_artifact "tailscale-serve-status.json" "${serve_json}"
      return 0
    fi
    fail "tailscale serve already has a non-empty config; rerun with --manage-tailscale-serve false and a known --host-tailscale-url, or reset serve first"
  fi
  log "Configuring tailscale serve for ${HOST_TAILSCALE_URL} -> ${HOST_ADMIN_API_URL}"
  tailscale serve --bg "http://127.0.0.1:${HOST_EFFECTIVE_BACKEND_PORT}" >/dev/null
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
    SWARM_LANE="${HOST_LANE}" \
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
  if [[ -z "${HOST_DESKTOP_SESSION_COOKIE_FILE:-}" ]]; then
    HOST_DESKTOP_SESSION_COOKIE_FILE="$(mktemp)"
  fi
  curl -fsS \
    -c "${HOST_DESKTOP_SESSION_COOKIE_FILE}" \
    -b "${HOST_DESKTOP_SESSION_COOKIE_FILE}" \
    -H "Origin: ${HOST_ADMIN_API_URL%/}" \
    -H "Referer: ${HOST_ADMIN_API_URL%/}/" \
    -H 'Sec-Fetch-Site: same-origin' \
    "${HOST_ADMIN_API_URL%/}/v1/auth/desktop/session" >/dev/null
  ATTACH_TOKEN=""
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
    SWARM_LANE="${HOST_LANE}" \
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
  local create_started_ms create_duration_ms start_started_ms start_duration_ms timing_extra_json
  tailscale_auth_key=""
  sync_vault_password=""
  if [[ "${TRANSPORT_MODE}" == "tailscale" && "${TAILSCALE_AUTH_MODE}" == "key" ]]; then
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
      --arg transport_mode "${TRANSPORT_MODE}" \
      --arg remote_advertise_host "${REMOTE_ADVERTISE_HOST}" \
      --arg group_id "${TARGET_GROUP_ID}" \
      --arg group_name "${TARGET_GROUP_NAME}" \
      --arg remote_runtime "${REMOTE_RUNTIME}" \
      --arg image_delivery_mode "${IMAGE_DELIVERY_MODE}" \
      --argjson sync_enabled "${SYNC_ENABLED}" \
      --arg source_path "${SOURCE_WORKSPACE_PATH}" \
      --arg workspace_path "${SOURCE_WORKSPACE_PATH}" \
      --arg workspace_name "${WORKSPACE_NAME}" \
      --arg target_path "/workspaces" \
      '{name:$name,ssh_session_target:$ssh_session_target,transport_mode:$transport_mode,group_id:$group_id,group_name:$group_name,remote_runtime:$remote_runtime,image_delivery_mode:$image_delivery_mode,sync_enabled:$sync_enabled,payloads:[{source_path:$source_path,workspace_path:$workspace_path,workspace_name:$workspace_name,target_path:$target_path,mode:"rw"}]} + (if $remote_advertise_host != "" then {remote_advertise_host:$remote_advertise_host} else {} end)')"
    create_started_ms="$(now_ms)"
    create_response="$(api_post '/v1/deploy/remote/session/create' "${create_payload}")"
    create_duration_ms=$(( $(now_ms) - create_started_ms ))
    write_artifact "remote-session-$(printf '%02d' "${idx}")-create.json" "${create_response}"
    timing_extra_json="$(jq -nc --arg session_name "${session_name}" --arg ssh_target "${target}" '{session_name:$session_name,ssh_target:$ssh_target}')"
    write_timing_artifact "remote-session-$(printf '%02d' "${idx}")-create-timing.json" "remote_session_create" "${create_duration_ms}" "${timing_extra_json}"
    session_id="$(printf '%s' "${create_response}" | jq -r '.session.id // empty')"
    [[ -n "${session_id}" ]] || fail "remote session create for ${target} returned no session id"

    if [[ "${TRANSPORT_MODE}" == "tailscale" && "${TAILSCALE_AUTH_MODE}" == "key" ]]; then
      start_payload="$(jq -nc --arg session_id "${session_id}" --arg tailscale_auth_key "${tailscale_auth_key}" --arg sync_vault_password "${sync_vault_password}" '{session_id:$session_id,tailscale_auth_key:$tailscale_auth_key} + (if $sync_vault_password != "" then {sync_vault_password:$sync_vault_password} else {} end)')"
    else
      start_payload="$(jq -nc --arg session_id "${session_id}" --arg sync_vault_password "${sync_vault_password}" '{session_id:$session_id} + (if $sync_vault_password != "" then {sync_vault_password:$sync_vault_password} else {} end)')"
    fi
    start_started_ms="$(now_ms)"
    start_response="$(api_post_with_timeout '/v1/deploy/remote/session/start' "${start_payload}" "${REMOTE_START_MAX_TIME_SECONDS}")"
    start_duration_ms=$(( $(now_ms) - start_started_ms ))
    write_artifact "remote-session-$(printf '%02d' "${idx}")-start.json" "${start_response}"
    timing_extra_json="$(jq -nc --arg session_id "${session_id}" --arg session_name "${session_name}" --arg ssh_target "${target}" '{session_id:$session_id,session_name:$session_name,ssh_target:$ssh_target}')"
    write_timing_artifact "remote-session-${session_id}-start-timing.json" "remote_session_start" "${start_duration_ms}" "${timing_extra_json}"

    SESSION_IDS+=("${session_id}")
    SESSION_NAMES+=("${session_name}")
    SESSION_TARGETS+=("${target}")
    SESSION_WAIT_START_MS["${session_id}"]="$(now_ms)"
  done
}

refresh_remote_sessions() {
  REMOTE_SESSIONS_JSON="$(api_get '/v1/deploy/remote/session?refresh=1')"
  write_artifact "remote-sessions-latest.json" "${REMOTE_SESSIONS_JSON}"
}

try_refresh_remote_sessions() {
  local output err_file err_text
  err_file="$(mktemp)"
  if output="$(api_get '/v1/deploy/remote/session?refresh=1' 2>"${err_file}")"; then
    rm -f -- "${err_file}"
    REMOTE_SESSIONS_JSON="${output}"
    write_artifact "remote-sessions-latest.json" "${REMOTE_SESSIONS_JSON}"
    return 0
  fi
  err_text="$(cat -- "${err_file}")"
  rm -f -- "${err_file}"
  write_artifact "remote-sessions-refresh-error-latest.txt" "${err_text}"
  log "Remote session refresh failed; retrying: $(printf '%s' "${err_text}" | tail -n 1)"
  return 1
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

remote_child_json_request_over_ssh() {
  local remote_session_json="${1:-}"
  local method="${2:-GET}"
  local path="${3:-}"
  local body="${4:-}"
  local max_time_seconds="${5:-60}"
  [[ -n "${remote_session_json}" ]] || fail "remote session json is required for remote child request"
  [[ -n "${path}" ]] || fail "remote child request path is required"

  local ssh_target remote_root socket_path remote_script
  ssh_target="$(printf '%s' "${remote_session_json}" | jq -r '.ssh_session_target // empty')"
  remote_root="$(printf '%s' "${remote_session_json}" | jq -r '.preflight.remote_root // empty')"
  [[ -n "${ssh_target}" ]] || fail "remote session is missing ssh_session_target"
  [[ -n "${remote_root}" ]] || fail "remote session is missing preflight.remote_root"
  socket_path="${remote_root%/}/state/swarmd/local-transport/api.sock"

  remote_script="$(printf '%s\n' \
    'set -euo pipefail' \
    'request_body="$(mktemp)"' \
    'cleanup() { rm -f -- "${request_body}"; }' \
    'trap cleanup EXIT' \
    'cat >"${request_body}"' \
    "args=(-fsS --connect-timeout 3 --max-time '${max_time_seconds}' --unix-socket '${socket_path}' -H 'Accept: application/json' -X '${method}')" \
    'if [ -s "${request_body}" ]; then' \
    '  args+=(-H "Content-Type: application/json" --data-binary "@${request_body}")' \
    'fi' \
    "curl \"\${args[@]}\" 'http://swarmd${path}'")"
  printf '%s' "${body}" | ssh "${ssh_target}" "bash -lc $(printf '%q' "${remote_script}")"
}

seed_remote_child_provider_key() {
  local remote_session_json="${1:-}"
  local provider="${2:-}"
  local api_key="${3:-}"
  [[ -n "${remote_session_json}" ]] || fail "remote session json is required for provider seeding"
  [[ -n "${provider}" ]] || fail "provider is required for provider seeding"
  [[ -n "${api_key}" ]] || fail "provider api key is required for provider seeding"
  local payload response
  payload="$(SWARM_HARNESS_PROVIDER="${provider}" SWARM_HARNESS_API_KEY="${api_key}" jq -nc '{provider:env.SWARM_HARNESS_PROVIDER,type:"api",api_key:env.SWARM_HARNESS_API_KEY,active:true}')"
  response="$(remote_child_json_request_over_ssh "${remote_session_json}" POST '/v1/auth/credentials' "${payload}" 60)"
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

  local child_swarm_id child_base_url runtime_workspace_path provider_key want_storage_mode bash_expected
  local started_ms duration_ms timing_extra_json exit_total_started_ms bash_total_started_ms
  child_swarm_id="$(printf '%s' "${remote_session_json}" | jq -r '.child_swarm_id // empty')"
  child_base_url="$(printf '%s' "${remote_session_json}" | jq -r '.remote_endpoint // .remote_tailnet_url // empty')"
  runtime_workspace_path="$(printf '%s' "${remote_session_json}" | jq -r '.preflight.payloads[0].target_path // empty')"
  if [[ -z "${runtime_workspace_path}" || "${runtime_workspace_path}" == "null" ]]; then
    runtime_workspace_path="/workspaces/${WORKSPACE_NAME}"
  fi
  bash_expected="${PROOF_BASH_EXPECTED}"
  if [[ -z "${bash_expected}" ]]; then
    bash_expected="${runtime_workspace_path}"
  fi
  provider_key="${!PROOF_PROVIDER_KEY_ENV:-}"
  want_storage_mode=""

  [[ -n "${child_swarm_id}" ]] || fail "remote session ${session_id} is missing child_swarm_id for routed AI proof"
  [[ -n "${child_base_url}" ]] || fail "remote session ${session_id} is missing remote_tailnet_url for routed AI proof"
  [[ -n "${provider_key}" ]] || fail "environment variable ${PROOF_PROVIDER_KEY_ENV} is required for routed AI proof"

  case "${PROOF_AUTH_SOURCE}" in
    child)
      local child_seed_response
      if ! child_seed_response="$(seed_remote_child_provider_key "${remote_session_json}" "${PROOF_PROVIDER}" "${provider_key}")"; then
        fail "failed to seed provider ${PROOF_PROVIDER} on remote child for session ${session_id}"
      fi
      write_artifact "remote-session-${session_id}-child-auth-seed.json" "${child_seed_response}"
      ;;
    host-sync)
      if [[ -n "${HOST_VAULT_PASSWORD_ENV}" ]]; then
        want_storage_mode="pebble/vault"
      fi
      local child_sync_response
      if ! child_sync_response="$(wait_for_remote_child_synced_provider "${remote_session_json}" "${PROOF_PROVIDER}" "${want_storage_mode}")"; then
        fail "failed waiting for provider ${PROOF_PROVIDER} on remote child for session ${session_id}"
      fi
      write_artifact "remote-session-${session_id}-child-auth-synced.json" "${child_sync_response}"
      ;;
    *)
      fail "unsupported proof auth source: ${PROOF_AUTH_SOURCE}"
      ;;
  esac

  local routed_create_json routed_session_id
  started_ms="$(now_ms)"
  routed_create_json="$(create_routed_host_session "${child_swarm_id}" "${runtime_workspace_path}")"
  duration_ms=$(( $(now_ms) - started_ms ))
  write_artifact "remote-session-${session_id}-routed-session-create.json" "${routed_create_json}"
  timing_extra_json="$(jq -nc --arg session_id "${session_id}" --arg child_swarm_id "${child_swarm_id}" '{session_id:$session_id,child_swarm_id:$child_swarm_id}')"
  write_timing_artifact "remote-session-${session_id}-routed-session-create-timing.json" "routed_session_create" "${duration_ms}" "${timing_extra_json}"
  routed_session_id="$(printf '%s' "${routed_create_json}" | jq -r '.session.id // empty')"
  [[ -n "${routed_session_id}" ]] || fail "routed session create for remote session ${session_id} returned no session id"

  exit_total_started_ms="$(now_ms)"
  started_ms="${exit_total_started_ms}"
  write_artifact "remote-session-${session_id}-run-exit-plan-start.json" "$(start_routed_session_run "${routed_session_id}" "Exit plan mode. After approval, reply with exactly: I got out.")"
  duration_ms=$(( $(now_ms) - started_ms ))
  timing_extra_json="$(jq -nc --arg session_id "${session_id}" --arg routed_session_id "${routed_session_id}" '{session_id:$session_id,routed_session_id:$routed_session_id}')"
  write_timing_artifact "remote-session-${session_id}-run-exit-plan-start-timing.json" "routed_run_start_exit_plan" "${duration_ms}" "${timing_extra_json}"
  local exit_permission_json exit_permission_id
  started_ms="$(now_ms)"
  exit_permission_json="$(wait_for_pending_permission "${routed_session_id}" "exit_plan_mode")"
  duration_ms=$(( $(now_ms) - started_ms ))
  write_artifact "remote-session-${session_id}-permission-exit-plan-pending.json" "${exit_permission_json}"
  write_timing_artifact "remote-session-${session_id}-permission-exit-plan-pending-timing.json" "routed_wait_exit_plan_permission" "${duration_ms}" "${timing_extra_json}"
  exit_permission_id="$(printf '%s' "${exit_permission_json}" | jq -r '.id // empty')"
  [[ -n "${exit_permission_id}" ]] || fail "pending exit_plan_mode permission for session ${routed_session_id} returned no id"
  started_ms="$(now_ms)"
  write_artifact "remote-session-${session_id}-permission-exit-plan-resolve.json" "$(resolve_session_permission "${routed_session_id}" "${exit_permission_id}" "approve" "ok")"
  duration_ms=$(( $(now_ms) - started_ms ))
  write_timing_artifact "remote-session-${session_id}-permission-exit-plan-resolve-timing.json" "routed_resolve_exit_plan_permission" "${duration_ms}" "${timing_extra_json}"
  started_ms="$(now_ms)"
  write_artifact "remote-session-${session_id}-session-auto.json" "$(wait_for_session_mode "${routed_session_id}" "auto")"
  duration_ms=$(( $(now_ms) - started_ms ))
  write_timing_artifact "remote-session-${session_id}-session-auto-timing.json" "routed_wait_session_auto" "${duration_ms}" "${timing_extra_json}"
  started_ms="$(now_ms)"
  write_artifact "remote-session-${session_id}-message-i-got-out.json" "$(wait_for_message_content "${routed_session_id}" "I got out.")"
  duration_ms=$(( $(now_ms) - started_ms ))
  write_timing_artifact "remote-session-${session_id}-message-i-got-out-timing.json" "routed_wait_exit_plan_message" "${duration_ms}" "${timing_extra_json}"
  duration_ms=$(( $(now_ms) - exit_total_started_ms ))
  write_timing_artifact "remote-session-${session_id}-exit-plan-total-timing.json" "routed_exit_plan_total_host_to_response" "${duration_ms}" "${timing_extra_json}"

  bash_total_started_ms="$(now_ms)"
  started_ms="${bash_total_started_ms}"
  write_artifact "remote-session-${session_id}-run-bash-start.json" "$(start_routed_session_run "${routed_session_id}" "Run exactly this bash command and return only its output: ${PROOF_BASH_COMMAND}")"
  duration_ms=$(( $(now_ms) - started_ms ))
  write_timing_artifact "remote-session-${session_id}-run-bash-start-timing.json" "routed_run_start_bash" "${duration_ms}" "${timing_extra_json}"
  local bash_permission_json bash_permission_id
  started_ms="$(now_ms)"
  bash_permission_json="$(wait_for_pending_permission "${routed_session_id}" "bash")"
  duration_ms=$(( $(now_ms) - started_ms ))
  write_artifact "remote-session-${session_id}-permission-bash-pending.json" "${bash_permission_json}"
  write_timing_artifact "remote-session-${session_id}-permission-bash-pending-timing.json" "routed_wait_bash_permission" "${duration_ms}" "${timing_extra_json}"
  bash_permission_id="$(printf '%s' "${bash_permission_json}" | jq -r '.id // empty')"
  [[ -n "${bash_permission_id}" ]] || fail "pending bash permission for session ${routed_session_id} returned no id"
  started_ms="$(now_ms)"
  write_artifact "remote-session-${session_id}-permission-bash-resolve.json" "$(resolve_session_permission "${routed_session_id}" "${bash_permission_id}" "approve" "ok")"
  duration_ms=$(( $(now_ms) - started_ms ))
  write_timing_artifact "remote-session-${session_id}-permission-bash-resolve-timing.json" "routed_resolve_bash_permission" "${duration_ms}" "${timing_extra_json}"
  started_ms="$(now_ms)"
  write_artifact "remote-session-${session_id}-message-bash-output.json" "$(wait_for_message_content "${routed_session_id}" "${bash_expected}")"
  duration_ms=$(( $(now_ms) - started_ms ))
  write_timing_artifact "remote-session-${session_id}-message-bash-output-timing.json" "routed_wait_bash_message" "${duration_ms}" "${timing_extra_json}"
  duration_ms=$(( $(now_ms) - bash_total_started_ms ))
  write_timing_artifact "remote-session-${session_id}-bash-total-timing.json" "routed_bash_total_host_to_remote_response" "${duration_ms}" "${timing_extra_json}"
}

approve_remote_session() {
  local session_id="${1:-}"
  local session_name="${2:-}"
  local ssh_target="${3:-}"
  local response
  local started_ms duration_ms timing_extra_json
  started_ms="$(now_ms)"
  response="$(api_post "/v1/deploy/remote/session/${session_id}/approve")"
  duration_ms=$(( $(now_ms) - started_ms ))
  write_artifact "remote-session-${session_id}-approve.json" "${response}"
  timing_extra_json="$(jq -nc --arg session_id "${session_id}" --arg session_name "${session_name}" --arg ssh_target "${ssh_target}" '{session_id:$session_id,session_name:$session_name,ssh_target:$ssh_target}')"
  write_timing_artifact "remote-session-${session_id}-approve-timing.json" "remote_session_approve" "${duration_ms}" "${timing_extra_json}"
}

wait_for_remote_attach() {
  local start_ts
  start_ts="$(date +%s)"
  declare -gA LAST_PRINTED_AUTH_URLS=()
  while :; do
    if ! try_refresh_remote_sessions; then
      if (( "$(date +%s)" - start_ts >= POLL_TIMEOUT_SECONDS )); then
        fail "timed out waiting for remote sessions to attach after ${POLL_TIMEOUT_SECONDS}s"
      fi
      sleep "${POLL_INTERVAL_SECONDS}"
      continue
    fi
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
        if [[ -n "${SESSION_WAIT_START_MS[${session_id}]:-}" && -z "${SESSION_AUTH_TIMING_RECORDED[${session_id}]:-}" ]]; then
          local wait_started_ms auth_wait_duration_ms timing_extra_json
          wait_started_ms="${SESSION_WAIT_START_MS[${session_id}]}"
          auth_wait_duration_ms=$(( $(now_ms) - wait_started_ms ))
          timing_extra_json="$(jq -nc --arg session_id "${session_id}" --arg session_name "${session_name}" --arg ssh_target "${target}" '{session_id:$session_id,session_name:$session_name,ssh_target:$ssh_target}')"
          write_timing_artifact "remote-session-${session_id}-auth-url-timing.json" "remote_session_wait_for_auth_url" "${auth_wait_duration_ms}" "${timing_extra_json}"
          SESSION_AUTH_TIMING_RECORDED["${session_id}"]="1"
        fi
      fi

      case "${status}" in
        attached)
          if [[ -n "${SESSION_WAIT_START_MS[${session_id}]:-}" && -z "${SESSION_ATTACHED_TIMING_RECORDED[${session_id}]:-}" ]]; then
            local wait_started_ms attach_wait_duration_ms timing_extra_json
            wait_started_ms="${SESSION_WAIT_START_MS[${session_id}]}"
            attach_wait_duration_ms=$(( $(now_ms) - wait_started_ms ))
            timing_extra_json="$(jq -nc --arg session_id "${session_id}" --arg session_name "${session_name}" --arg ssh_target "${target}" '{session_id:$session_id,session_name:$session_name,ssh_target:$ssh_target}')"
            write_timing_artifact "remote-session-${session_id}-attached-timing.json" "remote_session_wait_for_attached" "${attach_wait_duration_ms}" "${timing_extra_json}"
            SESSION_ATTACHED_TIMING_RECORDED["${session_id}"]="1"
          fi
          ;;
        failed)
          fail "remote session ${session_name} (${target}) failed: ${last_error:-unknown error}"
          ;;
        *)
          pending_count=$((pending_count + 1))
          if [[ -n "${enrollment_id}" && "${AUTO_APPROVE}" == "true" ]]; then
            log "Approving remote session ${session_name} (${target}) enrollment ${enrollment_id}"
            approve_remote_session "${session_id}" "${session_name}" "${target}"
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
    --arg host_callback_url "${HOST_CALLBACK_URL}" \
    --arg host_lane "${HOST_LANE}" \
    --arg transport_mode "${TRANSPORT_MODE}" \
    --arg host_tailscale_url "${HOST_TAILSCALE_URL}" \
    --arg image_delivery_mode "${IMAGE_DELIVERY_MODE}" \
    --arg group_id "${TARGET_GROUP_ID}" \
    --arg group_name "${TARGET_GROUP_NAME}" \
    --arg workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg workspace_name "${WORKSPACE_NAME}" \
    --argjson sessions "${REMOTE_SESSIONS_JSON}" \
    '{host_root:$host_root,host_api_url:$host_api_url,host_desktop_url:$host_desktop_url,host_callback_url:$host_callback_url,host_lane:$host_lane,transport_mode:$transport_mode,image_delivery_mode:$image_delivery_mode,host_tailscale_url:$host_tailscale_url,group_id:$group_id,group_name:$group_name,workspace_path:$workspace_path,workspace_name:$workspace_name,sessions:($sessions.sessions // [])}')"
  write_artifact "summary.json" "${summary_json}"
}

cleanup_remote_sessions() {
  if [[ -z "${REMOTE_SESSIONS_JSON:-}" ]]; then
    refresh_remote_sessions || true
  fi
  if (( ${#SESSION_IDS[@]} == 0 )); then
    populate_session_arrays_from_remote_sessions
  fi
  local idx session_id target session_json remote_root remote_runtime container_name
  for idx in "${!SESSION_IDS[@]}"; do
    session_id="${SESSION_IDS[$idx]}"
    target="${SESSION_TARGETS[$idx]}"
    session_json="$(session_json_by_id "${session_id}")"
    [[ -n "${session_json}" ]] || continue
    remote_root="$(printf '%s' "${session_json}" | jq -r '.preflight.remote_root // empty')"
    remote_runtime="$(printf '%s' "${session_json}" | jq -r '.preflight.remote_runtime // empty')"
    [[ -n "${remote_runtime}" ]] || remote_runtime="docker"
    container_name="swarm-remote-child-${session_id}"
    log "Cleaning remote session ${session_id} on ${target}"
    ssh "${target}" "bash -lc $(printf '%q' "set -euo pipefail
if [ '${remote_runtime}' = 'podman' ]; then
  sudo podman rm -f '${container_name}' >/dev/null 2>&1 || true
else
  sudo docker rm -f '${container_name}' >/dev/null 2>&1 || true
fi
if [ -n '${remote_root}' ]; then
  rm -rf '${remote_root}'
fi
")" >/dev/null 2>&1 || true
  done
}

print_next_steps() {
  local idx session_id session_name target session_json status auth_url remote_endpoint tailnet_url child_swarm_id transport_mode
  refresh_remote_sessions
  log ""
  log "Remote deploy harness summary:"
  log "  host root: ${HOST_ROOT}"
  log "  artifacts: ${ARTIFACT_DIR}"
  log "  host api: ${HOST_ADMIN_API_URL}"
  log "  host desktop: ${HOST_DESKTOP_URL}"
  log "  host lane: ${HOST_LANE}"
  log "  transport mode: ${TRANSPORT_MODE}"
  log "  image delivery mode: ${IMAGE_DELIVERY_MODE}"
  log "  host callback: ${HOST_CALLBACK_URL}"
  log "  timings: ${ARTIFACT_DIR}/timings.ndjson"
  [[ -n "${HOST_TAILSCALE_URL}" ]] && log "  host tailscale url: ${HOST_TAILSCALE_URL}"
  for idx in "${!SESSION_IDS[@]}"; do
    session_id="${SESSION_IDS[$idx]}"
    session_name="${SESSION_NAMES[$idx]}"
    target="${SESSION_TARGETS[$idx]}"
    session_json="$(session_json_by_id "${session_id}")"
    [[ -n "${session_json}" ]] || continue
    status="$(printf '%s' "${session_json}" | jq -r '.status // empty')"
    transport_mode="$(printf '%s' "${session_json}" | jq -r '.transport_mode // empty')"
    auth_url="$(printf '%s' "${session_json}" | jq -r '.remote_auth_url // empty')"
    remote_endpoint="$(printf '%s' "${session_json}" | jq -r '.remote_endpoint // empty')"
    tailnet_url="$(printf '%s' "${session_json}" | jq -r '.remote_tailnet_url // empty')"
    child_swarm_id="$(printf '%s' "${session_json}" | jq -r '.child_swarm_id // empty')"
    log "  - ${session_name} (${target})"
    log "    session_id: ${session_id}"
    [[ -n "${transport_mode}" ]] && log "    transport_mode: ${transport_mode}"
    log "    status: ${status:-<empty>}"
    [[ -n "${child_swarm_id}" ]] && log "    child_swarm_id: ${child_swarm_id}"
    [[ -n "${remote_endpoint}" ]] && log "    remote_endpoint: ${remote_endpoint}"
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
HOST_LANE="main"
HOST_BIND_HOST_OVERRIDE=""
HOST_BIND_HOST="127.0.0.1"
HOST_ADVERTISE_HOST_OVERRIDE=""
HOST_ADVERTISE_HOST=""
HOST_CALLBACK_URL=""
REMOTE_ADVERTISE_HOST=""
HOST_BACKEND_PORT=17781
HOST_DESKTOP_PORT=15555
HOST_PEER_PORT=17791
HOST_EFFECTIVE_BACKEND_PORT=17781
HOST_EFFECTIVE_DESKTOP_PORT=15555
HOST_TAILSCALE_URL_OVERRIDE=""
HOST_TAILSCALE_URL=""

SOURCE_WORKSPACE_PATH="${ROOT_DIR}"
WORKSPACE_NAME="$(basename "${SOURCE_WORKSPACE_PATH}")"
GROUP_ID=""
GROUP_NAME=""
REMOTE_RUNTIME="docker"
TRANSPORT_MODE="tailscale"
IMAGE_DELIVERY_MODE="registry"
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
PROOF_BASH_EXPECTED=""

SSH_TARGETS=()
SESSION_IDS=()
SESSION_NAMES=()
SESSION_TARGETS=()
EXPANDED_TARGETS=()
declare -A SESSION_WAIT_START_MS=()
declare -A SESSION_AUTH_TIMING_RECORDED=()
declare -A SESSION_ATTACHED_TIMING_RECORDED=()

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
    --transport-mode)
      shift
      [[ $# -gt 0 ]] || fail "--transport-mode requires a value"
      TRANSPORT_MODE="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
      ;;
    --remote-runtime)
      shift
      [[ $# -gt 0 ]] || fail "--remote-runtime requires a value"
      REMOTE_RUNTIME="$1"
      ;;
    --image-delivery-mode)
      shift
      [[ $# -gt 0 ]] || fail "--image-delivery-mode requires a value"
      IMAGE_DELIVERY_MODE="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')"
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
    --host-lane)
      shift
      [[ $# -gt 0 ]] || fail "--host-lane requires a value"
      HOST_LANE="$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]' | tr -d '[:space:]')"
      ;;
    --host-root)
      shift
      [[ $# -gt 0 ]] || fail "--host-root requires a value"
      HOST_ROOT_OVERRIDE="$1"
      ;;
    --host-bind-host)
      shift
      [[ $# -gt 0 ]] || fail "--host-bind-host requires a value"
      HOST_BIND_HOST_OVERRIDE="$1"
      ;;
    --host-advertise-host)
      shift
      [[ $# -gt 0 ]] || fail "--host-advertise-host requires a value"
      HOST_ADVERTISE_HOST_OVERRIDE="$1"
      ;;
    --remote-advertise-host)
      shift
      [[ $# -gt 0 ]] || fail "--remote-advertise-host requires a value"
      REMOTE_ADVERTISE_HOST="$1"
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
    --proof-bash-expected)
      shift
      [[ $# -gt 0 ]] || fail "--proof-bash-expected requires a value"
      PROOF_BASH_EXPECTED="$1"
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

case "${TRANSPORT_MODE}" in
  tailscale|lan) ;;
  *) fail "--transport-mode must be tailscale or lan" ;;
esac

case "${IMAGE_DELIVERY_MODE}" in
  registry|archive) ;;
  *) fail "--image-delivery-mode must be registry or archive" ;;
esac

case "${HOST_LANE}" in
  main|dev) ;;
  *) fail "--host-lane must be main or dev" ;;
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

if [[ "${TRANSPORT_MODE}" == "tailscale" && "${TAILSCALE_AUTH_MODE}" == "key" ]]; then
  [[ -n "${TAILSCALE_AUTH_KEY_ENV}" ]] || fail "--tailscale-auth-key-env is required with --tailscale-auth-mode key"
  [[ -n "${!TAILSCALE_AUTH_KEY_ENV:-}" ]] || fail "environment variable ${TAILSCALE_AUTH_KEY_ENV} is required for --tailscale-auth-mode key"
fi

case "${MANAGE_TAILSCALE_SERVE}" in
  true|false) ;;
  *) fail "--manage-tailscale-serve must be true or false" ;;
esac

if [[ "${TRANSPORT_MODE}" == "tailscale" ]]; then
  require_command tailscale
fi

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

resolve_host_transport
prepare_isolated_host
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
