#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"
cd "${ROOT_DIR}"

if [[ "${SWARM_HARNESS_VM_GUEST:-}" != "1" ]]; then
  echo "slice-1 sessions account-scope proof must run inside swarm-harness VM; dispatching through scripts/swarm-harness-vm.sh" >&2
  exec ./scripts/swarm-harness-vm.sh run -- env SWARM_HARNESS_VM_GUEST=1 SLICE1_FIREWORKS_API_KEY="${SLICE1_FIREWORKS_API_KEY:-}" ./scripts/vm/slice-1-sessions-account-scope.sh
fi

if [[ ! -f swarmd/go.mod ]]; then
  echo "must run from swarm-go repo root" >&2
  exit 1
fi
if [[ -f scripts/lib-go.sh ]]; then
  # shellcheck disable=SC1091
  source scripts/lib-go.sh
  swarm_require_go "${ROOT_DIR}"
fi
for cmd in go curl jq awk ss; do
  command -v "${cmd}" >/dev/null 2>&1 || { echo "required command not found in VM: ${cmd}" >&2; exit 1; }
done

EVIDENCE_DIR="${SLICE1_EVIDENCE_DIR:-.tmp/user-account-scope/slice-1-sessions/$(date -u +%Y%m%dT%H%M%SZ)-vm-proof}"
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
  local msg="${1:-slice-1 sessions account-scope VM proof failed}"
  jq -nc --arg status FAIL --arg error "${msg}" --arg evidence_dir "${EVIDENCE_DIR}" '{status:$status,error:$error,evidence_dir:$evidence_dir,exit_code:1}' >"${SUMMARY_JSON}" 2>/dev/null || true
  printf 'error: %s\n' "${msg}" >&2
  exit 1
}

RUN_ROOT="$(mktemp -d -t swarm-slice1-sessions-XXXXXX)"
DAEMON_PID=""
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
SWARM_DATA_DIR="${RUN_ROOT}/system-data/swarm"
RUNTIME_DIR="${RUN_ROOT}/system-runtime"
CONFIG_DIR="${RUN_ROOT}/system-config"
CACHE_DIR="${RUN_ROOT}/system-cache"
HOME_DIR="${RUN_ROOT}/home"
STARTUP_CONFIG_PATH="${CONFIG_DIR}/swarm/swarm.conf"
mkdir -p "${SWARM_DATA_DIR}" "${RUNTIME_DIR}" "${CONFIG_DIR}" "${CACHE_DIR}" "${RUN_ROOT}/system-logs" "${HOME_DIR}" "$(dirname -- "${DB_PATH}")" "$(dirname -- "${STARTUP_CONFIG_PATH}")"

choose_base_port() { awk 'BEGIN{srand(); print int(22000 + rand() * 20000)}'; }
API_PORT="${SLICE1_API_PORT:-$(choose_base_port)}"
DESKTOP_PORT="${SLICE1_DESKTOP_PORT:-$((API_PORT + 1))}"
for _ in $(seq 1 80); do
  PEER_PORT="$((API_PORT + 2))"
  if ! ss -H -ltn "sport = :${API_PORT}" 2>/dev/null | grep -q . && ! ss -H -ltn "sport = :${DESKTOP_PORT}" 2>/dev/null | grep -q . && ! ss -H -ltn "sport = :${PEER_PORT}" 2>/dev/null | grep -q .; then
    break
  fi
  API_PORT="$((API_PORT + 3))"
  DESKTOP_PORT="$((API_PORT + 1))"
done
PEER_PORT="$((API_PORT + 2))"
APP_URL="http://127.0.0.1:${API_PORT}"
DESKTOP_URL="http://127.0.0.1:${DESKTOP_PORT}"
COOKIE_A="${RUN_ROOT}/account-a.cookies"
COOKIE_B="${RUN_ROOT}/account-b.cookies"
export SWARM_DATA_DIR APP_URL DB_PATH

STARTUP_CONFIG_TEXT="$(printf 'startup_mode = interactive\nswarm_name = Slice1 Sessions Device\nswarm_mode = true\nhost = 127.0.0.1\nport = %s\ndesktop_port = %s\npeer_transport_port = %s\n' "${API_PORT}" "${DESKTOP_PORT}" "${PEER_PORT}")"
printf '%s\n' "${STARTUP_CONFIG_TEXT}" >"${STARTUP_CONFIG_PATH}"

sanitize_request_body() {
  local tmpbody="${1:-}" out="${2:-}"
  if jq . "${tmpbody}" >/dev/null 2>&1; then
    jq 'walk(if type == "string" and (test("^[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+$") or startswith("fw_") or length > 160) then "<redacted>" else . end)' "${tmpbody}" >"${out}"
  else
    sed -E 's/[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/<redacted>/g; s/fw_[A-Za-z0-9]+/<redacted>/g' "${tmpbody}" >"${out}"
  fi
}

record_request() {
  local name="${1:-}" method="${2:-}" url="${3:-}" body="${4:-}" out="${5:-}" auth="${6:-cookie-a}"
  local status headers tmpbody curl_args=()
  headers="${RUN_ROOT}/${name}.headers"
  tmpbody="${RUN_ROOT}/${name}.body"
  curl_args=(-sS -o "${tmpbody}" -D "${headers}" -w '%{http_code}' -X "${method}" -H "Origin: ${DESKTOP_URL}" -H "Referer: ${DESKTOP_URL}/" -H 'Sec-Fetch-Site: same-origin')
  case "${auth}" in
    cookie-a) curl_args+=(-c "${COOKIE_A}" -b "${COOKIE_A}") ;;
    cookie-b) curl_args+=(-c "${COOKIE_B}" -b "${COOKIE_B}") ;;
    token:*) curl_args+=(-H "X-Swarm-Token: ${auth#token:}") ;;
    bearer:*) curl_args+=(-H "Authorization: Bearer ${auth#bearer:}") ;;
    none) ;;
    *) fail "unknown request auth mode ${auth}" ;;
  esac
  if [[ -n "${body}" ]]; then
    curl_args+=(-H 'Content-Type: application/json' --data "${body}")
  fi
  curl_args+=("${url}")
  status="$(curl "${curl_args[@]}")"
  sanitize_request_body "${tmpbody}" "${out}"
  jq -nc --arg name "${name}" --arg method "${method}" --arg url "${url}" --arg status "${status}" --rawfile body "${out}" '{name:$name,method:$method,url:$url,status:($status|tonumber),body:$body}' >>"${REQUESTS_LOG}"
  printf '%s' "${status}"
}

start_daemon() {
  : >"${DAEMON_LOG}.current"
  log_cmd "start swarmd in VM --listen 127.0.0.1:${API_PORT} --desktop-port ${DESKTOP_PORT} --db-path <tmp>"
  HOME="${HOME_DIR}" \
  XDG_DATA_HOME="${RUN_ROOT}/xdg-data" \
  XDG_RUNTIME_DIR="${RUN_ROOT}/xdg-runtime" \
  XDG_CONFIG_HOME="${RUN_ROOT}/xdg-config" \
  XDG_CACHE_HOME="${RUN_ROOT}/xdg-cache" \
  STATE_DIRECTORY="${RUN_ROOT}/system-data" \
  RUNTIME_DIRECTORY="${RUNTIME_DIR}" \
  CONFIGURATION_DIRECTORY="${CONFIG_DIR}" \
  CACHE_DIRECTORY="${CACHE_DIR}" \
  LOGS_DIRECTORY="${RUN_ROOT}/system-logs" \
  SWARM_CHILD_STARTUP_CONFIG="${STARTUP_CONFIG_TEXT}" \
    "${BIN}" --listen "127.0.0.1:${API_PORT}" --desktop-port "${DESKTOP_PORT}" --data-dir "${SWARM_DATA_DIR}" --db-path "${DB_PATH}" --lock-path "${RUNTIME_DIR}/swarmd.lock" \
    >"${DAEMON_LOG}.current" 2>&1 &
  DAEMON_PID="$!"
  for _ in $(seq 1 240); do
    if curl -fsS "${APP_URL}/readyz" >/dev/null 2>&1; then
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
  curl -fsS -X POST -H 'Content-Type: application/json' --data '{"reason":"slice-1-sessions-account-scope"}' "${APP_URL}/v1/system/shutdown" >/dev/null 2>&1 || kill "${DAEMON_PID}" 2>/dev/null || true
  for _ in $(seq 1 120); do
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

copy_db_for_inspect() {
  local tmpdir="${RUN_ROOT}/inspect-copy"
  rm -rf -- "${tmpdir}"
  mkdir -p "${tmpdir}"
  cp -a "${DB_PATH}" "${tmpdir}/pebble"
  printf '%s\n' "${tmpdir}/pebble"
}

create_account_probe() {
  local probe_dir="swarmd/.cache/slice1/accountprobe"
  mkdir -p "${probe_dir}"
  cat >"${probe_dir}/main.go" <<'GOEOF'
package main

import (
  "flag"
  "fmt"
  "os"
  "strings"

  "swarm/packages/swarmd/internal/identity"
  pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func main() {
  dbPath := flag.String("db", "", "path to swarmd pebble DB")
  username := flag.String("username", "slice1-account-b", "username")
  flag.Parse()
  if strings.TrimSpace(*dbPath) == "" {
    fmt.Fprintln(os.Stderr, "--db is required")
    os.Exit(2)
  }
  store, err := pebblestore.Open(*dbPath)
  if err != nil { panic(err) }
  defer store.Close()
  identities := pebblestore.NewIdentityStore(store)
  userID := "user_slice1_b"
  accountScopeID := "acct_slice1_b"
  if _, err := identities.CreateUserIfAbsent(pebblestore.UserRecord{ID: userID, Username: *username, DisplayName: *username, AccountScopeID: accountScopeID, AuthProvider: identity.LocalProductSessionIssuer, AuthSubject: userID}); err != nil { panic(err) }
  if _, err := identities.CreateAccountScopeIfAbsent(pebblestore.AccountScopeRecord{ID: accountScopeID, Type: pebblestore.AccountScopeTypePersonal, CreatedByUserID: userID, UserID: userID, Role: pebblestore.AccountRoleOwner}); err != nil { panic(err) }
  if _, err := identities.CreateAccountUserIfAbsent(pebblestore.AccountUserRecord{ID: accountScopeID + ":" + userID, AccountScopeID: accountScopeID, UserID: userID, Status: pebblestore.AccountUserStatusActive}); err != nil { panic(err) }
  if _, err := identities.PutCurrentSelection(pebblestore.CurrentSelectionRecord{UserID: userID}); err != nil { panic(err) }
  fmt.Printf("{\"user_id\":%q,\"account_scope_id\":%q}\n", userID, accountScopeID)
}
GOEOF
}

create_db_probe() {
  local probe_dir="swarmd/.cache/slice1/sessiondbprobe"
  mkdir -p "${probe_dir}"
  cat >"${probe_dir}/main.go" <<'GOEOF'
package main

import (
  "encoding/json"
  "flag"
  "fmt"
  "os"
  "strings"

  pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

type output struct {
  SessionID string `json:"session_id"`
  UserID string `json:"user_id"`
  AccountScopeID string `json:"account_scope_id"`
  AccountSessionKey string `json:"account_session_key"`
  AccountSessionIndexed bool `json:"account_session_indexed"`
  LegacySessionPresent bool `json:"legacy_session_present"`
  MessageCount int `json:"message_count"`
  MessageAccountScopes []string `json:"message_account_scopes"`
  PlanCount int `json:"plan_count"`
  PlanAccountScopes []string `json:"plan_account_scopes"`
  UsageSummaryAccountScopeID string `json:"usage_summary_account_scope_id"`
  UsageSummaryAccountIndexed bool `json:"usage_summary_account_indexed"`
}

func main() {
  dbPath := flag.String("db", "", "path to swarmd pebble DB")
  sessionID := flag.String("session", "", "session id")
  flag.Parse()
  if strings.TrimSpace(*dbPath) == "" || strings.TrimSpace(*sessionID) == "" {
    fmt.Fprintln(os.Stderr, "--db and --session are required")
    os.Exit(2)
  }
  store, err := pebblestore.Open(*dbPath)
  if err != nil { panic(err) }
  defer store.Close()
  sessions := pebblestore.NewSessionStore(store)
  session, ok, err := sessions.GetSession(*sessionID)
  if err != nil { panic(err) }
  if !ok { panic("session not found") }
  messages, err := sessions.ListMessages(session.ID, 0, 200)
  if err != nil { panic(err) }
  plans, err := sessions.ListPlans(session.ID, 200)
  if err != nil { panic(err) }
  summary, hasSummary, err := sessions.GetUsageSummary(session.ID)
  if err != nil { panic(err) }
  var indexedID string
  accountSessionIndexed := false
  if payload, ok, err := store.GetBytes(pebblestore.KeySessionByAccount(session.AccountScopeID, session.ID)); err != nil { panic(err) } else if ok {
    indexedID = string(payload)
    accountSessionIndexed = indexedID == session.ID
  }
  usageIndexed := false
  if session.AccountScopeID != "" {
    _, usageIndexed, err = store.GetBytes(pebblestore.KeySessionUsageSummaryByAccount(session.AccountScopeID, session.ID))
    if err != nil { panic(err) }
  }
  _, legacyPresent, err := store.GetBytes(pebblestore.KeySession(session.ID))
  if err != nil { panic(err) }
  out := output{SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, AccountSessionKey: pebblestore.KeySessionByAccount(session.AccountScopeID, session.ID), AccountSessionIndexed: accountSessionIndexed, LegacySessionPresent: legacyPresent, MessageCount: len(messages), PlanCount: len(plans)}
  for _, msg := range messages { out.MessageAccountScopes = append(out.MessageAccountScopes, msg.AccountScopeID) }
  for _, plan := range plans { out.PlanAccountScopes = append(out.PlanAccountScopes, plan.AccountScopeID) }
  if hasSummary { out.UsageSummaryAccountScopeID = summary.AccountScopeID; out.UsageSummaryAccountIndexed = usageIndexed }
  enc := json.NewEncoder(os.Stdout)
  enc.SetIndent("", "  ")
  if err := enc.Encode(out); err != nil { panic(err) }
}
GOEOF
}

log_cmd "hostname && date -Is"
{ hostname; date -Is; } | tee "${EVIDENCE_DIR}/vm-host.txt" >/dev/null
log_cmd "git status --short"
if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  git status --short | tee "${EVIDENCE_DIR}/git-status.txt" >/dev/null
  log_cmd "git rev-parse HEAD"
  git rev-parse HEAD | tee "${EVIDENCE_DIR}/git-head.txt" >/dev/null
else
  printf 'synced VM checkout has no .git directory (rsync excludes .git)\n' | tee "${EVIDENCE_DIR}/git-status.txt" >/dev/null
  log_cmd "git rev-parse HEAD skipped: VM checkout has no .git directory"
  printf 'unavailable: rsync checkout excludes .git\n' >"${EVIDENCE_DIR}/git-head.txt"
fi

log_cmd "cd swarmd && go test ./internal/store/pebble -run 'TestListTopSessionsByWorkspace' -count=1"
(cd swarmd && go test ./internal/store/pebble -run 'TestListTopSessionsByWorkspace' -count=1)

log_cmd "cd swarmd && go build -o <tmp>/swarmd ./cmd/swarmd"
(cd swarmd && go build -o "${BIN}" ./cmd/swarmd)

create_account_probe
create_db_probe
start_daemon

bootstrap_a_status="$(record_request onboarding-bootstrap-a POST "${DESKTOP_URL}/v1/onboarding" '{"username":"slice1-account-a","swarm_name":"Slice1 Sessions Device"}' "${EVIDENCE_DIR}/onboarding-bootstrap-a.json" cookie-a)"
[[ "${bootstrap_a_status}" == "200" ]] || fail "account A onboarding status ${bootstrap_a_status}, want 200"
SESSION_A_STATUS="$(record_request auth-session-a GET "${DESKTOP_URL}/v1/auth/desktop/session" "" "${EVIDENCE_DIR}/auth-session-a.json" cookie-a)"
[[ "${SESSION_A_STATUS}" == "200" ]] || fail "account A desktop session status ${SESSION_A_STATUS}, want 200"
TOKEN_A="$(jq -r '.token // empty' "${RUN_ROOT}/auth-session-a.body")"
[[ -n "${TOKEN_A}" ]] || fail "account A desktop session response did not include token"
ACCOUNT_A="$(jq -r '.identity.account_scope_id // empty' "${EVIDENCE_DIR}/onboarding-bootstrap-a.json")"
USER_A="$(jq -r '.identity.user_id // empty' "${EVIDENCE_DIR}/onboarding-bootstrap-a.json")"
[[ -n "${ACCOUNT_A}" && -n "${USER_A}" ]] || fail "account A identity missing user/account scope"

if [[ -n "${SLICE1_FIREWORKS_API_KEY:-}" ]]; then
  fireworks_payload="$(jq -nc --arg api_key "${SLICE1_FIREWORKS_API_KEY}" '{provider:"fireworks",type:"api",label:"slice-1 VM Fireworks proof",api_key:$api_key,active:true}')"
  credential_status="$(record_request fireworks-credential-a POST "${APP_URL}/v1/auth/credentials" "${fireworks_payload}" "${EVIDENCE_DIR}/fireworks-credential-a.json" "token:${TOKEN_A}")"
  [[ "${credential_status}" == "200" ]] || fail "Fireworks credential create status ${credential_status}, want 200"
  jq -e '.provider == "fireworks" and .active == true and (.connection.connected == true)' "${EVIDENCE_DIR}/fireworks-credential-a.json" >/dev/null || fail "Fireworks credential did not verify as connected"
else
  printf '{"skipped":true,"reason":"SLICE1_FIREWORKS_API_KEY not set"}\n' >"${EVIDENCE_DIR}/fireworks-credential-a.json"
fi

preference_payload='{"provider":"fireworks","model":"accounts/fireworks/models/minimax-m2p7","thinking":"high"}'
model_pref_status="$(record_request model-preference-a POST "${APP_URL}/v1/model" "${preference_payload}" "${EVIDENCE_DIR}/model-preference-a.json" "token:${TOKEN_A}")"
[[ "${model_pref_status}" == "200" ]] || fail "model preference status ${model_pref_status}, want 200"

WORKSPACE_A_DIR="${RUN_ROOT}/slice1-account-a-workspace"
mkdir -p "${WORKSPACE_A_DIR}"
workspace_payload="$(jq -nc --arg path "${WORKSPACE_A_DIR}" '{path:$path,name:"slice1-account-a-workspace",make_current:true}')"
workspace_a_status="$(record_request workspace-add-a POST "${APP_URL}/v1/workspace/add" "${workspace_payload}" "${EVIDENCE_DIR}/workspace-add-a.json" "token:${TOKEN_A}")"
[[ "${workspace_a_status}" == "200" ]] || fail "account A workspace add status ${workspace_a_status}, want 200"
session_payload="$(jq -nc --arg path "${WORKSPACE_A_DIR}" '{title:"Slice 1 account A proof",mode:"auto",workspace_path:$path,workspace_name:"slice1-account-a-workspace",preference:{provider:"fireworks",model:"accounts/fireworks/models/minimax-m2p7",thinking:"high"}}')"
create_session_status="$(record_request session-create-a POST "${APP_URL}/v1/sessions" "${session_payload}" "${EVIDENCE_DIR}/session-create-a.json" "token:${TOKEN_A}")"
[[ "${create_session_status}" == "200" ]] || fail "account A session create status ${create_session_status}, want 200"
SESSION_ID="$(jq -r '.session.id // empty' "${EVIDENCE_DIR}/session-create-a.json")"
[[ -n "${SESSION_ID}" ]] || fail "account A session create did not return session id"
jq -e --arg account "${ACCOUNT_A}" '.session.account_scope_id == $account' "${EVIDENCE_DIR}/session-create-a.json" >/dev/null || fail "account A created session missing AccountScopeID"

message_status="$(record_request session-message-a POST "${APP_URL}/v1/sessions/${SESSION_ID}/messages" '{"role":"user","content":"account A visible message"}' "${EVIDENCE_DIR}/session-message-a.json" "token:${TOKEN_A}")"
[[ "${message_status}" == "200" ]] || fail "account A message append status ${message_status}, want 200"
plan_status="$(record_request session-plan-a POST "${APP_URL}/v1/sessions/${SESSION_ID}/plans" '{"title":"Account A Plan","plan":"# Account A Plan\n1. prove isolation","status":"draft","approval_state":"draft"}' "${EVIDENCE_DIR}/session-plan-a.json" "token:${TOKEN_A}")"
[[ "${plan_status}" == "200" ]] || fail "account A plan save status ${plan_status}, want 200"
run_status="$(record_request session-run-a POST "${APP_URL}/v1/sessions/${SESSION_ID}/run" '{"prompt":"Reply with exactly: slice1-account-a-pass","instructions":"No tools. Keep the answer to exactly the requested phrase.","tool_scope":{"deny_tools":["bash","read","write","edit","search","websearch","webfetch","task"]}}' "${EVIDENCE_DIR}/session-run-a.json" "token:${TOKEN_A}")"
if [[ -n "${SLICE1_FIREWORKS_API_KEY:-}" ]]; then
  [[ "${run_status}" == "200" ]] || fail "account A real model run status ${run_status}, want 200"
  jq -e '.ok == true and (.result.assistant_message.content // "" | length > 0)' "${EVIDENCE_DIR}/session-run-a.json" >/dev/null || fail "account A real model run did not return assistant content"
else
  [[ "${run_status}" == "400" || "${run_status}" == "500" ]] || fail "account A no-key run status ${run_status}, want expected auth failure without API key"
fi
usage_a_status="$(record_request session-usage-a GET "${APP_URL}/v1/sessions/${SESSION_ID}/usage" "" "${EVIDENCE_DIR}/session-usage-a.json" "token:${TOKEN_A}")"
[[ "${usage_a_status}" == "200" ]] || fail "account A usage status ${usage_a_status}, want 200"

stop_daemon

log_cmd "cd swarmd && go run ./.cache/slice1/accountprobe --db <db> --username slice1-account-b"
(cd swarmd && go run ./.cache/slice1/accountprobe --db "${DB_PATH}" --username slice1-account-b) >"${EVIDENCE_DIR}/account-b-created.json"
start_daemon

SESSION_B_STATUS="$(record_request auth-session-b GET "${DESKTOP_URL}/v1/auth/desktop/session" "" "${EVIDENCE_DIR}/auth-session-b.json" cookie-b)"
[[ "${SESSION_B_STATUS}" == "200" ]] || fail "account B desktop session status ${SESSION_B_STATUS}, want 200"
TOKEN_B="$(jq -r '.token // empty' "${RUN_ROOT}/auth-session-b.body")"
[[ -n "${TOKEN_B}" ]] || fail "account B desktop session response did not include token"
me_b_status="$(record_request me-b GET "${APP_URL}/me" "" "${EVIDENCE_DIR}/me-b.json" "token:${TOKEN_B}")"
[[ "${me_b_status}" == "200" ]] || fail "account B /me status ${me_b_status}, want 200"
ACCOUNT_B="$(jq -r '.accountScopeID // empty' "${EVIDENCE_DIR}/me-b.json")"
[[ "${ACCOUNT_B}" == "acct_slice1_b" ]] || fail "account B principal account_scope_id ${ACCOUNT_B}, want acct_slice1_b"
[[ "${ACCOUNT_B}" != "${ACCOUNT_A}" ]] || fail "account A and B account scopes unexpectedly match"

list_b_status="$(record_request sessions-list-b GET "${APP_URL}/v1/sessions?limit=20" "" "${EVIDENCE_DIR}/sessions-list-b.json" "token:${TOKEN_B}")"
[[ "${list_b_status}" == "200" ]] || fail "account B sessions list status ${list_b_status}, want 200"
jq -e --arg session_id "${SESSION_ID}" '[.sessions[]? | select(.id == $session_id)] | length == 0' "${EVIDENCE_DIR}/sessions-list-b.json" >/dev/null || fail "account B list exposed account A session"

for check in \
  "session-read-b|GET|/v1/sessions/${SESSION_ID}|" \
  "session-messages-b|GET|/v1/sessions/${SESSION_ID}/messages|" \
  "session-metadata-b|POST|/v1/sessions/${SESSION_ID}/metadata|{\"metadata\":{\"b\":true}}" \
  "session-preference-b|POST|/v1/sessions/${SESSION_ID}/preference|{\"thinking\":\"high\"}" \
  "session-plans-b|GET|/v1/sessions/${SESSION_ID}/plans|" \
  "session-plan-active-b|GET|/v1/sessions/${SESSION_ID}/plans/active|" \
  "session-usage-b|GET|/v1/sessions/${SESSION_ID}/usage|" \
  "session-run-b|POST|/v1/sessions/${SESSION_ID}/run|{\"prompt\":\"should be rejected\"}"; do
  IFS='|' read -r name method path body <<<"${check}"
  status="$(record_request "${name}" "${method}" "${APP_URL}${path}" "${body}" "${EVIDENCE_DIR}/${name}.json" "token:${TOKEN_B}")"
  [[ "${status}" == "404" ]] || fail "account B ${name} status ${status}, want 404"
done

read_a_status="$(record_request session-read-a-after-b GET "${APP_URL}/v1/sessions/${SESSION_ID}" "" "${EVIDENCE_DIR}/session-read-a-after-b.json" "token:${TOKEN_A}")"
[[ "${read_a_status}" == "200" ]] || fail "account A session read after B checks status ${read_a_status}, want 200"

INSPECT_DB_PATH="$(copy_db_for_inspect)"
log_cmd "cd swarmd && go run ./.cache/slice1/sessiondbprobe --db <db-copy> --session ${SESSION_ID}"
(cd swarmd && go run ./.cache/slice1/sessiondbprobe --db "${INSPECT_DB_PATH}" --session "${SESSION_ID}") >"${EVIDENCE_DIR}/session-db-proof.json"
jq -e --arg account "${ACCOUNT_A}" --arg user "${USER_A}" '
  .user_id == $user and
  .account_scope_id == $account and
  .account_session_indexed == true and
  .legacy_session_present == true and
  .message_count >= 1 and
  ([.message_account_scopes[]? | select(. != $account)] | length) == 0 and
  .plan_count >= 1 and
  ([.plan_account_scopes[]? | select(. != $account)] | length) == 0 and
  ((.usage_summary_account_scope_id == "") or (.usage_summary_account_scope_id == $account))
' "${EVIDENCE_DIR}/session-db-proof.json" >/dev/null || fail "session DB account-scope invariants failed"

stop_daemon

jq -n \
  --arg status PASS \
  --arg evidence_dir "${EVIDENCE_DIR}" \
  --arg app_url "${APP_URL}" \
  --arg desktop_url "${DESKTOP_URL}" \
  --arg session_id "${SESSION_ID}" \
  --arg account_a "${ACCOUNT_A}" \
  --arg account_b "${ACCOUNT_B}" \
  --arg fireworks_key_used "$([[ -n "${SLICE1_FIREWORKS_API_KEY:-}" ]] && printf true || printf false)" \
  --slurpfile db "${EVIDENCE_DIR}/session-db-proof.json" \
  --slurpfile list_b "${EVIDENCE_DIR}/sessions-list-b.json" \
  --slurpfile run_a "${EVIDENCE_DIR}/session-run-a.json" \
  '{status:$status,exit_code:0,evidence_dir:$evidence_dir,app_url:$app_url,desktop_url:$desktop_url,session_id:$session_id,account_a:$account_a,account_b:$account_b,fireworks_key_used:($fireworks_key_used == "true"),checks:{account_b_list_excludes_account_a_session:(([ $list_b[0].sessions[]? | select(.id == $session_id) ] | length) == 0),account_b_subresources_rejected_404:true,session_db_proof:$db[0],real_model_run_status:($run_a[0].ok // false)}}' >"${SUMMARY_JSON}"

printf 'PASS slice-1 sessions account-scope VM proof evidence_dir=%s\n' "${EVIDENCE_DIR}"
