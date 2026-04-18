#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/lib-dev.sh"

declare -A target_pids=()

if daemon_running; then
  pid="$(read_pid)"
  if [[ -n "${pid}" ]]; then
    target_pids["${pid}"]=1
  fi
fi

while IFS= read -r pid; do
  if [[ -n "${pid}" ]]; then
    target_pids["${pid}"]=1
  fi
done < <(listener_swarmd_pids)

while IFS= read -r pid; do
  if [[ -n "${pid}" ]]; then
    target_pids["${pid}"]=1
  fi
done < <(cmdline_swarmd_pids)

if ((${#target_pids[@]} == 0)); then
  rm -f "${PID_FILE}"
  clear_port_assignment
  echo "swarmd is not running (lane=${SWARM_LANE})"
  exit 0
fi

for pid in "${!target_pids[@]}"; do
  echo "stopping swarmd (pid=${pid}, lane=${SWARM_LANE})"
  kill "${pid}" >/dev/null 2>&1 || true
done

for pid in "${!target_pids[@]}"; do
  attempts=30
  while ((attempts > 0)); do
    if ! kill -0 "${pid}" >/dev/null 2>&1; then
      break
    fi
    attempts=$((attempts - 1))
    sleep 0.2
  done
  if kill -0 "${pid}" >/dev/null 2>&1; then
    echo "swarmd (pid=${pid}) still alive after timeout; sending SIGKILL"
    kill -9 "${pid}" >/dev/null 2>&1 || true
  fi
done

rm -f "${PID_FILE}"
clear_port_assignment
echo "swarmd stopped (lane=${SWARM_LANE})"
