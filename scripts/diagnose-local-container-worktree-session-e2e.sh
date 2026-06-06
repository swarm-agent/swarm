#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/diagnose-local-container-worktree-session-e2e.sh [options]

Live SSH-backed E2E for the primary -> local-container Sessions API path with
explicit worktree_mode:on. The test creates a brand-new local container on the
primary testbench host, opens a routed session using only swarm_id plus
workspace_binding_id, verifies the child-created git worktree, sends a real AI
run through the hosted session, and captures evidence/logs.

Options:
  --primary-ssh <alias>              SSH alias for the primary host. Default: testbench
  --primary-api-url <url>            API URL as seen from the primary host. Default: http://127.0.0.1:7781
  --source-workspace-path <path>     Source workspace path on the primary host. Auto-detected when omitted.
  --container-name <name>            New local container name. Default: local-worktree-e2e-<timestamp>
  --base-branch <branch>             Worktree base branch. Default: current branch in source workspace.
  --branch-name <name>               Requested worktree branch. Default: agent/e2e-local-container-worktree-<timestamp>
  --artifact-dir <path>              Local evidence dir. Default: .tmp/local-container-worktree-session-e2e/<timestamp>
  --provider <provider>              AI provider. Default: fireworks
  --model <model>                    AI model. Default: accounts/fireworks/models/kimi-k2p6
  --thinking <level>                 Thinking level. Default: low
  --fireworks-key-path <path>        Remote Fireworks key path used to seed credentials if needed.
                                      If omitted, auto-detects exactly one *fireworks*.key in the remote temp dir.
  --timeout-seconds <seconds>        Wait timeout. Default: 420
  --service <unit>                   Remote service unit for log capture. Default: swarm.service
  --open-only                        Stop after session-open diagnostics; skip the AI run.
  --cleanup                          Delete the created diagnostic container at exit.
  --help                             Show help.

Environment equivalents:
  SWARM_PRIMARY_SSH, SWARM_PRIMARY_API_URL, SWARM_SOURCE_WORKSPACE_PATH,
  SWARM_LOCAL_CONTAINER_WORKTREE_TEST_NAME, SWARM_WORKTREE_BASE_BRANCH,
  SWARM_WORKTREE_BRANCH_NAME, SWARM_LOCAL_CONTAINER_WORKTREE_ARTIFACT_DIR,
  SWARM_LOCAL_CONTAINER_WORKTREE_PROVIDER, SWARM_LOCAL_CONTAINER_WORKTREE_MODEL,
  SWARM_LOCAL_CONTAINER_WORKTREE_THINKING, SWARM_FIREWORKS_KEY_PATH,
  SWARM_LOCAL_CONTAINER_WORKTREE_TIMEOUT_SECONDS, SWARM_SERVICE_UNIT
EOF
}

log() { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "${1:-}" >/dev/null 2>&1 || fail "required command not found: ${1:-}"; }
json_get() { jq -r "${2:-.}" "${1:-}"; }
urlencode() { jq -rn --arg v "${1:-}" '$v|@uri'; }

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
PRIMARY_SSH="${SWARM_PRIMARY_SSH:-testbench}"
PRIMARY_API_URL="${SWARM_PRIMARY_API_URL:-http://127.0.0.1:7781}"
SOURCE_WORKSPACE_PATH="${SWARM_SOURCE_WORKSPACE_PATH:-}"
CONTAINER_NAME="${SWARM_LOCAL_CONTAINER_WORKTREE_TEST_NAME:-local-worktree-e2e-$(date +%Y%m%d-%H%M%S)}"
BASE_BRANCH="${SWARM_WORKTREE_BASE_BRANCH:-}"
BRANCH_NAME="${SWARM_WORKTREE_BRANCH_NAME:-}"
ARTIFACT_DIR="${SWARM_LOCAL_CONTAINER_WORKTREE_ARTIFACT_DIR:-}"
PROVIDER="${SWARM_LOCAL_CONTAINER_WORKTREE_PROVIDER:-fireworks}"
MODEL="${SWARM_LOCAL_CONTAINER_WORKTREE_MODEL:-accounts/fireworks/models/kimi-k2p6}"
THINKING="${SWARM_LOCAL_CONTAINER_WORKTREE_THINKING:-low}"
FIREWORKS_KEY_PATH="${SWARM_FIREWORKS_KEY_PATH:-}"
TIMEOUT_SECONDS="${SWARM_LOCAL_CONTAINER_WORKTREE_TIMEOUT_SECONDS:-420}"
SERVICE_UNIT="${SWARM_SERVICE_UNIT:-swarm.service}"
OPEN_ONLY="${SWARM_LOCAL_CONTAINER_WORKTREE_OPEN_ONLY:-false}"
CLEANUP="false"
DEPLOYMENT_ID=""
CHILD_SWARM_ID=""
RUNTIME_WORKSPACE_PATH=""
WORKSPACE_BINDING_ID=""
SESSION_ID=""
RUN_ID=""
PROOF_TOKEN="LOCAL_CONTAINER_WORKTREE_E2E_OK_${RANDOM}_$(date +%s)"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --primary-ssh) PRIMARY_SSH="${2:-}"; shift 2 ;;
    --primary-api-url|--primary-url) PRIMARY_API_URL="${2:-}"; shift 2 ;;
    --source-workspace-path|--workspace) SOURCE_WORKSPACE_PATH="${2:-}"; shift 2 ;;
    --container-name) CONTAINER_NAME="${2:-}"; shift 2 ;;
    --base-branch) BASE_BRANCH="${2:-}"; shift 2 ;;
    --branch-name) BRANCH_NAME="${2:-}"; shift 2 ;;
    --artifact-dir|--evidence-dir) ARTIFACT_DIR="${2:-}"; shift 2 ;;
    --provider) PROVIDER="${2:-}"; shift 2 ;;
    --model) MODEL="${2:-}"; shift 2 ;;
    --thinking) THINKING="${2:-}"; shift 2 ;;
    --fireworks-key-path) FIREWORKS_KEY_PATH="${2:-}"; shift 2 ;;
    --timeout-seconds) TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --service) SERVICE_UNIT="${2:-}"; shift 2 ;;
    --open-only) OPEN_ONLY="true"; shift ;;
    --cleanup) CLEANUP="true"; shift ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

require_command ssh
require_command jq
require_command curl
require_command base64
[[ -n "${PRIMARY_SSH}" ]] || fail "--primary-ssh is required"
[[ -n "${PRIMARY_API_URL}" ]] || fail "--primary-api-url is required"
[[ -n "${CONTAINER_NAME}" ]] || fail "--container-name is required"
[[ -n "${PROVIDER}" ]] || fail "--provider is required"
[[ -n "${MODEL}" ]] || fail "--model is required"
[[ -n "${THINKING}" ]] || fail "--thinking is required"
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ && "${TIMEOUT_SECONDS}" -gt 0 ]] || fail "--timeout-seconds must be a positive integer"
PRIMARY_API_URL="${PRIMARY_API_URL%/}"
if [[ -z "${ARTIFACT_DIR}" ]]; then
  ARTIFACT_DIR="${ROOT_DIR}/.tmp/local-container-worktree-session-e2e/$(date +%Y%m%d-%H%M%S)"
fi
mkdir -p -- "${ARTIFACT_DIR}"

remote_detect_checkout() {
  local alias="${1:-}"
  ssh "${alias}" 'set -euo pipefail
for candidate in "$HOME/swarm-go" "$HOME/src/swarm-go" "$HOME/work/swarm-go"; do
  if [ -d "$candidate" ] && [ -f "$candidate/AGENTS.md" ] && [ -x "$candidate/rebuild" ]; then
    printf "%s\n" "$candidate"
    exit 0
  fi
done
find "$HOME" /opt /srv /tmp -maxdepth 4 -type d -name swarm-go 2>/dev/null \
  | while IFS= read -r candidate; do
      if [ -f "$candidate/AGENTS.md" ] && [ -x "$candidate/rebuild" ]; then
        printf "%s\n" "$candidate"
        exit 0
      fi
    done
exit 1'
}

remote_api_json() {
  local alias="${1:-}" api_url="${2:-}" method="${3:-GET}" path="${4:-}" body="${5:-}" output_file="${6:-}" max_time="${7:-30}"
  local body_b64 response_file status_file ssh_status
  body_b64="$(printf '%s' "${body}" | base64 -w0)"
  response_file="$(mktemp)"
  status_file="$(mktemp)"
  if ssh "${alias}" 'bash -s' -- "${api_url}" "${method}" "${path}" "${max_time}" "${body_b64}" >"${response_file}" 2>"${status_file}" <<'REMOTE_API'
set -euo pipefail
api_url="${1%/}"
method="$2"
path="$3"
max_time="$4"
body_b64="${5-}"
cookie_file="$(mktemp)"
response_file="$(mktemp)"
body_file=""
cleanup() { rm -f -- "${cookie_file}" "${response_file}"; if [ -n "${body_file}" ]; then rm -f -- "${body_file}"; fi; }
trap cleanup EXIT
curl -sS --connect-timeout 3 --max-time 20 \
  -H 'Accept: application/json' \
  -H "Origin: ${api_url}" \
  -H "Referer: ${api_url}/" \
  -H 'Sec-Fetch-Site: same-origin' \
  -c "${cookie_file}" -b "${cookie_file}" \
  "${api_url}/v1/auth/desktop/session" >/dev/null || true
auth_token=""
if [ -s "${cookie_file}" ]; then
  auth_token="$(awk '$6 == "swarm_desktop_session" { value=$7 } END { print value }' "${cookie_file}")"
fi
args=(-sS --connect-timeout 3 --max-time "${max_time}" -o "${response_file}" -w '%{http_code}'
  -H 'Accept: application/json'
  -H "Origin: ${api_url}"
  -H "Referer: ${api_url}/"
  -H 'Sec-Fetch-Site: same-origin'
  -c "${cookie_file}" -b "${cookie_file}"
  -X "${method}")
if [ -n "${auth_token}" ]; then args+=(-H "Authorization: Bearer ${auth_token}"); fi
if [ -n "${body_b64}" ]; then
  body_file="$(mktemp)"
  printf '%s' "${body_b64}" | base64 -d >"${body_file}"
  args+=(-H 'Content-Type: application/json' --data-binary "@${body_file}")
fi
http_code="000"
if http_code="$(curl "${args[@]}" "${api_url}${path}")"; then :; fi
cat -- "${response_file}"
case "${http_code}" in
  2*) exit 0 ;;
  *) printf 'HTTP %s for %s %s: %s\n' "${http_code}" "${method}" "${path}" "$(cat -- "${response_file}")" >&2; exit 22 ;;
esac
REMOTE_API
  then
    ssh_status=0
  else
    ssh_status=$?
  fi
  if [[ -n "${output_file}" ]]; then cp -- "${response_file}" "${output_file}"; fi
  if [[ "${ssh_status}" != "0" ]]; then
    cat "${status_file}" >&2 || true
    rm -f -- "${response_file}" "${status_file}"
    return "${ssh_status}"
  fi
  jq empty "${response_file}" >/dev/null
  rm -f -- "${response_file}" "${status_file}"
}

capture_remote_logs() {
  local label="${1:-logs}"
  local out="${ARTIFACT_DIR}/${label}.log"
  {
    printf '### capture=%s time=%s primary=%s service=%s container=%s\n' "${label}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${PRIMARY_SSH}" "${SERVICE_UNIT}" "${CONTAINER_NAME}"
    ssh "${PRIMARY_SSH}" 'bash -s' -- "${SERVICE_UNIT}" "${CONTAINER_NAME}" <<'REMOTE_LOGS'
set +e
service_unit="$1"
container_name="$2"
printf '### host=%s\n' "$(hostname -f 2>/dev/null || hostname 2>/dev/null || true)"
printf '### service status: %s\n' "${service_unit}"
systemctl --no-pager --full status "${service_unit}" 2>&1 | sed -n '1,30p'
printf '\n### journalctl %s tail\n' "${service_unit}"
journalctl -u "${service_unit}" --no-pager -n 700 2>&1
printf '\n### journalctl session/routing/worktree/container grep\n'
journalctl -u "${service_unit}" --no-pager -n 4000 2>&1 | grep -Ei 'session|routed|peer|swarm_id|worktree|workspace_binding|authority|run stream|fireworks|credential|replicate|deploy|container|trusted principal' || true
if command -v podman >/dev/null 2>&1; then
  printf '\n### podman ps selected\n'
  podman ps -a --filter "name=${container_name}" 2>&1 || true
  printf '\n### podman logs %s\n' "${container_name}"
  podman logs --tail 700 "${container_name}" 2>&1 || true
fi
if command -v docker >/dev/null 2>&1; then
  printf '\n### docker ps selected\n'
  docker ps -a --filter "name=${container_name}" 2>&1 || true
  printf '\n### docker logs %s\n' "${container_name}"
  docker logs --tail 700 "${container_name}" 2>&1 || true
fi
REMOTE_LOGS
  } >"${out}" 2>&1 || true
}

cleanup_created() {
  capture_remote_logs final || true
  if [[ "${CLEANUP}" != "true" || -z "${DEPLOYMENT_ID}" ]]; then
    return 0
  fi
  set +e
  remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" POST "/v1/deploy/container/delete" "$(jq -nc --arg id "${DEPLOYMENT_ID}" '{ids:[$id]}')" "${ARTIFACT_DIR}/cleanup_container_delete.json" 180 || true
  remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" POST "/v1/swarm/containers/local/delete" "$(jq -nc --arg id "${DEPLOYMENT_ID}" --arg name "${CONTAINER_NAME}" --arg swarm_id "${CHILD_SWARM_ID}" '{id:$id,name:$name,swarm_id:$swarm_id,ids:[$id],names:[$name]}')" "${ARTIFACT_DIR}/cleanup_local_container_delete.json" 180 || true
  set -e
}
trap cleanup_created EXIT

ensure_provider_ready() {
  if [[ "${PROVIDER}" != "fireworks" ]]; then
    jq -nc --arg provider "${PROVIDER}" '{skipped:true,reason:"provider is not fireworks",provider:$provider}' >"${ARTIFACT_DIR}/provider_ready.json"
    return 0
  fi
  remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/auth/credentials?provider=fireworks\&limit=20" "" "${ARTIFACT_DIR}/fireworks_credentials_before.json" 30
  if jq -e '[(.credentials // .records // [])[]? | select((.active // false) == true)] | length > 0' "${ARTIFACT_DIR}/fireworks_credentials_before.json" >/dev/null; then
    jq -nc '{ok:true,provider:"fireworks",active_credential_present:true,seeded:false}' >"${ARTIFACT_DIR}/provider_ready.json"
    return 0
  fi

  local seed_status_file="${ARTIFACT_DIR}/fireworks_credential_seed.json"
  if ssh "${PRIMARY_SSH}" 'bash -s' -- "${PRIMARY_API_URL}" "${FIREWORKS_KEY_PATH}" >"${seed_status_file}" <<'REMOTE_SEED_FIREWORKS'
set -euo pipefail
api_url="${1%/}"
key_path="${2:-}"
if [ -z "${key_path}" ]; then
  remote_tmp_dir="${TMPDIR:-${XDG_RUNTIME_DIR:-}}"
  if [ -z "${remote_tmp_dir}" ]; then
    remote_tmp_dir="$(mktemp -d)"
    rmdir "${remote_tmp_dir}"
  fi
  set -- "${remote_tmp_dir}"/*fireworks*.key
  if [ "$#" -eq 1 ] && [ -f "$1" ]; then
    key_path="$1"
  else
    jq -nc --arg count "$#" --arg remote_tmp_dir "${remote_tmp_dir}" '{ok:false,error:("no active fireworks credential and auto-detect expected exactly one *fireworks*.key in " + $remote_tmp_dir + ", found " + $count)}'
    exit 22
  fi
fi
if [ ! -s "${key_path}" ]; then
  jq -nc --arg key_path "${key_path}" '{ok:false,error:"fireworks key file is missing or empty",key_path:$key_path}'
  exit 22
fi
api_key="$(tr -d '\r\n' <"${key_path}")"
if [ -z "${api_key}" ]; then
  jq -nc --arg key_path "${key_path}" '{ok:false,error:"fireworks key file is empty after trimming",key_path:$key_path}'
  exit 22
fi
cookie_file="$(mktemp)"
body_file="$(mktemp)"
response_file="$(mktemp)"
cleanup() { rm -f -- "${cookie_file}" "${body_file}" "${response_file}"; }
trap cleanup EXIT
curl -sS --connect-timeout 3 --max-time 20 \
  -H 'Accept: application/json' \
  -H "Origin: ${api_url}" \
  -H "Referer: ${api_url}/" \
  -H 'Sec-Fetch-Site: same-origin' \
  -c "${cookie_file}" -b "${cookie_file}" \
  "${api_url}/v1/auth/desktop/session" >/dev/null || true
auth_token=""
if [ -s "${cookie_file}" ]; then
  auth_token="$(awk '$6 == "swarm_desktop_session" { value=$7 } END { print value }' "${cookie_file}")"
fi
jq -nc --arg api_key "${api_key}" '{provider:"fireworks",type:"api",label:"local-container worktree e2e fireworks",api_key:$api_key,active:true}' >"${body_file}"
args=(-sS --connect-timeout 3 --max-time 90 -o "${response_file}" -w '%{http_code}'
  -H 'Accept: application/json'
  -H 'Content-Type: application/json'
  -H "Origin: ${api_url}"
  -H "Referer: ${api_url}/"
  -H 'Sec-Fetch-Site: same-origin'
  -c "${cookie_file}" -b "${cookie_file}"
  --data-binary "@${body_file}"
  -X POST)
if [ -n "${auth_token}" ]; then args+=(-H "Authorization: Bearer ${auth_token}"); fi
http_code="000"
if http_code="$(curl "${args[@]}" "${api_url}/v1/auth/credentials")"; then :; fi
case "${http_code}" in
  2*) jq -nc --arg key_path "${key_path}" '{ok:true,provider:"fireworks",seeded:true,key_path:$key_path}' ;;
  *) jq -nc --arg status "${http_code}" --arg body "$(cat -- "${response_file}")" '{ok:false,error:"credential seed request failed",status:$status,response:$body}'; exit 22 ;;
esac
REMOTE_SEED_FIREWORKS
  then
    :
  else
    fail "failed to seed fireworks credential on ${PRIMARY_SSH}: $(cat "${seed_status_file}" 2>/dev/null || true)"
  fi
  jq -e '.ok == true' "${seed_status_file}" >/dev/null || fail "failed to seed fireworks credential on ${PRIMARY_SSH}: $(cat "${seed_status_file}")"
  remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/auth/credentials?provider=fireworks\&limit=20" "" "${ARTIFACT_DIR}/fireworks_credentials_after.json" 30
  if ! jq -e '[(.credentials // .records // [])[]? | select((.active // false) == true)] | length > 0' "${ARTIFACT_DIR}/fireworks_credentials_after.json" >/dev/null; then
    fail "fireworks credential seed completed but no active credential is visible"
  fi
  jq -nc --slurpfile seed "${seed_status_file}" '{ok:true,provider:"fireworks",active_credential_present:true,seeded:true,seed:$seed[0]}' >"${ARTIFACT_DIR}/provider_ready.json"
}

wait_for_child_target() {
  local encoded_child deadline
  encoded_child="$(urlencode "${CHILD_SWARM_ID}")"
  deadline=$((SECONDS + TIMEOUT_SECONDS))
  while :; do
    remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/swarm/targets?swarm_id=${encoded_child}" "" "${ARTIFACT_DIR}/child_target_poll.json" 30
    if jq -e --arg id "${CHILD_SWARM_ID}" '.targets[]? | select((.swarm_id // "") == $id and (.online // false) == true and (.selectable // false) == true)' "${ARTIFACT_DIR}/child_target_poll.json" >/dev/null; then
      jq -c --arg id "${CHILD_SWARM_ID}" '.targets[]? | select((.swarm_id // "") == $id)' "${ARTIFACT_DIR}/child_target_poll.json" | head -n 1 | jq '.' >"${ARTIFACT_DIR}/child_target.json"
      return 0
    fi
    [[ "${SECONDS}" -lt "${deadline}" ]] || fail "timed out waiting for child target ${CHILD_SWARM_ID} online/selectable"
    sleep 3
  done
}

remote_container_worktree_check() {
  local route_runtime_path="${1:-}" expected_branch="${2:-}" output_file="${3:-}"
  ssh "${PRIMARY_SSH}" 'bash -s' -- "${CONTAINER_NAME}" "${RUNTIME_WORKSPACE_PATH}" "${route_runtime_path}" "${expected_branch}" >"${output_file}" <<'REMOTE_CHECK'
set -euo pipefail
container_name="$1"
base_path="$2"
route_path="$3"
expected_branch="$4"
fail_json() { jq -nc --arg error "$1" '{ok:false,error:$error}'; exit 0; }
runtime=""
if command -v podman >/dev/null 2>&1; then
  runtime="podman"
elif command -v docker >/dev/null 2>&1; then
  runtime="docker"
else
  fail_json "no podman or docker runtime on primary host"
fi
"${runtime}" inspect "${container_name}" >/dev/null 2>&1 || fail_json "container is not inspectable"
route_exists="$(${runtime} exec "${container_name}" sh -c '[ -d "$1" ] && printf true || printf false' sh "${route_path}" 2>/dev/null || printf false)"
inside="$(${runtime} exec "${container_name}" sh -c 'git -C "$1" rev-parse --is-inside-work-tree 2>/dev/null || true' sh "${route_path}" 2>/dev/null || true)"
toplevel="$(${runtime} exec "${container_name}" sh -c 'git -C "$1" rev-parse --show-toplevel 2>/dev/null || true' sh "${route_path}" 2>/dev/null || true)"
actual_branch="$(${runtime} exec "${container_name}" sh -c 'git -C "$1" branch --show-current 2>/dev/null || true' sh "${route_path}" 2>/dev/null || true)"
worktree_list="$(${runtime} exec "${container_name}" sh -c 'git -C "$1" worktree list --porcelain 2>/dev/null || git -C "$2" worktree list --porcelain 2>/dev/null || true' sh "${base_path}" "${route_path}" 2>/dev/null || true)"
branch_ok=false
case "${actual_branch}" in
  "${expected_branch}"|"${expected_branch}"/*) branch_ok=true ;;
esac
list_contains=false
if printf '%s\n' "${worktree_list}" | grep -Fqx "worktree ${route_path}"; then
  list_contains=true
fi
ok=false
if [ "${route_exists}" = true ] && [ "${inside}" = true ] && [ "${toplevel}" = "${route_path}" ] && [ "${branch_ok}" = true ] && [ "${list_contains}" = true ]; then
  ok=true
fi
jq -nc \
  --argjson ok "${ok}" \
  --arg runtime "${runtime}" \
  --arg container_name "${container_name}" \
  --arg base_runtime_workspace_path "${base_path}" \
  --arg route_runtime_workspace_path "${route_path}" \
  --arg expected_branch "${expected_branch}" \
  --arg actual_branch "${actual_branch}" \
  --arg toplevel "${toplevel}" \
  --arg inside "${inside}" \
  --arg route_exists "${route_exists}" \
  --arg branch_ok "${branch_ok}" \
  --arg list_contains "${list_contains}" \
  --arg worktree_list "${worktree_list}" \
  '{ok:$ok,runtime:$runtime,container_name:$container_name,base_runtime_workspace_path:$base_runtime_workspace_path,route_runtime_workspace_path:$route_runtime_workspace_path,expected_branch:$expected_branch,actual_branch:$actual_branch,toplevel:$toplevel,inside:$inside,route_exists:($route_exists == "true"),branch_ok:($branch_ok == "true"),worktree_list_contains_route:($list_contains == "true"),worktree_list:$worktree_list}'
REMOTE_CHECK
  jq empty "${output_file}" >/dev/null
}

assert_session_list_membership() {
  local route_runtime_path="${1:-}"
  local source_encoded runtime_encoded source_list runtime_list source_count runtime_count
  source_encoded="$(urlencode "${SOURCE_WORKSPACE_PATH}")"
  runtime_encoded="$(urlencode "${route_runtime_path}")"
  source_list="${ARTIFACT_DIR}/sessions_source_exact.json"
  runtime_list="${ARTIFACT_DIR}/sessions_runtime_exact.json"
  remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/sessions?cwd=${source_encoded}\&exact_path=true\&limit=200" "" "${source_list}" 30
  remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/sessions?cwd=${runtime_encoded}\&exact_path=true\&limit=200" "" "${runtime_list}" 30
  source_count="$(jq -r --arg id "${SESSION_ID}" '[.sessions[]? | select((.id // "") == $id)] | length' "${source_list}")"
  runtime_count="$(jq -r --arg id "${SESSION_ID}" '[.sessions[]? | select((.id // "") == $id)] | length' "${runtime_list}")"
  [[ "${source_count}" == "1" ]] || fail "session ${SESSION_ID} is not listed under requested/source workspace ${SOURCE_WORKSPACE_PATH}"
  [[ "${runtime_count}" == "0" ]] || fail "session ${SESSION_ID} is listed under realized container worktree workspace ${route_runtime_path}; this is workspace jump evidence"
}

if [[ -z "${SOURCE_WORKSPACE_PATH}" ]]; then
  SOURCE_WORKSPACE_PATH="$(remote_detect_checkout "${PRIMARY_SSH}")" || fail "could not auto-detect primary checkout path; pass --source-workspace-path"
fi
if [[ -z "${BASE_BRANCH}" ]]; then
  BASE_BRANCH="$(ssh "${PRIMARY_SSH}" git -C "${SOURCE_WORKSPACE_PATH}" rev-parse --abbrev-ref HEAD)" || fail "could not detect primary git branch; pass --base-branch"
fi
[[ -n "${BASE_BRANCH}" && "${BASE_BRANCH}" != "HEAD" ]] || fail "base branch must be an explicit branch name"
if [[ -z "${BRANCH_NAME}" ]]; then
  BRANCH_NAME="agent/e2e-local-container-worktree-$(date +%Y%m%d-%H%M%S)"
fi

log "local-container worktree E2E: primary=${PRIMARY_SSH} workspace=${SOURCE_WORKSPACE_PATH} container=${CONTAINER_NAME} branch=${BRANCH_NAME} artifacts=${ARTIFACT_DIR}"
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/readyz" "" "${ARTIFACT_DIR}/readyz.json" 20
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/auth/desktop/session" "" "${ARTIFACT_DIR}/desktop_session.json" 20
ensure_provider_ready
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" POST "/v1/model" "$(jq -nc --arg provider "${PROVIDER}" --arg model "${MODEL}" --arg thinking "${THINKING}" '{provider:$provider,model:$model,thinking:$thinking}')" "${ARTIFACT_DIR}/model_config_response.json" 60
capture_remote_logs before

replicate_body="$(jq -nc \
  --arg mode local \
  --arg swarm_name "${CONTAINER_NAME}" \
  --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
  '{mode:$mode,swarm_name:$swarm_name,sync:{enabled:true,mode:"managed"},workspaces:[{source_workspace_path:$source_workspace_path,replication_mode:"bundle",writable:true}]}')"
printf '%s\n' "${replicate_body}" >"${ARTIFACT_DIR}/replicate_request.json"
log "creating new local container through /v1/swarm/replicate: ${CONTAINER_NAME}"
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" POST "/v1/swarm/replicate" "${replicate_body}" "${ARTIFACT_DIR}/replicate_response.json" "${TIMEOUT_SECONDS}"
[[ "$(json_get "${ARTIFACT_DIR}/replicate_response.json" '.ok // false')" == "true" ]] || fail "replicate ok=false"
DEPLOYMENT_ID="$(json_get "${ARTIFACT_DIR}/replicate_response.json" '.swarm.deployment_id // empty')"
CHILD_SWARM_ID="$(json_get "${ARTIFACT_DIR}/replicate_response.json" '.swarm.id // empty')"
RUNTIME_WORKSPACE_PATH="$(json_get "${ARTIFACT_DIR}/replicate_response.json" '.workspaces[0].binding.destination_workspace_path // empty')"
WORKSPACE_BINDING_ID="$(json_get "${ARTIFACT_DIR}/replicate_response.json" '.workspaces[0].binding.binding_id // empty')"
[[ -n "${DEPLOYMENT_ID}" ]] || fail "replicate missing deployment_id"
[[ -n "${CHILD_SWARM_ID}" ]] || fail "replicate missing child swarm id"
[[ -n "${RUNTIME_WORKSPACE_PATH}" ]] || fail "replicate missing runtime workspace path"
[[ -n "${WORKSPACE_BINDING_ID}" ]] || fail "replicate missing workspace binding id"
wait_for_child_target
capture_remote_logs after_replicate

session_body="$(jq -nc \
  --arg title "Local container worktree E2E ${CONTAINER_NAME}" \
  --arg workspace_name "$(basename "${SOURCE_WORKSPACE_PATH}")" \
  --arg workspace_binding_id "${WORKSPACE_BINDING_ID}" \
  --arg base_branch "${BASE_BRANCH}" \
  --arg branch_name "${BRANCH_NAME}" \
  --arg provider "${PROVIDER}" \
  --arg model "${MODEL}" \
  --arg thinking "${THINKING}" \
  '{title:$title,workspace_name:$workspace_name,workspace_binding_id:$workspace_binding_id,mode:"auto",agent_name:"swarm",worktree_mode:"on",worktree_use_current_branch:false,worktree_base_branch:$base_branch,worktree_branch_name:$branch_name,preference:{provider:$provider,model:$model,thinking:$thinking},metadata:{local_container_worktree_session_e2e:true}}')"
printf '%s\n' "${session_body}" >"${ARTIFACT_DIR}/session_create_request.json"
if jq -e 'has("workspace_path") or has("host_workspace_path") or has("runtime_workspace_path")' "${ARTIFACT_DIR}/session_create_request.json" >/dev/null; then
  fail "session create request contains forbidden workspace path authority fields"
fi
encoded_child="$(urlencode "${CHILD_SWARM_ID}")"
log "opening routed worktree session through primary /v1/sessions?swarm_id=${CHILD_SWARM_ID}"
if ! remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" POST "/v1/sessions?swarm_id=${encoded_child}" "${session_body}" "${ARTIFACT_DIR}/session_create_response.json" "${TIMEOUT_SECONDS}"; then
  capture_remote_logs session_create_failed
  fail "session create failed; inspect ${ARTIFACT_DIR}/session_create_response.json and ${ARTIFACT_DIR}/session_create_failed.log for session_create_worktree_* and desktop_routed_peer_open_* diagnostics"
fi
jq -e '.ok == true' "${ARTIFACT_DIR}/session_create_response.json" >/dev/null || { capture_remote_logs session_create_not_ok; fail "session create ok=false; inspect ${ARTIFACT_DIR}/session_create_response.json and ${ARTIFACT_DIR}/session_create_not_ok.log"; }
SESSION_ID="$(json_get "${ARTIFACT_DIR}/session_create_response.json" '.session.id // empty')"
[[ -n "${SESSION_ID}" ]] || { capture_remote_logs session_create_missing_id; fail "session create response missing session.id"; }

remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/sessions/${SESSION_ID}" "" "${ARTIFACT_DIR}/session_after_open.json" 30
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/swarm/topology/session-route?session_id=$(urlencode "${SESSION_ID}")" "" "${ARTIFACT_DIR}/session_route_after_open.json" 30

primary_workspace="$(json_get "${ARTIFACT_DIR}/session_after_open.json" '.session.workspace_path // empty')"
worktree_enabled="$(json_get "${ARTIFACT_DIR}/session_after_open.json" '.session.worktree_enabled // false')"
worktree_root="$(json_get "${ARTIFACT_DIR}/session_after_open.json" '.session.worktree_root_path // empty')"
worktree_branch="$(json_get "${ARTIFACT_DIR}/session_after_open.json" '.session.worktree_branch // empty')"
hosted_runtime="$(json_get "${ARTIFACT_DIR}/session_after_open.json" '.session.metadata.swarm_routed_runtime_workspace_path // empty')"
route_runtime="$(json_get "${ARTIFACT_DIR}/session_route_after_open.json" '.route.runtime_workspace_path // empty')"
route_binding="$(json_get "${ARTIFACT_DIR}/session_route_after_open.json" '.route.workspace_binding_id // empty')"
route_runtime_swarm="$(json_get "${ARTIFACT_DIR}/session_route_after_open.json" '.route.runtime_swarm_id // empty')"
route_backend="$(json_get "${ARTIFACT_DIR}/session_route_after_open.json" '.route.backend_url // empty')"
route_host_workspace="$(json_get "${ARTIFACT_DIR}/session_route_after_open.json" '.route.host_workspace_path // empty')"
route_placement_generation="$(json_get "${ARTIFACT_DIR}/session_route_after_open.json" '.route.placement_generation // 0')"
route_binding_generation="$(json_get "${ARTIFACT_DIR}/session_route_after_open.json" '.route.binding_generation // 0')"

[[ "${primary_workspace}" == "${SOURCE_WORKSPACE_PATH}" ]] || fail "primary session workspace_path=${primary_workspace}, want source ${SOURCE_WORKSPACE_PATH}"
[[ "${worktree_enabled}" == "true" ]] || fail "session did not mirror child worktree_enabled=true"
[[ -n "${worktree_root}" && -n "${worktree_branch}" ]] || fail "session missing mirrored worktree root/branch"
[[ -n "${route_runtime}" ]] || fail "topology session route missing runtime_workspace_path"
[[ "${route_runtime}" != "${RUNTIME_WORKSPACE_PATH}" ]] || fail "worktree_mode:on route runtime did not move to a child-realized worktree path; still ${route_runtime}"
[[ "${route_runtime}" == "${hosted_runtime}" ]] || fail "hosted runtime metadata=${hosted_runtime}, route runtime=${route_runtime}"
[[ "${route_binding}" == "${WORKSPACE_BINDING_ID}" ]] || fail "route binding=${route_binding}, want ${WORKSPACE_BINDING_ID}"
[[ "${route_runtime_swarm}" == "${CHILD_SWARM_ID}" ]] || fail "route runtime swarm=${route_runtime_swarm}, want child ${CHILD_SWARM_ID}"
[[ -z "${route_backend}" ]] || fail "topology session route exposed forbidden backend_url=${route_backend}"
[[ -z "${route_host_workspace}" ]] || fail "topology session route exposed forbidden host_workspace_path=${route_host_workspace}"
[[ "${route_placement_generation}" =~ ^[0-9]+$ && "${route_placement_generation}" -gt 0 ]] || fail "route placement_generation is missing/zero"
[[ "${route_binding_generation}" =~ ^[0-9]+$ && "${route_binding_generation}" -gt 0 ]] || fail "route binding_generation is missing/zero"

remote_container_worktree_check "${route_runtime}" "${BRANCH_NAME}" "${ARTIFACT_DIR}/container_worktree_check.json"
jq -e '.ok == true' "${ARTIFACT_DIR}/container_worktree_check.json" >/dev/null || fail "container filesystem worktree check failed: $(cat "${ARTIFACT_DIR}/container_worktree_check.json")"
assert_session_list_membership "${route_runtime}"
capture_remote_logs after_open

if [[ "${OPEN_ONLY}" == "true" || "${OPEN_ONLY}" == "1" ]]; then
  jq -nc \
    --arg artifact_dir "${ARTIFACT_DIR}" \
    --arg primary_ssh "${PRIMARY_SSH}" \
    --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg deployment_id "${DEPLOYMENT_ID}" \
    --arg child_swarm_id "${CHILD_SWARM_ID}" \
    --arg workspace_binding_id "${WORKSPACE_BINDING_ID}" \
    --arg base_runtime_workspace_path "${RUNTIME_WORKSPACE_PATH}" \
    --arg route_runtime_workspace_path "${route_runtime}" \
    --arg session_id "${SESSION_ID}" \
    --arg worktree_root_path "${worktree_root}" \
    --arg worktree_branch "${worktree_branch}" \
    --arg requested_branch "${BRANCH_NAME}" \
    --arg route_placement_generation "${route_placement_generation}" \
    --arg route_binding_generation "${route_binding_generation}" \
    '{ok:true,open_only:true,artifact_dir:$artifact_dir,primary_ssh:$primary_ssh,source_workspace_path:$source_workspace_path,deployment_id:$deployment_id,child_swarm_id:$child_swarm_id,workspace_binding_id:$workspace_binding_id,base_runtime_workspace_path:$base_runtime_workspace_path,route_runtime_workspace_path:$route_runtime_workspace_path,session_id:$session_id,worktree_root_path:$worktree_root_path,worktree_branch:$worktree_branch,requested_branch:$requested_branch,route_placement_generation:($route_placement_generation|tonumber),route_binding_generation:($route_binding_generation|tonumber),evidence:["replicate_request.json","replicate_response.json","session_create_request.json","session_create_response.json","session_after_open.json","session_route_after_open.json","container_worktree_check.json","before.log","after_replicate.log","after_open.log","final.log"]}' \
    >"${ARTIFACT_DIR}/summary.json"
  log "PASS local-container worktree session-open diagnostics"
  cat "${ARTIFACT_DIR}/summary.json"
  exit 0
fi

prompt="Local container worktree E2E for routed session ${SESSION_ID}. Reply with exactly: ${PROOF_TOKEN}"
printf '%s\n' "${prompt}" >"${ARTIFACT_DIR}/prompt.txt"
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" POST "/v1/sessions/${SESSION_ID}/messages" "$(jq -nc --arg content "${prompt}" '{role:"user",content:$content}')" "${ARTIFACT_DIR}/user_message_response.json" 60
run_body="$(jq -nc --arg prompt "${prompt}" '{type:"run.start",prompt:$prompt,background:true,execution_context:{worktree_mode:"on"}}')"
printf '%s\n' "${run_body}" >"${ARTIFACT_DIR}/run_request.json"
log "starting real AI run through routed worktree session ${SESSION_ID}"
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" POST "/v1/sessions/${SESSION_ID}/run/stream" "${run_body}" "${ARTIFACT_DIR}/run_start_response.json" 120
RUN_ID="$(json_get "${ARTIFACT_DIR}/run_start_response.json" '.run_id // empty')"
[[ -n "${RUN_ID}" ]] || fail "run start response missing run_id"
capture_remote_logs after_run_start

deadline=$((SECONDS + TIMEOUT_SECONDS))
while :; do
  remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/sessions/${SESSION_ID}" "" "${ARTIFACT_DIR}/session_poll.json" 30 || true
  remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/sessions/${SESSION_ID}/messages?limit=100" "" "${ARTIFACT_DIR}/messages_poll.json" 30
  if jq -e --arg token "${PROOF_TOKEN}" '[.messages[]? | select((.role // "") == "assistant" and ((.content // "") | contains($token)))] | length > 0' "${ARTIFACT_DIR}/messages_poll.json" >/dev/null; then
    cp -- "${ARTIFACT_DIR}/messages_poll.json" "${ARTIFACT_DIR}/messages.json"
    break
  fi
  lifecycle_error="$(json_get "${ARTIFACT_DIR}/session_poll.json" '.session.lifecycle.error // empty' 2>/dev/null || true)"
  [[ -z "${lifecycle_error}" ]] || fail "run lifecycle error: ${lifecycle_error}"
  [[ "${SECONDS}" -lt "${deadline}" ]] || { cp -- "${ARTIFACT_DIR}/messages_poll.json" "${ARTIFACT_DIR}/messages.json"; fail "timed out waiting for assistant proof token ${PROOF_TOKEN}"; }
  sleep 5
done

remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/sessions/${SESSION_ID}" "" "${ARTIFACT_DIR}/session_after_run.json" 30
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/swarm/topology/session-route?session_id=$(urlencode "${SESSION_ID}")" "" "${ARTIFACT_DIR}/session_route_after_run.json" 30
post_run_route_runtime="$(json_get "${ARTIFACT_DIR}/session_route_after_run.json" '.route.runtime_workspace_path // empty')"
[[ "${post_run_route_runtime}" == "${route_runtime}" ]] || fail "route runtime changed after run: before=${route_runtime} after=${post_run_route_runtime}"
capture_remote_logs after_success

jq -nc \
  --arg artifact_dir "${ARTIFACT_DIR}" \
  --arg primary_ssh "${PRIMARY_SSH}" \
  --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
  --arg deployment_id "${DEPLOYMENT_ID}" \
  --arg child_swarm_id "${CHILD_SWARM_ID}" \
  --arg workspace_binding_id "${WORKSPACE_BINDING_ID}" \
  --arg base_runtime_workspace_path "${RUNTIME_WORKSPACE_PATH}" \
  --arg route_runtime_workspace_path "${route_runtime}" \
  --arg session_id "${SESSION_ID}" \
  --arg run_id "${RUN_ID}" \
  --arg worktree_root_path "${worktree_root}" \
  --arg worktree_branch "${worktree_branch}" \
  --arg requested_branch "${BRANCH_NAME}" \
  --arg provider "${PROVIDER}" \
  --arg model "${MODEL}" \
  --arg proof_token "${PROOF_TOKEN}" \
  --arg route_placement_generation "${route_placement_generation}" \
  --arg route_binding_generation "${route_binding_generation}" \
  '{ok:true,artifact_dir:$artifact_dir,primary_ssh:$primary_ssh,source_workspace_path:$source_workspace_path,deployment_id:$deployment_id,child_swarm_id:$child_swarm_id,workspace_binding_id:$workspace_binding_id,base_runtime_workspace_path:$base_runtime_workspace_path,route_runtime_workspace_path:$route_runtime_workspace_path,session_id:$session_id,run_id:$run_id,worktree_root_path:$worktree_root_path,worktree_branch:$worktree_branch,requested_branch:$requested_branch,provider:$provider,model:$model,proof_token:$proof_token,route_placement_generation:($route_placement_generation|tonumber),route_binding_generation:($route_binding_generation|tonumber),request_contract:"POST /v1/sessions?swarm_id=<child> with workspace_binding_id and no workspace_path/host_workspace_path/runtime_workspace_path",product_path:"primary testbench /v1/swarm/replicate -> /v1/sessions?swarm_id=local-child worktree_mode:on -> /v1/sessions/{id}/run/stream -> local-container authority/backend",evidence:["replicate_request.json","replicate_response.json","session_create_request.json","session_create_response.json","session_route_after_open.json","container_worktree_check.json","run_request.json","run_start_response.json","messages.json","session_route_after_run.json","before.log","after_replicate.log","after_open.log","after_run_start.log","after_success.log","final.log"]}' \
  >"${ARTIFACT_DIR}/summary.json"

log "PASS local-container worktree session E2E"
cat "${ARTIFACT_DIR}/summary.json"
