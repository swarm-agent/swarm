#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
IMAGE_NAME="swarm-container-mvp:latest"
INTERACTIVE_NAME="swarm-container-mvp-interactive"
BOX_NAME="swarm-container-mvp-box"
INTERACTIVE_TS_VOL="swarm-mvp-ts-state-interactive"
INTERACTIVE_DATA_VOL="swarm-mvp-data-interactive"
BOX_TS_VOL="swarm-mvp-ts-state-box"
BOX_DATA_VOL="swarm-mvp-data-box"

cleanup() {
  podman rm -f "${INTERACTIVE_NAME}" >/dev/null 2>&1 || true
  podman rm -f "${BOX_NAME}" >/dev/null 2>&1 || true
}

extract_auth_url() {
  local name="$1"
  podman logs "${name}" 2>&1 | sed -n 's/.*AuthURL is \(https:\/\/login.tailscale.com\/a\/[A-Za-z0-9]*\).*/\1/p' | tail -n 1
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
    if podman exec "${name}" sh -lc 'test -f "$(cd && pwd)/.config/swarm/swarm.conf"' >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "timed out waiting for swarm.conf in ${name}" >&2
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

run_case() {
  local name="$1"
  local ts_vol="$2"
  local data_vol="$3"
  local startup_mode="$4"
  local expected_config_mode="$5"
  local expected_runtime_mode="$6"

  podman rm -f "${name}" >/dev/null 2>&1 || true
  podman volume rm "${ts_vol}" >/dev/null 2>&1 || true
  podman volume rm "${data_vol}" >/dev/null 2>&1 || true

  podman run -d \
    --name "${name}" \
    -v "${ts_vol}:/var/lib/tailscale" \
    -v "${data_vol}:/var/lib/swarmd" \
    -e SWARM_STARTUP_MODE="${startup_mode}" \
    localhost/${IMAGE_NAME} >/dev/null

  wait_for_config "${name}"
  sleep 2

  local config
  config="$(podman exec "${name}" sh -lc 'cat "$(cd && pwd)/.config/swarm/swarm.conf"')"
  local processes
  processes="$(podman exec "${name}" sh -lc 'ps -ef | grep -E "swarmd|tailscaled" | grep -v grep')"
  local auth_url
  auth_url="$(wait_for_auth_url "${name}" || true)"

  assert_contains "${config}" "mode = ${expected_config_mode}" "${name} config"
  assert_contains "${config}" "host = 127.0.0.1" "${name} config"
  assert_contains "${config}" "port = 7781" "${name} config"
  assert_contains "${config}" "desktop_port = 5555" "${name} config"
  assert_contains "${processes}" "--mode=${expected_runtime_mode}" "${name} process list"
  assert_contains "${processes}" "tailscaled --tun=userspace-networking" "${name} process list"

  printf '=== %s ===\n' "${name}"
  printf 'auth_url=%s\n' "${auth_url}"
  printf '%s\n' "${config}"
  printf '%s\n' "${processes}"
  printf '\n'
}

cleanup
cd "${ROOT_DIR}"
podman build -f deploy/container-mvp/Containerfile -t "${IMAGE_NAME}" . >/dev/null
run_case "${INTERACTIVE_NAME}" "${INTERACTIVE_TS_VOL}" "${INTERACTIVE_DATA_VOL}" "single" "interactive" "single"
run_case "${BOX_NAME}" "${BOX_TS_VOL}" "${BOX_DATA_VOL}" "box" "box" "box"
