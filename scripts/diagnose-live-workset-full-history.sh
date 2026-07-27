#!/usr/bin/env bash
set -euo pipefail

# Hits the currently running local Swarm backend. No test server. No seeded data.
# Measures POST /v3/sessions:workset with history.mode=full and no
# max_messages_per_session, which the backend treats as all messages.

DEFAULT_API_PORT=7781
DEFAULT_DESKTOP_PORT=5555
DEFAULT_PEER_TRANSPORT_PORT=7791

BASE_URL="${SWARM_API_BASE_URL:-}"
LIMIT=50
WORKSPACES=()

usage() {
  cat <<'USAGE'
Usage: scripts/diagnose-live-workset-full-history.sh [--base-url URL] [--limit N] [--workspace PATH]...

Discovers the live Swarm backend and POSTs /v3/sessions:workset with full
history and no max_messages_per_session limit.

Default ports:
  backend/API:      7781
  desktop:          5555
  peer transport:   7791
USAGE
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --base-url) BASE_URL="${2:-}"; shift 2 ;;
    --limit) LIMIT="${2:-}"; shift 2 ;;
    --workspace) WORKSPACES+=("${2:-}"); shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage >&2; exit 2 ;;
  esac
done

command -v curl >/dev/null || { echo "curl required" >&2; exit 1; }
command -v python3 >/dev/null || { echo "python3 required" >&2; exit 1; }

normalize_url() {
  local raw="$1"
  raw="${raw%/}"
  if [[ "$raw" == http://* || "$raw" == https://* ]]; then
    printf '%s' "$raw"
  else
    printf 'http://%s' "$raw"
  fi
}

listen_to_hostport() {
  local listen="$1"
  listen="${listen%/}"
  if [[ "$listen" == http://* || "$listen" == https://* ]]; then
    printf '%s' "$listen"
  elif [[ "$listen" == :* ]]; then
    printf '127.0.0.1%s' "$listen"
  elif [[ "$listen" == 0.0.0.0:* || "$listen" == '[::]:'* ]]; then
    printf '127.0.0.1:%s' "${listen##*:}"
  else
    printf '%s' "$listen"
  fi
}

json_field() {
  python3 - "$1" "$2" <<'PY'
import json, sys
try:
    data = json.load(open(sys.argv[1], encoding='utf-8'))
    cur = data
    for part in sys.argv[2].split('.'):
        cur = cur[part]
    print(cur)
except Exception:
    sys.exit(1)
PY
}

conf_value() {
  python3 - "$1" "$2" <<'PY'
import re, sys
try:
    lines = open(sys.argv[1], encoding='utf-8').read().splitlines()
except Exception:
    sys.exit(1)
key = sys.argv[2]
for line in lines:
    line = line.strip()
    if not line or line.startswith('#'):
        continue
    m = re.match(r'([A-Za-z0-9_.-]+)\s*=\s*(.*)', line)
    if m and m.group(1) == key:
        print(m.group(2).strip().strip('"').strip("'"))
        sys.exit(0)
sys.exit(1)
PY
}

candidates=()
add_candidate() {
  local value="${1:-}"
  [[ -n "$value" ]] || return 0
  candidates+=("$(normalize_url "$(listen_to_hostport "$value")")")
}

add_candidate "$BASE_URL"
add_candidate "${SWARMD_LISTEN:-}"

if [[ -n "${RUNTIME_DIRECTORY:-}" && -f "${RUNTIME_DIRECTORY%/}/swarmd.lock" ]]; then
  add_candidate "$(json_field "${RUNTIME_DIRECTORY%/}/swarmd.lock" listen_addr 2>/dev/null || true)"
fi
if [[ -f /run/swarmd/swarmd.lock ]]; then
  add_candidate "$(json_field /run/swarmd/swarmd.lock listen_addr 2>/dev/null || true)"
fi
if [[ -f /etc/swarmd/swarm.conf ]]; then
  host="$(conf_value /etc/swarmd/swarm.conf host 2>/dev/null || true)"
  port="$(conf_value /etc/swarmd/swarm.conf port 2>/dev/null || true)"
  [[ -n "$port" ]] && add_candidate "${host:-127.0.0.1}:$port"
fi

add_candidate "127.0.0.1:$DEFAULT_API_PORT"
add_candidate "127.0.0.1:$DEFAULT_DESKTOP_PORT"
mapfile -t candidates < <(printf '%s\n' "${candidates[@]}" | awk 'NF && !seen[$0]++')

LIVE_URL=""
for candidate in "${candidates[@]}"; do
  if curl -fsS --max-time 2 "$candidate/readyz" >/dev/null 2>&1 || curl -fsS --max-time 2 "$candidate/healthz" >/dev/null 2>&1; then
    LIVE_URL="$candidate"
    break
  fi
done

if [[ -z "$LIVE_URL" ]]; then
  echo "failed to find running Swarm backend" >&2
  echo "checked candidates:" >&2
  printf '  %s\n' "${candidates[@]}" >&2
  echo "default_ports api=$DEFAULT_API_PORT desktop=$DEFAULT_DESKTOP_PORT peer_transport=$DEFAULT_PEER_TRANSPORT_PORT" >&2
  exit 1
fi

cookie_jar="$(mktemp)"
request_body="$(mktemp)"
response_body="$(mktemp)"
trap 'rm -f "$cookie_jar" "$request_body" "$response_body"' EXIT

# Same desktop auth bootstrap path used by web/src/app/api.ts. The backend
# only treats this as desktop-browser auth when same-origin browser headers are present.
origin="$LIVE_URL"
curl -fsS --max-time 10 -c "$cookie_jar" -b "$cookie_jar" \
  -H 'Accept: application/json' \
  -H "Origin: $origin" \
  -H "Referer: $origin/" \
  -H 'Sec-Fetch-Site: same-origin' \
  "$LIVE_URL/v1/auth/desktop/session" >/dev/null 2>&1 || true

python3 - "$request_body" "$LIMIT" "${WORKSPACES[@]}" <<'PY'
import json, sys
path = sys.argv[1]
limit = int(sys.argv[2])
workspaces = [w for w in sys.argv[3:] if w.strip()]
payload = {
    "recent": {"limit": limit},
    "history": {
        "mode": "full",
        "max_events_per_session": 0,
        "manifest_policy": "manifest",
        "include_events": False,
    },
}
if workspaces:
    payload["workspace"] = {"workspace_paths": workspaces}
open(path, 'w', encoding='utf-8').write(json.dumps(payload, separators=(',', ':')))
PY

echo "base_url=$LIVE_URL"
echo "checked_candidates=$(IFS=,; echo "${candidates[*]}")"
echo "default_ports api=$DEFAULT_API_PORT desktop=$DEFAULT_DESKTOP_PORT peer_transport=$DEFAULT_PEER_TRANSPORT_PORT"
echo "request=$(cat "$request_body")"

start_ns="$(date +%s%N)"
http_code="$(curl -sS -o "$response_body" -w '%{http_code}' \
  --max-time 300 \
  -c "$cookie_jar" -b "$cookie_jar" \
  -H 'Accept: application/json' \
  -H 'Content-Type: application/json' \
  -H "Origin: $origin" \
  -H "Referer: $origin/" \
  -H 'Sec-Fetch-Site: same-origin' \
  --data-binary "@$request_body" \
  "$LIVE_URL/v3/sessions:workset")"
end_ns="$(date +%s%N)"

elapsed_ms="$(( (end_ns - start_ns) / 1000000 ))"
response_bytes="$(wc -c < "$response_body" | tr -d ' ')"

echo "http_code=$http_code"
echo "elapsed_ms=$elapsed_ms"
echo "response_bytes=$response_bytes"

python3 - "$response_body" <<'PY'
import json, sys
try:
    payload = json.load(open(sys.argv[1], encoding='utf-8'))
except Exception as exc:
    print(f"json_parse_error={exc}")
    print(open(sys.argv[1], encoding='utf-8', errors='replace').read()[:4000])
    sys.exit(0)
sessions = payload.get('sessions_by_id') or {}
messages = payload.get('messages_by_session') or {}
omissions = payload.get('omissions') or []
plans = payload.get('plans_by_session') or {}
revisions = payload.get('plan_revisions_by_session') or {}
message_count = sum(len(v or []) for v in messages.values())
max_messages = max([len(v or []) for v in messages.values()] or [0])
print(f"ok={payload.get('ok')}")
print(f"session_count={len(sessions)}")
print(f"message_count={message_count}")
print(f"max_messages_in_one_session={max_messages}")
print(f"omission_count={len(omissions)}")
print(f"plans_by_session_count={len(plans)}")
print(f"plan_revisions_by_session_count={len(revisions)}")
if omissions:
    print("first_omission=" + json.dumps(omissions[0], separators=(',', ':')))
PY

if [[ "$http_code" != 2* ]]; then
  echo "non-2xx response body:" >&2
  sed -n '1,160p' "$response_body" >&2
  exit 1
fi
