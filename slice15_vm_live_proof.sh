#!/usr/bin/env bash
set -euo pipefail

EVIDENCE_DIR="${1:-.tmp/checkpoint-1/slice-1.5/$(date -u +%Y%m%dT%H%M%SZ)-vm-protected-api-guard-proof}"
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

RUN_ROOT="$(mktemp -d /var/tmp/swarm-slice15-live-XXXXXX)"
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
WORKSPACE_DIR="${RUN_ROOT}/guard-workspace"
mkdir -p "${WORKSPACE_DIR}"

log_cmd "cd swarmd && go test ./internal/api ./internal/identity -run 'TestProtectedCreateAPIs|TestDesktopSession|TestOnboarding|TestSession'"
(cd swarmd && go test ./internal/api ./internal/identity -run 'TestProtectedCreateAPIs|TestDesktopSession|TestOnboarding|TestSession')

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
  curl -fsS -X POST -H 'Content-Type: application/json' --data '{"reason":"slice15-proof"}' "${API_URL}/v1/system/shutdown" >/dev/null 2>&1 || kill "${DAEMON_PID}" 2>/dev/null || true
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
    '{name:$name,method:$method,url:$url,status:($status|tonumber),auth:"redacted",body:$body}' >>"${REQUESTS_LOG}"
  printf '%s' "${status}"
}

STARTUP_CONFIG_TEXT="$(printf 'startup_mode = interactive\nhost = 127.0.0.1\nport = %s\ndesktop_port = %s\npeer_transport_port = %s\n' "${API_PORT}" "${DESKTOP_PORT}" "${PEER_PORT}")"
start_daemon

workspace_before_status="$(record_request create-workspace-before-bootstrap POST "${DESKTOP_URL}/v1/workspace/add" "{\"path\":\"${WORKSPACE_DIR}\"}" "${EVIDENCE_DIR}/create-workspace-before-bootstrap.json")"
agent_before_status="$(record_request create-agent-before-bootstrap PUT "${DESKTOP_URL}/v2/agents/slice15-vm" '{"mode":"subagent","description":"Slice 1.5 VM guard"}' "${EVIDENCE_DIR}/create-agent-before-bootstrap.json")"
credential_before_status="$(record_request create-credential-before-bootstrap POST "${DESKTOP_URL}/v1/auth/credentials" '{"provider":"codex","type":"api","api_key":"test-key"}' "${EVIDENCE_DIR}/create-credential-before-bootstrap.json")"
[[ "${workspace_before_status}" == "401" ]] || fail "workspace before bootstrap status ${workspace_before_status}, want 401"
[[ "${agent_before_status}" == "401" ]] || fail "agent before bootstrap status ${agent_before_status}, want 401"
[[ "${credential_before_status}" == "401" ]] || fail "credential before bootstrap status ${credential_before_status}, want 401"

bootstrap_status="$(record_request onboarding-bootstrap POST "${DESKTOP_URL}/v1/onboarding" '{"username":"slice15-user","swarm_name":"Slice15 Device"}' "${EVIDENCE_DIR}/onboarding-bootstrap.json")"
[[ "${bootstrap_status}" == "200" ]] || fail "onboarding bootstrap status ${bootstrap_status}, want 200"
jq -e '.identity.bootstrapped == true and .identity.user_id != "" and .identity.username == "slice15-user"' "${EVIDENCE_DIR}/onboarding-bootstrap.json" >/dev/null || fail "bootstrap response missing user-first identity"

workspace_after_status="$(record_request create-workspace-after-bootstrap POST "${DESKTOP_URL}/v1/workspace/add" "{\"path\":\"${WORKSPACE_DIR}\"}" "${EVIDENCE_DIR}/create-workspace-after-bootstrap.json")"
[[ "${workspace_after_status}" == "200" ]] || fail "workspace after bootstrap status ${workspace_after_status}, want 200"
jq -e '.ok == true and .workspace.workspace_path != ""' "${EVIDENCE_DIR}/create-workspace-after-bootstrap.json" >/dev/null || fail "workspace create response missing workspace"

old_cookie_status="$(curl -sS -o "${RUN_ROOT}/old-cookie-create-agent.body" -w '%{http_code}' -X PUT -H 'Content-Type: application/json' --data '{"mode":"subagent"}' -H 'Cookie: swarm_desktop_session=old-random-cookie' -H "Origin: ${DESKTOP_URL}" -H "Referer: ${DESKTOP_URL}/" -H 'Sec-Fetch-Site: same-origin' "${DESKTOP_URL}/v2/agents/old-cookie-vm")"
sed -E 's/[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+/<redacted>/g' "${RUN_ROOT}/old-cookie-create-agent.body" >"${EVIDENCE_DIR}/old-cookie-create-agent-rejected.json"
jq -nc --arg status "${old_cookie_status}" --rawfile body "${EVIDENCE_DIR}/old-cookie-create-agent-rejected.json" '{name:"old-cookie-create-agent-rejected",method:"PUT",url:"/v2/agents/old-cookie-vm",status:($status|tonumber),auth:"redacted",body:$body}' >>"${REQUESTS_LOG}"
[[ "${old_cookie_status}" == "401" ]] || fail "old cookie create agent status ${old_cookie_status}, want 401"

stop_daemon

jq -n \
  --arg status PASS \
  --arg evidence_dir "${EVIDENCE_DIR}" \
  --arg api_url "${API_URL}" \
  --arg desktop_url "${DESKTOP_URL}" \
  --argjson before_workspace "${workspace_before_status}" \
  --argjson before_agent "${agent_before_status}" \
  --argjson before_credential "${credential_before_status}" \
  --argjson after_workspace "${workspace_after_status}" \
  --argjson old_cookie "${old_cookie_status}" \
  '{status:$status,evidence_dir:$evidence_dir,api_url:$api_url,desktop_url:$desktop_url,checks:{pre_bootstrap:{create_workspace_status:$before_workspace,create_agent_status:$before_agent,create_credential_status:$before_credential},post_bootstrap:{create_workspace_status:$after_workspace},negative:{old_cookie_create_agent_status:$old_cookie},auth_redacted:true}}' >"${SUMMARY_JSON}"

if [[ -n "${HOST_EVIDENCE_DIR}" ]]; then
  mkdir -p "${HOST_EVIDENCE_DIR}"
  cp -a "${EVIDENCE_DIR}/." "${HOST_EVIDENCE_DIR}/"
fi
printf 'PASS slice 1.5 VM live proof evidence_dir=%s\n' "${EVIDENCE_DIR}"
