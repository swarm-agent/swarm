#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/testbench-e2e-tunnel.sh <check|run> [command [args...]]

Loads the ignored repository-root .env (or SWARM_TESTBENCH_ENV_FILE), validates
that it contains only the supported non-secret alias/port settings, and opens
loopback-only SSH forwards to the testbench Desktop and API listeners.

Commands:
  check  Validate .env, SSH alias resolution, remote loopback listeners, and
         local port availability without opening a persistent tunnel.
  run    Open the forwards, export SWARM_DESKTOP_URL and SWARM_PRIMARY_API_URL,
         then run the supplied command. With no command, wait until interrupted.

Optional reverse forwarding is enabled only when both
SWARM_TESTBENCH_REVERSE_LOCAL_PORT and SWARM_TESTBENCH_REVERSE_REMOTE_PORT are set.
USAGE
}

fail() {
  printf 'testbench-e2e-tunnel: %s\n' "$*" >&2
  exit 1
}

[[ $# -ge 1 ]] || { usage; exit 2; }
ACTION="$1"
shift
case "${ACTION}" in
  check|run) ;;
  -h|--help) usage; exit 0 ;;
  *) usage; exit 2 ;;
esac

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib-testbench-e2e.sh
source "${ROOT_DIR}/scripts/lib-testbench-e2e.sh"
swarm_testbench_load_env "${ROOT_DIR}" || exit 1
swarm_testbench_validate_env || exit 1

command -v ssh >/dev/null 2>&1 || fail "ssh is required"
ssh -G "${SWARM_PRIMARY_SSH}" >/dev/null 2>&1 || fail "SSH alias ${SWARM_PRIMARY_SSH} does not resolve"

if swarm_testbench_port_open "${SWARM_TESTBENCH_LOCAL_DESKTOP_PORT}"; then
  fail "local Desktop port ${SWARM_TESTBENCH_LOCAL_DESKTOP_PORT} is already in use"
fi
if swarm_testbench_port_open "${SWARM_TESTBENCH_LOCAL_API_PORT}"; then
  fail "local API port ${SWARM_TESTBENCH_LOCAL_API_PORT} is already in use"
fi

remote_ports="${SWARM_REMOTE_DESKTOP_PORT},${SWARM_TESTBENCH_REMOTE_API_PORT}"
remote_state="$(ssh "${SWARM_PRIMARY_SSH}" 'bash -s' -- "${SWARM_REMOTE_DESKTOP_PORT}" "${SWARM_TESTBENCH_REMOTE_API_PORT}" <<'REMOTE_CHECK'
set -euo pipefail
for port in "$@"; do
  if (exec 3<>"/dev/tcp/127.0.0.1/${port}") >/dev/null 2>&1; then
    printf '%s=open\n' "${port}"
  else
    printf '%s=closed\n' "${port}"
  fi
done
REMOTE_CHECK
)" || fail "could not inspect remote loopback listeners through ${SWARM_PRIMARY_SSH}"
printf '%s\n' "${remote_state}"
grep -qx "${SWARM_REMOTE_DESKTOP_PORT}=open" <<<"${remote_state}" || fail "remote Desktop loopback port ${SWARM_REMOTE_DESKTOP_PORT} is not listening"
grep -qx "${SWARM_TESTBENCH_REMOTE_API_PORT}=open" <<<"${remote_state}" || fail "remote API loopback port ${SWARM_TESTBENCH_REMOTE_API_PORT} is not listening"

printf 'testbench-e2e-tunnel: alias=%s desktop=http://127.0.0.1:%s api=http://127.0.0.1:%s remote_ports=%s\n' \
  "${SWARM_PRIMARY_SSH}" "${SWARM_TESTBENCH_LOCAL_DESKTOP_PORT}" "${SWARM_TESTBENCH_LOCAL_API_PORT}" "${remote_ports}"
if [[ "${ACTION}" == "check" ]]; then
  exit 0
fi

swarm_testbench_tunnel_args
ssh "${SWARM_TESTBENCH_TUNNEL_ARGS[@]}" &
tunnel_pid=$!
cleanup() {
  kill "${tunnel_pid}" 2>/dev/null || true
  wait "${tunnel_pid}" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

swarm_testbench_wait_for_port "${SWARM_TESTBENCH_LOCAL_DESKTOP_PORT}" "${tunnel_pid}" || fail "Desktop forward did not become ready"
swarm_testbench_wait_for_port "${SWARM_TESTBENCH_LOCAL_API_PORT}" "${tunnel_pid}" || fail "API forward did not become ready"

export SWARM_DESKTOP_URL="http://127.0.0.1:${SWARM_TESTBENCH_LOCAL_DESKTOP_PORT}"
export SWARM_PRIMARY_API_URL="http://127.0.0.1:${SWARM_TESTBENCH_LOCAL_API_PORT}"
export SWARM_REMOTE_DESKTOP_PORT SWARM_PRIMARY_SSH
printf 'testbench-e2e-tunnel: ready SWARM_DESKTOP_URL=%s SWARM_PRIMARY_API_URL=%s\n' "${SWARM_DESKTOP_URL}" "${SWARM_PRIMARY_API_URL}"

if [[ $# -eq 0 ]]; then
  wait "${tunnel_pid}"
else
  "$@"
fi
