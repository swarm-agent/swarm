# opencode2 - Minimal AI Terminal for Coders

## What This Is

This is a minimal extraction of the opencode CLI from the full monorepo.
**Goal: Security through minimization** - only the essential packages needed to build and run the CLI.

## Size Comparison

| Version         | Total Size | node_modules | Runtime             |
| --------------- | ---------- | ------------ | ------------------- |
| Full monorepo   | 4.9 GB     | 4.0 GB       | ~1,729 npm packages |
| opencode2       | 1.6 GB     | 779 MB       | ~1,146 npm packages |
| **Binary only** | **129 MB** | **0**        | **0 npm packages**  |

## Packages Included (4 total)

| Package              | Purpose               |
| -------------------- | --------------------- |
| `packages/opencode/` | The CLI itself        |
| `packages/sdk/js/`   | SDK types used by CLI |
| `packages/plugin/`   | Plugin system         |
| `packages/script/`   | Build utilities       |

## How to Build

### Prerequisites

- Bun 1.3.3+ installed
- Linux x64 (for the binary)

### Build Steps

```bash
# 1. Install dependencies
BUN_TMPDIR=/tmp BUN_INSTALL=$(pwd)/.bun bun install

# 2. Build the binary (current platform only)
cd packages/opencode
BUN_TMPDIR=/tmp BUN_INSTALL=$(pwd)/../../.bun bun run build --single

# 3. Binary is at:
# packages/opencode/dist/opencode-linux-x64/bin/opencode
```

### Quick Build (use the script)

```bash
./build.sh
```

### Install Globally

```bash
cp packages/opencode/dist/swarm-linux-x64/bin/swarm ~/.local/bin/
```

## Development

```bash
# Run in dev mode (uses source, not binary)
bun dev
```

## Environment Variables Required for Build

The build needs these due to sandbox restrictions:

- `BUN_TMPDIR=/tmp` - Writable temp directory
- `BUN_INSTALL=$(pwd)/.bun` - Writable bun cache

## What Was Removed

Everything else from the original monorepo:

- `packages/desktop/` - Desktop GUI app
- `packages/ui/` - UI components (desktop only)
- `packages/web/` - Marketing/docs website
- `packages/slack/` - Slack bot
- `packages/console/` - Web console (5 sub-packages)
- `packages/function/` - Cloud API backend
- `packages/sdk/go/` - Go SDK
- `packages/sdk/python/` - Python SDK
- `packages/extensions/` - Editor extensions
- `packages/identity/` - Logo files
- `infra/` - SST deployment configs
- `github/` - GitHub Actions

## Security Notes

1. **Run the binary in production**, not dev mode
2. The binary has **zero runtime npm dependencies**
3. For extra isolation, run under `firejail` or `bubblewrap`
4. Rebuild periodically to get updates

## Git Workflow

This is a separate repo from the main opencode monorepo.

- No need to sync with upstream unless you want updates
- To update: copy new source from upstream and rebuild
