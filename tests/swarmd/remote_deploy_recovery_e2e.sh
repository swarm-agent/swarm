#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"

usage() {
  cat <<'EOF'
Usage: ./tests/swarmd/remote_deploy_recovery_e2e.sh [options]

Run remote recovery checks on top of the real SSH + Tailscale remote deploy path.

If --host-root is not supplied, this runner first boots a fresh remote child by
calling tests/swarmd/remote_deploy_e2e.sh and then reuses that exact host root.

This runner creates one real routed session on the host and then drives:
  - RR-01 child restart, host still running
  - RR-02 host restart, child still running
  - RR-03 both down, host first
  - RR-04 both down, child first

Each scenario verifies:
  - the host still serves the routed session and messages locally
  - the remote deploy record still points at the same child swarm
  - the remote child reaches attached/ready again when expected
  - a follow-up routed write or AI/permission proof succeeds after recovery

Options:
  --scenario <rr-01|rr-02|rr-03|rr-04|all>   Scenario to run. Default: all
  --host-root <path>                          Reuse an existing isolated host root
  --remote-session-id <id>                    Reuse a specific remote deploy session from the host root
  --ssh-target <target>                       SSH alias/host for bootstrap. Required when no --host-root is supplied.
  --remote-runtime <docker|podman>            Remote runtime for bootstrap. Default: docker
  --workspace-path <path>                     Source workspace path. Default: repo root
  --workspace-name <name>                     Workspace display name. Default: basename of workspace path
  --group-id <id>                             Existing target group id
  --group-name <name>                         Existing target group name, or name to create
  --host-root-name <name>                     Host swarm name for bootstrap. Default: Remote Recovery Test Host
  --host-backend-port <port>                  Host backend/API port. Default: 17781
  --host-desktop-port <port>                  Host desktop port. Default: 15555
  --host-peer-port <port>                     Host peer transport port. Default: 17791
  --host-tailscale-url <url>                  Override host tailscale callback URL
  --manage-tailscale-serve <true|false>       Manage `tailscale serve` during bootstrap. Default: true
  --tailscale-auth-mode <manual|key>          Remote child auth mode for bootstrap. Default: manual
  --tailscale-auth-key-env <name>             Env var containing the launch-only Tailscale auth key
  --skip-host-rebuild                         Reuse the current host binaries during bootstrap
  --poll-timeout <seconds>                    Restart/reconnect timeout. Default: 180
  --poll-interval <seconds>                   Poll interval. Default: 3
  --remote-start-timeout <seconds>            Timeout for remote session start during bootstrap. Default: 600
  --prove-routed-ai                           After each recovery, run a real routed bash approval proof
  --proof-provider <id>                       Provider for routed AI proof. Default: fireworks
  --proof-model <id>                          Model for routed AI proof. Default: accounts/fireworks/models/kimi-k2p5
  --proof-thinking <low|medium|high>          Thinking level for routed AI proof. Default: high
  --proof-provider-key-env <name>             Env var containing the provider API key for child seeding
  --proof-bash-command <cmd>                  Command prefix for bash approval proof. Default: pwd
  --log-tail <lines>                          Remote log tail lines to retain. Default: 200
  --help                                      Show this help text

Artifacts:
  Results are written under:
    <host-root>/remote-recovery-artifacts/<timestamp>/
EOF
}

log() {
  printf '%s\n' "$*"
}

fail() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

require_command() {
  local name="${1:-}"
  command -v "${name}" >/dev/null 2>&1 || fail "required command not found: ${name}"
}

trim() {
  local value="${1-}"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "${value}"
}

config_value() {
  local path="${1:-}"
  local key="${2:-}"
  [[ -f "${path}" ]] || return 1
  awk -F'=' -v target="${key}" '
    {
      key=$1
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", key)
      if (key != target) {
        next
      }
      value=$2
      sub(/^[[:space:]]+/, "", value)
      sub(/[[:space:]]+$/, "", value)
      print value
      exit
    }
  ' "${path}"
}

write_artifact() {
  local name="${1:-}"
  local content="${2-}"
  printf '%s' "${content}" >"${ARTIFACT_DIR}/${name}"
}

curl_http_code() {
  local url="${1:-}"
  local body_file
  body_file="$(mktemp)"
  local http_code
  if http_code="$(curl -sS --connect-timeout 2 --max-time 8 -o "${body_file}" -w '%{http_code}' "${url}" 2>/dev/null)"; then
    :
  else
    http_code="000"
  fi
  rm -f -- "${body_file}"
  printf '%s' "${http_code}"
}

host_ready() {
  [[ "$(curl_http_code "${HOST_ADMIN_API_URL%/}/readyz")" == "200" ]]
}

ensure_tailscale_serve_for_host() {
  [[ "${MANAGE_TAILSCALE_SERVE}" == "true" ]] || return 0
  [[ -n "${HOST_TAILSCALE_URL}" ]] || fail "host tailscale url is required to manage tailscale serve"
  local serve_json serve_host current_proxy want_proxy
  serve_json="$(tailscale serve status --json 2>/dev/null || printf '{}')"
  serve_host="${HOST_TAILSCALE_URL#https://}"
  serve_host="${serve_host#http://}"
  serve_host="${serve_host%/}"
  want_proxy="${HOST_ADMIN_API_URL}"
  current_proxy="$(printf '%s' "${serve_json}" | jq -r --arg host "${serve_host}:443" '.Web[$host].Handlers["/"].Proxy // empty')"
  if [[ "${current_proxy}" == "${want_proxy}" ]]; then
    return 0
  fi
  tailscale serve reset >/dev/null 2>&1 || true
  tailscale serve --bg "${want_proxy}" >/dev/null
}

wait_for_host_ready() {
  local start_ts
  start_ts="$(date +%s)"
  while :; do
    if host_ready; then
      return 0
    fi
    if (( "$(date +%s)" - start_ts >= POLL_TIMEOUT_SECONDS )); then
      return 1
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

wait_for_host_down() {
  local start_ts
  start_ts="$(date +%s)"
  while :; do
    if ! host_ready; then
      return 0
    fi
    if (( "$(date +%s)" - start_ts >= POLL_TIMEOUT_SECONDS )); then
      return 1
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

api_request() {
  local method="${1:-GET}"
  local path="${2:-}"
  local body="${3:-}"
  local max_time_seconds="${4:-60}"
  local url="${HOST_ADMIN_API_URL%/}${path}"
  local body_file
  body_file="$(mktemp)"
  local request_body_file=""
  local http_code
  local args=(
    -sS
    --connect-timeout 3
    --max-time "${max_time_seconds}"
    -o "${body_file}"
    -w '%{http_code}'
    -H "Authorization: Bearer ${ATTACH_TOKEN}"
    -H 'Accept: application/json'
    -X "${method}"
  )
  if [[ -n "${body}" ]]; then
    request_body_file="$(mktemp)"
    printf '%s' "${body}" >"${request_body_file}"
    args+=(-H 'Content-Type: application/json' --data-binary "@${request_body_file}")
  fi
  if http_code="$(curl "${args[@]}" "${url}")"; then
    :
  else
    http_code="000"
  fi
  local response_body
  response_body="$(cat -- "${body_file}")"
  rm -f -- "${body_file}"
  if [[ -n "${request_body_file}" ]]; then
    rm -f -- "${request_body_file}"
  fi
  if [[ "${http_code}" != 2* ]]; then
    printf 'request failed method=%s url=%s status=%s body=%s\n' "${method}" "${url}" "${http_code}" "${response_body}" >&2
    return 1
  fi
  printf '%s' "${response_body}"
}

api_get() {
  api_request GET "$1"
}

api_post() {
  api_request POST "$1" "${2:-}"
}

fetch_attach_token() {
  local response
  response="$(curl -fsS \
    -H "Origin: ${HOST_ADMIN_API_URL%/}" \
    -H "Referer: ${HOST_ADMIN_API_URL%/}/" \
    -H 'Sec-Fetch-Site: same-origin' \
    "${HOST_ADMIN_API_URL%/}/v1/auth/attach/token")" || return 1
  ATTACH_TOKEN="$(printf '%s' "${response}" | jq -r '.token // empty')"
  [[ -n "${ATTACH_TOKEN}" ]]
}

fetch_attach_token_for_base() {
  local base_url="${1:-}"
  local response
  response="$(curl -fsS \
    -H "Origin: ${base_url%/}" \
    -H "Referer: ${base_url%/}/" \
    -H 'Sec-Fetch-Site: same-origin' \
    "${base_url%/}/v1/auth/attach/token")"
  printf '%s' "${response}" | jq -r '.token // empty'
}

json_request_with_bearer() {
  local base_url="${1:-}"
  local token="${2:-}"
  local method="${3:-GET}"
  local path="${4:-}"
  local body="${5:-}"
  local max_time_seconds="${6:-60}"
  local url="${base_url%/}${path}"
  local body_file
  body_file="$(mktemp)"
  local request_body_file=""
  local http_code
  local args=(
    -sS
    --connect-timeout 3
    --max-time "${max_time_seconds}"
    -o "${body_file}"
    -w '%{http_code}'
    -H "Authorization: Bearer ${token}"
    -H 'Accept: application/json'
    -X "${method}"
  )
  if [[ -n "${body}" ]]; then
    request_body_file="$(mktemp)"
    printf '%s' "${body}" >"${request_body_file}"
    args+=(-H 'Content-Type: application/json' --data-binary "@${request_body_file}")
  fi
  if http_code="$(curl "${args[@]}" "${url}")"; then
    :
  else
    http_code="000"
  fi
  local response_body
  response_body="$(cat -- "${body_file}")"
  rm -f -- "${body_file}"
  if [[ -n "${request_body_file}" ]]; then
    rm -f -- "${request_body_file}"
  fi
  if [[ "${http_code}" != 2* ]]; then
    fail "${method} ${url} failed with status ${http_code}: ${response_body}"
  fi
  printf '%s' "${response_body}"
}

ensure_host_running() {
  if host_ready; then
    fetch_attach_token
    return 0
  fi
  log "Starting isolated host at ${HOST_ADMIN_API_URL}"
  if [[ -x "${HOST_START_SCRIPT}" ]]; then
    "${HOST_START_SCRIPT}" >/dev/null
  else
    fail "missing host start script: ${HOST_START_SCRIPT}"
  fi
  wait_for_host_ready || return 1
  fetch_attach_token
}

stop_host() {
  log "Stopping isolated host at ${HOST_ADMIN_API_URL}"
  if [[ -x "${HOST_STOP_SCRIPT}" ]]; then
    "${HOST_STOP_SCRIPT}" >/dev/null 2>&1 || true
  fi
  wait_for_host_down
}

session_json_by_id() {
  local remote_sessions_json="${1:-}"
  local session_id="${2:-}"
  printf '%s' "${remote_sessions_json}" | jq -c --arg session_id "${session_id}" '.sessions[]? | select(.id == $session_id)' | head -n 1
}

current_remote_session_json() {
  local sessions_json session_json
  sessions_json="$(api_get '/v1/deploy/remote/session')" || return 1
  LAST_REMOTE_SESSIONS_JSON="${sessions_json}"
  session_json="$(session_json_by_id "${sessions_json}" "${REMOTE_DEPLOY_SESSION_ID}")"
  [[ -n "${session_json}" ]] || return 1
  LAST_REMOTE_SESSION_JSON="${session_json}"
  printf '%s' "${session_json}"
}

remote_child_ready() {
  local tailnet_url="${1:-}"
  [[ -n "${tailnet_url}" ]] || return 1
  [[ "$(curl_http_code "${tailnet_url%/}/readyz")" == "200" ]] && return 0
  [[ "$(curl_http_code "${tailnet_url%/}/healthz")" == "200" ]]
}

wait_for_remote_attached() {
  local start_ts
  start_ts="$(date +%s)"
  while :; do
    local remote_session_json status child_swarm_id tailnet_url enrollment_id
    remote_session_json="$(current_remote_session_json)" || true
    if [[ -n "${remote_session_json}" ]]; then
      status="$(printf '%s' "${remote_session_json}" | jq -r '.status // empty')"
      child_swarm_id="$(printf '%s' "${remote_session_json}" | jq -r '.child_swarm_id // empty')"
      tailnet_url="$(printf '%s' "${remote_session_json}" | jq -r '.remote_tailnet_url // empty')"
      enrollment_id="$(printf '%s' "${remote_session_json}" | jq -r '.enrollment_id // empty')"
      if [[ "${status}" == "attached" && "${child_swarm_id}" == "${CHILD_SWARM_ID}" && -n "${tailnet_url}" ]]; then
        REMOTE_TAILNET_URL="${tailnet_url}"
        if remote_child_ready "${tailnet_url}"; then
          return 0
        fi
      fi
      if [[ "${status}" == "waiting_for_approval" && -n "${enrollment_id}" ]]; then
        log "Approving remote session ${REMOTE_DEPLOY_SESSION_ID} enrollment ${enrollment_id}"
        api_post "/v1/deploy/remote/session/${REMOTE_DEPLOY_SESSION_ID}/approve" >/dev/null || return 1
      fi
    fi
    if (( "$(date +%s)" - start_ts >= POLL_TIMEOUT_SECONDS )); then
      return 1
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

wait_for_remote_down() {
  local start_ts
  start_ts="$(date +%s)"
  while :; do
    local tailnet_url="${REMOTE_TAILNET_URL}"
    if [[ -z "${tailnet_url}" ]]; then
      current_remote_session_json >/dev/null 2>&1 || true
      tailnet_url="$(printf '%s' "${LAST_REMOTE_SESSION_JSON:-}" | jq -r '.remote_tailnet_url // empty' 2>/dev/null || true)"
    fi
    if [[ -n "${tailnet_url}" ]] && ! remote_child_ready "${tailnet_url}"; then
      return 0
    fi
    if (( "$(date +%s)" - start_ts >= POLL_TIMEOUT_SECONDS )); then
      return 1
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

remote_ssh() {
  local cmd="${1:-}"
  ssh "${REMOTE_SSH_TARGET}" "bash -lc $(printf '%q' "${cmd}")"
}

remote_child_stop() {
  local cmd
  if [[ -n "${REMOTE_SYSTEMD_UNIT}" ]]; then
    cmd="$(cat <<EOF
set -euo pipefail
${REMOTE_SUDO_PREFIX}systemctl stop ${REMOTE_SYSTEMD_UNIT}
EOF
)"
  else
    cmd="$(cat <<EOF
set -euo pipefail
if [ "${REMOTE_RUNTIME}" = "podman" ]; then
  podman stop swarm-remote-child >/dev/null
else
  ${REMOTE_SUDO_PREFIX}docker stop swarm-remote-child >/dev/null
fi
EOF
)"
  fi
  remote_ssh "${cmd}" >/dev/null
}

remote_child_start() {
  local cmd
  if [[ -n "${REMOTE_SYSTEMD_UNIT}" ]]; then
    cmd="$(cat <<EOF
set -euo pipefail
${REMOTE_SUDO_PREFIX}systemctl start ${REMOTE_SYSTEMD_UNIT}
EOF
)"
  else
    cmd="$(cat <<EOF
set -euo pipefail
if [ "${REMOTE_RUNTIME}" = "podman" ]; then
  podman start swarm-remote-child >/dev/null
else
  ${REMOTE_SUDO_PREFIX}docker start swarm-remote-child >/dev/null
fi
EOF
)"
  fi
  remote_ssh "${cmd}" >/dev/null
}

capture_remote_logs() {
  local label="${1:-}"
  local cmd
  cmd="$(cat <<EOF
set -euo pipefail
if [ "${REMOTE_RUNTIME}" = "podman" ]; then
  podman logs --tail ${LOG_TAIL} swarm-remote-child 2>&1 || true
else
  ${REMOTE_SUDO_PREFIX}docker logs --tail ${LOG_TAIL} swarm-remote-child 2>&1 || true
fi
EOF
)"
  remote_ssh "${cmd}" >"${ARTIFACT_DIR}/${label}-remote-child-log-tail.txt" 2>&1 || true
}

create_routed_session() {
  local payload response
  if [[ "${PROVE_ROUTED_AI}" == "true" ]]; then
    payload="$(jq -nc \
      --arg title "remote-recovery-${RUN_ID}" \
      --arg workspace_path "${SOURCE_WORKSPACE_PATH}" \
      --arg host_workspace_path "${SOURCE_WORKSPACE_PATH}" \
      --arg runtime_workspace_path "${RUNTIME_WORKSPACE_PATH}" \
      --arg workspace_name "${WORKSPACE_NAME}" \
      --arg provider "${PROOF_PROVIDER}" \
      --arg model "${PROOF_MODEL}" \
      --arg thinking "${PROOF_THINKING}" \
      '{title:$title,workspace_path:$workspace_path,host_workspace_path:$host_workspace_path,runtime_workspace_path:$runtime_workspace_path,workspace_name:$workspace_name,mode:"auto",preference:{provider:$provider,model:$model,thinking:$thinking}}')"
  else
    payload="$(jq -nc \
      --arg title "remote-recovery-${RUN_ID}" \
      --arg workspace_path "${SOURCE_WORKSPACE_PATH}" \
      --arg host_workspace_path "${SOURCE_WORKSPACE_PATH}" \
      --arg runtime_workspace_path "${RUNTIME_WORKSPACE_PATH}" \
      --arg workspace_name "${WORKSPACE_NAME}" \
      '{title:$title,workspace_path:$workspace_path,host_workspace_path:$host_workspace_path,runtime_workspace_path:$runtime_workspace_path,workspace_name:$workspace_name,mode:"auto"}')"
  fi
  response="$(api_post "/v1/sessions?swarm_id=${CHILD_SWARM_ID}" "${payload}")" || return 1
  write_artifact "session-create-response.json" "${response}"
  SESSION_ID="$(printf '%s' "${response}" | jq -r '.session.id // empty')"
  [[ -n "${SESSION_ID}" ]]
}

append_message_and_verify() {
  local label="${1:-}"
  local content="${2:-}"
  local payload response messages_json
  payload="$(jq -nc --arg role "user" --arg content "${content}" '{role:$role,content:$content}')"
  response="$(api_post "/v1/sessions/${SESSION_ID}/messages" "${payload}")" || return 1
  write_artifact "${label}-message-post.json" "${response}"
  messages_json="$(api_get "/v1/sessions/${SESSION_ID}/messages?limit=200")" || return 1
  write_artifact "${label}-messages.json" "${messages_json}"
  local found
  found="$(printf '%s' "${messages_json}" | jq -r --arg content "${content}" '[.messages[]? | select(.content == $content)] | length')"
  [[ "${found}" != "0" ]]
}

seed_remote_child_provider_key() {
  local child_base_url="${1:-}"
  local provider="${2:-}"
  local api_key="${3:-}"
  local child_token payload response
  [[ -n "${child_base_url}" ]] || fail "child base url is required for provider seeding"
  [[ -n "${provider}" ]] || fail "provider is required for provider seeding"
  [[ -n "${api_key}" ]] || fail "provider api key is required for provider seeding"
  child_token="$(fetch_attach_token_for_base "${child_base_url}")"
  [[ -n "${child_token}" ]] || fail "failed to fetch child attach token from ${child_base_url}"
  payload="$(jq -nc --arg provider "${provider}" --arg api_key "${api_key}" '{provider:$provider,type:"api",api_key:$api_key,active:true}')"
  response="$(json_request_with_bearer "${child_base_url}" "${child_token}" POST '/v1/auth/credentials' "${payload}" 60)"
  printf '%s' "${response}"
}

start_routed_session_run() {
  local session_id="${1:-}"
  local prompt="${2:-}"
  local body response
  body="$(jq -nc --arg prompt "${prompt}" '{type:"run.start",prompt:$prompt,background:true}')"
  local url="${HOST_ADMIN_API_URL%/}/v1/sessions/${session_id}/run/stream"
  local start_ts
  start_ts="$(date +%s)"
  while :; do
    local body_file request_body_file http_code response_body
    body_file="$(mktemp)"
    request_body_file="$(mktemp)"
    printf '%s' "${body}" >"${request_body_file}"
    if http_code="$(curl \
      -sS \
      --connect-timeout 3 \
      --max-time 30 \
      -o "${body_file}" \
      -w '%{http_code}' \
      -H "Authorization: Bearer ${ATTACH_TOKEN}" \
      -H 'Accept: application/json' \
      -H 'Content-Type: application/json' \
      --data-binary "@${request_body_file}" \
      -X POST \
      "${url}")"; then
      :
    else
      http_code="000"
    fi
    response_body="$(cat -- "${body_file}")"
    rm -f -- "${body_file}" "${request_body_file}"
    if [[ "${http_code}" == 2* ]]; then
      printf '%s' "${response_body}"
      return 0
    fi
    if [[ "${http_code}" != "000" && "${http_code}" != "502" && "${http_code}" != "503" ]]; then
      fail "POST ${url} failed with status ${http_code}: ${response_body}"
    fi
    if (( "$(date +%s)" - start_ts >= POLL_TIMEOUT_SECONDS )); then
      fail "timed out starting routed run for session ${session_id}: status=${http_code} body=${response_body}"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

wait_for_pending_permission() {
  local session_id="${1:-}"
  local tool_name="${2:-}"
  local start_ts permission_json match
  start_ts="$(date +%s)"
  while :; do
    permission_json="$(api_get "/v1/sessions/${session_id}/permissions?limit=200")"
    match="$(printf '%s' "${permission_json}" | jq -c --arg tool_name "${tool_name}" '[.permissions[]? | select((.status // "") == "pending" and (.tool_name // "") == $tool_name)] | sort_by(.created_at // 0) | last // empty')"
    if [[ -n "${match}" && "${match}" != "null" ]]; then
      printf '%s' "${match}"
      return 0
    fi
    if (( "$(date +%s)" - start_ts >= POLL_TIMEOUT_SECONDS )); then
      fail "timed out waiting for pending permission ${tool_name} on session ${session_id}"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

resolve_session_permission() {
  local session_id="${1:-}"
  local permission_id="${2:-}"
  local action="${3:-approve}"
  local reason="${4:-ok}"
  local payload response
  payload="$(jq -nc --arg action "${action}" --arg reason "${reason}" '{action:$action,reason:$reason}')"
  response="$(api_post "/v1/sessions/${session_id}/permissions/${permission_id}/resolve" "${payload}")"
  printf '%s' "${response}"
}

wait_for_message_content() {
  local session_id="${1:-}"
  local want_content="${2:-}"
  local start_ts messages_json
  start_ts="$(date +%s)"
  while :; do
    messages_json="$(api_get "/v1/sessions/${session_id}/messages?limit=200")"
    if printf '%s' "${messages_json}" | jq -e --arg want "${want_content}" '.messages[]? | select(((.content // "") | contains($want)))' >/dev/null 2>&1; then
      printf '%s' "${messages_json}"
      return 0
    fi
    if (( "$(date +%s)" - start_ts >= POLL_TIMEOUT_SECONDS )); then
      fail "timed out waiting for session ${session_id} message ${want_content}"
    fi
    sleep "${POLL_INTERVAL_SECONDS}"
  done
}

verify_session_state() {
  local label="${1:-}"
  local session_json messages_json permissions_json sessions_json child_meta found_seed session_list_found
  session_json="$(api_get "/v1/sessions/${SESSION_ID}")" || return 1
  messages_json="$(api_get "/v1/sessions/${SESSION_ID}/messages?limit=200")" || return 1
  permissions_json="$(api_get "/v1/sessions/${SESSION_ID}/permissions?limit=50")" || return 1
  sessions_json="$(api_get "/v1/sessions?limit=200&swarm_id=${CHILD_SWARM_ID}")" || return 1
  write_artifact "${label}-session.json" "${session_json}"
  write_artifact "${label}-session-messages.json" "${messages_json}"
  write_artifact "${label}-session-permissions.json" "${permissions_json}"
  write_artifact "${label}-sessions-list.json" "${sessions_json}"
  child_meta="$(printf '%s' "${session_json}" | jq -r '.session.metadata.swarm_routed_child_swarm_id // empty')"
  [[ "${child_meta}" == "${CHILD_SWARM_ID}" ]] || return 1
  found_seed="$(printf '%s' "${messages_json}" | jq -r --arg content "${SEED_MESSAGE_CONTENT}" '[.messages[]? | select(.content == $content)] | length')"
  [[ "${found_seed}" != "0" ]] || return 1
  session_list_found="$(printf '%s' "${sessions_json}" | jq -r --arg session_id "${SESSION_ID}" '[.sessions[]? | select(.id == $session_id)] | length')"
  [[ "${session_list_found}" != "0" ]]
}

verify_remote_session_state() {
  local label="${1:-}"
  local remote_session_json status child_swarm_id
  remote_session_json="$(current_remote_session_json)" || return 1
  write_artifact "${label}-remote-session.json" "${remote_session_json}"
  status="$(printf '%s' "${remote_session_json}" | jq -r '.status // empty')"
  child_swarm_id="$(printf '%s' "${remote_session_json}" | jq -r '.child_swarm_id // empty')"
  [[ "${status}" == "attached" ]] || return 1
  [[ "${child_swarm_id}" == "${CHILD_SWARM_ID}" ]]
}

run_follow_up_check() {
  local label="${1:-}"
  local marker="${2:-}"
  if [[ "${PROVE_ROUTED_AI}" != "true" ]]; then
    append_message_and_verify "${label}" "${marker}" || return 1
    return 0
  fi
  local prompt start_json pending_json permission_id
  prompt="Run exactly this bash command and return only its output: ${PROOF_BASH_COMMAND} && echo ${marker}"
  start_json="$(start_routed_session_run "${SESSION_ID}" "${prompt}")" || return 1
  write_artifact "${label}-run-start.json" "${start_json}"
  pending_json="$(wait_for_pending_permission "${SESSION_ID}" "bash")" || return 1
  write_artifact "${label}-permission-bash-pending.json" "${pending_json}"
  permission_id="$(printf '%s' "${pending_json}" | jq -r '.id // empty')"
  [[ -n "${permission_id}" ]] || return 1
  write_artifact "${label}-permission-bash-resolve.json" "$(resolve_session_permission "${SESSION_ID}" "${permission_id}" "approve" "ok")"
  write_artifact "${label}-message-bash-output.json" "$(wait_for_message_content "${SESSION_ID}" "${marker}")"
  write_artifact "${label}-message-bash-workspace.json" "$(wait_for_message_content "${SESSION_ID}" "/workspaces/${WORKSPACE_NAME}")"
}

scenario_rr01() {
  verify_session_state "rr-01-before" || return 1
  verify_remote_session_state "rr-01-before-remote" || return 1
  remote_child_stop || return 1
  wait_for_remote_down || return 1
  capture_remote_logs "rr-01-after-stop"
  remote_child_start || return 1
  wait_for_remote_attached || return 1
  verify_session_state "rr-01-after" || return 1
  verify_remote_session_state "rr-01-after-remote" || return 1
  run_follow_up_check "rr-01-follow-up" "rr-01-${RUN_ID}" || return 1
  SCENARIO_NOTE="remote child restarted under the running host, reattached on the same child swarm, and accepted a follow-up routed proof"
}

scenario_rr02() {
  verify_session_state "rr-02-before" || return 1
  verify_remote_session_state "rr-02-before-remote" || return 1
  stop_host || return 1
  ensure_host_running || return 1
  wait_for_remote_attached || return 1
  verify_session_state "rr-02-after" || return 1
  verify_remote_session_state "rr-02-after-remote" || return 1
  run_follow_up_check "rr-02-follow-up" "rr-02-${RUN_ID}" || return 1
  SCENARIO_NOTE="host restarted on the same root while the remote child stayed up, and the routed session recovered without changing child ownership"
}

scenario_rr03() {
  verify_session_state "rr-03-before" || return 1
  verify_remote_session_state "rr-03-before-remote" || return 1
  remote_child_stop || return 1
  wait_for_remote_down || return 1
  stop_host || return 1
  ensure_host_running || return 1
  verify_session_state "rr-03-host-only" || return 1
  remote_child_start || return 1
  wait_for_remote_attached || return 1
  verify_session_state "rr-03-after" || return 1
  verify_remote_session_state "rr-03-after-remote" || return 1
  run_follow_up_check "rr-03-follow-up" "rr-03-${RUN_ID}" || return 1
  SCENARIO_NOTE="with both sides down, bringing the host back first preserved host session reads and the remote child reattached when started later"
}

scenario_rr04() {
  verify_session_state "rr-04-before" || return 1
  verify_remote_session_state "rr-04-before-remote" || return 1
  remote_child_stop || return 1
  wait_for_remote_down || return 1
  stop_host || return 1
  remote_child_start || return 1
  sleep "${POLL_INTERVAL_SECONDS}"
  ensure_host_running || return 1
  wait_for_remote_attached || return 1
  verify_session_state "rr-04-after" || return 1
  verify_remote_session_state "rr-04-after-remote" || return 1
  run_follow_up_check "rr-04-follow-up" "rr-04-${RUN_ID}" || return 1
  SCENARIO_NOTE="child was already running before the host returned, the host recovered its route state, and routed execution resumed on the same child"
}

run_scenario() {
  local id="${1:-}"
  local fn="${2:-}"
  local status note started_at finished_at
  started_at="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
  SCENARIO_NOTE=""
  if "${fn}"; then
    status="PASS"
  else
    status="FAIL"
    OVERALL_FAILURE=1
  fi
  note="${SCENARIO_NOTE}"
  finished_at="$(date -u +"%Y-%m-%dT%H:%M:%SZ")"
  jq -nc \
    --arg id "${id}" \
    --arg status "${status}" \
    --arg started_at "${started_at}" \
    --arg finished_at "${finished_at}" \
    --arg session_id "${SESSION_ID}" \
    --arg remote_deploy_session_id "${REMOTE_DEPLOY_SESSION_ID}" \
    --arg child_swarm_id "${CHILD_SWARM_ID}" \
    --arg ssh_target "${REMOTE_SSH_TARGET}" \
    --arg remote_tailnet_url "${REMOTE_TAILNET_URL}" \
    --arg note "${note}" \
    '{id:$id,status:$status,started_at:$started_at,finished_at:$finished_at,session_id:$session_id,remote_deploy_session_id:$remote_deploy_session_id,child_swarm_id:$child_swarm_id,ssh_target:$ssh_target,remote_tailnet_url:$remote_tailnet_url,note:$note}' \
    >"${ARTIFACT_DIR}/${id}.json"
  SCENARIO_FILES+=("${ARTIFACT_DIR}/${id}.json")
}

bootstrap_if_needed() {
  if [[ -n "${HOST_ROOT}" ]]; then
    return 0
  fi
  [[ -n "${BOOTSTRAP_SSH_TARGET}" ]] || fail "--ssh-target is required when --host-root is not supplied"
  HOST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/swarm-remote-recovery-XXXXXX")"
  OWN_BOOTSTRAP_ENV="true"
  GROUP_NAME="${GROUP_NAME:-remote-recovery-$(date +%Y%m%d-%H%M%S)}"
  local args=(
    "./tests/swarmd/remote_deploy_e2e.sh"
    "--host-root" "${HOST_ROOT}"
    "--ssh-target" "${BOOTSTRAP_SSH_TARGET}"
    "--remote-runtime" "${REMOTE_RUNTIME}"
    "--workspace-path" "${WORKSPACE_PATH}"
    "--workspace-name" "${WORKSPACE_NAME}"
    "--group-name" "${GROUP_NAME}"
    "--host-swarm-name" "${HOST_SWARM_NAME}"
    "--host-backend-port" "${HOST_BACKEND_PORT}"
    "--host-desktop-port" "${HOST_DESKTOP_PORT}"
    "--host-peer-port" "${HOST_PEER_PORT}"
    "--manage-tailscale-serve" "${MANAGE_TAILSCALE_SERVE}"
    "--poll-timeout" "${POLL_TIMEOUT_SECONDS}"
    "--poll-interval" "${POLL_INTERVAL_SECONDS}"
    "--remote-start-timeout" "${REMOTE_START_MAX_TIME_SECONDS}"
  )
  if [[ -n "${GROUP_ID}" ]]; then
    args+=("--group-id" "${GROUP_ID}")
  fi
  if [[ -n "${HOST_TAILSCALE_URL_OVERRIDE}" ]]; then
    args+=("--host-tailscale-url" "${HOST_TAILSCALE_URL_OVERRIDE}")
  fi
  if [[ "${REBUILD_HOST}" != "true" ]]; then
    args+=("--skip-host-rebuild")
  fi
  if [[ "${TAILSCALE_AUTH_MODE}" == "key" ]]; then
    args+=("--tailscale-auth-mode" "key" "--tailscale-auth-key-env" "${TAILSCALE_AUTH_KEY_ENV}")
  fi
  (
    cd "${ROOT_DIR}"
    "${args[@]}"
  )
}

cleanup_owned_bootstrap() {
  if [[ "${OWN_BOOTSTRAP_ENV}" != "true" || -z "${HOST_ROOT}" || ! -d "${HOST_ROOT}" ]]; then
    return 0
  fi
  local args=(
    "./tests/swarmd/remote_deploy_e2e.sh"
    "--host-root" "${HOST_ROOT}"
    "--ssh-target" "${REMOTE_SSH_TARGET:-${BOOTSTRAP_SSH_TARGET:-}}"
    "--teardown-only"
  )
  (
    cd "${ROOT_DIR}"
    "${args[@]}"
  ) >/dev/null 2>&1 || true
}

cleanup() {
  cleanup_owned_bootstrap
}

load_context() {
  local summary_file summary_json session_count selected_session_json startup_cfg port desktop_port tailscale_url
  HOST_ROOT="$(cd "${HOST_ROOT}" && pwd)"
  summary_file="${HOST_ROOT}/artifacts/summary.json"
  [[ -f "${summary_file}" ]] || fail "missing remote deploy summary: ${summary_file}"
  summary_json="$(cat -- "${summary_file}")"

  RUN_ID="$(date +%Y%m%d-%H%M%S)"
  ARTIFACT_DIR="${HOST_ROOT}/remote-recovery-artifacts/${RUN_ID}"
  mkdir -p "${ARTIFACT_DIR}"
  write_artifact "bootstrap-summary.json" "${summary_json}"

  startup_cfg="${HOST_ROOT}/xdg/config/swarm/swarm.conf"
  port="$(trim "$(config_value "${startup_cfg}" "port" || true)")"
  desktop_port="$(trim "$(config_value "${startup_cfg}" "desktop_port" || true)")"
  tailscale_url="$(trim "$(config_value "${startup_cfg}" "tailscale_url" || true)")"

  HOST_ADMIN_API_URL="$(printf '%s' "${summary_json}" | jq -r '.host_api_url // empty')"
  HOST_DESKTOP_URL="$(printf '%s' "${summary_json}" | jq -r '.host_desktop_url // empty')"
  HOST_TAILSCALE_URL="$(printf '%s' "${summary_json}" | jq -r '.host_tailscale_url // empty')"
  if [[ -n "${port}" ]]; then
    HOST_ADMIN_API_URL="http://127.0.0.1:${port}"
  fi
  if [[ -n "${desktop_port}" ]]; then
    HOST_DESKTOP_URL="http://127.0.0.1:${desktop_port}"
  fi
  if [[ -n "${tailscale_url}" ]]; then
    HOST_TAILSCALE_URL="${tailscale_url}"
  fi
  TARGET_GROUP_ID="$(printf '%s' "${summary_json}" | jq -r '.group_id // empty')"
  TARGET_GROUP_NAME="$(printf '%s' "${summary_json}" | jq -r '.group_name // empty')"
  SOURCE_WORKSPACE_PATH="$(printf '%s' "${summary_json}" | jq -r '.workspace_path // empty')"
  if [[ -z "${SOURCE_WORKSPACE_PATH}" ]]; then
    SOURCE_WORKSPACE_PATH="${WORKSPACE_PATH}"
  fi

  session_count="$(printf '%s' "${summary_json}" | jq -r '.sessions | length')"
  if [[ -n "${REMOTE_DEPLOY_SESSION_ID_OVERRIDE}" ]]; then
    selected_session_json="$(printf '%s' "${summary_json}" | jq -c --arg session_id "${REMOTE_DEPLOY_SESSION_ID_OVERRIDE}" '.sessions[]? | select(.id == $session_id)' | head -n 1)"
    [[ -n "${selected_session_json}" ]] || fail "remote session id ${REMOTE_DEPLOY_SESSION_ID_OVERRIDE} was not found in ${summary_file}"
  else
    if [[ "${session_count}" != "1" ]]; then
      fail "expected exactly one remote session in ${summary_file}; supply --remote-session-id to select one"
    fi
    selected_session_json="$(printf '%s' "${summary_json}" | jq -c '.sessions[0]')"
  fi

  REMOTE_DEPLOY_SESSION_ID="$(printf '%s' "${selected_session_json}" | jq -r '.id // empty')"
  REMOTE_SSH_TARGET="$(printf '%s' "${selected_session_json}" | jq -r '.ssh_session_target // empty')"
  REMOTE_RUNTIME="$(printf '%s' "${selected_session_json}" | jq -r '.remote_runtime // "docker"')"
  REMOTE_SYSTEMD_UNIT="$(printf '%s' "${selected_session_json}" | jq -r '.preflight.systemd_unit // empty')"
  REMOTE_ROOT="$(printf '%s' "${selected_session_json}" | jq -r '.preflight.remote_root // empty')"
  REMOTE_TAILNET_URL="$(printf '%s' "${selected_session_json}" | jq -r '.remote_tailnet_url // empty')"
  CHILD_SWARM_ID="$(printf '%s' "${selected_session_json}" | jq -r '.child_swarm_id // empty')"
  HOST_START_SCRIPT="${HOST_ROOT}/start-host.sh"
  HOST_STOP_SCRIPT="${HOST_ROOT}/stop-host.sh"
  WORKSPACE_NAME="${WORKSPACE_NAME:-$(basename "${SOURCE_WORKSPACE_PATH}")}"
  RUNTIME_WORKSPACE_PATH="/workspaces/${WORKSPACE_NAME}"

  [[ -n "${HOST_ADMIN_API_URL}" ]] || fail "host api url missing from ${summary_file}"
  [[ -n "${REMOTE_DEPLOY_SESSION_ID}" ]] || fail "remote deploy session id missing from ${summary_file}"
  [[ -n "${REMOTE_SSH_TARGET}" ]] || fail "ssh target missing from ${summary_file}"
  [[ -n "${CHILD_SWARM_ID}" ]] || fail "child swarm id missing from ${summary_file}"

  REMOTE_SUDO_PREFIX="sudo "
}

seed_context() {
  ensure_host_running || fail "failed to start host ${HOST_ADMIN_API_URL}"
  ensure_tailscale_serve_for_host
  wait_for_remote_attached || fail "remote session ${REMOTE_DEPLOY_SESSION_ID} did not reach attached/ready"
  if [[ "${PROVE_ROUTED_AI}" == "true" ]]; then
    write_artifact "child-provider-seed.json" "$(seed_remote_child_provider_key "${REMOTE_TAILNET_URL}" "${PROOF_PROVIDER}" "${!PROOF_PROVIDER_KEY_ENV}")"
  fi
  create_routed_session || fail "failed to create routed session on host ${HOST_ADMIN_API_URL}"
  SEED_MESSAGE_CONTENT="remote recovery seed ${RUN_ID}"
  append_message_and_verify "seed" "${SEED_MESSAGE_CONTENT}" || fail "failed to append initial routed seed message"
  verify_session_state "seed-state" || fail "initial routed session state verification failed"
  verify_remote_session_state "seed-remote-state" || fail "initial remote deploy state verification failed"
}

write_final_summary() {
  capture_remote_logs "final"
  jq -s \
    --arg host_root "${HOST_ROOT}" \
    --arg host_api_url "${HOST_ADMIN_API_URL}" \
    --arg host_desktop_url "${HOST_DESKTOP_URL}" \
    --arg host_tailscale_url "${HOST_TAILSCALE_URL}" \
    --arg session_id "${SESSION_ID}" \
    --arg remote_deploy_session_id "${REMOTE_DEPLOY_SESSION_ID}" \
    --arg ssh_target "${REMOTE_SSH_TARGET}" \
    --arg child_swarm_id "${CHILD_SWARM_ID}" \
    --arg remote_tailnet_url "${REMOTE_TAILNET_URL}" \
    --arg workspace_path "${SOURCE_WORKSPACE_PATH}" \
    --arg workspace_name "${WORKSPACE_NAME}" \
    '{host_root:$host_root,host_api_url:$host_api_url,host_desktop_url:$host_desktop_url,host_tailscale_url:$host_tailscale_url,session_id:$session_id,remote_deploy_session_id:$remote_deploy_session_id,ssh_target:$ssh_target,child_swarm_id:$child_swarm_id,remote_tailnet_url:$remote_tailnet_url,workspace_path:$workspace_path,workspace_name:$workspace_name,scenarios:.}' \
    "${SCENARIO_FILES[@]}" >"${ARTIFACT_DIR}/summary.json"
  log ""
  log "Remote recovery summary"
  cat "${ARTIFACT_DIR}/summary.json" | jq .
  log ""
  log "Host root: ${HOST_ROOT}"
  log "Artifacts: ${ARTIFACT_DIR}"
}

HOST_ROOT=""
REMOTE_DEPLOY_SESSION_ID_OVERRIDE=""
BOOTSTRAP_SSH_TARGET=""
REMOTE_RUNTIME="docker"
WORKSPACE_PATH="${ROOT_DIR}"
WORKSPACE_NAME="$(basename "${WORKSPACE_PATH}")"
GROUP_ID=""
GROUP_NAME=""
HOST_SWARM_NAME="Remote Recovery Test Host"
HOST_BACKEND_PORT="17781"
HOST_DESKTOP_PORT="15555"
HOST_PEER_PORT="17791"
HOST_TAILSCALE_URL_OVERRIDE=""
MANAGE_TAILSCALE_SERVE="true"
TAILSCALE_AUTH_MODE="manual"
TAILSCALE_AUTH_KEY_ENV=""
REBUILD_HOST="true"
POLL_TIMEOUT_SECONDS="180"
POLL_INTERVAL_SECONDS="3"
REMOTE_START_MAX_TIME_SECONDS="600"
PROVE_ROUTED_AI="false"
PROOF_PROVIDER="fireworks"
PROOF_MODEL="accounts/fireworks/models/kimi-k2p5"
PROOF_THINKING="high"
PROOF_PROVIDER_KEY_ENV=""
PROOF_BASH_COMMAND="pwd"
LOG_TAIL="200"
SCENARIO="all"

ATTACH_TOKEN=""
HOST_ADMIN_API_URL=""
HOST_DESKTOP_URL=""
HOST_TAILSCALE_URL=""
HOST_START_SCRIPT=""
HOST_STOP_SCRIPT=""
TARGET_GROUP_ID=""
TARGET_GROUP_NAME=""
REMOTE_DEPLOY_SESSION_ID=""
REMOTE_SSH_TARGET=""
REMOTE_SYSTEMD_UNIT=""
REMOTE_ROOT=""
REMOTE_SUDO_PREFIX=""
REMOTE_TAILNET_URL=""
CHILD_SWARM_ID=""
SOURCE_WORKSPACE_PATH=""
RUNTIME_WORKSPACE_PATH=""
SESSION_ID=""
SEED_MESSAGE_CONTENT=""
RUN_ID=""
ARTIFACT_DIR=""
LAST_REMOTE_SESSIONS_JSON=""
LAST_REMOTE_SESSION_JSON=""
OVERALL_FAILURE=0
SCENARIO_FILES=()
SCENARIO_NOTE=""
OWN_BOOTSTRAP_ENV="false"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --scenario)
      SCENARIO="$(printf '%s' "${2:-}" | tr '[:upper:]' '[:lower:]')"
      shift 2
      ;;
    --host-root)
      HOST_ROOT="${2:-}"
      shift 2
      ;;
    --remote-session-id)
      REMOTE_DEPLOY_SESSION_ID_OVERRIDE="${2:-}"
      shift 2
      ;;
    --ssh-target)
      BOOTSTRAP_SSH_TARGET="${2:-}"
      shift 2
      ;;
    --remote-runtime)
      REMOTE_RUNTIME="${2:-}"
      shift 2
      ;;
    --workspace-path)
      WORKSPACE_PATH="${2:-}"
      shift 2
      ;;
    --workspace-name)
      WORKSPACE_NAME="${2:-}"
      shift 2
      ;;
    --group-id)
      GROUP_ID="${2:-}"
      shift 2
      ;;
    --group-name)
      GROUP_NAME="${2:-}"
      shift 2
      ;;
    --host-root-name)
      HOST_SWARM_NAME="${2:-}"
      shift 2
      ;;
    --host-backend-port)
      HOST_BACKEND_PORT="${2:-}"
      shift 2
      ;;
    --host-desktop-port)
      HOST_DESKTOP_PORT="${2:-}"
      shift 2
      ;;
    --host-peer-port)
      HOST_PEER_PORT="${2:-}"
      shift 2
      ;;
    --host-tailscale-url)
      HOST_TAILSCALE_URL_OVERRIDE="${2:-}"
      shift 2
      ;;
    --manage-tailscale-serve)
      MANAGE_TAILSCALE_SERVE="$(printf '%s' "${2:-}" | tr '[:upper:]' '[:lower:]')"
      shift 2
      ;;
    --tailscale-auth-mode)
      TAILSCALE_AUTH_MODE="$(printf '%s' "${2:-}" | tr '[:upper:]' '[:lower:]')"
      shift 2
      ;;
    --tailscale-auth-key-env)
      TAILSCALE_AUTH_KEY_ENV="${2:-}"
      shift 2
      ;;
    --skip-host-rebuild)
      REBUILD_HOST="false"
      shift
      ;;
    --poll-timeout)
      POLL_TIMEOUT_SECONDS="${2:-}"
      shift 2
      ;;
    --poll-interval)
      POLL_INTERVAL_SECONDS="${2:-}"
      shift 2
      ;;
    --remote-start-timeout)
      REMOTE_START_MAX_TIME_SECONDS="${2:-}"
      shift 2
      ;;
    --prove-routed-ai)
      PROVE_ROUTED_AI="true"
      shift
      ;;
    --proof-provider)
      PROOF_PROVIDER="${2:-}"
      shift 2
      ;;
    --proof-model)
      PROOF_MODEL="${2:-}"
      shift 2
      ;;
    --proof-thinking)
      PROOF_THINKING="${2:-}"
      shift 2
      ;;
    --proof-provider-key-env)
      PROOF_PROVIDER_KEY_ENV="${2:-}"
      shift 2
      ;;
    --proof-bash-command)
      PROOF_BASH_COMMAND="${2:-}"
      shift 2
      ;;
    --log-tail)
      LOG_TAIL="${2:-}"
      shift 2
      ;;
    --help|-h)
      usage
      exit 0
      ;;
    *)
      fail "unknown option: $1"
      ;;
  esac
done

[[ "${POLL_TIMEOUT_SECONDS}" =~ ^[0-9]+$ ]] || fail "--poll-timeout must be a positive integer"
[[ "${POLL_INTERVAL_SECONDS}" =~ ^[0-9]+$ ]] || fail "--poll-interval must be a positive integer"
[[ "${REMOTE_START_MAX_TIME_SECONDS}" =~ ^[0-9]+$ ]] || fail "--remote-start-timeout must be a positive integer"
[[ "${LOG_TAIL}" =~ ^[0-9]+$ ]] || fail "--log-tail must be a positive integer"
[[ "${SCENARIO}" == "all" || "${SCENARIO}" == "rr-01" || "${SCENARIO}" == "rr-02" || "${SCENARIO}" == "rr-03" || "${SCENARIO}" == "rr-04" ]] || fail "--scenario must be one of rr-01, rr-02, rr-03, rr-04, all"

case "${REMOTE_RUNTIME}" in
  docker|podman) ;;
  *) fail "--remote-runtime must be docker or podman" ;;
esac

case "${MANAGE_TAILSCALE_SERVE}" in
  true|false) ;;
  *) fail "--manage-tailscale-serve must be true or false" ;;
esac

case "${TAILSCALE_AUTH_MODE}" in
  manual) ;;
  key) ;;
  *) fail "--tailscale-auth-mode must be manual or key" ;;
esac

if [[ "${TAILSCALE_AUTH_MODE}" == "key" ]]; then
  [[ -n "${TAILSCALE_AUTH_KEY_ENV}" ]] || fail "--tailscale-auth-key-env is required with --tailscale-auth-mode key"
  [[ -n "${!TAILSCALE_AUTH_KEY_ENV:-}" ]] || fail "environment variable ${TAILSCALE_AUTH_KEY_ENV} is required for --tailscale-auth-mode key"
fi

if [[ "${PROVE_ROUTED_AI}" == "true" ]]; then
  [[ -n "${PROOF_PROVIDER_KEY_ENV}" ]] || fail "--proof-provider-key-env is required with --prove-routed-ai"
  [[ -n "${!PROOF_PROVIDER_KEY_ENV:-}" ]] || fail "environment variable ${PROOF_PROVIDER_KEY_ENV} is required for --prove-routed-ai"
fi

WORKSPACE_PATH="$(cd "${WORKSPACE_PATH}" && pwd)"
[[ -d "${WORKSPACE_PATH}" ]] || fail "--workspace-path must point to an existing directory"

require_command curl
require_command jq
require_command ssh
require_command tailscale
trap cleanup EXIT

bootstrap_if_needed
load_context
ensure_host_running || fail "failed to start host ${HOST_ADMIN_API_URL}"
seed_context

log "Running remote recovery checks"
log "host root: ${HOST_ROOT}"
log "host api: ${HOST_ADMIN_API_URL}"
log "remote deploy session: ${REMOTE_DEPLOY_SESSION_ID}"
log "ssh target: ${REMOTE_SSH_TARGET}"
log "child swarm: ${CHILD_SWARM_ID}"
log "session: ${SESSION_ID}"
log "artifacts: ${ARTIFACT_DIR}"

case "${SCENARIO}" in
  all)
    run_scenario "RR-01" scenario_rr01
    run_scenario "RR-02" scenario_rr02
    run_scenario "RR-03" scenario_rr03
    run_scenario "RR-04" scenario_rr04
    ;;
  rr-01)
    run_scenario "RR-01" scenario_rr01
    ;;
  rr-02)
    run_scenario "RR-02" scenario_rr02
    ;;
  rr-03)
    run_scenario "RR-03" scenario_rr03
    ;;
  rr-04)
    run_scenario "RR-04" scenario_rr04
    ;;
esac

write_final_summary
exit "${OVERALL_FAILURE}"
