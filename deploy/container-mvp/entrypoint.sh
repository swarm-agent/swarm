#!/bin/sh
set -eu

TS_SOCKET="${TS_SOCKET:?TS_SOCKET must be set}"
TS_STATE_DIR="${TS_STATE_DIR:?TS_STATE_DIR must be set}"
TS_STATE_FILE="${TS_STATE_FILE:-${TS_STATE_DIR}/tailscaled.state}"
TS_HOSTNAME="${TS_HOSTNAME:-swarm-box}"
TS_OUTBOUND_HTTP_PROXY_LISTEN="${TS_OUTBOUND_HTTP_PROXY_LISTEN:-}"
SWARM_STARTUP_MODE="${SWARM_STARTUP_MODE:-}"
SWARM_CONTAINER_OFFLINE="${SWARM_CONTAINER_OFFLINE:-}"
SWARMD_DATA_DIR="${SWARMD_DATA_DIR:-/var/lib/swarmd}"
SWARMD_CONFIG_DIR="${SWARMD_CONFIG_DIR:-/etc/swarmd}"
SWARMD_CACHE_DIR="${SWARMD_CACHE_DIR:-/var/cache/swarmd}"
SWARMD_LOG_DIR="${SWARMD_LOG_DIR:-/var/log/swarmd}"
SWARMD_RUNTIME_DIR="${SWARMD_RUNTIME_DIR:-/run/swarmd}"
SWARMD_LOCK_PATH="${SWARMD_LOCK_PATH:-${SWARMD_RUNTIME_DIR}/swarmd.lock}"
SWARMD_LISTEN="${SWARMD_LISTEN:?SWARMD_LISTEN must be set}"
SWARM_DESKTOP_PORT="${SWARM_DESKTOP_PORT:?SWARM_DESKTOP_PORT must be set}"
SWARM_WEB_DIST_DIR="${SWARM_WEB_DIST_DIR:?SWARM_WEB_DIST_DIR must be set}"
SWARM_PROCESS_HOME="${SWARM_PROCESS_HOME:-/nonexistent}"
TS_UP_LOG="$(mktemp)"
TS_SERVE_LOG="$(mktemp)"

mkdir -p "${TS_STATE_DIR}" "$(dirname "${TS_SOCKET}")" /workspaces

child_cfg_value() {
  key="${1:-}"
  if [ -z "${key}" ] || [ -z "${SWARM_CHILD_STARTUP_CONFIG:-}" ]; then
    return 0
  fi
  printf '%b\n' "${SWARM_CHILD_STARTUP_CONFIG}" | awk -F= -v key="${key}" '
    /^[[:space:]]*#/ { next }
    index($0, "=") == 0 { next }
    {
      current=$1
      value=substr($0, index($0, "=") + 1)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", current)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", value)
      if (current == key) {
        print value
        exit
      }
    }
  '
}

is_true() {
  case "$(printf '%s' "${1:-}" | tr '[:upper:]' '[:lower:]')" in
    1|true|yes|on)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

swarm_backend_port() {
  listen="${SWARMD_LISTEN:-}"
  case "${listen}" in
    *:*)
      port="${listen##*:}"
      ;;
    *)
      port="${listen}"
      ;;
  esac
  printf '%s' "${port}" | awk '/^[0-9]+$/ { print; exit }'
}

runtime_uid() {
  printf '%s' "${SWARM_RUNTIME_UID:-65534}" | awk '/^[0-9]+$/ { print; exit }'
}

runtime_gid() {
  printf '%s' "${SWARM_RUNTIME_GID:-65534}" | awk '/^[0-9]+$/ { print; exit }'
}

validate_container_storage_path() {
  name="${1:-path}"
  value="${2:-}"
  case "${value}" in
    ""|~|~/*)
      echo "[swarm-container] ${name} must be an absolute system path" >&2
      exit 1
      ;;
    /*)
      ;;
    *)
      echo "[swarm-container] ${name} must be absolute: ${value}" >&2
      exit 1
      ;;
  esac
  case "${value}" in
    */../*|*/..|*/./*|*/.)
      echo "[swarm-container] ${name} must be clean and must not contain traversal: ${value}" >&2
      exit 1
      ;;
    /root|/root/*|/home|/home/*|/workspaces|/workspaces/*|/tmp|/tmp/*)
      echo "[swarm-container] ${name} must not be under a user home, workspace, or temp root: ${value}" >&2
      exit 1
      ;;
  esac
}

ensure_runtime_permissions() {
  runtime_uid_value="$(runtime_uid)"
  runtime_gid_value="$(runtime_gid)"
  if [ -z "${runtime_uid_value}" ] || [ -z "${runtime_gid_value}" ]; then
    echo "[swarm-container] SWARM_RUNTIME_UID and SWARM_RUNTIME_GID must be numeric" >&2
    exit 1
  fi
  validate_container_storage_path SWARMD_DATA_DIR "${SWARMD_DATA_DIR}"
  validate_container_storage_path SWARMD_CONFIG_DIR "${SWARMD_CONFIG_DIR}"
  validate_container_storage_path SWARMD_CACHE_DIR "${SWARMD_CACHE_DIR}"
  validate_container_storage_path SWARMD_LOG_DIR "${SWARMD_LOG_DIR}"
  validate_container_storage_path SWARMD_RUNTIME_DIR "${SWARMD_RUNTIME_DIR}"
  validate_container_storage_path SWARMD_LOCK_PATH "${SWARMD_LOCK_PATH}"
  mkdir -p "${SWARMD_DATA_DIR}" "${SWARMD_CONFIG_DIR}" "${SWARMD_CACHE_DIR}" "${SWARMD_LOG_DIR}" "${SWARMD_RUNTIME_DIR}" "$(dirname "${SWARMD_LOCK_PATH}")" /workspaces
  # /workspaces is an intentional host-shared mount boundary; do not rewrite ownership.
  chown -R "${runtime_uid_value}:${runtime_gid_value}" "${SWARMD_DATA_DIR}" "${SWARMD_CONFIG_DIR}" "${SWARMD_CACHE_DIR}" "${SWARMD_LOG_DIR}" "${SWARMD_RUNTIME_DIR}" "$(dirname "${SWARMD_LOCK_PATH}")"
}

run_as_swarm_user() {
  runtime_uid_value="$(runtime_uid)"
  runtime_gid_value="$(runtime_gid)"
  if [ -z "${runtime_uid_value}" ] || [ -z "${runtime_gid_value}" ]; then
    echo "[swarm-container] SWARM_RUNTIME_UID and SWARM_RUNTIME_GID must be numeric" >&2
    exit 1
  fi
  setpriv --reuid="${runtime_uid_value}" --regid="${runtime_gid_value}" --clear-groups \
    env \
      -u XDG_CONFIG_HOME \
      -u XDG_DATA_HOME \
      -u XDG_STATE_HOME \
      -u XDG_CACHE_HOME \
      -u XDG_RUNTIME_DIR \
      HOME="${SWARM_PROCESS_HOME}" \
      STATE_DIRECTORY="${SWARMD_DATA_DIR}" \
      CONFIGURATION_DIRECTORY="${SWARMD_CONFIG_DIR}" \
      CACHE_DIRECTORY="${SWARMD_CACHE_DIR}" \
      LOGS_DIRECTORY="${SWARMD_LOG_DIR}" \
      RUNTIME_DIRECTORY="${SWARMD_RUNTIME_DIR}" \
      "$@"
}

start_swarmd() {
  offline_state="no"
  if is_true "${SWARM_CONTAINER_OFFLINE}"; then
    offline_state="yes"
  fi
  child_cfg_state="no"
  if [ -n "${SWARM_CHILD_STARTUP_CONFIG:-}" ]; then
    child_cfg_state="yes"
  fi
  echo "[swarm-container] startup mode=${SWARM_STARTUP_MODE:-} listen=${SWARMD_LISTEN} desktop_port=${SWARM_DESKTOP_PORT} offline=${offline_state}"
  echo "[swarm-container] child startup config env present=${child_cfg_state}"
  if [ -n "${SWARM_CHILD_STARTUP_CONFIG:-}" ]; then
    echo "[swarm-container] child bootstrap summary deployment_id=$(child_cfg_value deploy_container_deployment_id) swarm_name=$(child_cfg_value swarm_name) parent_swarm_id=$(child_cfg_value parent_swarm_id) pairing_state=$(child_cfg_value pairing_state)"
    echo "[swarm-container] child bootstrap endpoints host_api_base_url=$(child_cfg_value deploy_container_host_api_base_url) host_desktop_url=$(child_cfg_value deploy_container_host_desktop_url) advertise_host=$(child_cfg_value advertise_host) advertise_port=$(child_cfg_value advertise_port) desktop_port=$(child_cfg_value desktop_port) local_transport_socket_path=$(child_cfg_value deploy_container_local_transport_socket_path)"
  fi
  echo "[swarm-container] starting swarmd"
  swarmd_bin="${SWARM_RUNTIME_BIN:-/usr/local/bin/swarmd}"
  set -- \
    "${swarmd_bin}" \
    --listen="${SWARMD_LISTEN}" \
    --desktop-port="${SWARM_DESKTOP_PORT}" \
    --data-dir="${SWARMD_DATA_DIR}" \
    --db-path="${SWARMD_DATA_DIR}/swarmd.pebble" \
    --lock-path="${SWARMD_LOCK_PATH}"

  if [ -n "${SWARM_STARTUP_MODE}" ]; then
    set -- "$@" --mode="${SWARM_STARTUP_MODE}"
  fi

  run_as_swarm_user env SWARM_WEB_DIST_DIR="${SWARM_WEB_DIST_DIR}" "$@" &
  SWARMD_PID=$!
}

ts_cleanup() {
  if [ -n "${TS_UP_PID:-}" ]; then
    kill "${TS_UP_PID}" 2>/dev/null || true
  fi
  if [ -n "${SWARMD_PID:-}" ]; then
    kill "${SWARMD_PID}" 2>/dev/null || true
  fi
  if [ -n "${TAILSCALED_PID:-}" ]; then
    kill "${TAILSCALED_PID}" 2>/dev/null || true
  fi
  rm -f "${TS_UP_LOG}" "${TS_SERVE_LOG}" 2>/dev/null || true
}

trap ts_cleanup INT TERM EXIT

ensure_runtime_permissions
start_swarmd

if is_true "${SWARM_CONTAINER_OFFLINE}"; then
  echo "[swarm-container] offline mode enabled; skipping tailscaled/tailscale serve"
  wait "${SWARMD_PID}"
  exit $?
fi

echo "[swarm-container] starting tailscaled"
ts_tun_mode="${TS_TUN_MODE:-userspace-networking}"
userspace_networking=0
set -- \
  --state="${TS_STATE_FILE}" \
  --socket="${TS_SOCKET}"
case "${ts_tun_mode}" in
  "")
    set -- --tun=userspace-networking "$@"
    userspace_networking=1
    echo "[swarm-container] tailscaled using userspace networking"
    ;;
  auto)
    if [ ! -c /dev/net/tun ]; then
      set -- --tun=userspace-networking "$@"
      userspace_networking=1
      echo "[swarm-container] tailscaled using userspace networking"
    else
      echo "[swarm-container] tailscaled using kernel tun"
    fi
    ;;
  userspace-networking)
    set -- --tun=userspace-networking "$@"
    userspace_networking=1
    echo "[swarm-container] tailscaled using userspace networking"
    ;;
  *)
    set -- --tun="${ts_tun_mode}" "$@"
    echo "[swarm-container] tailscaled using tun mode ${ts_tun_mode}"
    ;;
esac
if [ "${userspace_networking}" = "1" ] && [ -n "${TS_OUTBOUND_HTTP_PROXY_LISTEN}" ]; then
  set -- --outbound-http-proxy-listen="${TS_OUTBOUND_HTTP_PROXY_LISTEN}" "$@"
fi
tailscaled "$@" &
TAILSCALED_PID=$!

ready=0
for _ in $(seq 1 60); do
  if [ -S "${TS_SOCKET}" ]; then
    ready=1
    break
  fi
  sleep 1
done

if [ "${ready}" != "1" ]; then
  echo "[swarm-container] tailscaled did not become ready" >&2
  exit 1
fi

set -- \
  --socket="${TS_SOCKET}" \
  up \
  --reset \
  --hostname="${TS_HOSTNAME}" \
  --accept-dns=true \
  --accept-routes=false

if [ -n "${TS_AUTHKEY:-}" ]; then
  set -- "$@" --auth-key="${TS_AUTHKEY}"
fi

echo "[swarm-container] requesting tailscale connectivity"
tailscale "$@" >"${TS_UP_LOG}" 2>&1 &
TS_UP_PID=$!

auth_url=""
served=0

while :; do
  status_json="$(tailscale --socket="${TS_SOCKET}" status --json 2>/dev/null | tr -d '\n' || true)"
  backend_state=""
  if [ -n "${status_json}" ]; then
    if [ -z "${auth_url}" ]; then
      auth_url="$(printf '%s' "${status_json}" | jq -r '.AuthURL // ""' 2>/dev/null || true)"
      if [ -n "${auth_url}" ]; then
        echo "TAILSCALE_AUTH_URL=${auth_url}"
      fi
    fi

    backend_state="$(printf '%s' "${status_json}" | jq -r '.BackendState // ""' 2>/dev/null || true)"
    dns_name="$(printf '%s' "${status_json}" | jq -r '.Self.DNSName // ""' 2>/dev/null | sed 's/\.$//' || true)"

    if [ "${backend_state}" = "Running" ] && [ -n "${dns_name}" ]; then
      backend_port="$(swarm_backend_port)"
      if [ -z "${backend_port}" ]; then
        echo "[swarm-container] could not derive backend port from SWARMD_LISTEN=${SWARMD_LISTEN}" >&2
        exit 1
      fi
      echo "SWARM_TAILNET_BACKEND_URL=http://${dns_name}:${backend_port}"
      if [ "${served}" != "1" ]; then
        tailscale --socket="${TS_SOCKET}" serve --bg "${SWARM_DESKTOP_PORT}" >"${TS_SERVE_LOG}" 2>&1 || {
          echo "[swarm-container] tailscale serve failed" >&2
          cat "${TS_SERVE_LOG}" >&2 || true
          exit 1
        }
        served=1
      fi
      echo "SWARM_TAILNET_URL=https://${dns_name}"
      break
    fi
  fi

  if [ -n "${TS_UP_PID:-}" ] && ! kill -0 "${TS_UP_PID}" 2>/dev/null; then
    ts_up_status=0
    wait "${TS_UP_PID}" || ts_up_status="$?"
    TS_UP_PID=""
    if [ "${backend_state}" != "Running" ] && [ -z "${auth_url}" ]; then
      echo "[swarm-container] tailscale up exited before auth URL or running state (status=${ts_up_status})" >&2
      cat "${TS_UP_LOG}" >&2 || true
    fi
  fi

  if ! kill -0 "${SWARMD_PID}" 2>/dev/null; then
    echo "[swarm-container] swarmd exited unexpectedly" >&2
    exit 1
  fi
  if ! kill -0 "${TAILSCALED_PID}" 2>/dev/null; then
    echo "[swarm-container] tailscaled exited unexpectedly" >&2
    exit 1
  fi

  sleep 1
done

wait "${SWARMD_PID}"
