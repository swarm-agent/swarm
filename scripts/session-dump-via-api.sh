#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage: scripts/session-dump-via-api.sh <session-url>

Requests a development session dump through the same-machine Desktop API
passthrough. The session URL supplies both the local Desktop origin and the
session ID. The daemon writes the private JSON dump to its session-dumps
directory and this script prints that absolute path.

Example:
  scripts/session-dump-via-api.sh http://127.0.0.1:5555/swarm-go/<session-id>

Requirements:
  - The session URL must use localhost or a loopback IP address.
  - The daemon must have dev_mode = true in swarm.conf.
  - TMPDIR must name a writable directory.
USAGE
}

fail() {
  printf 'session-dump-via-api: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

if [[ $# -eq 1 && ( "$1" == "-h" || "$1" == "--help" ) ]]; then
  usage
  exit 0
fi
if [[ $# -ne 1 ]]; then
  usage
  exit 2
fi

require_command curl
require_command jq
require_command python3

[[ -n "${TMPDIR:-}" ]] || fail "TMPDIR must be set"
[[ -d "${TMPDIR}" && -w "${TMPDIR}" ]] || fail "TMPDIR is not a writable directory: ${TMPDIR}"

parsed="$(python3 - "$1" <<'PY'
import ipaddress
import re
import sys
from urllib.parse import urlsplit

raw = sys.argv[1].strip()
try:
    parsed = urlsplit(raw)
except ValueError as exc:
    raise SystemExit(f"invalid session URL: {exc}")
if parsed.scheme not in {"http", "https"}:
    raise SystemExit("session URL scheme must be http or https")
if parsed.username is not None or parsed.password is not None:
    raise SystemExit("session URL must not contain credentials")
host = (parsed.hostname or "").strip().lower()
if not host:
    raise SystemExit("session URL must contain a host")
try:
    loopback = ipaddress.ip_address(host).is_loopback
except ValueError:
    loopback = host == "localhost"
if not loopback:
    raise SystemExit("session URL host must be localhost or a loopback IP address")
segments = [segment for segment in parsed.path.split("/") if segment]
if not segments:
    raise SystemExit("session URL path must end with a session ID")
session_id = segments[-1]
if not re.fullmatch(r"[A-Za-z0-9._:-]{1,200}", session_id):
    raise SystemExit("session ID contains unsupported characters")
origin = f"{parsed.scheme}://{parsed.netloc}"
print(origin)
print(session_id)
PY
)" || fail "could not parse session URL"

origin="${parsed%%$'\n'*}"
session_id="${parsed#*$'\n'}"
[[ -n "${origin}" && -n "${session_id}" && "${session_id}" != *$'\n'* ]] || fail "could not parse session URL"

umask 077
scratch="$(mktemp -d "${TMPDIR%/}/swarm-session-api-dump.XXXXXX")"
cleanup() {
  rm -f -- "${scratch}/cookies" "${scratch}/bootstrap.json" "${scratch}/dump-response.json"
  rmdir -- "${scratch}" 2>/dev/null || true
}
trap cleanup EXIT

common_headers=(
  -H "Origin: ${origin}"
  -H "Referer: ${origin}/"
  -H 'Sec-Fetch-Site: same-origin'
  -H 'Accept: application/json'
)

bootstrap_status="$(curl --silent --show-error --max-time 15 \
  --output "${scratch}/bootstrap.json" \
  --write-out '%{http_code}' \
  --cookie-jar "${scratch}/cookies" \
  "${common_headers[@]}" \
  "${origin}/v1/auth/desktop/session")"
if [[ ! "${bootstrap_status}" =~ ^2[0-9][0-9]$ ]]; then
  message="$(jq -r '.error // empty' "${scratch}/bootstrap.json" 2>/dev/null || true)"
  [[ -n "${message}" ]] || message="desktop session bootstrap returned HTTP ${bootstrap_status}"
  fail "${message}"
fi

request_body="$(jq -nc --arg session_id "${session_id}" '{session_id: $session_id}')"
dump_status="$(curl --silent --show-error --max-time 120 \
  --output "${scratch}/dump-response.json" \
  --write-out '%{http_code}' \
  --cookie "${scratch}/cookies" \
  "${common_headers[@]}" \
  -H 'Content-Type: application/json' \
  --data-binary "${request_body}" \
  "${origin}/v3/developer/session-dump")"
if [[ ! "${dump_status}" =~ ^2[0-9][0-9]$ ]]; then
  message="$(jq -r '.error // empty' "${scratch}/dump-response.json" 2>/dev/null || true)"
  [[ -n "${message}" ]] || message="session dump API returned HTTP ${dump_status}"
  fail "${message}"
fi

response_session_id="$(jq -er '.session_id | select(type == "string" and length > 0)' "${scratch}/dump-response.json")" \
  || fail "session dump API response omitted session_id"
[[ "${response_session_id}" == "${session_id}" ]] || fail "session dump API returned the wrong session_id"

dump_path="$(jq -er '.path | select(type == "string" and startswith("/"))' "${scratch}/dump-response.json")" \
  || fail "session dump API response omitted an absolute dump path"
bytes_written="$(jq -er '.bytes_written | select(type == "number" and . > 0)' "${scratch}/dump-response.json")" \
  || fail "session dump API response did not confirm a non-empty dump"

if [[ -e "${dump_path}" ]]; then
  [[ -f "${dump_path}" && -s "${dump_path}" ]] || fail "API dump path is not a non-empty regular file: ${dump_path}"
fi

printf 'session-dump-via-api: wrote %s bytes for session %s\n' "${bytes_written}" "${session_id}" >&2
printf '%s\n' "${dump_path}"
