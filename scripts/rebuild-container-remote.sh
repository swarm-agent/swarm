#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
GO_LIB="${ROOT_DIR}/scripts/lib-go.sh"

IMAGE_NAME="${IMAGE_NAME:-localhost/swarm-container-mvp:latest}"
CONTAINER_NAME="${CONTAINER_NAME:-swarm-container-mvp}"
TS_VOLUME="${TS_VOLUME:-swarm-mvp-ts-state}"
DATA_VOLUME="${DATA_VOLUME:-swarm-mvp-data}"
CONFIG_VOLUME="${CONFIG_VOLUME:-swarm-mvp-config}"
CACHE_VOLUME="${CACHE_VOLUME:-swarm-mvp-cache}"
LOG_VOLUME="${LOG_VOLUME:-swarm-mvp-logs}"
PRIMARY_REPO_HOST="${PRIMARY_REPO_HOST:-${ROOT_DIR}}"
PRIMARY_REPO_CONTAINER_PATH="${PRIMARY_REPO_CONTAINER_PATH:-/workspaces/swarm-go}"
STARTUP_MODE="${STARTUP_MODE:-box}"
TAILSCALE_STATE_MOUNT="${TAILSCALE_STATE_MOUNT:-/var/lib/tailscale}"
SWARMD_DATA_MOUNT="${SWARMD_DATA_MOUNT:-/var/lib/swarmd}"
SWARMD_CONFIG_MOUNT="${SWARMD_CONFIG_MOUNT:-/etc/swarmd}"
SWARMD_CACHE_MOUNT="${SWARMD_CACHE_MOUNT:-/var/cache/swarmd}"
SWARMD_LOG_MOUNT="${SWARMD_LOG_MOUNT:-/var/log/swarmd}"
SHUTDOWN_REASON="${SWARM_REBUILD_REASON:-swarm-container-rebuild}"
TS_HOSTNAME="${TS_HOSTNAME:-swarm-box}"
SWARM_WEBAUTH_ENABLED="${SWARM_WEBAUTH_ENABLED:-}"
GO_CACHE_ROOT="${GO_CACHE_ROOT:-${ROOT_DIR}/.cache/go}"
GOCACHE_DIR="${GOCACHE_DIR:-${GO_CACHE_ROOT}/build}"
GOMODCACHE_DIR="${GOMODCACHE_DIR:-${GO_CACHE_ROOT}/mod}"
GOPATH_DIR="${GOPATH_DIR:-${GO_CACHE_ROOT}/path}"
BIN_DIR="${BIN_DIR:-${ROOT_DIR}/.bin/main}"

for required in podman npm; do
  command -v "${required}" > /dev/null 2>&1 || {
    echo "missing required command: ${required}" >&2
    exit 1
  }
done

if [[ ! -f "${GO_LIB}" ]]; then
  echo "missing go resolver script at ${GO_LIB}" >&2
  exit 1
fi
# shellcheck disable=SC1091
source "${GO_LIB}"
swarm_require_go "${ROOT_DIR}"

ensure_podman_volume() {
  local volume_name="$1"
  if podman volume exists "${volume_name}"; then
    return
  fi
  podman volume create "${volume_name}" > /dev/null
}

build_container_artifacts() {
  mkdir -p "${BIN_DIR}" "${GOCACHE_DIR}" "${GOMODCACHE_DIR}" "${GOPATH_DIR}"

  echo "[rebuild-container-remote] building backend binaries"
  (
    cd "${ROOT_DIR}/swarmd"
    GOCACHE="${GOCACHE_DIR}" \
    GOMODCACHE="${GOMODCACHE_DIR}" \
    GOPATH="${GOPATH_DIR}" \
    GOTOOLCHAIN="${GOTOOLCHAIN}" \
    "${GO_BIN}" build -trimpath -o "${BIN_DIR}/swarmd" ./cmd/swarmd

    GOCACHE="${GOCACHE_DIR}" \
    GOMODCACHE="${GOMODCACHE_DIR}" \
    GOPATH="${GOPATH_DIR}" \
    GOTOOLCHAIN="${GOTOOLCHAIN}" \
    "${GO_BIN}" build -trimpath -o "${BIN_DIR}/swarmctl" ./cmd/swarmctl

    GOCACHE="${GOCACHE_DIR}" \
    GOMODCACHE="${GOMODCACHE_DIR}" \
    GOPATH="${GOPATH_DIR}" \
    GOTOOLCHAIN="${GOTOOLCHAIN}" \
    "${GO_BIN}" build -trimpath -o "${BIN_DIR}/swarm-fff-search" ./cmd/swarm-fff-search
  )

  echo "[rebuild-container-remote] building web assets"
  (
    cd "${ROOT_DIR}/web"
    npm run build
  )
}

request_container_shutdown() {
  local api_base_url="http://127.0.0.1:7781"
  local ready_url="${api_base_url}/readyz"
  local attach_token_url="${api_base_url}/v1/auth/attach/token"
  local ready_code="000"
  local token_value=""
  local shutdown_code="000"
  local shutdown_confirmed=0

  if ! podman ps --format '{{.Names}}' | grep -Fx "${CONTAINER_NAME}" > /dev/null 2>&1; then
    return
  fi

  if ready_code="$(podman exec "${CONTAINER_NAME}" sh -lc "curl -sS --connect-timeout 1 --max-time 2 -o /dev/null -w '%{http_code}' '${ready_url}'" 2>/dev/null)"; then
    :
  else
    ready_code="000"
  fi

  if [[ "${ready_code}" != "200" ]]; then
    echo "[rebuild-container-remote] container not ready (readyz=${ready_code}); continuing with forced removal"
    return
  fi

  if token_value="$(podman exec "${CONTAINER_NAME}" sh -lc "curl -sS --connect-timeout 2 --max-time 6 '${attach_token_url}'" 2>/dev/null | sed -n 's/.*\"token\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p' | head -n 1)"; then
    :
  else
    token_value=""
  fi

  echo "[rebuild-container-remote] requesting graceful shutdown for ${CONTAINER_NAME}"
  if [[ -n "${token_value}" ]]; then
    if shutdown_code="$({
      podman exec \
        -e SWARM_REBUILD_TOKEN="${token_value}" \
        -e SWARM_REBUILD_REASON="${SHUTDOWN_REASON}" \
        "${CONTAINER_NAME}" \
        sh -lc '
          curl -sS \
            --connect-timeout 2 \
            --max-time 8 \
            -o /dev/null \
            -w "%{http_code}" \
            -X POST "http://127.0.0.1:7781/v1/system/shutdown" \
            -H "Accept: application/json" \
            -H "Content-Type: application/json" \
            -H "X-Swarm-Token: ${SWARM_REBUILD_TOKEN}" \
            --data "{\"reason\":\"${SWARM_REBUILD_REASON}\"}"
        ' 2>/dev/null
    })"; then
      :
    else
      shutdown_code="000"
    fi
  else
    if shutdown_code="$({
      podman exec \
        -e SWARM_REBUILD_REASON="${SHUTDOWN_REASON}" \
        "${CONTAINER_NAME}" \
        sh -lc '
          curl -sS \
            --connect-timeout 2 \
            --max-time 8 \
            -o /dev/null \
            -w "%{http_code}" \
            -X POST "http://127.0.0.1:7781/v1/system/shutdown" \
            -H "Accept: application/json" \
            -H "Content-Type: application/json" \
            --data "{\"reason\":\"${SWARM_REBUILD_REASON}\"}"
        ' 2>/dev/null
    })"; then
      :
    else
      shutdown_code="000"
    fi
  fi

  if [[ "${shutdown_code}" != "202" ]]; then
    echo "[rebuild-container-remote] graceful shutdown request failed (status=${shutdown_code}); continuing with forced removal" >&2
    return
  fi

  for _ in $(seq 1 30); do
    if ready_code="$(podman exec "${CONTAINER_NAME}" sh -lc "curl -sS --connect-timeout 1 --max-time 2 -o /dev/null -w '%{http_code}' '${ready_url}'" 2>/dev/null)"; then
      :
    else
      ready_code="000"
    fi
    if [[ "${ready_code}" == "503" || "${ready_code}" == "000" ]]; then
      shutdown_confirmed=1
      break
    fi
    sleep 0.1
  done

  if [[ "${shutdown_confirmed}" == "1" ]]; then
    echo "[rebuild-container-remote] graceful shutdown confirmed"
  else
    echo "[rebuild-container-remote] shutdown accepted but daemon stayed ready; continuing with forced removal" >&2
  fi
}

print_container_access_details() {
  local auth_url=""
  local tailnet_url=""
  local log_output=""

  for _ in $(seq 1 30); do
    if log_output="$(podman logs "${CONTAINER_NAME}" 2>&1 || true)"; then
      auth_url="$(printf '%s\n' "${log_output}" | sed -n 's/^TAILSCALE_AUTH_URL=//p' | tail -n 1)"
      tailnet_url="$(printf '%s\n' "${log_output}" | sed -n 's/^SWARM_TAILNET_URL=//p' | tail -n 1)"
    fi
    if [[ -n "${auth_url}" || -n "${tailnet_url}" ]]; then
      break
    fi
    sleep 1
  done

  if [[ -n "${auth_url}" ]]; then
    echo "[rebuild-container-remote] tailscale login: ${auth_url}"
  fi
  if [[ -n "${tailnet_url}" ]]; then
    echo "[rebuild-container-remote] swarm desktop url: ${tailnet_url}"
  fi
  if [[ -z "${auth_url}" && -z "${tailnet_url}" ]]; then
    echo "[rebuild-container-remote] tailscale url not ready yet; check with: podman logs -f ${CONTAINER_NAME}"
  elif [[ -n "${auth_url}" && -z "${tailnet_url}" ]]; then
    echo "[rebuild-container-remote] after approving the login, rerun: podman logs --tail 200 ${CONTAINER_NAME}"
  fi
}

for required_path in \
  "${PRIMARY_REPO_HOST}" \
  "${ROOT_DIR}/deploy/container-mvp/Containerfile.base" \
  "${ROOT_DIR}/deploy/container-mvp/Containerfile" \
  "${ROOT_DIR}/swarmd/internal/fff/lib/linux-amd64-gnu/libfff_c.so" \
  "${ROOT_DIR}/web/package.json"
do
  if [[ ! -e "${required_path}" ]]; then
    echo "missing required path: ${required_path}" >&2
    exit 1
  fi
done

echo "[rebuild-container-remote] rebuilding binaries and desktop assets"
build_container_artifacts

echo "[rebuild-container-remote] ensuring podman volumes"
ensure_podman_volume "${TS_VOLUME}"
ensure_podman_volume "${DATA_VOLUME}"
ensure_podman_volume "${CONFIG_VOLUME}"
ensure_podman_volume "${CACHE_VOLUME}"
ensure_podman_volume "${LOG_VOLUME}"

echo "[rebuild-container-remote] rebuilding image ${IMAGE_NAME}"
(
  cd "${ROOT_DIR}"
  base_image_name="${SWARM_BASE_IMAGE_NAME:-localhost/swarm-container-base:latest}"
  podman build -f deploy/container-mvp/Containerfile.base -t "${base_image_name}" .
  podman build \
    --build-arg "SWARM_BASE_IMAGE=${base_image_name}" \
    --label "swarmagent.image.contract=swarm.container.v1" \
    --label "swarmagent.image.role=app" \
    --label "swarmagent.image.base.ref=${base_image_name}" \
    -f deploy/container-mvp/Containerfile \
    -t "${IMAGE_NAME}" \
    .
)

request_container_shutdown

echo "[rebuild-container-remote] replacing container ${CONTAINER_NAME}"
podman rm -f "${CONTAINER_NAME}" > /dev/null 2>&1 || true

podman_args=(
  run -d
  --name "${CONTAINER_NAME}"
  --security-opt=no-new-privileges
  --cap-drop=ALL
  -v "${TS_VOLUME}:${TAILSCALE_STATE_MOUNT}:Z"
  -v "${DATA_VOLUME}:${SWARMD_DATA_MOUNT}:Z"
  -v "${CONFIG_VOLUME}:${SWARMD_CONFIG_MOUNT}:Z"
  -v "${CACHE_VOLUME}:${SWARMD_CACHE_MOUNT}:Z"
  -v "${LOG_VOLUME}:${SWARMD_LOG_MOUNT}:Z"
  -v "${PRIMARY_REPO_HOST}:${PRIMARY_REPO_CONTAINER_PATH}:Z"
  -e "SWARM_STARTUP_MODE=${STARTUP_MODE}"
  -e "TS_HOSTNAME=${TS_HOSTNAME}"
)

if [[ -n "${SWARM_WEBAUTH_ENABLED}" ]]; then
  podman_args+=( -e "SWARM_WEBAUTH_ENABLED=${SWARM_WEBAUTH_ENABLED}" )
fi

podman_args+=( "${IMAGE_NAME}" )
podman "${podman_args[@]}"

echo "[rebuild-container-remote] container started"
podman ps --filter "name=${CONTAINER_NAME}" --format '{{.Names}} {{.Image}} {{.Status}}'
print_container_access_details
