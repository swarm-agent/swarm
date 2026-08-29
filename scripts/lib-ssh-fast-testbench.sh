#!/usr/bin/env bash

swarm_fast_testbench_fail() {
  printf 'ssh-fast-testbench: %s\n' "$*" >&2
  return 1
}

swarm_fast_testbench_env_key_allowed() {
  case "$1" in
    SWARM_PRIMARY_SSH|SWARM_TESTBENCH_LOCAL_DESKTOP_PORT|SWARM_REMOTE_DESKTOP_PORT|SWARM_TESTBENCH_LOCAL_API_PORT|SWARM_TESTBENCH_REMOTE_API_PORT|SWARM_TESTBENCH_REVERSE_LOCAL_PORT|SWARM_TESTBENCH_REVERSE_REMOTE_PORT|SWARM_TESTBENCH_PROVIDER|SWARM_TESTBENCH_MODEL|SWARM_TESTBENCH_THINKING|SWARM_TESTBENCH_ACTION_MODEL|SWARM_TESTBENCH_ACTION_THINKING|SWARM_TESTBENCH_PLAN_MODEL|SWARM_TESTBENCH_PLAN_THINKING|SWARM_TESTBENCH_CODER_MODEL|SWARM_TESTBENCH_CODER_THINKING|SWARM_TESTBENCH_DESIGNER_MODEL|SWARM_TESTBENCH_DESIGNER_THINKING|SWARM_TESTBENCH_LINKED_WORKSPACE_PATH)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

swarm_fast_testbench_load_env() {
  local root_dir="$1"
  local env_file="${SWARM_TESTBENCH_ENV_FILE:-${root_dir}/.env}"
  local line key value

  if [[ ! -f "${env_file}" ]]; then
    if [[ -n "${SWARM_TESTBENCH_ENV_FILE:-}" ]]; then
      swarm_fast_testbench_fail "missing configured environment file ${env_file}"
      return 1
    fi
    return 0
  fi

  while IFS= read -r line || [[ -n "${line}" ]]; do
    line="${line%$'\r'}"
    [[ -z "${line//[[:space:]]/}" || "${line}" =~ ^[[:space:]]*# ]] && continue
    if [[ ! "${line}" =~ ^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]]; then
      swarm_fast_testbench_fail "${env_file} contains a non-assignment line"
      return 1
    fi
    key="${BASH_REMATCH[1]}"
    value="${BASH_REMATCH[2]}"
    value="${value#${value%%[![:space:]]*}}"
    value="${value%${value##*[![:space:]]}}"
    if [[ "${value}" =~ ^\"(.*)\"$ || "${value}" =~ ^\'(.*)\'$ ]]; then
      value="${BASH_REMATCH[1]}"
    fi
    if ! swarm_fast_testbench_env_key_allowed "${key}"; then
      swarm_fast_testbench_fail "${env_file} contains unsupported key ${key}; testbench environment files must remain non-secret and connection-only"
      return 1
    fi
    if [[ "${key}" =~ (TOKEN|SECRET|PASSWORD|PASSWD|API_KEY|PRIVATE_KEY|CREDENTIAL|COOKIE) ]]; then
      swarm_fast_testbench_fail "${env_file} contains credential-like key ${key}; credentials are forbidden"
      return 1
    fi
    printf -v "${key}" '%s' "${value}"
    export "${key}"
  done <"${env_file}"
}

swarm_fast_testbench_discover_candidate_repo() {
  local ssh_alias="$1"
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

swarm_fast_testbench_candidate_state() {
  local ssh_alias="$1"
  local remote_repo="$2"
  local expected_commit="$3"
  ssh "${ssh_alias}" 'bash -s' -- "${remote_repo}" "${expected_commit}" <<'REMOTE_CANDIDATE_STATE'
set -euo pipefail
repo="$1"
expected="$2"
cd "$repo"
head="$(git rev-parse --verify HEAD)"
status="$(git status --porcelain)"
contains=false
git merge-base --is-ancestor "$expected" HEAD && contains=true
clean=false
[ -z "$status" ] && clean=true
printf 'head=%s clean=%s contains_expected=%s\n' "$head" "$clean" "$contains"
[ "$clean" = true ] && [ "$contains" = true ]
REMOTE_CANDIDATE_STATE
}

swarm_fast_testbench_configure() {
  local root_dir="$1"
  local ssh_argument="${2:-}"

  swarm_fast_testbench_load_env "${root_dir}" || return 1
  SSH_HOST="${ssh_argument:-${SWARM_PRIMARY_SSH:-}}"
  [[ "${SSH_HOST}" =~ ^[A-Za-z0-9._-]+$ ]] || {
    swarm_fast_testbench_fail "pass a configured SSH alias as the first argument or set SWARM_PRIMARY_SSH in .env"
    return 1
  }
  SERVICE_UNIT="${SWARM_SERVICE_UNIT:-swarm.service}"
  API_URL="${SWARM_PRIMARY_API_URL:-http://127.0.0.1:${SWARM_TESTBENCH_REMOTE_API_PORT:-7781}}"
  EXPECTED_COMMIT="${SWARM_EXPECTED_COMMIT:-}"
  if [[ -z "${EXPECTED_COMMIT}" ]]; then
    EXPECTED_COMMIT="$(git -C "${root_dir}" rev-parse --verify HEAD)" || return 1
  fi
  git -C "${root_dir}" rev-parse --verify "${EXPECTED_COMMIT}^{commit}" >/dev/null || {
    swarm_fast_testbench_fail "expected candidate commit is not available locally: ${EXPECTED_COMMIT}"
    return 1
  }
  REMOTE_REPO="${SWARM_REMOTE_REPO:-}"
  if [[ -z "${REMOTE_REPO}" ]]; then
    REMOTE_REPO="$(swarm_fast_testbench_discover_candidate_repo "${SSH_HOST}")" || {
      swarm_fast_testbench_fail "could not discover the remote candidate checkout; set SWARM_REMOTE_REPO"
      return 1
    }
  fi
  export SSH_HOST SERVICE_UNIT API_URL EXPECTED_COMMIT REMOTE_REPO
}

swarm_fast_testbench_prepare_candidate() {
  local root_dir="$1"
  local state

  if state="$(swarm_fast_testbench_candidate_state "${SSH_HOST}" "${REMOTE_REPO}" "${EXPECTED_COMMIT}")"; then
    printf 'ssh-fast-testbench: candidate ready %s\n' "${state}"
    return 0
  fi

  printf 'ssh-fast-testbench: candidate requires fast deployment (%s)\n' "${state:-state unavailable}"
  "${root_dir}/scripts/ssh-fast-test.sh" "${SSH_HOST}" \
    --remote-dir "${REMOTE_REPO}" \
    --service "${SERVICE_UNIT}" \
    --allow-dirty-committed-ref

  state="$(swarm_fast_testbench_candidate_state "${SSH_HOST}" "${REMOTE_REPO}" "${EXPECTED_COMMIT}")" || {
    swarm_fast_testbench_fail "candidate is still not clean or does not contain ${EXPECTED_COMMIT} after fast deployment (${state:-state unavailable})"
    return 1
  }
  printf 'ssh-fast-testbench: candidate ready after fast deployment %s\n' "${state}"
}
