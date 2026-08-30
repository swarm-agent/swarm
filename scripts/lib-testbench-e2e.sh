#!/usr/bin/env bash

# Shared, source-only helpers for alias-driven testbench E2E runners.
# The .env parser is intentionally data-only: it never evals shell content and
# accepts only the non-secret connection and Fireworks model settings listed below.

swarm_testbench_fail() {
  printf 'testbench-e2e: %s\n' "$*" >&2
  return 1
}

swarm_testbench_env_key_allowed() {
  case "$1" in
    SWARM_PRIMARY_SSH|SWARM_TESTBENCH_LOCAL_DESKTOP_PORT|SWARM_REMOTE_DESKTOP_PORT|SWARM_TESTBENCH_LOCAL_API_PORT|SWARM_TESTBENCH_REMOTE_API_PORT|SWARM_TESTBENCH_REVERSE_LOCAL_PORT|SWARM_TESTBENCH_REVERSE_REMOTE_PORT|SWARM_TESTBENCH_PROVIDER|SWARM_TESTBENCH_MODEL|SWARM_TESTBENCH_THINKING|SWARM_TESTBENCH_ACTION_MODEL|SWARM_TESTBENCH_ACTION_THINKING|SWARM_TESTBENCH_PLAN_MODEL|SWARM_TESTBENCH_PLAN_THINKING|SWARM_TESTBENCH_CODER_MODEL|SWARM_TESTBENCH_CODER_THINKING|SWARM_TESTBENCH_DESIGNER_MODEL|SWARM_TESTBENCH_DESIGNER_THINKING|SWARM_TESTBENCH_LINKED_WORKSPACE_PATH)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

swarm_testbench_load_env() {
  local root_dir="$1"
  local env_file="${SWARM_TESTBENCH_ENV_FILE:-${root_dir}/.env}"
  local line key value

  [[ -f "${env_file}" ]] || swarm_testbench_fail "missing ${env_file}; copy .env.example to .env and set the SSH alias and loopback ports"

  while IFS= read -r line || [[ -n "${line}" ]]; do
    line="${line%$'\r'}"
    [[ -z "${line//[[:space:]]/}" || "${line}" =~ ^[[:space:]]*# ]] && continue
    if [[ ! "${line}" =~ ^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]]; then
      swarm_testbench_fail "${env_file} contains a non-assignment line"
      return 1
    fi
    key="${BASH_REMATCH[1]}"
    value="${BASH_REMATCH[2]}"
    value="${value#${value%%[![:space:]]*}}"
    value="${value%${value##*[![:space:]]}}"
    if [[ "${value}" =~ ^\"(.*)\"$ || "${value}" =~ ^\'(.*)\'$ ]]; then
      value="${BASH_REMATCH[1]}"
    fi
    if ! swarm_testbench_env_key_allowed "${key}"; then
      swarm_testbench_fail "${env_file} contains unsupported key ${key}; testbench .env files must remain non-secret and connection-only"
      return 1
    fi
    if [[ "${key}" =~ (TOKEN|SECRET|PASSWORD|PASSWD|API_KEY|PRIVATE_KEY|CREDENTIAL|COOKIE) ]]; then
      swarm_testbench_fail "${env_file} contains credential-like key ${key}; credentials are forbidden in the testbench .env"
      return 1
    fi
    printf -v "${key}" '%s' "${value}"
    export "${key}"
  done <"${env_file}"

  SWARM_PRIMARY_SSH="${SWARM_PRIMARY_SSH:-}"
  SWARM_TESTBENCH_LOCAL_DESKTOP_PORT="${SWARM_TESTBENCH_LOCAL_DESKTOP_PORT:-15555}"
  SWARM_REMOTE_DESKTOP_PORT="${SWARM_REMOTE_DESKTOP_PORT:-5555}"
  SWARM_TESTBENCH_LOCAL_API_PORT="${SWARM_TESTBENCH_LOCAL_API_PORT:-17781}"
  SWARM_TESTBENCH_REMOTE_API_PORT="${SWARM_TESTBENCH_REMOTE_API_PORT:-7781}"
  SWARM_TESTBENCH_REVERSE_LOCAL_PORT="${SWARM_TESTBENCH_REVERSE_LOCAL_PORT:-}"
  SWARM_TESTBENCH_REVERSE_REMOTE_PORT="${SWARM_TESTBENCH_REVERSE_REMOTE_PORT:-}"
  SWARM_TESTBENCH_PROVIDER="${SWARM_TESTBENCH_PROVIDER:-}"
  SWARM_TESTBENCH_MODEL="${SWARM_TESTBENCH_MODEL:-}"
  SWARM_TESTBENCH_THINKING="${SWARM_TESTBENCH_THINKING:-}"
  SWARM_TESTBENCH_ACTION_MODEL="${SWARM_TESTBENCH_ACTION_MODEL:-}"
  SWARM_TESTBENCH_ACTION_THINKING="${SWARM_TESTBENCH_ACTION_THINKING:-}"
  SWARM_TESTBENCH_PLAN_MODEL="${SWARM_TESTBENCH_PLAN_MODEL:-}"
  SWARM_TESTBENCH_PLAN_THINKING="${SWARM_TESTBENCH_PLAN_THINKING:-}"
  SWARM_TESTBENCH_CODER_MODEL="${SWARM_TESTBENCH_CODER_MODEL:-}"
  SWARM_TESTBENCH_CODER_THINKING="${SWARM_TESTBENCH_CODER_THINKING:-}"
  SWARM_TESTBENCH_DESIGNER_MODEL="${SWARM_TESTBENCH_DESIGNER_MODEL:-}"
  SWARM_TESTBENCH_DESIGNER_THINKING="${SWARM_TESTBENCH_DESIGNER_THINKING:-}"
  SWARM_TESTBENCH_LINKED_WORKSPACE_PATH="${SWARM_TESTBENCH_LINKED_WORKSPACE_PATH:-}"

  export SWARM_PRIMARY_SSH SWARM_TESTBENCH_LOCAL_DESKTOP_PORT SWARM_REMOTE_DESKTOP_PORT
  export SWARM_TESTBENCH_LOCAL_API_PORT SWARM_TESTBENCH_REMOTE_API_PORT
  export SWARM_TESTBENCH_REVERSE_LOCAL_PORT SWARM_TESTBENCH_REVERSE_REMOTE_PORT SWARM_TESTBENCH_PROVIDER
  export SWARM_TESTBENCH_MODEL SWARM_TESTBENCH_THINKING SWARM_TESTBENCH_LINKED_WORKSPACE_PATH
  export SWARM_TESTBENCH_ACTION_MODEL SWARM_TESTBENCH_ACTION_THINKING SWARM_TESTBENCH_PLAN_MODEL SWARM_TESTBENCH_PLAN_THINKING
  export SWARM_TESTBENCH_CODER_MODEL SWARM_TESTBENCH_CODER_THINKING SWARM_TESTBENCH_DESIGNER_MODEL SWARM_TESTBENCH_DESIGNER_THINKING
}

swarm_testbench_validate_port() {
  local name="$1"
  local value="$2"
  [[ "${value}" =~ ^[0-9]+$ ]] || swarm_testbench_fail "${name} must be an integer"
  (( value >= 1 && value <= 65535 )) || swarm_testbench_fail "${name} must be between 1 and 65535"
}

swarm_testbench_validate_env() {
  [[ "${SWARM_PRIMARY_SSH}" =~ ^[A-Za-z0-9._-]+$ ]] || swarm_testbench_fail "SWARM_PRIMARY_SSH must be a configured SSH alias, not a hostname, command, or user@host value"
  swarm_testbench_validate_port SWARM_TESTBENCH_LOCAL_DESKTOP_PORT "${SWARM_TESTBENCH_LOCAL_DESKTOP_PORT}" || return 1
  swarm_testbench_validate_port SWARM_REMOTE_DESKTOP_PORT "${SWARM_REMOTE_DESKTOP_PORT}" || return 1
  swarm_testbench_validate_port SWARM_TESTBENCH_LOCAL_API_PORT "${SWARM_TESTBENCH_LOCAL_API_PORT}" || return 1
  swarm_testbench_validate_port SWARM_TESTBENCH_REMOTE_API_PORT "${SWARM_TESTBENCH_REMOTE_API_PORT}" || return 1
  [[ "${SWARM_TESTBENCH_LOCAL_DESKTOP_PORT}" != "${SWARM_TESTBENCH_LOCAL_API_PORT}" ]] || swarm_testbench_fail "local Desktop and API ports must differ"
  [[ "${SWARM_TESTBENCH_PROVIDER}" == fireworks ]] || swarm_testbench_fail "SWARM_TESTBENCH_PROVIDER must be fireworks; testbench live tests do not support Codex or another provider"
  local model_name thinking_name model_value thinking_value
  for model_name in SWARM_TESTBENCH_MODEL SWARM_TESTBENCH_ACTION_MODEL SWARM_TESTBENCH_PLAN_MODEL SWARM_TESTBENCH_CODER_MODEL SWARM_TESTBENCH_DESIGNER_MODEL; do
    model_value="${!model_name}"
    [[ "${model_value}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]] || swarm_testbench_fail "${model_name} must be an explicit Fireworks catalog model ID, not a request path"
  done
  for thinking_name in SWARM_TESTBENCH_THINKING SWARM_TESTBENCH_ACTION_THINKING SWARM_TESTBENCH_PLAN_THINKING SWARM_TESTBENCH_CODER_THINKING SWARM_TESTBENCH_DESIGNER_THINKING; do
    thinking_value="${!thinking_name}"
    [[ "${thinking_value}" =~ ^(low|medium|high|xhigh)$ ]] || swarm_testbench_fail "${thinking_name} must be low, medium, high, or xhigh"
  done
  if [[ -n "${SWARM_TESTBENCH_REVERSE_LOCAL_PORT}" || -n "${SWARM_TESTBENCH_REVERSE_REMOTE_PORT}" ]]; then
    [[ -n "${SWARM_TESTBENCH_REVERSE_LOCAL_PORT}" && -n "${SWARM_TESTBENCH_REVERSE_REMOTE_PORT}" ]] || swarm_testbench_fail "set both reverse-port variables or leave both empty"
    swarm_testbench_validate_port SWARM_TESTBENCH_REVERSE_LOCAL_PORT "${SWARM_TESTBENCH_REVERSE_LOCAL_PORT}" || return 1
    swarm_testbench_validate_port SWARM_TESTBENCH_REVERSE_REMOTE_PORT "${SWARM_TESTBENCH_REVERSE_REMOTE_PORT}" || return 1
  fi
}

swarm_testbench_discover_candidate_repo() {
  local ssh_alias="$1"
  [[ "${ssh_alias}" =~ ^[A-Za-z0-9._-]+$ ]] || swarm_testbench_fail "candidate repository discovery requires a configured SSH alias"
  ssh "${ssh_alias}" 'bash -s' <<'REMOTE_DISCOVER_CANDIDATE'
set -euo pipefail
for candidate in "$HOME/swarm-go" "$HOME/src/swarm-go" "$HOME/work/swarm-go"; do
  if [ -d "$candidate/.git" ] && [ -f "$candidate/AGENTS.md" ] && [ -x "$candidate/rebuild" ]; then
    printf '%s\n' "$candidate"
    exit 0
  fi
done
exit 1
REMOTE_DISCOVER_CANDIDATE
}

swarm_testbench_port_open() {
  local port="$1"
  (exec 3<>"/dev/tcp/127.0.0.1/${port}") >/dev/null 2>&1
}

swarm_testbench_wait_for_port() {
  local port="$1"
  local tunnel_pid="$2"
  local deadline=$((SECONDS + 15))
  while (( SECONDS < deadline )); do
    swarm_testbench_port_open "${port}" && return 0
    kill -0 "${tunnel_pid}" 2>/dev/null || return 1
    sleep 0.1
  done
  return 1
}

swarm_testbench_tunnel_args() {
  SWARM_TESTBENCH_TUNNEL_ARGS=(
    -NT
    -o ExitOnForwardFailure=yes
    -o ServerAliveInterval=15
    -o ServerAliveCountMax=3
    -L "${SWARM_TESTBENCH_LOCAL_DESKTOP_PORT}:127.0.0.1:${SWARM_REMOTE_DESKTOP_PORT}"
    -L "${SWARM_TESTBENCH_LOCAL_API_PORT}:127.0.0.1:${SWARM_TESTBENCH_REMOTE_API_PORT}"
  )
  if [[ -n "${SWARM_TESTBENCH_REVERSE_LOCAL_PORT}" ]]; then
    SWARM_TESTBENCH_TUNNEL_ARGS+=(
      -R "${SWARM_TESTBENCH_REVERSE_REMOTE_PORT}:127.0.0.1:${SWARM_TESTBENCH_REVERSE_LOCAL_PORT}"
    )
  fi
  SWARM_TESTBENCH_TUNNEL_ARGS+=("${SWARM_PRIMARY_SSH}")
}
