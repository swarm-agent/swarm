# Swarm SDK - Complete LLM Reference

> **For LLMs**: This is YOUR reference for building agents with the Swarm SDK.
> Read this BEFORE writing any code. The SDK abstracts container orchestration, permissions, and tool execution.

## TL;DR - What This SDK Does

```
Your Code (TypeScript)
       ↓
   SDK spawn()
       ↓
  Swarm Server (auto-started)
       ↓
  Agent executes in container (podman/docker)
       ↓
  Results streamed back via SSE
```

**You write TypeScript. The SDK handles containers, permissions, and execution.**

---

## Quick Start

### Install

```bash
npm install @opencode-ai/sdk zod
# or
bun add @opencode-ai/sdk zod
```

### Pattern 1: Simple Spawn

```typescript
import { createOpencode } from "@opencode-ai/sdk"

const { spawn, server } = await createOpencode()

// Fire and wait
const result = await spawn("analyze this codebase and list all TODO comments").wait()
console.log(result.summary)

server.close()
```

### Pattern 2: Stream Events

```typescript
const handle = spawn("refactor the auth module")

for await (const event of handle.stream()) {
  switch (event.type) {
    case "text":
      process.stdout.write(event.delta || "")
      break
    case "tool.start":
      console.log(`\nUsing tool: ${event.name}`)
      break
    case "tool.end":
      console.log(`Result: ${event.output.slice(0, 100)}...`)
      break
    case "completed":
      console.log("\nDone!")
      break
  }
}
```

### Pattern 3: Custom Tools

```typescript
import { createOpencode, tool, createSwarmMcpServer } from "@opencode-ai/sdk"
import { z } from "zod"

// Define a tool
const searchFiles = tool(
  "search_files",
  "Search for files matching a pattern",
  {
    pattern: z.string().describe("Glob pattern like *.ts"),
    directory: z.string().optional().describe("Directory to search in"),
  },
  async (args) => {
    // Your implementation
    return `Found 15 files matching ${args.pattern}`
  }
)

// Create MCP server with tools
const mcpServer = createSwarmMcpServer({
  name: "my-tools",
  tools: [searchFiles],
})

// Use in spawn
const { spawn, client, server } = await createOpencode()

// Register MCP server
await client.mcp.add({
  body: {
    name: "my-tools",
    config: { type: "remote", url: "http://localhost:19876" },
  },
})

await spawn("Find all TypeScript files in src/").wait()
```

---

## Core Concepts

### 1. `createOpencode()` - Entry Point

Starts a swarm server and returns clients:

```typescript
const { client, spawn, server } = await createOpencode({
  // Optional: specify port
  port: 3456,
  // Optional: working directory
  cwd: "/path/to/project",
})

// client - HTTP client for direct API calls
// spawn - spawn() function for running agents
// server - server handle (call server.close() when done)
```

### 2. `spawn()` - Run Agents

```typescript
// String form (simple)
spawn("do something")

// Options form (full control)
spawn({
  prompt: "do something",
  agent: "code",              // Optional: agent type
  profile: "full",            // Permission profile: analyze|edit|full|yolo
  mode: "noninteractive",     // Permission mode: interactive|noninteractive
  onPermission: async (p) => "approve",  // For interactive mode
  tools: { bash: true, edit: true },     // Override specific tools
})
```

**Returns `SpawnHandle`:**

```typescript
interface SpawnHandle {
  sessionId: string
  stream(): AsyncIterable<SpawnEvent>  // Real-time events
  wait(): Promise<SpawnResult>         // Block until done
  abort(): void                        // Cancel execution
  respondToPermission(id: string, response: PermissionResponse): Promise<void>
}
```

### 3. `SpawnEvent` - Event Types

```typescript
type SpawnEvent =
  | { type: "text"; delta?: string; text?: string }
  | { type: "tool.start"; name: string; input: Record<string, unknown> }
  | { type: "tool.end"; name: string; output: string }
  | { type: "todo"; todos: Array<{ content: string; status: string }> }
  | { type: "permission"; id: string; permissionType: string; title: string; metadata: unknown }
  | { type: "permission.handled"; id: string; response: string }
  | { type: "completed"; result: SpawnResult }
  | { type: "error"; error: { message: string } }
```

### 4. `SpawnResult` - Final Result

```typescript
interface SpawnResult {
  success: boolean
  error?: string
  duration_ms: number
  tool_calls: number
  files_modified: string[]
  summary: string
}
```

---

## Custom Tools

### `tool()` - Define Tools

```typescript
import { tool } from "@opencode-ai/sdk"
import { z } from "zod"

const myTool = tool(
  "tool_name",           // Unique name (alphanumeric, underscores, hyphens)
  "Description for AI",  // Be detailed - AI uses this to decide when to call
  {
    // Zod schema for parameters
    param1: z.string().describe("What this param is for"),
    param2: z.number().optional().default(10),
    param3: z.enum(["a", "b", "c"]),
  },
  async (args, context) => {
    // Implementation
    // args is typed based on schema
    // Return string or ToolResult
    return `Result: ${args.param1}`
  },
  {
    permission: "allow",  // allow|ask|pin|deny
  }
)
```

### Permission Levels

| Level | Behavior |
|-------|----------|
| `allow` | Executes immediately, no approval needed |
| `ask` | Requires user approval before executing |
| `pin` | Requires PIN verification (for sensitive ops) |
| `deny` | Always blocked, returns error |

### `createSwarmMcpServer()` - Host Tools

```typescript
import { createSwarmMcpServer } from "@opencode-ai/sdk"

const server = createSwarmMcpServer({
  name: "my-api",        // Server name (used in tool namespacing)
  version: "1.0.0",      // Optional version
  tools: [tool1, tool2], // Array of tools
})

// Server methods:
server.listTools()                    // Get all tool definitions
server.executeTool(name, args, ctx)   // Execute a tool
server.getTool(name)                  // Get tool by name
```

### Complete Tools Example

```typescript
import { createOpencode, tool, createSwarmMcpServer } from "@opencode-ai/sdk"
import { z } from "zod"
import { serve } from "bun"

// Define tools with different permission levels
const readData = tool(
  "read_data",
  "Read data from the database",
  { table: z.string(), limit: z.number().optional().default(100) },
  async (args) => {
    return `Read ${args.limit} rows from ${args.table}`
  },
  { permission: "allow" }  // Safe read - no approval needed
)

const writeData = tool(
  "write_data",
  "Write data to the database",
  { table: z.string(), data: z.record(z.unknown()) },
  async (args) => {
    return `Wrote to ${args.table}`
  },
  { permission: "ask" }  // Requires approval
)

const deleteData = tool(
  "delete_data",
  "Delete records from database",
  { table: z.string(), where: z.string() },
  async (args) => {
    return `Deleted from ${args.table} where ${args.where}`
  },
  { permission: "pin" }  // Requires PIN
)

// Create MCP server
const mcpServer = createSwarmMcpServer({
  name: "database",
  tools: [readData, writeData, deleteData],
})

// Host as HTTP server (MCP protocol)
const httpServer = serve({
  port: 19876,
  async fetch(req) {
    const body = await req.json()
    
    if (body.method === "initialize") {
      return Response.json({
        jsonrpc: "2.0", id: body.id,
        result: {
          protocolVersion: "2024-11-05",
          capabilities: { tools: {} },
          serverInfo: { name: "database", version: "1.0.0" },
        },
      })
    }
    
    if (body.method === "tools/list") {
      return Response.json({
        jsonrpc: "2.0", id: body.id,
        result: { tools: mcpServer.listTools() },
      })
    }
    
    if (body.method === "tools/call") {
      const result = await mcpServer.executeTool(
        body.params.name,
        body.params.arguments || {},
        { onPermission: async (p) => true }  // Auto-approve for demo
      )
      return Response.json({
        jsonrpc: "2.0", id: body.id,
        result,
      })
    }
    
    return Response.json({ error: "Unknown method" })
  },
})

// Register with swarm
const { client, spawn, server } = await createOpencode()
await client.mcp.add({
  body: { name: "database", config: { type: "remote", url: "http://localhost:19876" } },
})

// Now agent can use your tools
await spawn("Read the users table and summarize the data").wait()

httpServer.stop()
server.close()
```

---

## Permission Handling

### Mode: Non-Interactive (Default)

For CI/automation - permissions are auto-handled:

```typescript
spawn({
  prompt: "run tests and fix failures",
  mode: "noninteractive",  // This is the default
})
```

Behavior:
- `allow` tools → Execute immediately
- `ask` tools → Auto-approved
- `pin` tools → Rejected (no way to enter PIN)
- `deny` tools → Blocked

### Mode: Interactive

For programmatic control with human-in-the-loop:

```typescript
spawn({
  prompt: "deploy to production",
  mode: "interactive",
  onPermission: async (request) => {
    // request.id - Permission ID
    // request.permissionType - "bash", "edit", "pin", etc.
    // request.title - Human-readable description
    // request.metadata - Additional context
    
    console.log(`Permission requested: ${request.title}`)
    
    // Return one of:
    return "approve"          // Approve once
    return "always"           // Approve and remember
    return "reject"           // Reject
    return { reject: "reason" }  // Reject with message
    return { pin: "1234" }    // Provide PIN
  },
})
```

### Manual Permission Response

For advanced control:

```typescript
const handle = spawn({
  prompt: "do something sensitive",
  mode: "interactive",
  // No onPermission callback - handle manually
})

for await (const event of handle.stream()) {
  if (event.type === "permission") {
    // Decide asynchronously
    const shouldApprove = await askUserSomehow(event.title)
    await handle.respondToPermission(
      event.id,
      shouldApprove ? "approve" : "reject"
    )
  }
}
```

---

## Profiles

### Built-in Profiles

| Profile | Tools | Bash | Use Case |
|---------|-------|------|----------|
| `analyze` | read, glob, grep, list, webfetch | deny | Code review, understanding |
| `edit` | + edit, write, patch | deny | Safe refactoring |
| `full` | all tools | ask | Full tasks with oversight |
| `yolo` | all tools | allow | CI/automation (DANGEROUS!) |

```typescript
spawn({ prompt: "...", profile: "analyze" })
spawn({ prompt: "...", profile: "edit" })
spawn({ prompt: "...", profile: "full" })
spawn({ prompt: "...", profile: "yolo" })  // Only in CI!
```

### Custom Profiles

```typescript
import { createSwarmProfile, createSwarmMcpServer, tool } from "@opencode-ai/sdk"

const myProfile = createSwarmProfile({
  name: "backend-dev",
  description: "Backend development with database access",
  base: "full",                    // Start from built-in profile
  envFile: ".env.development",     // Load secrets from file
  env: { NODE_ENV: "development" }, // Additional env vars
  agent: "code",                   // Default agent
  model: "your-preferred-model",   // Default model
  tools: {
    webfetch: false,               // Disable specific tools
  },
  permission: {
    bash: { "npm *": "allow" },    // Custom bash permissions
  },
  mcpServers: {
    "my-db": createSwarmMcpServer({ name: "my-db", tools: [...] }),
  },
})

// Use profile
spawn({ prompt: "...", profile: myProfile })
```

### Environment Loading

```typescript
import { loadEnvFile, injectEnvFile, createEnvLoader } from "@opencode-ai/sdk"

// Load and get vars (doesn't modify process.env)
const vars = await loadEnvFile(".env.production")
console.log(vars.API_KEY)

// Load and inject into process.env
await injectEnvFile(".env.production")
console.log(process.env.API_KEY)

// Create reusable loader
const envLoader = createEnvLoader(".env.production")
if (envLoader.exists()) {
  await envLoader.inject()
}
```

---

## Container Profiles

### What They Are

Container profiles let you run agents inside isolated containers (podman/docker).
**All bash commands are routed through the container.**

### Configuration (opencode.json)

```json
{
  "container": {
    "runtime": "podman",
    "profiles": {
      "sandbox": {
        "name": "sandbox",
        "image": "ubuntu:22.04",
        "workdir": "/workspace",
        "volumes": [
          { "host": ".", "container": "/workspace", "readonly": false }
        ],
        "environment": {
          "NODE_ENV": "development"
        },
        "network": {
          "mode": "bridge",
          "exposePorts": [3000, 8080]
        }
      }
    }
  }
}
```

### Network Modes

| Mode | Behavior |
|------|----------|
| `bridge` | Isolated network, container can reach out, nothing can reach in |
| `none` | No network access at all |
| `host` | Full host network access (not recommended) |

### Volume Options

```json
{
  "volumes": [
    { "host": ".", "container": "/workspace", "readonly": false },
    { "host": "~/.npm", "container": "/root/.npm", "readonly": false },
    { "host": "/etc/ssl", "container": "/etc/ssl", "readonly": true }
  ]
}
```

### Using Container Profiles

```bash
# CLI
swarm run --profile sandbox "install dependencies and run tests"
swarm serve --profile sandbox

# Programmatically
# Profile is started automatically when sessions use it
```

### ContainerRuntime API (Internal)

The SDK uses this internally, but you can access it:

```typescript
import { ContainerRuntime } from "@opencode-ai/sdk"

// Check if runtime is available
const hasPodman = await ContainerRuntime.checkRuntime("podman")

// Create container
const containerId = await ContainerRuntime.create({
  runtime: "podman",
  name: "my-container",
  image: "ubuntu:22.04",
  workdir: "/workspace",
  volumes: [{ host: ".", container: "/workspace", readonly: false }],
  environment: { FOO: "bar" },
  network: { mode: "bridge" },
})

// Start, exec, stop
await ContainerRuntime.start(containerId)
const result = await ContainerRuntime.exec(containerId, ["ls", "-la"])
console.log(result.stdout)
await ContainerRuntime.stop(containerId)
await ContainerRuntime.remove(containerId)
```

---

## Complete Example: Code Review Bot

A bot that reviews PRs, runs linters, and posts comments.

```typescript
#!/usr/bin/env bun
import { createOpencode, tool, createSwarmMcpServer, createSwarmProfile } from "@opencode-ai/sdk"
import { z } from "zod"
import { serve } from "bun"

// =============================================================================
// TOOLS
// =============================================================================

const runLinter = tool(
  "run_linter",
  "Run ESLint on specified files and return issues found",
  {
    files: z.array(z.string()).describe("File paths to lint"),
  },
  async (args) => {
    // In real implementation, shell out to eslint
    console.log(`[run_linter] Checking ${args.files.length} files`)
    return JSON.stringify({
      errors: 2,
      warnings: 5,
      issues: [
        { file: args.files[0], line: 10, message: "Unused variable 'x'" },
        { file: args.files[0], line: 25, message: "Missing return type" },
      ],
    })
  },
  { permission: "allow" }
)

const runTests = tool(
  "run_tests",
  "Run test suite and return results",
  {
    pattern: z.string().optional().describe("Test file pattern"),
  },
  async (args) => {
    console.log(`[run_tests] Running tests matching: ${args.pattern || "*"}`)
    return JSON.stringify({
      total: 50,
      passed: 48,
      failed: 2,
      failures: [
        { test: "auth.test.ts > login > should validate email", error: "Expected true, got false" },
      ],
    })
  },
  { permission: "allow" }
)

const postComment = tool(
  "post_comment",
  "Post a review comment on the PR",
  {
    body: z.string().describe("Comment body in markdown"),
    file: z.string().optional().describe("File to comment on"),
    line: z.number().optional().describe("Line number"),
  },
  async (args) => {
    // In real implementation, call GitHub API
    console.log(`[post_comment] Posting comment:`)
    console.log(args.body.slice(0, 200))
    return `Posted comment${args.file ? ` on ${args.file}:${args.line}` : ""}`
  },
  { permission: "ask" }  // Requires approval before posting
)

const approvePR = tool(
  "approve_pr",
  "Approve the pull request",
  {},
  async () => {
    console.log(`[approve_pr] Approving PR`)
    return "PR approved"
  },
  { permission: "pin" }  // Requires PIN - sensitive action
)

// =============================================================================
// MCP SERVER
// =============================================================================

const mcpServer = createSwarmMcpServer({
  name: "code-review",
  tools: [runLinter, runTests, postComment, approvePR],
})

// =============================================================================
// PROFILE
// =============================================================================

const reviewProfile = createSwarmProfile({
  name: "code-reviewer",
  base: "analyze",  // Read-only base
  envFile: ".env.github",  // Load GITHUB_TOKEN
  tools: {
    bash: false,  // No shell access
  },
})

// =============================================================================
// MAIN
// =============================================================================

async function main() {
  const MCP_PORT = 19876
  
  // Start MCP HTTP server
  const httpServer = serve({
    port: MCP_PORT,
    async fetch(req) {
      const body = await req.json() as any
      
      if (body.method === "initialize") {
        return Response.json({
          jsonrpc: "2.0", id: body.id,
          result: {
            protocolVersion: "2024-11-05",
            capabilities: { tools: {} },
            serverInfo: { name: "code-review", version: "1.0.0" },
          },
        })
      }
      
      if (body.method === "tools/list") {
        return Response.json({
          jsonrpc: "2.0", id: body.id,
          result: { tools: mcpServer.listTools() },
        })
      }
      
      if (body.method === "tools/call") {
        const result = await mcpServer.executeTool(
          body.params.name,
          body.params.arguments || {},
          {}
        )
        return Response.json({ jsonrpc: "2.0", id: body.id, result })
      }
      
      return Response.json({ error: "Unknown method" })
    },
  })

  // Start opencode
  const { client, spawn, server } = await createOpencode()
  
  // Register MCP server
  await client.mcp.add({
    body: { name: "code-review", config: { type: "remote", url: `http://localhost:${MCP_PORT}` } },
  })
  
  // Load profile env
  await reviewProfile.injectEnv()
  
  // Run the review
  console.log("Starting code review...\n")
  
  const handle = spawn({
    prompt: `
      Review this pull request:
      1. Run the linter on all changed TypeScript files
      2. Run the test suite
      3. If there are issues, post a comment summarizing them
      4. If everything passes, approve the PR
      
      Changed files: src/auth.ts, src/utils.ts
    `,
    mode: "interactive",
    onPermission: async (p) => {
      console.log(`\nPermission requested: ${p.tool}`)
      console.log(`  ${p.description}`)
      
      if (p.permission === "pin") {
        // In real app, prompt for PIN
        return { pin: process.env.REVIEW_PIN || "1234" }
      }
      
      // Auto-approve comments for demo
      return "approve"
    },
  })
  
  for await (const event of handle.stream()) {
    switch (event.type) {
      case "text":
        process.stdout.write(event.delta || "")
        break
      case "tool.start":
        console.log(`\n> ${event.name}`)
        break
      case "completed":
        console.log("\n\nReview complete!")
        console.log(`Duration: ${event.result.duration_ms}ms`)
        console.log(`Tools used: ${event.result.tool_calls}`)
        break
    }
  }
  
  httpServer.stop()
  server.close()
}

main().catch(console.error)
```

---

## API Reference

### Types

```typescript
// spawn.ts
interface SpawnOptions {
  prompt: string
  agent?: string
  profile?: ProfileName | CustomProfile
  mode?: "interactive" | "noninteractive"
  onPermission?: (p: PermissionRequest) => Promise<PermissionResponse>
  tools?: Record<string, boolean>
  model?: string
}

interface SpawnHandle {
  sessionId: string
  stream(): AsyncIterable<SpawnEvent>
  wait(): Promise<SpawnResult>
  abort(): void
  respondToPermission(id: string, response: PermissionResponse): Promise<void>
}

interface SpawnResult {
  success: boolean
  error?: string
  duration_ms: number
  tool_calls: number
  files_modified: string[]
  summary: string
}

interface PermissionRequest {
  id: string
  permissionType: string
  title: string
  metadata: Record<string, unknown>
}

type PermissionResponse =
  | "approve"
  | "always"
  | "reject"
  | { reject: string }
  | { pin: string }

// tool.ts
type ToolPermission = "allow" | "ask" | "deny" | "pin"

interface ToolDefinition<T> {
  name: string
  description: string
  schema: ZodObject<T>
  permission: ToolPermission
  execute: (args: T, context: ToolContext) => Promise<ToolResult | string>
}

interface ToolContext {
  sessionID?: string
  messageID?: string
  agent?: string
  abort?: AbortSignal
  onPermission?: (request: ToolPermissionRequest) => Promise<boolean>
}

// profiles.ts
type ProfileName = "analyze" | "edit" | "full" | "yolo"

interface CustomProfileConfig {
  name: string
  description?: string
  base?: ProfileName
  envFile?: string | null
  env?: Record<string, string>
  mcpServers?: McpServers
  tools?: Record<string, boolean>
  permission?: Partial<Config["permission"]>
  agent?: string
  model?: string
}

// container/runtime.ts
interface ContainerOptions {
  runtime: "podman" | "docker"
  name: string
  image: string
  volumes: Array<{ host: string; container: string; readonly?: boolean }>
  environment: Record<string, string>
  network: { mode: "bridge" | "none" | "host"; exposePorts?: number[] }
  workdir: string
  user?: string
}
```

### Functions

```typescript
// Main entry point
createOpencode(options?: ServerOptions): Promise<{
  client: OpencodeClient
  spawn: (prompt: string | SpawnOptions) => SpawnHandle
  server: { url: string; close: () => void }
}>

// Tools
tool<T>(name, description, schema, execute, options?): ToolDefinition<T>
createSwarmMcpServer(options): SwarmMcpServer

// Profiles
createSwarmProfile(config): CustomProfile
getProfile(name: ProfileName): Profile

// Env
loadEnvFile(path, options?): Promise<Record<string, string>>
injectEnvFile(path, options?): Promise<Record<string, string>>
createEnvLoader(path): EnvLoader
loadEnvString(content): Record<string, string>
resolveEnvVars(str, env?): string

// Container (internal)
ContainerRuntime.checkRuntime(runtime): Promise<boolean>
ContainerRuntime.create(options): Promise<string>
ContainerRuntime.start(id): Promise<void>
ContainerRuntime.stop(id, timeout?): Promise<void>
ContainerRuntime.remove(id): Promise<void>
ContainerRuntime.exec(id, command): Promise<ExecResult>
ContainerRuntime.logs(id, options?): AsyncIterable<string>
```

---

## Common Mistakes

### 1. Forgetting to close the server

```typescript
// WRONG
const { spawn, server } = await createOpencode()
await spawn("...").wait()
// Process hangs!

// RIGHT
const { spawn, server } = await createOpencode()
await spawn("...").wait()
server.close()  // Always close!
```

### 2. Using interactive mode without onPermission

```typescript
// WRONG - permissions will hang forever
spawn({ prompt: "...", mode: "interactive" })

// RIGHT
spawn({
  prompt: "...",
  mode: "interactive",
  onPermission: async (p) => "approve",
})
```

### 3. Not waiting for MCP registration

```typescript
// WRONG - tools may not be available yet
await client.mcp.add({ body: { name: "x", config: {...} } })
spawn("use my tool")  // Tool not ready!

// RIGHT
await client.mcp.add({ body: { name: "x", config: {...} } })
await new Promise(r => setTimeout(r, 1000))  // Give it time
spawn("use my tool")
```

### 4. Missing tool descriptions

```typescript
// WRONG - AI won't know when to use it
tool("do_thing", "", { x: z.string() }, ...)

// RIGHT - Be detailed!
tool(
  "do_thing",
  "Does a specific thing when the user asks to process X. Use this when...",
  { x: z.string().describe("The X value to process") },
  ...
)
```

---

## Troubleshooting

### "Connection refused" on spawn

The server didn't start. Check:
- Port conflict (try different port)
- Permissions (can't bind to ports < 1024 without sudo)

### MCP tools not appearing

1. Check MCP server is running and returning correct format
2. Wait after `client.mcp.add()` before spawning
3. Check `client.mcp.status()` for errors

### Container not starting

1. Check runtime is installed: `podman --version` or `docker --version`
2. Check image exists: `podman pull ubuntu:22.04`
3. Check volume paths exist

### Permissions hanging

- In `noninteractive` mode: `pin` permissions always reject
- In `interactive` mode: You MUST provide `onPermission` callback

---

## Files Reference

```
packages/sdk/js/src/
├── index.ts        # Main exports, createOpencode()
├── spawn.ts        # spawn() function, SpawnHandle
├── tool.ts         # tool() helper
├── mcp-server.ts   # createSwarmMcpServer()
├── profiles.ts     # Built-in and custom profiles
├── env.ts          # Environment loading
├── client.ts       # HTTP client
└── server.ts       # Server management

packages/opencode/src/
├── container/
│   └── runtime.ts  # ContainerRuntime abstraction
├── profile/
│   └── index.ts    # Profile lifecycle management
└── server/
    └── profile.ts  # HTTP endpoints for profiles
```
