#!/bin/bash
set -e

cd "$(dirname "$0")"

echo "=== swarm build ==="
echo ""

# Set up environment for sandboxed builds
export BUN_TMPDIR=/tmp
export BUN_INSTALL="$(pwd)/.bun"

# Install deps if needed
if [ ! -d "node_modules" ] || [ ! -d "packages/swarm/node_modules" ]; then
    echo "=== Installing dependencies ==="
    bun install
    echo ""
fi

# Build
echo "=== Building binary ==="
cd packages/swarm
bun run build --single

echo ""
echo "✅ Built: $(pwd)/dist/swarm-linux-x64/bin/swarm ($(ls -lh dist/swarm-linux-x64/bin/swarm | awk '{print $5}'))"

# Install to ~/.local/bin
if [ -d "$HOME/.local/bin" ]; then
    # Kill running swarm processes first (ignore errors if none running)
    if pgrep -x swarm >/dev/null 2>&1; then
        echo "⚠️  Killing running swarm processes..."
        pkill -9 -x swarm 2>/dev/null || true
        sleep 0.5
    fi
    cp dist/swarm-linux-x64/bin/swarm "$HOME/.local/bin/swarm"
    echo "✅ Installed: ~/.local/bin/swarm"
fi
