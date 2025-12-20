#!/bin/bash
# create-release.sh - Create clean swarm-cli release repo
set -e

DEST="${1:-$HOME/swarm-cli-release}"
SRC="$(dirname "$0")"

echo "Creating clean release at: $DEST"
echo "Source: $SRC"
echo ""

# Clean and create
rm -rf "$DEST"
mkdir -p "$DEST/packages" "$DEST/docs"
cd "$DEST"
git init -b main

# ============================================================================
# PHASE 1: Copy packages
# ============================================================================
echo "📦 Copying packages..."
cp -r "$SRC/packages/opencode" "$DEST/packages/swarm"
cp -r "$SRC/packages/sdk" "$DEST/packages/sdk"
cp -r "$SRC/packages/plugin" "$DEST/packages/plugin"
cp -r "$SRC/packages/script" "$DEST/packages/script"

# Remove node_modules and dist from copied packages
find "$DEST/packages" -type d -name "node_modules" -exec rm -rf {} + 2>/dev/null || true
find "$DEST/packages" -type d -name "dist" -exec rm -rf {} + 2>/dev/null || true

# ============================================================================
# PHASE 2: Copy root config
# ============================================================================
echo "📄 Copying root config..."
cp "$SRC/package.json" "$DEST/"
cp "$SRC/tsconfig.json" "$DEST/"
cp "$SRC/turbo.json" "$DEST/"
cp "$SRC/bunfig.toml" "$DEST/"
cp "$SRC/.gitignore" "$DEST/"
cp "$SRC/build.sh" "$DEST/"
cp "$SRC/install.sh" "$DEST/"
cp "$SRC/clean.sh" "$DEST/"

# Fix build.sh to use packages/swarm
sed -i 's|packages/opencode|packages/swarm|g' "$DEST/build.sh"

# Fix twitter-bot example name
sed -i 's/twitter-bot/my-agent/g' "$DEST/packages/sdk/js/src/spawn.ts"

# Copy clean docs only
cp "$SRC/docs/SDK_PLAN.md" "$DEST/docs/" 2>/dev/null || true
cp "$SRC/docs/SDK_PERMISSIONS_PLAN.md" "$DEST/docs/" 2>/dev/null || true

# ============================================================================
# PHASE 3: Global renames
# ============================================================================
echo "🔄 Renaming opencode → swarm..."

# 3a. NPM scope: @opencode-ai → @swarm-ai
find "$DEST" -type f \( -name "*.ts" -o -name "*.json" -o -name "*.tsx" \) -exec sed -i 's/@opencode-ai/@swarm-ai/g' {} +

# 3b. Config dir: .opencode → .swarm
find "$DEST" -type f \( -name "*.ts" -o -name "*.json" -o -name "*.md" -o -name "*.tsx" \) -exec sed -i 's/\.opencode/.swarm/g' {} +

# 3c. Config files: opencode.json/jsonc → swarm.json/jsonc
find "$DEST" -type f \( -name "*.ts" -o -name "*.tsx" \) -exec sed -i 's/opencode\.jsonc/swarm.jsonc/g' {} +
find "$DEST" -type f \( -name "*.ts" -o -name "*.tsx" \) -exec sed -i 's/opencode\.json/swarm.json/g' {} +

# 3c-fix. Rename opencode.json theme file to swarm.json (the import was renamed above)
mv "$DEST/packages/swarm/src/cli/cmd/tui/context/theme/opencode.json" "$DEST/packages/swarm/src/cli/cmd/tui/context/theme/swarm.json" 2>/dev/null || true

# 3d. Env vars: OPENCODE_ → SWARM_
find "$DEST" -type f \( -name "*.ts" -o -name "*.tsx" \) -exec sed -i 's/OPENCODE_/SWARM_/g' {} +

# 3e. Package paths
find "$DEST" -type f \( -name "*.ts" -o -name "*.json" -o -name "*.tsx" \) -exec sed -i 's|packages/opencode|packages/swarm|g' {} +

# 3f. Domain: opencode.ai → swarm.ai (in URLs, not imports)
find "$DEST" -type f \( -name "*.ts" -o -name "*.tsx" \) -exec sed -i 's/opencode\.ai/swarm.ai/g' {} +

# 3g. Misc opencode refs in strings/comments
find "$DEST" -type f \( -name "*.ts" -o -name "*.tsx" \) -exec sed -i 's/"opencode"/"swarm"/g' {} +

# ============================================================================
# PHASE 4: Update root package.json
# ============================================================================
echo "📝 Updating root package.json..."
sed -i 's|--cwd packages/opencode|--cwd packages/swarm|g' "$DEST/package.json"
sed -i 's|"packages/opencode"|"packages/swarm"|g' "$DEST/package.json"

# ============================================================================
# PHASE 5: Fix hardcoded paths
# ============================================================================
echo "🔧 Fixing hardcoded paths..."

# Fix /home/roy/swarm-cli in container-test.ts
sed -i 's|/home/roy/swarm-cli|process.cwd()|g' "$DEST/packages/sdk/js/example/container-test.ts"

# Remove any remaining /home/roy references
find "$DEST" -type f \( -name "*.ts" -o -name "*.tsx" -o -name "*.json" -o -name "*.md" \) -exec sed -i 's|/home/roy/[^"]*|/path/to/workspace|g' {} + 2>/dev/null || true

# ============================================================================
# PHASE 6: Security scan
# ============================================================================
echo ""
echo "🔍 Security scan..."

echo -n "  /home/roy/ paths: "
COUNT=$(grep -r "/home/roy" "$DEST" --include="*.ts" --include="*.json" --include="*.md" 2>/dev/null | wc -l)
if [ "$COUNT" -eq 0 ]; then echo "✅ None"; else echo "⚠️  $COUNT found"; fi

echo -n "  API keys (sk-ant-, gsk_): "
COUNT=$(grep -rE "sk-ant-[a-zA-Z0-9]|gsk_[a-zA-Z0-9]" "$DEST" --include="*.ts" --include="*.json" 2>/dev/null | grep -v "sk-ant-..." | wc -l)
if [ "$COUNT" -eq 0 ]; then echo "✅ None"; else echo "⚠️  $COUNT found"; fi

echo -n "  Personal projects (voiceagent, socialmedia): "
COUNT=$(grep -rE "voiceagent|socialmedia|twitter-bot" "$DEST" --include="*.ts" --include="*.json" 2>/dev/null | wc -l)
if [ "$COUNT" -eq 0 ]; then echo "✅ None"; else echo "⚠️  $COUNT found"; fi

# ============================================================================
# PHASE 7: Create minimal README
# ============================================================================
echo "📝 Creating README..."
cat > "$DEST/README.md" << 'EOF'
# swarm-cli

Minimal AI terminal for coders.

## Quick Start

```bash
# Install dependencies
bun install

# Build binary
./build.sh

# Install globally
./install.sh
```

## Development

```bash
bun dev
```

## Packages

| Package | Description |
|---------|-------------|
| `packages/swarm` | CLI application |
| `packages/sdk/js` | JavaScript SDK |
| `packages/plugin` | Plugin system |
| `packages/script` | Build utilities |

## Configuration

Config file: `.swarm/swarm.json`

## License

MIT
EOF

# ============================================================================
# PHASE 8: Create minimal AGENTS.md
# ============================================================================
cat > "$DEST/AGENTS.md" << 'EOF'
# swarm-cli

## Build

```bash
bun install
./build.sh
```

Binary output: `packages/swarm/dist/swarm-linux-x64/bin/swarm`

## Configuration

Config location: `.swarm/swarm.json`

```json
{
  "model": "anthropic/claude-sonnet-4-5",
  "sandbox": {
    "enabled": true
  }
}
```

## SDK Usage

```typescript
import { createSwarm, tool, z } from "@swarm-ai/sdk"

const { spawn, server } = await createSwarm()

const handle = spawn("List all TypeScript files")
await handle.wait()

server.close()
```
EOF

# ============================================================================
# DONE
# ============================================================================
echo ""
echo "✅ Release repo created at: $DEST"
echo ""
echo "Next steps:"
echo "  cd $DEST"
echo "  bun install"
echo "  ./build.sh"
echo "  git add . && git commit -m 'Initial commit: swarm v1.0.0'"
