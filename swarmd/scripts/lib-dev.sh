#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
SWARMD_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

resolve_project_root() {
  local candidate
  for candidate in "${SWARMD_ROOT}/.." "${SWARMD_ROOT}/../.."; do
    candidate="$(cd -- "${candidate}" 2>/dev/null && pwd || true)"
    if [[ -z "${candidate}" ]]; then
      continue
    fi
    if [[ -f "${candidate}/scripts/lib-lane.sh" ]] || [[ -f "${candidate}/swarmtui/scripts/lib-lane.sh" ]]; then
      printf "%s\n" "${candidate}"
      return 0
    fi
  done
  return 1
}

if ! PROJECT_ROOT="$(resolve_project_root)"; then
  echo "failed to resolve project root with lane resolver from ${SWARMD_ROOT}" >&2
  exit 1
fi

LANE_LIB="${PROJECT_ROOT}/scripts/lib-lane.sh"
if [[ ! -f "${LANE_LIB}" ]]; then
  LANE_LIB="${PROJECT_ROOT}/swarmtui/scripts/lib-lane.sh"
fi
if [[ ! -f "${LANE_LIB}" ]]; then
  echo "missing lane resolver script under ${PROJECT_ROOT}" >&2
  exit 1
fi
# shellcheck disable=SC1091
source "${LANE_LIB}"
lane="$(swarm_lane_default)"
incoming_bypass_permissions="${SWARM_BYPASS_PERMISSIONS-}"
swarm_lane_export_profile "${lane}" "${PROJECT_ROOT}"
if [[ -n "${incoming_bypass_permissions}" ]]; then
  export SWARM_BYPASS_PERMISSIONS="${incoming_bypass_permissions}"
fi

HOST_ROOT="$(cd -- "${PROJECT_ROOT}/.." && pwd)"
GO_LIB="${PROJECT_ROOT}/scripts/lib-go.sh"

BIN_DIR="${BIN_DIR:-${SWARM_BIN_DIR:-${SWARMD_ROOT}/.bin}}"
LISTEN="${LISTEN:-${SWARMD_LISTEN:-127.0.0.1:7781}}"
ADDR="${ADDR:-${SWARMD_URL:-http://${LISTEN}}}"

if [[ -z "${SWARM_LANE_PORT:-}" ]]; then
  if [[ "${LISTEN}" =~ :([0-9]+)$ ]]; then
    SWARM_LANE_PORT="${BASH_REMATCH[1]}"
  else
    SWARM_LANE_PORT=""
  fi
fi

PID_FILE="${PID_FILE:-${STATE_ROOT}/swarmd.pid}"
LOG_FILE="${LOG_FILE:-${STATE_ROOT}/swarmd.log}"
DB_PATH="${DB_PATH:-${DATA_DIR}/swarmd.pebble}"
LOCK_PATH="${LOCK_PATH:-${STATE_ROOT}/swarmd.lock}"
STARTUP_CWD="${STARTUP_CWD:-${PWD}}"

GO_CACHE_ROOT="${GO_CACHE_ROOT:-${PROJECT_ROOT}/.cache/go}"
GOCACHE_DIR="${GOCACHE_DIR:-${GO_CACHE_ROOT}/build}"
GOMODCACHE_DIR="${GOMODCACHE_DIR:-${GO_CACHE_ROOT}/mod}"
GOPATH_DIR="${GOPATH_DIR:-${GO_CACHE_ROOT}/path}"

read_pid() {
  if [[ -f "${PID_FILE}" ]]; then
    tr -d "[:space:]" <"${PID_FILE}" || true
  fi
}

daemon_running() {
  local pid
  pid="$(read_pid)"
  if [[ -z "${pid}" ]]; then
    return 1
  fi
  kill -0 "${pid}" >/dev/null 2>&1
}

require_go() {
  if [[ ! -f "${GO_LIB}" ]]; then
    echo "missing go resolver script under ${PROJECT_ROOT}" >&2
    return 1
  fi
  # shellcheck disable=SC1091
  source "${GO_LIB}"
  swarm_require_go "${PROJECT_ROOT}"
  mkdir -p "${GOCACHE_DIR}" "${GOMODCACHE_DIR}" "${GOPATH_DIR}"
}

run_go() {
  require_go
  CGO_ENABLED=1 \
  GOCACHE="${GOCACHE_DIR}" \
  GOMODCACHE="${GOMODCACHE_DIR}" \
  GOPATH="${GOPATH_DIR}" \
  GOTOOLCHAIN="${GOTOOLCHAIN}" \
  "${GO_BIN}" "$@"
}

wait_for_health() {
  local attempts="${1:-80}"
  while ((attempts > 0)); do
    if curl -fsS "${ADDR}/healthz" >/dev/null 2>&1; then
      return 0
    fi
    attempts=$((attempts - 1))
    sleep 0.2
  done
  return 1
}

listen_port() {
  local listen="${LISTEN##*:}"
  printf "%s\n" "${listen}"
}

listener_swarmd_pids() {
  local port
  port="$(listen_port)"
  local raw_pids=""

  if command -v lsof >/dev/null 2>&1; then
    raw_pids="$(lsof -nP -tiTCP:"${port}" -sTCP:LISTEN 2>/dev/null || true)"
  elif command -v ss >/dev/null 2>&1; then
    raw_pids="$(ss -ltnp "sport = :${port}" 2>/dev/null | sed -n 's/.*pid=\([0-9][0-9]*\).*/\1/p' || true)"
  fi

  local pid
  for pid in ${raw_pids}; do
    if [[ -z "${pid}" ]]; then
      continue
    fi
    if ps -p "${pid}" -o args= 2>/dev/null | grep -Eq '(^|[[:space:]/])swarmd([[:space:]]|$)'; then
      printf "%s\n" "${pid}"
    fi
  done | sort -u
}

cmdline_swarmd_pids() {
  if ! command -v ps >/dev/null 2>&1; then
    return 0
  fi
  local escaped_listen
  escaped_listen="$(printf "%s\n" "${LISTEN}" | sed 's/[][(){}.^$*+?|\\]/\\&/g')"
  ps -eo pid,args | sed '1d' | sed -n "s/^[[:space:]]*\([0-9][0-9]*\)[[:space:]]\+\(.*\)$/\1\t\2/p" | while IFS=$'\t' read -r pid args; do
    if [[ -z "${pid}" || -z "${args}" ]]; then
      continue
    fi
    if [[ "${args}" =~ (^|[[:space:]/])swarmd([[:space:]]|$) ]] && [[ "${args}" =~ --listen[[:space:]]+${escaped_listen}([[:space:]]|$) ]]; then
      printf "%s\n" "${pid}"
    fi
  done | sort -u
}

record_port_assignment() {
  mkdir -p "${SWARM_PORTS_DIR}"
  cat >"${SWARM_PORT_RECORD}" <<EOF
SWARM_LANE=${SWARM_LANE}
SWARMD_LISTEN=${LISTEN}
SWARMD_URL=${ADDR}
SWARM_PORT=${SWARM_LANE_PORT}
STATE_ROOT=${STATE_ROOT}
PID_FILE=${PID_FILE}
LOG_FILE=${LOG_FILE}
UPDATED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF
}

clear_port_assignment() {
  if [[ -n "${SWARM_PORT_RECORD:-}" ]]; then
    rm -f "${SWARM_PORT_RECORD}"
  fi
}
