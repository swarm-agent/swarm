#!/bin/bash
set -e

cd "$(dirname "$0")"

echo "=== swarm build ==="
echo ""

# Set up environment for sandboxed builds
export BUN_TMPDIR=/tmp
export BUN_INSTALL="$(pwd)/.bun"

# Install deps if needed
if [ ! -d "node_modules" ] || [ ! -d "packages/opencode/node_modules" ]; then
    echo "=== Installing dependencies ==="
    bun install
    echo ""
fi

# Build
echo "=== Building binary ==="
cd packages/opencode
bun run build --single

echo ""
echo "✅ Built: $(pwd)/dist/swarm-linux-x64/bin/swarm ($(ls -lh dist/swarm-linux-x64/bin/swarm | awk '{print $5}'))"
