#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"
cd "${ROOT_DIR}"

if [[ "${SWARM_HARNESS_VM_GUEST:-}" != "1" ]]; then
  if [[ "${CHECKPOINT1_ALLOW_HOST_RUN:-}" == "1" ]]; then
    echo "warning: CHECKPOINT1_ALLOW_HOST_RUN=1 set; running on host. VM proof is NOT satisfied." >&2
  else
    echo "checkpoint-1 VM proof must run inside swarm-harness VM; dispatching through scripts/swarm-harness-vm.sh" >&2
    exec ./scripts/swarm-harness-vm.sh run -- env SWARM_HARNESS_VM_GUEST=1 CHECKPOINT1_RESET=1 CHECKPOINT1_BOOTSTRAP_VIA_API=1 ./scripts/vm/checkpoint-1-user-account-foundation.sh
  fi
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

EVIDENCE_DIR="${CHECKPOINT1_EVIDENCE_DIR:-.tmp/checkpoint-1/user-account-foundation/$(date -u +%Y%m%dT%H%M%SZ)-vm-proof}"
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
  local msg="${1:-checkpoint-1 VM proof failed}"
  jq -nc --arg status FAIL --arg error "${msg}" --arg evidence_dir "${EVIDENCE_DIR}" '{status:$status,error:$error,evidence_dir:$evidence_dir}' >"${SUMMARY_JSON}" 2>/dev/null || true
  printf 'error: %s\n' "${msg}" >&2
  exit 1
}

RUN_ROOT="$(mktemp -d -t swarm-checkpoint1-user-account-XXXXXX)"
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

choose_base_port() { awk 'BEGIN{srand(); print int(20000 + rand() * 20000)}'; }
API_PORT="${CHECKPOINT1_API_PORT:-$(choose_base_port)}"
DESKTOP_PORT="${CHECKPOINT1_DESKTOP_PORT:-$((API_PORT + 1))}"
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
COOKIE_JAR="${RUN_ROOT}/cookies.txt"
LOCAL_TRANSPORT_SOCKET="${SWARM_DATA_DIR}/local-transport/api.sock"
export SWARM_DATA_DIR APP_URL DB_PATH

STARTUP_CONFIG_TEXT="$(printf 'swarm_name = Checkpoint1 Device\nhost = 127.0.0.1\nport = %s\ndesktop_port = %s\npeer_transport_port = %s\n' "${API_PORT}" "${DESKTOP_PORT}" "${PEER_PORT}")"
printf '%s\n' "${STARTUP_CONFIG_TEXT}" >"${STARTUP_CONFIG_PATH}"

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
  elif [[ "${auth}" == token:* ]]; then
    curl_args+=(-H "X-Swarm-Token: ${auth#token:}")
  fi
  if [[ -n "${body}" ]]; then
    curl_args+=(-H 'Content-Type: application/json' --data "${body}")
  fi
  curl_args+=("${url}")
  status="$(curl "${curl_args[@]}")"
  sanitize_request_body "${tmpbody}" "${out}"
  jq -nc --arg name "${name}" --arg method "${method}" --arg url "${url}" --arg status "${status}" --rawfile body "${out}" '{name:$name,method:$method,url:$url,status:($status|tonumber),body:$body}' >>"${REQUESTS_LOG}"
  printf '%s' "${status}"
}

sanitize_request_body() {
  local tmpbody="${1:-}" out="${2:-}"
  if jq . "${tmpbody}" >/dev/null 2>&1; then
    jq 'walk(if type == "string" and (test("^[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+$") or length > 120) then "<redacted>" else . end)' "${tmpbody}" >"${out}"
  else
    sed -E 's/[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/<redacted>/g' "${tmpbody}" >"${out}"
  fi
}

record_local_transport_request() {
  local name="${1:-}" method="${2:-}" path="${3:-}" body="${4:-}" out="${5:-}" auth="${6:-none}"
  local status headers tmpbody curl_args=()
  headers="${RUN_ROOT}/${name}.headers"
  tmpbody="${RUN_ROOT}/${name}.body"
  curl_args=(-sS --unix-socket "${LOCAL_TRANSPORT_SOCKET}" -o "${tmpbody}" -D "${headers}" -w '%{http_code}' -X "${method}")
  if [[ "${auth}" == bearer:* ]]; then
    curl_args+=(-H "Authorization: Bearer ${auth#bearer:}")
  elif [[ "${auth}" == token:* ]]; then
    curl_args+=(-H "X-Swarm-Token: ${auth#token:}")
  fi
  if [[ -n "${body}" ]]; then
    curl_args+=(-H 'Content-Type: application/json' --data "${body}")
  fi
  curl_args+=("http://swarm-local-transport${path}")
  status="$(curl "${curl_args[@]}")"
  sanitize_request_body "${tmpbody}" "${out}"
  jq -nc --arg name "${name}" --arg method "${method}" --arg url "local-transport:${path}" --arg status "${status}" --rawfile body "${out}" '{name:$name,method:$method,url:$url,status:($status|tonumber),body:$body}' >>"${REQUESTS_LOG}"
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
  for _ in $(seq 1 180); do
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
  curl -fsS -X POST -H 'Content-Type: application/json' --data '{"reason":"checkpoint-1-user-account-foundation"}' "${APP_URL}/v1/system/shutdown" >/dev/null 2>&1 || kill "${DAEMON_PID}" 2>/dev/null || true
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

if [[ "${CHECKPOINT1_SKIP_INSTALL:-}" != "1" ]]; then
  log_cmd "skip make install: no repository Makefile; building swarmd directly inside VM"
fi

if [[ "${CHECKPOINT1_RESET:-1}" == "1" ]]; then
  log_cmd "reset VM proof data root ${RUN_ROOT}"
  rm -rf "${SWARM_DATA_DIR}" "${DB_PATH}"
  mkdir -p "${SWARM_DATA_DIR}" "$(dirname -- "${DB_PATH}")"
fi

log_cmd "cd swarmd && go test ./internal/store/pebble ./internal/identity ./internal/api -run 'Test(DesktopSession|LocalTransportSession|ProtectedCreateAPIs|Session|Onboarding|.*Identity|.*Principal)'"
(cd swarmd && go test ./internal/store/pebble ./internal/identity ./internal/api -run 'Test(DesktopSession|LocalTransportSession|ProtectedCreateAPIs|Session|Onboarding|.*Identity|.*Principal)')

log_cmd "cd swarmd && go build -o <tmp>/swarmd ./cmd/swarmd"
(cd swarmd && go build -o "${BIN}" ./cmd/swarmd)

start_daemon

before_status="$(record_request auth-session-before-bootstrap GET "${DESKTOP_URL}/v1/auth/desktop/session" "" "${EVIDENCE_DIR}/auth-session-before-bootstrap.json")"
[[ "${before_status}" == "401" ]] || fail "desktop session before bootstrap status ${before_status}, want 401"

bootstrap_status="$(record_request onboarding-bootstrap POST "${DESKTOP_URL}/v1/onboarding" '{"username":"checkpoint1","swarm_name":"Checkpoint1 Device"}' "${EVIDENCE_DIR}/onboarding-bootstrap.json")"
[[ "${bootstrap_status}" == "200" ]] || fail "onboarding bootstrap status ${bootstrap_status}, want 200"
jq -e '.identity.bootstrapped == true and .identity.user_id != "" and .identity.account_scope_id != "" and (.identity.team_id // "") == ""' "${EVIDENCE_DIR}/onboarding-bootstrap.json" >/dev/null || fail "bootstrap response missing canonical user/account identity or includes team_id"

session_status="$(record_request auth-session-after-bootstrap GET "${DESKTOP_URL}/v1/auth/desktop/session" "" "${EVIDENCE_DIR}/auth-session-after-bootstrap.json")"
[[ "${session_status}" == "200" ]] || fail "desktop session after bootstrap status ${session_status}, want 200"
TOKEN="$(jq -r '.token // empty' "${RUN_ROOT}/auth-session-after-bootstrap.body")"
[[ -n "${TOKEN}" ]] || fail "desktop session response did not include token"

me_status="$(record_request me-cookie GET "${APP_URL}/me" "" "${EVIDENCE_DIR}/me-cookie.json")"
[[ "${me_status}" == "200" ]] || fail "/me cookie status ${me_status}, want 200"
jq -e '.type == "user" and .userID != "" and .accountScopeID != "" and .teamID == null' "${EVIDENCE_DIR}/me-cookie.json" >/dev/null || fail "/me cookie did not prove user/account/no-team principal"

me_token_status="$(record_request me-token GET "${APP_URL}/me" "" "${EVIDENCE_DIR}/me-token.json" "token:${TOKEN}")"
[[ "${me_token_status}" == "200" ]] || fail "/me X-Swarm-Token status ${me_token_status}, want 200"
jq -e '.type == "user" and .userID != "" and .accountScopeID != "" and .teamID == null and .accountScopeSource == "session"' "${EVIDENCE_DIR}/me-token.json" >/dev/null || fail "/me X-Swarm-Token did not prove session-derived user/account/no-team principal"

[[ -S "${LOCAL_TRANSPORT_SOCKET}" ]] || fail "local transport socket was not created at ${LOCAL_TRANSPORT_SOCKET}"
local_session_status="$(record_local_transport_request auth-session-local-transport GET "/v1/auth/desktop/session" "" "${EVIDENCE_DIR}/auth-session-local-transport.json")"
[[ "${local_session_status}" == "200" ]] || fail "local transport desktop session status ${local_session_status}, want 200"
LOCAL_TOKEN="$(jq -r '.token // empty' "${RUN_ROOT}/auth-session-local-transport.body")"
[[ -n "${LOCAL_TOKEN}" ]] || fail "local transport session response did not include token"
me_local_token_status="$(record_local_transport_request me-local-token GET "/me" "" "${EVIDENCE_DIR}/me-local-token.json" "token:${LOCAL_TOKEN}")"
[[ "${me_local_token_status}" == "200" ]] || fail "local transport /me X-Swarm-Token status ${me_local_token_status}, want 200"
jq -e '.type == "user" and .userID != "" and .accountScopeID != "" and .teamID == null and .accountScopeSource == "session"' "${EVIDENCE_DIR}/me-local-token.json" >/dev/null || fail "local transport /me X-Swarm-Token did not prove session-derived user/account/no-team principal"

INSPECT_DB_PATH="$(copy_db_for_inspect)"
log_cmd "cd swarmd && go run ./cmd/pebble-inspect --db <db-copy> --check identity-foundation --json"
(cd swarmd && go run ./cmd/pebble-inspect --db "${INSPECT_DB_PATH}" --check identity-foundation --json) | tee "${EVIDENCE_DIR}/pebble-inspect-identity-foundation.json" >/dev/null
jq -e '.passed == true and .users >= 1 and .accountScopes >= 1 and .accountUsers >= 1 and .authSubjectIndexes >= 1' "${EVIDENCE_DIR}/pebble-inspect-identity-foundation.json" >/dev/null || fail "identity-foundation invariant failed"
log_cmd "cd swarmd && go run ./cmd/pebble-inspect --db <db-copy> --check no-teams-no-iam --json"
(cd swarmd && go run ./cmd/pebble-inspect --db "${INSPECT_DB_PATH}" --check no-teams-no-iam --json) | tee "${EVIDENCE_DIR}/pebble-inspect-no-teams-no-iam.json" >/dev/null
jq -e '.passed == true and (.teamKeys // 0) == 0 and (.teamMembershipKeys // 0) == 0 and (.iamKeys // 0) == 0' "${EVIDENCE_DIR}/pebble-inspect-no-teams-no-iam.json" >/dev/null || fail "no-teams-no-iam invariant failed"

stop_daemon

jq -n \
  --arg status PASS \
  --arg evidence_dir "${EVIDENCE_DIR}" \
  --arg app_url "${APP_URL}" \
  --arg desktop_url "${DESKTOP_URL}" \
  --arg db_path "${DB_PATH}" \
  --slurpfile foundation "${EVIDENCE_DIR}/pebble-inspect-identity-foundation.json" \
  --slurpfile noiam "${EVIDENCE_DIR}/pebble-inspect-no-teams-no-iam.json" \
  --slurpfile me "${EVIDENCE_DIR}/me-token.json" \
  --slurpfile local_me "${EVIDENCE_DIR}/me-local-token.json" \
  '{status:$status,exit_code:0,evidence_dir:$evidence_dir,app_url:$app_url,desktop_url:$desktop_url,db_path:$db_path,local_transport_socket:"<data-dir>/local-transport/api.sock",checks:{identity_foundation:$foundation[0],no_teams_no_iam:$noiam[0],x_swarm_token_principal:$me[0],local_transport_x_swarm_token_principal:$local_me[0]}}' >"${SUMMARY_JSON}"

printf 'PASS checkpoint-1 user/account foundation VM proof evidence_dir=%s\n' "${EVIDENCE_DIR}"
