#!/bin/bash
set -e

echo "═══════════════════════════════════════════════════════════"
echo "  BUILD & COMMIT: SDK + Twitter Bot"
echo "═══════════════════════════════════════════════════════════"

# 1. Build SDK
echo ""
echo "📦 Building SDK..."
cd /home/roy/swarm-cli/packages/sdk/js
bun run script/build.ts
echo "✅ SDK built"

# 2. Commit SDK changes
echo ""
echo "📝 Committing SDK changes..."
cd /home/roy/swarm-cli
git add packages/sdk/js/src/index.ts packages/sdk/js/src/spawn.ts
git commit -m "feat(sdk): add Zod re-export + containerProfile support

- Re-export z from zod to prevent instance mismatch errors
- Add containerProfile option to spawn() for container isolation
- Users can now: import { tool, z } from '@opencode-ai/sdk'
- spawn({ prompt, containerProfile: 'my-profile' }) runs in container" || echo "Nothing to commit in swarm-cli"
echo "✅ SDK committed"

# 3. Commit socialmedia
echo ""
echo "📝 Committing socialmedia Twitter bot..."
cd /home/roy/socialmedia
git add swarm-sdk-twitter/
git commit -m "feat: Twitter bot with SDK container profile support

- Import z from SDK (fixes Zod instance mismatch)
- Add CONTAINER_PROFILE env var for container mode
- Add container-profile.json example config
- Update README with container docs
- Fix event handler (no event.result.duration_ms)" || echo "Nothing to commit"
git push
echo "✅ socialmedia pushed"

echo ""
echo "═══════════════════════════════════════════════════════════"
echo "  ✅ ALL DONE!"
echo "═══════════════════════════════════════════════════════════"
echo ""
echo "To test:"
echo "  cd /home/roy/swarm-cli"
echo "  EXA_API_KEY=8da1cf9f-abc7-485d-825e-921c02957c42 bun run test-twitter-bot.ts"
echo ""
echo "To run with container:"
echo "  CONTAINER_PROFILE=twitter-bot bun run run.ts"
