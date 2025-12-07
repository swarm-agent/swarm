#!/bin/bash
set -e

cd "$(dirname "$0")"

BINARY="packages/opencode/dist/swarm-linux-x64/bin/swarm"
INSTALL_DIR="${HOME}/.local/bin"

if [ ! -f "$BINARY" ]; then
    echo "Binary not found. Building first..."
    ./build.sh
fi

mkdir -p "$INSTALL_DIR"
cp "$BINARY" "$INSTALL_DIR/swarm"
chmod +x "$INSTALL_DIR/swarm"

echo ""
echo "Installed to: $INSTALL_DIR/swarm"
echo ""
echo "Make sure $INSTALL_DIR is in your PATH:"
echo "  export PATH=\"\$HOME/.local/bin:\$PATH\""
echo ""
echo "Test it:"
echo "  swarm --version"
