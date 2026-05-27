#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/diagnose-from-zero-managed-host-link-e2e.sh [options]

Control-node SSH-only live diagnostic/setup:
  1. From this control checkout, rebuild BOTH SSH targets from zero with scripts/ssh-fast-test.sh.
  2. From the control node, call each server's local API over direct SSH to that server.
  3. Start managed-host pairing on the managed server, approve it on the primary server.
  4. Create/verify the managed-host workspace binding for the primary checkout path.

This script does not SSH-hop through either server and does not call any local workstation
swarmd API. All API calls are executed via direct SSH to --primary-ssh or --managed-ssh
against that host's 127.0.0.1 swarmd API.

Required:
  --primary-ssh <alias>             Primary/manager SSH alias reachable from this control node.
  --managed-ssh <alias>             Managed-host SSH alias reachable from this control node.

Options:
  --primary-remote-dir <path>       Primary checkout path. Auto-detected when omitted.
  --managed-remote-dir <path>       Managed checkout path. Auto-detected when omitted.
  --primary-api-url <url>           Primary local API URL used on the primary host. Default: http://127.0.0.1:7781
  --managed-api-url <url>           Managed local API URL used on the managed host. Default: http://127.0.0.1:7781
  --source-workspace-path <path>    Primary source workspace path. Default: --primary-remote-dir.
  --managed-workspace-path <path>   Managed destination workspace path. Default: --managed-remote-dir.
  --workspace-name <name>           Workspace name for the binding. Default: basename of source workspace.
  --destination-root <path>         Managed destination root for API validation. Default: ~
  --artifact-dir <path>             Local evidence directory. Default: tmp/from-zero-managed-host-link/<timestamp>
  --timeout-seconds <n>             Readiness/link polling timeout. Default: 240
  --service <unit>                  Service unit passed to ssh-fast-test.sh. Default: swarm.service
  --db-path <path>                  DB path passed to ssh-fast-test.sh. Default: /var/lib/swarmd/swarmd.pebble
  --skip-rebuild                   Do not run ssh-fast-test.sh --from-zero; only relink/verify current remotes.
  --help                           Show this help.

Environment equivalents:
  SWARM_PRIMARY_SSH, SWARM_MANAGED_SSH, SWARM_PRIMARY_REMOTE_DIR,
  SWARM_MANAGED_REMOTE_DIR, SWARM_SOURCE_WORKSPACE_PATH,
  SWARM_MANAGED_WORKSPACE_PATH, SWARM_FROM_ZERO_LINK_ARTIFACT_DIR

No Flow diagnostics, multi-scenario harnesses, or unit tests are run by this script.
EOF
}

log() { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "${1:-}" >/dev/null 2>&1 || fail "required command not found: ${1:-}"; }
urlencode() { jq -rn --arg v "${1:-}" '$v|@uri'; }
json_get() { jq -r "${2:-.}" "${1:-}"; }

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
PRIMARY_SSH="${SWARM_PRIMARY_SSH:-}"
MANAGED_SSH="${SWARM_MANAGED_SSH:-}"
PRIMARY_REMOTE_DIR="${SWARM_PRIMARY_REMOTE_DIR:-}"
MANAGED_REMOTE_DIR="${SWARM_MANAGED_REMOTE_DIR:-}"
PRIMARY_API_URL="${SWARM_PRIMARY_API_URL:-http://127.0.0.1:7781}"
MANAGED_API_URL="${SWARM_MANAGED_API_URL:-http://127.0.0.1:7781}"
SOURCE_WORKSPACE_PATH="${SWARM_SOURCE_WORKSPACE_PATH:-}"
MANAGED_WORKSPACE_PATH="${SWARM_MANAGED_WORKSPACE_PATH:-}"
WORKSPACE_NAME="${SWARM_WORKSPACE_NAME:-}"
DESTINATION_ROOT="${SWARM_MANAGED_DESTINATION_ROOT:-~}"
ARTIFACT_DIR="${SWARM_FROM_ZERO_LINK_ARTIFACT_DIR:-}"
TIMEOUT_SECONDS="${SWARM_FROM_ZERO_LINK_TIMEOUT_SECONDS:-240}"
SERVICE_UNIT="${SWARM_SERVICE_UNIT:-swarm.service}"
DB_PATH="${SWARM_DB_PATH:-/var/lib/swarmd/swarmd.pebble}"
RUN_REBUILD="true"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --primary-ssh) PRIMARY_SSH="${2:-}"; shift 2 ;;
    --managed-ssh) MANAGED_SSH="${2:-}"; shift 2 ;;
    --primary-remote-dir) PRIMARY_REMOTE_DIR="${2:-}"; shift 2 ;;
    --managed-remote-dir) MANAGED_REMOTE_DIR="${2:-}"; shift 2 ;;
    --primary-api-url|--primary-url) PRIMARY_API_URL="${2:-}"; shift 2 ;;
    --managed-api-url|--managed-url) MANAGED_API_URL="${2:-}"; shift 2 ;;
    --source-workspace-path|--workspace) SOURCE_WORKSPACE_PATH="${2:-}"; shift 2 ;;
    --managed-workspace-path|--destination-path) MANAGED_WORKSPACE_PATH="${2:-}"; shift 2 ;;
    --workspace-name) WORKSPACE_NAME="${2:-}"; shift 2 ;;
    --destination-root) DESTINATION_ROOT="${2:-}"; shift 2 ;;
    --artifact-dir) ARTIFACT_DIR="${2:-}"; shift 2 ;;
    --timeout-seconds) TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --service) SERVICE_UNIT="${2:-}"; shift 2 ;;
    --db-path) DB_PATH="${2:-}"; shift 2 ;;
    --skip-rebuild|--no-rebuild) RUN_REBUILD="false"; shift ;;
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
PRIMARY_API_URL="${PRIMARY_API_URL%/}"
MANAGED_API_URL="${MANAGED_API_URL%/}"
if [[ -z "${ARTIFACT_DIR}" ]]; then
  ARTIFACT_DIR="${ROOT_DIR}/tmp/from-zero-managed-host-link/$(date +%Y%m%d-%H%M%S)"
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
  if [[ -n "${output_file}" ]]; then
    jq empty "${output_file}" >/dev/null
  fi
  rm -f -- "${response_file}" "${status_file}"
}

wait_remote_ready() {
  local alias="${1:-}" api_url="${2:-}" label="${3:-remote}" deadline out_file
  deadline=$((SECONDS + TIMEOUT_SECONDS))
  while :; do
    out_file="${ARTIFACT_DIR}/${label}_readyz_poll.json"
    if remote_api_json "${alias}" "${api_url}" GET "/readyz" "" "${out_file}" 20 >/dev/null 2>"${ARTIFACT_DIR}/${label}_readyz_poll.err"; then
      cp -- "${out_file}" "${ARTIFACT_DIR}/${label}_readyz.json"
      return 0
    fi
    [[ "${SECONDS}" -lt "${deadline}" ]] || fail "timed out waiting for ${label} readyz on ${alias}"
    sleep 3
  done
}

if [[ -z "${PRIMARY_REMOTE_DIR}" ]]; then
  log "detecting primary checkout on ${PRIMARY_SSH}"
  PRIMARY_REMOTE_DIR="$(remote_detect_checkout "${PRIMARY_SSH}")" || fail "could not detect primary checkout; pass --primary-remote-dir"
fi
if [[ -z "${MANAGED_REMOTE_DIR}" ]]; then
  log "detecting managed checkout on ${MANAGED_SSH}"
  MANAGED_REMOTE_DIR="$(remote_detect_checkout "${MANAGED_SSH}")" || fail "could not detect managed checkout; pass --managed-remote-dir"
fi
[[ -n "${SOURCE_WORKSPACE_PATH}" ]] || SOURCE_WORKSPACE_PATH="${PRIMARY_REMOTE_DIR}"
[[ -n "${MANAGED_WORKSPACE_PATH}" ]] || MANAGED_WORKSPACE_PATH="${MANAGED_REMOTE_DIR}"
[[ -n "${WORKSPACE_NAME}" ]] || WORKSPACE_NAME="$(basename "${SOURCE_WORKSPACE_PATH}")"

jq -nc \
  --arg primary_ssh "${PRIMARY_SSH}" \
  --arg managed_ssh "${MANAGED_SSH}" \
  --arg primary_remote_dir "${PRIMARY_REMOTE_DIR}" \
  --arg managed_remote_dir "${MANAGED_REMOTE_DIR}" \
  --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
  --arg managed_workspace_path "${MANAGED_WORKSPACE_PATH}" \
  --arg workspace_name "${WORKSPACE_NAME}" \
  '{primary_ssh:$primary_ssh,managed_ssh:$managed_ssh,primary_remote_dir:$primary_remote_dir,managed_remote_dir:$managed_remote_dir,source_workspace_path:$source_workspace_path,managed_workspace_path:$managed_workspace_path,workspace_name:$workspace_name}' \
  >"${ARTIFACT_DIR}/inputs.json"

log "from-zero managed-host link setup: artifacts=${ARTIFACT_DIR}"
log "control node direct SSH targets: primary=${PRIMARY_SSH} managed=${MANAGED_SSH}"
log "remote checkouts: primary=${PRIMARY_REMOTE_DIR} managed=${MANAGED_REMOTE_DIR}"

if [[ "${RUN_REBUILD}" == "true" ]]; then
  log "from-zero rebuild from control -> primary ${PRIMARY_SSH}"
  "${ROOT_DIR}/scripts/ssh-fast-test.sh" "${PRIMARY_SSH}" --from-zero --remote-dir "${PRIMARY_REMOTE_DIR}" --service "${SERVICE_UNIT}" --db-path "${DB_PATH}" | tee "${ARTIFACT_DIR}/primary_ssh_fast.log"
  log "from-zero rebuild from control -> managed ${MANAGED_SSH}"
  "${ROOT_DIR}/scripts/ssh-fast-test.sh" "${MANAGED_SSH}" --from-zero --remote-dir "${MANAGED_REMOTE_DIR}" --service "${SERVICE_UNIT}" --db-path "${DB_PATH}" | tee "${ARTIFACT_DIR}/managed_ssh_fast.log"
else
  log "skipping from-zero rebuild by explicit request"
fi

wait_remote_ready "${PRIMARY_SSH}" "${PRIMARY_API_URL}" primary
wait_remote_ready "${MANAGED_SSH}" "${MANAGED_API_URL}" managed

remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/swarm/discovery" "" "${ARTIFACT_DIR}/primary_discovery.json" 30
remote_api_json "${MANAGED_SSH}" "${MANAGED_API_URL}" GET "/v1/swarm/discovery" "" "${ARTIFACT_DIR}/managed_discovery.json" 30
primary_endpoint="$(json_get "${ARTIFACT_DIR}/primary_discovery.json" '.endpoint // empty')"
primary_swarm_id="$(json_get "${ARTIFACT_DIR}/primary_discovery.json" '.swarm_id // empty')"
managed_swarm_id="$(json_get "${ARTIFACT_DIR}/managed_discovery.json" '.swarm_id // empty')"
[[ -n "${primary_endpoint}" ]] || fail "primary discovery did not expose a reachable endpoint; see ${ARTIFACT_DIR}/primary_discovery.json"
[[ -n "${primary_swarm_id}" ]] || fail "primary discovery missing swarm_id"
[[ -n "${managed_swarm_id}" ]] || fail "managed discovery missing swarm_id"

start_body="$(jq -nc \
  --slurpfile primary "${ARTIFACT_DIR}/primary_discovery.json" \
  --slurpfile managed "${ARTIFACT_DIR}/managed_discovery.json" \
  '{endpoint:$primary[0].endpoint,manager_swarm_id:$primary[0].swarm_id,manager_name:($primary[0].name // "Manager"),managed_swarm_id:$managed[0].swarm_id,managed_name:($managed[0].name // "Managed Host"),rendezvous_transports:($primary[0].rendezvous_transports // [])}')"
printf '%s\n' "${start_body}" >"${ARTIFACT_DIR}/pairing_start_request.json"
log "starting managed-host pairing on managed via direct SSH: ${MANAGED_SSH} -> ${MANAGED_API_URL}"
remote_api_json "${MANAGED_SSH}" "${MANAGED_API_URL}" POST "/v1/swarm/remote-pairing/start" "${start_body}" "${ARTIFACT_DIR}/pairing_start_response.json" 90
request_id="$(json_get "${ARTIFACT_DIR}/pairing_start_response.json" '.request.request_id // empty')"
ceremony_code="$(json_get "${ARTIFACT_DIR}/pairing_start_response.json" '.ceremony.code // .request.ceremony_code // empty')"
managed_swarm_id="$(json_get "${ARTIFACT_DIR}/pairing_start_response.json" '.request.managed_swarm_id // empty')"
[[ -n "${request_id}" ]] || fail "pairing start response missing request_id"
[[ -n "${ceremony_code}" ]] || fail "pairing start response missing ceremony code"
[[ -n "${managed_swarm_id}" ]] || fail "pairing start response missing managed_swarm_id"

approve_body="$(jq -nc --arg request_id "${request_id}" --arg ceremony_code "${ceremony_code}" '{request_id:$request_id,approve:true,confirmed:true,ceremony_code:$ceremony_code}')"
printf '%s\n' "${approve_body}" >"${ARTIFACT_DIR}/pairing_approve_request.json"
log "approving managed-host pairing on primary via direct SSH: ${PRIMARY_SSH} -> ${PRIMARY_API_URL}"
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" POST "/v1/swarm/remote-pairing/approve" "${approve_body}" "${ARTIFACT_DIR}/pairing_approve_response.json" 180
[[ "$(json_get "${ARTIFACT_DIR}/pairing_approve_response.json" '.ok // false')" == "true" ]] || fail "pairing approve ok=false"

log "waiting for managed host target online/selectable on primary"
encoded_managed="$(urlencode "${managed_swarm_id}")"
deadline=$((SECONDS + TIMEOUT_SECONDS))
while :; do
  remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/swarm/targets?swarm_id=${encoded_managed}" "" "${ARTIFACT_DIR}/targets_poll.json" 30 || true
  if jq -e --arg id "${managed_swarm_id}" '.targets[]? | select((.swarm_id // "") == $id and (.online // false) == true and (.selectable // false) == true)' "${ARTIFACT_DIR}/targets_poll.json" >/dev/null; then
    jq -c --arg id "${managed_swarm_id}" '.targets[]? | select((.swarm_id // "") == $id)' "${ARTIFACT_DIR}/targets_poll.json" | head -n 1 | jq '.' >"${ARTIFACT_DIR}/managed_target.json"
    break
  fi
  [[ "${SECONDS}" -lt "${deadline}" ]] || fail "timed out waiting for managed target ${managed_swarm_id} online/selectable"
  sleep 3
done

upsert_body="$(jq -nc \
  --arg workspace_path "${SOURCE_WORKSPACE_PATH}" \
  --arg target_swarm_id "${managed_swarm_id}" \
  --arg destination_root "${DESTINATION_ROOT}" \
  --arg destination_path "${MANAGED_WORKSPACE_PATH}" \
  --arg workspace_name "${WORKSPACE_NAME}" \
  '{workspace_path:$workspace_path,target_swarm_id:$target_swarm_id,destination_root:$destination_root,destination_path:$destination_path,workspace_name:$workspace_name,provision:false}')"
printf '%s\n' "${upsert_body}" >"${ARTIFACT_DIR}/managed_workspace_link_request.json"
log "creating managed-host workspace binding: ${SOURCE_WORKSPACE_PATH} -> ${MANAGED_WORKSPACE_PATH}"
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" POST "/v1/workspace/managed-links/upsert" "${upsert_body}" "${ARTIFACT_DIR}/managed_workspace_link_response.json" 180
[[ "$(json_get "${ARTIFACT_DIR}/managed_workspace_link_response.json" '.ok // false')" == "true" ]] || fail "managed workspace link ok=false"
binding_id="$(json_get "${ARTIFACT_DIR}/managed_workspace_link_response.json" '.binding.binding_id // empty')"
[[ -n "${binding_id}" ]] || fail "managed workspace link response missing binding_id"

bindings_path="/v1/swarm/topology/workspace-bindings?source_workspace_path=$(urlencode "${SOURCE_WORKSPACE_PATH}")"
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "${bindings_path}" "" "${ARTIFACT_DIR}/workspace_bindings.json" 30
if ! jq -e --arg binding_id "${binding_id}" --arg managed_swarm_id "${managed_swarm_id}" '[.bindings[]? | select((.binding_id // "") == $binding_id and (.destination_runtime_swarm_id // "") == $managed_swarm_id)] | length == 1' "${ARTIFACT_DIR}/workspace_bindings.json" >/dev/null; then
  fail "verified workspace bindings did not contain ${binding_id} for ${managed_swarm_id}"
fi
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/swarm/topology" "" "${ARTIFACT_DIR}/topology_after_link.json" 30 || true

jq -nc \
  --arg primary_ssh "${PRIMARY_SSH}" \
  --arg managed_ssh "${MANAGED_SSH}" \
  --arg primary_swarm_id "${primary_swarm_id}" \
  --arg managed_swarm_id "${managed_swarm_id}" \
  --arg request_id "${request_id}" \
  --arg ceremony_code "${ceremony_code}" \
  --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
  --arg managed_workspace_path "${MANAGED_WORKSPACE_PATH}" \
  --arg workspace_binding_id "${binding_id}" \
  --arg artifact_dir "${ARTIFACT_DIR}" \
  '{ok:true,primary_ssh:$primary_ssh,managed_ssh:$managed_ssh,primary_swarm_id:$primary_swarm_id,managed_swarm_id:$managed_swarm_id,pairing_request_id:$request_id,ceremony_code:$ceremony_code,source_workspace_path:$source_workspace_path,managed_workspace_path:$managed_workspace_path,workspace_binding_id:$workspace_binding_id,artifact_dir:$artifact_dir}' \
  >"${ARTIFACT_DIR}/summary.json"

log "PASS from-zero managed-host link setup: ${ARTIFACT_DIR}/summary.json"
cat "${ARTIFACT_DIR}/summary.json"
