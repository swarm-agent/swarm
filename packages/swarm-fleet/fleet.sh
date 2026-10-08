#!/usr/bin/env bash
# Starts swarm-fleet for an MCP client (Claude Code's .mcp.json runs this).
# Reads SWARM_FLEET and, to join the tailnet, TS_AUTHKEY from the environment.
# Builds the image on first use. stdout is the MCP stream; everything else
# goes to stderr.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
image=${SWARM_FLEET_IMAGE:-swarm-fleet:local}
[[ -n ${SWARM_FLEET:-} ]] || { echo "swarm-fleet: set SWARM_FLEET (see the Swarm app's AI access section)" >&2; exit 2; }
if ! docker image inspect "$image" >/dev/null 2>&1; then
  docker build -q -t "$image" "$here" >&2
fi
args=(run -i --rm --init --read-only --tmpfs /tmp:mode=1777 --cap-drop ALL --security-opt no-new-privileges
      --pids-limit 128 --memory 256m -e SWARM_FLEET -e TS_AUTHKEY
      -e TS_HOSTNAME="${TS_HOSTNAME:-claude-$(hostname | tr -cd 'a-z0-9-' | cut -c1-20)}")
# Behind an inspecting HTTPS proxy (Claude Code on the web), Tailscale's
# control and relay connections must use the proxy and trust its CA: set
# SWARM_FLEET_CA_BUNDLE to that CA bundle file.
if [[ -n ${HTTPS_PROXY:-} && -n ${SWARM_FLEET_CA_BUNDLE:-} ]]; then
  [[ -f $SWARM_FLEET_CA_BUNDLE ]] || { echo "swarm-fleet: SWARM_FLEET_CA_BUNDLE is not a file" >&2; exit 2; }
  args+=(--network host -e HTTPS_PROXY -e SSL_CERT_FILE=/run/ca.crt -v "$SWARM_FLEET_CA_BUNDLE:/run/ca.crt:ro")
fi
exec docker "${args[@]}" "$image"
