#!/bin/sh
# Joins the tailnet (when TS_AUTHKEY is set) as a throwaway node, then serves
# swarm-fleet on stdin/stdout. Tailscale runs in userspace: no device, no
# privileges, state in memory. The auth key is not passed to the MCP server.
set -eu
if [ -n "${TS_AUTHKEY:-}" ]; then
  mkdir -p /tmp/ts
  tailscaled --no-logs-no-support --tun=userspace-networking --state=mem: --statedir=/tmp/ts --socket=/tmp/ts/tailscaled.sock \
    --outbound-http-proxy-listen=127.0.0.1:1055 >/tmp/ts/tailscaled.log 2>&1 &
  if ! tailscale --socket=/tmp/ts/tailscaled.sock up --auth-key="$TS_AUTHKEY" \
      --hostname="${TS_HOSTNAME:-claude-fleet}" --accept-dns=false --timeout=90s >&2; then
    echo "swarm-fleet: could not join the tailnet; last tailscaled log lines:" >&2
    tail -n 20 /tmp/ts/tailscaled.log >&2 || true
    exit 1
  fi
  echo "swarm-fleet: joined the tailnet as ${TS_HOSTNAME:-claude-fleet}" >&2
  # Leave the tailnet as soon as the client closes the server.
  env -u TS_AUTHKEY -u HTTPS_PROXY -u https_proxy FLEET_PROXY=http://127.0.0.1:1055 node /app/server.mjs || true
  tailscale --socket=/tmp/ts/tailscaled.sock logout >/dev/null 2>&1 || true
  exit 0
fi
exec node /app/server.mjs
