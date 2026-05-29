#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/diagnose-managed-host-worktree-session-e2e.sh [options]

Live SSH-backed E2E for managed-host session worktree behavior.

Default live testbench path:
  - SSH to primary alias `testbench`.
  - Through that primary server's local API, open managed-host sessions on `testbench2`.
  - Verify explicit worktree_mode:off stays on the managed-host workspace with no worktree.
  - Verify explicit worktree_mode:on creates/routes to a real managed-host git worktree.
  - Verify primary route/session metadata reflects the realized managed-host path.

This is not a unit test and not a mock. It uses the live primary + managed-host pairing.

Options:
  --primary-ssh <alias>             Primary/manager SSH alias. Default: testbench
  --managed-ssh <alias>             Managed-host SSH alias. Default: testbench2
  --primary-api-url <url>           Primary local API URL used on the primary host. Default: http://127.0.0.1:7781
  --source-workspace-path <path>    Primary source workspace path. Auto-detected when omitted.
  --managed-swarm-id <id>           Managed host swarm_id. Optional if --managed-name resolves it.
  --managed-name <name>             Managed host display name. If omitted, requires exactly one online non-self/non-local target.
  --case <off|on|both>              Which case to run. Default: both
  --base-branch <branch>            Explicit worktree base branch. Default: current branch in the primary workspace.
  --branch-name <name>              Explicit new worktree branch prefix. Default: agent/e2e-managed-worktree-<timestamp>
  --artifact-dir <path>             Local evidence directory. Default: .tmp/managed-host-worktree-session-e2e/<timestamp>
  --timeout-seconds <n>             SSH/API timeout. Default: 120
  --help                           Show this help.

Environment equivalents:
  SWARM_PRIMARY_SSH, SWARM_MANAGED_SSH, SWARM_PRIMARY_API_URL,
  SWARM_SOURCE_WORKSPACE_PATH, SWARM_MANAGED_SWARM_ID, SWARM_MANAGED_NAME,
  SWARM_MANAGED_WORKTREE_SESSION_CASE, SWARM_WORKTREE_BASE_BRANCH,
  SWARM_WORKTREE_BRANCH_NAME, SWARM_MANAGED_WORKTREE_SESSION_ARTIFACT_DIR,
  SWARM_MANAGED_WORKTREE_SESSION_TIMEOUT_SECONDS
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
CASE_TO_RUN="${SWARM_MANAGED_WORKTREE_SESSION_CASE:-both}"
BASE_BRANCH="${SWARM_WORKTREE_BASE_BRANCH:-}"
BRANCH_NAME="${SWARM_WORKTREE_BRANCH_NAME:-}"
ARTIFACT_DIR="${SWARM_MANAGED_WORKTREE_SESSION_ARTIFACT_DIR:-}"
TIMEOUT_SECONDS="${SWARM_MANAGED_WORKTREE_SESSION_TIMEOUT_SECONDS:-120}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --primary-ssh) PRIMARY_SSH="${2:-}"; shift 2 ;;
    --managed-ssh) MANAGED_SSH="${2:-}"; shift 2 ;;
    --primary-api-url|--primary-url) PRIMARY_API_URL="${2:-}"; shift 2 ;;
    --source-workspace-path|--workspace) SOURCE_WORKSPACE_PATH="${2:-}"; shift 2 ;;
    --managed-swarm-id) MANAGED_SWARM_ID="${2:-}"; shift 2 ;;
    --managed-name) MANAGED_NAME="${2:-}"; shift 2 ;;
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
[[ -n "${PRIMARY_SSH}" ]] || fail "--primary-ssh is required"
[[ -n "${MANAGED_SSH}" ]] || fail "--managed-ssh is required"
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ && "${TIMEOUT_SECONDS}" -gt 0 ]] || fail "--timeout-seconds must be a positive integer"
case "${CASE_TO_RUN}" in off|on|both) ;; *) fail "--case must be off, on, or both" ;; esac
PRIMARY_API_URL="${PRIMARY_API_URL%/}"
if [[ -z "${ARTIFACT_DIR}" ]]; then
  ARTIFACT_DIR="${ROOT_DIR}/.tmp/managed-host-worktree-session-e2e/$(date +%Y%m%d-%H%M%S)"
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
cleanup() {
  rm -f -- "${cookie_file}" "${response_file}"
  if [ -n "${body_file}" ]; then rm -f -- "${body_file}"; fi
}
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
if [ -n "${auth_token}" ]; then
  args+=(-H "Authorization: Bearer ${auth_token}")
fi
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
  if [[ -n "${output_file}" ]]; then
    cp -- "${response_file}" "${output_file}"
  fi
  if [[ "${ssh_status}" != "0" ]]; then
    cat "${status_file}" >&2 || true
    rm -f -- "${response_file}" "${status_file}"
    return "${ssh_status}"
  fi
  jq empty "${response_file}" >/dev/null
  rm -f -- "${response_file}" "${status_file}"
}

remote_check_git_workspace() {
  local workspace_path="${1:-}" output_file="${2:-}"
  [[ -n "${workspace_path}" ]] || fail "managed session did not return a workspace_path"
  ssh "${MANAGED_SSH}" 'bash -s' -- "${workspace_path}" <<'REMOTE_CHECK' >"${output_file}"
set -euo pipefail
workspace_path="$1"
fail_json() {
  jq -nc --arg error "$1" '{ok:false,error:$error}'
  exit 1
}
[ -d "${workspace_path}" ] || fail_json "workspace_path does not exist on managed host"
inside="$(git -C "${workspace_path}" rev-parse --is-inside-work-tree 2>/dev/null || true)"
[ "${inside}" = "true" ] || fail_json "workspace_path is not inside a git worktree"
toplevel="$(git -C "${workspace_path}" rev-parse --show-toplevel 2>/dev/null || true)"
[ "${toplevel}" = "${workspace_path}" ] || fail_json "workspace_path is not the git top-level"
actual_branch="$(git -C "${workspace_path}" branch --show-current 2>/dev/null || true)"
worktree_list="$(git -C "${workspace_path}" worktree list --porcelain 2>/dev/null || true)"
jq -nc \
  --arg workspace_path "${workspace_path}" \
  --arg toplevel "${toplevel}" \
  --arg branch "${actual_branch}" \
  --arg worktree_list "${worktree_list}" \
  '{ok:true,workspace_path:$workspace_path,toplevel:$toplevel,branch:$branch,worktree_list:$worktree_list}'
REMOTE_CHECK
}

remote_check_managed_worktree() {
  local workspace_path="${1:-}" root_path="${2:-}" branch_name="${3:-}" output_file="${4:-}"
  [[ -n "${workspace_path}" ]] || fail "managed session did not return a workspace_path"
  [[ -n "${root_path}" ]] || fail "managed session did not return a worktree root path"
  ssh "${MANAGED_SSH}" 'bash -s' -- "${workspace_path}" "${root_path}" "${branch_name}" <<'REMOTE_CHECK' >"${output_file}"
set -euo pipefail
workspace_path="$1"
root_path="$2"
branch_name="$3"
fail_json() {
  jq -nc --arg error "$1" '{ok:false,error:$error}'
  exit 1
}
[ -d "${workspace_path}" ] || fail_json "workspace_path does not exist on managed host"
[ -d "${root_path}" ] || fail_json "worktree root path does not exist on managed host"
inside="$(git -C "${workspace_path}" rev-parse --is-inside-work-tree 2>/dev/null || true)"
[ "${inside}" = "true" ] || fail_json "workspace_path is not inside a git worktree"
toplevel="$(git -C "${workspace_path}" rev-parse --show-toplevel 2>/dev/null || true)"
[ "${toplevel}" = "${workspace_path}" ] || fail_json "workspace_path is not the git worktree top-level"
actual_branch="$(git -C "${workspace_path}" branch --show-current 2>/dev/null || true)"
worktree_list="$(git -C "${root_path}" worktree list --porcelain 2>/dev/null || true)"
printf '%s\n' "${worktree_list}" | grep -Fqx "worktree ${workspace_path}" || fail_json "root git worktree list does not contain workspace_path"
if [ -n "${branch_name}" ] && [ "${actual_branch}" != "${branch_name}" ]; then
  jq -nc --arg error "worktree branch mismatch" --arg want "${branch_name}" --arg got "${actual_branch}" '{ok:false,error:$error,want:$want,got:$got}'
  exit 1
fi
jq -nc \
  --arg workspace_path "${workspace_path}" \
  --arg root_path "${root_path}" \
  --arg toplevel "${toplevel}" \
  --arg branch "${actual_branch}" \
  --arg worktree_list "${worktree_list}" \
  '{ok:true,workspace_path:$workspace_path,root_path:$root_path,toplevel:$toplevel,branch:$branch,worktree_list:$worktree_list}'
REMOTE_CHECK
}

validate_primary_route_and_mirror() {
  local case_name="${1:-}" session_id="${2:-}" expected_workspace="${3:-}"
  local encoded_session route_file sessions_file mirrored_workspace route_workspace
  encoded_session="$(urlencode "${session_id}")"
  route_file="${ARTIFACT_DIR}/${case_name}_primary_session_route.json"
  sessions_file="${ARTIFACT_DIR}/${case_name}_primary_sessions_list.json"
  remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/sessions?limit=100" "" "${sessions_file}" 30
  mirrored_workspace="$(jq -r --arg id "${session_id}" '.sessions[]? | select((.id // "") == $id) | .workspace_path // empty' "${sessions_file}" | head -n 1)"
  [[ -n "${mirrored_workspace}" ]] || fail "${case_name}: primary /v1/sessions did not include mirrored session ${session_id}"
  [[ "${mirrored_workspace}" == "${expected_workspace}" ]] || fail "${case_name}: primary mirrored workspace_path=${mirrored_workspace}, want ${expected_workspace}"

  remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/swarm/topology/session-route?session_id=${encoded_session}" "" "${route_file}" 30
  jq -e '.ok == true and (.route.session_id // "") != ""' "${route_file}" >/dev/null || fail "${case_name}: primary topology route missing for session ${session_id}"
  route_workspace="$(json_get "${route_file}" '.route.runtime_workspace_path // empty')"
  [[ "${route_workspace}" == "${expected_workspace}" ]] || fail "${case_name}: route runtime_workspace_path=${route_workspace}, want ${expected_workspace}"
}

open_and_validate_case() {
  local mode="${1:-}" branch_for_case title response_file request_file session_id session_workspace worktree_enabled worktree_root worktree_branch
  local runtime_cwd runtime_workspace_meta runtime_worktree_path host_workspace_meta filesystem_check_file
  branch_for_case="${BRANCH_NAME}"
  if [[ "${mode}" == "on" ]]; then
    branch_for_case="${BRANCH_NAME}/on"
  else
    branch_for_case="${BRANCH_NAME}/off-should-not-be-used"
  fi
  title="E2E managed-host worktree ${mode} $(date +%H%M%S)"
  request_file="${ARTIFACT_DIR}/managed_host_session_${mode}_request.json"
  response_file="${ARTIFACT_DIR}/managed_host_session_${mode}_response.json"
  open_body="$(jq -nc \
    --arg target_swarm_id "${MANAGED_SWARM_ID}" \
    --arg title "${title}" \
    --arg workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg workspace_name "$(basename "${SOURCE_WORKSPACE_PATH}")" \
    --arg worktree_mode "${mode}" \
    --arg base_branch "${BASE_BRANCH}" \
    --arg branch_name "${branch_for_case}" \
    '{target_swarm_id:$target_swarm_id,title:$title,workspace_path:$workspace_path,workspace_name:$workspace_name,mode:"auto",agent_name:"swarm",worktree_mode:$worktree_mode,worktree_use_current_branch:false,worktree_base_branch:$base_branch,worktree_branch_name:$branch_name,preference:{provider:"codex",model:"gpt-5.5",thinking:"low"}}')"
  printf '%s\n' "${open_body}" >"${request_file}"
  log "opening managed-host session with explicit worktree_mode:${mode} through ${PRIMARY_SSH} target=${MANAGED_NAME:-${MANAGED_SWARM_ID}}"
  remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" POST "/v1/swarm/managed-hosts/sessions/open" "${open_body}" "${response_file}" "${TIMEOUT_SECONDS}"

  jq -e '.ok == true' "${response_file}" >/dev/null || fail "${mode}: open response ok was not true"
  session_id="$(json_get "${response_file}" '.session.id // empty')"
  session_workspace="$(json_get "${response_file}" '.session.workspace_path // empty')"
  worktree_enabled="$(json_get "${response_file}" '.session.worktree_enabled // false')"
  worktree_root="$(json_get "${response_file}" '.session.worktree_root_path // empty')"
  worktree_branch="$(json_get "${response_file}" '.session.worktree_branch // empty')"
  runtime_cwd="$(json_get "${response_file}" '.session.metadata.swarm_managed_host_runtime_cwd // empty')"
  runtime_workspace_meta="$(json_get "${response_file}" '.session.metadata.swarm_managed_host_runtime_workspace_path // empty')"
  runtime_worktree_path="$(json_get "${response_file}" '.session.metadata.swarm_managed_host_runtime_worktree_path // empty')"
  host_workspace_meta="$(json_get "${response_file}" '.session.metadata.swarm_managed_host_host_workspace_path // empty')"

  [[ -n "${session_id}" ]] || fail "${mode}: open response missing session id"
  [[ -n "${session_workspace}" ]] || fail "${mode}: session ${session_id} missing workspace_path"
  [[ -n "${runtime_workspace_meta}" ]] || fail "${mode}: session ${session_id} missing managed-host runtime workspace metadata"
  [[ -n "${host_workspace_meta}" ]] || fail "${mode}: session ${session_id} missing managed-host host workspace metadata"

  if [[ "${mode}" == "off" ]]; then
    [[ "${worktree_enabled}" == "false" ]] || fail "off: session ${session_id} unexpectedly has worktree_enabled=true"
    [[ -z "${worktree_root}" ]] || fail "off: session ${session_id} unexpectedly has worktree_root_path=${worktree_root}"
    [[ -z "${worktree_branch}" ]] || fail "off: session ${session_id} unexpectedly has worktree_branch=${worktree_branch}"
    [[ -z "${runtime_worktree_path}" ]] || fail "off: session ${session_id} unexpectedly has runtime_worktree_path=${runtime_worktree_path}"
    [[ "${runtime_cwd}" == "${session_workspace}" ]] || fail "off: runtime_cwd=${runtime_cwd}, want session workspace ${session_workspace}"
    [[ "${runtime_workspace_meta}" == "${session_workspace}" ]] || fail "off: runtime workspace metadata=${runtime_workspace_meta}, want session workspace ${session_workspace}"
    filesystem_check_file="${ARTIFACT_DIR}/managed_host_filesystem_off_workspace_check.json"
    remote_check_git_workspace "${session_workspace}" "${filesystem_check_file}"
    jq -e '.ok == true' "${filesystem_check_file}" >/dev/null || fail "off: managed host regular workspace filesystem check failed"
  else
    [[ "${worktree_enabled}" == "true" ]] || fail "on: session ${session_id} is not worktree_enabled"
    [[ -n "${worktree_root}" ]] || fail "on: session ${session_id} missing worktree_root_path"
    [[ -n "${worktree_branch}" ]] || fail "on: session ${session_id} missing worktree_branch"
    [[ "${session_workspace}" != "${SOURCE_WORKSPACE_PATH}" ]] || fail "on: worktree session workspace stayed on primary source workspace"
    [[ "${session_workspace}" != "${worktree_root}" ]] || fail "on: worktree session workspace stayed on managed root workspace"
    [[ "${runtime_cwd}" == "${session_workspace}" ]] || fail "on: runtime_cwd=${runtime_cwd}, want session workspace ${session_workspace}"
    [[ "${runtime_worktree_path}" == "${session_workspace}" ]] || fail "on: runtime_worktree_path=${runtime_worktree_path}, want session workspace ${session_workspace}"
    case "${worktree_branch}" in
      "${branch_for_case}"|"${branch_for_case}"/*) ;;
      *) fail "on: session worktree_branch=${worktree_branch}, want ${branch_for_case} or ${branch_for_case}/<session-id>" ;;
    esac
    filesystem_check_file="${ARTIFACT_DIR}/managed_host_filesystem_on_worktree_check.json"
    remote_check_managed_worktree "${session_workspace}" "${worktree_root}" "${worktree_branch}" "${filesystem_check_file}"
    jq -e '.ok == true' "${filesystem_check_file}" >/dev/null || fail "on: managed host filesystem worktree check failed"
  fi

  validate_primary_route_and_mirror "${mode}" "${session_id}" "${session_workspace}"

  jq -nc \
    --arg mode "${mode}" \
    --arg session_id "${session_id}" \
    --arg target_swarm_id "${MANAGED_SWARM_ID}" \
    --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg managed_workspace_path "${session_workspace}" \
    --arg runtime_workspace_path "${runtime_workspace_meta}" \
    --arg worktree_root_path "${worktree_root}" \
    --arg worktree_branch "${worktree_branch}" \
    --arg base_branch "${BASE_BRANCH}" \
    '{ok:true,mode:$mode,session_id:$session_id,target_swarm_id:$target_swarm_id,source_workspace_path:$source_workspace_path,managed_workspace_path:$managed_workspace_path,runtime_workspace_path:$runtime_workspace_path,worktree_root_path:$worktree_root_path,worktree_branch:$worktree_branch,base_branch:$base_branch}' \
    >"${ARTIFACT_DIR}/summary_${mode}.json"
  log "PASS managed-host worktree_mode:${mode} session=${session_id} workspace=${session_workspace}"
}

if [[ -z "${SOURCE_WORKSPACE_PATH}" ]]; then
  SOURCE_WORKSPACE_PATH="$(remote_detect_checkout "${PRIMARY_SSH}")" || fail "could not auto-detect primary checkout path; pass --source-workspace-path"
fi
if [[ -z "${BASE_BRANCH}" ]]; then
  BASE_BRANCH="$(ssh "${PRIMARY_SSH}" git -C "${SOURCE_WORKSPACE_PATH}" rev-parse --abbrev-ref HEAD)" || fail "could not detect primary git branch; pass --base-branch"
fi
[[ -n "${BASE_BRANCH}" && "${BASE_BRANCH}" != "HEAD" ]] || fail "base branch must be an explicit branch name"
if [[ -z "${BRANCH_NAME}" ]]; then
  BRANCH_NAME="agent/e2e-managed-worktree-$(date +%Y%m%d-%H%M%S)"
fi

log "managed-host worktree E2E: primary=${PRIMARY_SSH} managed=${MANAGED_SSH} workspace=${SOURCE_WORKSPACE_PATH} case=${CASE_TO_RUN} artifacts=${ARTIFACT_DIR}"
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/readyz" "" "${ARTIFACT_DIR}/primary_readyz.json" 20
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/auth/desktop/session" "" "${ARTIFACT_DIR}/primary_desktop_session.json" 20
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/swarm/targets" "" "${ARTIFACT_DIR}/targets.json" 30

if [[ -n "${MANAGED_SWARM_ID}" ]]; then
  jq -c --arg id "${MANAGED_SWARM_ID}" '.targets[]? | select((.swarm_id // "") == $id)' "${ARTIFACT_DIR}/targets.json" | head -n 1 | jq '.' >"${ARTIFACT_DIR}/managed_target.json"
elif [[ -n "${MANAGED_NAME}" ]]; then
  jq -c --arg name "${MANAGED_NAME}" '.targets[]? | select((.name // "") == $name)' "${ARTIFACT_DIR}/targets.json" | head -n 1 | jq '.' >"${ARTIFACT_DIR}/managed_target.json"
else
  managed_ssh_url_needle="${MANAGED_SSH}."
  count="$(jq -r --arg needle "${managed_ssh_url_needle}" '[.targets[]? | select((.online // false) == true and (.selectable // false) == true and ((.kind // "") == "host") and (((.backend_url // "") | contains($needle)) or ((.name // "") == "swarm-go-managed")))] | length' "${ARTIFACT_DIR}/targets.json")"
  if [[ "${count}" == "1" ]]; then
    jq -c --arg needle "${managed_ssh_url_needle}" '.targets[]? | select((.online // false) == true and (.selectable // false) == true and ((.kind // "") == "host") and (((.backend_url // "") | contains($needle)) or ((.name // "") == "swarm-go-managed")))' "${ARTIFACT_DIR}/targets.json" | head -n 1 | jq '.' >"${ARTIFACT_DIR}/managed_target.json"
  else
    count="$(jq -r '[.targets[]? | select((.online // false) == true and (.selectable // false) == true and ((.kind // "") != "self") and ((.kind // "") != "local"))] | length' "${ARTIFACT_DIR}/targets.json")"
    [[ "${count}" == "1" ]] || fail "managed target not specified and auto-detect found ${count}; pass --managed-swarm-id or --managed-name"
    jq -c '.targets[]? | select((.online // false) == true and (.selectable // false) == true and ((.kind // "") != "self") and ((.kind // "") != "local"))' "${ARTIFACT_DIR}/targets.json" | head -n 1 | jq '.' >"${ARTIFACT_DIR}/managed_target.json"
  fi
fi
[[ -s "${ARTIFACT_DIR}/managed_target.json" ]] || fail "managed target not found"
MANAGED_SWARM_ID="$(json_get "${ARTIFACT_DIR}/managed_target.json" '.swarm_id // empty')"
MANAGED_NAME="$(json_get "${ARTIFACT_DIR}/managed_target.json" '.name // empty')"
[[ -n "${MANAGED_SWARM_ID}" ]] || fail "managed target missing swarm_id"
[[ "$(json_get "${ARTIFACT_DIR}/managed_target.json" '.online // false')" == "true" ]] || fail "managed target ${MANAGED_SWARM_ID} is not online"
[[ "$(json_get "${ARTIFACT_DIR}/managed_target.json" '.selectable // false')" == "true" ]] || fail "managed target ${MANAGED_SWARM_ID} is not selectable"

case "${CASE_TO_RUN}" in
  off) open_and_validate_case off ;;
  on) open_and_validate_case on ;;
  both)
    open_and_validate_case off
    open_and_validate_case on
    ;;
esac

summary_args=(--arg artifact_dir "${ARTIFACT_DIR}" --arg primary_ssh "${PRIMARY_SSH}" --arg managed_ssh "${MANAGED_SSH}" --arg managed_swarm_id "${MANAGED_SWARM_ID}" --arg managed_name "${MANAGED_NAME}" --arg case_to_run "${CASE_TO_RUN}")
summary_filter='{ok:true,artifact_dir:$artifact_dir,primary_ssh:$primary_ssh,managed_ssh:$managed_ssh,managed_swarm_id:$managed_swarm_id,managed_name:$managed_name,case:$case_to_run}'
if [[ -f "${ARTIFACT_DIR}/summary_off.json" && -f "${ARTIFACT_DIR}/summary_on.json" ]]; then
  jq -nc "${summary_args[@]}" --slurpfile off "${ARTIFACT_DIR}/summary_off.json" --slurpfile on "${ARTIFACT_DIR}/summary_on.json" "${summary_filter} + {off:$off[0],on:$on[0]}" >"${ARTIFACT_DIR}/summary.json"
elif [[ -f "${ARTIFACT_DIR}/summary_off.json" ]]; then
  jq -nc "${summary_args[@]}" --slurpfile off "${ARTIFACT_DIR}/summary_off.json" "${summary_filter} + {off:$off[0]}" >"${ARTIFACT_DIR}/summary.json"
elif [[ -f "${ARTIFACT_DIR}/summary_on.json" ]]; then
  jq -nc "${summary_args[@]}" --slurpfile on "${ARTIFACT_DIR}/summary_on.json" "${summary_filter} + {on:$on[0]}" >"${ARTIFACT_DIR}/summary.json"
fi

log "PASS managed-host worktree session E2E"
log "artifacts=${ARTIFACT_DIR}"
