#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: ./tests/swarmd/managed_dev_update_binary_refresh_e2e.sh [options]

Verifies the managed dev update path actually refreshes the running swarmd
binary on both the primary host and a managed host. Run this on the primary
SSH testbench host after syncing the desired source commit there.

The test triggers the product update API, waits for primary and managed update
jobs to complete, then proves the running /proc/<pid>/exe Go VCS revision on
both hosts equals the source checkout HEAD. It also records whether
/usr/local/bin/swarmd exists and whether it is the live service binary.

Options:
  --primary-url <url>              Primary swarmd URL. Default: http://127.0.0.1:7781
  --primary-ssh-host <host>        SSH host for primary binary evidence when run off-host.
  --managed-swarm-id <id>          Managed host swarm_id. Optional if --managed-name is set.
  --managed-name <name>            Managed host name used to resolve swarm_id and as SSH host.
  --managed-ssh-host <host>        SSH host for managed-host binary evidence. Required.
  --source-workspace-path <path>   Source checkout path on both hosts. Required.
  --service-name <name>            systemd service name. Default: swarm.service
  --timeout-seconds <seconds>      Update wait timeout. Default: 1800
  --evidence-dir <path>            Evidence output directory. Default: mktemp under /tmp
  --allow-local-workstation-run    Allow non SwarmTarget1 hostname execution.
  --help                          Show help.

Environment shortcuts:
  SWARM_PRIMARY_URL, SWARM_PRIMARY_SSH_HOST, SWARM_MANAGED_SWARM_ID,
  SWARM_MANAGED_NAME, SWARM_MANAGED_SSH_HOST, SWARM_SOURCE_WORKSPACE_PATH,
  SWARM_UPDATE_VERIFY_EVIDENCE_DIR, SWARM_UPDATE_VERIFY_ALLOW_LOCAL
EOF
}

log() { printf '%s\n' "$*"; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "${1:-}" >/dev/null 2>&1 || fail "required command not found: ${1:-}"; }

require_testbench_execution() {
  if [[ "${ALLOW_LOCAL_WORKSTATION_RUN}" == "true" ]]; then
    return 0
  fi
  if [[ -n "${PRIMARY_SSH_HOST}" ]]; then
    return 0
  fi
  local host
  host="$(hostname -s 2>/dev/null || hostname 2>/dev/null || true)"
  case "${host}" in
    SwarmTarget1|SwarmTarget1-*) return 0 ;;
  esac
  fail "run this verification on the primary SSH testbench host, or pass --primary-ssh-host for off-host binary evidence"
}

init_evidence() {
  if [[ -z "${EVIDENCE_DIR}" ]]; then
    EVIDENCE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/swarm-update-binary-refresh-XXXXXX")"
  fi
  mkdir -p -- "${EVIDENCE_DIR}"
  COOKIE_FILE="${EVIDENCE_DIR}/primary.cookies"
  : >"${COOKIE_FILE}"
}

api_request_capture() {
  local method="${1:-GET}"
  local path="${2:-}"
  local body="${3:-}"
  local output_file="${4:-}"
  local max_time="${5:-30}"
  local response_file payload_file http_code
  response_file="$(mktemp)"
  payload_file=""
  local args=(
    -sS
    --connect-timeout 3
    --max-time "${max_time}"
    -o "${response_file}"
    -w '%{http_code}'
    -H 'Accept: application/json'
    -H "Origin: ${PRIMARY_URL%/}"
    -H "Referer: ${PRIMARY_URL%/}/"
    -H 'Sec-Fetch-Site: same-origin'
    -c "${COOKIE_FILE}"
    -b "${COOKIE_FILE}"
    -X "${method}"
  )
  if [[ -n "${body}" ]]; then
    payload_file="$(mktemp)"
    printf '%s' "${body}" >"${payload_file}"
    args+=(-H 'Content-Type: application/json' --data-binary "@${payload_file}")
  fi
  if http_code="$(curl "${args[@]}" "${PRIMARY_URL%/}${path}")"; then
    :
  else
    http_code="000"
  fi
  if [[ -n "${output_file}" ]]; then
    cp -- "${response_file}" "${output_file}"
  fi
  API_STATUS="${http_code}"
  API_BODY="$(cat -- "${response_file}")"
  rm -f -- "${response_file}"
  [[ -z "${payload_file}" ]] || rm -f -- "${payload_file}"
}

api_json() {
  local method="${1:-GET}"
  local path="${2:-}"
  local body="${3:-}"
  local output_file="${4:-}"
  local max_time="${5:-30}"
  api_request_capture "${method}" "${path}" "${body}" "${output_file}" "${max_time}"
  if [[ "${API_STATUS}" -lt 200 || "${API_STATUS}" -ge 300 ]]; then
    fail "API ${method} ${path} failed with HTTP ${API_STATUS}: ${API_BODY}"
  fi
}

start_desktop_session() {
  curl -fsS --connect-timeout 3 --max-time 20 \
    -c "${COOKIE_FILE}" \
    -b "${COOKIE_FILE}" \
    -H "Origin: ${PRIMARY_URL%/}" \
    -H "Referer: ${PRIMARY_URL%/}/" \
    -H 'Sec-Fetch-Site: same-origin' \
    "${PRIMARY_URL%/}/v1/auth/desktop/session" >/dev/null
}

resolve_managed_target() {
  if [[ -n "${MANAGED_SWARM_ID}" ]]; then
    return 0
  fi
  [[ -n "${MANAGED_NAME}" ]] || fail "--managed-swarm-id or --managed-name is required"
  api_json GET "/v1/swarm/targets" "" "${EVIDENCE_DIR}/swarm_targets.json" 30
  MANAGED_SWARM_ID="$(jq -r --arg name "${MANAGED_NAME}" '
    [.targets[]? | select((.name // "") == $name or (.swarm_id // "") == $name) | .swarm_id][0] // ""
  ' "${EVIDENCE_DIR}/swarm_targets.json")"
  [[ -n "${MANAGED_SWARM_ID}" && "${MANAGED_SWARM_ID}" != "null" ]] || fail "could not resolve managed target ${MANAGED_NAME}"
}

host_probe_script() {
  cat <<'EOF'
set -euo pipefail
service_name="${1:?service}"
source_path="${2:?source path}"
host_label="$(hostname -f 2>/dev/null || hostname 2>/dev/null || true)"
source_head=""
source_branch=""
source_clean=""
source_status=""
if [[ -d "${source_path}/.git" ]]; then
  source_head="$(git -C "${source_path}" rev-parse HEAD 2>/dev/null || true)"
  source_branch="$(git -C "${source_path}" branch --show-current 2>/dev/null || true)"
  if git -C "${source_path}" diff --quiet --ignore-submodules -- 2>/dev/null && git -C "${source_path}" diff --cached --quiet --ignore-submodules -- 2>/dev/null; then
    source_clean="true"
  else
    source_clean="false"
  fi
  source_status="$(git -C "${source_path}" status --short 2>/dev/null | sed ':a;N;$!ba;s/\n/; /g' || true)"
fi
launcher_pid="$(systemctl show -p MainPID --value "${service_name}" 2>/dev/null || true)"
pid=""
if [[ -n "${launcher_pid}" && "${launcher_pid}" != "0" ]]; then
  pid="$(pgrep -P "${launcher_pid}" -x swarmd 2>/dev/null | head -n1 || true)"
fi
if [[ -z "${pid}" ]]; then
  pid="$(pgrep -xo swarmd 2>/dev/null || true)"
fi
exe=""
exe_real=""
exe_sha=""
exe_mtime=""
exe_vcs_revision=""
exe_vcs_modified=""
if [[ -n "${pid}" && -e "/proc/${pid}/exe" ]]; then
  exe="/proc/${pid}/exe"
  exe_real="$(readlink -f "${exe}" 2>/dev/null || true)"
  exe_sha="$(sha256sum "${exe}" 2>/dev/null | awk '{print $1}' || true)"
  exe_mtime="$(stat -Lc '%Y' "${exe}" 2>/dev/null || true)"
  go_bin=""
  for candidate in "${source_path}/.tools/go/bin/go" "$(dirname "${source_path}")/.tools/go/bin/go" /usr/local/go/bin/go go; do
    if command -v "${candidate}" >/dev/null 2>&1; then
      go_bin="$(command -v "${candidate}")"
      break
    fi
    if [[ -x "${candidate}" ]]; then
      go_bin="${candidate}"
      break
    fi
  done
  if [[ -n "${go_bin}" ]]; then
    exe_vcs_revision="$(${go_bin} version -m "${exe}" 2>/dev/null | sed -n 's/^[[:space:]]*build[[:space:]]\+vcs\.revision=//p' | head -n1 || true)"
    exe_vcs_modified="$(${go_bin} version -m "${exe}" 2>/dev/null | sed -n 's/^[[:space:]]*build[[:space:]]\+vcs\.modified=//p' | head -n1 || true)"
  fi
fi
usr_path="/usr/local/bin/swarmd"
usr_exists="false"
usr_real=""
usr_sha=""
usr_mtime=""
usr_vcs_revision=""
usr_vcs_modified=""
if [[ -e "${usr_path}" ]]; then
  usr_exists="true"
  usr_real="$(readlink -f "${usr_path}" 2>/dev/null || true)"
  usr_sha="$(sha256sum "${usr_path}" 2>/dev/null | awk '{print $1}' || true)"
  usr_mtime="$(stat -Lc '%Y' "${usr_path}" 2>/dev/null || true)"
  if [[ -n "${go_bin:-}" ]]; then
    usr_vcs_revision="$(${go_bin} version -m "${usr_path}" 2>/dev/null | sed -n 's/^[[:space:]]*build[[:space:]]\+vcs\.revision=//p' | head -n1 || true)"
    usr_vcs_modified="$(${go_bin} version -m "${usr_path}" 2>/dev/null | sed -n 's/^[[:space:]]*build[[:space:]]\+vcs\.modified=//p' | head -n1 || true)"
  fi
fi
live_matches_usr="false"
if [[ -n "${exe_real}" && -n "${usr_real}" && "${exe_real}" == "${usr_real}" ]]; then
  live_matches_usr="true"
fi
jq -nc \
  --arg host "${host_label}" \
  --arg service "${service_name}" \
  --arg source_path "${source_path}" \
  --arg source_head "${source_head}" \
  --arg source_branch "${source_branch}" \
  --arg source_clean "${source_clean}" \
  --arg source_status "${source_status}" \
  --arg launcher_pid "${launcher_pid}" \
  --arg pid "${pid}" \
  --arg exe_real "${exe_real}" \
  --arg exe_sha "${exe_sha}" \
  --arg exe_mtime "${exe_mtime}" \
  --arg exe_vcs_revision "${exe_vcs_revision}" \
  --arg exe_vcs_modified "${exe_vcs_modified}" \
  --arg usr_path "${usr_path}" \
  --arg usr_exists "${usr_exists}" \
  --arg usr_real "${usr_real}" \
  --arg usr_sha "${usr_sha}" \
  --arg usr_mtime "${usr_mtime}" \
  --arg usr_vcs_revision "${usr_vcs_revision}" \
  --arg usr_vcs_modified "${usr_vcs_modified}" \
  --arg live_matches_usr "${live_matches_usr}" \
  '{host:$host,service:$service,source:{path:$source_path,branch:$source_branch,head:$source_head,clean:($source_clean == "true"),status_short:$source_status},running:{launcher_pid:$launcher_pid,pid:$pid,exe:$exe_real,sha256:$exe_sha,mtime_unix:($exe_mtime|tonumber? // 0),vcs_revision:$exe_vcs_revision,vcs_modified:$exe_vcs_modified},usr_local_bin_swarmd:{path:$usr_path,exists:($usr_exists == "true"),realpath:$usr_real,sha256:$usr_sha,mtime_unix:($usr_mtime|tonumber? // 0),vcs_revision:$usr_vcs_revision,vcs_modified:$usr_vcs_modified,live_service_binary:($live_matches_usr == "true")}}'
EOF
}

collect_primary_state() {
  local output_file="${1:?output}"
  if [[ -n "${PRIMARY_SSH_HOST}" ]]; then
    ssh -o BatchMode=yes -o ConnectTimeout=10 -- "${PRIMARY_SSH_HOST}" \
      "bash -s -- '${SERVICE_NAME}' '${SOURCE_WORKSPACE_PATH}'" < <(host_probe_script) >"${output_file}"
    return 0
  fi
  bash -s -- "${SERVICE_NAME}" "${SOURCE_WORKSPACE_PATH}" < <(host_probe_script) >"${output_file}"
}

collect_managed_state() {
  local output_file="${1:?output}"
  [[ -n "${MANAGED_SSH_HOST}" ]] || fail "--managed-ssh-host is required for managed-host binary evidence"
  ssh -o BatchMode=yes -o ConnectTimeout=10 -- "${MANAGED_SSH_HOST}" \
    "bash -s -- '${SERVICE_NAME}' '${SOURCE_WORKSPACE_PATH}'" < <(host_probe_script) >"${output_file}"
}

wait_primary_completed() {
  local deadline=$((SECONDS + TIMEOUT_SECONDS))
  while true; do
    api_request_capture GET "/v1/update/run" "" "${EVIDENCE_DIR}/primary_update_status_poll.json" 30
    if [[ "${API_STATUS}" -ge 200 && "${API_STATUS}" -lt 300 ]]; then
      cp -- "${EVIDENCE_DIR}/primary_update_status_poll.json" "${EVIDENCE_DIR}/primary_update_status.json"
      local status error
      status="$(jq -r '.job.status // .status // ""' "${EVIDENCE_DIR}/primary_update_status.json")"
      error="$(jq -r '.job.error // .error // ""' "${EVIDENCE_DIR}/primary_update_status.json")"
      if [[ "${status}" == "completed" ]]; then
        return 0
      fi
      if [[ "${status}" == "failed" || -n "${error}" ]]; then
        fail "primary update failed: ${error:-${status}}"
      fi
    fi
    [[ "${SECONDS}" -lt "${deadline}" ]] || fail "timed out waiting for primary update completion"
    sleep 5
  done
}

managed_status_request() {
  local output_file="${1:?output}"
  jq -nc --arg target_swarm_id "${MANAGED_SWARM_ID}" '{target_swarm_id:$target_swarm_id}' >"${EVIDENCE_DIR}/managed_update_status_request.json"
  api_request_capture POST "/v1/swarm/managed-hosts/update/status" "$(cat "${EVIDENCE_DIR}/managed_update_status_request.json")" "${output_file}" 30
}

wait_managed_completed() {
  local deadline=$((SECONDS + TIMEOUT_SECONDS))
  while true; do
    managed_status_request "${EVIDENCE_DIR}/managed_update_status_poll.json"
    if [[ "${API_STATUS}" -ge 200 && "${API_STATUS}" -lt 300 ]]; then
      cp -- "${EVIDENCE_DIR}/managed_update_status_poll.json" "${EVIDENCE_DIR}/managed_update_status.json"
      local status error
      status="$(jq -r '.job.status // .status // ""' "${EVIDENCE_DIR}/managed_update_status.json")"
      error="$(jq -r '.job.error // .error // ""' "${EVIDENCE_DIR}/managed_update_status.json")"
      if [[ "${status}" == "completed" ]]; then
        return 0
      fi
      if [[ "${status}" == "failed" || -n "${error}" ]]; then
        fail "managed update failed: ${error:-${status}}"
      fi
    fi
    [[ "${SECONDS}" -lt "${deadline}" ]] || fail "timed out waiting for managed update completion"
    sleep 5
  done
}

assert_refresh() {
  local host_label="${1:?host label}"
  local before_file="${2:?before}"
  local after_file="${3:?after}"
  local expected_head="${4:?expected head}"
  local before_rev after_rev before_mtime after_mtime
  before_rev="$(jq -r '.running.vcs_revision // ""' "${before_file}")"
  after_rev="$(jq -r '.running.vcs_revision // ""' "${after_file}")"
  before_mtime="$(jq -r '.running.mtime_unix // 0' "${before_file}")"
  after_mtime="$(jq -r '.running.mtime_unix // 0' "${after_file}")"
  [[ -n "${after_rev}" ]] || fail "${host_label}: running swarmd VCS revision is empty after update"
  [[ "${after_rev}" == "${expected_head}" ]] || fail "${host_label}: running swarmd VCS revision ${after_rev} != source HEAD ${expected_head}"
  if [[ "${before_rev}" == "${expected_head}" ]]; then
    [[ "${after_mtime}" -gt "${before_mtime}" ]] || fail "${host_label}: revision was already current, but running binary mtime did not increase (${before_mtime} -> ${after_mtime})"
  fi
}

PRIMARY_URL="${SWARM_PRIMARY_URL:-http://127.0.0.1:7781}"
PRIMARY_SSH_HOST="${SWARM_PRIMARY_SSH_HOST:-}"
MANAGED_SWARM_ID="${SWARM_MANAGED_SWARM_ID:-}"
MANAGED_NAME="${SWARM_MANAGED_NAME:-}"
MANAGED_SSH_HOST="${SWARM_MANAGED_SSH_HOST:-}"
SOURCE_WORKSPACE_PATH="${SWARM_SOURCE_WORKSPACE_PATH:-}"
SERVICE_NAME="swarm.service"
TIMEOUT_SECONDS="1800"
EVIDENCE_DIR="${SWARM_UPDATE_VERIFY_EVIDENCE_DIR:-}"
ALLOW_LOCAL_WORKSTATION_RUN="${SWARM_UPDATE_VERIFY_ALLOW_LOCAL:-false}"
COOKIE_FILE=""
API_STATUS=""
API_BODY=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --primary-url) PRIMARY_URL="${2:-}"; shift 2 ;;
    --primary-ssh-host) PRIMARY_SSH_HOST="${2:-}"; shift 2 ;;
    --managed-swarm-id) MANAGED_SWARM_ID="${2:-}"; shift 2 ;;
    --managed-name) MANAGED_NAME="${2:-}"; shift 2 ;;
    --managed-ssh-host) MANAGED_SSH_HOST="${2:-}"; shift 2 ;;
    --source-workspace-path) SOURCE_WORKSPACE_PATH="${2:-}"; shift 2 ;;
    --service-name) SERVICE_NAME="${2:-}"; shift 2 ;;
    --timeout-seconds) TIMEOUT_SECONDS="${2:-}"; shift 2 ;;
    --evidence-dir) EVIDENCE_DIR="${2:-}"; shift 2 ;;
    --allow-local-workstation-run) ALLOW_LOCAL_WORKSTATION_RUN="true"; shift ;;
    --help|-h) usage; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done

PRIMARY_URL="${PRIMARY_URL%/}"
[[ -n "${SOURCE_WORKSPACE_PATH}" ]] || fail "--source-workspace-path is required"
[[ "${TIMEOUT_SECONDS}" =~ ^[0-9]+$ ]] || fail "--timeout-seconds must be numeric"
require_testbench_execution
require_command curl
require_command jq
require_command git
require_command systemctl
require_command ssh
init_evidence
start_desktop_session
resolve_managed_target
collect_primary_state "${EVIDENCE_DIR}/primary_before.json"
collect_managed_state "${EVIDENCE_DIR}/managed_before.json"
EXPECTED_HEAD="$(jq -r '.source.head // ""' "${EVIDENCE_DIR}/primary_before.json")"
[[ -n "${EXPECTED_HEAD}" && "${EXPECTED_HEAD}" != "null" ]] || fail "could not determine primary source HEAD"
MANAGED_SOURCE_HEAD="$(jq -r '.source.head // ""' "${EVIDENCE_DIR}/managed_before.json")"
if [[ -n "${MANAGED_SOURCE_HEAD}" && "${MANAGED_SOURCE_HEAD}" != "null" && "${MANAGED_SOURCE_HEAD}" != "${EXPECTED_HEAD}" ]]; then
  log "managed source starts at ${MANAGED_SOURCE_HEAD}; update must sync it to ${EXPECTED_HEAD}"
fi

jq -nc --arg expected_head "${EXPECTED_HEAD}" --arg managed_swarm_id "${MANAGED_SWARM_ID}" --arg managed_ssh_host "${MANAGED_SSH_HOST}" '{expected_head:$expected_head,managed_swarm_id:$managed_swarm_id,managed_ssh_host:$managed_ssh_host}' >"${EVIDENCE_DIR}/update_request_summary.json"
api_json POST "/v1/update/run" "{}" "${EVIDENCE_DIR}/primary_update_run_response.json" 30
wait_primary_completed
wait_managed_completed

collect_primary_state "${EVIDENCE_DIR}/primary_after.json"
collect_managed_state "${EVIDENCE_DIR}/managed_after.json"

assert_refresh "primary" "${EVIDENCE_DIR}/primary_before.json" "${EVIDENCE_DIR}/primary_after.json" "${EXPECTED_HEAD}"
assert_refresh "managed" "${EVIDENCE_DIR}/managed_before.json" "${EVIDENCE_DIR}/managed_after.json" "${EXPECTED_HEAD}"

jq -n \
  --slurpfile primary_before "${EVIDENCE_DIR}/primary_before.json" \
  --slurpfile managed_before "${EVIDENCE_DIR}/managed_before.json" \
  --slurpfile primary_after "${EVIDENCE_DIR}/primary_after.json" \
  --slurpfile managed_after "${EVIDENCE_DIR}/managed_after.json" \
  --slurpfile primary_update "${EVIDENCE_DIR}/primary_update_status.json" \
  --slurpfile managed_update "${EVIDENCE_DIR}/managed_update_status.json" \
  --arg expected_head "${EXPECTED_HEAD}" \
  '{ok:true,proof_token:"UPDATE_BINARY_REFRESH_OK",expected_head:$expected_head,primary:{before:$primary_before[0],after:$primary_after[0],update:$primary_update[0]},managed:{before:$managed_before[0],after:$managed_after[0],update:$managed_update[0]}}' \
  >"${EVIDENCE_DIR}/update_binary_refresh_summary.json"

log "UPDATE_BINARY_REFRESH_OK"
log "evidence: ${EVIDENCE_DIR}"
log "summary: ${EVIDENCE_DIR}/update_binary_refresh_summary.json"
