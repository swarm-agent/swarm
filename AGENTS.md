# Swarm CLI

AI-powered terminal for developers. This file provides guidance for AI agents working on this codebase.

## Quick Reference

| Command | Description |
|---------|-------------|
| `bun install` | Install dependencies |
| `./build.sh` | Build binary (auto-installs to ~/.local/bin) |
| `bun test` | Run all tests |
| `bun test test/path.test.ts` | Run single test file |
| `bun run typecheck` | Type check all packages (via turbo) |
| `bun dev` | Run development server |
| `./clean.sh` | Clean all build artifacts |
| `./create-release.sh` | Create GitHub release with platform binaries |

Binary output: `packages/swarm/dist/swarm-<platform>/bin/swarm`

## Project Structure

```
packages/
├── swarm/          # Main CLI application (Bun + TypeScript + SolidJS TUI)
│   ├── src/
│   │   ├── cli/cmd/tui/   # Terminal UI components (SolidJS + @opentui)
│   │   ├── tool/          # Tool implementations (bash, read, write, etc.)
│   │   ├── session/       # Session management, prompts
│   │   ├── config/        # Configuration loading
│   │   ├── server/        # HTTP API server
│   │   └── provider/      # LLM provider integrations
│   └── test/              # Test files (bun test)
├── sdk/js/         # JavaScript/TypeScript SDK (@swarm-ai/sdk)
├── plugin/         # Plugin system (@swarm-ai/plugin)
└── script/         # Build utilities (@swarm-ai/script)
```

## Configuration

Config file: `.swarm/swarm.json` or `.swarm/swarm.jsonc`

```json
{
  "model": "anthropic/claude-sonnet-4-5",
  "sandbox": {
    "enabled": true
  }
}
```

Config is loaded from multiple sources (merged in order):
1. Global config: `~/.config/swarm/swarm.json`
2. Project config: `.swarm/swarm.json` (searched up from cwd)
3. `SWARM_CONFIG` env var (path to custom config)
4. `SWARM_CONFIG_CONTENT` env var (inline JSON)

## SDK (Important!)

The SDK allows programmatic control of Swarm sessions:

```typescript
import { createSwarm, tool, z } from "@swarm-ai/sdk"

// Create a swarm instance
const { spawn, server } = await createSwarm()

// Spawn an agent session
const handle = spawn("List all TypeScript files")
await handle.wait()

// Clean up
server.close()
```

### Custom Tools

```typescript
import { createSwarm, tool, z } from "@swarm-ai/sdk"

const myTool = tool({
  name: "greet",
  description: "Greet a user",
  parameters: z.object({
    name: z.string().describe("Name to greet"),
  }),
  execute: async ({ name }) => {
    return `Hello, ${name}!`
  },
})

const { spawn, server } = await createSwarm({
  tools: [myTool],
})
```

## Code Style

- **Runtime**: Bun with TypeScript ESM modules
- **UI Framework**: SolidJS with @opentui for terminal rendering
- **Imports**: Relative imports for local modules, path aliases `@/*` and `@tui/*`
- **Types**: Zod schemas for validation, TypeScript interfaces for structure
- **Naming**: camelCase for variables/functions, PascalCase for classes/namespaces
- **Error handling**: Use Result patterns, avoid throwing exceptions in tools
- **File structure**: Namespace-based organization (e.g., `Tool.define()`, `Session.create()`)
- **Logging**: Use `Log.create({ service: "name" })` pattern

## Architecture Patterns

### Tool Definition

Tools implement the `Tool.Info` interface:

```typescript
import { Tool } from "../tool/tool"
import z from "zod"

export const MyTool = Tool.define<z.ZodObject<...>, MetadataType>(
  "mytool",
  {
    description: "What this tool does",
    parameters: z.object({
      param: z.string().describe("Parameter description"),
    }),
    async execute(args, ctx) {
      // ctx.sessionID, ctx.abort, ctx.metadata() available
      return {
        title: "Result title",
        metadata: { /* streaming metadata */ },
        output: "Result output",
      }
    },
  }
)
```

### Context & DI

- Pass `sessionID` in tool context
- Use `App.provide()` for dependency injection
- Storage via `Storage` namespace for persistence

### API Server

The TypeScript server (`packages/swarm/src/server/`) exposes HTTP endpoints. When modifying endpoints, regenerate the client SDK.

## Testing

```bash
# Run all tests
bun test

# Run specific test file
bun test packages/swarm/test/tool/bash.test.ts

# Run tests matching pattern
bun test --grep "pattern"
```

Test files use `.test.ts` extension and are located in `packages/swarm/test/`.

## Key Files

| File | Purpose |
|------|---------|
| `packages/swarm/src/index.ts` | CLI entry point |
| `packages/swarm/src/tool/registry.ts` | Tool registration |
| `packages/swarm/src/config/config.ts` | Config loading logic |
| `packages/swarm/src/cli/cmd/tui/app.tsx` | TUI application root |
| `packages/sdk/js/src/index.ts` | SDK entry point |

## Development

See `packages/swarm/AGENTS.md` for detailed development guidelines including:
- Tool animation system (custom spinners, streaming output)
- TUI component patterns
- State management

## Session Log

| Date | Summary |
|------|---------|
| 2024-12-21 | Regenerated SDK types and fixed budget token calculation in provider transform |
| 2024-12-20 | Restored original background agent indicator style with border |
| 2024-12-20 | Simplified background agent indicator to global muted display |
| 2024-12-20 | Use useRoute() directly in BackgroundAgentsBlock for proper SolidJS reactivity |
| 2024-12-20 | Fixed SolidJS reactivity for background agent session filtering in status indicator |
| 2024-12-20 | Fixed background agent indicator to require exact session ID match |
| 2024-12-20 | Added create-release.sh script for GitHub releases with multi-platform binaries |
| 2024-12-20 | Simplified status bar indicator back to icon + count (reverted complex display) |
| 2024-12-20 | Added background agents section to sidebar showing child agents tied to parent session |
| 2024-12-20 | Enhanced background agent indicator with description and completion state display |

## Containerized Agents (SDK)

The SDK supports spawning agents in isolated containers with custom tools via MCP.

### Container Spawning

```typescript
import { spawnContainer, sendMessage, stopContainer } from "@swarm-ai/sdk/container"

const container = await spawnContainer({
  name: "my-agent",
  workspace: "/path/to/workspace",  // Mounted at /workspace in container
  volumes: [
    // Additional volume mounts - MUST mount inside /workspace for agent visibility
    { host: "/path/on/host", container: "/workspace/mydir", readonly: false },
  ],
  network: "host",  // "host" for localhost access, "bridge" for isolation
  model: "anthropic/claude-opus-4-5",
  system: "You are a helpful assistant...",
})

// Send messages
const result = await sendMessage(container.url, sessionId, "Hello", {
  autoApprove: true,
  timeout: 300000,
  thinking: false,  // Disable extended thinking for fast responses
})

// Clean up
await container.stop()
```

### Volume Mounting (IMPORTANT!)

**The agent's working directory is `/workspace`**. If you mount volumes elsewhere (e.g., `/mydir`), the agent won't find them naturally when exploring.

```typescript
// WRONG - agent won't find this easily
volumes: [{ host: "/data", container: "/data" }]

// CORRECT - agent sees it at /workspace/data
volumes: [{ host: "/data", container: "/workspace/data" }]
```

### MCP Tools (Host-side)

Tools run on the HOST, not inside the container. This lets them access host resources (git credentials, local APIs, etc).

```typescript
import { startMcpServer, tool, z } from "@swarm-ai/sdk"

// Define a "mega tool" with multiple actions (reduces tool count)
const myTool = tool(
  "mytool",
  `Description of what this tool does.

Actions:
- action1: Does thing 1
- action2: Does thing 2`,
  {
    action: z.enum(["action1", "action2"]).describe("Action to perform"),
    param: z.string().optional().describe("Optional parameter"),
  },
  async (args) => {
    switch (args.action) {
      case "action1": return "Result 1"
      case "action2": return "Result 2"
      default: return `Unknown action: ${args.action}`
    }
  },
  { permission: "allow" }
)

// Start MCP server
const mcpServer = await startMcpServer({
  name: "my-tools",
  port: 19876,
  hostname: "127.0.0.1",
  tools: [myTool],
})

// Register with container (use host network so localhost works)
await container.client.mcp.add({
  body: {
    name: "my-tools",
    config: { type: "remote", url: `http://localhost:${mcpServer.port}` },
  },
})
```

### Mega-Tool Pattern

Instead of 50 individual tools, consolidate into "mega tools" with an `action` parameter:

```typescript
// Instead of: spotify_play, spotify_pause, spotify_next, spotify_search...
// Use one tool:
const spotifyTool = tool(
  "spotify",
  `Control Spotify. Actions: play, pause, next, search, ...`,
  {
    action: z.enum(["play", "pause", "next", "search", ...]),
    query: z.string().optional(),
    uri: z.string().optional(),
  },
  async (args) => {
    switch (args.action) {
      case "play": ...
      case "pause": ...
    }
  }
)
```

Benefits:
- Fewer tools = less token overhead in system prompt
- Related actions grouped logically
- Easier to extend

### Container Options Reference

```typescript
type ContainerOptions = {
  name: string              // Unique container name
  workspace: string         // Host path → /workspace
  volumes?: Array<{         // Additional mounts
    host: string
    container: string
    readonly?: boolean
  }>
  port?: number             // API port (0 = auto)
  runtime?: "podman" | "docker"
  network?: "bridge" | "host"  // host = share host network
  system?: string           // System prompt
  model?: string            // Model ID
}
```

### Container Security Modes

**Host Network (Development)**
```typescript
// Container shares host network - can access localhost services directly
const container = await spawnContainer({
  network: "host",  // Default
  ...
})

// MCP via localhost
const mcpServer = await startMcpServer({
  port: 19876,
  hostname: "127.0.0.1",
  tools: [...],
})
await container.client.mcp.add({
  body: { name: "tools", config: { type: "remote", url: "http://localhost:19876" } },
})
```

**Bridge Network + Unix Socket (Secure/Production)**

Container is isolated from host localhost but has internet access. MCP tools accessible via mounted unix socket.

```typescript
const MCP_SOCKET = "/tmp/my-mcp.sock"

// 1. Start MCP server on socket (not TCP port)
const mcpServer = await startMcpServer({
  socket: MCP_SOCKET,  // Unix socket instead of port
  tools: [...],
})

// 2. Spawn container with bridge network + socket mount
const container = await spawnContainer({
  network: "bridge",  // Isolated from host localhost
  volumes: [
    { host: MCP_SOCKET, container: "/mcp.sock", readonly: false },
  ],
  ...
})

// 3. Register MCP with socket type
await container.client.mcp.add({
  body: {
    name: "tools",
    config: { type: "socket", socket: "/mcp.sock" },
  },
})
```

**Security Comparison**

| Mode | Host localhost | Internet | MCP Access |
|------|----------------|----------|------------|
| `host` | ✅ Full access | ✅ Yes | Via localhost port |
| `bridge` + socket | ❌ Blocked | ✅ Yes | Via mounted socket only |

With bridge + socket:
- Container **cannot** access host services (databases, APIs on localhost)
- Container **can** access the internet (web APIs, etc.)
- Container **can** use MCP tools via the explicitly mounted socket
- You control exactly what the agent can do through MCP tool definitions

## Notes
- swarm-task and swarm-theme tools are opt-in - only enabled when SWARM_AGENT_API_KEY is configured or user runs `swarm auth login` for swarmagent provider.

- Memory tool creates sections if they don't exist
- TypeScript native preview (`tsgo`) used for type checking
- Bun preloads `@opentui/solid/preload` for JSX support
- Config supports JSONC (JSON with comments)
- Memory.init() must be called in InstanceBootstrap (not server.ts) to ensure proper Bus scope

## Session Log

- Added swarm-task and swarm-theme tools for SwarmAgent web dashboard integration. Fixed PostgreSQL affectedRows compatibility bug in swarm-web that was causing result submission to fail with 409 errors.
