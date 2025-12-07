#!/bin/bash
set -e

cd "$(dirname "$0")"

UPSTREAM="${1:-/home/roy/opencode}"

if [ ! -d "$UPSTREAM/packages/opencode" ]; then
    echo "Error: Upstream not found at $UPSTREAM"
    echo "Usage: ./update-from-upstream.sh [path-to-opencode-monorepo]"
    exit 1
fi

echo "=== Updating from upstream: $UPSTREAM ==="
echo ""

# Clean first
echo "Cleaning..."
rm -rf packages/opencode/src packages/opencode/script
rm -rf packages/plugin/src
rm -rf packages/script/src
rm -rf packages/sdk/js/src

# Copy source files
echo "Copying packages/opencode..."
cp -r "$UPSTREAM/packages/opencode/src" packages/opencode/
cp -r "$UPSTREAM/packages/opencode/script" packages/opencode/
cp "$UPSTREAM/packages/opencode/package.json" packages/opencode/
cp "$UPSTREAM/packages/opencode/tsconfig.json" packages/opencode/

echo "Copying packages/plugin..."
cp -r "$UPSTREAM/packages/plugin/src" packages/plugin/
cp "$UPSTREAM/packages/plugin/package.json" packages/plugin/
cp "$UPSTREAM/packages/plugin/tsconfig.json" packages/plugin/

echo "Copying packages/script..."
cp -r "$UPSTREAM/packages/script/src" packages/script/
cp "$UPSTREAM/packages/script/package.json" packages/script/
cp "$UPSTREAM/packages/script/tsconfig.json" packages/script/

echo "Copying packages/sdk/js..."
cp -r "$UPSTREAM/packages/sdk/js/src" packages/sdk/js/
cp "$UPSTREAM/packages/sdk/js/package.json" packages/sdk/js/
cp "$UPSTREAM/packages/sdk/js/tsconfig.json" packages/sdk/js/

# Update catalog versions from upstream
echo "Updating catalog versions..."
node -e "
const fs = require('fs');
const upstream = JSON.parse(fs.readFileSync('$UPSTREAM/package.json'));
const local = JSON.parse(fs.readFileSync('package.json'));
local.workspaces.catalog = upstream.workspaces.catalog;
local.packageManager = upstream.packageManager;
fs.writeFileSync('package.json', JSON.stringify(local, null, 2) + '\n');
"

echo ""
echo "=== Done! ==="
echo ""
echo "Now run:"
echo "  ./clean.sh"
echo "  ./build.sh"
