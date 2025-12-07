#!/bin/bash
set -e

cd "$(dirname "$0")"

echo "=== opencode2 build ==="
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
echo "Binary: $(pwd)/dist/opencode-linux-x64/bin/opencode"
echo "Size:   $(ls -lh dist/opencode-linux-x64/bin/opencode | awk '{print $5}')"
echo ""
echo "To install globally:"
echo "  cp $(pwd)/dist/opencode-linux-x64/bin/opencode ~/.local/bin/opencode"
