#!/bin/bash
set -e

cd "$(dirname "$0")"

echo "=== Cleaning opencode2 ==="

rm -rf node_modules
rm -rf .bun
rm -rf packages/*/node_modules
rm -rf packages/sdk/js/node_modules
rm -rf packages/opencode/dist
rm -rf .turbo
rm -f bun.lock

echo "Done. Run ./build.sh to rebuild."
