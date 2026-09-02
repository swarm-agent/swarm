#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/testbench-container-deploy.sh <deploy|status|stop|tunnel> [--no-key-sync]

Deploy the current committed checkout into the dedicated testbench systemd-nspawn
machine. Source is transported as a verified Git bundle, built in the container,
and installed only under container-local system paths. The host's canonical
Swarm installation and release-gate paths are never deployment targets.

The root-owned fixed broker must provide these exact no-argument actions:
  test-container-prepare, test-container-start, test-container-stop,
  test-container-status, test-container-sync-fireworks
EOF
}

fail() { printf 'testbench-container-deploy: %s\n' "$*" >&2; exit 1; }

[[ $# -ge 1 ]] || { usage; exit 2; }
action="$1"; shift
case "$action" in deploy|status|stop|tunnel) ;; -h|--help) usage; exit 0 ;; *) usage; exit 2 ;; esac
sync_key=true
while (($#)); do
  case "$1" in --no-key-sync) sync_key=false ;; -h|--help) usage; exit 0 ;; *) fail "unknown argument: $1" ;; esac
  shift
done

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib-testbench-e2e.sh
source "$root/scripts/lib-testbench-e2e.sh"
swarm_testbench_load_env "$root" || exit 1
swarm_testbench_validate_env || exit 1

readonly broker='/usr/local/sbin/swarm-testbench-admin'
readonly inbox='.local/share/swarm/test-container-inbox'
readonly remote_desktop_port=5655
readonly remote_api_port=7881
readonly local_desktop_port=15655
readonly local_api_port=17881
broker_action() { ssh "$SWARM_PRIMARY_SSH" "sudo -n $broker $1"; }

case "$action" in
  status) broker_action test-container-status; exit 0 ;;
  stop) broker_action test-container-stop; exit 0 ;;
  tunnel)
    printf 'desktop_url=http://127.0.0.1:%s\napi_url=http://127.0.0.1:%s\n' "$local_desktop_port" "$local_api_port"
    exec ssh -N \
      -L "$local_desktop_port:127.0.0.1:$remote_desktop_port" \
      -L "$local_api_port:127.0.0.1:$remote_api_port" \
      "$SWARM_PRIMARY_SSH"
    ;;
  deploy) ;;
esac

git -C "$root" diff --quiet --ignore-submodules -- || fail 'deploy requires committed changes only'
git -C "$root" diff --cached --quiet --ignore-submodules -- || fail 'deploy requires committed changes only'
[[ -z "$(git -C "$root" ls-files --others --exclude-standard)" ]] || fail 'deploy requires no untracked source files'
head="$(git -C "$root" rev-parse --verify HEAD)"
bundle="$(mktemp "${TMPDIR:?TMPDIR must be set}/swarm-test-container-${head:0:12}.XXXXXX.bundle")"
trap 'rm -f -- "$bundle"' EXIT
git -C "$root" bundle create "$bundle" HEAD >/dev/null
git bundle verify "$bundle" >/dev/null

ssh "$SWARM_PRIMARY_SSH" "install -d -m 0700 $inbox && cat > $inbox/candidate.bundle && printf '%s\\n' '$head' > $inbox/candidate-head" <"$bundle"
broker_action test-container-prepare
broker_action test-container-start
if [[ "$sync_key" == true ]]; then broker_action test-container-sync-fireworks; fi
broker_action test-container-status
printf 'desktop_url=http://127.0.0.1:%s\napi_url=http://127.0.0.1:%s\n' "$local_desktop_port" "$local_api_port"
printf 'next=./scripts/testbench-container-deploy.sh tunnel\n'
