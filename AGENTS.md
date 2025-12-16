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

## Sandbox (Anthropic Sandbox Runtime)

The CLI uses `@anthropic-ai/sandbox-runtime` for OS-level sandboxing of bash commands.

### Package Location (IMPORTANT - npm 404s, we have it cached!)

```
# Symlink in workspace:
packages/opencode/node_modules/@anthropic-ai/sandbox-runtime

# Actual cached location:
node_modules/.bun/@anthropic-ai+sandbox-runtime@0.0.18/node_modules/@anthropic-ai/sandbox-runtime

# Contains:
- dist/           # Compiled JS
- vendor/seccomp/ # Pre-built seccomp binaries
  - x64/apply-seccomp      # 827KB static binary
  - x64/unix-block.bpf     # 104 bytes BPF filter
  - arm64/apply-seccomp    # 542KB static binary  
  - arm64/unix-block.bpf   # 88 bytes BPF filter
```

### Linux Dependencies

```bash
apt install bubblewrap socat ripgrep
```

### Config (in opencode.json)

```json
{
  "sandbox": {
    "enabled": true,
    "network": {
      "allowedDomains": ["github.com", "*.github.com", "npmjs.org"],
      "deniedDomains": [],
      "allowLocalBinding": true,
      "socketBridges": {
        "/tmp/swarm-api.sock": "localhost:3456"
      }
    },
    "filesystem": {
      "denyRead": ["~/.ssh", "~/.gnupg"],
      "allowWrite": [".", "/tmp"],
      "denyWrite": [".env", "*.key"]
    }
  }
}
```

### How It Works

1. Uses **bubblewrap** (`bwrap`) with `--unshare-net` for network namespace isolation
2. HTTP/SOCKS5 proxies filter network by domain allowlist
3. Unix sockets bridge proxy traffic into the sandbox
4. **seccomp BPF** blocks new Unix socket creation (prevents bypassing proxy)
5. Filesystem bind mounts enforce read/write restrictions

### Socket Bridges (Local API Access)

Socket bridges allow sandboxed commands to access local TCP services securely via Unix sockets.

**Why needed:** The sandbox blocks all network access including `localhost`. Socket bridges provide a controlled way to reach local APIs.

**Configuration:** Add to `sandbox.network.socketBridges` in opencode.json:
```json
"socketBridges": {
  "/tmp/swarm-api.sock": "localhost:3456",
  "/tmp/my-db.sock": "localhost:5432"
}
```

**Usage from sandboxed commands:**
```bash
# Direct localhost access is BLOCKED:
curl http://localhost:3456/health  # ❌ Connection refused

# Use the Unix socket instead:
curl --unix-socket /tmp/swarm-api.sock http://localhost/health  # ✅ Works!

# The URL host doesn't matter when using --unix-socket, but use "localhost" by convention
curl --unix-socket /tmp/swarm-api.sock http://localhost/api/tasks
```

**How it works:** When the sandbox starts, `socat` processes are spawned to bridge each Unix socket to its TCP target. The sockets are bind-mounted into the sandbox, allowing access while the network remains isolated.

## Security Notes

1. **Run the binary in production**, not dev mode
2. The binary has **zero runtime npm dependencies**
3. **Enable sandbox** in config for OS-level isolation
4. Rebuild periodically to get updates

## Configuration

The main configuration file is at `.opencode/opencode.json`. This file controls:
- Model selection
- Sandbox settings (network, filesystem)
- Permission rules for bash commands
- Agent definitions (build, plan, auto modes)

**Location:** `.opencode/opencode.json` (this is the base config that gets used)

### Bash Permission Levels

| Level | Meaning |
|-------|---------|
| `allow` | Command runs without asking |
| `ask` | Prompts user for approval |
| `pin` | Requires PIN verification |
| `deny` | Blocked completely - cannot be bypassed |

### Safety Rules (Hardcoded Denials)

These commands are **always blocked** regardless of mode:
- `rm -rf *`, `rm -r *`, `rm -R *`, `rm --recursive *`, `rm -fr *` - No recursive directory deletion
- `dd *`, `mkfs *`, `fdisk *` - Disk operations
- `reboot`, `shutdown`, `halt`, `poweroff` - System control
- `chown *`, `chgrp *` - Ownership changes
- `shred *` - Secure delete
- `git push --force *`, `git reset --hard *` - Destructive git operations

Single file deletion (`rm file.txt`, `rm -f file.txt`) requires PIN.

## Container Profiles (Podman/Docker Isolation)

Container profiles provide **complete filesystem isolation** for AI agents. The agent runs inside a Podman/Docker container and can ONLY access directories you explicitly mount.

### Why Use Container Profiles?

Without a container profile, the agent runs on your host system and can see:
- Your entire home directory
- SSH keys, credentials, config files
- Any file your user can access

With a container profile, the agent ONLY sees:
- The specific folder(s) you mount
- The container's own filesystem (Debian/Alpine/etc.)
- Nothing else from your host

### How to Set Up a Container Profile

#### Step 1: Define the profile in `opencode.json`

```json
{
  "container": {
    "enabled": true,
    "runtime": "podman",
    "profiles": {
      "my-agent": {
        "name": "my-agent",
        "image": "docker.io/oven/bun:1.3",
        "volumes": [
          { 
            "host": "/home/user/my-project",
            "container": "/workspace",
            "readonly": false 
          }
        ],
        "environment": {
          "NODE_ENV": "development"
        },
        "network": {
          "mode": "bridge",
          "socketBridges": {
            "/tmp/my-api.sock": "localhost:8080"
          }
        },
        "workdir": "/workspace",
        "keepAlive": true,
        "idleTimeoutMinutes": 30
      }
    }
  }
}
```

#### Step 2: Start the container

```bash
swarm profile start my-agent
```

#### Step 3: Use in SDK

```typescript
import { createOpencode } from "@anthropic-ai/swarm-sdk"

const { spawn } = await createOpencode()

const handle = spawn({
  prompt: "List files in the workspace",
  containerProfile: "my-agent",  // <-- THIS IS THE KEY
  mode: "noninteractive",
})

await handle.wait()
```

### Profile Configuration Options

| Option | Description |
|--------|-------------|
| `name` | Unique profile identifier |
| `image` | Docker/Podman image (e.g., `docker.io/oven/bun:1.3`) |
| `volumes` | Array of `{host, container, readonly}` mount points |
| `environment` | Environment variables inside container |
| `network.mode` | `bridge` (default), `host`, or `none` |
| `network.socketBridges` | Unix socket to TCP bridges for local API access |
| `workdir` | Working directory inside container |
| `keepAlive` | Keep container running between commands |
| `idleTimeoutMinutes` | Auto-stop after idle (0 = never) |

### Managing Profiles

```bash
# List all profiles
swarm profile list

# Start a profile's container
swarm profile start my-agent

# Stop a profile's container
swarm profile stop my-agent

# Restart
swarm profile restart my-agent

# Check status
swarm profile status my-agent
```

### Profile Storage Location

Profiles are stored in: `~/.local/share/opencode/profiles/`

Each profile has a JSON file with its config and current state (running/stopped, container ID, etc.)

### Testing Container Isolation

Run a quick test to verify isolation is working:

```bash
# Execute commands directly in the container
podman exec $(podman ps -q --filter name=swarm-my-agent) pwd
podman exec $(podman ps -q --filter name=swarm-my-agent) ls -la /
podman exec $(podman ps -q --filter name=swarm-my-agent) ls /home
```

**Expected results:**
- `pwd` → `/workspace`
- `ls /` → Container filesystem (Debian/Alpine), NOT your host
- `ls /home` → Container user (e.g., `bun`), NOT your home directory files

### Common Mistakes

#### ❌ Using relative paths in volumes

```json
"volumes": [{ "host": ".", "container": "/workspace" }]
```

This is **unpredictable** - "." depends on where you run the server from.

#### ✅ Always use absolute paths

```json
"volumes": [{ "host": "/home/user/my-project", "container": "/workspace" }]
```

#### ❌ Forgetting to start the container

```typescript
spawn({ prompt: "...", containerProfile: "my-agent" })
// Error: Container not running
```

#### ✅ Start the profile first

```bash
swarm profile start my-agent
```

### Example Profiles

#### Voice Agent (isolated workspace)
```json
{
  "voice-agent": {
    "name": "voice-agent",
    "image": "docker.io/oven/bun:1.3",
    "volumes": [
      { "host": "/home/user/voiceagent", "container": "/workspace" }
    ],
    "network": {
      "mode": "bridge",
      "socketBridges": {
        "/tmp/voiceagent.sock": "localhost:8080"
      }
    },
    "workdir": "/workspace"
  }
}
```

#### Twitter Bot (with API access)
```json
{
  "twitter-bot": {
    "name": "twitter-bot",
    "image": "docker.io/oven/bun:1.3",
    "volumes": [
      { "host": "/home/user/twitter-bot", "container": "/workspace" }
    ],
    "environment": {
      "TWITTER_API_KEY": "..."
    },
    "network": {
      "mode": "bridge",
      "allowedDomains": ["api.twitter.com", "api.anthropic.com"]
    },
    "workdir": "/workspace"
  }
}
```

---

## SDK Usage

### Basic Usage

```typescript
import { createOpencode } from "@anthropic-ai/swarm-sdk"

// Start a server and get spawn function
const { spawn, client, server } = await createOpencode()

// Simple spawn
const handle = spawn("Fix the tests")
await handle.wait()

// With options
const handle = spawn({
  prompt: "Run the build",
  containerProfile: "my-agent",
  mode: "noninteractive",
})

// Stream events
for await (const event of handle.stream()) {
  if (event.type === "text") console.log(event.delta)
  if (event.type === "tool.start") console.log(`Running: ${event.name}`)
}
```

### Spawn Options

| Option | Description |
|--------|-------------|
| `prompt` | The task for the agent |
| `containerProfile` | Run inside this container profile |
| `mode` | `"noninteractive"` (auto-approve) or `"interactive"` |
| `profile` | Permission profile: `"analyze"`, `"edit"`, `"full"`, `"yolo"` |
| `agent` | Agent type: `"build"`, `"plan"`, custom |
| `tools` | Tool overrides `{ bash: true, edit: false }` |
| `onPermission` | Callback for interactive permission handling |
| `onComplete` | Callback when done (for fire-and-forget) |

### Permission Profiles

| Profile | Tools | Permissions |
|---------|-------|-------------|
| `analyze` | Read-only (read, grep, glob, list) | No modifications |
| `edit` | + edit, write | No bash |
| `full` | All tools | Bash requires approval |
| `yolo` | All tools | Everything auto-approved (DANGER!) |

### Custom Tools via MCP

```typescript
import { createOpencode, tool, createSwarmMcpServer, z } from "@anthropic-ai/swarm-sdk"

// Define a custom tool
const speakTool = tool(
  "speak",
  "Send text to voice API",
  { text: z.string() },
  async (args) => {
    await fetch("http://localhost:8080/speak", {
      method: "POST",
      body: JSON.stringify({ text: args.text })
    })
    return `Spoke: ${args.text}`
  },
  { permission: "allow" }
)

// Create MCP server with custom tools
const mcpServer = createSwarmMcpServer({
  name: "my-tools",
  tools: [speakTool],
})

// Use in spawn
spawn({
  prompt: "Say hello",
  mcpServers: { "my-tools": mcpServer },
})
```

---

## Git Workflow

This is a separate repo from the main opencode monorepo.

- No need to sync with upstream unless you want updates
- To update: copy new source from upstream and rebuild
