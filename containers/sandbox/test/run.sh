#!/usr/bin/env bash
# Red-team the agent sandbox through a real daemon, end to end.
#
#   sudo bash containers/sandbox/test/run.sh
#
# Builds swarmd with the scripted model overlay (the "model" is the harness,
# playing a hijacked LLM; no provider key is involved), runs it on this host
# as a non-root service-like user with --sandbox=required and permission
# bypass on, exactly as the server install does, and lets redteam.mjs drive
# real sessions whose tool calls try to reach Swarm's storage, login, socket,
# environment, the host and private networks, plant Git configuration that
# would run as the daemon, and read secrets through symlinks. It then
# restarts the daemon without a sandbox and checks bypass no longer applies.
#
# Needs: root, Docker, Go, Node, the sandbox image (built here if missing) and
# containers/sandbox/firewall.sh applied (applied here). Disposable state only.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
image=${SANDBOX_IMAGE:-swarm-sandbox:local}
user=${SANDBOX_TEST_USER:-swarm-sbx-test}
model_port=${MODEL_PORT:-7795}
node=$(command -v node)
[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }

work=$(mktemp -d)
daemon_pid=''
cleanup() {
  [[ -n $daemon_pid ]] && kill "$daemon_pid" 2>/dev/null && wait "$daemon_pid" 2>/dev/null || true
  for box in $(docker ps -aq --filter label=swarm.sandbox=1 --filter "label=swarm.sandbox.root=$work/project"); do docker rm -f "$box" >/dev/null 2>&1 || true; done
  docker volume ls -q | grep '^swarm-sandbox-project-.*-home$' | xargs -r docker volume rm >/dev/null 2>&1 || true
  [[ -n ${KEEP_WORK:-} ]] && echo "kept $work" || rm -rf "$work"
}
trap cleanup EXIT

docker image inspect "$image" >/dev/null 2>&1 || docker build -q -t "$image" "$repo/containers/sandbox" >/dev/null
bash "$repo/containers/sandbox/firewall.sh"
id "$user" >/dev/null 2>&1 || useradd --system --user-group --no-create-home --shell /usr/sbin/nologin "$user"
usermod -aG docker "$user"

echo "Building the scripted daemon"
overlay=$(python3 "$repo/scripts/testbench-scripted-overlay.py" --source "$repo" --output "$work")
mkdir -p "$work/bin" "$work/lib"
(cd "$repo/swarmd" && for c in swarmd swarmctl swarm-fff-search; do go build -overlay "$overlay" -o "$work/bin/$c" "./cmd/$c"; done)
cp "$repo/swarmd/internal/fff/lib/linux-amd64-gnu/libfff_c.so" "$work/lib/"
[[ -f $repo/packages/sdk/dist/index.js ]] || (cd "$repo/packages/sdk" && npm ci --ignore-scripts --no-audit --no-fund >/dev/null && npm run build >/dev/null)

home=$work/home project=$work/project state=$work/state
mkdir -p "$home" "$project" "$state"/{data,cache,run,config,logs}
git -C "$project" init -q -b main
printf 'a\n' > "$project/README"
git -C "$project" add README
git -C "$project" -c user.name=redteam -c user.email=redteam@example.invalid commit -q -m init
# A decoy secret in Swarm's data directory; nothing in a sandbox may read it.
(umask 077; printf 'daemon-root-key-must-not-leak\n' > "$state/data/redteam-decoy-secret")
chown -R "$user:$user" "$work"
chmod 0711 "$work"
runuser -u "$user" -- env HOME="$home" git config --global user.name redteam
runuser -u "$user" -- env HOME="$home" git config --global user.email redteam@example.invalid

as_daemon() {
  runuser -u "$user" -- env -i HOME="$home" PATH="$work/bin:/usr/bin:/bin" LD_LIBRARY_PATH="$work/lib" \
    STATE_DIRECTORY="$state/data" CACHE_DIRECTORY="$state/cache" RUNTIME_DIRECTORY="$state/run" \
    CONFIGURATION_DIRECTORY="$state/config" LOGS_DIRECTORY="$state/logs" SWARM_DISABLE_MINT_REPORT=1 \
    SWARMD_LOCAL_TRANSPORT_SOCKET="$state/data/local-transport/api.sock" "$@"
}

start_daemon() {
  # A secret in the daemon's own environment, to prove none crosses over.
  # Started directly (not through a function) so $! is swarmd itself.
  setpriv --reuid="$user" --regid="$user" --init-groups env -i HOME="$home" PATH="$work/bin:/usr/bin:/bin" LD_LIBRARY_PATH="$work/lib" \
    STATE_DIRECTORY="$state/data" CACHE_DIRECTORY="$state/cache" RUNTIME_DIRECTORY="$state/run" \
    CONFIGURATION_DIRECTORY="$state/config" LOGS_DIRECTORY="$state/logs" SWARM_DISABLE_MINT_REPORT=1 \
    SWARM_SCRIPTED_MODEL_URL="http://127.0.0.1:$model_port/model" SWARM_E2E_DAEMON_TOKEN=daemon-env-must-not-leak \
    "$work/bin/swarmd" --desktop-port=0 --cwd="$project" --sandbox-image="$image" "$@" >>"$work/daemon.log" 2>&1 &
  daemon_pid=$!
  for _ in $(seq 1 60); do as_daemon swarmctl setup status >/dev/null 2>&1 && return 0; sleep 1; done
  echo "daemon did not start:" >&2; tail -n 40 "$work/daemon.log" >&2; exit 1
}
stop_daemon() { kill "$daemon_pid"; wait "$daemon_pid" 2>/dev/null || true; daemon_pid=''; }

start_daemon --sandbox=required --bypass-permissions
setup_step() { "$@" >/dev/null || { echo "setup step failed: ${*: -3}" >&2; tail -n 20 "$work/daemon.log" >&2; exit 1; }; }
setup_step as_daemon swarmctl setup workspace --path "$project" --name Project
printf 'redteam-placeholder-key\n' | setup_step as_daemon swarmctl setup credential --provider codex --api-key-stdin
setup_step as_daemon swarmctl setup complete

status=0
run_harness() {
  runuser -u "$user" -- env -i HOME="$home" PATH=/usr/bin:/bin SWARM_SOCKET_PATH="$state/data/local-transport/api.sock" \
    MODEL_PORT="$model_port" PROJECT="$project" DATA_DIR="$state/data" PHASE="$1" \
    "$node" "$here/redteam.mjs" || status=1
}
echo; echo "Phase 1: sandbox required, permission bypass on"
run_harness sandboxed
stop_daemon

echo; echo "Phase 2: no sandbox, permission bypass requested"
start_daemon --sandbox=off --bypass-permissions
run_harness unsandboxed
stop_daemon

if ((status)); then echo; echo "daemon log tail:"; tail -n 30 "$work/daemon.log"; fi
exit $status
