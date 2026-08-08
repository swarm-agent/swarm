#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage: scripts/run-runner-test.sh <target> <provider> [test-name] [--api-url <url>] [--workspace-path <path>] [--timeout-ms <ms>]

Runs a checked-in runner test against an already-running Swarm target.

Arguments:
  target       SSH alias, or an http(s) URL for direct execution
  provider     Provider id whose catalog recommendations select Plan and Auto models
  test-name    Runner under scripts/runners without .mjs (default: basic-plan-auto)

Options:
  --api-url          API URL used from the target host (SSH default: http://127.0.0.1:7781)
  --workspace-path   Existing bound workspace to use (default: first bound workspace)
  --timeout-ms       Overall runner wait budget (default: 900000)

Environment:
  SWARM_RUNNER_TOKEN  Optional auth token, required when direct URL access cannot use
                      loopback desktop-session bootstrap. It is passed to SSH via stdin,
                      never embedded in a remote command or copied config file.

Examples:
  scripts/run-runner-test.sh runner-alias codex
  scripts/run-runner-test.sh https://runner.example.invalid codex
USAGE
}

fail() {
  printf 'run-runner-test: %s\n' "$*" >&2
  exit 1
}

if [[ $# -eq 1 && ( "$1" == "-h" || "$1" == "--help" ) ]]; then
  usage
  exit 0
fi
[[ $# -ge 2 ]] || { usage; exit 2; }
TARGET="$1"
PROVIDER="$2"
shift 2

TEST_NAME="basic-plan-auto"
if [[ $# -gt 0 && "$1" != --* ]]; then
  TEST_NAME="$1"
  shift
fi

API_URL=""
WORKSPACE_PATH=""
TIMEOUT_MS="900000"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --api-url)
      [[ $# -ge 2 ]] || fail "--api-url requires a value"
      API_URL="$2"
      shift 2
      ;;
    --workspace-path)
      [[ $# -ge 2 ]] || fail "--workspace-path requires a value"
      WORKSPACE_PATH="$2"
      shift 2
      ;;
    --timeout-ms)
      [[ $# -ge 2 ]] || fail "--timeout-ms requires a value"
      TIMEOUT_MS="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

[[ "${TEST_NAME}" =~ ^[A-Za-z0-9._-]+$ ]] || fail "test-name contains unsupported characters"
[[ "${PROVIDER}" =~ ^[A-Za-z0-9._-]+$ ]] || fail "provider contains unsupported characters"
[[ "${TIMEOUT_MS}" =~ ^[0-9]+$ ]] || fail "--timeout-ms must be an integer"
(( TIMEOUT_MS >= 30000 )) || fail "--timeout-ms must be at least 30000"

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
RUNNER="${ROOT_DIR}/scripts/runners/${TEST_NAME}.mjs"
[[ -f "${RUNNER}" ]] || fail "runner not found: scripts/runners/${TEST_NAME}.mjs"
command -v node >/dev/null 2>&1 || fail "missing required command: node"

runner_args=(--provider "${PROVIDER}" --timeout-ms "${TIMEOUT_MS}")
if [[ -n "${WORKSPACE_PATH}" ]]; then
  runner_args+=(--workspace-path "${WORKSPACE_PATH}")
fi

if [[ "${TARGET}" =~ ^https?:// ]]; then
  [[ -z "${API_URL}" ]] || fail "--api-url cannot be used when target is already a URL"
  API_URL="${TARGET}"
  exec env SWARM_RUNNER_TOKEN="${SWARM_RUNNER_TOKEN:-}" node "${RUNNER}" --api-url "${API_URL}" "${runner_args[@]}"
fi

command -v ssh >/dev/null 2>&1 || fail "missing required command: ssh"
command -v scp >/dev/null 2>&1 || fail "missing required command: scp"
quote_remote() {
  printf "'"
  printf '%s' "$1" | sed "s/'/'\\\\''/g"
  printf "'"
}
API_URL="${API_URL:-http://127.0.0.1:7781}"
REMOTE_RUNNER="$(ssh "${TARGET}" 'mktemp -t swarm-runner.XXXXXX.mjs')"
[[ -n "${REMOTE_RUNNER}" ]] || fail "target did not return a temporary runner path"
cleanup() {
  local remote_path
  remote_path="$(quote_remote "${REMOTE_RUNNER}")"
  ssh "${TARGET}" "bash -c 'rm -f -- \"\$1\"' bash ${remote_path}" >/dev/null 2>&1 || true
}
trap cleanup EXIT
scp -q "${RUNNER}" "${TARGET}:${REMOTE_RUNNER}"

remote_runner="$(quote_remote "${REMOTE_RUNNER}")"
remote_api_url="$(quote_remote "${API_URL}")"
remote_provider="$(quote_remote "${PROVIDER}")"
remote_timeout="$(quote_remote "${TIMEOUT_MS}")"
remote_workspace="$(quote_remote "${WORKSPACE_PATH}")"
remote_script='IFS= read -r token || true; export SWARM_RUNNER_TOKEN="$token"; if [ -n "$5" ]; then exec node "$1" --api-url "$2" --provider "$3" --timeout-ms "$4" --workspace-path "$5"; fi; exec node "$1" --api-url "$2" --provider "$3" --timeout-ms "$4"'
printf '%s\n' "${SWARM_RUNNER_TOKEN:-}" | ssh "${TARGET}" \
  "bash -c '${remote_script}' bash ${remote_runner} ${remote_api_url} ${remote_provider} ${remote_timeout} ${remote_workspace}"
