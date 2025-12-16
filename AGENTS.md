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

## Swarm SDK - Complete Reference

The SDK lets you programmatically spawn AI agents that run in isolated containers with custom tools.

### Import Path

```typescript
// From source (during development)
import { createOpencode, tool, createSwarmMcpServer, z } from "/path/to/swarm-cli/packages/sdk/js/src/index.ts"

// Or copy sdk-dist/ to your project and import from there
import { createOpencode, tool, createSwarmMcpServer, z } from "./sdk-dist/index.js"
```

### What the SDK Exports

| Export | Purpose |
|--------|---------|
| `createOpencode(options)` | Start a server, get `{spawn, client, server}` |
| `spawn(prompt \| options)` | Run an agent session |
| `tool(name, desc, params, fn, opts)` | Define a custom tool |
| `createSwarmMcpServer({name, tools})` | Bundle tools into an MCP server |
| `createSwarmProfile(config)` | Create custom permission profiles |
| `z` | Re-exported Zod (use for tool parameter schemas) |
| `loadEnvFile(path)` | Load .env file to object |
| `injectEnvFile(path)` | Load .env and inject into process.env |

---

### Quick Start: Minimal Agent

```typescript
import { createOpencode } from "./sdk-dist/index.js"

const { spawn, server } = await createOpencode()

const handle = spawn("List all TypeScript files")
await handle.wait()

server.close()
```

---

### Quick Start: Agent in Container with Custom Tool

```typescript
import { createOpencode, tool, createSwarmMcpServer, z } from "./sdk-dist/index.js"

// 1. Define a custom tool
const notifyTool = tool(
  "notify",
  "Send a notification to the user",
  { message: z.string().describe("The message to send") },
  async (args) => {
    console.log(`[NOTIFY] ${args.message}`)
    return `Notified: ${args.message}`
  },
  { permission: "allow" }
)

// 2. Bundle into MCP server
const mcpServer = createSwarmMcpServer({
  name: "my-tools",
  tools: [notifyTool],
})

// 3. Start SDK server (auto-starts container profile if specified)
const { spawn, client, server } = await createOpencode({
  system: "You are a helpful assistant. Use the notify tool to communicate.",
  profile: "my-agent",  // Container profile name
  port: 4096,
})

// 4. Register MCP server with the running swarm server
await client.mcp.add({
  body: {
    name: "my-tools",
    config: { type: "remote", url: "http://localhost:19876" },  // You need to serve mcpServer separately
  },
})

// 5. Spawn agent in container
const handle = spawn({
  prompt: "Say hello to the user",
  containerProfile: "my-agent",
  mode: "noninteractive",
})

// 6. Stream events
for await (const event of handle.stream()) {
  if (event.type === "text" && event.delta) {
    process.stdout.write(event.delta)
  }
  if (event.type === "tool.start") {
    console.log(`\n[TOOL] ${event.name}`)
  }
}

server.close()
```

---

### createOpencode(options) - Start the Server

```typescript
const { spawn, client, server } = await createOpencode({
  // System prompt applied to all sessions
  system: "You are a coding assistant.",
  
  // Container profile to auto-start (optional)
  profile: "my-agent",
  
  // Server binding
  hostname: "127.0.0.1",
  port: 4096,
  
  // Startup timeout (ms)
  timeout: 5000,
})
```

**Returns:**
- `spawn` - Function to create agent sessions
- `client` - Raw API client for advanced operations
- `server` - Server handle with `url` and `close()` method

---

### spawn(options) - Run an Agent

```typescript
// Simple: just a prompt
const handle = spawn("Fix the failing tests")

// Full options
const handle = spawn({
  // Required
  prompt: "Deploy to staging",
  
  // Container isolation (IMPORTANT!)
  containerProfile: "my-agent",
  
  // Permission mode
  mode: "noninteractive",  // auto-approve (for bots)
  // mode: "interactive",  // use onPermission callback
  
  // Permission profile (predefined)
  profile: "full",  // or "analyze", "edit", "yolo"
  
  // Agent type
  agent: "build",  // or "plan", "general", custom
  
  // Tool overrides (merge with profile)
  tools: { bash: true, edit: false },
  
  // Interactive permission handling
  onPermission: async (req) => {
    console.log(`Permission requested: ${req.title}`)
    return "approve"  // or "reject", "always", { pin: "1234" }
  },
  
  // Fire-and-forget callback
  onComplete: (result) => {
    console.log(`Done: ${result.success}`)
  },
})
```

**SpawnHandle methods:**
```typescript
// Wait for completion (blocks)
const result = await handle.wait()

// Stream events (async generator)
for await (const event of handle.stream()) {
  // event.type: "text", "tool.start", "tool.end", "permission", "completed", "aborted", "error"
}

// Get session ID
const sessionId = await handle.sessionId

// Abort the session
await handle.abort()

// Respond to permission manually
await handle.respondToPermission(permissionId, "approve")
```

---

### Event Types from stream()

```typescript
for await (const event of handle.stream()) {
  switch (event.type) {
    case "text":
      // event.text - full text so far
      // event.delta - new text since last event
      break
    
    case "tool.start":
      // event.name - tool name (e.g., "bash", "edit", "mcp__my-tools__notify")
      // event.input - tool arguments
      break
    
    case "tool.end":
      // event.name - tool name
      // event.output - tool result
      break
    
    case "permission":
      // event.id - permission ID
      // event.permissionType - "bash", "edit", "pin", etc.
      // event.title - human-readable description
      // event.metadata - additional context
      break
    
    case "permission.handled":
      // event.id - permission ID
      // event.response - how it was handled
      break
    
    case "todo":
      // event.todos - array of todo items
      break
    
    case "completed":
      // Session finished successfully
      break
    
    case "aborted":
      // Session was aborted
      break
    
    case "error":
      // event.error - Error object
      break
  }
}
```

---

### Permission Profiles

| Profile | Description | Tools Enabled | Bash |
|---------|-------------|---------------|------|
| `analyze` | Read-only | read, grep, glob, list, webfetch | ❌ Denied |
| `edit` | File editing | + edit, write, patch | ❌ Denied |
| `full` | Everything | All tools | ⚠️ Asks permission |
| `yolo` | No restrictions | All tools | ✅ Auto-approved |

```typescript
// Use predefined profile
spawn({ prompt: "...", profile: "full" })

// Create custom profile
import { createSwarmProfile } from "./sdk-dist/index.js"

const myProfile = createSwarmProfile({
  name: "backend-dev",
  base: "full",  // extend from predefined
  envFile: ".env.backend",  // load secrets
  tools: { "background-agent": false },  // disable specific tools
  permission: {
    bash: { "npm *": "allow", "bun *": "allow" },  // allow specific commands
  },
})

spawn({ prompt: "...", profile: myProfile })
```

---

### Custom Tools with tool()

```typescript
import { tool, z } from "./sdk-dist/index.js"

const myTool = tool(
  // Name (alphanumeric, underscores, hyphens)
  "search_docs",
  
  // Description (shown to Claude)
  "Search internal documentation for a query",
  
  // Parameters (Zod schema object)
  {
    query: z.string().describe("Search query"),
    limit: z.number().optional().default(10).describe("Max results"),
  },
  
  // Execute function
  async (args, context) => {
    // args.query, args.limit are typed
    // context.sessionID, context.abort available
    
    const results = await searchDocs(args.query, args.limit)
    return `Found ${results.length} results:\n${results.join("\n")}`
    
    // Or return structured result:
    // return { content: [{ type: "text", text: "..." }] }
  },
  
  // Options
  {
    permission: "allow",  // "allow", "ask", "pin", "deny"
  }
)
```

**Permission levels for tools:**
| Level | Behavior |
|-------|----------|
| `allow` | Runs immediately, no prompt |
| `ask` | Requires user approval |
| `pin` | Requires PIN verification |
| `deny` | Blocked, returns error |

---

### MCP Server for Custom Tools

Tools need to be served via MCP protocol. Two approaches:

#### Approach 1: HTTP MCP Server (Recommended for production)

```typescript
import { createSwarmMcpServer, tool, z } from "./sdk-dist/index.js"
import { serve } from "bun"

const mcpServer = createSwarmMcpServer({
  name: "my-tools",
  tools: [
    tool("greet", "Say hello", { name: z.string() }, async (args) => `Hello, ${args.name}!`),
    tool("time", "Get current time", {}, async () => new Date().toISOString()),
  ],
})

// Serve MCP over HTTP
const httpServer = serve({
  port: 19876,
  async fetch(req) {
    const body = await req.json()
    
    if (body.method === "initialize") {
      return Response.json({
        jsonrpc: "2.0",
        id: body.id,
        result: {
          protocolVersion: "2024-11-05",
          capabilities: { tools: {} },
          serverInfo: { name: "my-tools", version: "1.0.0" },
        },
      })
    }
    
    if (body.method === "tools/list") {
      return Response.json({
        jsonrpc: "2.0",
        id: body.id,
        result: { tools: mcpServer.listTools() },
      })
    }
    
    if (body.method === "tools/call") {
      const result = await mcpServer.executeTool(
        body.params?.name || "",
        body.params?.arguments || {},
        {}
      )
      return Response.json({ jsonrpc: "2.0", id: body.id, result })
    }
    
    return Response.json({ error: "Unknown method" })
  },
})

// Register with swarm
await client.mcp.add({
  body: {
    name: "my-tools",
    config: { type: "remote", url: "http://localhost:19876" },
  },
})
```

#### Approach 2: In-Process (simpler, for development)

```typescript
// Pass mcpServers directly to spawn (not yet fully implemented in server)
spawn({
  prompt: "Use my tools",
  mcpServers: { "my-tools": mcpServer },
})
```

---

### Container Profiles - Full Setup

#### 1. Create profile JSON file

Location: `~/.local/share/opencode/profiles/my-agent.json`

```json
{
  "name": "my-agent",
  "config": {
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
    "idleTimeoutMinutes": 30,
    "permission": {
      "edit": "allow",
      "bash": "allow",
      "webfetch": "allow"
    }
  },
  "status": "stopped",
  "sessionCount": 0
}
```

#### 2. Start the container

```bash
swarm profile start my-agent
```

#### 3. Use in SDK

```typescript
const { spawn } = await createOpencode({ profile: "my-agent" })

spawn({
  prompt: "Build the project",
  containerProfile: "my-agent",
  mode: "noninteractive",
})
```

#### 4. Check status

```bash
swarm profile status my-agent
swarm profile list
```

---

### Complete Example: Voice Agent

See `/home/roy/pi/voiceagent/swarm-agent/voice-agent.ts` for a full implementation with:
- Custom "speak" tool
- Session persistence
- HTTP API for external integration
- Container isolation
- Fire-and-forget task execution

Key patterns:
```typescript
// Fire and forget (returns immediately)
agent.fireAndForget("Do something in background", (success) => {
  console.log(`Task completed: ${success}`)
})

// Continue conversation in same session
const response = await agent.continueConversation("Follow up question")

// New conversation (creates new session)
const { response, sessionId } = await agent.newConversation("New task")
```

---

### SDK File Structure

```
packages/sdk/js/
├── src/
│   ├── index.ts          # Main exports
│   ├── spawn.ts          # spawn() implementation
│   ├── server.ts         # createOpencodeServer()
│   ├── client.ts         # API client
│   ├── tool.ts           # tool() helper
│   ├── mcp-server.ts     # createSwarmMcpServer()
│   ├── profiles.ts       # Permission profiles
│   ├── env.ts            # Environment loading
│   └── gen/              # Generated API types
└── example/
    └── *.ts              # Example scripts
```

---

### Troubleshooting

#### "Container not running"
```bash
swarm profile start my-agent
```

#### "Permission denied" in container
Check volume mounts in profile config. Container user must have access.

#### Tools not appearing
1. Check MCP server is running
2. Check registration: `client.mcp.list()`
3. Tool names are prefixed: `mcp__server-name__tool-name`

#### Session hangs
- In `interactive` mode without `onPermission` callback = hangs forever
- Use `noninteractive` for bots
- Check for permission requests in event stream

---

## Git Workflow

This is a separate repo from the main opencode monorepo.

- No need to sync with upstream unless you want updates
- To update: copy new source from upstream and rebuild
