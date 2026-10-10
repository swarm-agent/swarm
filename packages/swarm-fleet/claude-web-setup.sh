#!/bin/bash
# Setup script for a Claude Code on the web environment: gives every session
# the Swarm tools for every Swarm machine on your tailnet.
#
# Paste this file into the environment's settings under "Setup script", and
# add these environment variables there:
#   TS_AUTHKEY=<reusable, ephemeral, pre-approved auth key tagged tag:claude>
#   MCP_TIMEOUT=120000
#
# The script only installs files; nothing secret is used here. When a session
# starts the "swarm" MCP server, fleet.sh joins the tailnet as a throwaway
# node, finds the machines tagged tag:swarm and serves their tools. The
# tailnet policy decides what that node may do on each machine.
#
# It must exit 0, or the session does not start: a failed step is reported on
# stderr and retried by the next session instead of stopping the setup.
REF=${SWARM_FLEET_REF:-dev}
DIR=/opt/swarm-fleet
BASE=https://raw.githubusercontent.com/swarm-agent/swarm/$REF/packages/swarm-fleet

mkdir -p "$DIR"
for f in server.mjs fleet.sh; do
  curl -fsSL "$BASE/$f" -o "$DIR/$f.new" && mv "$DIR/$f.new" "$DIR/$f" ||
    echo "swarm-fleet setup: could not fetch $f from $REF" >&2
done
chmod 0755 "$DIR/fleet.sh" 2>/dev/null

# Fetch the pinned Tailscale now so the first session does not wait for it.
mkdir -p "$DIR/runtime" && chmod 700 "$DIR/runtime"
bash "$DIR/fleet.sh" --fetch-only ||
  echo "swarm-fleet setup: Tailscale download failed; the first session will retry" >&2

# Register the server for every session (user scope), whatever repositories
# the session has.
python3 - "$DIR" <<'PY' || echo "swarm-fleet setup: could not register the MCP server" >&2
import json, os, sys
directory = sys.argv[1]
path = os.path.expanduser("~/.claude.json")
try:
    with open(path) as f:
        config = json.load(f)
except FileNotFoundError:
    config = {}
config.setdefault("mcpServers", {})["swarm"] = {
    "type": "stdio",
    "command": "bash",
    "args": [os.path.join(directory, "fleet.sh")],
}
tmp = path + ".swarm-fleet"
with open(tmp, "w") as f:
    json.dump(config, f, indent=2)
os.chmod(tmp, 0o600)
os.replace(tmp, path)
PY
exit 0
