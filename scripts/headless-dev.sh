#!/usr/bin/env bash
# Run the bare headless box natively from this checkout: swarmd plus the
# examples/headless-app web client. No Docker, installer or published image.
# Development only, Linux only; it never touches an installed daemon.
#
# The daemon's state, config, cache, runtime, logs, worktrees and local
# secrets file all live under one dev root (absolute, outside $HOME):
#   SWARM_DEV_ROOT        default: $TMPDIR/swarm-headless-dev
#   SWARM_DEV_API_PORT    daemon API on 127.0.0.1 (default 7881)
#   SWARM_DEV_PEER_PORT   daemon peer transport on 127.0.0.1 (default 7891)
# The app listens on http://127.0.0.1:8443. The Desktop listener is off.
# Agents run without the sandbox (--sandbox=off): commands you approve run
# as you, with your own home directory. Use a disposable account or the
# sandboxed install (containers/headless) for anything you do not trust.
# Ctrl-C stops both processes; state in the dev root is kept between runs.
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

die() { echo "headless-dev: $*" >&2; exit 1; }

[[ "$(uname -s)" == Linux ]] || die "Linux only"
(( BASH_VERSINFO[0] > 5 || (BASH_VERSINFO[0] == 5 && BASH_VERSINFO[1] >= 1) )) || die "bash 5.1 or newer is required"

root="${SWARM_DEV_ROOT:-}"
if [[ -z "${root}" ]]; then
  [[ -n "${TMPDIR:-}" ]] || die "set SWARM_DEV_ROOT (or TMPDIR) to an absolute directory outside \$HOME"
  root="${TMPDIR%/}/swarm-headless-dev"
fi
[[ "${root}" == /* ]] || die "SWARM_DEV_ROOT must be absolute: ${root}"
root="${root%/}"
api_port="${SWARM_DEV_API_PORT:-7881}"
peer_port="${SWARM_DEV_PEER_PORT:-7891}"

socket="${root}/data/local-transport/api.sock"
(( ${#socket} <= 107 )) || die "socket path is ${#socket} bytes (max 107); use a shorter SWARM_DEV_ROOT"

for tool in go node npm curl; do
  command -v "${tool}" >/dev/null || die "${tool} is required"
done

umask 077
mkdir -p "${root}"/{bin,config,data,cache,run,logs,app,project,xdg}

echo "headless-dev: building swarmd"
(cd "${REPO}/swarmd" && go build -o "${root}/bin/swarmd" ./cmd/swarmd)
# Reinstall when the lockfile changed since node_modules was last installed.
deps() {
  local dir="$1"
  if [[ ! -f "${dir}/node_modules/.package-lock.json" || "${dir}/package-lock.json" -nt "${dir}/node_modules/.package-lock.json" ]]; then
    npm --prefix "${dir}" ci --no-audit --no-fund
  fi
}
echo "headless-dev: building the SDK"
deps "${REPO}/packages/sdk"
npm --prefix "${REPO}/packages/sdk" run --silent build
deps "${REPO}/examples/headless-app"

# Written once; an existing file (and anything the daemon appended) is kept.
conf="${root}/config/swarm.conf"
if [[ ! -e "${conf}" ]]; then
  printf 'host = 127.0.0.1\nport = %s\ndesktop_port = 0\npeer_transport_port = %s\n' "${api_port}" "${peer_port}" >"${conf}"
fi

export STATE_DIRECTORY="${root}/data" CACHE_DIRECTORY="${root}/cache" RUNTIME_DIRECTORY="${root}/run"
export CONFIGURATION_DIRECTORY="${root}/config" LOGS_DIRECTORY="${root}/logs"
# Session and worker worktrees live under the user data root
# (appstorage.WorktreeDataDir); keep this box's out of the real home.
export XDG_DATA_HOME="${root}/xdg"
# Tokens the daemon saves for local use (security.SecretsFilePath) stay in
# the dev root instead of the real ~/.config/swarm/secrets.env.
export SWARM_SECRETS_FILE="${root}/config/secrets.env"
export SWARM_DISABLE_MINT_REPORT=1

daemon_pid="" app_pid=""
stop() {
  trap - EXIT INT TERM
  [[ -n "${app_pid}" ]] && kill "${app_pid}" 2>/dev/null || true
  [[ -n "${daemon_pid}" ]] && kill "${daemon_pid}" 2>/dev/null || true
  wait 2>/dev/null || true
}
interrupted() { stop; echo "headless-dev: stopped" >&2; exit 0; }
trap stop EXIT
trap interrupted INT TERM

"${root}/bin/swarmd" --cwd="${root}/project" --sandbox=off >>"${root}/logs/swarmd.log" 2>&1 &
daemon_pid=$!

ready=0
for _ in $(seq 1 300); do
  kill -0 "${daemon_pid}" 2>/dev/null || die "swarmd exited during startup; see ${root}/logs/swarmd.log"
  if curl -fsS --max-time 1 --unix-socket "${socket}" http://swarmd/readyz >/dev/null 2>&1; then ready=1; break; fi
  sleep 0.1
done
(( ready )) || die "swarmd was not ready within 30s; see ${root}/logs/swarmd.log"

APP_CONFIG_DIR="${root}/app" SWARM_SOCKET_PATH="${socket}" APP_PROJECT="${root}/project" \
  APP_LISTEN=127.0.0.1 APP_ORIGIN=http://127.0.0.1:8443 \
  node "${REPO}/examples/headless-app/server.mjs" >>"${root}/logs/app.log" 2>&1 &
app_pid=$!

echo "headless-dev: swarmd pid ${daemon_pid} on 127.0.0.1:${api_port}, socket ${socket}"
echo "headless-dev: logs in ${root}/logs (swarmd.log, app.log)"
echo "headless-dev: open http://127.0.0.1:8443"
wait -n "${daemon_pid}" "${app_pid}" || true
die "a process exited; stopping. See ${root}/logs"
