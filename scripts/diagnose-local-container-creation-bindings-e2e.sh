#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/diagnose-local-container-creation-bindings-e2e.sh [options]

Focused live E2E for only the local-container creation/linking checkpoint.
It creates one local container on the primary host, then verifies the primary and
container-side topology binding records agree on the two-sided binding contract.
It does not create/open a session and does not use route/session authority.

Options:
  --primary-ssh <alias>              SSH alias for the primary host. Default: testbench
  --primary-api-url <url>            API URL as seen from the primary host. Default: http://127.0.0.1:7781
  --source-workspace-path <path>     Source workspace path on the primary host. Auto-detected when omitted.
  --container-name <name>            New local container name. Default: local-binding-e2e-<timestamp>
  --artifact-dir <path>              Local evidence dir. Default: .tmp/local-container-creation-bindings-e2e/<timestamp>
  --timeout-seconds <seconds>        Wait timeout. Default: 420
  --service <unit>                   Remote service unit for log capture. Default: swarm.service
  --cleanup                          Delete the created diagnostic container at exit.
  --help                             Show help.

Environment equivalents:
  SWARM_PRIMARY_SSH, SWARM_PRIMARY_API_URL, SWARM_SOURCE_WORKSPACE_PATH,
  SWARM_LOCAL_CONTAINER_BINDING_TEST_NAME, SWARM_LOCAL_CONTAINER_BINDING_ARTIFACT_DIR,
  SWARM_LOCAL_CONTAINER_BINDING_TIMEOUT_SECONDS, SWARM_SERVICE_UNIT
EOF
}

log() { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "${1:-}" >/dev/null 2>&1 || fail "required command not found: ${1:-}"; }
json_get() { jq -r "${2:-.}" "${1:-}"; }
urlencode() { jq -rn --arg v "${1:-}" '$v|@uri'; }
b64() { base64 | tr -d '\n'; }

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
PRIMARY_SSH="${SWARM_PRIMARY_SSH:-testbench}"
PRIMARY_API_URL="${SWARM_PRIMARY_API_URL:-http://127.0.0.1:7781}"
SOURCE_WORKSPACE_PATH="${SWARM_SOURCE_WORKSPACE_PATH:-}"
CONTAINER_NAME="${SWARM_LOCAL_CONTAINER_BINDING_TEST_NAME:-local-binding-e2e-$(date +%Y%m%d-%H%M%S)}"
ARTIFACT_DIR="${SWARM_LOCAL_CONTAINER_BINDING_ARTIFACT_DIR:-}"
TIMEOUT_SECONDS="${SWARM_LOCAL_CONTAINER_BINDING_TIMEOUT_SECONDS:-420}"
SERVICE_UNIT="${SWARM_SERVICE_UNIT:-swarm.service}"
CLEANUP="false"
DEPLOYMENT_ID=""
CHILD_SWARM_ID=""
WORKSPACE_BINDING_ID=""
RUNTIME_WORKSPACE_PATH=""
PRIMARY_SWARM_ID=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --primary-ssh) PRIMARY_SSH="${2:-}"; shift 2 ;;
    --primary-api-url|--primary-url) PRIMARY_API_URL="${2:-}"; shift 2 ;;
    --source-workspace-path|--workspace) SOURCE_WORKSPACE_PATH="${2:-}"; shift 2 ;;
    --container-name) CONTAINER_NAME="${2:-}"; shift 2 ;;
    --artifact-dir|--evidence-dir) ARTIFACT_DIR="${2:-}"; shift 2 ;;
    --timeout-seconds) TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --service) SERVICE_UNIT="${2:-}"; shift 2 ;;
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
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ && "${TIMEOUT_SECONDS}" -gt 0 ]] || fail "--timeout-seconds must be a positive integer"
PRIMARY_API_URL="${PRIMARY_API_URL%/}"
if [[ -z "${ARTIFACT_DIR}" ]]; then
  ARTIFACT_DIR="${ROOT_DIR}/.tmp/local-container-creation-bindings-e2e/$(date +%Y%m%d-%H%M%S)"
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
  body_b64="$(printf '%s' "${body}" | b64)"
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

remote_container_api_json() {
  local alias="${1:-}" container="${2:-}" api_url="${3:-http://127.0.0.1:7781}" method="${4:-GET}" path="${5:-}" body="${6:-}" output_file="${7:-}" max_time="${8:-30}"
  local body_b64 response_file status_file ssh_status
  body_b64="$(printf '%s' "${body}" | b64)"
  response_file="$(mktemp)"
  status_file="$(mktemp)"
  if ssh "${alias}" 'bash -s' -- "${container}" "${api_url}" "${method}" "${path}" "${max_time}" "${body_b64}" >"${response_file}" 2>"${status_file}" <<'REMOTE_CONTAINER_API'
set -euo pipefail
container="$1"
api_url="${2%/}"
method="$3"
path="$4"
max_time="$5"
body_b64="${6-}"
runtime=""
if command -v podman >/dev/null 2>&1 && podman container exists "${container}" >/dev/null 2>&1; then
  runtime="podman"
elif command -v docker >/dev/null 2>&1 && docker container inspect "${container}" >/dev/null 2>&1; then
  runtime="docker"
else
  printf 'container not found for podman/docker: %s\n' "${container}" >&2
  exit 22
fi
"${runtime}" exec -i "${container}" sh -s -- "${api_url}" "${method}" "${path}" "${max_time}" "${body_b64}" <<'IN_CONTAINER_API'
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
http_code="000"
if [ -n "${body_b64}" ]; then
  body_file="$(mktemp)"
  printf '%s' "${body_b64}" | base64 -d >"${body_file}"
  if http_code="$(curl -sS --connect-timeout 3 --max-time "${max_time}" \
    -o "${response_file}" -w '%{http_code}' \
    -H 'Accept: application/json' \
    -H "Origin: ${api_url}" \
    -H "Referer: ${api_url}/" \
    -H 'Sec-Fetch-Site: same-origin' \
    -c "${cookie_file}" -b "${cookie_file}" \
    -X "${method}" \
    -H 'Content-Type: application/json' \
    --data-binary "@${body_file}" \
    "${api_url}${path}")"; then :; fi
else
  if http_code="$(curl -sS --connect-timeout 3 --max-time "${max_time}" \
    -o "${response_file}" -w '%{http_code}' \
    -H 'Accept: application/json' \
    -H "Origin: ${api_url}" \
    -H "Referer: ${api_url}/" \
    -H 'Sec-Fetch-Site: same-origin' \
    -c "${cookie_file}" -b "${cookie_file}" \
    -X "${method}" \
    "${api_url}${path}")"; then :; fi
fi
cat -- "${response_file}"
case "${http_code}" in
  2*) exit 0 ;;
  *) printf 'HTTP %s for %s %s: %s\n' "${http_code}" "${method}" "${path}" "$(cat -- "${response_file}")" >&2; exit 22 ;;
esac
IN_CONTAINER_API
REMOTE_CONTAINER_API
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
systemctl --user --no-pager --full status "${service_unit}" 2>&1 | sed -n '1,30p'
systemctl --no-pager --full status "${service_unit}" 2>&1 | sed -n '1,30p'
printf '\n### journalctl %s binding/container grep\n' "${service_unit}"
journalctl -u "${service_unit}" --no-pager -n 4000 2>&1 | grep -Ei 'workspace_binding|binding|authority|replicate|deploy|container|topology|stale' || true
if command -v podman >/dev/null 2>&1; then
  printf '\n### podman ps selected\n'
  podman ps -a --filter "name=${container_name}" 2>&1 || true
  printf '\n### podman logs %s\n' "${container_name}"
  podman logs --tail 500 "${container_name}" 2>&1 || true
fi
if command -v docker >/dev/null 2>&1; then
  printf '\n### docker ps selected\n'
  docker ps -a --filter "name=${container_name}" 2>&1 || true
  printf '\n### docker logs %s\n' "${container_name}"
  docker logs --tail 500 "${container_name}" 2>&1 || true
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

wait_remote_ready() {
  local deadline=$((SECONDS + TIMEOUT_SECONDS))
  while :; do
    if remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/readyz" "" "${ARTIFACT_DIR}/readyz_poll.json" 15 >/dev/null 2>&1; then
      cp -- "${ARTIFACT_DIR}/readyz_poll.json" "${ARTIFACT_DIR}/readyz.json"
      return 0
    fi
    [[ "${SECONDS}" -lt "${deadline}" ]] || fail "timed out waiting for primary readyz"
    sleep 2
  done
}

wait_for_child_target() {
  local encoded_child deadline
  encoded_child="$(urlencode "${CHILD_SWARM_ID}")"
  deadline=$((SECONDS + TIMEOUT_SECONDS))
  while :; do
    remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/swarm/targets?swarm_id=${encoded_child}" "" "${ARTIFACT_DIR}/child_target_poll.json" 30 || true
    if jq -e --arg id "${CHILD_SWARM_ID}" '.targets[]? | select((.swarm_id // "") == $id and (.online // false) == true and (.selectable // false) == true)' "${ARTIFACT_DIR}/child_target_poll.json" >/dev/null; then
      jq -c --arg id "${CHILD_SWARM_ID}" '.targets[]? | select((.swarm_id // "") == $id)' "${ARTIFACT_DIR}/child_target_poll.json" | head -n 1 | jq '.' >"${ARTIFACT_DIR}/child_target.json"
      return 0
    fi
    [[ "${SECONDS}" -lt "${deadline}" ]] || fail "timed out waiting for child target ${CHILD_SWARM_ID} online/selectable"
    sleep 3
  done
}

wait_for_container_api() {
  local deadline=$((SECONDS + TIMEOUT_SECONDS))
  while :; do
    if remote_container_api_json "${PRIMARY_SSH}" "${CONTAINER_NAME}" "http://127.0.0.1:7781" GET "/readyz" "" "${ARTIFACT_DIR}/container_readyz_poll.json" 15 >/dev/null 2>&1; then
      cp -- "${ARTIFACT_DIR}/container_readyz_poll.json" "${ARTIFACT_DIR}/container_readyz.json"
      return 0
    fi
    [[ "${SECONDS}" -lt "${deadline}" ]] || fail "timed out waiting for container API readyz for ${CONTAINER_NAME}"
    sleep 3
  done
}

if [[ -z "${SOURCE_WORKSPACE_PATH}" ]]; then
  log "detecting primary checkout on ${PRIMARY_SSH}"
  SOURCE_WORKSPACE_PATH="$(remote_detect_checkout "${PRIMARY_SSH}")" || fail "could not detect primary checkout; pass --source-workspace-path"
fi

jq -nc \
  --arg primary_ssh "${PRIMARY_SSH}" \
  --arg primary_api_url "${PRIMARY_API_URL}" \
  --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
  --arg container_name "${CONTAINER_NAME}" \
  --arg artifact_dir "${ARTIFACT_DIR}" \
  '{primary_ssh:$primary_ssh,primary_api_url:$primary_api_url,source_workspace_path:$source_workspace_path,container_name:$container_name,artifact_dir:$artifact_dir}' \
  >"${ARTIFACT_DIR}/inputs.json"

log "local-container creation/binding E2E: primary=${PRIMARY_SSH} workspace=${SOURCE_WORKSPACE_PATH} artifacts=${ARTIFACT_DIR}"
wait_remote_ready
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/swarm/discovery" "" "${ARTIFACT_DIR}/primary_discovery.json" 30
PRIMARY_SWARM_ID="$(json_get "${ARTIFACT_DIR}/primary_discovery.json" '.swarm_id // empty')"
[[ -n "${PRIMARY_SWARM_ID}" ]] || fail "primary discovery missing swarm_id"
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/swarm/topology" "" "${ARTIFACT_DIR}/topology_before.json" 30 || true
capture_remote_logs before

workspace_add_body="$(jq -nc --arg path "${SOURCE_WORKSPACE_PATH}" --arg name "$(basename "${SOURCE_WORKSPACE_PATH}")" '{path:$path,name:$name,make_current:true}')"
printf '%s\n' "${workspace_add_body}" >"${ARTIFACT_DIR}/workspace_add_request.json"
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" POST "/v1/workspace/add" "${workspace_add_body}" "${ARTIFACT_DIR}/workspace_add_response.json" 60
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/workspace/list?limit=500" "" "${ARTIFACT_DIR}/primary_workspace_list.json" 30
primary_source_workspace_id="$(jq -r --arg path "${SOURCE_WORKSPACE_PATH}" '[.workspaces[]? | select((.path // "") == $path)] | last | .workspace_id // empty' "${ARTIFACT_DIR}/primary_workspace_list.json")"
primary_source_workspace_generation="$(jq -r --arg path "${SOURCE_WORKSPACE_PATH}" '[.workspaces[]? | select((.path // "") == $path)] | last | .workspace_generation // 0' "${ARTIFACT_DIR}/primary_workspace_list.json")"
[[ -n "${primary_source_workspace_id}" ]] || fail "primary workspace list missing source workspace ${SOURCE_WORKSPACE_PATH}"
[[ "${primary_source_workspace_generation}" =~ ^[0-9]+$ && "${primary_source_workspace_generation}" -gt 0 ]] || fail "primary source workspace generation missing/zero"

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
WORKSPACE_BINDING_ID="$(json_get "${ARTIFACT_DIR}/replicate_response.json" '.workspaces[0].binding.binding_id // empty')"
RUNTIME_WORKSPACE_PATH="$(json_get "${ARTIFACT_DIR}/replicate_response.json" '.workspaces[0].binding.destination_workspace_path // empty')"
[[ -n "${DEPLOYMENT_ID}" ]] || fail "replicate missing deployment_id"
[[ -n "${CHILD_SWARM_ID}" ]] || fail "replicate missing child swarm id"
[[ -n "${WORKSPACE_BINDING_ID}" ]] || fail "replicate missing workspace binding id"
[[ -n "${RUNTIME_WORKSPACE_PATH}" ]] || fail "replicate missing destination workspace path"
wait_for_child_target
wait_for_container_api
capture_remote_logs after_replicate

encoded_binding="$(urlencode "${WORKSPACE_BINDING_ID}")"
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/swarm/topology/workspace-bindings?workspace_binding_id=${encoded_binding}" "" "${ARTIFACT_DIR}/primary_workspace_binding_lookup.json" 30
jq -e '.ok == true and ([.bindings[]?] | length == 1)' "${ARTIFACT_DIR}/primary_workspace_binding_lookup.json" >/dev/null || fail "primary binding lookup did not return exactly one binding"
jq '.bindings[0]' "${ARTIFACT_DIR}/primary_workspace_binding_lookup.json" >"${ARTIFACT_DIR}/primary_binding.json"
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/swarm/topology/runtime-owner?runtime_swarm_id=$(urlencode "${CHILD_SWARM_ID}")" "" "${ARTIFACT_DIR}/primary_runtime_owner.json" 30
remote_api_json "${PRIMARY_SSH}" "${PRIMARY_API_URL}" GET "/v1/swarm/topology" "" "${ARTIFACT_DIR}/topology_after_replicate.json" 30 || true

remote_container_api_json "${PRIMARY_SSH}" "${CONTAINER_NAME}" "http://127.0.0.1:7781" GET "/v1/swarm/discovery" "" "${ARTIFACT_DIR}/container_discovery.json" 30
container_swarm_id="$(json_get "${ARTIFACT_DIR}/container_discovery.json" '.swarm_id // empty')"
[[ "${container_swarm_id}" == "${CHILD_SWARM_ID}" ]] || fail "container discovery swarm_id=${container_swarm_id}, want child ${CHILD_SWARM_ID}"
remote_container_api_json "${PRIMARY_SSH}" "${CONTAINER_NAME}" "http://127.0.0.1:7781" GET "/v1/swarm/topology/workspace-bindings?workspace_binding_id=${encoded_binding}" "" "${ARTIFACT_DIR}/container_workspace_binding_lookup.json" 30
jq -e '.ok == true and ([.bindings[]?] | length == 1)' "${ARTIFACT_DIR}/container_workspace_binding_lookup.json" >/dev/null || fail "container binding lookup did not return exactly one binding"
jq '.bindings[0]' "${ARTIFACT_DIR}/container_workspace_binding_lookup.json" >"${ARTIFACT_DIR}/container_binding.json"
remote_container_api_json "${PRIMARY_SSH}" "${CONTAINER_NAME}" "http://127.0.0.1:7781" GET "/v1/workspace/list?limit=500" "" "${ARTIFACT_DIR}/container_workspace_list.json" 30 || true

jq -n \
  --slurpfile primary "${ARTIFACT_DIR}/primary_binding.json" \
  --slurpfile container "${ARTIFACT_DIR}/container_binding.json" \
  --slurpfile owner "${ARTIFACT_DIR}/primary_runtime_owner.json" \
  --slurpfile containerWorkspaces "${ARTIFACT_DIR}/container_workspace_list.json" \
  --arg binding_id "${WORKSPACE_BINDING_ID}" \
  --arg source_workspace_id "${primary_source_workspace_id}" \
  --argjson source_workspace_generation "${primary_source_workspace_generation}" \
  --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
  --arg primary_swarm_id "${PRIMARY_SWARM_ID}" \
  --arg child_swarm_id "${CHILD_SWARM_ID}" \
  --arg runtime_workspace_path "${RUNTIME_WORKSPACE_PATH}" \
  '
  def assert($cond; $msg): if $cond then . else error($msg) end;
  def nonempty($v): (($v // "") | length) > 0;
  ($primary[0]) as $p |
  ($container[0]) as $c |
  ($owner[0].host_container // {}) as $hc |
  (($containerWorkspaces[0].workspaces // []) | map(select((.path // "") == $runtime_workspace_path)) | last // {}) as $localWorkspace |
  assert($p.binding_id == $binding_id; "primary binding_id mismatch") |
  assert($c.binding_id == $binding_id; "container binding_id mismatch") |
  assert($p.source_workspace_id == $source_workspace_id; "primary source_workspace_id does not match canonical workspace") |
  assert($p.source_workspace_generation == $source_workspace_generation; "primary source_workspace_generation does not match canonical workspace") |
  assert($p.source_workspace_path == $source_workspace_path; "primary source_workspace_path mismatch") |
  assert($p.destination_runtime_swarm_id == $child_swarm_id; "primary destination_runtime_swarm_id mismatch") |
  assert($p.destination_runtime_kind == "container"; "primary destination_runtime_kind is not container") |
  assert($p.destination_authority_host_swarm_id == $primary_swarm_id; "primary destination_authority_host_swarm_id mismatch") |
  assert(nonempty($p.destination_container_id); "primary destination_container_id is empty") |
  assert($p.destination_workspace_path == $runtime_workspace_path; "primary destination_workspace_path mismatch") |
  assert($p.destination_workspace_path != $p.source_workspace_path; "destination workspace path collapsed into source path") |
  assert(($p.placement_generation // 0) > 0; "primary placement_generation missing") |
  assert(($p.binding_generation // 0) > 0; "primary binding_generation missing") |
  assert($p.state == "bound"; "primary binding is not bound") |
  assert($p.access_mode == "read_write"; "primary binding is not read_write") |
  assert($p.writable == true; "primary binding is not writable") |
  assert($p.attested_by_host_swarm_id == $primary_swarm_id; "primary attested_by_host_swarm_id mismatch") |
  assert(($hc.host_container_id // "") == $p.destination_container_id; "runtime-owner host_container_id does not match binding destination_container_id") |
  assert(($hc.host_swarm_id // "") == $primary_swarm_id; "runtime-owner host_swarm_id does not match primary") |
  assert($c.source_workspace_id == $p.source_workspace_id; "container cached source_workspace_id diverges from primary binding") |
  assert($c.source_workspace_generation == $p.source_workspace_generation; "container cached source_workspace_generation diverges from primary binding") |
  assert($c.source_workspace_path == $p.source_workspace_path; "container cached source_workspace_path diverges from primary binding") |
  assert($c.destination_runtime_swarm_id == $p.destination_runtime_swarm_id; "container cached destination_runtime_swarm_id diverges") |
  assert($c.destination_runtime_kind == $p.destination_runtime_kind; "container cached destination_runtime_kind diverges") |
  assert($c.destination_authority_host_swarm_id == $p.destination_authority_host_swarm_id; "container cached destination_authority_host_swarm_id diverges") |
  assert($c.destination_container_id == $p.destination_container_id; "container cached destination_container_id diverges") |
  assert($c.destination_workspace_path == $p.destination_workspace_path; "container cached destination_workspace_path diverges") |
  assert($c.placement_generation == $p.placement_generation; "container cached placement_generation diverges") |
  assert($c.binding_generation == $p.binding_generation; "container cached binding_generation diverges") |
  assert($c.attested_by_host_swarm_id == $p.attested_by_host_swarm_id; "container cached attested_by_host_swarm_id diverges") |
  assert($c.state == "bound"; "container cached binding is not bound") |
  assert($c.access_mode == "read_write"; "container cached binding is not read_write") |
  assert($c.writable == true; "container cached binding is not writable") |
  assert((($localWorkspace.workspace_id // "") == "") or (($localWorkspace.workspace_id // "") != $c.source_workspace_id); "container local workspace id overwrote binding source_workspace_id") |
  {
    ok: true,
    primary_binding: $p,
    container_binding: $c,
    runtime_owner_host_container: $hc,
    container_local_runtime_workspace: $localWorkspace,
    checked: [
      "primary binding uses canonical source workspace id/generation/path",
      "primary binding has container destination runtime/container/path facts",
      "container binding cache exactly matches primary binding authority fields",
      "container local workspace id did not replace SourceWorkspaceID"
    ]
  }' >"${ARTIFACT_DIR}/binding_contract_validation.json"

jq -nc \
  --arg artifact_dir "${ARTIFACT_DIR}" \
  --arg primary_ssh "${PRIMARY_SSH}" \
  --arg primary_swarm_id "${PRIMARY_SWARM_ID}" \
  --arg source_workspace_path "${SOURCE_WORKSPACE_PATH}" \
  --arg source_workspace_id "${primary_source_workspace_id}" \
  --arg source_workspace_generation "${primary_source_workspace_generation}" \
  --arg deployment_id "${DEPLOYMENT_ID}" \
  --arg container_name "${CONTAINER_NAME}" \
  --arg child_swarm_id "${CHILD_SWARM_ID}" \
  --arg workspace_binding_id "${WORKSPACE_BINDING_ID}" \
  --arg runtime_workspace_path "${RUNTIME_WORKSPACE_PATH}" \
  '{ok:true,artifact_dir:$artifact_dir,primary_ssh:$primary_ssh,primary_swarm_id:$primary_swarm_id,source_workspace_path:$source_workspace_path,source_workspace_id:$source_workspace_id,source_workspace_generation:($source_workspace_generation|tonumber),deployment_id:$deployment_id,container_name:$container_name,child_swarm_id:$child_swarm_id,workspace_binding_id:$workspace_binding_id,runtime_workspace_path:$runtime_workspace_path,product_path:"/v1/swarm/replicate only; no session/open/route authority exercised",evidence:["inputs.json","primary_discovery.json","primary_workspace_list.json","replicate_request.json","replicate_response.json","primary_workspace_binding_lookup.json","primary_binding.json","primary_runtime_owner.json","container_discovery.json","container_workspace_binding_lookup.json","container_binding.json","container_workspace_list.json","binding_contract_validation.json","topology_before.json","topology_after_replicate.json","before.log","after_replicate.log","final.log"]}' \
  >"${ARTIFACT_DIR}/summary.json"

log "PASS local-container creation/binding E2E: ${ARTIFACT_DIR}/summary.json"
cat "${ARTIFACT_DIR}/summary.json"
