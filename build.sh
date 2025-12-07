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
echo "=== Done! ==="
echo ""
echo "Binary: $(pwd)/dist/swarm-linux-x64/bin/swarm"
echo "Size:   $(ls -lh dist/swarm-linux-x64/bin/swarm | awk '{print $5}')"
echo ""
echo "To install globally:"
echo "  cp $(pwd)/dist/swarm-linux-x64/bin/swarm ~/.local/bin/swarm"
