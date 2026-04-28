#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"

usage() {
  cat <<'EOF'
Usage: ./tests/swarmd/live_prod_update_e2e.sh [options]

Run a live production update/local-container lifecycle inside the harness VM:
  1. install the source Swarm release from GitHub into an isolated XDG root
  2. start that installed production Swarm
  3. create and attach a real local child container from the source release
  4. verify GitHub says the target release is the production update
  5. run the installed production update path
  6. verify the runtime and seeded local container are updated to the target release

Options:
  --source-version <tag>        Source GitHub release to install. Default: v0.1.17
  --target-version <tag>        Expected next GitHub release. Default: v0.1.18
  --runtime <podman|docker>     Explicit local container runtime. Default: host recommendation
  --host-root <path>            Isolated XDG root. Default: mktemp under /tmp
  --host-port <port>            Host backend/API port. Default: 7781
  --host-desktop-port <port>    Host desktop port. Default: 5555
  --peer-transport-port <port>  Host peer transport port. Default: 7791
  --swarm-name <name>           Child container Swarm name. Default: live-prod-update-child-<timestamp>
  --group-name <name>           Group name. Default: live-prod-update-group-<timestamp>
  --group-network-name <name>   Explicit group network name. Default: derived by backend
  --poll-timeout <seconds>      Overall update verification timeout. Default: 900
  --poll-interval <seconds>     API/container polling interval. Default: 2
  --ready-poll-interval <secs>  readyz polling interval during backend restart. Default: 0.25
  --log-tail <lines>            Log tail lines captured on failure/success. Default: 240
  --help                        Show this help text

This harness intentionally uses GitHub release install/update paths only. It fails
if SWARM_UPDATE_* or SWARM_PRODUCTION_IMAGE_METADATA_* is present in the incoming
environment because those can override or contaminate the live production path.
EOF
}

log() {
  printf '%s\n' "$*"
}

fail() {
  capture_context "$*" || true
  printf 'error: %s\n' "$*" >&2
  exit 1
}

require_harness_vm_guest() {
  [[ "${SWARM_HARNESS_VM_GUEST:-}" == "1" ]] || fail "live production update harness must be run via scripts/swarm-harness-vm.sh live-prod-update inside the harness VM"
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

now_ms() {
  date +%s%3N
}

ms_diff() {
  local start="${1:-}" end="${2:-}"
  if [[ -z "${start}" || -z "${end}" ]]; then
    printf 'not-observed'
    return 0
  fi
  awk -v start="${start}" -v end="${end}" 'BEGIN { printf "%.3f", (end - start) / 1000 }'
}

write_artifact() {
  local name="${1:-}" content="${2-}"
  [[ -n "${ARTIFACT_DIR:-}" ]] || return 0
  mkdir -p "${ARTIFACT_DIR}"
  printf '%s' "${content}" >"${ARTIFACT_DIR}/${name}"
}

safe_copy_file_artifact() {
  local source="${1:-}" name="${2:-}"
  [[ -n "${ARTIFACT_DIR:-}" && -f "${source}" ]] || return 0
  cp -- "${source}" "${ARTIFACT_DIR}/${name}"
}

capture_context() {
  local reason="${1:-failure}"
  if [[ "${FAILURE_CONTEXT_CAPTURED:-false}" == "true" ]]; then
    return 0
  fi
  FAILURE_CONTEXT_CAPTURED="true"
  [[ -n "${ARTIFACT_DIR:-}" ]] || return 0
  mkdir -p "${ARTIFACT_DIR}"
  write_artifact "failure-reason.txt" "${reason}\n"
  if [[ -n "${HOST_ADMIN_API_URL:-}" ]]; then
    local body_file code
    body_file="$(mktemp)"
    if code="$(curl -sS --connect-timeout 2 --max-time 8 -o "${body_file}" -w '%{http_code}' "${HOST_ADMIN_API_URL%/}/readyz" 2>/dev/null)"; then
      :
    else
      code="000"
    fi
    write_artifact "failure-readyz-code.txt" "${code}\n"
    rm -f -- "${body_file}"
  fi
  if [[ -n "${HOST_LOG_FILE:-}" && -f "${HOST_LOG_FILE}" ]]; then
    tail -n "${LOG_TAIL:-240}" "${HOST_LOG_FILE}" >"${ARTIFACT_DIR}/host-log-tail.txt" || true
  fi
  if [[ -n "${APPLY_LOG_FILE:-}" && -f "${APPLY_LOG_FILE}" ]]; then
    tail -n "${LOG_TAIL:-240}" "${APPLY_LOG_FILE}" >"${ARTIFACT_DIR}/apply-log-tail.txt" || true
  fi
  if [[ -n "${RUNTIME:-}" && -n "${CONTAINER_NAME:-}" ]] && command -v "${RUNTIME}" >/dev/null 2>&1; then
    "${RUNTIME}" logs --tail "${LOG_TAIL:-240}" "${CONTAINER_NAME}" >"${ARTIFACT_DIR}/child-container-log-tail.txt" 2>&1 || true
  fi
}

cleanup() {
  if [[ -n "${APPLY_PID:-}" ]] && kill -0 "${APPLY_PID}" >/dev/null 2>&1; then
    kill "${APPLY_PID}" >/dev/null 2>&1 || true
    wait "${APPLY_PID}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${HOST_DESKTOP_SESSION_COOKIE_FILE:-}" && -f "${HOST_DESKTOP_SESSION_COOKIE_FILE}" ]]; then
    rm -f -- "${HOST_DESKTOP_SESSION_COOKIE_FILE}"
  fi
}

trap cleanup EXIT

forbidden_env_present() {
  env | awk -F= '/^SWARM_UPDATE_/ || /^SWARM_PRODUCTION_IMAGE_METADATA_/ { print $1 }'
}

ensure_no_live_update_overrides() {
  local forbidden
  forbidden="$(forbidden_env_present)"
  if [[ -n "${forbidden}" ]]; then
    printf '%s\n' "${forbidden}" >&2
    fail "live production update harness refuses SWARM_UPDATE_* or SWARM_PRODUCTION_IMAGE_METADATA_* environment variables"
  fi
}

port_is_available() {
  local port="${1:-0}"
  if command -v ss >/dev/null 2>&1; then
    if ss -ltn "( sport = :${port} )" 2>/dev/null | awk 'NR > 1 { found = 1 } END { exit(found ? 0 : 1) }'; then
      return 1
    fi
    return 0
  fi
  fail "unable to check local port availability because ss is not installed"
}

curl_http_code() {
  local url="${1:-}"
  local body_file http_code
  body_file="$(mktemp)"
  if http_code="$(curl -sS --connect-timeout 2 --max-time 8 -o "${body_file}" -w '%{http_code}' "${url}" 2>/dev/null)"; then
    :
  else
    http_code="000"
  fi
  rm -f -- "${body_file}"
  printf '%s' "${http_code}"
}

json_request_capture() {
  local method="${1:-GET}" path="${2:-}" body="${3:-}" max_time="${4:-30}"
  local url="${HOST_ADMIN_API_URL%/}${path}"
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
    payload_file="$(mktemp)"
    printf '%s' "${body}" >"${payload_file}"
    args+=(-H 'Content-Type: application/json' --data-binary "@${payload_file}")
  fi
  if http_code="$(curl "${args[@]}" "${url}")"; then
    :
  else
    http_code="000"
  fi
  JSON_REQUEST_STATUS="${http_code}"
  JSON_REQUEST_BODY="$(cat -- "${response_file}")"
  rm -f -- "${response_file}"
  if [[ -n "${payload_file}" ]]; then
    rm -f -- "${payload_file}"
  fi
}

api_request() {
  local method="${1:-GET}" path="${2:-}" body="${3:-}" max_time="${4:-30}"
  json_request_capture "${method}" "${path}" "${body}" "${max_time}"
  if [[ "${JSON_REQUEST_STATUS}" != 2* ]]; then
    fail "${method} ${path} failed with status ${JSON_REQUEST_STATUS}: ${JSON_REQUEST_BODY}"
  fi
  printf '%s' "${JSON_REQUEST_BODY}"
}

api_get() {
  api_request GET "$1" "" "${2:-30}"
}

api_post() {
  api_request POST "$1" "${2:-}" "${3:-30}"
}

host_env_init() {
  HOST_ENV=(env -i)
  HOST_ENV+=("HOME=${HOME}")
  HOST_ENV+=("USER=${USER:-$(id -un)}")
  HOST_ENV+=("LOGNAME=${LOGNAME:-${USER:-$(id -un)}}")
  HOST_ENV+=("PATH=${HOST_XDG_BIN_HOME}:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
  HOST_ENV+=("XDG_BIN_HOME=${HOST_XDG_BIN_HOME}")
  HOST_ENV+=("XDG_CONFIG_HOME=${HOST_XDG_CONFIG_HOME}")
  HOST_ENV+=("XDG_DATA_HOME=${HOST_XDG_DATA_HOME}")
  HOST_ENV+=("XDG_STATE_HOME=${HOST_XDG_STATE_HOME}")
  HOST_ENV+=("XDG_CACHE_HOME=${HOST_XDG_CACHE_HOME}")
  HOST_ENV+=("SWARM_LANE=main")
  if [[ -n "${SHELL:-}" ]]; then HOST_ENV+=("SHELL=${SHELL}"); fi
  if [[ -n "${TERM:-}" ]]; then HOST_ENV+=("TERM=${TERM}"); fi
  if [[ -n "${LANG:-}" ]]; then HOST_ENV+=("LANG=${LANG}"); fi
  if [[ -n "${LC_ALL:-}" ]]; then HOST_ENV+=("LC_ALL=${LC_ALL}"); fi
  if [[ -n "${XDG_RUNTIME_DIR:-}" ]]; then HOST_ENV+=("XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR}"); fi
  if [[ -n "${DBUS_SESSION_BUS_ADDRESS:-}" ]]; then HOST_ENV+=("DBUS_SESSION_BUS_ADDRESS=${DBUS_SESSION_BUS_ADDRESS}"); fi
  if [[ -n "${DOCKER_HOST:-}" ]]; then HOST_ENV+=("DOCKER_HOST=${DOCKER_HOST}"); fi
  if [[ -n "${CONTAINER_HOST:-}" ]]; then HOST_ENV+=("CONTAINER_HOST=${CONTAINER_HOST}"); fi
  if [[ -n "${HTTP_PROXY:-}" ]]; then HOST_ENV+=("HTTP_PROXY=${HTTP_PROXY}"); fi
  if [[ -n "${HTTPS_PROXY:-}" ]]; then HOST_ENV+=("HTTPS_PROXY=${HTTPS_PROXY}"); fi
  if [[ -n "${NO_PROXY:-}" ]]; then HOST_ENV+=("NO_PROXY=${NO_PROXY}"); fi
  if [[ -n "${http_proxy:-}" ]]; then HOST_ENV+=("http_proxy=${http_proxy}"); fi
  if [[ -n "${https_proxy:-}" ]]; then HOST_ENV+=("https_proxy=${https_proxy}"); fi
  if [[ -n "${no_proxy:-}" ]]; then HOST_ENV+=("no_proxy=${no_proxy}"); fi
  HOST_ENV+=("SWARM_SHARED_RUNTIME_ROOT=${HOST_XDG_DATA_HOME}/swarm")
}

run_host_env() {
  (
    cd "${HOST_ROOT:-/}"
    exec "${HOST_ENV[@]}" "$@"
  )
}

prepare_isolated_root() {
  if [[ -n "${HOST_ROOT_OVERRIDE}" ]]; then
    HOST_ROOT="${HOST_ROOT_OVERRIDE}"
    if [[ -e "${HOST_ROOT}" ]]; then
      [[ -d "${HOST_ROOT}" ]] || fail "--host-root exists but is not a directory: ${HOST_ROOT}"
      if find "${HOST_ROOT}" -mindepth 1 -maxdepth 1 | grep -q .; then
        fail "--host-root must be empty for a clean production lifecycle run: ${HOST_ROOT}"
      fi
    fi
    mkdir -p "${HOST_ROOT}"
  else
    HOST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/swarm-live-prod-update-XXXXXX")"
  fi
  HOST_ROOT="$(cd "${HOST_ROOT}" && pwd)"
  HOST_XDG_BIN_HOME="${HOST_ROOT}/xdg/bin"
  HOST_XDG_CONFIG_HOME="${HOST_ROOT}/xdg/config"
  HOST_XDG_DATA_HOME="${HOST_ROOT}/xdg/data"
  HOST_XDG_STATE_HOME="${HOST_ROOT}/xdg/state"
  HOST_XDG_CACHE_HOME="${HOST_ROOT}/xdg/cache"
  ARTIFACT_DIR="${HOST_ROOT}/artifacts"
  mkdir -p "${HOST_XDG_BIN_HOME}" "${HOST_XDG_CONFIG_HOME}" "${HOST_XDG_DATA_HOME}" "${HOST_XDG_STATE_HOME}" "${HOST_XDG_CACHE_HOME}" "${ARTIFACT_DIR}"
  HOST_STARTUP_CONFIG="${HOST_XDG_CONFIG_HOME}/swarm/swarm.conf"
  HOST_ADMIN_API_URL="http://127.0.0.1:${HOST_BACKEND_PORT}"
  HOST_DESKTOP_URL="http://127.0.0.1:${HOST_DESKTOP_PORT}"
  HOST_LOG_FILE="${HOST_XDG_STATE_HOME}/swarm/swarmd/main/swarmd.log"
  HOST_DESKTOP_SESSION_COOKIE_FILE=""
  host_env_init
}

write_startup_config() {
  mkdir -p "$(dirname -- "${HOST_STARTUP_CONFIG}")"
  cat >"${HOST_STARTUP_CONFIG}" <<EOF
startup_mode = box
dev_mode = false
dev_root =
host = 127.0.0.1
port = ${HOST_BACKEND_PORT}
advertise_host =
advertise_port = ${HOST_BACKEND_PORT}
desktop_port = ${HOST_DESKTOP_PORT}
bypass_permissions = true
retain_tool_output_history = false
swarm_name = Live Production Update Host
swarm_mode = true
child = false
mode = lan
tailscale_url =
peer_transport_port = ${HOST_PEER_TRANSPORT_PORT}
parent_swarm_id =
pairing_state =
deploy_container_enabled = false
deploy_container_host_driven = false
deploy_container_sync_enabled = false
deploy_container_sync_mode =
deploy_container_sync_modules =
deploy_container_sync_owner_swarm_id =
deploy_container_sync_credential_url =
deploy_container_sync_agent_url =
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
remote_deploy_sync_enabled = false
remote_deploy_sync_mode =
remote_deploy_sync_owner_swarm_id =
remote_deploy_sync_credential_url =
EOF
  safe_copy_file_artifact "${HOST_STARTUP_CONFIG}" "startup-config.txt"
}

install_source_release() {
  local installer_url installer_path
  installer_url="https://raw.githubusercontent.com/swarm-agent/swarm/${SOURCE_VERSION}/install.sh"
  installer_path="${ARTIFACT_DIR}/install-${SOURCE_VERSION}.sh"
  log "Downloading installer from ${installer_url}"
  curl -fsSL "${installer_url}" -o "${installer_path}"
  log "Installing Swarm ${SOURCE_VERSION} from GitHub release assets"
  if ! cat "${installer_path}" | run_host_env sh -s -- --version "${SOURCE_VERSION}" >"${ARTIFACT_DIR}/install.log" 2>&1; then
    fail "GitHub release install failed; see ${ARTIFACT_DIR}/install.log"
  fi
}

runtime_version() {
  local current_root build_info version_file
  current_root="${HOST_XDG_DATA_HOME}/swarm/current"
  build_info="${current_root}/build-info.txt"
  version_file="${current_root}/.version"
  if [[ -f "${build_info}" ]]; then
    sed -n 's/^version=//p' "${build_info}" | head -n 1
    return 0
  fi
  if [[ -f "${version_file}" ]]; then
    tr -d '\n' <"${version_file}"
    return 0
  fi
  return 1
}

verify_runtime_version() {
  local expected="${1:-}" label="${2:-runtime}" actual
  actual="$(runtime_version || true)"
  write_artifact "runtime-version-${label}.txt" "${actual}\n"
  [[ "${actual}" == "${expected}" ]] || fail "${label} runtime version is ${actual:-<empty>}, expected ${expected}"
}

start_host() {
  log "Starting installed production Swarm ${SOURCE_VERSION} at ${HOST_ADMIN_API_URL}"
  if ! run_host_env "${HOST_XDG_BIN_HOME}/swarm" main server on >"${ARTIFACT_DIR}/server-start.log" 2>&1; then
    fail "installed production Swarm did not start; see ${ARTIFACT_DIR}/server-start.log"
  fi
  wait_for_ready "${HOST_ADMIN_API_URL}" "host" "60"
}

wait_for_ready() {
  local base_url="${1:-}" label="${2:-service}" timeout="${3:-60}"
  local start_ts code
  start_ts="$(date +%s)"
  while :; do
    code="$(curl_http_code "${base_url%/}/readyz")"
    if [[ "${code}" == "200" ]]; then
      return 0
    fi
    if (( "$(date +%s)" - start_ts >= timeout )); then
      fail "${label} did not become ready at ${base_url} (last readyz=${code})"
    fi
    sleep 1
  done
}

fetch_desktop_session() {
  if [[ -n "${HOST_DESKTOP_SESSION_COOKIE_FILE:-}" && -f "${HOST_DESKTOP_SESSION_COOKIE_FILE}" ]]; then
    rm -f -- "${HOST_DESKTOP_SESSION_COOKIE_FILE}"
  fi
  HOST_DESKTOP_SESSION_COOKIE_FILE="$(mktemp)"
  curl -fsS \
    -c "${HOST_DESKTOP_SESSION_COOKIE_FILE}" \
    -b "${HOST_DESKTOP_SESSION_COOKIE_FILE}" \
    -H "Origin: ${HOST_ADMIN_API_URL%/}" \
    -H "Referer: ${HOST_ADMIN_API_URL%/}/" \
    -H 'Sec-Fetch-Site: same-origin' \
    "${HOST_ADMIN_API_URL%/}/v1/auth/desktop/session" >/dev/null
}

resolve_runtime() {
  local runtime_status_json
  runtime_status_json="$(api_get '/v1/deploy/container/runtime')"
  write_artifact "runtime-status-before.json" "${runtime_status_json}"
  if [[ -z "${RUNTIME}" ]]; then
    RUNTIME="$(printf '%s' "${runtime_status_json}" | jq -r '.runtime.recommended // empty')"
  fi
  if [[ -z "${RUNTIME}" ]]; then
    RUNTIME="$(printf '%s' "${runtime_status_json}" | jq -r '.runtime.available[0] // empty')"
  fi
  [[ -n "${RUNTIME}" ]] || fail "no supported local container runtime is available"
  require_command "${RUNTIME}"
}

ensure_host_state() {
  local state_json role swarm_id
  state_json="$(api_get '/v1/swarm/state')"
  write_artifact "host-state-initial.json" "${state_json}"
  role="$(printf '%s' "${state_json}" | jq -r '.state.node.role // empty')"
  swarm_id="$(printf '%s' "${state_json}" | jq -r '.state.node.swarm_id // empty')"
  [[ "${role}" == "master" ]] || fail "production host is not a master swarm (role=${role:-<empty>})"
  [[ -n "${swarm_id}" ]] || fail "production host is missing a local swarm id"
}

ensure_target_group() {
  local payload response
  if [[ -z "${GROUP_NAME}" ]]; then
    GROUP_NAME="live-prod-update-group-$(date +%Y%m%d-%H%M%S)"
  fi
  payload="$(jq -nc \
    --arg name "${GROUP_NAME}" \
    --arg network_name "${GROUP_NETWORK_NAME}" \
    '{name:$name,network_name:$network_name,set_current:true}')"
  response="$(api_post '/v1/swarm/groups/upsert' "${payload}")"
  write_artifact "group-upsert-response.json" "${response}"
  TARGET_GROUP_ID="$(printf '%s' "${response}" | jq -r '.group.id // empty')"
  TARGET_GROUP_NAME="$(printf '%s' "${response}" | jq -r '.group.name // empty')"
  TARGET_GROUP_NETWORK_NAME="$(printf '%s' "${response}" | jq -r '.group.networkName // empty')"
  [[ -n "${TARGET_GROUP_ID}" ]] || fail "failed to create or resolve target group"
}

create_seed_local_container() {
  local payload response
  if [[ -z "${CHILD_SWARM_NAME}" ]]; then
    CHILD_SWARM_NAME="live-prod-update-child-$(date +%Y%m%d-%H%M%S)"
  fi
  log "Creating production local child container ${CHILD_SWARM_NAME} with ${RUNTIME}"
  payload="$(jq -nc \
    --arg name "${CHILD_SWARM_NAME}" \
    --arg runtime "${RUNTIME}" \
    --arg group_id "${TARGET_GROUP_ID}" \
    --arg group_name "${TARGET_GROUP_NAME}" \
    --arg group_network_name "${TARGET_GROUP_NETWORK_NAME}" \
    '{name:$name,runtime:$runtime,group_id:$group_id,group_name:$group_name,group_network_name:$group_network_name,sync_enabled:false,bypass_permissions:true,mounts:[]}')"
  write_artifact "deploy-create-request.redacted.json" "${payload}"
  response="$(api_post '/v1/deploy/container/create' "${payload}" "600")"
  write_artifact "deploy-create-response.json" "${response}"
  DEPLOYMENT_ID="$(printf '%s' "${response}" | jq -r '.deployment.id // empty')"
  CONTAINER_NAME="$(printf '%s' "${response}" | jq -r '.deployment.container_name // empty')"
  CHILD_BACKEND_URL="$(printf '%s' "${response}" | jq -r '.deployment.child_backend_url // empty')"
  [[ -n "${DEPLOYMENT_ID}" ]] || fail "deploy container create response missing deployment id"
  [[ -n "${CONTAINER_NAME}" ]] || fail "deploy container create response missing container name"
  wait_for_deployment_attached
  wait_for_ready "${CHILD_BACKEND_URL}" "child container" "120"
  capture_seed_container_state
}

wait_for_deployment_attached() {
  local start_ts deployments_json deployment_json attach_status last_error backend_port
  start_ts="$(date +%s)"
  while :; do
    deployments_json="$(api_get '/v1/deploy/container')"
    write_artifact "deployments-poll.json" "${deployments_json}"
    deployment_json="$(printf '%s' "${deployments_json}" | jq -c --arg deployment_id "${DEPLOYMENT_ID}" '.deployments[] | select(.id == $deployment_id)' | head -n 1)"
    [[ -n "${deployment_json}" ]] || fail "deployment ${DEPLOYMENT_ID} disappeared"
    attach_status="$(printf '%s' "${deployment_json}" | jq -r '.attach_status // empty')"
    last_error="$(printf '%s' "${deployment_json}" | jq -r '.last_attach_error // empty')"
    log "Attach poll deployment=${DEPLOYMENT_ID} attach_status=${attach_status:-<empty>} error=${last_error:-<empty>}"
    if [[ "${attach_status}" == "attached" ]]; then
      DEPLOYMENT_JSON="${deployment_json}"
      CONTAINER_NAME="$(printf '%s' "${deployment_json}" | jq -r '.container_name // empty')"
      CHILD_BACKEND_URL="$(printf '%s' "${deployment_json}" | jq -r '.child_backend_url // empty')"
      if [[ -z "${CHILD_BACKEND_URL}" ]]; then
        backend_port="$(printf '%s' "${deployment_json}" | jq -r '.backend_host_port // 0')"
        CHILD_BACKEND_URL="http://127.0.0.1:${backend_port}"
      fi
      write_artifact "deployment-attached.json" "${deployment_json}"
      return 0
    fi
    if [[ "${attach_status}" == "failed" || "${attach_status}" == "rejected" ]]; then
      fail "deployment ${DEPLOYMENT_ID} attach failed: ${last_error:-unknown error}"
    fi
    if (( "$(date +%s)" - start_ts >= POLL_TIMEOUT_SECONDS )); then
      fail "timed out waiting for deployment ${DEPLOYMENT_ID} to attach"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

capture_seed_container_state() {
  local containers_json local_json image
  containers_json="$(api_get '/v1/swarm/containers/local')"
  write_artifact "local-containers-before-update.json" "${containers_json}"
  local_json="$(printf '%s' "${containers_json}" | jq -c --arg container_name "${CONTAINER_NAME}" '.containers[] | select(.container_name == $container_name or .id == $container_name)' | head -n 1)"
  [[ -n "${local_json}" ]] || fail "local container record missing for ${CONTAINER_NAME}"
  LOCAL_CONTAINER_ID="$(printf '%s' "${local_json}" | jq -r '.id // empty')"
  SEED_CONTAINER_IMAGE="$(printf '%s' "${local_json}" | jq -r '.image // empty')"
  write_artifact "seed-local-container.json" "${local_json}"
  image="${SEED_CONTAINER_IMAGE}"
  [[ -n "${image}" ]] || fail "seed local container has no recorded image"
  log "Seed local container ${LOCAL_CONTAINER_ID} image ${image}"
}

verify_update_status_before() {
  local status_json
  status_json="$(api_get '/v1/update/status' "60")"
  write_artifact "update-status-before.json" "${status_json}"
  printf '%s' "${status_json}" | jq -e \
    --arg source "${SOURCE_VERSION}" \
    --arg target "${TARGET_VERSION}" \
    '.current_version == $source and .latest_version == $target and .update_available == true and .dev_mode == false and (.error // "") == "" and (.suppressed // false) == false' >/dev/null \
    || fail "/v1/update/status did not report current=${SOURCE_VERSION}, latest=${TARGET_VERSION}, update_available=true, dev_mode=false"
}

verify_local_container_update_plan_before() {
  local plan_json
  plan_json="$(api_get "/v1/update/local-containers?dev_mode=false&target_version=${TARGET_VERSION}" "120")"
  write_artifact "local-container-update-plan-before.json" "${plan_json}"
  printf '%s' "${plan_json}" | jq -e \
    --arg target "${TARGET_VERSION}" \
    --arg container_id "${LOCAL_CONTAINER_ID}" \
    '.mode == "release" and .dev_mode == false and .target.version == $target and (.target.image_ref // "") != "" and (.target.digest_ref // "") != "" and (.summary.total // 0) >= 1 and (.summary.needs_update // 0) >= 1 and ([.containers[] | select(.id == $container_id and .state == "needs-update")] | length) == 1' >/dev/null \
    || fail "local container update preflight did not show seeded container ${LOCAL_CONTAINER_ID} needs production update to ${TARGET_VERSION}"
}

start_apply_update() {
  APPLY_LOG_FILE="${ARTIFACT_DIR}/swarm-update-apply.log"
  APPLY_START_MS="$(now_ms)"
  log "Starting installed production update from ${SOURCE_VERSION} to ${TARGET_VERSION}"
  (
    cd "${HOST_ROOT}"
    exec "${HOST_ENV[@]}" "${HOST_XDG_BIN_HOME}/swarm" main update apply
  ) < /dev/null >"${APPLY_LOG_FILE}" 2>&1 &
  APPLY_PID="$!"
  write_artifact "apply-pid.txt" "${APPLY_PID}\n"
}

poll_backend_restart_window() {
  local start_ts code now
  start_ts="$(date +%s)"
  BACKEND_DOWN_MS=""
  BACKEND_READY_MS=""
  while :; do
    code="$(curl_http_code "${HOST_ADMIN_API_URL%/}/readyz")"
    now="$(now_ms)"
    if [[ -z "${BACKEND_DOWN_MS}" && "${code}" != "200" ]]; then
      BACKEND_DOWN_MS="${now}"
      write_artifact "backend-down-ms.txt" "${BACKEND_DOWN_MS}\n"
      log "Observed backend down during update"
    fi
    if [[ -n "${BACKEND_DOWN_MS}" && "${code}" == "200" ]]; then
      BACKEND_READY_MS="${now}"
      write_artifact "backend-ready-ms.txt" "${BACKEND_READY_MS}\n"
      log "Observed backend ready after update restart"
      return 0
    fi
    if ! kill -0 "${APPLY_PID}" >/dev/null 2>&1 && [[ -z "${BACKEND_DOWN_MS}" ]]; then
      fail "update apply process exited before backend shutdown was observed"
    fi
    if (( "$(date +%s)" - start_ts >= POLL_TIMEOUT_SECONDS )); then
      fail "timed out waiting for backend down/up during update"
    fi
    sleep "${READY_POLL_INTERVAL_SECONDS}"
  done
}

local_container_state_for_target() {
  local plan_json
  json_request_capture GET "/v1/update/local-containers?dev_mode=false&target_version=${TARGET_VERSION}" "" "60"
  if [[ "${JSON_REQUEST_STATUS}" != 2* ]]; then
    printf 'api-error'
    return 0
  fi
  plan_json="${JSON_REQUEST_BODY}"
  write_artifact "local-container-update-plan-poll.json" "${plan_json}"
  printf '%s' "${plan_json}" | jq -r --arg container_id "${LOCAL_CONTAINER_ID}" '.containers[]? | select(.id == $container_id) | .state' | head -n 1
}

verify_final_update_state() {
  local start_ts state ready_code status_json final_version containers_json deployments_json local_json updated_image
  start_ts="$(date +%s)"
  CONTAINER_UPDATE_START_MS="${BACKEND_READY_MS}"
  CONTAINER_UPDATE_DONE_MS=""
  while :; do
    final_version="$(runtime_version || true)"
    state="$(local_container_state_for_target)"
    ready_code="$(curl_http_code "${CHILD_BACKEND_URL%/}/readyz")"
    log "Final poll runtime=${final_version:-<empty>} local_container_state=${state:-<empty>} child_readyz=${ready_code}"
    if [[ "${final_version}" == "${TARGET_VERSION}" && "${state}" == "already-current" && "${ready_code}" == "200" ]]; then
      CONTAINER_UPDATE_DONE_MS="$(now_ms)"
      write_artifact "container-update-done-ms.txt" "${CONTAINER_UPDATE_DONE_MS}\n"
      break
    fi
    if ! kill -0 "${APPLY_PID}" >/dev/null 2>&1 && [[ "${final_version}" != "${TARGET_VERSION}" ]]; then
      fail "update apply process exited before installed runtime reached ${TARGET_VERSION}"
    fi
    if (( "$(date +%s)" - start_ts >= POLL_TIMEOUT_SECONDS )); then
      fail "timed out waiting for runtime/container update to reach ${TARGET_VERSION}"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done

  fetch_desktop_session

  status_json="$(api_get '/v1/update/status' "60")"
  write_artifact "update-status-after.json" "${status_json}"
  printf '%s' "${status_json}" | jq -e \
    --arg target "${TARGET_VERSION}" \
    '.current_version == $target and .latest_version == $target and .update_available == false and .dev_mode == false and (.error // "") == ""' >/dev/null \
    || fail "post-update /v1/update/status did not report current=${TARGET_VERSION}, latest=${TARGET_VERSION}, update_available=false"

  containers_json="$(api_get '/v1/swarm/containers/local')"
  deployments_json="$(api_get '/v1/deploy/container')"
  write_artifact "local-containers-after-update.json" "${containers_json}"
  write_artifact "deployments-after-update.json" "${deployments_json}"
  local_json="$(printf '%s' "${containers_json}" | jq -c --arg container_id "${LOCAL_CONTAINER_ID}" '.containers[] | select(.id == $container_id)' | head -n 1)"
  [[ -n "${local_json}" ]] || fail "updated local container record missing for ${LOCAL_CONTAINER_ID}"
  updated_image="$(printf '%s' "${local_json}" | jq -r '.image // empty')"
  write_artifact "updated-local-container.json" "${local_json}"
  [[ -n "${updated_image}" ]] || fail "updated local container image is empty"
  [[ "${updated_image}" != "${SEED_CONTAINER_IMAGE}" ]] || fail "local container image did not change after update (${updated_image})"

  printf '%s' "${deployments_json}" | jq -e \
    --arg deployment_id "${DEPLOYMENT_ID}" \
    '.deployments[] | select(.id == $deployment_id and .attach_status == "attached")' >/dev/null \
    || fail "deployment ${DEPLOYMENT_ID} is not attached after local container update"
}

finish_apply_process() {
  APPLY_END_MS="$(now_ms)"
  APPLY_EXIT="still-running"
  if kill -0 "${APPLY_PID}" >/dev/null 2>&1; then
    log "Update command reached foreground TUI after successful verification; terminating it for harness completion"
    kill "${APPLY_PID}" >/dev/null 2>&1 || true
    wait "${APPLY_PID}" >/dev/null 2>&1 || true
    APPLY_EXIT="terminated-after-success"
  else
    if wait "${APPLY_PID}"; then
      APPLY_EXIT="0"
    else
      APPLY_EXIT="$?"
    fi
  fi
  write_artifact "apply-exit.txt" "${APPLY_EXIT}\n"
}

write_summary() {
  local total_s backend_down_s container_s final_version
  final_version="$(runtime_version || true)"
  total_s="$(ms_diff "${APPLY_START_MS}" "${APPLY_END_MS}")"
  backend_down_s="$(ms_diff "${BACKEND_DOWN_MS}" "${BACKEND_READY_MS}")"
  container_s="$(ms_diff "${CONTAINER_UPDATE_START_MS}" "${CONTAINER_UPDATE_DONE_MS}")"
  cat >"${ARTIFACT_DIR}/summary.txt" <<EOF
Live production update/local-container lifecycle complete
source_version=${SOURCE_VERSION}
target_version=${TARGET_VERSION}
final_runtime_version=${final_version}
runtime=${RUNTIME}
host_root=${HOST_ROOT}
host_api_url=${HOST_ADMIN_API_URL}
child_swarm_name=${CHILD_SWARM_NAME}
deployment_id=${DEPLOYMENT_ID}
local_container_id=${LOCAL_CONTAINER_ID}
container_name=${CONTAINER_NAME}
child_backend_url=${CHILD_BACKEND_URL}
seed_container_image=${SEED_CONTAINER_IMAGE}
apply_process=${APPLY_EXIT}
apply_total_seconds=${total_s}
backend_down_seconds=${backend_down_s}
container_update_seconds_after_backend_ready=${container_s}
artifacts=${ARTIFACT_DIR}
EOF
  log ""
  cat "${ARTIFACT_DIR}/summary.txt"
  log ""
  log "Artifacts: ${ARTIFACT_DIR}"
  log "Host log: ${HOST_LOG_FILE}"
  log "Apply log: ${APPLY_LOG_FILE}"
}

SOURCE_VERSION="v0.1.17"
TARGET_VERSION="v0.1.18"
RUNTIME="${RUNTIME:-}"
HOST_ROOT_OVERRIDE="${HOST_ROOT:-}"
HOST_BACKEND_PORT="7781"
HOST_DESKTOP_PORT="5555"
HOST_PEER_TRANSPORT_PORT="7791"
CHILD_SWARM_NAME=""
GROUP_NAME=""
GROUP_NETWORK_NAME=""
POLL_TIMEOUT_SECONDS="900"
POLL_INTERVAL_SECONDS="2"
READY_POLL_INTERVAL_SECONDS="0.25"
LOG_TAIL="240"
FAILURE_CONTEXT_CAPTURED="false"
JSON_REQUEST_STATUS=""
JSON_REQUEST_BODY=""
APPLY_PID=""
APPLY_LOG_FILE=""
ARTIFACT_DIR=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --source-version)
      SOURCE_VERSION="${2:-}"
      shift 2
      ;;
    --target-version)
      TARGET_VERSION="${2:-}"
      shift 2
      ;;
    --runtime)
      RUNTIME="${2:-}"
      shift 2
      ;;
    --host-root)
      HOST_ROOT_OVERRIDE="${2:-}"
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
    --peer-transport-port)
      HOST_PEER_TRANSPORT_PORT="${2:-}"
      shift 2
      ;;
    --swarm-name)
      CHILD_SWARM_NAME="${2:-}"
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
    --poll-timeout)
      POLL_TIMEOUT_SECONDS="${2:-}"
      shift 2
      ;;
    --poll-interval)
      POLL_INTERVAL_SECONDS="${2:-}"
      shift 2
      ;;
    --ready-poll-interval)
      READY_POLL_INTERVAL_SECONDS="${2:-}"
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

[[ -n "${SOURCE_VERSION}" ]] || fail "--source-version must not be empty"
[[ -n "${TARGET_VERSION}" ]] || fail "--target-version must not be empty"
[[ "${SOURCE_VERSION}" != "${TARGET_VERSION}" ]] || fail "source and target versions must differ"
[[ "${HOST_BACKEND_PORT}" =~ ^[0-9]+$ ]] || fail "--host-port must be a positive integer"
[[ "${HOST_DESKTOP_PORT}" =~ ^[0-9]+$ ]] || fail "--host-desktop-port must be a positive integer"
[[ "${HOST_PEER_TRANSPORT_PORT}" =~ ^[0-9]+$ ]] || fail "--peer-transport-port must be a positive integer"
[[ "${POLL_TIMEOUT_SECONDS}" =~ ^[0-9]+$ ]] || fail "--poll-timeout must be a positive integer"
[[ "${POLL_INTERVAL_SECONDS}" =~ ^[0-9]+$ ]] || fail "--poll-interval must be a positive integer"
[[ "${LOG_TAIL}" =~ ^[0-9]+$ ]] || fail "--log-tail must be a positive integer"

require_harness_vm_guest

require_command awk
require_command curl
require_command date
require_command jq
require_command sed
require_command ss
require_command tar

ensure_no_live_update_overrides
port_is_available "${HOST_BACKEND_PORT}" || fail "requested host backend port ${HOST_BACKEND_PORT} is already in use"
port_is_available "${HOST_DESKTOP_PORT}" || fail "requested host desktop port ${HOST_DESKTOP_PORT} is already in use"
port_is_available "${HOST_PEER_TRANSPORT_PORT}" || fail "requested peer transport port ${HOST_PEER_TRANSPORT_PORT} is already in use"

prepare_isolated_root
write_startup_config
install_source_release
verify_runtime_version "${SOURCE_VERSION}" "installed"
start_host
fetch_desktop_session
ensure_host_state
resolve_runtime
ensure_target_group
create_seed_local_container
verify_update_status_before
verify_local_container_update_plan_before
start_apply_update
poll_backend_restart_window
fetch_desktop_session
verify_final_update_state
finish_apply_process
verify_runtime_version "${TARGET_VERSION}" "updated"
write_summary
