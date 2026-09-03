#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/testbench-container-deploy.sh <deploy|status|stop|check|ensure|run> [--no-key-sync] [command [args...]]

Deploy the current committed checkout into the dedicated testbench systemd-nspawn
machine. Source is transported as a verified Git bundle, built in the container,
and installed only under container-local system paths. The host's canonical
Swarm installation and release-gate paths are never deployment targets.

The root-owned fixed broker must provide these exact no-argument actions:
  test-container-prepare, test-container-start, test-container-stop,
  test-container-status, test-container-sync-fireworks

Commands:
  deploy  Build and start the current clean committed checkout in the container.
  status  Read the broker-owned container status and exact candidate HEAD.
  stop    Stop only the isolated test container.
  check   Read-only verification of the active container and exact current HEAD.
  ensure  Deploy the exact current clean committed HEAD only when needed.
  run     Ensure the exact current clean committed HEAD is deployed, open
          temporary loopback forwards, export the test URLs, then run the command.

There is intentionally no host-service fallback. Live candidate tests never use
host swarm.service or host ports 5555/7781.
EOF
}

fail() { printf 'testbench-container-deploy: %s\n' "$*" >&2; exit 1; }

[[ $# -ge 1 ]] || { usage; exit 2; }
action="$1"; shift
case "$action" in deploy|status|stop|check|ensure|run) ;; -h|--help) usage; exit 0 ;; *) usage; exit 2 ;; esac
sync_key=true
while [[ "$action" == deploy && $# -gt 0 ]]; do
  case "$1" in --no-key-sync) sync_key=false ;; -h|--help) usage; exit 0 ;; *) fail "unknown argument for deploy: $1" ;; esac
  shift
done
if [[ "$action" != run && $# -gt 0 ]]; then fail "$action does not accept a command"; fi

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib-testbench-e2e.sh
source "$root/scripts/lib-testbench-e2e.sh"
swarm_testbench_load_env "$root" || exit 1
swarm_testbench_validate_env || exit 1

readonly broker='/usr/local/sbin/swarm-testbench-admin'
readonly inbox='/var/cache/swarm-test-container/inbox'
readonly remote_desktop_port="$SWARM_REMOTE_DESKTOP_PORT"
readonly remote_api_port="$SWARM_TESTBENCH_REMOTE_API_PORT"
readonly local_desktop_port="$SWARM_TESTBENCH_LOCAL_DESKTOP_PORT"
readonly local_api_port="$SWARM_TESTBENCH_LOCAL_API_PORT"
broker_action() { ssh "$SWARM_PRIMARY_SSH" "sudo -n $broker $1"; }
current_head() { git -C "$root" rev-parse --verify HEAD; }
require_clean_checkout() {
  git -C "$root" diff --quiet --ignore-submodules -- || fail 'candidate testing requires committed changes only'
  git -C "$root" diff --cached --quiet --ignore-submodules -- || fail 'candidate testing requires committed changes only'
  [[ -z "$(git -C "$root" ls-files --others --exclude-standard)" ]] || fail 'candidate testing requires no untracked source files'
}
container_status() { broker_action test-container-status; }
current_candidate_status() {
  local expected status deployed
  expected="$(current_head)"
  status="$(container_status 2>/dev/null)" || return 1
  deployed="$(awk -F= '$1 == "candidate_head" { print $2 }' <<<"$status")"
  [[ "$deployed" == "$expected" ]] || return 1
  printf '%s\n' "$status"
}
require_current_candidate() {
  local expected status deployed
  expected="$(current_head)"
  status="$(container_status)" || fail 'isolated test container is not active; run deploy from a clean committed checkout'
  deployed="$(awk -F= '$1 == "candidate_head" { print $2 }' <<<"$status")"
  [[ "$deployed" == "$expected" ]] || fail "isolated test container is stale: deployed ${deployed:-unknown}, current HEAD $expected; run deploy from a clean committed checkout"
  printf '%s\n' "$status"
}

case "$action" in
  status) container_status; exit 0 ;;
  stop) broker_action test-container-stop; exit 0 ;;
  check) require_clean_checkout; require_current_candidate; exit 0 ;;
  ensure)
    require_clean_checkout
    if ! current_candidate_status >/dev/null; then
      printf 'testbench-container-deploy: container is stopped or stale; deploying current committed HEAD\n'
      "$0" deploy
    fi
    require_current_candidate
    exit 0
    ;;
  run)
    [[ $# -gt 0 ]] || fail 'run requires a command'
    "$0" ensure >/dev/null
    if swarm_testbench_port_open "$local_desktop_port" || swarm_testbench_port_open "$local_api_port"; then
      fail "local container tunnel ports $local_desktop_port/$local_api_port must both be free"
    fi
    tunnel_args=(-NT -o ExitOnForwardFailure=yes -o ServerAliveInterval=15 -o ServerAliveCountMax=3
      -L "$local_desktop_port:127.0.0.1:$remote_desktop_port"
      -L "$local_api_port:127.0.0.1:$remote_api_port")
    if [[ -n "$SWARM_TESTBENCH_REVERSE_LOCAL_PORT" ]]; then
      tunnel_args+=(-R "$SWARM_TESTBENCH_REVERSE_REMOTE_PORT:127.0.0.1:$SWARM_TESTBENCH_REVERSE_LOCAL_PORT")
    fi
    ssh "${tunnel_args[@]}" "$SWARM_PRIMARY_SSH" &
    tunnel_pid=$!
    cleanup() { kill "$tunnel_pid" 2>/dev/null || true; wait "$tunnel_pid" 2>/dev/null || true; }
    trap cleanup EXIT INT TERM
    swarm_testbench_wait_for_port "$local_desktop_port" "$tunnel_pid" || fail 'container Desktop forward did not become ready'
    swarm_testbench_wait_for_port "$local_api_port" "$tunnel_pid" || fail 'container API forward did not become ready'
    export SWARM_DESKTOP_URL="http://127.0.0.1:$local_desktop_port"
    export SWARM_PRIMARY_API_URL="http://127.0.0.1:$local_api_port"
    export SWARM_RUNNER_API_URL="$SWARM_DESKTOP_URL"
    set +e
    "$@"
    command_status=$?
    set -e
    exit "$command_status"
    ;;
  deploy) ;;
esac

require_clean_checkout
head="$(git -C "$root" rev-parse --verify HEAD)"
bundle="$(mktemp "${TMPDIR:?TMPDIR must be set}/swarm-test-container-${head:0:12}.XXXXXX.bundle")"
trap 'rm -f -- "$bundle"' EXIT
git -C "$root" bundle create "$bundle" HEAD >/dev/null
git bundle verify "$bundle" >/dev/null

ssh "$SWARM_PRIMARY_SSH" "test -d $inbox && cat > $inbox/candidate.bundle && printf '%s\\n' '$head' > $inbox/candidate-head" <"$bundle"
broker_action test-container-prepare
broker_action test-container-start
if [[ "$sync_key" == true ]]; then broker_action test-container-sync-fireworks; fi
broker_action test-container-status
printf 'desktop_url=http://127.0.0.1:%s\napi_url=http://127.0.0.1:%s\n' "$local_desktop_port" "$local_api_port"
printf 'next=./scripts/testbench-container-deploy.sh run <command...>\n'
