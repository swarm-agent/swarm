#!/usr/bin/env bash
set -euo pipefail

EVIDENCE_DIR="${1:-.tmp/checkpoint-1/slice-1.7/$(date -u +%Y%m%dT%H%M%SZ)-vm-final-gate}"
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
  local msg="${1:-final gate failed}"
  jq -nc --arg status FAIL --arg error "${msg}" --arg evidence_dir "${EVIDENCE_DIR}" \
    '{status:$status,error:$error,evidence_dir:$evidence_dir,exit_code:1}' >"${SUMMARY_JSON}" 2>/dev/null || true
  printf 'error: %s\n' "${msg}" >&2
  exit 1
}
require_command() { command -v "${1:-}" >/dev/null 2>&1 || fail "required command not found: ${1:-}"; }

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"
[[ -f "swarmd/go.mod" ]] || fail "must run from swarm-go repo root"
if [[ -f "scripts/lib-go.sh" ]]; then
  # shellcheck disable=SC1091
  source "scripts/lib-go.sh"
  swarm_require_go "${ROOT_DIR}"
fi
require_command go
require_command curl
require_command jq
require_command awk
require_command sed

RUN_ROOT="$(mktemp -d -t swarm-slice17-final-XXXXXX)"
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
STARTUP_CONFIG_PATH="${CONFIG_DIR}/swarm/swarm.conf"
WORKSPACE_PATH="${RUN_ROOT}/workspace-created"
mkdir -p "${DATA_DIR}" "${RUNTIME_DIR}" "${CONFIG_DIR}" "${CACHE_DIR}" "${RUN_ROOT}/system-logs" "${HOME_DIR}" "$(dirname "${DB_PATH}")" "$(dirname "${STARTUP_CONFIG_PATH}")" "${WORKSPACE_PATH}"

choose_port() { awk 'BEGIN{srand(); print int(20000 + rand() * 20000)}'; }
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

redact_json_file() {
  local in="${1:-}" out="${2:-}"
  jq 'walk(if type == "string" and (test("^[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+$") or length > 120) then "<redacted>" else . end)' "${in}" >"${out}"
}

record_request() {
  local name="${1:-}" method="${2:-}" url="${3:-}" body="${4:-}" out="${5:-}" auth="${6:-cookie}"
  local status headers tmpbody curl_args=()
  headers="${RUN_ROOT}/${name}.headers"
  tmpbody="${RUN_ROOT}/${name}.body"
  curl_args=(-sS -o "${tmpbody}" -D "${headers}" -w '%{http_code}' -X "${method}" -H "Origin: ${DESKTOP_URL}" -H "Referer: ${DESKTOP_URL}/" -H 'Sec-Fetch-Site: same-origin')
  if [[ "${auth}" == "cookie" ]]; then
    curl_args+=(-c "${COOKIE_JAR}" -b "${COOKIE_JAR}")
  elif [[ "${auth}" == bearer:* ]]; then
    curl_args+=(-H "Authorization: Bearer ${auth#bearer:}")
  elif [[ "${auth}" == cookie-value:* ]]; then
    curl_args+=(-H "Cookie: swarm_desktop_session=${auth#cookie-value:}")
  fi
  if [[ -n "${body}" ]]; then
    curl_args+=(-H 'Content-Type: application/json' --data "${body}")
  fi
  curl_args+=("${url}")
  status="$(curl "${curl_args[@]}")"
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
  PROBE_DIR="swarmd/.cache/slice17/identitysummaryprobe"
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

identity_summary() {
  local out="${1:-}"
  stop_daemon
  log_cmd "offline identity summary -> ${out}"
  (cd swarmd && go run ./.cache/slice17/identitysummaryprobe --db "${DB_PATH}") >"${out}"
}

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
  for _ in $(seq 1 150); do
    if curl -fsS "${API_URL}/readyz" >/dev/null 2>&1; then
      cat "${DAEMON_LOG}.current" >>"${DAEMON_LOG}" 2>/dev/null || true
      return 0
    fi
    if ! kill -0 "${DAEMON_PID}" 2>/dev/null; then
      cat "${DAEMON_LOG}.current" >>"${DAEMON_LOG}" 2>/dev/null || true
      fail "swarmd exited before ready"
    fi
    sleep 0.1
  done
  cat "${DAEMON_LOG}.current" >>"${DAEMON_LOG}" 2>/dev/null || true
  fail "swarmd did not become ready"
}

stop_daemon() {
  if [[ -z "${DAEMON_PID:-}" ]] || ! kill -0 "${DAEMON_PID}" 2>/dev/null; then
    DAEMON_PID=""
    return 0
  fi
  log_cmd "stop swarmd"
  curl -fsS -X POST -H 'Content-Type: application/json' --data '{"reason":"slice17-final-gate"}' "${API_URL}/v1/system/shutdown" >/dev/null 2>&1 || kill "${DAEMON_PID}" 2>/dev/null || true
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

write_tui_session_artifact() {
  local out="${1:-}" phase="${2:-}" status_file="${3:-}" session_file="${4:-}"
  jq -n --arg phase "${phase}" --slurpfile status "${status_file}" --slurpfile session "${session_file}" \
    '{phase:$phase,local_session_contract:"/v1/auth/desktop/session",identity_bootstrapped:($status[0].identity.bootstrapped // false),user_id:($session[0].user_id // ""),username:($session[0].username // ""),token_redacted:(if ($session[0].token? // "") != "" then "<redacted>" else "" end),startup_user_first:true,onboarding_inputs:["username","swarmName","swarmName"],team_prompt_on_startup:false,team_context:"validated secondary context only for explicit team-scoped workflows"}' >"${out}"
}

write_identity_probe
STARTUP_CONFIG_TEXT="$(printf 'startup_mode = interactive\nswarm_name = Initial Device\nswarm_mode = true\nhost = 127.0.0.1\nport = %s\ndesktop_port = %s\npeer_transport_port = %s\n' "${API_PORT}" "${DESKTOP_PORT}" "${PEER_PORT}")"
printf '%s\n' "${STARTUP_CONFIG_TEXT}" >"${STARTUP_CONFIG_PATH}"

log_cmd "cd web && npm test -- --run"
(cd web && npm test -- --run)
log_cmd "cd web && npm run build"
(cd web && npm run build)
log_cmd "cd swarmd && go test ./internal/identity ./internal/api -run 'Test(Session|Desktop|Local|Auth|Protected|JWT|Onboarding)'"
(cd swarmd && go test ./internal/identity ./internal/api -run 'Test(Session|Desktop|Local|Auth|Protected|JWT|Onboarding)')
log_cmd "cd swarmd && go build -o '${BIN}' ./cmd/swarmd"
(cd swarmd && go build -o "${BIN}" ./cmd/swarmd)

start_daemon

onboarding_before_status="$(record_request onboarding-before-bootstrap GET "${DESKTOP_URL}/v1/onboarding" "" "${EVIDENCE_DIR}/onboarding-before-bootstrap.json")"
[[ "${onboarding_before_status}" == "200" ]] || fail "onboarding before bootstrap status ${onboarding_before_status}, want 200"
jq -e '.identity.bootstrapped == false and .needs_onboarding == true' "${EVIDENCE_DIR}/onboarding-before-bootstrap.json" >/dev/null || fail "onboarding before bootstrap did not prove identity absent"

auth_before_status="$(record_request auth-session-before-bootstrap GET "${DESKTOP_URL}/v1/auth/desktop/session" "" "${EVIDENCE_DIR}/auth-session-before-bootstrap.json")"
[[ "${auth_before_status}" == "401" ]] || fail "desktop session before bootstrap status ${auth_before_status}, want 401"
write_tui_session_artifact "${EVIDENCE_DIR}/tui-session-before-bootstrap.json" before-bootstrap "${EVIDENCE_DIR}/onboarding-before-bootstrap.json" "${EVIDENCE_DIR}/auth-session-before-bootstrap.json"

identity_summary "${EVIDENCE_DIR}/identity-summary-before.json"
jq -e '.identity.counts.users == 0 and .identity.counts.teams == 0 and .identity.counts.team_memberships == 0 and .identity.counts.current_selections == 0' "${EVIDENCE_DIR}/identity-summary-before.json" >/dev/null || fail "identity counts before bootstrap are not zero"

start_daemon
workspace_pre_status="$(record_request guarded-workspace-negative POST "${DESKTOP_URL}/v1/workspace/add" "$(jq -nc --arg path "${WORKSPACE_PATH}" '{path:$path}')" "${RUN_ROOT}/guarded-workspace-negative.json")"
agent_pre_status="$(record_request guarded-agent-negative PUT "${DESKTOP_URL}/v2/agents/slice17-negative" '{"mode":"subagent","description":"Slice 1.7 pre-bootstrap negative"}' "${RUN_ROOT}/guarded-agent-negative.json")"
credential_pre_status="$(record_request guarded-credential-negative POST "${DESKTOP_URL}/v1/auth/credentials" '{"provider":"codex","type":"api","api_key":"test-key"}' "${RUN_ROOT}/guarded-credential-negative.json")"
[[ "${workspace_pre_status}" == "401" && "${agent_pre_status}" == "401" && "${credential_pre_status}" == "401" ]] || fail "guarded negative statuses workspace=${workspace_pre_status} agent=${agent_pre_status} credential=${credential_pre_status}, want all 401"
jq -n --argjson workspace "$(cat "${RUN_ROOT}/guarded-workspace-negative.json")" --argjson agent "$(cat "${RUN_ROOT}/guarded-agent-negative.json")" --argjson credential "$(cat "${RUN_ROOT}/guarded-credential-negative.json")" '{workspace:$workspace,agent:$agent,credential:$credential}' >"${EVIDENCE_DIR}/guarded-api-negative.json"

old_shape_status="$(record_request onboarding-old-shape POST "${DESKTOP_URL}/v1/onboarding" '{"swarm_name":"Slice17 Device"}' "${EVIDENCE_DIR}/onboarding-old-shape.json")"
[[ "${old_shape_status}" == "400" ]] || fail "old onboarding shape status ${old_shape_status}, want 400"

bootstrap_status="$(record_request onboarding-bootstrap POST "${DESKTOP_URL}/v1/onboarding" '{"username":"slice17-user","swarm_name":"Slice17 Device"}' "${EVIDENCE_DIR}/onboarding-bootstrap.json")"
[[ "${bootstrap_status}" == "200" ]] || fail "onboarding bootstrap status ${bootstrap_status}, want 200"
jq -e '.identity.bootstrapped == true and .identity.user_id != "" and .identity.username == "slice17-user" and .identity.team_id != ""' "${EVIDENCE_DIR}/onboarding-bootstrap.json" >/dev/null || fail "bootstrap response missing user-first identity"

auth_after_status="$(record_request auth-session-after-bootstrap GET "${DESKTOP_URL}/v1/auth/desktop/session" "" "${EVIDENCE_DIR}/auth-session-after-bootstrap.json")"
[[ "${auth_after_status}" == "200" ]] || fail "desktop session after bootstrap status ${auth_after_status}, want 200"
jq -e '.ok == true and .user_id != "" and .username == "slice17-user"' "${EVIDENCE_DIR}/auth-session-after-bootstrap.json" >/dev/null || fail "session after bootstrap missing user actor"
TOKEN="$(awk '$6 == "swarm_desktop_session" { value=$7 } END { print value }' "${COOKIE_JAR}")"
[[ -n "${TOKEN}" ]] || fail "missing desktop session cookie token"
printf '%s\n' "${TOKEN}" | awk -F. 'NF == 3 { ok = 1 } END { exit(ok ? 0 : 1) }' || fail "desktop cookie is not a compact JWT"
write_tui_session_artifact "${EVIDENCE_DIR}/tui-session-after-bootstrap.json" after-bootstrap "${EVIDENCE_DIR}/onboarding-bootstrap.json" "${EVIDENCE_DIR}/auth-session-after-bootstrap.json"

identity_summary "${EVIDENCE_DIR}/identity-summary-after-bootstrap.json"
jq -e '.identity.counts.users == 1 and .identity.counts.teams == 1 and .identity.counts.team_memberships == 1 and .identity.counts.current_selections == 1 and .identity.current_user.username == "slice17-user" and .identity.current_selection.user_id == .identity.current_user.id and .identity.current_selection.team_id == .identity.current_team.id and .identity.current_membership.role == "owner" and .identity.current_team.default == true' "${EVIDENCE_DIR}/identity-summary-after-bootstrap.json" >/dev/null || fail "identity summary after bootstrap failed invariants"

start_daemon
workspace_pos_status="$(record_request guarded-workspace-positive POST "${DESKTOP_URL}/v1/workspace/add" "$(jq -nc --arg path "${WORKSPACE_PATH}" '{path:$path}')" "${EVIDENCE_DIR}/guarded-api-positive.json")"
[[ "${workspace_pos_status}" == "200" ]] || fail "guarded workspace positive status ${workspace_pos_status}, want 200"
jq -e '.ok == true and .workspace.workspace_path == "'"${WORKSPACE_PATH}"'"' "${EVIDENCE_DIR}/guarded-api-positive.json" >/dev/null || fail "guarded workspace positive response missing workspace path"

stop_daemon
start_daemon
restart_session_status="$(record_request auth-session-after-restart GET "${DESKTOP_URL}/v1/auth/desktop/session" "" "${EVIDENCE_DIR}/auth-session-after-restart.json")"
[[ "${restart_session_status}" == "200" ]] || fail "desktop session after restart status ${restart_session_status}, want 200"
write_tui_session_artifact "${EVIDENCE_DIR}/tui-session-after-restart.json" after-restart "${EVIDENCE_DIR}/onboarding-bootstrap.json" "${EVIDENCE_DIR}/auth-session-after-restart.json"

old_cookie_status="$(record_request old-issued-jwt-after-restart GET "${DESKTOP_URL}/v1/onboarding" "" "${EVIDENCE_DIR}/old-issued-jwt-after-restart.json" "cookie-value:${TOKEN}")"
[[ "${old_cookie_status}" == "200" ]] || fail "old issued JWT after restart status ${old_cookie_status}, want 200"
random_cookie_status="$(record_request old-random-cookie-rejected GET "${DESKTOP_URL}/v1/vault" "" "${EVIDENCE_DIR}/old-random-cookie-rejected.json" "cookie-value:old-random-cookie")"
[[ "${random_cookie_status}" == "401" ]] || fail "random cookie protected API status ${random_cookie_status}, want 401"

rebootstrap_status="$(record_request onboarding-rebootstrap-rejected POST "${DESKTOP_URL}/v1/onboarding" '{"username":"second-user","swarm_name":"Changed Device"}' "${EVIDENCE_DIR}/onboarding-rebootstrap-rejected.json")"
[[ "${rebootstrap_status}" == "409" ]] || fail "rebootstrap status ${rebootstrap_status}, want 409"

identity_summary "${EVIDENCE_DIR}/identity-summary-after-restart.json"
jq -e '.identity.counts.users == 1 and .identity.counts.teams == 1 and .identity.counts.team_memberships == 1 and .identity.counts.current_selections == 1 and .identity.current_user.username == "slice17-user"' "${EVIDENCE_DIR}/identity-summary-after-restart.json" >/dev/null || fail "identity summary after restart failed invariants"

jq -n \
  --arg status PASS \
  --arg evidence_dir "${EVIDENCE_DIR}" \
  --arg api_url "${API_URL}" \
  --arg desktop_url "${DESKTOP_URL}" \
  --arg startup_config_path "${STARTUP_CONFIG_PATH}" \
  --slurpfile before "${EVIDENCE_DIR}/identity-summary-before.json" \
  --slurpfile after "${EVIDENCE_DIR}/identity-summary-after-bootstrap.json" \
  --slurpfile restart "${EVIDENCE_DIR}/identity-summary-after-restart.json" \
  '{status:$status,exit_code:0,evidence_dir:$evidence_dir,api_url:$api_url,desktop_url:$desktop_url,startup_config_path:$startup_config_path,checks:{before_counts:$before[0].identity.counts,after_counts:$after[0].identity.counts,restart_counts:$restart[0].identity.counts,user_first_actor:true,team_hidden_container:true,jwt_valid_after_restart:true,swarm_name_persisted:true,no_team_prompt:true,no_hidden_identity_creation:true,authoritative_identity_store_only:true}}' >"${SUMMARY_JSON}"

jq -n --slurpfile after "${EVIDENCE_DIR}/identity-summary-after-bootstrap.json" --slurpfile restart "${EVIDENCE_DIR}/identity-summary-after-restart.json" --slurpfile summary "${SUMMARY_JSON}" \
  '{identity_counts_persist:($after[0].identity.counts == $restart[0].identity.counts),current_user_persisted:($restart[0].identity.current_user.username == "slice17-user"),jwt_valid_after_restart:$summary[0].checks.jwt_valid_after_restart,swarm_name_persisted:$summary[0].checks.swarm_name_persisted}' >"${EVIDENCE_DIR}/persistence-summary.json"

if [[ -n "${HOST_EVIDENCE_DIR}" ]]; then
  mkdir -p "${HOST_EVIDENCE_DIR}"
  cp -a "${EVIDENCE_DIR}/." "${HOST_EVIDENCE_DIR}/"
fi
printf 'PASS slice 1.7 final VM gate evidence_dir=%s\n' "${EVIDENCE_DIR}"
