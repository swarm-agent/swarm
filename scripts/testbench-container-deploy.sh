#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/testbench-container-deploy.sh <deploy|status|stop|tunnel|pool-status> [--source-worktree <path>] [--no-key-sync]

Map one clean swarm-go checkout/worktree to a stable slot in the bounded remote
systemd-nspawn test pool. The broker reuses an existing lane for that worktree or
assigns one inactive slot. Two slots may run in parallel; a five-minute server
timer stops slots idle for 30 minutes.

Candidate source is transported as a verified Git bundle and built only on the
testbench server. Google-managed Fireworks credentials remain in the private
broker boundary. Host swarm.service is never a deployment target.
EOF
}

fail() { printf 'testbench-container-deploy: %s\n' "$*" >&2; exit 1; }

[[ $# -ge 1 ]] || { usage; exit 2; }
action="$1"; shift
case "$action" in deploy|status|stop|tunnel|pool-status) ;; -h|--help) usage; exit 0 ;; *) usage; exit 2 ;; esac
sync_key=true
source_worktree=""
while (($#)); do
  case "$1" in
    --no-key-sync) sync_key=false; shift ;;
    --source-worktree) [[ $# -ge 2 ]] || fail '--source-worktree requires a path'; source_worktree="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
# shellcheck source=scripts/lib-testbench-e2e.sh
source "$root/scripts/lib-testbench-e2e.sh"
swarm_testbench_load_env "$root" || exit 1
swarm_testbench_validate_env || exit 1

readonly broker='/usr/local/sbin/swarm-testbench-admin'
readonly inbox_root='/var/cache/swarm-test-container/inbox'
broker_action() { ssh "$SWARM_PRIMARY_SSH" "sudo -n $broker $1"; }

if [[ "$action" == pool-status ]]; then
  [[ -z "$source_worktree" ]] || fail 'pool-status does not accept --source-worktree'
  broker_action test-container-pool-status
  exit 0
fi

candidate_root="$root"
if [[ -n "$source_worktree" ]]; then
  [[ -d "$source_worktree" ]] || fail 'source worktree does not exist'
  candidate_root="$(git -C "$source_worktree" rev-parse --show-toplevel 2>/dev/null)" || fail 'source worktree is not a Git checkout'
fi
root_common="$(git -C "$root" rev-parse --path-format=absolute --git-common-dir)" || fail 'script checkout has no Git authority'
candidate_common="$(git -C "$candidate_root" rev-parse --path-format=absolute --git-common-dir)" || fail 'source worktree has no Git authority'
[[ "$candidate_common" == "$root_common" ]] || fail 'source worktree belongs to another repository'
lane_id="$(printf '%s\0%s' "$root_common" "$candidate_root" | sha256sum | cut -c1-16)"
[[ "$lane_id" =~ ^[0-9a-f]{16}$ ]] || fail 'could not derive a stable lane identity'

pool="$(broker_action test-container-pool-status)" || fail 'could not inspect the test container pool'
slot="$(awk -v lane="$lane_id" '{ for (i=1;i<=NF;i++) { split($i,p,"="); if (p[1]=="slot") s=p[2]; if (p[1]=="lane_id") l=p[2] } if (l==lane) { print s; exit } }' <<<"$pool")"
if [[ "$action" != deploy ]]; then
  [[ "$slot" =~ ^[12]$ ]] || fail 'no lane is assigned for this worktree; deploy it first'
fi

if [[ "$action" != deploy ]]; then
  remote_desktop_port="$((5654 + slot))"
  remote_api_port="$((7880 + slot))"
  local_desktop_port="$((15654 + slot))"
  local_api_port="$((17880 + slot))"
fi
slot_action() { broker_action "test-container-$1-$slot"; }

case "$action" in
  status)
    slot_action status
    exit 0
    ;;
  stop)
    slot_action stop
    exit 0
    ;;
  tunnel)
    slot_action status >/dev/null
    slot_action touch >/dev/null
    printf 'slot=%s\nlane_id=%s\ndesktop_url=http://127.0.0.1:%s\napi_url=http://127.0.0.1:%s\n' "$slot" "$lane_id" "$local_desktop_port" "$local_api_port"
    ssh -N -o ExitOnForwardFailure=yes \
      -L "$local_desktop_port:127.0.0.1:$remote_desktop_port" \
      -L "$local_api_port:127.0.0.1:$remote_api_port" \
      "$SWARM_PRIMARY_SSH" &
    tunnel_pid=$!
    heartbeat() {
      while kill -0 "$tunnel_pid" 2>/dev/null; do
        sleep 60
        kill -0 "$tunnel_pid" 2>/dev/null || break
        slot_action touch >/dev/null 2>&1 || break
      done
    }
    heartbeat & heartbeat_pid=$!
    cleanup() { kill "$heartbeat_pid" "$tunnel_pid" 2>/dev/null || true; wait "$heartbeat_pid" "$tunnel_pid" 2>/dev/null || true; }
    trap cleanup EXIT INT TERM
    wait "$tunnel_pid"
    exit $?
    ;;
  deploy) ;;
esac

git -C "$candidate_root" diff --quiet --ignore-submodules -- || fail 'deploy requires committed changes only'
git -C "$candidate_root" diff --cached --quiet --ignore-submodules -- || fail 'deploy requires committed changes only'
[[ -z "$(git -C "$candidate_root" ls-files --others --exclude-standard)" ]] || fail 'deploy requires no untracked source files'
head="$(git -C "$candidate_root" rev-parse --verify HEAD)"
bundle="$(mktemp "${TMPDIR:?TMPDIR must be set}/swarm-test-container-${head:0:12}.XXXXXX.bundle")"
trap 'rm -f -- "$bundle"' EXIT
git -C "$candidate_root" bundle create "$bundle" HEAD >/dev/null
git bundle verify "$bundle" >/dev/null

claim_root="$inbox_root/claims"
ssh "$SWARM_PRIMARY_SSH" "test -d '$claim_root' && cat > '$claim_root/$lane_id.bundle' && printf '%s\\n' '$head' > '$claim_root/$lane_id.head' && printf '%s\\n' '$lane_id' > '$claim_root/$lane_id.claim'" <"$bundle"
if [[ "$sync_key" == true ]]; then
  broker_action test-container-deploy-auto
else
  broker_action test-container-deploy-no-key-auto
fi
pool="$(broker_action test-container-pool-status)"
slot="$(awk -v lane="$lane_id" '{ for (i=1;i<=NF;i++) { split($i,p,"="); if (p[1]=="slot") s=p[2]; if (p[1]=="lane_id") l=p[2] } if (l==lane) { print s; exit } }' <<<"$pool")"
[[ "$slot" =~ ^[12]$ ]] || fail 'server did not assign the requested worktree lane'
printf 'slot=%s\nnext=%s tunnel --source-worktree %s\n' "$slot" "$0" "$candidate_root"
