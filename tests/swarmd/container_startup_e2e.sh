#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
IMAGE_NAME="swarm-container-mvp:latest"
CONTAINER_NAME="swarm-container-mvp-no-mode"
TS_VOL="swarm-mvp-ts-state-no-mode"
DATA_VOL="swarm-mvp-data-no-mode"
CONFIG_VOL="swarm-mvp-config-no-mode"
CACHE_VOL="swarm-mvp-cache-no-mode"
LOG_VOL="swarm-mvp-logs-no-mode"

cleanup() {
  podman rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
}

extract_auth_url() {
  local name="$1"
  podman logs "${name}" 2>&1 | sed -n \
    -e 's/^TAILSCALE_AUTH_URL=//p' \
    -e 's/.*AuthURL is \(https:\/\/login.tailscale.com\/a\/[A-Za-z0-9]*\).*/\1/p' | tail -n 1
}

wait_for_auth_url() {
  local name="$1"
  for _ in $(seq 1 30); do
    local auth_url
    auth_url="$(extract_auth_url "${name}")"
    if [[ -n "${auth_url}" ]]; then
      printf '%s\n' "${auth_url}"
      return 0
    fi
    sleep 1
  done
  return 1
}

wait_for_config() {
  local name="$1"
  for _ in $(seq 1 30); do
    if podman exec "${name}" sh -lc 'test -f /etc/swarmd/swarm.conf' >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "timed out waiting for /etc/swarmd/swarm.conf in ${name}" >&2
  return 1
}

assert_contains() {
  local haystack="$1"
  local needle="$2"
  local context="$3"
  if [[ "${haystack}" != *"${needle}"* ]]; then
    echo "assertion failed: expected ${context} to contain: ${needle}" >&2
    return 1
  fi
}

assert_not_contains() {
  local haystack="$1"
  local needle="$2"
  local context="$3"
  if [[ "${haystack}" == *"${needle}"* ]]; then
    echo "assertion failed: expected ${context} not to contain: ${needle}" >&2
    return 1
  fi
}

run_case() {
  podman rm -f "${CONTAINER_NAME}" >/dev/null 2>&1 || true
  podman volume rm "${TS_VOL}" "${DATA_VOL}" "${CONFIG_VOL}" "${CACHE_VOL}" "${LOG_VOL}" >/dev/null 2>&1 || true

  podman run -d \
    --name "${CONTAINER_NAME}" \
    -v "${TS_VOL}:/var/lib/tailscale" \
    -v "${DATA_VOL}:/var/lib/swarmd" \
    -v "${CONFIG_VOL}:/etc/swarmd" \
    -v "${CACHE_VOL}:/var/cache/swarmd" \
    -v "${LOG_VOL}:/var/log/swarmd" \
    localhost/${IMAGE_NAME} >/dev/null

  wait_for_config "${CONTAINER_NAME}"
  sleep 2

  local config
  config="$(podman exec "${CONTAINER_NAME}" sh -lc 'cat /etc/swarmd/swarm.conf')"
  local processes
  processes="$(podman exec "${CONTAINER_NAME}" sh -lc 'ps -ef | grep -E "swarmd|tailscaled" | grep -v grep')"
  local logs
  logs="$(podman logs "${CONTAINER_NAME}" 2>&1 || true)"
  local auth_url
  auth_url="$(wait_for_auth_url "${CONTAINER_NAME}" || true)"

  assert_not_contains "${config}" "mode =" "${CONTAINER_NAME} config"
  assert_contains "${config}" "host = 127.0.0.1" "${CONTAINER_NAME} config"
  assert_contains "${config}" "port = 7781" "${CONTAINER_NAME} config"
  assert_contains "${config}" "desktop_port = 5555" "${CONTAINER_NAME} config"
  assert_not_contains "${config}" "startup_mode" "${CONTAINER_NAME} config"
  assert_not_contains "${processes}" "--mode=" "${CONTAINER_NAME} process list"
  assert_not_contains "${logs}" "startup mode" "${CONTAINER_NAME} logs"
  assert_contains "${processes}" "tailscaled --tun=userspace-networking" "${CONTAINER_NAME} process list"

  printf '=== %s ===\n' "${CONTAINER_NAME}"
  printf 'auth_url=%s\n' "${auth_url}"
  printf '%s\n' "${config}"
  printf '%s\n' "${processes}"
  printf '\n'
}

cleanup
trap cleanup EXIT
cd "${ROOT_DIR}"
podman build -f deploy/container-mvp/Containerfile -t "${IMAGE_NAME}" . >/dev/null
run_case
