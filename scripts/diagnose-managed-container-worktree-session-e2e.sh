#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/diagnose-managed-container-worktree-session-e2e.sh [options]

Live testbench E2E for the desktop-relevant managed-host CONTAINER routed session path.

This test uses the live SSH aliases by default:
  - primary/API server: testbench
  - managed host: testbench2

What it proves/fails:
  1. Creates a real managed child container on testbench2 through testbench /v1/swarm/replicate.
  2. Opens routed sessions through testbench /v1/sessions?swarm_id=<child>.
  3. Tests explicit worktree_mode:off and worktree_mode:on.
  4. Verifies primary session metadata stays pinned to the requested/source workspace.
  5. Verifies topology route runtime_workspace_path points to the realized child runtime/worktree path.
  6. Verifies source-workspace session list contains the session and realized runtime/worktree workspace list does not.

Options:
  --primary-ssh <alias>             Default: testbench
  --managed-ssh <alias>             Default: testbench2
  --primary-api-url <url>           Default: http://127.0.0.1:7781, used on primary host
  --source-workspace-path <path>    Auto-detected on primary when omitted
  --managed-swarm-id <id>           Managed host swarm_id. Optional if --managed-name resolves it.
  --managed-name <name>             Managed host display name. Auto-detects testbench2 host when omitted.
  --container-name <name>           Default: worktree-route-e2e-<timestamp>
  --case <off|on|both>              Default: both
  --base-branch <branch>            Default: current branch in primary workspace
  --branch-name <name>              Default: agent/e2e-managed-container-worktree-<timestamp>
  --artifact-dir <path>             Default: .tmp/managed-container-worktree-session-e2e/<timestamp>
  --timeout-seconds <n>             Default: 180
  --help                           Show help
EOF
}

log() { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "${1:-}" >/dev/null 2>&1 || fail "required command not found: ${1:-}"; }
json_get() { jq -r "${2:-.}" "${1:-}"; }
urlencode() { jq -rn --arg v "${1:-}" '$v|@uri'; }

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
PRIMARY_SSH="${SWARM_PRIMARY_SSH:-testbench}"
MANAGED_SSH="${SWARM_MANAGED_SSH:-testbench2}"
PRIMARY_API_URL="${SWARM_PRIMARY_API_URL:-http://127.0.0.1:7781}"
SOURCE_WORKSPACE_PATH="${SWARM_SOURCE_WORKSPACE_PATH:-}"
MANAGED_SWARM_ID="${SWARM_MANAGED_SWARM_ID:-}"
MANAGED_NAME="${SWARM_MANAGED_NAME:-}"
CONTAINER_NAME="${SWARM_MANAGED_CONTAINER_WORKTREE_TEST_NAME:-worktree-route-e2e-$(date +%Y%m%d-%H%M%S)}"
CASE_TO_RUN="${SWARM_MANAGED_CONTAINER_WORKTREE_CASE:-both}"
BASE_BRANCH="${SWARM_WORKTREE_BASE_BRANCH:-}"
BRANCH_NAME="${SWARM_WORKTREE_BRANCH_NAME:-}"
ARTIFACT_DIR="${SWARM_MANAGED_CONTAINER_WORKTREE_ARTIFACT_DIR:-}"
TIMEOUT_SECONDS="${SWARM_MANAGED_CONTAINER_WORKTREE_TIMEOUT_SECONDS:-180}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --primary-ssh) PRIMARY_SSH="${2:-}"; shift 2 ;;
    --managed-ssh) MANAGED_SSH="${2:-}"; shift 2 ;;
    --primary-api-url|--primary-url) PRIMARY_API_URL="${2:-}"; shift 2 ;;
    --source-workspace-path|--workspace) SOURCE_WORKSPACE_PATH="${2:-}"; shift 2 ;;
    --managed-swarm-id) MANAGED_SWARM_ID="${2:-}"; shift 2 ;;
    --managed-name) MANAGED_NAME="${2:-}"; shift 2 ;;
    --container-name) CONTAINER_NAME="${2:-}"; shift 2 ;;
    --case) CASE_TO_RUN="${2:-}"; shift 2 ;;
    --base-branch) BASE_BRANCH="${2:-}"; shift 2 ;;
    --branch-name) BRANCH_NAME="${2:-}"; shift 2 ;;
    --artifact-dir|--evidence-dir) ARTIFACT_DIR="${2:-}"; shift 2 ;;
    --timeout-seconds) TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

require_command ssh
require_command jq
require_command curl
require_command base64
[[ "${CASE_TO_RUN}" =~ ^(off|on|both)$ ]] || fail "--case must be off, on, or both"
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ && "${TIMEOUT_SECONDS}" -gt 0 ]] || fail "--timeout-seconds must be a positive integer"
PRIMARY_API_URL="${PRIMARY_API_URL%/}"
if [[ -z "${ARTIFACT_DIR}" ]]; then
  ARTIFACT_DIR="${ROOT_DIR}/.tmp/managed-container-worktree-session-e2e/$(date +%Y%m%d-%H%M%S)"
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

if [[ -z "${SOURCE_WORKSPACE_PATH}" ]]; then
  SOURCE_WORKSPACE_PATH="$(remote_detect_checkout "${PRIMARY_SSH}")" || fail "could not auto-detect primary checkout path; pass --source-workspace-path"
fi
if [[ -z "${BASE_BRANCH}" ]]; then
  BASE_BRANCH="$(ssh "${PRIMARY_SSH}" git -C "${SOURCE_WORKSPACE_PATH}" rev-parse --abbrev-ref HEAD)" || fail "could not detect primary git branch; pass --base-branch"
fi
[[ -n "${BASE_BRANCH}" && "${BASE_BRANCH}" != "HEAD" ]] || fail "base branch must be an explicit branch name"
if [[ -z "${BRANCH_NAME}" ]]; then
  BRANCH_NAME="agent/e2e-managed-container-worktree-$(date +%Y%m%d-%H%M%S)"
fi

log "managed-container routed worktree E2E: primary=${PRIMARY_SSH} managed=${MANAGED_SSH} workspace=${SOURCE_WORKSPACE_PATH} container=${CONTAINER_NAME} case=${CASE_TO_RUN} artifacts=${ARTIFACT_DIR}"
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/readyz" "" "${ARTIFACT_DIR}/primary_readyz.json" 20
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/auth/desktop/session" "" "${ARTIFACT_DIR}/primary_desktop_session.json" 20
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/swarm/targets" "" "${ARTIFACT_DIR}/targets_before.json" 30

if [[ -n "${MANAGED_SWARM_ID}" ]]; then
  jq -c --arg id "${MANAGED_SWARM_ID}" '.targets[]? | select((.swarm_id // "") == $id)' "${ARTIFACT_DIR}/targets_before.json" | head -n 1 | jq '.' >"${ARTIFACT_DIR}/managed_target.json"
elif [[ -n "${MANAGED_NAME}" ]]; then
  jq -c --arg name "${MANAGED_NAME}" '.targets[]? | select((.name // "") == $name)' "${ARTIFACT_DIR}/targets_before.json" | head -n 1 | jq '.' >"${ARTIFACT_DIR}/managed_target.json"
else
  needle="${MANAGED_SSH}."
  jq -c --arg needle "${needle}" '.targets[]? | select((.online // false) == true and (.selectable // false) == true and ((.kind // "") == "host") and (((.backend_url // "") | contains($needle)) or ((.name // "") == "swarm-go-managed")))' "${ARTIFACT_DIR}/targets_before.json" | head -n 1 | jq '.' >"${ARTIFACT_DIR}/managed_target.json"
fi
[[ -s "${ARTIFACT_DIR}/managed_target.json" ]] || fail "managed host target not found; pass --managed-swarm-id or --managed-name"
MANAGED_SWARM_ID="$(json_get "${ARTIFACT_DIR}/managed_target.json" '.swarm_id // empty')"
MANAGED_NAME="$(json_get "${ARTIFACT_DIR}/managed_target.json" '.name // empty')"
[[ -n "${MANAGED_SWARM_ID}" ]] || fail "managed target missing swarm_id"
[[ "$(json_get "${ARTIFACT_DIR}/managed_target.json" '.online // false')" == "true" ]] || fail "managed target ${MANAGED_SWARM_ID} is not online"

replicate_body="$(jq -nc \
  --arg mode local \
  --arg swarm_name "${CONTAINER_NAME}" \
  --arg target_host_swarm_id "${MANAGED_SWARM_ID}" \
  --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
  '{mode:$mode,swarm_name:$swarm_name,target_host_swarm_id:$target_host_swarm_id,sync:{enabled:true,mode:"managed"},workspaces:[{source_workspace_path:$source_workspace_path,replication_mode:"bundle",writable:true}]}')"
printf '%s\n' "${replicate_body}" >"${ARTIFACT_DIR}/replicate_request.json"
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" POST "/v1/swarm/replicate" "${replicate_body}" "${ARTIFACT_DIR}/replicate_response.json" "${TIMEOUT_SECONDS}"
[[ "$(json_get "${ARTIFACT_DIR}/replicate_response.json" '.ok // false')" == "true" ]] || fail "replicate ok=false"
DEPLOYMENT_ID="$(json_get "${ARTIFACT_DIR}/replicate_response.json" '.swarm.deployment_id // empty')"
CHILD_SWARM_ID="$(json_get "${ARTIFACT_DIR}/replicate_response.json" '.swarm.id // empty')"
RUNTIME_WORKSPACE_PATH="$(json_get "${ARTIFACT_DIR}/replicate_response.json" '.workspaces[0].binding.destination_workspace_path // empty')"
WORKSPACE_BINDING_ID="$(json_get "${ARTIFACT_DIR}/replicate_response.json" '.workspaces[0].binding.binding_id // empty')"
[[ -n "${DEPLOYMENT_ID}" && -n "${CHILD_SWARM_ID}" && -n "${RUNTIME_WORKSPACE_PATH}" ]] || fail "replicate missing deployment/child/runtime workspace"
log "created managed container deployment=${DEPLOYMENT_ID} child=${CHILD_SWARM_ID} runtime_workspace=${RUNTIME_WORKSPACE_PATH}"

encoded_child="$(urlencode "${CHILD_SWARM_ID}")"
deadline=$((SECONDS + TIMEOUT_SECONDS))
while :; do
  remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/swarm/targets?swarm_id=${encoded_child}" "" "${ARTIFACT_DIR}/child_target_poll.json" 30
  if jq -e --arg id "${CHILD_SWARM_ID}" '.targets[]? | select((.swarm_id // "") == $id and (.online // false) == true and (.selectable // false) == true)' "${ARTIFACT_DIR}/child_target_poll.json" >/dev/null; then
    jq -c --arg id "${CHILD_SWARM_ID}" '.targets[]? | select((.swarm_id // "") == $id)' "${ARTIFACT_DIR}/child_target_poll.json" | head -n 1 | jq '.' >"${ARTIFACT_DIR}/child_target.json"
    break
  fi
  [[ "${SECONDS}" -lt "${deadline}" ]] || fail "timed out waiting for child target ${CHILD_SWARM_ID} online/selectable"
  sleep 3
done

assert_session_list_membership() {
  local case_name="${1}" session_id="${2}" expected_source="${3}" realized_runtime="${4}"
  local source_encoded runtime_encoded source_list runtime_list source_count runtime_count
  source_encoded="$(urlencode "${expected_source}")"
  runtime_encoded="$(urlencode "${realized_runtime}")"
  source_list="${ARTIFACT_DIR}/${case_name}_sessions_source_exact.json"
  runtime_list="${ARTIFACT_DIR}/${case_name}_sessions_runtime_exact.json"
  remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/sessions?cwd=${source_encoded}\&exact_path=true\&limit=200" "" "${source_list}" 30
  remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/sessions?cwd=${runtime_encoded}\&exact_path=true\&limit=200" "" "${runtime_list}" 30
  source_count="$(jq -r --arg id "${session_id}" '[.sessions[]? | select((.id // "") == $id)] | length' "${source_list}")"
  runtime_count="$(jq -r --arg id "${session_id}" '[.sessions[]? | select((.id // "") == $id)] | length' "${runtime_list}")"
  [[ "${source_count}" == "1" ]] || fail "${case_name}: session ${session_id} is not listed under requested/source workspace ${expected_source}"
  [[ "${runtime_count}" == "0" ]] || fail "${case_name}: session ${session_id} is listed under realized runtime/worktree workspace ${realized_runtime}; this is workspace jump evidence"
}

open_case() {
  local mode="${1}" request_file response_file session_file route_file branch_for_case session_id primary_workspace primary_worktree_enabled primary_worktree_root primary_worktree_branch hosted_runtime route_runtime
  branch_for_case="${BRANCH_NAME}/${mode}"
  request_file="${ARTIFACT_DIR}/session_${mode}_request.json"
  response_file="${ARTIFACT_DIR}/session_${mode}_response.json"
  session_file="${ARTIFACT_DIR}/session_${mode}_primary_get.json"
  route_file="${ARTIFACT_DIR}/session_${mode}_route.json"
  body="$(jq -nc \
    --arg title "Managed container worktree ${mode} ${CONTAINER_NAME}" \
    --arg workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg host_workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg runtime_workspace_path "${RUNTIME_WORKSPACE_PATH}" \
    --arg workspace_name "$(basename "${SOURCE_WORKSPACE_PATH}")" \
    --arg worktree_mode "${mode}" \
    --arg base_branch "${BASE_BRANCH}" \
    --arg branch_name "${branch_for_case}" \
    '{title:$title,workspace_path:$workspace_path,host_workspace_path:$host_workspace_path,runtime_workspace_path:$runtime_workspace_path,workspace_name:$workspace_name,mode:"auto",agent_name:"swarm",worktree_mode:$worktree_mode,worktree_use_current_branch:false,worktree_base_branch:$base_branch,worktree_branch_name:$branch_name,preference:{provider:"codex",model:"gpt-5.5",thinking:"low"}}')"
  printf '%s\n' "${body}" >"${request_file}"
  log "opening routed managed-container session worktree_mode:${mode} child=${CHILD_SWARM_ID}"
  remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" POST "/v1/sessions?swarm_id=${encoded_child}" "${body}" "${response_file}" "${TIMEOUT_SECONDS}"
  jq -e '.ok == true' "${response_file}" >/dev/null || fail "${mode}: session create ok=false"
  session_id="$(json_get "${response_file}" '.session.id // empty')"
  [[ -n "${session_id}" ]] || fail "${mode}: response missing session.id"

  remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/sessions/${session_id}" "" "${session_file}" 30
  remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/swarm/topology/session-route?session_id=$(urlencode "${session_id}")" "" "${route_file}" 30

  primary_workspace="$(json_get "${session_file}" '.session.workspace_path // empty')"
  primary_worktree_enabled="$(json_get "${session_file}" '.session.worktree_enabled // false')"
  primary_worktree_root="$(json_get "${session_file}" '.session.worktree_root_path // empty')"
  primary_worktree_branch="$(json_get "${session_file}" '.session.worktree_branch // empty')"
  hosted_runtime="$(json_get "${session_file}" '.session.metadata.swarm_routed_runtime_workspace_path // .session.metadata.hosted_runtime_workspace_path // empty')"
  route_runtime="$(json_get "${route_file}" '.route.runtime_workspace_path // empty')"

  [[ "${primary_workspace}" == "${SOURCE_WORKSPACE_PATH}" ]] || fail "${mode}: primary session workspace_path=${primary_workspace}, want requested/source ${SOURCE_WORKSPACE_PATH}; this is the desktop jump"
  jq -e '.ok == true and (.route.session_id // "") != ""' "${route_file}" >/dev/null || fail "${mode}: missing primary topology route for ${session_id}"
  [[ -n "${route_runtime}" ]] || fail "${mode}: topology route missing runtime_workspace_path"

  if [[ "${mode}" == "off" ]]; then
    [[ "${primary_worktree_enabled}" == "false" ]] || fail "off: primary session unexpectedly worktree_enabled=true"
    [[ -z "${primary_worktree_root}" && -z "${primary_worktree_branch}" ]] || fail "off: primary session has worktree fields root=${primary_worktree_root} branch=${primary_worktree_branch}"
    [[ "${route_runtime}" == "${RUNTIME_WORKSPACE_PATH}" ]] || fail "off: route runtime=${route_runtime}, want container runtime workspace ${RUNTIME_WORKSPACE_PATH}"
    [[ "${hosted_runtime}" == "${RUNTIME_WORKSPACE_PATH}" ]] || fail "off: hosted runtime metadata=${hosted_runtime}, want ${RUNTIME_WORKSPACE_PATH}"
  else
    [[ "${primary_worktree_enabled}" == "true" ]] || fail "on: primary session did not mirror child worktree_enabled=true"
    [[ -n "${primary_worktree_root}" && -n "${primary_worktree_branch}" ]] || fail "on: primary session missing mirrored worktree fields"
    [[ "${route_runtime}" != "${RUNTIME_WORKSPACE_PATH}" ]] || fail "on: route runtime did not move to child-realized worktree path; still ${route_runtime}"
    [[ "${route_runtime}" == "${hosted_runtime}" ]] || log "WARN on: route runtime=${route_runtime}, hosted runtime metadata=${hosted_runtime}; route runtime is authoritative for child-realized worktree"
  fi
  assert_session_list_membership "${mode}" "${session_id}" "${SOURCE_WORKSPACE_PATH}" "${route_runtime}"

  jq -nc \
    --arg mode "${mode}" \
    --arg session_id "${session_id}" \
    --arg primary_workspace "${primary_workspace}" \
    --arg route_runtime "${route_runtime}" \
    --arg hosted_runtime "${hosted_runtime}" \
    --arg worktree_enabled "${primary_worktree_enabled}" \
    '{ok:true,mode:$mode,session_id:$session_id,primary_workspace_path:$primary_workspace,route_runtime_workspace_path:$route_runtime,hosted_runtime_workspace_path:$hosted_runtime,worktree_enabled:$worktree_enabled}' \
    >"${ARTIFACT_DIR}/summary_${mode}.json"
  log "PASS managed-container routed session worktree_mode:${mode} session=${session_id} primary_workspace=${primary_workspace} route_runtime=${route_runtime}"
}

case "${CASE_TO_RUN}" in
  off) open_case off ;;
  on) open_case on ;;
  both)
    open_case off
    open_case on
    ;;
esac

if [[ -f "${ARTIFACT_DIR}/summary_off.json" && -f "${ARTIFACT_DIR}/summary_on.json" ]]; then
  jq -nc --slurpfile off "${ARTIFACT_DIR}/summary_off.json" --slurpfile on "${ARTIFACT_DIR}/summary_on.json" \
    --arg artifact_dir "${ARTIFACT_DIR}" --arg child_swarm_id "${CHILD_SWARM_ID}" --arg deployment_id "${DEPLOYMENT_ID}" \
    '{ok:true,artifact_dir:$artifact_dir,child_swarm_id:$child_swarm_id,deployment_id:$deployment_id,off:$off[0],on:$on[0]}' >"${ARTIFACT_DIR}/summary.json"
elif [[ -f "${ARTIFACT_DIR}/summary_off.json" ]]; then
  jq -nc --slurpfile off "${ARTIFACT_DIR}/summary_off.json" --arg artifact_dir "${ARTIFACT_DIR}" --arg child_swarm_id "${CHILD_SWARM_ID}" --arg deployment_id "${DEPLOYMENT_ID}" '{ok:true,artifact_dir:$artifact_dir,child_swarm_id:$child_swarm_id,deployment_id:$deployment_id,off:$off[0]}' >"${ARTIFACT_DIR}/summary.json"
elif [[ -f "${ARTIFACT_DIR}/summary_on.json" ]]; then
  jq -nc --slurpfile on "${ARTIFACT_DIR}/summary_on.json" --arg artifact_dir "${ARTIFACT_DIR}" --arg child_swarm_id "${CHILD_SWARM_ID}" --arg deployment_id "${DEPLOYMENT_ID}" '{ok:true,artifact_dir:$artifact_dir,child_swarm_id:$child_swarm_id,deployment_id:$deployment_id,on:$on[0]}' >"${ARTIFACT_DIR}/summary.json"
fi

log "PASS managed-container routed worktree E2E"
cat "${ARTIFACT_DIR}/summary.json"
