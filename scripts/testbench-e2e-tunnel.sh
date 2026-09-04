#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/testbench-e2e-tunnel.sh <check|run> [command [args...]]

Compatibility runner for this clean worktree's assigned slot in the bounded
broker-owned systemd-nspawn test pool.

Commands:
  check  Require the assigned slot to be active on this worktree's exact HEAD.
  run    Deploy the exact clean HEAD when the slot is absent or stale, open
         temporary loopback forwards for the assigned slot, export the test URLs,
         and run the supplied command.

The host Swarm service and host ports 5555/7781 are never candidate targets.
USAGE
}

fail() {
  printf 'testbench-e2e-tunnel: %s\n' "$*" >&2
  exit 1
}

[[ $# -ge 1 ]] || { usage; exit 2; }
action="$1"
shift
case "$action" in
  check) [[ $# -eq 0 ]] || { usage; exit 2; } ;;
  run) [[ $# -gt 0 ]] || { usage; exit 2; } ;;
  -h|--help) usage; exit 0 ;;
  *) usage; exit 2 ;;
esac

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
client="$root/scripts/testbench-container-deploy.sh"
# shellcheck source=scripts/lib-testbench-e2e.sh
source "$root/scripts/lib-testbench-e2e.sh"
swarm_testbench_load_env "$root" || exit 1
swarm_testbench_validate_env || exit 1

require_clean_checkout() {
  git -C "$root" diff --quiet --ignore-submodules -- || fail 'candidate testing requires committed changes only'
  git -C "$root" diff --cached --quiet --ignore-submodules -- || fail 'candidate testing requires committed changes only'
  [[ -z "$(git -C "$root" ls-files --others --exclude-standard)" ]] || fail 'candidate testing requires no untracked source files'
}

read_current_status() {
  local expected status deployed
  expected="$(git -C "$root" rev-parse --verify HEAD)"
  status="$($client status --source-worktree "$root" 2>/dev/null)" || return 1
  deployed="$(awk -F= '$1 == "candidate_head" { print $2 }' <<<"$status")"
  [[ "$deployed" == "$expected" ]] || return 1
  printf '%s\n' "$status"
}

require_clean_checkout
if ! status="$(read_current_status)"; then
  if [[ "$action" == check ]]; then
    fail 'assigned container slot is absent, inactive, or stale; run a checked-in test to deploy the exact clean HEAD'
  fi
  "$client" deploy --source-worktree "$root"
  status="$(read_current_status)" || fail 'deployment completed without an active exact-HEAD slot'
fi

slot="$(awk -F= '$1 == "slot" { print $2 }' <<<"$status")"
[[ "$slot" =~ ^[12]$ ]] || fail 'assigned container slot is invalid'
export SWARM_TESTBENCH_LOCAL_DESKTOP_PORT="$((15654 + slot))"
export SWARM_TESTBENCH_LOCAL_API_PORT="$((17880 + slot))"
export SWARM_REMOTE_DESKTOP_PORT="$((5654 + slot))"
export SWARM_TESTBENCH_REMOTE_API_PORT="$((7880 + slot))"

if swarm_testbench_port_open "$SWARM_TESTBENCH_LOCAL_DESKTOP_PORT"; then
  fail "local Desktop port $SWARM_TESTBENCH_LOCAL_DESKTOP_PORT is already in use"
fi
if swarm_testbench_port_open "$SWARM_TESTBENCH_LOCAL_API_PORT"; then
  fail "local API port $SWARM_TESTBENCH_LOCAL_API_PORT is already in use"
fi

printf 'testbench-e2e-tunnel: slot=%s exact_head=%s desktop=http://127.0.0.1:%s api=http://127.0.0.1:%s\n' \
  "$slot" "$(git -C "$root" rev-parse --verify HEAD)" "$SWARM_TESTBENCH_LOCAL_DESKTOP_PORT" "$SWARM_TESTBENCH_LOCAL_API_PORT"
if [[ "$action" == check ]]; then
  exit 0
fi

tunnel_log="$(mktemp "${TMPDIR:?TMPDIR must be set}/swarm-test-container-tunnel.XXXXXX.log")"
"$client" tunnel --source-worktree "$root" >"$tunnel_log" 2>&1 &
tunnel_pid=$!
cleanup() {
  kill "$tunnel_pid" 2>/dev/null || true
  wait "$tunnel_pid" 2>/dev/null || true
  rm -f -- "$tunnel_log"
}
trap cleanup EXIT INT TERM
swarm_testbench_wait_for_port "$SWARM_TESTBENCH_LOCAL_DESKTOP_PORT" "$tunnel_pid" || {
  tail -n 40 "$tunnel_log" >&2 || true
  fail 'container Desktop forward did not become ready'
}
swarm_testbench_wait_for_port "$SWARM_TESTBENCH_LOCAL_API_PORT" "$tunnel_pid" || {
  tail -n 40 "$tunnel_log" >&2 || true
  fail 'container API forward did not become ready'
}

export SWARM_DESKTOP_URL="http://127.0.0.1:$SWARM_TESTBENCH_LOCAL_DESKTOP_PORT"
export SWARM_PRIMARY_API_URL="http://127.0.0.1:$SWARM_TESTBENCH_LOCAL_API_PORT"
export SWARM_RUNNER_API_URL="$SWARM_DESKTOP_URL"
common_dir="$(git -C "$root" rev-parse --path-format=absolute --git-common-dir)"
common_root="$(dirname -- "$common_dir")"
if [[ -f "$common_root/web/package.json" ]]; then
  export SWARM_RUNNER_WEB_PACKAGE="$common_root/web/package.json"
fi
command_args=()
for argument in "$@"; do
  if [[ "$argument" == __SWARM_DESKTOP_URL__ ]]; then
    command_args+=("$SWARM_DESKTOP_URL")
  else
    command_args+=("$argument")
  fi
done
"${command_args[@]}"
