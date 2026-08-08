#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage: scripts/ssh-time-v3-sync-bootstrap.sh [ssh-target] [options]

Directly times the full /v3/sync/bootstrap API response on a remote SSH host.
This does not use the frontend. It obtains a desktop session token from the API,
POSTs the Desktop bootstrap payload, downloads the full response body to a
remote temp file, parses counts, and reports curl total time after the whole
payload has been received.

Options:
  --api-url <url>              Remote-local API URL. Default: http://127.0.0.1:7781
  --service <unit>             Service unit for optional journal timing grep. Default: swarm.service
  --include-active <bool>      Payload include_active. Default: true (current Desktop default)
  --session-limit <n>          Payload recent.limit. Default: 50
  --message-limit <n>          Payload history.max_messages_per_session. Default: 200
  --repeat <n>                 Number of full bootstrap calls. Default: 1
  --remote-work-dir <path>     Remote temp/work directory. Default: mktemp on remote
  --keep-remote                Keep remote temp files. Default: remove them
  --help                       Show this help

Environment equivalents:
  SWARM_PRIMARY_SSH, SWARM_PRIMARY_API_URL, SWARM_SERVICE_UNIT,
  SWARM_BOOTSTRAP_INCLUDE_ACTIVE, SWARM_BOOTSTRAP_SESSION_LIMIT,
  SWARM_BOOTSTRAP_MESSAGE_LIMIT, SWARM_BOOTSTRAP_REPEAT,
  SWARM_BOOTSTRAP_REMOTE_WORK_DIR
USAGE
}

fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"; }

SSH_ALIAS="${SWARM_PRIMARY_SSH:-}"
API_URL="${SWARM_PRIMARY_API_URL:-http://127.0.0.1:7781}"
SERVICE_UNIT="${SWARM_SERVICE_UNIT:-swarm.service}"
INCLUDE_ACTIVE="${SWARM_BOOTSTRAP_INCLUDE_ACTIVE:-true}"
SESSION_LIMIT="${SWARM_BOOTSTRAP_SESSION_LIMIT:-50}"
MESSAGE_LIMIT="${SWARM_BOOTSTRAP_MESSAGE_LIMIT:-200}"
REPEAT="${SWARM_BOOTSTRAP_REPEAT:-1}"
REMOTE_WORK_DIR="${SWARM_BOOTSTRAP_REMOTE_WORK_DIR:-}"
KEEP_REMOTE="false"

if [[ $# -gt 0 && "${1:-}" != --* ]]; then
  SSH_ALIAS="$1"
  shift
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --api-url) API_URL="${2:-}"; shift 2 ;;
    --service) SERVICE_UNIT="${2:-}"; shift 2 ;;
    --include-active) INCLUDE_ACTIVE="${2:-}"; shift 2 ;;
    --session-limit) SESSION_LIMIT="${2:-}"; shift 2 ;;
    --message-limit) MESSAGE_LIMIT="${2:-}"; shift 2 ;;
    --repeat) REPEAT="${2:-}"; shift 2 ;;
    --remote-work-dir) REMOTE_WORK_DIR="${2:-}"; shift 2 ;;
    --keep-remote) KEEP_REMOTE="true"; shift ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

require_command ssh
[[ -n "${SSH_ALIAS}" ]] || fail "pass an SSH target as the first argument or set SWARM_PRIMARY_SSH"
[[ -n "${API_URL}" ]] || fail "api URL is required"
[[ "${INCLUDE_ACTIVE}" == "true" || "${INCLUDE_ACTIVE}" == "false" ]] || fail "--include-active must be true or false"
[[ "${SESSION_LIMIT}" =~ ^[0-9]+$ && "${SESSION_LIMIT}" -gt 0 ]] || fail "--session-limit must be a positive integer"
[[ "${MESSAGE_LIMIT}" =~ ^[0-9]+$ && "${MESSAGE_LIMIT}" -ge 0 ]] || fail "--message-limit must be a non-negative integer"
[[ "${REPEAT}" =~ ^[0-9]+$ && "${REPEAT}" -gt 0 ]] || fail "--repeat must be a positive integer"
API_URL="${API_URL%/}"

printf '[bootstrap-timing] ssh=%s api=%s include_active=%s session_limit=%s message_limit=%s repeat=%s\n' \
  "${SSH_ALIAS}" "${API_URL}" "${INCLUDE_ACTIVE}" "${SESSION_LIMIT}" "${MESSAGE_LIMIT}" "${REPEAT}"

ssh "${SSH_ALIAS}" \
  "SWARM_BOOTSTRAP_API_URL='${API_URL}' SWARM_BOOTSTRAP_SERVICE_UNIT='${SERVICE_UNIT}' SWARM_BOOTSTRAP_INCLUDE_ACTIVE='${INCLUDE_ACTIVE}' SWARM_BOOTSTRAP_SESSION_LIMIT='${SESSION_LIMIT}' SWARM_BOOTSTRAP_MESSAGE_LIMIT='${MESSAGE_LIMIT}' SWARM_BOOTSTRAP_REPEAT='${REPEAT}' SWARM_BOOTSTRAP_REMOTE_WORK_DIR='${REMOTE_WORK_DIR}' SWARM_BOOTSTRAP_KEEP_REMOTE='${KEEP_REMOTE}' bash -s" <<'REMOTE'
set -euo pipefail

fail() { printf 'REMOTE_ERROR %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "missing command: $1"; }
need curl
need python3

api_url="${SWARM_BOOTSTRAP_API_URL%/}"
service_unit="${SWARM_BOOTSTRAP_SERVICE_UNIT:-swarm.service}"
include_active="${SWARM_BOOTSTRAP_INCLUDE_ACTIVE:-true}"
session_limit="${SWARM_BOOTSTRAP_SESSION_LIMIT:-50}"
message_limit="${SWARM_BOOTSTRAP_MESSAGE_LIMIT:-200}"
repeat_count="${SWARM_BOOTSTRAP_REPEAT:-1}"
keep_remote="${SWARM_BOOTSTRAP_KEEP_REMOTE:-false}"
work_dir="${SWARM_BOOTSTRAP_REMOTE_WORK_DIR:-}"
if [[ -z "${work_dir}" ]]; then
  work_dir="$(mktemp -d -t swarm-bootstrap-timing.XXXXXX)"
else
  mkdir -p -- "${work_dir}"
fi
chmod 700 "${work_dir}" 2>/dev/null || true
cleanup() {
  # Never leave bearer/cookie material behind, even when preserving non-secret artifacts.
  rm -f -- "${work_dir}/curl-auth.conf" "${work_dir}/auth.json" "${work_dir}/auth.headers" 2>/dev/null || true
  if [[ "${keep_remote}" != "true" ]]; then
    rm -rf -- "${work_dir}"
  else
    printf 'REMOTE_WORK_DIR %s\n' "${work_dir}"
  fi
}
trap cleanup EXIT

started_iso="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf 'REMOTE_HOST %s\n' "$(hostname 2>/dev/null || printf unknown)"
printf 'REMOTE_STARTED_AT %s\n' "${started_iso}"
printf 'REMOTE_CPU %s\n' "$(nproc 2>/dev/null || printf unknown)"
awk '/MemTotal|MemAvailable/ {printf "REMOTE_%s %s %s\n", $1, $2, $3}' /proc/meminfo 2>/dev/null || true
printf 'REMOTE_LOADAVG %s\n' "$(cat /proc/loadavg 2>/dev/null || printf unknown)"

origin="${api_url}"
auth_json="${work_dir}/auth.json"
auth_headers="${work_dir}/auth.headers"
# Token bootstrap: direct API, same-origin headers, no frontend.
curl --silent --show-error --fail \
  --dump-header "${auth_headers}" \
  --output "${auth_json}" \
  --header 'Accept: application/json' \
  --header "Origin: ${origin}" \
  --header "Referer: ${origin}/app" \
  --header 'Sec-Fetch-Site: same-origin' \
  "${api_url}/v1/auth/desktop/session" >/dev/null

token="$(${PYTHON:-python3} - "${auth_json}" <<'PY'
import json, sys
with open(sys.argv[1], 'r', encoding='utf-8') as f:
    data=json.load(f)
token=str(data.get('token') or '').strip()
if not token:
    raise SystemExit('auth response did not include token')
print(token)
PY
)"
[[ -n "${token}" ]] || fail "empty token"
printf 'AUTH_OK token_acquired=true\n'

payload="${work_dir}/payload.json"
INCLUDE_ACTIVE="${include_active}" SESSION_LIMIT="${session_limit}" MESSAGE_LIMIT="${message_limit}" python3 - "${payload}" <<'PY'
import json, os, sys
include_active = os.environ['INCLUDE_ACTIVE'].lower() == 'true'
session_limit = int(os.environ['SESSION_LIMIT'])
message_limit = int(os.environ['MESSAGE_LIMIT'])
payload = {
    'surface': 'desktop',
    'selector': {
        'kind': 'recent',
        'global': True,
        'recent': {'limit': session_limit},
    },
    'history': {
        'mode': 'tail',
        'max_messages_per_session': message_limit,
        'manifest_policy': 'manifest',
    },
    'resources': {
        'messages': True,
        'events': False,
        'run_intents': True,
        'active_plan': True,
        'plan_revisions': False,
    },
    'include_active': include_active,
}
with open(sys.argv[1], 'w', encoding='utf-8') as f:
    json.dump(payload, f, separators=(',', ':'))
PY
printf 'PAYLOAD %s\n' "$(cat "${payload}")"

curl_config="${work_dir}/curl-auth.conf"
chmod 600 "${curl_config}" 2>/dev/null || true
{
  printf 'header = "Accept: application/json"\n'
  printf 'header = "Content-Type: application/json"\n'
  printf 'header = "Origin: %s"\n' "${origin}"
  printf 'header = "Referer: %s/app"\n' "${origin}"
  printf 'header = "Sec-Fetch-Site: same-origin"\n'
  printf 'header = "X-Swarm-Token: %s"\n' "${token}"
  printf 'header = "Cookie: swarm_desktop_session=%s"\n' "${token}"
} >"${curl_config}"
chmod 600 "${curl_config}" 2>/dev/null || true

for i in $(seq 1 "${repeat_count}"); do
  response="${work_dir}/bootstrap-${i}.json"
  metrics="${work_dir}/curl-${i}.metrics"
  curl_exit=0
  curl --silent --show-error \
    --request POST \
    --config "${curl_config}" \
    --data-binary "@${payload}" \
    --output "${response}" \
    --write-out 'http_code=%{http_code}\ntime_namelookup=%{time_namelookup}\ntime_connect=%{time_connect}\ntime_starttransfer=%{time_starttransfer}\ntime_total=%{time_total}\nsize_download=%{size_download}\nspeed_download=%{speed_download}\n' \
    "${api_url}/v3/sync/bootstrap" >"${metrics}" || curl_exit=$?
  cat "${metrics}" | sed "s/^/CURL_${i} /"
  if [[ "${curl_exit}" -ne 0 ]]; then
    printf 'CALL_%s_ERROR curl_exit=%s\n' "${i}" "${curl_exit}" >&2
    head -c 1200 "${response}" 2>/dev/null >&2 || true
    exit "${curl_exit}"
  fi
  http_code="$(awk -F= '/^http_code=/ {print $2}' "${metrics}")"
  if [[ ! "${http_code}" =~ ^2 ]]; then
    printf 'CALL_%s_ERROR http_code=%s\n' "${i}" "${http_code}" >&2
    head -c 1200 "${response}" 2>/dev/null >&2 || true
    exit 2
  fi
  python3 - "${response}" "${metrics}" "${i}" <<'PY'
import json, os, sys, time
response_path, metrics_path, idx = sys.argv[1], sys.argv[2], sys.argv[3]
metrics = {}
with open(metrics_path, 'r', encoding='utf-8') as f:
    for line in f:
        if '=' in line:
            k, v = line.strip().split('=', 1)
            metrics[k] = v
parse_start = time.perf_counter()
with open(response_path, 'r', encoding='utf-8') as f:
    data = json.load(f)
parse_ms = (time.perf_counter() - parse_start) * 1000
sessions_by_id = data.get('sessions_by_id') or {}
messages_by_session = data.get('messages_by_session') or {}
run_intents_by_session = data.get('run_intents_by_session') or {}
plans_by_session = data.get('plans_by_session') or {}
tombstones_by_session = data.get('tombstones_by_session') or {}
omissions = data.get('omissions') or []
summary = {
    'call': int(idx),
    'ok': bool(data.get('ok')),
    'http_code': int(metrics.get('http_code') or 0),
    'time_total_ms_full_body': round(float(metrics.get('time_total') or 0) * 1000, 3),
    'time_starttransfer_ms': round(float(metrics.get('time_starttransfer') or 0) * 1000, 3),
    'download_bytes_curl': int(float(metrics.get('size_download') or 0)),
    'response_file_bytes': os.path.getsize(response_path),
    'download_speed_bytes_per_sec': int(float(metrics.get('speed_download') or 0)),
    'json_parse_ms_remote': round(parse_ms, 3),
    'sessions': len(sessions_by_id),
    'session_order': len(data.get('session_order') or []),
    'messages': sum(len(v or []) for v in messages_by_session.values()),
    'sessions_with_messages': sum(1 for v in messages_by_session.values() if v),
    'run_intents': sum(len(v or []) for v in run_intents_by_session.values()),
    'sessions_with_run_intents': sum(1 for v in run_intents_by_session.values() if v),
    'active_plans': len(plans_by_session),
    'tombstones': len(tombstones_by_session),
    'omissions': len(omissions),
    'has_snapshot_cursor': isinstance(data.get('snapshot_endpoint_cursor'), str) and data.get('snapshot_endpoint_cursor', '').startswith('v3c1.'),
    'sync_scope': data.get('sync_scope') or {},
}
print('SUMMARY_JSON ' + json.dumps(summary, sort_keys=True, separators=(',', ':')))
PY
  rm -f -- "${response}"
done

printf 'JOURNAL_BOOTSTRAP_TIMINGS_BEGIN\n'
(
  journalctl --user -u "${service_unit}" --since "${started_iso}" --no-pager 2>/dev/null || \
  sudo -n journalctl -u "${service_unit}" --since "${started_iso}" --no-pager 2>/dev/null || \
  journalctl -u "${service_unit}" --since "${started_iso}" --no-pager 2>/dev/null || true
) | grep 'v3 sync bootstrap api timings' | tail -n 20 || true
printf 'JOURNAL_BOOTSTRAP_TIMINGS_END\n'
REMOTE
