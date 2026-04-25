#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
# shellcheck disable=SC1091
source "${ROOT_DIR}/scripts/lib-lane.sh"

LANE="${SWARM_LANE:-$(swarm_lane_default)}"
swarm_lane_export_profile "${LANE}" "${ROOT_DIR}"

IMAGE_NAME="${IMAGE_NAME:-localhost/swarm-container-mvp:latest}"
CONTAINER_NAME="${CONTAINER_NAME:-swarm-container-mvp}"
TS_VOLUME="${TS_VOLUME:-swarm-mvp-ts-state}"
DATA_VOLUME="${DATA_VOLUME:-swarm-mvp-data}"
CONFIG_VOLUME="${CONFIG_VOLUME:-swarm-mvp-config}"
BUILD_RUNTIME="${BUILD_RUNTIME:-podman}"
PRIMARY_REPO_HOST="${PRIMARY_REPO_HOST:-${ROOT_DIR}}"
PRIMARY_REPO_CONTAINER_PATH="${PRIMARY_REPO_CONTAINER_PATH:-/workspaces/swarm-go}"
STARTUP_MODE="${STARTUP_MODE:-box}"
TAILSCALE_STATE_MOUNT="${TAILSCALE_STATE_MOUNT:-/var/lib/tailscale}"
SWARMD_DATA_MOUNT="${SWARMD_DATA_MOUNT:-/var/lib/swarmd}"
CONTAINER_HOME_USER="${CONTAINER_HOME_USER:-root}"
CONTAINER_CONFIG_HOME="${CONTAINER_CONFIG_HOME:-/${CONTAINER_HOME_USER}/.config}"
SWARM_CONFIG_MOUNT="${SWARM_CONFIG_MOUNT:-${CONTAINER_CONFIG_HOME}/swarm}"
SHUTDOWN_REASON="${SWARM_REBUILD_REASON:-swarm-container-rebuild}"
IMAGE_ONLY=0
SKIP_LOCAL_ARTIFACT_REBUILD="${SWARM_SKIP_LOCAL_ARTIFACT_REBUILD:-0}"
DEV_IMAGE_FINGERPRINT="${SWARM_CONTAINER_DEV_FINGERPRINT:-}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --image-only)
      IMAGE_ONLY=1
      ;;
    *)
      echo "usage: $0 [--image-only]" >&2
      exit 2
      ;;
  esac
  shift
done

if [[ "${IMAGE_ONLY}" != "1" && "${BUILD_RUNTIME}" != "podman" ]]; then
  echo "BUILD_RUNTIME=${BUILD_RUNTIME} is only supported with --image-only" >&2
  exit 2
fi

for required in "${BUILD_RUNTIME}"; do
  command -v "${required}" > /dev/null 2>&1 || {
    echo "missing required command: ${required}" >&2
    exit 1
  }
done

ensure_podman_volume() {
  local volume_name="$1"
  if podman volume exists "${volume_name}"; then
    return
  fi
  podman volume create "${volume_name}" > /dev/null
}

stage_container_binaries() {
  local source_bin_dir="${SWARM_BIN_DIR}"
  local target_bin_dir="${ROOT_DIR}/.bin/main"
  mkdir -p "${target_bin_dir}"

  if [[ ! -x "${source_bin_dir}/swarmd" || ! -x "${source_bin_dir}/swarmctl" ]]; then
    source_bin_dir="${ROOT_DIR}/.bin/main"
  fi

  for binary_name in swarmd swarmctl; do
    local source_path="${source_bin_dir}/${binary_name}"
    local target_path="${target_bin_dir}/${binary_name}"
    if [[ ! -x "${source_path}" ]]; then
      echo "missing required lane binary: ${source_path}" >&2
      exit 1
    fi
    if [[ "$(realpath -m "${source_path}")" == "$(realpath -m "${target_path}")" ]]; then
      continue
    fi
    install -m 0755 "${source_path}" "${target_path}"
  done
}

build_web_assets() {
  local node_major="0"
  if command -v node >/dev/null 2>&1; then
    node_major="$(node -p 'Number(process.versions.node.split(".")[0])' 2>/dev/null || printf '0')"
  fi

  if [[ "${node_major}" =~ ^[0-9]+$ ]] && ((node_major >= 20)); then
    npm run build
    return
  fi

  npm exec --yes --package=node@22 -- npm run build
}

build_local_artifacts_without_restart() {
  echo "[rebuild-container] building backend binaries without restarting local swarmd"
  (
    cd "${ROOT_DIR}"
    SWARMD_BUILD_HARD_RESTART=0 ./swarmd/scripts/dev-build.sh
  )

  echo "[rebuild-container] building local swarm binaries and desktop assets"
  (
    cd "${ROOT_DIR}"
    ./scripts/dev-build.sh
    cd web
    build_web_assets
  )
}

request_container_shutdown() {
  local api_base_url="http://127.0.0.1:7781"
  local ready_url="${api_base_url}/readyz"
  local attach_token_url="${api_base_url}/v1/auth/attach/token"
  local shutdown_url="${api_base_url}/v1/system/shutdown"
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
    echo "[rebuild-container] container not ready (readyz=${ready_code}); continuing with forced removal"
    return
  fi

  if token_value="$(podman exec "${CONTAINER_NAME}" sh -lc "curl -sS --connect-timeout 2 --max-time 6 '${attach_token_url}'" 2>/dev/null | sed -n 's/.*\"token\"[[:space:]]*:[[:space:]]*\"\([^\"]*\)\".*/\1/p' | head -n 1)"; then
    :
  else
    token_value=""
  fi

  echo "[rebuild-container] requesting graceful shutdown for ${CONTAINER_NAME}"
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
    echo "[rebuild-container] graceful shutdown request failed (status=${shutdown_code}); continuing with forced removal" >&2
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
    echo "[rebuild-container] graceful shutdown confirmed"
  else
    echo "[rebuild-container] shutdown accepted but daemon stayed ready; continuing with forced removal" >&2
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
    echo "[rebuild-container] tailscale login: ${auth_url}"
  fi
  if [[ -n "${tailnet_url}" ]]; then
    echo "[rebuild-container] swarm desktop url: ${tailnet_url}"
  fi
  if [[ -z "${auth_url}" && -z "${tailnet_url}" ]]; then
    echo "[rebuild-container] tailscale url not ready yet; check with: podman logs -f ${CONTAINER_NAME}"
  elif [[ -n "${auth_url}" && -z "${tailnet_url}" ]]; then
    echo "[rebuild-container] after approving the login, rerun: podman logs --tail 200 ${CONTAINER_NAME}"
  fi
}

for required_path in \
  "${PRIMARY_REPO_HOST}" \
  "${ROOT_DIR}/deploy/container-mvp/Containerfile.base" \
  "${ROOT_DIR}/deploy/container-mvp/Containerfile"
do
  if [[ ! -e "${required_path}" ]]; then
    echo "missing required path: ${required_path}" >&2
    exit 1
  fi
done

if [[ "${SKIP_LOCAL_ARTIFACT_REBUILD}" == "1" ]]; then
  echo "[rebuild-container] skipping local artifact rebuild; using current staged build inputs"
else
  echo "[rebuild-container] rebuilding binaries and desktop assets"
  build_local_artifacts_without_restart
fi

echo "[rebuild-container] staging lane binaries for container image"
stage_container_binaries

echo "[rebuild-container] rebuilding image ${IMAGE_NAME}"
(
  cd "${ROOT_DIR}"
  base_image_name="${SWARM_BASE_IMAGE_NAME:-localhost/swarm-container-base:latest}"
  build_args=()
  build_args+=(
    --label "swarmagent.image.contract=swarm.container.v1"
    --label "swarmagent.image.role=app"
    --label "swarmagent.image.base.ref=${base_image_name}"
  )
  if [[ -n "${DEV_IMAGE_FINGERPRINT}" ]]; then
    build_args+=(
      --label "swarmagent.dev-mode=true"
      --label "swarmagent.dev-fingerprint=${DEV_IMAGE_FINGERPRINT}"
    )
  fi
  "${BUILD_RUNTIME}" build -f deploy/container-mvp/Containerfile.base -t "${base_image_name}" .
  "${BUILD_RUNTIME}" build "${build_args[@]}" --build-arg "SWARM_BASE_IMAGE=${base_image_name}" -f deploy/container-mvp/Containerfile -t "${IMAGE_NAME}" .
)

if [[ "${IMAGE_ONLY}" == "1" ]]; then
  echo "[rebuild-container] image build complete"
  exit 0
fi

echo "[rebuild-container] ensuring podman volumes"
ensure_podman_volume "${TS_VOLUME}"
ensure_podman_volume "${DATA_VOLUME}"
ensure_podman_volume "${CONFIG_VOLUME}"

request_container_shutdown

echo "[rebuild-container] replacing container ${CONTAINER_NAME}"
podman rm -f "${CONTAINER_NAME}" > /dev/null 2>&1 || true
podman run -d \
  --name "${CONTAINER_NAME}" \
  --security-opt=no-new-privileges \
  --cap-drop=ALL \
  -v "${TS_VOLUME}:${TAILSCALE_STATE_MOUNT}:Z" \
  -v "${DATA_VOLUME}:${SWARMD_DATA_MOUNT}:Z" \
  -v "${CONFIG_VOLUME}:${SWARM_CONFIG_MOUNT}:Z" \
  -v "${PRIMARY_REPO_HOST}:${PRIMARY_REPO_CONTAINER_PATH}:Z" \
  -e SWARM_STARTUP_MODE="${STARTUP_MODE}" \
  "${IMAGE_NAME}"

echo "[rebuild-container] container started"
podman ps --filter "name=${CONTAINER_NAME}" --format '{{.Names}} {{.Image}} {{.Status}}'
print_container_access_details
