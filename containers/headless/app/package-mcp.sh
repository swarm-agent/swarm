#!/usr/bin/env bash
# Copy the shared plugin bridge into a headless release without a second client.
# Release assembly must call: bash containers/headless/app/package-mcp.sh HEADLESS_DIR
set -euo pipefail
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)
dest=${1:?usage: package-mcp.sh HEADLESS_DIR}/share/swarm-mcp
install -d -m 0755 "$dest/lib"
install -m 0755 "$root/plugins/swarm/bin/swarm-mcp" "$dest/swarm-mcp"
install -m 0644 "$root/plugins/swarm/bin/swarm-mcp.mjs" "$dest/swarm-mcp.mjs"
install -m 0644 "$root/plugins/swarm/bin/lib/connection.mjs" "$dest/lib/connection.mjs"
