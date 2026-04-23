#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_ROOT="${RUN_ROOT:-$(mktemp -d "${ROOT_DIR}/.tmp/local-tui-pty-XXXXXX")}"
ARTIFACTS="${RUN_ROOT}/artifacts"
TRANSCRIPT="${ARTIFACTS}/tui.typescript"
TRANSCRIPT_TEXT="${ARTIFACTS}/tui.cleaned.txt"
FEED_LOG="${ARTIFACTS}/feed.log"
BUILD_LOG="${ARTIFACTS}/build.log"
INFO_FILE="${ARTIFACTS}/swarmdev-info.env"
HEALTH_FILE="${ARTIFACTS}/healthz.json"
CTL_HEALTH_FILE="${ARTIFACTS}/swarmctl-health.json"
BACKEND_LOG_TAIL="${ARTIFACTS}/backend-log.tail"
PROCESS_BEFORE="${ARTIFACTS}/processes.before.txt"
PROCESS_DURING="${ARTIFACTS}/processes.during.txt"
PROCESS_AFTER="${ARTIFACTS}/processes.after.txt"
FIFO="${RUN_ROOT}/tui-input.fifo"
TUI_RUNNER="${RUN_ROOT}/run-tui.sh"

SCRIPT_PID=""
SWARMDEV=""
SWARMCTL=""
SWARMD_URL_VALUE=""
LOG_FILE_VALUE=""

log() {
  printf '[%(%Y-%m-%dT%H:%M:%S%z)T] local-tui-pty: %s\n' -1 "$*"
}

fail() {
  log "FAIL: $*"
  log "artifact root: ${RUN_ROOT}"
  if [[ -f "${TRANSCRIPT_TEXT}" ]]; then
    log "clean transcript tail:"
    tail -80 "${TRANSCRIPT_TEXT}" >&2 || true
  elif [[ -f "${TRANSCRIPT}" ]]; then
    log "raw transcript tail:"
    tail -80 "${TRANSCRIPT}" >&2 || true
  fi
  if [[ -f "${BUILD_LOG}" ]]; then
    log "build log tail:"
    tail -80 "${BUILD_LOG}" >&2 || true
  fi
  exit 1
}

snapshot_processes() {
  local out="$1"
  ps -eo pid,ppid,stat,comm,args \
    | awk 'NR == 1 || /swarmd|swarmtui|swarmdev/ { print }' >"${out}" || true
}

choose_free_local_port() {
  local start="${1:-18000}"
  local port
  for ((port = start; port < start + 2000 && port <= 65535; port++)); do
    if timeout 0.2 bash -c "</dev/tcp/127.0.0.1/${port}" >/dev/null 2>&1; then
      continue
    fi
    printf '%s\n' "${port}"
    return 0
  done
  return 1
}

set_startup_config_value() {
  local key="$1"
  local value="$2"
  local file="$3"
  local tmp="${file}.tmp"
  awk -F= -v key="${key}" -v value="${value}" '
    BEGIN { written = 0 }
    /^[[:space:]]*#/ || /^[[:space:]]*$/ { print; next }
    {
      raw_key = $1
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", raw_key)
      if (raw_key == key) {
        print key " = " value
        written = 1
        next
      }
      print
    }
    END {
      if (!written) {
        print key " = " value
      }
    }
  ' "${file}" >"${tmp}"
  mv "${tmp}" "${file}"
}

cleanup() {
  local status=$?
  set +e
  exec 9>&- 2>/dev/null || true
  if [[ -n "${SCRIPT_PID}" ]]; then
    kill "${SCRIPT_PID}" >/dev/null 2>&1 || true
    wait "${SCRIPT_PID}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${SWARMDEV}" && -x "${SWARMDEV}" ]]; then
    HOME="${RUN_ROOT}/home" \
    XDG_CONFIG_HOME="${RUN_ROOT}/xdg/config" \
    XDG_DATA_HOME="${RUN_ROOT}/xdg/data" \
    XDG_STATE_HOME="${RUN_ROOT}/xdg/state" \
    XDG_CACHE_HOME="${RUN_ROOT}/xdg/cache" \
    SWARM_LANE=dev \
    SWARM_ROOT="${ROOT_DIR}" \
    "${SWARMDEV}" server off >>"${ARTIFACTS}/cleanup.log" 2>&1 || true
  fi
  if [[ -n "${LOG_FILE_VALUE}" && -f "${LOG_FILE_VALUE}" ]]; then
    tail -120 "${LOG_FILE_VALUE}" >"${BACKEND_LOG_TAIL}" 2>/dev/null || true
  fi
  snapshot_processes "${PROCESS_AFTER}" || true
  if [[ ${status} -eq 0 ]]; then
    log "PASS artifact root: ${RUN_ROOT}"
  fi
  exit "${status}"
}
trap cleanup EXIT

assert_file_contains() {
  local file="$1"
  local needle="$2"
  if ! grep -Fq -- "${needle}" "${file}"; then
    fail "${file} does not contain expected text: ${needle}"
  fi
}

wait_for_health() {
  local url="$1"
  local out="$2"
  local attempts="${3:-80}"
  local i
  for ((i = 0; i < attempts; i++)); do
    if curl --noproxy '*' -fsS "${url%/}/healthz" >"${out}.tmp" 2>"${out}.err"; then
      mv "${out}.tmp" "${out}"
      rm -f "${out}.err"
      return 0
    fi
    sleep 0.25
  done
  return 1
}

clean_transcript() {
  if [[ ! -f "${TRANSCRIPT}" ]]; then
    return 1
  fi
  awk '{
    gsub(/\033\][^\007]*(\007|\033\\)/, "")
    gsub(/\033\[[0-?]*[ -\/]*[@-~]/, "")
    gsub(/\033[()][A-Za-z0-9]/, "")
    gsub(/\r/, "")
    print
  }' "${TRANSCRIPT}" >"${TRANSCRIPT_TEXT}"
}

mkdir -p "${ARTIFACTS}" "${RUN_ROOT}/home" "${RUN_ROOT}/xdg/config" "${RUN_ROOT}/xdg/data" "${RUN_ROOT}/xdg/state" "${RUN_ROOT}/xdg/cache"
rm -f "${TRANSCRIPT}" "${TRANSCRIPT_TEXT}" "${FIFO}"
mkfifo "${FIFO}"

log "artifact root: ${RUN_ROOT}"
log "isolated HOME/XDG root created"

# shellcheck disable=SC1091
source "${ROOT_DIR}/scripts/lib-lane.sh"
export HOME="${RUN_ROOT}/home"
export XDG_CONFIG_HOME="${RUN_ROOT}/xdg/config"
export XDG_DATA_HOME="${RUN_ROOT}/xdg/data"
export XDG_STATE_HOME="${RUN_ROOT}/xdg/state"
export XDG_CACHE_HOME="${RUN_ROOT}/xdg/cache"
export SWARM_LANE=dev
export SWARM_ROOT="${ROOT_DIR}"
export GOFLAGS="${GOFLAGS:--p=2}"
export GOMAXPROCS="${GOMAXPROCS:-2}"

log "building local dev lane TUI and launcher tools"
SWARM_LANE=dev bash "${ROOT_DIR}/scripts/dev-build.sh" >"${BUILD_LOG}" 2>&1 || fail "scripts/dev-build.sh failed"

swarm_lane_export_profile dev "${ROOT_DIR}"
peer_port="$(choose_free_local_port 18091)" || fail "could not find a free peer transport port"
set_startup_config_value peer_transport_port "${peer_port}" "${SWARM_STARTUP_CONFIG}"
log "isolated startup config: ${SWARM_STARTUP_CONFIG}"
log "isolated peer transport port: ${peer_port}"
# Reload the profile after modifying the config so launcher/env values agree with the file.
swarm_lane_export_profile dev "${ROOT_DIR}"
SWARMDEV="${SWARM_TOOL_BIN_DIR}/swarmdev"
SWARMCTL="${SWARM_BIN_DIR}/swarmctl"
[[ -x "${SWARMDEV}" ]] || fail "swarmdev binary missing at ${SWARMDEV}"

log "building local dev lane backend binaries"
"${SWARMDEV}" backend-build >>"${BUILD_LOG}" 2>&1 || fail "swarmdev backend-build failed"
[[ -x "${SWARM_BIN_DIR}/swarmtui" ]] || fail "swarmtui binary missing at ${SWARM_BIN_DIR}/swarmtui"
[[ -x "${SWARM_BIN_DIR}/swarmd" ]] || fail "swarmd binary missing at ${SWARM_BIN_DIR}/swarmd"
[[ -x "${SWARMCTL}" ]] || fail "swarmctl binary missing at ${SWARMCTL}"

log "capturing swarmdev info"
"${SWARMDEV}" info >"${INFO_FILE}" 2>&1 || fail "swarmdev info failed"
SWARMD_URL_VALUE="$(awk -F= '$1 == "url" { print $2 }' "${INFO_FILE}" | tail -1)"
LOG_FILE_VALUE="$(awk -F= '$1 == "log_file" { print $2 }' "${INFO_FILE}" | tail -1)"
[[ -n "${SWARMD_URL_VALUE}" ]] || fail "could not read url from ${INFO_FILE}"
[[ -n "${LOG_FILE_VALUE}" ]] || fail "could not read log_file from ${INFO_FILE}"
log "dev lane URL: ${SWARMD_URL_VALUE}"
log "backend log: ${LOG_FILE_VALUE}"

snapshot_processes "${PROCESS_BEFORE}"

cat >"${TUI_RUNNER}" <<EOF
#!/usr/bin/env bash
set -euo pipefail
cd "${ROOT_DIR}"
export HOME="${HOME}"
export XDG_CONFIG_HOME="${XDG_CONFIG_HOME}"
export XDG_DATA_HOME="${XDG_DATA_HOME}"
export XDG_STATE_HOME="${XDG_STATE_HOME}"
export XDG_CACHE_HOME="${XDG_CACHE_HOME}"
export SWARM_LANE=dev
export SWARM_ROOT="${ROOT_DIR}"
export TERM="xterm-256color"
exec "${SWARMDEV}" run
EOF
chmod +x "${TUI_RUNNER}"

log "starting real TUI under util-linux script PTY"
script -q -f -e -c "${TUI_RUNNER}" "${TRANSCRIPT}" <"${FIFO}" >"${ARTIFACTS}/script.stdout" 2>"${ARTIFACTS}/script.stderr" &
SCRIPT_PID=$!
exec 9>"${FIFO}"
printf '[%(%Y-%m-%dT%H:%M:%S%z)T] opened PTY input fd\n' -1 >>"${FEED_LOG}"

log "waiting for backend health from TUI-started dev lane"
wait_for_health "${SWARMD_URL_VALUE}" "${HEALTH_FILE}" 120 || fail "backend did not become healthy at ${SWARMD_URL_VALUE}"
log "backend /healthz captured"

log "running swarmctl health against same isolated backend"
"${SWARMCTL}" health --addr "${SWARMD_URL_VALUE}" >"${CTL_HEALTH_FILE}" 2>"${CTL_HEALTH_FILE}.err" || fail "swarmctl health failed"
log "swarmctl health captured"

snapshot_processes "${PROCESS_DURING}"

log "driving TUI commands through the PTY"
sleep 1
printf '/help\r' >&9
printf '[%(%Y-%m-%dT%H:%M:%S%z)T] sent /help\n' -1 >>"${FEED_LOG}"
sleep 2
printf '/update status\r' >&9
printf '[%(%Y-%m-%dT%H:%M:%S%z)T] sent /update status\n' -1 >>"${FEED_LOG}"
sleep 2
printf '/quit\r' >&9
printf '[%(%Y-%m-%dT%H:%M:%S%z)T] sent /quit\n' -1 >>"${FEED_LOG}"
exec 9>&-

log "waiting for TUI process to exit after /quit"
for _ in $(seq 1 80); do
  if ! kill -0 "${SCRIPT_PID}" >/dev/null 2>&1; then
    wait "${SCRIPT_PID}" || fail "script/TUI exited non-zero"
    SCRIPT_PID=""
    break
  fi
  sleep 0.25
done
if [[ -n "${SCRIPT_PID}" ]]; then
  clean_transcript || true
  fail "TUI did not exit within timeout after /quit"
fi

clean_transcript || fail "failed to clean transcript"

log "verifying real TUI transcript contains command output"
assert_file_contains "${TRANSCRIPT}" "/help"
assert_file_contains "${TRANSCRIPT}" "/quit"
assert_file_contains "${TRANSCRIPT_TEXT}" "/update"
assert_file_contains "${TRANSCRIPT_TEXT}" "[status] [apply]"
if ! grep -Eq 'updates are suppressed|dev_lane|/update status failed|update status refreshed' "${TRANSCRIPT_TEXT}"; then
  fail "transcript does not show /update status result"
fi
if ! grep -aFq 'COMMAND_EXIT_CODE="0"' "${TRANSCRIPT}"; then
  fail "TUI transcript does not show clean script exit code 0"
fi

log "verifying backend health payload"
assert_file_contains "${HEALTH_FILE}" "ok"
assert_file_contains "${CTL_HEALTH_FILE}" "ok"

if [[ -f "${LOG_FILE_VALUE}" ]]; then
  tail -120 "${LOG_FILE_VALUE}" >"${BACKEND_LOG_TAIL}" || true
fi

cat >"${ARTIFACTS}/summary.txt" <<EOF
local TUI PTY e2e PASS
run_root=${RUN_ROOT}
transcript=${TRANSCRIPT}
clean_transcript=${TRANSCRIPT_TEXT}
info=${INFO_FILE}
health=${HEALTH_FILE}
swarmctl_health=${CTL_HEALTH_FILE}
backend_log_tail=${BACKEND_LOG_TAIL}
processes_before=${PROCESS_BEFORE}
processes_during=${PROCESS_DURING}
processes_after=${PROCESS_AFTER}
commands_sent=/help,/update status,/quit
EOF

log "PASS: local real PTY TUI accepted /help, /update status, /quit and backend health checks passed"
log "summary: ${ARTIFACTS}/summary.txt"
