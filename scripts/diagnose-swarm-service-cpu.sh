#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

usage() {
  cat <<'EOF'
Usage: ./scripts/diagnose-swarm-service-cpu.sh [options]

Repeatable swarm.service CPU stress/profile harness. It samples the running user
systemd unit, drives a fixed desktop/API refresh workload, and writes artifacts
that can be compared before and after each performance fix.

Options:
  --service <name>             User systemd unit. Default: swarm.service
  --duration <seconds>         Total test duration. Default: 300
  --sample-interval <seconds>  ps/pidstat sample interval. Default: 1
  --artifact-dir <path>        Output directory. Default: tmp/swarm-service-cpu-test/<timestamp>
  --api-url <url>              API base URL. Default: derived from --config host/port
  --config <path>              swarm.conf path. Default: SWARM_CONFIG or canonical swarmd system config
  --workspace <path>           Workspace path for git endpoints. Default: current directory
  --stress-sleep <seconds>     Delay between endpoint batches per worker. Default: 0.25
  --stress-workers <count>     Concurrent API stress workers. Default: 1
  --curl-max-time <seconds>    Per-request curl max time. Default: 8
  --no-stress                  Only sample/profile; do not issue API requests
  --no-git-realtime           Do not POST /v1/workspace/git/realtime at startup
  --perf                      Run non-sudo perf record against the swarmd child process
  --sudo-perf                 Run perf through sudo, prompting for a password when needed
  --no-perf                   Disable perf record. Default: sudo perf when perf exists
  --perf-duration <seconds>    perf capture duration. Default: min(60, duration)
  --perf-frequency <hz>        perf sample frequency. Default: 99
  --help                      Show this help

Artifacts include:
  metadata.txt, process-samples.tsv, pidstat.txt, journal.log,
  api-timings.tsv, api-summary.tsv, perf-swarmd.data, perf-report.txt,
  summary.txt

Notes:
  - The workload is intentionally state-light: it uses local desktop/session
    cookies and GET requests, plus one git realtime POST unless disabled.
EOF
}

log() {
  printf '%s\n' "$*"
}

fail() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

require_command() {
  local name="${1:-}"
  command -v "${name}" >/dev/null 2>&1 || fail "required command not found: ${name}"
}

trim() {
  local value="${1-}"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "${value}"
}

conf_get() {
  local key="${1:-}"
  local path="${2:-}"
  [[ -n "${key}" && -f "${path}" ]] || return 0
  awk -F '=' -v want="${key}" '
    {
      left = $1
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", left)
      if (left == want) {
        value = substr($0, index($0, "=") + 1)
        sub(/[[:space:]]+#.*$/, "", value)
        gsub(/^[[:space:]]+|[[:space:]]+$/, "", value)
        print value
      }
    }
  ' "${path}" | tail -n 1
}

urlencode() {
  local input="${1-}"
  local output=""
  local i char encoded
  LC_CTYPE=C
  for ((i = 0; i < ${#input}; i += 1)); do
    char="${input:i:1}"
    case "${char}" in
      [a-zA-Z0-9.~_-]) output+="${char}" ;;
      *) printf -v encoded '%%%02X' "'${char}"; output+="${encoded}" ;;
    esac
  done
  printf '%s' "${output}"
}

is_positive_int() {
  [[ "${1:-}" =~ ^[0-9]+$ && "${1}" -gt 0 ]]
}

is_positive_number() {
  awk -v value="${1:-}" 'BEGIN { exit !(value ~ /^[0-9]+([.][0-9]+)?$/ && value > 0) }'
}

default_swarm_config_path() {
  printf '/%s/%s\n' "etc" "swarmd/swarm.conf"
}

SERVICE="${SWARM_CPU_TEST_SERVICE:-swarm.service}"
DURATION="${SWARM_CPU_TEST_DURATION:-300}"
SAMPLE_INTERVAL="${SWARM_CPU_TEST_SAMPLE_INTERVAL:-1}"
ARTIFACT_DIR="${SWARM_CPU_TEST_ARTIFACT_DIR:-}"
API_URL="${SWARM_CPU_TEST_API_URL:-}"
CONFIG_PATH="${SWARM_CONFIG:-$(default_swarm_config_path)}"
WORKSPACE_PATH="${SWARM_CPU_TEST_WORKSPACE:-${ROOT_DIR}}"
STRESS_SLEEP="${SWARM_CPU_TEST_STRESS_SLEEP:-0.25}"
STRESS_WORKERS="${SWARM_CPU_TEST_STRESS_WORKERS:-1}"
CURL_MAX_TIME="${SWARM_CPU_TEST_CURL_MAX_TIME:-8}"
STRESS_ENABLED="true"
GIT_REALTIME_ENABLED="true"
PERF_MODE="${SWARM_CPU_TEST_PERF:-auto}"
PERF_DURATION="${SWARM_CPU_TEST_PERF_DURATION:-}"
PERF_FREQ="${SWARM_CPU_TEST_PERF_FREQUENCY:-99}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --service)
      SERVICE="${2:-}"
      shift 2
      ;;
    --duration)
      DURATION="${2:-}"
      shift 2
      ;;
    --sample-interval)
      SAMPLE_INTERVAL="${2:-}"
      shift 2
      ;;
    --artifact-dir)
      ARTIFACT_DIR="${2:-}"
      shift 2
      ;;
    --api-url)
      API_URL="${2:-}"
      shift 2
      ;;
    --config)
      CONFIG_PATH="${2:-}"
      shift 2
      ;;
    --workspace)
      WORKSPACE_PATH="${2:-}"
      shift 2
      ;;
    --stress-sleep)
      STRESS_SLEEP="${2:-}"
      shift 2
      ;;
    --stress-workers)
      STRESS_WORKERS="${2:-}"
      shift 2
      ;;
    --curl-max-time)
      CURL_MAX_TIME="${2:-}"
      shift 2
      ;;
    --no-stress)
      STRESS_ENABLED="false"
      shift
      ;;
    --no-git-realtime)
      GIT_REALTIME_ENABLED="false"
      shift
      ;;
    --perf)
      PERF_MODE="on"
      shift
      ;;
    --sudo-perf)
      PERF_MODE="sudo"
      shift
      ;;
    --no-perf)
      PERF_MODE="off"
      shift
      ;;
    --perf-duration)
      PERF_DURATION="${2:-}"
      shift 2
      ;;
    --perf-frequency)
      PERF_FREQ="${2:-}"
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
done

[[ -n "$(trim "${SERVICE}")" ]] || fail "--service is required"
is_positive_int "${DURATION}" || fail "--duration must be a positive integer"
is_positive_number "${SAMPLE_INTERVAL}" || fail "--sample-interval must be positive"
is_positive_number "${STRESS_SLEEP}" || fail "--stress-sleep must be positive"
is_positive_int "${STRESS_WORKERS}" || fail "--stress-workers must be a positive integer"
is_positive_number "${CURL_MAX_TIME}" || fail "--curl-max-time must be positive"
is_positive_int "${PERF_FREQ}" || fail "--perf-frequency must be a positive integer"

if [[ -z "${PERF_DURATION}" ]]; then
  if [[ "${DURATION}" -lt 60 ]]; then
    PERF_DURATION="${DURATION}"
  else
    PERF_DURATION="60"
  fi
fi
is_positive_int "${PERF_DURATION}" || fail "--perf-duration must be a positive integer"
if [[ "${PERF_DURATION}" -gt "${DURATION}" ]]; then
  PERF_DURATION="${DURATION}"
fi

if [[ -z "${API_URL}" ]]; then
  host="$(trim "$(conf_get host "${CONFIG_PATH}")")"
  port="$(trim "$(conf_get port "${CONFIG_PATH}")")"
  [[ -n "${host}" ]] || host="127.0.0.1"
  [[ -n "${port}" ]] || port="7781"
  if [[ "${host}" == "0.0.0.0" || "${host}" == "::" ]]; then
    host="127.0.0.1"
  fi
  API_URL="http://${host}:${port}"
fi
API_URL="${API_URL%/}"

if [[ -z "${ARTIFACT_DIR}" ]]; then
  ARTIFACT_DIR="$(printf '%s/%s/%s' "${ROOT_DIR}" "tmp/swarm-service-cpu-test" "$(date -u +%Y%m%dT%H%M%SZ)")"
fi
mkdir -p -- "${ARTIFACT_DIR}"
ARTIFACT_DIR="$(cd -- "${ARTIFACT_DIR}" && pwd)"

if [[ -n "${WORKSPACE_PATH}" ]]; then
  WORKSPACE_PATH="$(cd -- "${WORKSPACE_PATH}" 2>/dev/null && pwd -P || printf '%s' "${WORKSPACE_PATH}")"
fi
WORKSPACE_QUERY="$(urlencode "${WORKSPACE_PATH}")"
COOKIE_FILE="${ARTIFACT_DIR}/desktop-cookie.txt"
START_EPOCH="$(date +%s)"
END_EPOCH="$((START_EPOCH + DURATION))"
START_ISO="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

require_command systemctl
require_command ps
require_command awk
require_command sed
if [[ "${STRESS_ENABLED}" == "true" ]]; then
  require_command curl
fi

main_pid="$(systemctl --user show "${SERVICE}" -p MainPID --value 2>/dev/null || true)"
if ! [[ "${main_pid}" =~ ^[0-9]+$ ]] || [[ "${main_pid}" == "0" ]]; then
  fail "could not resolve active MainPID for user unit ${SERVICE}"
fi

process_tree() {
  local root="${1:-}"
  local queue children child
  [[ "${root}" =~ ^[0-9]+$ ]] || return 0
  queue="${root}"
  while [[ -n "${queue}" ]]; do
    child="${queue%% *}"
    if [[ "${queue}" == "${child}" ]]; then
      queue=""
    else
      queue="${queue#* }"
    fi
    printf '%s\n' "${child}"
    children="$(pgrep -P "${child}" 2>/dev/null | tr '\n' ' ' | sed 's/[[:space:]]*$//')"
    if [[ -n "${children}" ]]; then
      if [[ -n "${queue}" ]]; then
        queue="${queue} ${children}"
      else
        queue="${children}"
      fi
    fi
  done
}

find_swarmd_pid() {
  local pid comm
  while read -r pid; do
    [[ -n "${pid}" ]] || continue
    comm="$(ps -o comm= -p "${pid}" 2>/dev/null | awk '{print $1}')"
    if [[ "${comm}" == "swarmd" ]]; then
      printf '%s' "${pid}"
      return 0
    fi
  done < <(process_tree "${main_pid}")
  return 1
}

swarmd_pid="$(find_swarmd_pid || true)"

cat >"${ARTIFACT_DIR}/metadata.txt" <<EOF
service=${SERVICE}
main_pid=${main_pid}
swarmd_pid=${swarmd_pid}
start_utc=${START_ISO}
duration_seconds=${DURATION}
sample_interval_seconds=${SAMPLE_INTERVAL}
api_url=${API_URL}
config_path=${CONFIG_PATH}
workspace_path=${WORKSPACE_PATH}
stress_enabled=${STRESS_ENABLED}
stress_sleep_seconds=${STRESS_SLEEP}
stress_workers=${STRESS_WORKERS}
git_realtime_enabled=${GIT_REALTIME_ENABLED}
perf_mode=${PERF_MODE}
perf_duration_seconds=${PERF_DURATION}
perf_frequency_hz=${PERF_FREQ}
EOF

bg_pids=()
cleanup() {
  local pid
  for pid in "${bg_pids[@]:-}"; do
    if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
      kill "${pid}" 2>/dev/null || true
    fi
  done
  if [[ -f "${COOKIE_FILE}" ]]; then
    rm -f -- "${COOKIE_FILE}"
  fi
}
trap cleanup EXIT

sample_processes() {
  printf 'timestamp_utc pid ppid comm pcpu pmem rss_kb vsz_kb nlwp stat args\n' >"${ARTIFACT_DIR}/process-samples.tsv"
  while [[ "$(date +%s)" -lt "${END_EPOCH}" ]]; do
    local ts pid
    ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    while read -r pid; do
      [[ -n "${pid}" ]] || continue
      ps -ww -o pid= -o ppid= -o comm= -o pcpu= -o pmem= -o rss= -o vsz= -o nlwp= -o stat= -o args= -p "${pid}" 2>/dev/null \
        | awk -v ts="${ts}" '{ print ts, $0 }' >>"${ARTIFACT_DIR}/process-samples.tsv"
    done < <(process_tree "${main_pid}")
    sleep "${SAMPLE_INTERVAL}"
  done
}

start_pidstat() {
  if ! command -v pidstat >/dev/null 2>&1; then
    printf 'pidstat not found; install sysstat for pidstat samples.\n' >"${ARTIFACT_DIR}/pidstat.txt"
    return 0
  fi
  local pids sample_count
  pids="$(process_tree "${main_pid}" | paste -sd, -)"
  if [[ -z "${pids}" ]]; then
    printf 'no pids resolved for pidstat.\n' >"${ARTIFACT_DIR}/pidstat.txt"
    return 0
  fi
  sample_count="$(awk -v duration="${DURATION}" -v interval="${SAMPLE_INTERVAL}" 'BEGIN { n = int(duration / interval); if (n < 1) n = 1; print n }')"
  pidstat -h -r -u -d -w -p "${pids}" "${SAMPLE_INTERVAL}" "${sample_count}" >"${ARTIFACT_DIR}/pidstat.txt" 2>"${ARTIFACT_DIR}/pidstat.stderr" &
  bg_pids+=("$!")
}

start_journal_follow() {
  if ! command -v journalctl >/dev/null 2>&1; then
    printf 'journalctl not found.\n' >"${ARTIFACT_DIR}/journal.log"
    return 0
  fi
  journalctl --user -u "${SERVICE}" --since "${START_ISO}" -f -o short-iso >"${ARTIFACT_DIR}/journal-follow.log" 2>"${ARTIFACT_DIR}/journal-follow.stderr" &
  bg_pids+=("$!")
}

curl_common_args() {
  printf '%s\0' \
    -sS \
    --connect-timeout 2 \
    --max-time "${CURL_MAX_TIME}" \
    -H 'Accept: application/json' \
    -H "Origin: ${API_URL}" \
    -H "Referer: ${API_URL}/" \
    -H 'Sec-Fetch-Site: same-origin' \
    -c "${COOKIE_FILE}" \
    -b "${COOKIE_FILE}"
}

api_request() {
  local method="${1:-GET}"
  local path="${2:-/readyz}"
  local label="${3:-${path}}"
  local code timing exit_code ts stderr_file
  ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  stderr_file="${ARTIFACT_DIR}/api-curl.stderr"
  set +e
  if [[ "${method}" == "POST" ]]; then
    timing="$(curl -sS --connect-timeout 2 --max-time "${CURL_MAX_TIME}" \
      -H 'Accept: application/json' \
      -H "Origin: ${API_URL}" \
      -H "Referer: ${API_URL}/" \
      -H 'Sec-Fetch-Site: same-origin' \
      -c "${COOKIE_FILE}" -b "${COOKIE_FILE}" \
      -X POST -o /dev/null -w '%{http_code}\t%{time_total}' \
      "${API_URL}${path}" 2>>"${stderr_file}")"
    exit_code=$?
  else
    timing="$(curl -sS --connect-timeout 2 --max-time "${CURL_MAX_TIME}" \
      -H 'Accept: application/json' \
      -H "Origin: ${API_URL}" \
      -H "Referer: ${API_URL}/" \
      -H 'Sec-Fetch-Site: same-origin' \
      -c "${COOKIE_FILE}" -b "${COOKIE_FILE}" \
      -o /dev/null -w '%{http_code}\t%{time_total}' \
      "${API_URL}${path}" 2>>"${stderr_file}")"
    exit_code=$?
  fi
  set -e
  if [[ "${timing}" == *$'\t'* ]]; then
    code="${timing%%$'\t'*}"
    timing="${timing#*$'\t'}"
  else
    code="000"
    timing="0"
  fi
  printf '%s\t%s\t%s\t%s\t%s\t%s\n' "${ts}" "${method}" "${label}" "${code}" "${timing}" "${exit_code}" >>"${ARTIFACT_DIR}/api-timings.tsv"
}

bootstrap_desktop_session() {
  api_request GET "/v1/auth/desktop/session" "desktop_session"
}

stress_worker() {
  local worker_id="${1:-0}"
  while [[ "$(date +%s)" -lt "${END_EPOCH}" ]]; do
    api_request GET "/readyz" "readyz:w${worker_id}"
    api_request GET "/v1/vault" "vault_status:w${worker_id}"
    api_request GET "/v1/workspace/current" "workspace_current:w${worker_id}"
    api_request GET "/v1/workspace/git/status?workspace_path=${WORKSPACE_QUERY}&recent_limit=8" "git_status:w${worker_id}"
    api_request GET "/v1/swarm/targets" "swarm_targets:w${worker_id}"
    sleep "${STRESS_SLEEP}"
  done
}

start_stress() {
  printf 'timestamp_utc\tmethod\tlabel\thttp_code\ttime_total_seconds\tcurl_exit\n' >"${ARTIFACT_DIR}/api-timings.tsv"
  bootstrap_desktop_session
  if [[ "${GIT_REALTIME_ENABLED}" == "true" ]]; then
    api_request POST "/v1/workspace/git/realtime?workspace_path=${WORKSPACE_QUERY}" "git_realtime_start"
  fi
  local worker
  for ((worker = 1; worker <= STRESS_WORKERS; worker += 1)); do
    stress_worker "${worker}" &
    bg_pids+=("$!")
  done
}

have_terminal() {
  [[ -r /dev/tty && -w /dev/tty ]]
}

perf_fail() {
  local message="${1:-perf failed}"
  printf '%s\n' "${message}" >"${ARTIFACT_DIR}/perf.stderr"
  fail "${message}"
}

ensure_sudo_cached() {
  local reason="${1:-perf}"
  if sudo -n true >/dev/null 2>&1; then
    return 0
  fi
  if have_terminal; then
    printf '%s requires sudo; enter your sudo password now.\n' "${reason}" >/dev/tty
    sudo -v </dev/tty >/dev/tty 2>/dev/tty || return 1
    sudo -n true >/dev/null 2>&1 || return 1
    return 0
  fi
  return 2
}

start_perf() {
  [[ "${PERF_MODE}" != "off" ]] || return 0
  if ! command -v perf >/dev/null 2>&1; then
    printf 'perf not found.\n' >"${ARTIFACT_DIR}/perf.stderr"
    return 0
  fi
  if [[ -z "${swarmd_pid}" ]]; then
    perf_fail "swarmd pid not found; cannot run perf."
  fi

  local output="${ARTIFACT_DIR}/perf-swarmd.data"
  local use_sudo="false"
  if [[ "${PERF_MODE}" == "sudo" || "${PERF_MODE}" == "auto" ]]; then
    use_sudo="true"
  fi

  if [[ "${use_sudo}" == "true" ]]; then
    command -v sudo >/dev/null 2>&1 || perf_fail "sudo not found; cannot run sudo perf."
    set +e
    ensure_sudo_cached "perf record"
    local sudo_status=$?
    set -e
    if [[ "${sudo_status}" -eq 2 ]]; then
      perf_fail "sudo credentials are required for perf, but no terminal is available for a password prompt. Run this script from a terminal, pre-run 'sudo -v', or use --no-perf."
    elif [[ "${sudo_status}" -ne 0 ]]; then
      perf_fail "sudo authentication failed; cannot run perf."
    fi
    sudo -n perf record -F "${PERF_FREQ}" -g -p "${swarmd_pid}" -o "${output}" -- sleep "${PERF_DURATION}" >"${ARTIFACT_DIR}/perf.stdout" 2>"${ARTIFACT_DIR}/perf.stderr" &
  else
    perf record -F "${PERF_FREQ}" -g -p "${swarmd_pid}" -o "${output}" -- sleep "${PERF_DURATION}" >"${ARTIFACT_DIR}/perf.stdout" 2>"${ARTIFACT_DIR}/perf.stderr" &
  fi
  bg_pids+=("$!")
}

write_api_summary() {
  if [[ ! -s "${ARTIFACT_DIR}/api-timings.tsv" ]]; then
    return 0
  fi
  awk -F '\t' '
    NR == 1 { next }
    {
      label=$3; code=$4; t=$5+0; curl_exit=$6+0
      count[label]++
      sum[label]+=t
      if (!(label in max) || t > max[label]) max[label]=t
      if (code !~ /^2/ || curl_exit != 0) fail[label]++
    }
    END {
      print "label\tcount\tfailures\tavg_seconds\tmax_seconds"
      for (label in count) {
        printf "%s\t%d\t%d\t%.6f\t%.6f\n", label, count[label], fail[label]+0, sum[label]/count[label], max[label]
      }
    }
  ' "${ARTIFACT_DIR}/api-timings.tsv" | sort >"${ARTIFACT_DIR}/api-summary.tsv"
}

write_process_summary() {
  awk '
    NR == 1 { next }
    {
      comm=$4; cpu=$5+0; rss=$7+0
      key=comm
      count[key]++
      cpu_sum[key]+=cpu
      if (!(key in cpu_max) || cpu > cpu_max[key]) cpu_max[key]=cpu
      if (!(key in rss_max) || rss > rss_max[key]) rss_max[key]=rss
    }
    END {
      print "comm samples avg_cpu_percent max_cpu_percent max_rss_kb"
      for (key in count) {
        printf "%s %d %.2f %.2f %.0f\n", key, count[key], cpu_sum[key]/count[key], cpu_max[key], rss_max[key]
      }
    }
  ' "${ARTIFACT_DIR}/process-samples.tsv" | sort >"${ARTIFACT_DIR}/process-summary.txt"
}

log "starting swarm.service CPU test"
log "artifacts: ${ARTIFACT_DIR}"
log "service=${SERVICE} main_pid=${main_pid} swarmd_pid=${swarmd_pid:-unknown} duration=${DURATION}s"

sample_processes &
bg_pids+=("$!")
start_pidstat
start_journal_follow
start_perf
if [[ "${STRESS_ENABLED}" == "true" ]]; then
  start_stress
fi

while [[ "$(date +%s)" -lt "${END_EPOCH}" ]]; do
  sleep 1
done

cleanup
trap - EXIT

if command -v journalctl >/dev/null 2>&1; then
  journalctl --user -u "${SERVICE}" --since "${START_ISO}" -o short-iso >"${ARTIFACT_DIR}/journal.log" 2>"${ARTIFACT_DIR}/journal.stderr" || true
fi

if [[ -s "${ARTIFACT_DIR}/perf-swarmd.data" ]] && command -v perf >/dev/null 2>&1; then
  if [[ ! -r "${ARTIFACT_DIR}/perf-swarmd.data" ]] && command -v sudo >/dev/null 2>&1; then
    set +e
    ensure_sudo_cached "perf report"
    sudo_status=$?
    set -e
    if [[ "${sudo_status}" -eq 0 ]]; then
      sudo -n chown "$(id -u):$(id -g)" "${ARTIFACT_DIR}/perf-swarmd.data" 2>"${ARTIFACT_DIR}/perf-chown.stderr" || true
    elif [[ "${sudo_status}" -eq 2 ]]; then
      printf 'sudo credentials are required to read perf data, but no terminal is available for a password prompt.\n' >"${ARTIFACT_DIR}/perf-report.stderr"
    else
      printf 'sudo authentication failed; cannot read perf data.\n' >"${ARTIFACT_DIR}/perf-report.stderr"
    fi
  fi

  if [[ -r "${ARTIFACT_DIR}/perf-swarmd.data" ]]; then
    perf report --stdio --no-children --sort comm,dso,symbol -i "${ARTIFACT_DIR}/perf-swarmd.data" >"${ARTIFACT_DIR}/perf-report.txt" 2>"${ARTIFACT_DIR}/perf-report.stderr" || true
  elif command -v sudo >/dev/null 2>&1; then
    set +e
    ensure_sudo_cached "perf report"
    sudo_status=$?
    set -e
    if [[ "${sudo_status}" -eq 0 ]]; then
      sudo -n perf report --stdio --no-children --sort comm,dso,symbol -i "${ARTIFACT_DIR}/perf-swarmd.data" >"${ARTIFACT_DIR}/perf-report.txt" 2>"${ARTIFACT_DIR}/perf-report.stderr" || true
    elif [[ "${sudo_status}" -eq 2 ]]; then
      printf 'perf data exists but sudo credentials are required and no terminal is available for a password prompt.\n' >"${ARTIFACT_DIR}/perf-report.stderr"
    else
      printf 'sudo authentication failed; cannot generate perf report.\n' >"${ARTIFACT_DIR}/perf-report.stderr"
    fi
  else
    printf 'perf data exists but is not readable and sudo is not installed; cannot generate perf report.\n' >"${ARTIFACT_DIR}/perf-report.stderr"
  fi
fi

write_api_summary
write_process_summary

{
  printf 'swarm.service CPU test summary\n'
  printf 'artifact_dir=%s\n' "${ARTIFACT_DIR}"
  printf 'service=%s\n' "${SERVICE}"
  printf 'main_pid=%s\n' "${main_pid}"
  printf 'swarmd_pid=%s\n' "${swarmd_pid:-unknown}"
  printf 'duration_seconds=%s\n' "${DURATION}"
  printf '\n== process summary ==\n'
  cat "${ARTIFACT_DIR}/process-summary.txt" 2>/dev/null || true
  printf '\n== api summary ==\n'
  cat "${ARTIFACT_DIR}/api-summary.tsv" 2>/dev/null || true
  printf '\n== perf top ==\n'
  if [[ -s "${ARTIFACT_DIR}/perf-report.txt" ]]; then
    sed -n '1,80p' "${ARTIFACT_DIR}/perf-report.txt"
  else
    cat "${ARTIFACT_DIR}/perf.stderr" 2>/dev/null || printf 'perf report unavailable\n'
  fi
} >"${ARTIFACT_DIR}/summary.txt"

cat "${ARTIFACT_DIR}/summary.txt"
