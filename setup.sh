#!/bin/bash
set -e
cd "$(dirname "$0")"

echo "🔧 Setting up swarm..."

# 1. Install deps
export BUN_TMPDIR=/tmp
export BUN_INSTALL="$(pwd)/.bun"
bun install

# 2. Build binary
cd packages/opencode && bun run build --single && cd ../..

# 3. Install binary
mkdir -p ~/.local/bin
cp packages/opencode/dist/swarm-linux-x64/bin/swarm ~/.local/bin/swarm
chmod +x ~/.local/bin/swarm

# 4. Symlink config (force-replace existing)
REPO_CONFIG="$(pwd)/.opencode"
GLOBAL_CONFIG="$HOME/.config/opencode"
mkdir -p "$GLOBAL_CONFIG"

for item in agent command themes opencode.json; do
    target="$GLOBAL_CONFIG/$item"
    # Backup if exists and not already a symlink
    [[ -e "$target" && ! -L "$target" ]] && mv "$target" "$target.bak"
    # Remove existing symlink
    [[ -L "$target" ]] && rm "$target"
    # Create symlink
    ln -s "$REPO_CONFIG/$item" "$target"
done

echo ""
echo "✅ Done!"
echo "   Binary: ~/.local/bin/swarm"
echo "   Config: ~/.config/opencode/ → $(pwd)/.opencode/"
echo ""
echo "Run: swarm"
