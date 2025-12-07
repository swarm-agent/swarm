#!/bin/bash
set -e

# Get the directory where this script lives
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PARENT_DIR="$(dirname "$SCRIPT_DIR")"
CURRENT_NAME="$(basename "$SCRIPT_DIR")"
TARGET_NAME="swarm"
TARGET_DIR="$PARENT_DIR/$TARGET_NAME"

echo "=== swarm setup ==="
echo ""

# Make scripts executable
chmod +x build.sh install.sh clean.sh update-from-upstream.sh 2>/dev/null || true

# Clean any broken caches
echo "Cleaning caches..."
rm -rf .bun packages/*/node_modules packages/sdk/js/node_modules 2>/dev/null || true

# Set up environment
export BUN_TMPDIR=/tmp
export BUN_INSTALL="$(pwd)/.bun"

# Install deps
echo "Installing dependencies..."
bun install

# Set up global config symlinks
echo ""
echo "=== Setting up global config ==="
GLOBAL_CONFIG="$HOME/.config/opencode"
mkdir -p "$GLOBAL_CONFIG"

# Link agent folder
if [ -L "$GLOBAL_CONFIG/agent" ]; then
    rm "$GLOBAL_CONFIG/agent"
fi
if [ ! -e "$GLOBAL_CONFIG/agent" ]; then
    ln -s "$SCRIPT_DIR/.opencode/agent" "$GLOBAL_CONFIG/agent"
    echo "Linked: $GLOBAL_CONFIG/agent -> $SCRIPT_DIR/.opencode/agent"
fi

# Link opencode.json
if [ -L "$GLOBAL_CONFIG/opencode.json" ]; then
    rm "$GLOBAL_CONFIG/opencode.json"
fi
if [ ! -e "$GLOBAL_CONFIG/opencode.json" ]; then
    ln -s "$SCRIPT_DIR/.opencode/opencode.json" "$GLOBAL_CONFIG/opencode.json"
    echo "Linked: $GLOBAL_CONFIG/opencode.json -> $SCRIPT_DIR/.opencode/opencode.json"
fi

# Link command folder
if [ -L "$GLOBAL_CONFIG/command" ]; then
    rm "$GLOBAL_CONFIG/command"
fi
if [ ! -e "$GLOBAL_CONFIG/command" ]; then
    ln -s "$SCRIPT_DIR/.opencode/command" "$GLOBAL_CONFIG/command"
    echo "Linked: $GLOBAL_CONFIG/command -> $SCRIPT_DIR/.opencode/command"
fi

# Link themes folder
if [ -L "$GLOBAL_CONFIG/themes" ]; then
    rm "$GLOBAL_CONFIG/themes"
fi
if [ ! -e "$GLOBAL_CONFIG/themes" ]; then
    ln -s "$SCRIPT_DIR/.opencode/themes" "$GLOBAL_CONFIG/themes"
    echo "Linked: $GLOBAL_CONFIG/themes -> $SCRIPT_DIR/.opencode/themes"
fi

# Build
echo ""
echo "=== Building binary ==="
cd packages/opencode
bun run build --single
cd ../..

# Install to ~/.local/bin
echo ""
echo "=== Installing to ~/.local/bin ==="
./install.sh

# Rename folder if needed
echo ""
if [ "$CURRENT_NAME" = "$TARGET_NAME" ]; then
    echo "Folder already named '$TARGET_NAME' ✓"
else
    echo "=== Renaming folder: $CURRENT_NAME -> $TARGET_NAME ==="
    
    if [ -e "$TARGET_DIR" ]; then
        echo ""
        echo "WARNING: $TARGET_DIR already exists!"
        echo "Skipping rename. To rename manually:"
        echo "  rm -rf $TARGET_DIR  # if you want to remove it"
        echo "  mv $SCRIPT_DIR $TARGET_DIR"
    else
        # Move to parent dir to do the rename
        cd "$PARENT_DIR"
        mv "$CURRENT_NAME" "$TARGET_NAME"
        echo "Done! Folder renamed to: $TARGET_DIR"
    fi
fi

echo ""
echo "=== Setup complete! ==="
echo ""
echo "Binary: packages/opencode/dist/swarm-linux-x64/bin/swarm"
echo ""
echo "Commands:"
echo "  swarm           - Launch swarm (installed to ~/.local/bin)"
echo "  ./build.sh      - Rebuild the binary"
echo "  ./install.sh    - Reinstall to ~/.local/bin"
echo "  ./clean.sh      - Clean all build artifacts"
echo "  bun dev         - Run in development mode"
echo ""
if [ "$CURRENT_NAME" != "$TARGET_NAME" ] && [ ! -e "$TARGET_DIR" ]; then
    echo "NOTE: Folder was renamed. Run: cd $TARGET_DIR"
    echo ""
fi
