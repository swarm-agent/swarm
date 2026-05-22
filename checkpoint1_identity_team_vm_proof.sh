#!/usr/bin/env bash
set -euo pipefail

EVIDENCE_DIR="${1:-.tmp/checkpoint-1/team-upgrade/$(date -u +%Y%m%dT%H%M%SZ)-vm-identity-team-proof}"
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
    '{status:$status,error:$error,evidence_dir:$evidence_dir}' >"${SUMMARY_JSON}" 2>/dev/null || true
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
require_command ss

RUN_ROOT="$(mktemp -d -t swarm-checkpoint1-team-live-XXXXXX)"
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
DATA_DIR="${RUN_ROOT}/system-data"
RUNTIME_DIR="${RUN_ROOT}/system-runtime"
CONFIG_DIR="${RUN_ROOT}/system-config"
CACHE_DIR="${RUN_ROOT}/system-cache"
HOME_DIR="${RUN_ROOT}/home"
DB_PROBE="${RUN_ROOT}/identity_db_probe.go"
mkdir -p "${DATA_DIR}" "${RUNTIME_DIR}" "${CONFIG_DIR}" "${CACHE_DIR}" "${RUN_ROOT}/system-logs" "${HOME_DIR}" "$(dirname "${DB_PATH}")"

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
STARTUP_CONFIG_TEXT="$(printf 'startup_mode = interactive\nhost = 127.0.0.1\nport = %s\ndesktop_port = %s\npeer_transport_port = %s\n' "${API_PORT}" "${DESKTOP_PORT}" "${PEER_PORT}")"

cat >"${DB_PROBE}" <<'GOEOF'
package main

import (
  "encoding/json"
  "fmt"
  "log"
  "os"
  "strings"

  "github.com/cockroachdb/pebble"
)

type entry struct {
  Key string `json:"key"`
  Value map[string]any `json:"value"`
}

type snapshot struct {
  Counts map[string]int `json:"counts"`
  Entries map[string][]entry `json:"entries"`
  Summary map[string]any `json:"summary"`
}

func main() {
  if len(os.Args) != 2 {
    log.Fatalf("usage: identity_db_probe <pebble-db-path>")
  }
  db, err := pebble.Open(os.Args[1], &pebble.Options{ReadOnly: true})
  if err != nil { log.Fatal(err) }
  defer db.Close()

  prefixes := map[string]string{
    "users": "identity/user/",
    "user_by_username": "identity/user_by_username/",
    "account_scopes": "identity/account_scope/",
    "teams": "identity/team/",
    "team_by_account_scope": "identity/team_by_account_scope/",
    "memberships": "identity/membership/",
    "current_selections": "identity/current_selection/",
    "events": "evt/",
  }
  snap := snapshot{Counts: map[string]int{}, Entries: map[string][]entry{}, Summary: map[string]any{}}
  for name, prefix := range prefixes {
    items, err := scan(db, prefix)
    if err != nil { log.Fatal(err) }
    snap.Entries[name] = items
    snap.Counts[name] = len(items)
  }
  if len(snap.Entries["users"]) == 1 {
    user := snap.Entries["users"][0].Value
    snap.Summary["user_id"] = stringValue(user["id"])
    snap.Summary["username"] = stringValue(user["username"])
    snap.Summary["user_account_scope_id"] = stringValue(user["account_scope_id"])
  }
  if len(snap.Entries["account_scopes"]) == 1 {
    acct := snap.Entries["account_scopes"][0].Value
    snap.Summary["account_scope_id"] = stringValue(acct["id"])
    snap.Summary["account_scope_user_id"] = stringValue(acct["user_id"])
    snap.Summary["account_scope_role"] = stringValue(acct["role"])
  }
  if len(snap.Entries["teams"]) == 1 {
    team := snap.Entries["teams"][0].Value
    snap.Summary["team_id"] = stringValue(team["id"])
    snap.Summary["team_account_scope_id"] = stringValue(team["account_scope_id"])
    snap.Summary["team_name"] = stringValue(team["name"])
    snap.Summary["team_default"] = team["default"]
  }
  if len(snap.Entries["memberships"]) == 1 {
    membership := snap.Entries["memberships"][0].Value
    snap.Summary["membership_team_id"] = stringValue(membership["team_id"])
    snap.Summary["membership_user_id"] = stringValue(membership["user_id"])
    snap.Summary["membership_role"] = stringValue(membership["role"])
  }
  if len(snap.Entries["current_selections"]) == 1 {
    selection := snap.Entries["current_selections"][0].Value
    snap.Summary["selection_user_id"] = stringValue(selection["user_id"])
    snap.Summary["selection_team_id"] = stringValue(selection["team_id"])
  }
  eventTypes := []string{}
  for _, event := range snap.Entries["events"] {
    if eventType := stringValue(event.Value["event_type"]); eventType != "" {
      eventTypes = append(eventTypes, eventType)
    }
  }
  snap.Summary["event_types"] = eventTypes
  out, err := json.MarshalIndent(snap, "", "  ")
  if err != nil { log.Fatal(err) }
  fmt.Println(string(out))
}

func scan(db *pebble.DB, prefix string) ([]entry, error) {
  iter, err := db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: prefixEnd(prefix)})
  if err != nil { return nil, err }
  defer iter.Close()
  out := []entry{}
  for iter.First(); iter.Valid(); iter.Next() {
    key := string(iter.Key())
    if prefix == "identity/team/" && strings.HasPrefix(key, "identity/team_by_account_scope/") {
      continue
    }
    valueBytes := append([]byte(nil), iter.Value()...)
    value := map[string]any{}
    if err := json.Unmarshal(valueBytes, &value); err != nil {
      value = map[string]any{"raw": string(valueBytes)}
    }
    out = append(out, entry{Key: key, Value: value})
  }
  if err := iter.Error(); err != nil { return nil, err }
  return out, nil
}

func prefixEnd(prefix string) []byte {
  b := []byte(prefix)
  for i := len(b)-1; i >= 0; i-- {
    if b[i] < 0xff { b[i]++; return b[:i+1] }
  }
  return nil
}

func stringValue(v any) string {
  if s, ok := v.(string); ok { return s }
  return ""
}
GOEOF

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
  for _ in $(seq 1 120); do
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
  log_cmd "stop swarmd for offline DB probe"
  curl -fsS -X POST -H 'Content-Type: application/json' --data '{"reason":"checkpoint1-identity-team-proof"}' "${API_URL}/v1/system/shutdown" >/dev/null 2>&1 || kill "${DAEMON_PID}" 2>/dev/null || true
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

redact_json_file() {
  local in="${1:-}" out="${2:-}"
  jq 'walk(if type == "string" and (test("^[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+$") or length > 120) then "<redacted>" else . end)' "${in}" >"${out}"
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
    '{name:$name,method:$method,url:$url,status:($status|tonumber),auth:"redacted",body:$body}' >>"${REQUESTS_LOG}"
  printf '%s' "${status}"
}

probe_db() {
  local out="${1:-}"
  log_cmd "offline DB probe -> ${out}"
  (cd swarmd && go run "${DB_PROBE}" "${DB_PATH}") >"${out}"
}

assert_private_db_state() {
  local file="${1:-}"
  jq -e '
    .counts.users == 1 and
    .counts.account_scopes == 1 and
    .counts.teams == 0 and
    .counts.team_by_account_scope == 0 and
    .counts.memberships == 0 and
    .counts.current_selections == 1 and
    (.summary.user_id | length > 0) and
    (.summary.account_scope_id | length > 0) and
    .summary.user_account_scope_id == .summary.account_scope_id and
    .summary.account_scope_user_id == .summary.user_id and
    .summary.account_scope_role == "owner" and
    .summary.selection_user_id == .summary.user_id and
    ((.summary.selection_team_id // "") == "")
  ' "${file}" >/dev/null || fail "private post-onboarding DB state is invalid"
}

assert_team_db_state() {
  local file="${1:-}"
  jq -e --arg team_name "Checkpoint 1 Real VM Team" '
    .counts.users == 1 and
    .counts.account_scopes == 1 and
    .counts.teams == 1 and
    .counts.team_by_account_scope == 1 and
    .counts.memberships == 1 and
    .counts.current_selections == 1 and
    (.summary.user_id | length > 0) and
    (.summary.account_scope_id | length > 0) and
    (.summary.team_id | length > 0) and
    .summary.user_account_scope_id == .summary.account_scope_id and
    .summary.team_account_scope_id == .summary.account_scope_id and
    .summary.team_name == $team_name and
    .summary.membership_user_id == .summary.user_id and
    .summary.membership_team_id == .summary.team_id and
    .summary.membership_role == "owner" and
    .summary.selection_user_id == .summary.user_id and
    .summary.selection_team_id == .summary.team_id and
    (.summary.event_types | index("identity.account.team_upgraded") != null)
  ' "${file}" >/dev/null || fail "team post-upgrade DB state is invalid"
}

log_cmd "cd swarmd && go build -o '${BIN}' ./cmd/swarmd"
(cd swarmd && go build -o "${BIN}" ./cmd/swarmd)

start_daemon
initial_status="$(record_request onboarding-initial GET "${DESKTOP_URL}/v1/onboarding" "" "${EVIDENCE_DIR}/onboarding-initial.json")"
[[ "${initial_status}" == "200" ]] || fail "initial onboarding status ${initial_status}, want 200"
jq -e '.identity.bootstrapped == false' "${EVIDENCE_DIR}/onboarding-initial.json" >/dev/null || fail "initial onboarding should not be bootstrapped"

bootstrap_status="$(record_request onboarding-bootstrap POST "${DESKTOP_URL}/v1/onboarding" '{"username":"checkpoint1-vm-user","swarm_name":"Checkpoint 1 VM Device"}' "${EVIDENCE_DIR}/onboarding-bootstrap.json")"
[[ "${bootstrap_status}" == "200" ]] || fail "onboarding bootstrap status ${bootstrap_status}, want 200"
jq -e '.identity.bootstrapped == true and .identity.user_id != "" and .identity.account_scope_id != "" and .identity.username == "checkpoint1-vm-user" and ((.identity.team_id // "") == "") and ((.identity.membership_role // "") == "")' "${EVIDENCE_DIR}/onboarding-bootstrap.json" >/dev/null || fail "bootstrap response violates private identity invariants"

stop_daemon
probe_db "${EVIDENCE_DIR}/db-after-bootstrap.json"
assert_private_db_state "${EVIDENCE_DIR}/db-after-bootstrap.json"

start_daemon
restart_status="$(record_request onboarding-after-restart GET "${DESKTOP_URL}/v1/onboarding" "" "${EVIDENCE_DIR}/onboarding-after-restart.json")"
[[ "${restart_status}" == "200" ]] || fail "onboarding after restart status ${restart_status}, want 200"
jq -e '.identity.bootstrapped == true and .identity.user_id != "" and .identity.account_scope_id != "" and ((.identity.team_id // "") == "")' "${EVIDENCE_DIR}/onboarding-after-restart.json" >/dev/null || fail "restart should preserve private identity and no team"

caller_team_status="$(record_request upgrade-with-caller-team-id POST "${DESKTOP_URL}/v1/account/team/upgrade" '{"team_name":"Bad Caller Team","team_id":"caller-supplied-team"}' "${EVIDENCE_DIR}/upgrade-with-caller-team-id.json")"
[[ "${caller_team_status}" == "400" ]] || fail "caller-supplied team_id status ${caller_team_status}, want 400"

upgrade_status="$(record_request upgrade-to-team POST "${DESKTOP_URL}/v1/account/team/upgrade" '{"team_name":"Checkpoint 1 Real VM Team"}' "${EVIDENCE_DIR}/upgrade-to-team.json")"
[[ "${upgrade_status}" == "200" ]] || fail "upgrade status ${upgrade_status}, want 200"
jq -e '.ok == true and .team.id != "" and .team.account_scope_id != "" and .team.display_name == "Checkpoint 1 Real VM Team" and .team.membership_role == "owner"' "${EVIDENCE_DIR}/upgrade-to-team.json" >/dev/null || fail "upgrade response missing team summary"

second_upgrade_status="$(record_request second-upgrade-rejected POST "${DESKTOP_URL}/v1/account/team/upgrade" '{"team_name":"Second Team"}' "${EVIDENCE_DIR}/second-upgrade-rejected.json")"
[[ "${second_upgrade_status}" == "409" ]] || fail "second upgrade status ${second_upgrade_status}, want 409"

stop_daemon
probe_db "${EVIDENCE_DIR}/db-after-team-upgrade.json"
assert_team_db_state "${EVIDENCE_DIR}/db-after-team-upgrade.json"

jq -n \
  --arg status PASS \
  --arg evidence_dir "${EVIDENCE_DIR}" \
  --arg api_url "${API_URL}" \
  --arg desktop_url "${DESKTOP_URL}" \
  --argjson initial_status "${initial_status}" \
  --argjson bootstrap_status "${bootstrap_status}" \
  --argjson restart_status "${restart_status}" \
  --argjson caller_team_status "${caller_team_status}" \
  --argjson upgrade_status "${upgrade_status}" \
  --argjson second_upgrade_status "${second_upgrade_status}" \
  --slurpfile private_db "${EVIDENCE_DIR}/db-after-bootstrap.json" \
  --slurpfile team_db "${EVIDENCE_DIR}/db-after-team-upgrade.json" \
  '{status:$status,evidence_dir:$evidence_dir,api_url:$api_url,desktop_url:$desktop_url,checks:{initial_onboarding_status:$initial_status,bootstrap_status:$bootstrap_status,restart_status:$restart_status,caller_supplied_team_id_status:$caller_team_status,upgrade_status:$upgrade_status,second_upgrade_status:$second_upgrade_status},private_db_summary:$private_db[0].summary,team_db_summary:$team_db[0].summary,auth_redacted:true}' >"${SUMMARY_JSON}"

if [[ -n "${HOST_EVIDENCE_DIR}" ]]; then
  mkdir -p "${HOST_EVIDENCE_DIR}"
  cp -a "${EVIDENCE_DIR}/." "${HOST_EVIDENCE_DIR}/"
fi
printf 'PASS checkpoint 1 real VM identity/team proof evidence_dir=%s\n' "${EVIDENCE_DIR}"
