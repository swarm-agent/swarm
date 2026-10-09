#!/usr/bin/env bash
# Starts swarm-fleet for an MCP client (Claude Code runs this). stdout is the
# MCP stream; everything else goes to stderr. No Docker, no root.
#
# On a device already in your tailnet (laptop, server), it uses that device's
# Tailscale. Anywhere else (Claude Code on the web, CI) it needs TS_AUTHKEY:
# it joins the tailnet as a throwaway node in userspace (no device, no
# privileges, state in memory), keeps that node for the rest of the session so
# later starts reuse it, and never passes the key to the MCP server.
#
# FLEET_USERSPACE=1 forces the throwaway node even where Tailscale is running.
# `fleet.sh --join-only` joins and exits (for a setup script, so the first
# session does not wait for the join).
# Tailscale's own connections use HTTPS_PROXY and SSL_CERT_FILE when set (an
# inspecting proxy, as in Claude Code on the web); SWARM_FLEET_CA_BUNDLE
# overrides the CA file for them.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
TS_VERSION=1.104.1
TS_SHA256=108d1d96ecf410d305571e173516f27038f870919a9b89c914a167c6d33a4528
log() { echo "swarm-fleet: $*" >&2; }

command -v node >/dev/null || { log "node 22 or newer is required"; exit 2; }

if [[ -z ${FLEET_USERSPACE:-} ]] && command -v tailscale >/dev/null && tailscale status >/dev/null 2>&1; then
  [[ ${1:-} == --join-only ]] && exit 0
  exec env -u TS_AUTHKEY node "$here/server.mjs"
fi
[[ -n ${TS_AUTHKEY:-} ]] || { log "this device is not on your tailnet: run on a device in it, or set TS_AUTHKEY (an ephemeral, tagged auth key)"; exit 2; }

runtime=${FLEET_RUNTIME_DIR:-${XDG_RUNTIME_DIR:-$HOME/.cache}/swarm-fleet}
mkdir -p "$runtime" && chmod 700 "$runtime"

# Tailscale binaries: the installed ones, else a pinned, checksummed release.
bin=''
if command -v tailscaled >/dev/null && command -v tailscale >/dev/null; then
  bin=$(dirname "$(command -v tailscaled)")
else
  bin=$runtime/tailscale_${TS_VERSION}_amd64
  if [[ ! -x $bin/tailscaled ]]; then
    [[ $(uname -sm) == "Linux x86_64" ]] || { log "install Tailscale on this device (no pinned build for $(uname -sm))"; exit 2; }
    tgz=$runtime/tailscale_${TS_VERSION}_amd64.tgz
    curl -fsSL "https://pkgs.tailscale.com/stable/tailscale_${TS_VERSION}_amd64.tgz" -o "$tgz" >&2
    echo "$TS_SHA256  $tgz" | sha256sum -c --quiet - >&2 || { log "Tailscale download failed its checksum"; exit 2; }
    tar -xzf "$tgz" -C "$runtime" && rm -f "$tgz"
  fi
fi

sock=$runtime/tailscaled.sock
port=${FLEET_PROXY_PORT:-1055}
running() { "$bin/tailscale" --socket="$sock" status --json 2>/dev/null | grep -q '"BackendState": *"Running"'; }
if ! running; then
  ca=${SWARM_FLEET_CA_BUNDLE:-${SSL_CERT_FILE:-}}
  # A node left from a failed join holds the socket: replace it.
  pkill -f -- "--socket=$sock" 2>/dev/null && sleep 0.5 || true
  env -u TS_AUTHKEY ${ca:+"SSL_CERT_FILE=$ca"} setsid "$bin/tailscaled" --no-logs-no-support --tun=userspace-networking \
    --state=mem: --statedir="$runtime/state" --socket="$sock" --outbound-http-proxy-listen="127.0.0.1:$port" \
    </dev/null >"$runtime/tailscaled.log" 2>&1 &
  tailscaled_pid=$!
  for _ in $(seq 1 50); do [[ -S $sock ]] && break; sleep 0.2; done
  keyfile=$(mktemp "$runtime/key.XXXXXX")
  printf '%s' "$TS_AUTHKEY" >"$keyfile"
  host=${TS_HOSTNAME:-claude-$(hostname | tr -cd 'a-z0-9-' | cut -c1-20)}
  if ! "$bin/tailscale" --socket="$sock" up --auth-key="file:$keyfile" --hostname="$host" --accept-dns=false --timeout=90s >&2; then
    rm -f "$keyfile"
    log "could not join the tailnet; last tailscaled log lines:"
    tail -n 20 "$runtime/tailscaled.log" >&2 || true
    kill "$tailscaled_pid" 2>/dev/null || true
    exit 1
  fi
  rm -f "$keyfile"
  log "joined the tailnet as $host"
fi
# --join-only: join (or confirm) and exit, for an environment setup script.
[[ ${1:-} == --join-only ]] && exit 0
exec env -u TS_AUTHKEY PATH="$bin:$PATH" FLEET_PROXY="http://127.0.0.1:$port" FLEET_TAILSCALE_SOCKET="$sock" node "$here/server.mjs"
