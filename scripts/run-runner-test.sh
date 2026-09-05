#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage: scripts/run-runner-test.sh <target> <provider> [test-name] [--api-url <url>] [--workspace-path <path>] [--linked-workspace-path <path>] [--model <id>] [--thinking <level>] [--action-model <id>] [--action-thinking <level>] [--plan-model <id>] [--plan-thinking <level>] [--coder-model <id>] [--coder-thinking <level>] [--designer-model <id>] [--designer-thinking <level>] [--browser-executable <path>] [--stage <name>] [--session-id <id>] [--initial-run-id <id>] [--artifact-id <id>] [--desktop-path <path>] [--source-session-id <id>] [--source-collection-id <id>] [--source-variant-id <id>] [--source-event-seq <seq>] [--timeout-ms <ms>]

Runs a checked-in runner test against an already-running Swarm target.

Arguments:
  target       SSH alias, or an http(s) URL for direct execution
  provider     Exact provider id paired with explicit role models
  test-name    Runner under scripts/runners without .mjs (default: basic-plan-auto)

Options:
  --api-url          API URL used from the target host (SSH default: http://127.0.0.1:7781)
  --workspace-path   Existing bound workspace to use (default: first bound workspace)
  --linked-workspace-path  Optional second bound workspace for multi-repository runners
  --model            Explicit shared model for TUI-compatible runners
  --thinking         Explicit shared thinking level for TUI-compatible runners
  --action-model     Required exact Auto/action model for role-aware runners
  --action-thinking  Required Auto/action thinking for role-aware runners
  --plan-model       Required exact Plan model for role-aware runners
  --plan-thinking    Required Plan thinking for role-aware runners
  --coder-model      Required exact Coder model for task-program runners
  --coder-thinking   Required Coder thinking for task-program runners
  --designer-model   Required exact Designer model for task-program runners
  --designer-thinking Required Designer thinking for task-program runners
  --browser-executable Existing trusted local browser path for browser-backed runners
  --stage            Optional resumable stage passed to runners that support it
  --session-id       Existing destination session used by a resumed runner stage
  --initial-run-id   Existing initial run used by a same-session resumed runner stage
  --artifact-id      Exact Artifact V3 ID used by a resumed runner stage
  --desktop-path     Absolute Desktop conversation path used by a resumed runner stage
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
ACTION_MODEL=""
ACTION_THINKING=""
PLAN_MODEL=""
PLAN_THINKING=""
CODER_MODEL=""
CODER_THINKING=""
DESIGNER_MODEL=""
DESIGNER_THINKING=""
BROWSER_EXECUTABLE=""
STAGE=""
SESSION_ID=""
INITIAL_RUN_ID=""
ARTIFACT_ID=""
DESKTOP_PATH=""
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
    --action-model) [[ $# -ge 2 ]] || fail "--action-model requires a value"; ACTION_MODEL="$2"; shift 2 ;;
    --action-thinking) [[ $# -ge 2 ]] || fail "--action-thinking requires a value"; ACTION_THINKING="$2"; shift 2 ;;
    --plan-model) [[ $# -ge 2 ]] || fail "--plan-model requires a value"; PLAN_MODEL="$2"; shift 2 ;;
    --plan-thinking) [[ $# -ge 2 ]] || fail "--plan-thinking requires a value"; PLAN_THINKING="$2"; shift 2 ;;
    --coder-model) [[ $# -ge 2 ]] || fail "--coder-model requires a value"; CODER_MODEL="$2"; shift 2 ;;
    --coder-thinking) [[ $# -ge 2 ]] || fail "--coder-thinking requires a value"; CODER_THINKING="$2"; shift 2 ;;
    --designer-model) [[ $# -ge 2 ]] || fail "--designer-model requires a value"; DESIGNER_MODEL="$2"; shift 2 ;;
    --designer-thinking) [[ $# -ge 2 ]] || fail "--designer-thinking requires a value"; DESIGNER_THINKING="$2"; shift 2 ;;
    --browser-executable) [[ $# -ge 2 ]] || fail "--browser-executable requires a value"; BROWSER_EXECUTABLE="$2"; shift 2 ;;
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
    --initial-run-id)
      [[ $# -ge 2 ]] || fail "--initial-run-id requires a value"
      INITIAL_RUN_ID="$2"
      shift 2
      ;;
    --artifact-id)
      [[ $# -ge 2 ]] || fail "--artifact-id requires a value"
      ARTIFACT_ID="$2"
      shift 2
      ;;
    --desktop-path)
      [[ $# -ge 2 ]] || fail "--desktop-path requires a value"
      DESKTOP_PATH="$2"
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
[[ -n "${ACTION_MODEL}" ]] && runner_args+=(--action-model "${ACTION_MODEL}")
[[ -n "${ACTION_THINKING}" ]] && runner_args+=(--action-thinking "${ACTION_THINKING}")
[[ -n "${PLAN_MODEL}" ]] && runner_args+=(--plan-model "${PLAN_MODEL}")
[[ -n "${PLAN_THINKING}" ]] && runner_args+=(--plan-thinking "${PLAN_THINKING}")
[[ -n "${CODER_MODEL}" ]] && runner_args+=(--coder-model "${CODER_MODEL}")
[[ -n "${CODER_THINKING}" ]] && runner_args+=(--coder-thinking "${CODER_THINKING}")
[[ -n "${DESIGNER_MODEL}" ]] && runner_args+=(--designer-model "${DESIGNER_MODEL}")
[[ -n "${DESIGNER_THINKING}" ]] && runner_args+=(--designer-thinking "${DESIGNER_THINKING}")
[[ -n "${BROWSER_EXECUTABLE}" ]] && runner_args+=(--browser-executable "${BROWSER_EXECUTABLE}")
if [[ -n "${STAGE}" ]]; then
  runner_args+=(--stage "${STAGE}")
fi
if [[ -n "${SESSION_ID}" ]]; then
  runner_args+=(--session-id "${SESSION_ID}")
fi
if [[ -n "${INITIAL_RUN_ID}" ]]; then
  runner_args+=(--initial-run-id "${INITIAL_RUN_ID}")
fi
if [[ -n "${ARTIFACT_ID}" ]]; then
  runner_args+=(--artifact-id "${ARTIFACT_ID}")
fi
if [[ -n "${DESKTOP_PATH}" ]]; then
  runner_args+=(--desktop-path "${DESKTOP_PATH}")
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
remote_action_model="$(quote_remote "${ACTION_MODEL}")"
remote_action_thinking="$(quote_remote "${ACTION_THINKING}")"
remote_plan_model="$(quote_remote "${PLAN_MODEL}")"
remote_plan_thinking="$(quote_remote "${PLAN_THINKING}")"
remote_coder_model="$(quote_remote "${CODER_MODEL}")"
remote_coder_thinking="$(quote_remote "${CODER_THINKING}")"
remote_designer_model="$(quote_remote "${DESIGNER_MODEL}")"
remote_designer_thinking="$(quote_remote "${DESIGNER_THINKING}")"
remote_browser_executable="$(quote_remote "${BROWSER_EXECUTABLE}")"
remote_stage="$(quote_remote "${STAGE}")"
remote_session="$(quote_remote "${SESSION_ID}")"
remote_source_session="$(quote_remote "${SOURCE_SESSION_ID}")"
remote_source_collection="$(quote_remote "${SOURCE_COLLECTION_ID}")"
remote_source_variant="$(quote_remote "${SOURCE_VARIANT_ID}")"
remote_source_event_seq="$(quote_remote "${SOURCE_EVENT_SEQ}")"
remote_script='IFS= read -r token || true; export SWARM_RUNNER_TOKEN="$token"; export TMPDIR="${TMPDIR:-$(dirname -- "$1")}"; printf "%s\\n" "$$" >"$1.pid"; args=(--api-url "$2" --provider "$3" --timeout-ms "$4"); if [ -n "$5" ]; then args+=(--workspace-path "$5"); fi; if [ -n "$6" ]; then args+=(--linked-workspace-path "$6"); fi; if [ -n "$7" ]; then args+=(--model "$7"); fi; if [ -n "$8" ]; then args+=(--thinking "$8"); fi; if [ -n "$9" ]; then args+=(--stage "$9"); fi; if [ -n "${10}" ]; then args+=(--session-id "${10}"); fi; if [ -n "${11}" ]; then args+=(--source-session-id "${11}"); fi; if [ -n "${12}" ]; then args+=(--source-collection-id "${12}"); fi; if [ -n "${13}" ]; then args+=(--source-variant-id "${13}"); fi; if [ -n "${14}" ]; then args+=(--source-event-seq "${14}"); fi; if [ -n "${15}" ]; then args+=(--action-model "${15}"); fi; if [ -n "${16}" ]; then args+=(--action-thinking "${16}"); fi; if [ -n "${17}" ]; then args+=(--plan-model "${17}"); fi; if [ -n "${18}" ]; then args+=(--plan-thinking "${18}"); fi; if [ -n "${19}" ]; then args+=(--coder-model "${19}"); fi; if [ -n "${20}" ]; then args+=(--coder-thinking "${20}"); fi; if [ -n "${21}" ]; then args+=(--designer-model "${21}"); fi; if [ -n "${22}" ]; then args+=(--designer-thinking "${22}"); fi; if [ -n "${23}" ]; then args+=(--browser-executable "${23}"); fi; exec node "$1" "${args[@]}"'
printf '%s\n' "${SWARM_RUNNER_TOKEN:-}" | ssh "${TARGET}" \
  "bash -c '${remote_script}' bash ${remote_runner} ${remote_api_url} ${remote_provider} ${remote_timeout} ${remote_workspace} ${remote_linked_workspace} ${remote_model} ${remote_thinking} ${remote_stage} ${remote_session} ${remote_source_session} ${remote_source_collection} ${remote_source_variant} ${remote_source_event_seq} ${remote_action_model} ${remote_action_thinking} ${remote_plan_model} ${remote_plan_thinking} ${remote_coder_model} ${remote_coder_thinking} ${remote_designer_model} ${remote_designer_thinking} ${remote_browser_executable}"
