#!/bin/bash
set -e

cd "$(dirname "$0")"

BINARY="packages/opencode/dist/opencode-linux-x64/bin/opencode"
INSTALL_DIR="${HOME}/.local/bin"

if [ ! -f "$BINARY" ]; then
    echo "Binary not found. Building first..."
    ./build.sh
fi

mkdir -p "$INSTALL_DIR"
cp "$BINARY" "$INSTALL_DIR/opencode"
chmod +x "$INSTALL_DIR/opencode"

echo ""
echo "Installed to: $INSTALL_DIR/opencode"
echo ""
echo "Make sure $INSTALL_DIR is in your PATH:"
echo "  export PATH=\"\$HOME/.local/bin:\$PATH\""
echo ""
echo "Test it:"
echo "  opencode --version"
