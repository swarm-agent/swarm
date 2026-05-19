#!/usr/bin/env bash
set -euo pipefail

EVIDENCE_DIR="${1:-.tmp/checkpoint-1/slice-1.3/$(date -u +%Y%m%dT%H%M%SZ)-vm-jwt-live-proof}"
HOST_EVIDENCE_DIR="${SWARM_HOST_EVIDENCE_DIR:-}"
mkdir -p "${EVIDENCE_DIR}"

COMMANDS_LOG="${EVIDENCE_DIR}/commands.log"
REQUESTS_LOG="${EVIDENCE_DIR}/requests.ndjson"
DAEMON_LOG="${EVIDENCE_DIR}/daemon.log"
SUMMARY_JSON="${EVIDENCE_DIR}/summary.json"
: >"${COMMANDS_LOG}"
: >"${REQUESTS_LOG}"
: >"${DAEMON_LOG}"

log_cmd() { printf '%s\n' "$*" | tee -a "${COMMANDS_LOG}" >&2; }
fail() {
  local msg="${1:-proof failed}"
  jq -nc --arg status FAIL --arg error "${msg}" --arg evidence_dir "${EVIDENCE_DIR}" \
    '{status:$status,error:$error,evidence_dir:$evidence_dir}' >"${SUMMARY_JSON}"
  printf 'error: %s\n' "${msg}" >&2
  exit 1
}

require_command() { command -v "${1:-}" >/dev/null 2>&1 || fail "required command not found: ${1:-}"; }

ROOT_DIR="$(pwd)"
if [[ ! -f "${ROOT_DIR}/swarmd/go.mod" ]]; then
  fail "must run from swarm-go repo root"
fi
if [[ -f "${ROOT_DIR}/scripts/lib-go.sh" ]]; then
  # shellcheck disable=SC1091
  source "${ROOT_DIR}/scripts/lib-go.sh"
  swarm_require_go "${ROOT_DIR}"
fi
require_command go
require_command curl
require_command jq
require_command awk
require_command sed

RUN_ROOT="$(mktemp -d /var/tmp/swarm-slice13-live-XXXXXX)"
cleanup() {
  if [[ -n "${DAEMON_PID:-}" ]] && kill -0 "${DAEMON_PID}" 2>/dev/null; then
    kill "${DAEMON_PID}" 2>/dev/null || true
    wait "${DAEMON_PID}" 2>/dev/null || true
  fi
  rm -rf -- "${RUN_ROOT}"
}
trap cleanup EXIT

BIN="${RUN_ROOT}/swarmd"
DB_PATH="${RUN_ROOT}/state/swarmd.pebble"
DATA_DIR="${RUN_ROOT}/system-data"
RUNTIME_DIR="${RUN_ROOT}/system-runtime"
CONFIG_DIR="${RUN_ROOT}/system-config"
CACHE_DIR="${RUN_ROOT}/system-cache"
HOME_DIR="${RUN_ROOT}/home"
mkdir -p "${DATA_DIR}" "${RUNTIME_DIR}" "${CONFIG_DIR}" "${CACHE_DIR}" "${RUN_ROOT}/system-logs" "${HOME_DIR}" "$(dirname "${DB_PATH}")"

choose_port() {
  awk 'BEGIN{srand(); print int(20000 + rand() * 20000)}'
}
API_PORT="$(choose_port)"
DESKTOP_PORT="$((API_PORT + 1))"
for _ in $(seq 1 50); do
  PEER_PORT="$((API_PORT + 2))"
  if ! ss -H -ltn "sport = :${API_PORT}" 2>/dev/null | grep -q . && ! ss -H -ltn "sport = :${DESKTOP_PORT}" 2>/dev/null | grep -q . && ! ss -H -ltn "sport = :${PEER_PORT}" 2>/dev/null | grep -q .; then
    break
  fi
  API_PORT="$((API_PORT + 3))"
  DESKTOP_PORT="$((API_PORT + 1))"
done
PEER_PORT="$((API_PORT + 2))"
API_URL="http://127.0.0.1:${API_PORT}"
DESKTOP_URL="http://127.0.0.1:${DESKTOP_PORT}"
COOKIE_JAR="${RUN_ROOT}/cookies.txt"

log_cmd "cd swarmd && go test ./internal/identity ./internal/api -run 'Test(Session|Desktop|Local|Auth|Protected|JWT|Onboarding)'"
(cd swarmd && go test ./internal/identity ./internal/api -run 'Test(Session|Desktop|Local|Auth|Protected|JWT|Onboarding)')

log_cmd "cd swarmd && go build -o '${BIN}' ./cmd/swarmd"
(cd swarmd && go build -o "${BIN}" ./cmd/swarmd)

start_daemon() {
  : >"${DAEMON_LOG}.current"
  log_cmd "start swarmd --listen 127.0.0.1:${API_PORT} --desktop-port ${DESKTOP_PORT} --data-dir <tmp> --db-path <tmp>"
  HOME="${HOME_DIR}" \
  XDG_DATA_HOME="${RUN_ROOT}/xdg-data" \
  XDG_RUNTIME_DIR="${RUN_ROOT}/xdg-runtime" \
  XDG_CONFIG_HOME="${RUN_ROOT}/xdg-config" \
  XDG_CACHE_HOME="${RUN_ROOT}/xdg-cache" \
  STATE_DIRECTORY="${DATA_DIR}" \
  RUNTIME_DIRECTORY="${RUNTIME_DIR}" \
  CONFIGURATION_DIRECTORY="${CONFIG_DIR}" \
  CACHE_DIRECTORY="${CACHE_DIR}" \
  LOGS_DIRECTORY="${RUN_ROOT}/system-logs" \
  SWARM_CHILD_STARTUP_CONFIG="${STARTUP_CONFIG_TEXT}" \
    "${BIN}" --listen "127.0.0.1:${API_PORT}" --desktop-port "${DESKTOP_PORT}" --data-dir "${DATA_DIR}/swarm" --db-path "${DB_PATH}" --lock-path "${RUNTIME_DIR}/swarmd.lock" \
    >"${DAEMON_LOG}.current" 2>&1 &
  DAEMON_PID="$!"
  for _ in $(seq 1 100); do
    if curl -fsS "${API_URL}/readyz" >/dev/null 2>&1; then
      cat "${DAEMON_LOG}.current" >>"${DAEMON_LOG}"
      return 0
    fi
    if ! kill -0 "${DAEMON_PID}" 2>/dev/null; then
      cat "${DAEMON_LOG}.current" >>"${DAEMON_LOG}" || true
      fail "swarmd exited before ready"
    fi
    sleep 0.1
  done
  cat "${DAEMON_LOG}.current" >>"${DAEMON_LOG}" || true
  fail "swarmd did not become ready"
}

stop_daemon() {
  if [[ -z "${DAEMON_PID:-}" ]] || ! kill -0 "${DAEMON_PID}" 2>/dev/null; then
    DAEMON_PID=""
    return 0
  fi
  log_cmd "stop swarmd before offline DB probe"
  curl -fsS -X POST -H 'Content-Type: application/json' --data '{"reason":"slice13-proof"}' "${API_URL}/v1/system/shutdown" >/dev/null 2>&1 || kill "${DAEMON_PID}" 2>/dev/null || true
  for _ in $(seq 1 100); do
    if ! kill -0 "${DAEMON_PID}" 2>/dev/null; then
      wait "${DAEMON_PID}" 2>/dev/null || true
      DAEMON_PID=""
      cat "${DAEMON_LOG}.current" >>"${DAEMON_LOG}" 2>/dev/null || true
      return 0
    fi
    sleep 0.1
  done
  kill "${DAEMON_PID}" 2>/dev/null || true
  wait "${DAEMON_PID}" 2>/dev/null || true
  DAEMON_PID=""
  cat "${DAEMON_LOG}.current" >>"${DAEMON_LOG}" 2>/dev/null || true
}

redact_json_file() {
  local in="${1:-}" out="${2:-}"
  jq 'walk(if type == "string" and (test("^[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+$") or length > 80) then "<redacted>" else . end)' "${in}" >"${out}"
}

record_request() {
  local name="${1:-}" method="${2:-}" url="${3:-}" body="${4:-}" out="${5:-}"
  local status headers tmpbody
  headers="${RUN_ROOT}/${name}.headers"
  tmpbody="${RUN_ROOT}/${name}.body"
  if [[ -n "${body}" ]]; then
    status="$(curl -sS -o "${tmpbody}" -D "${headers}" -w '%{http_code}' -X "${method}" -H 'Content-Type: application/json' --data "${body}" -c "${COOKIE_JAR}" -b "${COOKIE_JAR}" -H "Origin: ${DESKTOP_URL}" -H "Referer: ${DESKTOP_URL}/" -H 'Sec-Fetch-Site: same-origin' "${url}")"
  else
    status="$(curl -sS -o "${tmpbody}" -D "${headers}" -w '%{http_code}' -X "${method}" -c "${COOKIE_JAR}" -b "${COOKIE_JAR}" -H "Origin: ${DESKTOP_URL}" -H "Referer: ${DESKTOP_URL}/" -H 'Sec-Fetch-Site: same-origin' "${url}")"
  fi
  if jq . "${tmpbody}" >/dev/null 2>&1; then
    redact_json_file "${tmpbody}" "${out}"
  else
    sed -E 's/[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/<redacted>/g' "${tmpbody}" >"${out}"
  fi
  jq -nc --arg name "${name}" --arg method "${method}" --arg url "${url}" --arg status "${status}" --rawfile body "${out}" \
    '{name:$name,method:$method,url:$url,status:($status|tonumber),body:$body}' >>"${REQUESTS_LOG}"
  printf '%s' "${status}"
}

write_identity_probe() {
  PROBE_DIR="swarmd/.cache/slice13/identitysummaryprobe"
  mkdir -p "${PROBE_DIR}"
  cat >"${PROBE_DIR}/main.go" <<'GOEOF'
package main

import (
  "encoding/json"
  "flag"
  "fmt"
  "os"

  "swarm/packages/swarmd/internal/identity"
  pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func main() {
  dbPath := flag.String("db", "", "path to swarmd pebble DB")
  flag.Parse()
  if *dbPath == "" {
    fmt.Fprintln(os.Stderr, "--db is required")
    os.Exit(2)
  }
  store, err := pebblestore.Open(*dbPath)
  if err != nil {
    panic(fmt.Errorf("open pebble db: %w", err))
  }
  defer store.Close()
  svc := identity.NewService(pebblestore.NewIdentityStore(store))
  summary, err := svc.StateSummary()
  if err != nil {
    panic(err)
  }
  out := map[string]any{"identity": summary}
  enc := json.NewEncoder(os.Stdout)
  enc.SetIndent("", "  ")
  if err := enc.Encode(out); err != nil {
    panic(err)
  }
}
GOEOF
}

write_identity_probe
STARTUP_CONFIG_TEXT="$(printf 'startup_mode = interactive\\nhost = 127.0.0.1\\nport = %s\\ndesktop_port = %s\\npeer_transport_port = %s\\n' "${API_PORT}" "${DESKTOP_PORT}" "${PEER_PORT}")"

start_daemon

before_status="$(record_request auth-session-before-bootstrap GET "${DESKTOP_URL}/v1/auth/desktop/session" "" "${EVIDENCE_DIR}/auth-session-before-bootstrap.json")"
[[ "${before_status}" == "401" ]] || fail "desktop session before bootstrap status ${before_status}, want 401"

stop_daemon
log_cmd "offline identity summary before bootstrap"
(cd swarmd && go run ./.cache/slice13/identitysummaryprobe --db "${DB_PATH}") >"${EVIDENCE_DIR}/identity-summary-before-bootstrap.json"
jq -e '.identity.counts.users == 0 and .identity.counts.teams == 0 and .identity.counts.team_memberships == 0 and .identity.counts.current_selections == 0' "${EVIDENCE_DIR}/identity-summary-before-bootstrap.json" >/dev/null || fail "identity counts before bootstrap are not zero"

start_daemon
only_swarm_status="$(record_request onboarding-old-shape POST "${DESKTOP_URL}/v1/onboarding" '{"swarm_name":"Slice13 Device"}' "${EVIDENCE_DIR}/onboarding-old-shape.json")"
[[ "${only_swarm_status}" == "400" ]] || fail "old onboarding shape status ${only_swarm_status}, want 400"
bootstrap_status="$(record_request onboarding-bootstrap POST "${DESKTOP_URL}/v1/onboarding" '{"username":"slice13-user","swarm_name":"Slice13 Device"}' "${EVIDENCE_DIR}/onboarding-bootstrap.json")"
[[ "${bootstrap_status}" == "200" ]] || fail "onboarding bootstrap status ${bootstrap_status}, want 200"
jq -e '.identity.bootstrapped == true and .identity.user_id != "" and .identity.username == "slice13-user"' "${EVIDENCE_DIR}/onboarding-bootstrap.json" >/dev/null || fail "bootstrap response missing user-first identity"

session_status="$(record_request auth-session-after-bootstrap GET "${DESKTOP_URL}/v1/auth/desktop/session" "" "${EVIDENCE_DIR}/auth-session-after-bootstrap.json")"
[[ "${session_status}" == "200" ]] || fail "desktop session after bootstrap status ${session_status}, want 200"
jq -e '.ok == true and .user_id != ""' "${EVIDENCE_DIR}/auth-session-after-bootstrap.json" >/dev/null || fail "session response missing user_id"
TOKEN="$(awk '$6 == "swarm_desktop_session" { value=$7 } END { print value }' "${COOKIE_JAR}")"
[[ -n "${TOKEN}" ]] || fail "missing desktop session cookie token"
printf '%s\n' "${TOKEN}" | awk -F. 'NF == 3 { ok = 1 } END { exit(ok ? 0 : 1) }' || fail "desktop cookie is not a compact JWT"

stop_daemon
log_cmd "offline identity summary after bootstrap"
(cd swarmd && go run ./.cache/slice13/identitysummaryprobe --db "${DB_PATH}") >"${EVIDENCE_DIR}/identity-summary-after-bootstrap.json"
jq -e '.identity.counts.users == 1 and .identity.counts.teams == 1 and .identity.counts.team_memberships == 1 and .identity.counts.current_selections == 1 and .identity.current_user.username == "slice13-user"' "${EVIDENCE_DIR}/identity-summary-after-bootstrap.json" >/dev/null || fail "identity summary after bootstrap failed invariants"

start_daemon
restart_status="$(record_request auth-session-after-restart GET "${DESKTOP_URL}/v1/auth/desktop/session" "" "${EVIDENCE_DIR}/auth-session-after-restart.json")"
[[ "${restart_status}" == "200" ]] || fail "desktop session after restart status ${restart_status}, want 200"
old_cookie_status="$(curl -sS -o "${RUN_ROOT}/old-cookie.body" -w '%{http_code}' -H "Cookie: swarm_desktop_session=${TOKEN}" -H "Origin: ${DESKTOP_URL}" -H "Referer: ${DESKTOP_URL}/" -H 'Sec-Fetch-Site: same-origin' "${DESKTOP_URL}/v1/onboarding")"
cp "${RUN_ROOT}/old-cookie.body" "${EVIDENCE_DIR}/old-issued-jwt-after-restart.json"
[[ "${old_cookie_status}" == "200" ]] || fail "old issued JWT after restart status ${old_cookie_status}, want 200"
random_cookie_status="$(curl -sS -o "${RUN_ROOT}/random-cookie.body" -w '%{http_code}' -H 'Cookie: swarm_desktop_session=old-random-cookie' -H "Origin: ${DESKTOP_URL}" -H "Referer: ${DESKTOP_URL}/" -H 'Sec-Fetch-Site: same-origin' "${DESKTOP_URL}/v1/vault")"
cp "${RUN_ROOT}/random-cookie.body" "${EVIDENCE_DIR}/old-random-cookie-rejected.json"
[[ "${random_cookie_status}" == "401" ]] || fail "random cookie protected API status ${random_cookie_status}, want 401"

stop_daemon
log_cmd "offline identity summary after restart"
(cd swarmd && go run ./.cache/slice13/identitysummaryprobe --db "${DB_PATH}") >"${EVIDENCE_DIR}/identity-summary-after-restart.json"
jq -e '.identity.counts.users == 1 and .identity.current_user.username == "slice13-user"' "${EVIDENCE_DIR}/identity-summary-after-restart.json" >/dev/null || fail "identity summary after restart failed invariants"

jq -n \
  --arg status PASS \
  --arg evidence_dir "${EVIDENCE_DIR}" \
  --arg api_url "${API_URL}" \
  --arg desktop_url "${DESKTOP_URL}" \
  --slurpfile before "${EVIDENCE_DIR}/identity-summary-before-bootstrap.json" \
  --slurpfile after "${EVIDENCE_DIR}/identity-summary-after-bootstrap.json" \
  --slurpfile restart "${EVIDENCE_DIR}/identity-summary-after-restart.json" \
  '{status:$status,evidence_dir:$evidence_dir,api_url:$api_url,desktop_url:$desktop_url,checks:{before_counts:$before[0].identity.counts,after_counts:$after[0].identity.counts,restart_counts:$restart[0].identity.counts,jwt_valid_after_restart:true}}' >"${SUMMARY_JSON}"

if [[ -n "${HOST_EVIDENCE_DIR}" ]]; then
  mkdir -p "${HOST_EVIDENCE_DIR}"
  cp -a "${EVIDENCE_DIR}/." "${HOST_EVIDENCE_DIR}/"
fi
printf 'PASS slice 1.3 VM live proof evidence_dir=%s\n' "${EVIDENCE_DIR}"
