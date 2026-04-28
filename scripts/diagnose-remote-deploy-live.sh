#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

usage() {
  cat <<'EOF'
Usage: ./scripts/diagnose-remote-deploy-live.sh [options]

Diagnose the currently running remote deploy path without creating, starting,
approving, deleting, or rebuilding a session.

It reads the active local swarm config/API, selects a remote deploy session,
then uses SSH to inspect the planted remote child container, generated child
config, embedded Tailscale state, remote logs, and child-to-master reachability.

Options:
  --ssh-target <target>       SSH target to inspect. Optional when the API has a matching session.
  --session-id <id>           Remote deploy session id. Optional; defaults to newest matching session.
  --api-url <url>             Local swarmd API URL. Default: derived from swarm.conf.
  --config <path>             Local swarm.conf path. Default: $XDG_CONFIG_HOME/swarm/swarm.conf or ~/.config/swarm/swarm.conf.
  --runtime <docker|podman|auto>
                              Remote runtime override. Default: session runtime or auto.
  --artifact-dir <path>       Directory for diagnostic artifacts. Default: tmp/remote-deploy-diagnostics/<timestamp>.
  --tail-lines <count>        Remote log tail lines. Default: 300.
  --show-auth-url             Print Tailscale auth URLs instead of redacting them.
  --no-refresh                Do not call /v1/deploy/remote/session?refresh=true.
  --help                      Show this help text.

Exit codes:
  0  Diagnostic completed and did not classify a hard failure.
  1  Diagnostic found a concrete failure or could not complete required inspection.
EOF
}

log() {
  printf '%s\n' "$*"
}

section() {
  printf '\n== %s ==\n' "$*"
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

conf_get() {
  local key="${1:-}"
  local path="${2:-}"
  [[ -n "${key}" && -f "${path}" ]] || return 0
  awk -F '=' -v want="${key}" '
    {
      left = $1
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", left)
      if (left == want) {
        value = substr($0, index($0, "=") + 1)
        sub(/[[:space:]]+#.*$/, "", value)
        gsub(/^[[:space:]]+|[[:space:]]+$/, "", value)
        print value
      }
    }
  ' "${path}" | tail -n 1
}

redact_stream() {
  if [[ "${SHOW_AUTH_URL}" == "true" ]]; then
    cat
    return 0
  fi
  sed -E \
    -e 's#https://login\.tailscale\.com/a/[[:alnum:]_-]+#https://login.tailscale.com/a/<redacted>#g' \
    -e 's#(TAILSCALE_AUTH_URL=).*#\1<redacted>#g' \
    -e 's#(remote_auth_url":")[^"]+#\1<redacted>#g'
}

write_text_artifact() {
  local name="${1:-}"
  local content="${2-}"
  printf '%s' "${content}" >"${ARTIFACT_DIR}/${name}"
}

shell_quote() {
  printf '%q' "${1-}"
}

api_get_to_file() {
  local path="${1:-}"
  local output_path="${2:-}"
  local max_time="${3:-8}"
  local status_path="${output_path}.http_status"
  local stderr_path="${output_path}.stderr"
  local http_code
  local args=(
    -sS
    --connect-timeout 2
    --max-time "${max_time}"
    -o "${output_path}"
    -w '%{http_code}'
    -H 'Accept: application/json'
  )
  if [[ -n "${COOKIE_FILE:-}" ]]; then
    args+=(
      -c "${COOKIE_FILE}"
      -b "${COOKIE_FILE}"
      -H "Origin: ${API_URL%/}"
      -H "Referer: ${API_URL%/}/"
      -H 'Sec-Fetch-Site: same-origin'
    )
  fi
  if http_code="$(curl "${args[@]}" "${API_URL%/}${path}" 2>"${stderr_path}")"; then
    :
  else
    http_code="000"
  fi
  printf '%s\n' "${http_code}" >"${status_path}"
  [[ "${http_code}" == 2* ]]
}

init_desktop_cookie() {
  COOKIE_FILE="$(mktemp)"
  if api_get_to_file "/v1/auth/desktop/session" "${ARTIFACT_DIR}/desktop-session.json" 5; then
    return 0
  fi
  log "warning: desktop session cookie request failed; continuing with unauthenticated API reads"
}

resolve_defaults() {
  if [[ -z "${CONFIG_PATH}" ]]; then
    CONFIG_PATH="${XDG_CONFIG_HOME:-${HOME}/.config}/swarm/swarm.conf"
  fi

  if [[ -z "${API_URL}" ]]; then
    local host port api_host
    host="$(trim "$(conf_get host "${CONFIG_PATH}")")"
    port="$(trim "$(conf_get port "${CONFIG_PATH}")")"
    [[ -n "${host}" ]] || host="127.0.0.1"
    [[ -n "${port}" ]] || port="7781"
    api_host="${host}"
    if [[ "${api_host}" == "0.0.0.0" || "${api_host}" == "::" ]]; then
      api_host="127.0.0.1"
    fi
    API_URL="http://${api_host}:${port}"
  fi

  if [[ -z "${ARTIFACT_DIR}" ]]; then
    ARTIFACT_DIR="${ROOT_DIR}/tmp/remote-deploy-diagnostics/$(date -u +%Y%m%dT%H%M%SZ)"
  fi
  mkdir -p "${ARTIFACT_DIR}"
}

write_local_summary() {
  section "LOCAL_SWARM_CONFIG"
  log "config_path=${CONFIG_PATH}"
  log "api_url=${API_URL}"
  if [[ ! -f "${CONFIG_PATH}" ]]; then
    log "config_present=false"
    write_text_artifact "local-config-summary.txt" "config_present=false
config_path=${CONFIG_PATH}
api_url=${API_URL}
"
    return 0
  fi

  local keys key value summary
  keys=(
    startup_mode
    dev_mode
    dev_root
    host
    port
    advertise_host
    advertise_port
    desktop_port
    swarm_name
    swarm_mode
    child
    mode
    tailscale_url
    peer_transport_port
  )
  summary="config_present=true
config_path=${CONFIG_PATH}
api_url=${API_URL}
"
  for key in "${keys[@]}"; do
    value="$(conf_get "${key}" "${CONFIG_PATH}")"
    log "${key}=${value}"
    summary+="${key}=${value}"$'\n'
  done
  write_text_artifact "local-config-summary.txt" "${summary}"
}

fetch_sessions() {
  local cached_path="${ARTIFACT_DIR}/sessions-cached.json"
  local refresh_path="${ARTIFACT_DIR}/sessions-refresh.json"
  section "LOCAL_REMOTE_DEPLOY_API"
  if api_get_to_file "/readyz" "${ARTIFACT_DIR}/readyz.txt" 5; then
    log "readyz_http_status=$(cat -- "${ARTIFACT_DIR}/readyz.txt.http_status")"
  else
    log "readyz_http_status=$(cat -- "${ARTIFACT_DIR}/readyz.txt.http_status")"
    log "readyz_error=$(tr '\n' ' ' <"${ARTIFACT_DIR}/readyz.txt.stderr")"
  fi

  if api_get_to_file "/v1/deploy/remote/session?refresh=false" "${cached_path}" 8; then
    log "session_cached_http_status=$(cat -- "${cached_path}.http_status")"
  else
    log "session_cached_http_status=$(cat -- "${cached_path}.http_status")"
    log "session_cached_error=$(tr '\n' ' ' <"${cached_path}.stderr")"
  fi

  if [[ "${REFRESH_SESSIONS}" == "true" ]]; then
    if api_get_to_file "/v1/deploy/remote/session?refresh=true" "${refresh_path}" 20; then
      log "session_refresh_http_status=$(cat -- "${refresh_path}.http_status")"
    else
      log "session_refresh_http_status=$(cat -- "${refresh_path}.http_status")"
      log "session_refresh_error=$(tr '\n' ' ' <"${refresh_path}.stderr")"
    fi
  fi
}

select_session() {
  local source_path="${ARTIFACT_DIR}/sessions-refresh.json"
  if [[ ! -s "${source_path}" ]]; then
    source_path="${ARTIFACT_DIR}/sessions-cached.json"
  fi
  SELECTED_SESSION_JSON=""
  if [[ -s "${source_path}" ]] && jq -e '.sessions? | type == "array"' "${source_path}" >/dev/null 2>&1; then
    SELECTED_SESSION_JSON="$(jq -c --arg id "${SESSION_ID}" --arg target "${SSH_TARGET}" '
      [
        (.sessions // [])[]
        | select(($id == "" or (.id // "") == $id) and ($target == "" or (.ssh_session_target // "") == $target))
      ]
      | sort_by(.updated_at // .created_at // 0)
      | last // empty
    ' "${source_path}")"
  fi

  if [[ -n "${SELECTED_SESSION_JSON}" ]]; then
    write_text_artifact "selected-session.json" "${SELECTED_SESSION_JSON}"
    SESSION_ID="$(printf '%s' "${SELECTED_SESSION_JSON}" | jq -r '.id // empty')"
    if [[ -z "${SSH_TARGET}" ]]; then
      SSH_TARGET="$(printf '%s' "${SELECTED_SESSION_JSON}" | jq -r '.ssh_session_target // empty')"
    fi
    MASTER_ENDPOINT="$(printf '%s' "${SELECTED_SESSION_JSON}" | jq -r '.master_endpoint // empty')"
    REMOTE_ROOT="$(printf '%s' "${SELECTED_SESSION_JSON}" | jq -r '.preflight.remote_root // empty')"
    local session_runtime
    session_runtime="$(printf '%s' "${SELECTED_SESSION_JSON}" | jq -r '.remote_runtime // .preflight.remote_runtime // empty')"
    if [[ "${REMOTE_RUNTIME}" == "auto" && -n "${session_runtime}" && "${session_runtime}" != "null" ]]; then
      REMOTE_RUNTIME="${session_runtime}"
    fi
  fi

  section "SELECTED_REMOTE_SESSION"
  if [[ -z "${SELECTED_SESSION_JSON}" ]]; then
    log "selected=false"
    log "session_id=${SESSION_ID}"
    log "ssh_target=${SSH_TARGET}"
    [[ -n "${SSH_TARGET}" ]] || fail "no remote deploy session selected and --ssh-target was not provided"
    return 0
  fi

  printf '%s\n' "${SELECTED_SESSION_JSON}" | jq '{
    id,
    name,
    status,
    ssh_session_target,
    transport_mode,
    master_endpoint,
    remote_endpoint,
    remote_advertise_host,
    remote_runtime,
    image_delivery_mode,
    image_ref,
    image_archive_bytes,
    last_progress,
    last_error,
    remote_auth_url_present: (((.remote_auth_url // "") | length) > 0),
    remote_tailnet_url,
    updated_at,
    preflight: {
      remote_root: .preflight.remote_root,
      remote_runtime: .preflight.remote_runtime,
      remote_network_candidates: .preflight.remote_network_candidates,
      remote_disk: .preflight.remote_disk
    }
  }' | redact_stream
}

run_remote_probe() {
  local remote_output_path="${ARTIFACT_DIR}/remote-probe.txt"
  local remote_status_path="${ARTIFACT_DIR}/remote-probe.exit_status"
  local remote_command
  local ssh_status
  section "REMOTE_SSH_PROBE"
  log "ssh_target=${SSH_TARGET}"
  log "session_id=${SESSION_ID}"
  log "remote_runtime=${REMOTE_RUNTIME}"
  log "remote_root=${REMOTE_ROOT}"
  log "master_endpoint=${MASTER_ENDPOINT}"

  set +e
  remote_command="SWARM_DIAG_SESSION_ID=$(shell_quote "${SESSION_ID}") SWARM_DIAG_REMOTE_ROOT=$(shell_quote "${REMOTE_ROOT}") SWARM_DIAG_RUNTIME=$(shell_quote "${REMOTE_RUNTIME}") SWARM_DIAG_MASTER_ENDPOINT=$(shell_quote "${MASTER_ENDPOINT}") SWARM_DIAG_TAIL_LINES=$(shell_quote "${TAIL_LINES}") bash -s"
  ssh "${SSH_TARGET}" "${remote_command}" <<'REMOTE_SCRIPT' >"${remote_output_path}" 2>&1
set -u

section() {
  printf '\n== %s ==\n' "$*"
}

proof() {
  printf 'PROOF_%s=%s\n' "$1" "$2"
}

conf_value() {
  local key="${1:-}"
  local path="${2:-}"
  [ -n "$key" ] && [ -f "$path" ] || return 0
  awk -F '=' -v want="$key" '
    {
      left = $1
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", left)
      if (left == want) {
        value = substr($0, index($0, "=") + 1)
        sub(/[[:space:]]+#.*$/, "", value)
        gsub(/^[[:space:]]+|[[:space:]]+$/, "", value)
        print value
      }
    }
  ' "$path" | tail -n 1
}

have_runtime() {
  command -v "$1" >/dev/null 2>&1
}

runtime="${SWARM_DIAG_RUNTIME:-auto}"
session_id="${SWARM_DIAG_SESSION_ID:-}"
input_session_id="$session_id"
remote_root="${SWARM_DIAG_REMOTE_ROOT:-}"
master_endpoint="${SWARM_DIAG_MASTER_ENDPOINT:-}"
tail_lines="${SWARM_DIAG_TAIL_LINES:-300}"

if [ -z "$runtime" ] || [ "$runtime" = "null" ] || [ "$runtime" = "auto" ]; then
  if have_runtime docker; then
    runtime="docker"
  elif have_runtime podman; then
    runtime="podman"
  else
    runtime="missing"
  fi
fi

run_runtime() {
  if [ "$runtime" = "missing" ]; then
    printf 'remote runtime command is missing\n' >&2
    return 127
  fi
  if "$runtime" "$@"; then
    return 0
  fi
  local status="$?"
  if command -v sudo >/dev/null 2>&1; then
    sudo -n "$runtime" "$@"
    return "$?"
  fi
  return "$status"
}

run_runtime_quiet() {
  run_runtime "$@" >/dev/null 2>&1
}

section "REMOTE_ENV"
printf 'hostname=%s\n' "$(hostname 2>/dev/null || true)"
printf 'whoami=%s\n' "$(whoami 2>/dev/null || true)"
printf 'runtime=%s\n' "$runtime"
printf 'session_id=%s\n' "$session_id"
printf 'remote_root_input=%s\n' "$remote_root"
printf 'master_endpoint=%s\n' "$master_endpoint"
proof RUNTIME "$runtime"

section "REMOTE_CONTAINERS"
if run_runtime ps -a --format '{{.ID}}	{{.Names}}	{{.Status}}	{{.Image}}'; then
  :
else
  proof RUNTIME_PS_OK 0
fi

container_name=""
container_session_id=""
if [ -n "$session_id" ] && [ "$session_id" != "null" ]; then
  candidate="swarm-remote-child-${session_id}"
  if run_runtime_quiet inspect "$candidate"; then
    container_name="$candidate"
  fi
fi
if [ -z "$container_name" ]; then
  container_name="$(run_runtime ps -a --format '{{.Names}}' 2>/dev/null | awk '/^swarm-remote-child-/ { print }' | tail -n 1)"
fi
if [ -n "$container_name" ]; then
  container_session_id="${container_name#swarm-remote-child-}"
fi
if [ -n "$container_session_id" ]; then
  session_id="$container_session_id"
fi

section "REMOTE_SELECTED_CONTAINER"
printf 'container_name=%s\n' "$container_name"
printf 'input_session_id=%s\n' "$input_session_id"
printf 'container_session_id=%s\n' "$container_session_id"
proof REMOTE_CONTAINER_SESSION_ID "$container_session_id"
if [ -n "$input_session_id" ] && [ -n "$container_session_id" ] && [ "$input_session_id" != "$container_session_id" ]; then
  proof SESSION_CONTAINER_MISMATCH 1
else
  proof SESSION_CONTAINER_MISMATCH 0
fi
if [ -n "$container_name" ]; then
  proof REMOTE_CONTAINER_FOUND 1
  inspect_line="$(run_runtime inspect -f 'name={{.Name}} image={{.Config.Image}} status={{.State.Status}} running={{.State.Running}} exit_code={{.State.ExitCode}} started_at={{.State.StartedAt}} finished_at={{.State.FinishedAt}}' "$container_name" 2>&1 || true)"
  printf '%s\n' "$inspect_line"
  case "$inspect_line" in
    *running=true*) proof REMOTE_CONTAINER_RUNNING 1 ;;
    *) proof REMOTE_CONTAINER_RUNNING 0 ;;
  esac
else
  proof REMOTE_CONTAINER_FOUND 0
fi

case "$remote_root" in
  *"$session_id"*) ;;
  *) remote_root="" ;;
esac
if [ -z "$remote_root" ] || [ "$remote_root" = "null" ]; then
  data_home="${XDG_DATA_HOME:-${HOME}/.local/share}"
  for candidate_root in \
    "${data_home}/swarm/rd/${session_id}" \
    "${data_home}/swarm/rd/"*"${session_id}"*
  do
    if [ -d "$candidate_root" ]; then
      remote_root="$candidate_root"
      break
    fi
  done
fi
proof REMOTE_ROOT "$remote_root"

section "REMOTE_ROOT_TREE"
if [ -n "$remote_root" ] && [ -e "$remote_root" ]; then
  proof REMOTE_ROOT_EXISTS 1
  find "$remote_root" -maxdepth 2 -mindepth 1 -printf '%y %p\n' 2>/dev/null | sort | head -n 200
else
  proof REMOTE_ROOT_EXISTS 0
  printf 'remote root does not exist: %s\n' "$remote_root"
fi

config_path="${remote_root%/}/config/swarm/swarm.conf"
section "REMOTE_CONFIG_PATHS"
for path in "${remote_root%/}/config" "${remote_root%/}/config/swarm" "$config_path"; do
  if [ -e "$path" ]; then
    ls -ld "$path" 2>&1 || true
  else
    printf 'missing %s\n' "$path"
  fi
done

section "REMOTE_CHILD_CONFIG"
if [ -f "$config_path" ]; then
  proof CONFIG_FILE 1
  proof CHILD_CONFIG_HOST "$(conf_value host "$config_path")"
  proof CHILD_CONFIG_ADVERTISE_HOST "$(conf_value advertise_host "$config_path")"
  proof CHILD_CONFIG_MODE "$(conf_value mode "$config_path")"
  grep -E '^(startup_mode|dev_mode|host|port|advertise_host|advertise_port|desktop_port|swarm_name|swarm_mode|child|mode|tailscale_url|peer_transport_port|remote_deploy_enabled|remote_deploy_session_id|remote_deploy_host_api_base_url|remote_deploy_host_desktop_url|remote_deploy_sync_enabled|remote_deploy_sync_mode|remote_deploy_sync_owner_swarm_id|deploy_container_enabled|deploy_container_host_api_base_url)[[:space:]]*=' "$config_path" || true
elif [ -d "$config_path" ]; then
  proof CONFIG_FILE 0
  proof CONFIG_PATH_IS_DIRECTORY 1
  printf 'config path is a directory: %s\n' "$config_path"
else
  proof CONFIG_FILE 0
  printf 'config file is missing: %s\n' "$config_path"
fi

section "REMOTE_INSTALLER_LOG_MARKERS"
log_auth_present=0
log_tailnet_present=0
log_remote_url_present=0
if [ -n "$remote_root" ] && [ -d "${remote_root%/}/logs" ]; then
  find "${remote_root%/}/logs" -maxdepth 1 -type f -printf '%p\n' 2>/dev/null | sort
  grep -R -E 'TAILSCALE_AUTH_URL=|SWARM_TAILNET_URL=|SWARM_REMOTE_URL=|SWARM_REMOTE_TIMER|NeedsLogin|error|failed|panic' "${remote_root%/}/logs" 2>/dev/null | tail -n "$tail_lines" || true
  if grep -R -q '^TAILSCALE_AUTH_URL=' "${remote_root%/}/logs" 2>/dev/null; then
    log_auth_present=1
  fi
  if grep -R -q '^SWARM_TAILNET_URL=' "${remote_root%/}/logs" 2>/dev/null; then
    log_tailnet_present=1
  fi
  if grep -R -q '^SWARM_REMOTE_URL=' "${remote_root%/}/logs" 2>/dev/null; then
    log_remote_url_present=1
  fi
else
  printf 'remote log directory missing: %s\n' "${remote_root%/}/logs"
fi
proof LOG_AUTH_URL_PRESENT "$log_auth_present"
proof LOG_TAILNET_URL_PRESENT "$log_tailnet_present"
proof LOG_REMOTE_URL_PRESENT "$log_remote_url_present"

section "REMOTE_CONTAINER_LOG_MARKERS"
container_auth_present=0
container_tailnet_present=0
container_remote_url_present=0
container_startup_mode=""
if [ -n "$container_name" ]; then
  container_logs="$(run_runtime logs --tail "$tail_lines" "$container_name" 2>&1 || true)"
  printf '%s\n' "$container_logs" | grep -E 'TAILSCALE_AUTH_URL=|SWARM_TAILNET_URL=|SWARM_REMOTE_URL=|NeedsLogin|error|failed|panic|ready|listen' || true
  container_startup_mode="$(printf '%s\n' "$container_logs" | sed -n 's/^\[swarm-container\] startup mode=\([^ ]*\).*/\1/p' | tail -n 1)"
  if printf '%s\n' "$container_logs" | grep -q '^TAILSCALE_AUTH_URL='; then
    container_auth_present=1
  fi
  if printf '%s\n' "$container_logs" | grep -q '^SWARM_TAILNET_URL='; then
    container_tailnet_present=1
  fi
  if printf '%s\n' "$container_logs" | grep -q '^SWARM_REMOTE_URL='; then
    container_remote_url_present=1
  fi
fi
proof CONTAINER_STARTUP_MODE "$container_startup_mode"
proof CONTAINER_LOG_AUTH_URL_PRESENT "$container_auth_present"
proof CONTAINER_LOG_TAILNET_URL_PRESENT "$container_tailnet_present"
proof CONTAINER_LOG_REMOTE_URL_PRESENT "$container_remote_url_present"

section "REMOTE_HOST_MASTER_REACHABILITY"
host_http_code="000"
host_curl_exit="0"
if [ -n "$master_endpoint" ] && command -v curl >/dev/null 2>&1; then
  curl_output="$(curl -sS --connect-timeout 3 --max-time 8 -o /dev/null -w 'http_code=%{http_code}' "${master_endpoint%/}/readyz" 2>&1)"
  host_curl_exit="$?"
  printf 'curl_exit=%s %s\n' "$host_curl_exit" "$curl_output"
  host_http_code="$(printf '%s\n' "$curl_output" | sed -n 's/.*http_code=\([0-9][0-9][0-9]\).*/\1/p' | tail -n 1)"
  [ -n "$host_http_code" ] || host_http_code="000"
else
  printf 'skipped host curl: missing master endpoint or curl\n'
fi
proof REMOTE_HOST_MASTER_READYZ_CURL_EXIT "$host_curl_exit"
proof REMOTE_HOST_MASTER_READYZ_HTTP_CODE "$host_http_code"

section "REMOTE_CHILD_CONTAINER_PROBE"
if [ -n "$container_name" ]; then
  run_runtime exec \
    -e "SWARM_DIAG_REMOTE_ROOT=$remote_root" \
    -e "SWARM_DIAG_MASTER_ENDPOINT=$master_endpoint" \
    "$container_name" sh -lc '
set -u
proof() {
  printf "PROOF_%s=%s\n" "$1" "$2"
}
conf_value() {
  key="${1:-}"
  path="${2:-}"
  [ -n "$key" ] && [ -f "$path" ] || return 0
  awk -F "=" -v want="$key" "
    {
      left = \$1
      gsub(/^[[:space:]]+|[[:space:]]+$/, \"\", left)
      if (left == want) {
        value = substr(\$0, index(\$0, \"=\") + 1)
        sub(/[[:space:]]+#.*$/, \"\", value)
        gsub(/^[[:space:]]+|[[:space:]]+$/, \"\", value)
        print value
      }
    }
  " "$path" | tail -n 1
}
remote_root="${SWARM_DIAG_REMOTE_ROOT:-}"
master_endpoint="${SWARM_DIAG_MASTER_ENDPOINT:-}"
config_path="${remote_root%/}/config/swarm/swarm.conf"
printf "container_user=%s\n" "$(id -un 2>/dev/null || true)"
printf "container_config_path=%s\n" "$config_path"
if [ -f "$config_path" ]; then
  proof CONTAINER_CONFIG_FILE 1
  proof CONTAINER_CHILD_CONFIG_HOST "$(conf_value host "$config_path")"
  proof CONTAINER_CHILD_CONFIG_ADVERTISE_HOST "$(conf_value advertise_host "$config_path")"
  proof CONTAINER_CHILD_CONFIG_MODE "$(conf_value mode "$config_path")"
  grep -E "^(host|advertise_host|mode|tailscale_url|remote_deploy_enabled|remote_deploy_session_id|remote_deploy_host_api_base_url|peer_transport_port)[[:space:]]*=" "$config_path" || true
else
  proof CONTAINER_CONFIG_FILE 0
fi

ts_socket="${remote_root%/}/state/tailscale/tailscaled.sock"
printf "tailscale_socket=%s\n" "$ts_socket"
if command -v tailscale >/dev/null 2>&1 && [ -S "$ts_socket" ]; then
  ts_json="$(tailscale --socket="$ts_socket" status --json 2>&1 || true)"
  printf "%s\n" "$ts_json" | sed -n "1,80p"
  backend_state="$(printf "%s\n" "$ts_json" | sed -n "s/.*\"BackendState\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" | head -n 1)"
  dns_name="$(printf "%s\n" "$ts_json" | sed -n "s/.*\"DNSName\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p" | head -n 1)"
  auth_present=0
  if printf "%s\n" "$ts_json" | grep -Eq "\"AuthURL\"[[:space:]]*:[[:space:]]*\"[^\"]+\""; then
    auth_present=1
  fi
  proof TAILSCALE_BACKEND_STATE "$backend_state"
  proof TAILSCALE_AUTH_URL_PRESENT "$auth_present"
  proof TAILSCALE_DNS_NAME "$dns_name"
elif command -v tailscale >/dev/null 2>&1; then
  proof TAILSCALE_STATUS_AVAILABLE 0
  printf "tailscale socket missing\n"
else
  proof TAILSCALE_BINARY_PRESENT 0
  printf "tailscale binary missing in child container\n"
fi

tailscale_up_process_present=0
if command -v ps >/dev/null 2>&1; then
  ps -eo pid=,comm=,args= | grep -E "^[[:space:]]*[0-9]+[[:space:]]+tailscale[[:space:]].*[[:space:]]up([[:space:]]|$)" || true
  if ps -eo pid=,comm=,args= | grep -E "^[[:space:]]*[0-9]+[[:space:]]+tailscale[[:space:]].*[[:space:]]up([[:space:]]|$)" >/dev/null 2>&1; then
    tailscale_up_process_present=1
  fi
fi
proof TAILSCALE_UP_PROCESS_PRESENT "$tailscale_up_process_present"

child_http_code="000"
child_curl_exit="0"
if [ -n "$master_endpoint" ] && command -v curl >/dev/null 2>&1; then
  curl_output="$(curl -sS --connect-timeout 3 --max-time 8 -o /dev/null -w "http_code=%{http_code}" "${master_endpoint%/}/readyz" 2>&1)"
  child_curl_exit="$?"
  printf "master_readyz curl_exit=%s %s\n" "$child_curl_exit" "$curl_output"
  child_http_code="$(printf "%s\n" "$curl_output" | sed -n "s/.*http_code=\([0-9][0-9][0-9]\).*/\1/p" | tail -n 1)"
  [ -n "$child_http_code" ] || child_http_code="000"
else
  printf "skipped child curl: missing master endpoint or curl\n"
fi
proof CHILD_MASTER_READYZ_CURL_EXIT "$child_curl_exit"
proof CHILD_MASTER_READYZ_HTTP_CODE "$child_http_code"

local_socket="${remote_root%/}/state/swarmd/local-transport/api.sock"
local_http_code="000"
local_curl_exit="0"
if [ -S "$local_socket" ] && command -v curl >/dev/null 2>&1; then
  local_output="$(curl -sS --connect-timeout 2 --max-time 5 --unix-socket "$local_socket" -o /dev/null -w "http_code=%{http_code}" http://swarmd/readyz 2>&1)"
  local_curl_exit="$?"
  printf "local_transport_readyz curl_exit=%s %s\n" "$local_curl_exit" "$local_output"
  local_http_code="$(printf "%s\n" "$local_output" | sed -n "s/.*http_code=\([0-9][0-9][0-9]\).*/\1/p" | tail -n 1)"
  [ -n "$local_http_code" ] || local_http_code="000"
else
  printf "local transport socket missing: %s\n" "$local_socket"
fi
proof CHILD_LOCAL_TRANSPORT_READYZ_CURL_EXIT "$local_curl_exit"
proof CHILD_LOCAL_TRANSPORT_READYZ_HTTP_CODE "$local_http_code"
' 2>&1 || true
else
  printf 'skipped child container probe: no container selected\n'
fi
REMOTE_SCRIPT
  ssh_status="$?"
  set -e
  printf '%s\n' "${ssh_status}" >"${remote_status_path}"
  redact_stream <"${remote_output_path}" | tee "${ARTIFACT_DIR}/remote-probe.redacted.txt"
  if [[ "${ssh_status}" != "0" ]]; then
    log "remote_probe_exit_status=${ssh_status}"
    return 1
  fi
  return 0
}

proof_value() {
  local key="${1:-}"
  local path="${ARTIFACT_DIR}/remote-probe.redacted.txt"
  [[ -f "${path}" ]] || return 0
  awk -F '=' -v want="PROOF_${key}" '$1 == want { value = substr($0, index($0, "=") + 1); print value }' "${path}" | tail -n 1
}

api_session_value() {
  local jq_filter="${1:-}"
  [[ -n "${SELECTED_SESSION_JSON}" ]] || return 0
  printf '%s' "${SELECTED_SESSION_JSON}" | jq -r "${jq_filter}"
}

diagnose() {
  section "DIAGNOSIS"
  local hard_failure=0
  local transport status api_auth_present api_tailnet_present
  local container_found container_running container_session_id mismatch
  local config_file container_config_file config_path_is_dir config_host config_advertise
  local startup_mode ts_state ts_auth_present ts_up_process child_master_code child_master_exit local_transport_code
  local log_auth_present container_log_auth_present

  transport="$(api_session_value '.transport_mode // empty')"
  status="$(api_session_value '.status // empty')"
  api_auth_present="$(api_session_value 'if ((.remote_auth_url // "") | length) > 0 then "1" else "0" end')"
  api_tailnet_present="$(api_session_value 'if ((.remote_tailnet_url // .remote_endpoint // "") | length) > 0 then "1" else "0" end')"

  container_found="$(proof_value REMOTE_CONTAINER_FOUND)"
  container_running="$(proof_value REMOTE_CONTAINER_RUNNING)"
  container_session_id="$(proof_value REMOTE_CONTAINER_SESSION_ID)"
  mismatch="$(proof_value SESSION_CONTAINER_MISMATCH)"
  config_file="$(proof_value CONFIG_FILE)"
  container_config_file="$(proof_value CONTAINER_CONFIG_FILE)"
  config_path_is_dir="$(proof_value CONFIG_PATH_IS_DIRECTORY)"
  config_host="$(proof_value CHILD_CONFIG_HOST)"
  config_advertise="$(proof_value CHILD_CONFIG_ADVERTISE_HOST)"
  startup_mode="$(proof_value CONTAINER_STARTUP_MODE)"
  if [[ "${container_config_file}" == "1" ]]; then
    config_host="$(proof_value CONTAINER_CHILD_CONFIG_HOST)"
    config_advertise="$(proof_value CONTAINER_CHILD_CONFIG_ADVERTISE_HOST)"
  fi
  ts_state="$(proof_value TAILSCALE_BACKEND_STATE)"
  ts_auth_present="$(proof_value TAILSCALE_AUTH_URL_PRESENT)"
  ts_up_process="$(proof_value TAILSCALE_UP_PROCESS_PRESENT)"
  child_master_code="$(proof_value CHILD_MASTER_READYZ_HTTP_CODE)"
  child_master_exit="$(proof_value CHILD_MASTER_READYZ_CURL_EXIT)"
  local_transport_code="$(proof_value CHILD_LOCAL_TRANSPORT_READYZ_HTTP_CODE)"
  log_auth_present="$(proof_value LOG_AUTH_URL_PRESENT)"
  container_log_auth_present="$(proof_value CONTAINER_LOG_AUTH_URL_PRESENT)"

  log "session_status=${status:-unknown}"
  log "transport_mode=${transport:-unknown}"
  log "container_found=${container_found:-unknown}"
  log "container_running=${container_running:-unknown}"
  log "remote_container_session_id=${container_session_id:-unknown}"
  log "api_session_container_mismatch=${mismatch:-unknown}"
  log "host_config_file=${config_file:-unknown}"
  log "container_config_file=${container_config_file:-unknown}"
  log "child_config_host=${config_host:-unknown}"
  log "child_config_advertise_host=${config_advertise:-unknown}"
  log "container_startup_mode=${startup_mode:-unknown}"
  log "tailscale_backend_state=${ts_state:-unknown}"
  log "tailscale_auth_url_in_child=${ts_auth_present:-unknown}"
  log "tailscale_up_process_present=${ts_up_process:-unknown}"
  log "auth_url_in_remote_logs=${log_auth_present:-unknown}"
  log "auth_url_in_container_logs=${container_log_auth_present:-unknown}"
  log "auth_url_in_local_api=${api_auth_present:-unknown}"
  log "tailnet_endpoint_in_local_api=${api_tailnet_present:-unknown}"
  log "child_to_master_readyz_http=${child_master_code:-unknown}"
  log "child_to_master_readyz_exit=${child_master_exit:-unknown}"
  log "child_local_transport_readyz_http=${local_transport_code:-unknown}"

  if [[ "${container_found}" == "0" ]]; then
    log "FAIL: SSH/bootstrap did not leave a swarm-remote-child container on the remote host."
    hard_failure=1
  elif [[ "${container_running}" == "0" ]]; then
    log "FAIL: the remote child container exists but is not running."
    hard_failure=1
  fi

  if [[ "${config_path_is_dir}" == "1" ]]; then
    log "FAIL: the generated child config path is a directory, matching the installer path failure class."
    hard_failure=1
  elif [[ "${config_file}" == "0" && "${container_config_file}" != "1" ]]; then
    log "FAIL: the generated child config file is missing from both host root and child container."
    hard_failure=1
  fi

  if [[ "${mismatch}" == "1" ]]; then
    log "FAIL: the local API selected one session, but the running remote child container belongs to ${container_session_id}."
    hard_failure=1
  fi

  if [[ "${transport}" == "tailscale" && ( "${config_host}" == "10.0.0.1" || "${config_advertise}" == "10.0.0.1" ) ]]; then
    log "FAIL: tailscale child config is polluted with remote LAN address 10.0.0.1."
    hard_failure=1
  fi

  if [[ "${transport}" == "tailscale" && "${ts_state}" == "NeedsLogin" ]]; then
    if [[ "${ts_auth_present}" != "1" && "${log_auth_present}" != "1" && "${container_log_auth_present}" != "1" ]]; then
      log "FAIL: embedded Tailscale is in NeedsLogin but no auth URL was produced."
      hard_failure=1
    elif [[ "${api_auth_present}" != "1" && ( "${ts_auth_present}" == "1" || "${log_auth_present}" == "1" || "${container_log_auth_present}" == "1" ) ]]; then
      log "FAIL: child produced a Tailscale auth URL, but the local session API is not surfacing it."
      hard_failure=1
    else
      log "BLOCKED: embedded Tailscale is waiting for login; the deploy cannot reach the parent until that login is approved."
    fi
  fi

  if [[ "${transport}" == "tailscale" && "${ts_state}" == "NeedsLogin" && "${ts_up_process}" == "0" ]]; then
    log "FAIL: no live tailscale up process is holding the login attempt open."
    hard_failure=1
  fi

  if [[ "${transport}" == "tailscale" && "${startup_mode}" != "box" ]]; then
    log "FAIL: remote child container was launched without SWARM_STARTUP_MODE=box."
    hard_failure=1
  fi

  if [[ "${transport}" == "tailscale" && "${child_master_code}" == "000" && "${child_master_exit}" != "0" ]]; then
    log "FAIL: from inside the child container, the master endpoint is not reachable."
    hard_failure=1
  fi

  if [[ "${local_transport_code}" != "" && "${local_transport_code}" != "000" && "${local_transport_code}" != "200" ]]; then
    log "FAIL: child local transport answered with unexpected readyz status ${local_transport_code}."
    hard_failure=1
  fi

  if [[ "${hard_failure}" == "0" ]]; then
    log "RESULT: no hard failure classified by this script. Inspect artifacts for the raw proof."
  else
    log "RESULT: concrete failure classified. Artifacts are in ${ARTIFACT_DIR}"
  fi
  return "${hard_failure}"
}

cleanup() {
  if [[ -n "${COOKIE_FILE:-}" ]]; then
    rm -f -- "${COOKIE_FILE}"
  fi
}

API_URL=""
CONFIG_PATH=""
SSH_TARGET=""
SESSION_ID=""
REMOTE_RUNTIME="auto"
REMOTE_ROOT=""
MASTER_ENDPOINT=""
ARTIFACT_DIR=""
TAIL_LINES="300"
SHOW_AUTH_URL="false"
REFRESH_SESSIONS="true"
SELECTED_SESSION_JSON=""
COOKIE_FILE=""

while (($# > 0)); do
  case "$1" in
    --ssh-target)
      shift
      [[ $# -gt 0 ]] || fail "--ssh-target requires a value"
      SSH_TARGET="$1"
      ;;
    --session-id)
      shift
      [[ $# -gt 0 ]] || fail "--session-id requires a value"
      SESSION_ID="$1"
      ;;
    --api-url)
      shift
      [[ $# -gt 0 ]] || fail "--api-url requires a value"
      API_URL="$1"
      ;;
    --config)
      shift
      [[ $# -gt 0 ]] || fail "--config requires a value"
      CONFIG_PATH="$1"
      ;;
    --runtime)
      shift
      [[ $# -gt 0 ]] || fail "--runtime requires a value"
      REMOTE_RUNTIME="$1"
      ;;
    --artifact-dir)
      shift
      [[ $# -gt 0 ]] || fail "--artifact-dir requires a value"
      ARTIFACT_DIR="$1"
      ;;
    --tail-lines)
      shift
      [[ $# -gt 0 ]] || fail "--tail-lines requires a value"
      TAIL_LINES="$1"
      ;;
    --show-auth-url)
      SHOW_AUTH_URL="true"
      ;;
    --no-refresh)
      REFRESH_SESSIONS="false"
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
  shift
done

case "${REMOTE_RUNTIME}" in
  docker|podman|auto) ;;
  *) fail "--runtime must be docker, podman, or auto" ;;
esac
[[ "${TAIL_LINES}" =~ ^[0-9]+$ ]] || fail "--tail-lines must be a positive integer"
(( TAIL_LINES > 0 )) || fail "--tail-lines must be greater than zero"

require_command curl
require_command jq
require_command ssh
require_command awk
require_command sed

resolve_defaults
trap cleanup EXIT

write_local_summary
init_desktop_cookie
fetch_sessions
select_session

remote_probe_status=0
if ! run_remote_probe; then
  remote_probe_status=1
fi

if [[ "${REFRESH_SESSIONS}" == "true" ]]; then
  section "LOCAL_REMOTE_DEPLOY_API_AFTER_REMOTE_PROBE"
  if api_get_to_file "/v1/deploy/remote/session?refresh=true" "${ARTIFACT_DIR}/sessions-refresh-after-remote.json" 20; then
    log "session_refresh_after_remote_http_status=$(cat -- "${ARTIFACT_DIR}/sessions-refresh-after-remote.json.http_status")"
  else
    log "session_refresh_after_remote_http_status=$(cat -- "${ARTIFACT_DIR}/sessions-refresh-after-remote.json.http_status")"
    log "session_refresh_after_remote_error=$(tr '\n' ' ' <"${ARTIFACT_DIR}/sessions-refresh-after-remote.json.stderr")"
  fi
fi

diagnosis_status=0
if ! diagnose; then
  diagnosis_status=1
fi

section "ARTIFACTS"
log "${ARTIFACT_DIR}"

if [[ "${remote_probe_status}" != "0" || "${diagnosis_status}" != "0" ]]; then
  exit 1
fi
