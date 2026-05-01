#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/lib-dev.sh"

mkdir -p "${STATE_ROOT}" "${DATA_DIR}" "${BIN_DIR}"

if curl -fsS "${ADDR}/healthz" >/dev/null 2>&1; then
  echo "swarmd already reachable at ${ADDR} (lane=${SWARM_LANE})"
  record_port_assignment
  exit 0
fi

if daemon_running; then
  echo "swarmd already running (pid=$(read_pid), addr=${ADDR}, lane=${SWARM_LANE})"
  record_port_assignment
  exit 0
fi

if [[ ! -x "${BIN_DIR}/swarmd" || ! -x "${BIN_DIR}/swarm-fff-search" ]]; then
  echo "required swarmd binaries missing under ${BIN_DIR}; building first"
  "${SCRIPT_DIR}/dev-build.sh"
fi

echo "starting swarmd on ${LISTEN} (lane=${SWARM_LANE})"
desktop_port="${SWARM_DESKTOP_PORT}"
if [[ "${SWARMD_DISABLE_DESKTOP:-0}" == "1" ]]; then
  desktop_port="0"
fi
bypass_permissions="${SWARM_BYPASS_PERMISSIONS:-false}"
nohup "${BIN_DIR}/swarmd" \
  --listen "${LISTEN}" \
  --desktop-port "${desktop_port}" \
  --bypass-permissions="${bypass_permissions}" \
  --data-dir "${DATA_DIR}" \
  --db-path "${DB_PATH}" \
  --lock-path "${LOCK_PATH}" \
  --cwd "${STARTUP_CWD}" </dev/null >>"${LOG_FILE}" 2>&1 &

pid="$!"
echo "${pid}" >"${PID_FILE}"

if wait_for_health 100; then
  record_port_assignment
  echo "swarmd ready (pid=${pid}, addr=${ADDR}, lane=${SWARM_LANE})"
  echo "log: ${LOG_FILE}"
  exit 0
fi

echo "swarmd failed to become healthy; tailing log" >&2
tail -n 80 "${LOG_FILE}" >&2 || true
kill "${pid}" >/dev/null 2>&1 || true
rm -f "${PID_FILE}"
clear_port_assignment
exit 1
