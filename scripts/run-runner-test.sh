#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage: scripts/run-runner-test.sh <target> <provider> [test-name] [--api-url <url>] [--workspace-path <path>] [--linked-workspace-path <path>] [--model <id>] [--thinking <level>] [--stage <name>] [--session-id <id>] [--source-session-id <id>] [--source-collection-id <id>] [--source-variant-id <id>] [--source-event-seq <seq>] [--timeout-ms <ms>]

Runs a checked-in runner test against an already-running Swarm target.

Arguments:
  target       SSH alias, or an http(s) URL for direct execution
  provider     Provider id whose catalog recommendations select Plan and Auto models
  test-name    Runner under scripts/runners without .mjs (default: basic-plan-auto)

Options:
  --api-url          API URL used from the target host (SSH default: http://127.0.0.1:7781)
  --workspace-path   Existing bound workspace to use (default: first bound workspace)
  --linked-workspace-path  Optional second bound workspace for multi-repository runners
  --model            Optional exact model override passed to runners that support it
  --thinking         Optional thinking override passed to runners that support it
  --stage            Optional resumable stage passed to runners that support it
  --session-id       Existing destination session used by a resumed runner stage
  --source-session-id     Exact source artifact session for supported resumed stages
  --source-collection-id  Exact source artifact collection for supported resumed stages
  --source-variant-id     Exact source artifact variant for supported resumed stages
  --source-event-seq      Exact positive source artifact event sequence
  --timeout-ms       Per-stage runner wait budget (default: 600000; maximum: 600000)

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
LINKED_WORKSPACE_PATH=""
MODEL=""
THINKING=""
STAGE=""
SESSION_ID=""
SOURCE_SESSION_ID=""
SOURCE_COLLECTION_ID=""
SOURCE_VARIANT_ID=""
SOURCE_EVENT_SEQ=""
TIMEOUT_MS="600000"
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
    --linked-workspace-path)
      [[ $# -ge 2 ]] || fail "--linked-workspace-path requires a value"
      LINKED_WORKSPACE_PATH="$2"
      shift 2
      ;;
    --model)
      [[ $# -ge 2 ]] || fail "--model requires a value"
      MODEL="$2"
      shift 2
      ;;
    --thinking)
      [[ $# -ge 2 ]] || fail "--thinking requires a value"
      THINKING="$2"
      shift 2
      ;;
    --stage)
      [[ $# -ge 2 ]] || fail "--stage requires a value"
      STAGE="$2"
      shift 2
      ;;
    --session-id)
      [[ $# -ge 2 ]] || fail "--session-id requires a value"
      SESSION_ID="$2"
      shift 2
      ;;
    --source-session-id)
      [[ $# -ge 2 ]] || fail "--source-session-id requires a value"
      SOURCE_SESSION_ID="$2"
      shift 2
      ;;
    --source-collection-id)
      [[ $# -ge 2 ]] || fail "--source-collection-id requires a value"
      SOURCE_COLLECTION_ID="$2"
      shift 2
      ;;
    --source-variant-id)
      [[ $# -ge 2 ]] || fail "--source-variant-id requires a value"
      SOURCE_VARIANT_ID="$2"
      shift 2
      ;;
    --source-event-seq)
      [[ $# -ge 2 ]] || fail "--source-event-seq requires a value"
      SOURCE_EVENT_SEQ="$2"
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
(( TIMEOUT_MS <= 600000 )) || fail "--timeout-ms must not exceed 600000; split longer proofs into resumable stages"
if [[ -n "${SOURCE_EVENT_SEQ}" ]]; then
  [[ "${SOURCE_EVENT_SEQ}" =~ ^[1-9][0-9]*$ ]] || fail "--source-event-seq must be a positive integer"
fi

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
RUNNER="${ROOT_DIR}/scripts/runners/${TEST_NAME}.mjs"
[[ -f "${RUNNER}" ]] || fail "runner not found: scripts/runners/${TEST_NAME}.mjs"
command -v node >/dev/null 2>&1 || fail "missing required command: node"

runner_args=(--provider "${PROVIDER}" --timeout-ms "${TIMEOUT_MS}")
if [[ -n "${WORKSPACE_PATH}" ]]; then
  runner_args+=(--workspace-path "${WORKSPACE_PATH}")
fi
if [[ -n "${LINKED_WORKSPACE_PATH}" ]]; then
  runner_args+=(--linked-workspace-path "${LINKED_WORKSPACE_PATH}")
fi
if [[ -n "${MODEL}" ]]; then
  runner_args+=(--model "${MODEL}")
fi
if [[ -n "${THINKING}" ]]; then
  runner_args+=(--thinking "${THINKING}")
fi
if [[ -n "${STAGE}" ]]; then
  runner_args+=(--stage "${STAGE}")
fi
if [[ -n "${SESSION_ID}" ]]; then
  runner_args+=(--session-id "${SESSION_ID}")
fi
if [[ -n "${SOURCE_SESSION_ID}" ]]; then
  runner_args+=(--source-session-id "${SOURCE_SESSION_ID}")
fi
if [[ -n "${SOURCE_COLLECTION_ID}" ]]; then
  runner_args+=(--source-collection-id "${SOURCE_COLLECTION_ID}")
fi
if [[ -n "${SOURCE_VARIANT_ID}" ]]; then
  runner_args+=(--source-variant-id "${SOURCE_VARIANT_ID}")
fi
if [[ -n "${SOURCE_EVENT_SEQ}" ]]; then
  runner_args+=(--source-event-seq "${SOURCE_EVENT_SEQ}")
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
  ssh "${TARGET}" "bash -c 'pid_file=\"\$1.pid\"; if [ -f \"\${pid_file}\" ]; then pid=\"\$(cat \"\${pid_file}\")\"; kill \"\${pid}\" 2>/dev/null || true; fi; rm -f -- \"\$1\" \"\${pid_file}\"' bash ${remote_path}" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM
scp -q "${RUNNER}" "${TARGET}:${REMOTE_RUNNER}"

remote_runner="$(quote_remote "${REMOTE_RUNNER}")"
remote_api_url="$(quote_remote "${API_URL}")"
remote_provider="$(quote_remote "${PROVIDER}")"
remote_timeout="$(quote_remote "${TIMEOUT_MS}")"
remote_workspace="$(quote_remote "${WORKSPACE_PATH}")"
remote_linked_workspace="$(quote_remote "${LINKED_WORKSPACE_PATH}")"
remote_model="$(quote_remote "${MODEL}")"
remote_thinking="$(quote_remote "${THINKING}")"
remote_stage="$(quote_remote "${STAGE}")"
remote_session="$(quote_remote "${SESSION_ID}")"
remote_source_session="$(quote_remote "${SOURCE_SESSION_ID}")"
remote_source_collection="$(quote_remote "${SOURCE_COLLECTION_ID}")"
remote_source_variant="$(quote_remote "${SOURCE_VARIANT_ID}")"
remote_source_event_seq="$(quote_remote "${SOURCE_EVENT_SEQ}")"
remote_script='IFS= read -r token || true; export SWARM_RUNNER_TOKEN="$token"; printf "%s\\n" "$$" >"$1.pid"; args=(--api-url "$2" --provider "$3" --timeout-ms "$4"); if [ -n "$5" ]; then args+=(--workspace-path "$5"); fi; if [ -n "$6" ]; then args+=(--linked-workspace-path "$6"); fi; if [ -n "$7" ]; then args+=(--model "$7"); fi; if [ -n "$8" ]; then args+=(--thinking "$8"); fi; if [ -n "$9" ]; then args+=(--stage "$9"); fi; if [ -n "${10}" ]; then args+=(--session-id "${10}"); fi; if [ -n "${11}" ]; then args+=(--source-session-id "${11}"); fi; if [ -n "${12}" ]; then args+=(--source-collection-id "${12}"); fi; if [ -n "${13}" ]; then args+=(--source-variant-id "${13}"); fi; if [ -n "${14}" ]; then args+=(--source-event-seq "${14}"); fi; exec node "$1" "${args[@]}"'
printf '%s\n' "${SWARM_RUNNER_TOKEN:-}" | ssh "${TARGET}" \
  "bash -c '${remote_script}' bash ${remote_runner} ${remote_api_url} ${remote_provider} ${remote_timeout} ${remote_workspace} ${remote_linked_workspace} ${remote_model} ${remote_thinking} ${remote_stage} ${remote_session} ${remote_source_session} ${remote_source_collection} ${remote_source_variant} ${remote_source_event_seq}"
