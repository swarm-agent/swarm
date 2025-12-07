#!/bin/bash
set -e

cd "$(dirname "$0")"

echo "=== opencode2 setup ==="
echo ""

# Make scripts executable
chmod +x build.sh install.sh clean.sh update-from-upstream.sh

# Clean any copied node_modules
echo "Cleaning copied node_modules..."
rm -rf packages/*/node_modules packages/sdk/js/node_modules 2>/dev/null || true

# Set up environment
export BUN_TMPDIR=/tmp
export BUN_INSTALL="$(pwd)/.bun"

# Install deps
echo "Installing dependencies..."
bun install

# Build
echo ""
echo "Building binary..."
cd packages/opencode
bun run build --single
cd ../..

echo ""
echo "=== Setup complete! ==="
echo ""
echo "Binary: packages/opencode/dist/opencode-linux-x64/bin/opencode"
echo ""
echo "Commands:"
echo "  ./build.sh     - Rebuild the binary"
echo "  ./install.sh   - Install to ~/.local/bin"
echo "  ./clean.sh     - Clean all build artifacts"
echo "  bun dev        - Run in development mode"
echo ""
