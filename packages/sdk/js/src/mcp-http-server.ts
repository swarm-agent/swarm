/**
 * Standalone MCP HTTP Server
 * 
 * Serves custom tools over HTTP using the MCP protocol.
 * Runs on the HOST - accessible from containers, other processes, anywhere.
 * 
 * @example
 * ```typescript
 * import { startMcpServer, tool, z } from "@anthropic-ai/swarm-sdk"
 * 
 * const server = await startMcpServer({
 *   name: "my-tools",
 *   port: 9999,
 *   tools: [
 *     tool("greet", "Say hello", { name: z.string() }, async (args) => `Hello ${args.name}!`),
 *   ],
 * })
 * 
 * console.log(`MCP server at ${server.url}`)
 * 
 * // Register with swarm (running anywhere - host or container)
 * await client.mcp.add({
 *   body: { name: "my-tools", config: { type: "remote", url: server.url } }
 * })
 * 
 * // When done
 * server.stop()
 * ```
 */

import type { ToolDefinition, ToolContext, ToolResult, ToolPermissionRequest } from "./tool.js"

// Bun server type - use any for compatibility when types aren't available
type BunServer = {
  port: number
  stop: () => void
}
import { toolToJsonSchema } from "./tool.js"
import type { ZodRawShape } from "zod"

/**
 * Options for starting an MCP HTTP server
 */
export interface McpHttpServerOptions {
  /** Server name (used in MCP protocol) */
  name: string
  /** Server version */
  version?: string
  /** Port to listen on (0 = auto-assign). Ignored if socket is set. */
  port?: number
  /** Hostname to bind to (default: "0.0.0.0" for container access). Ignored if socket is set. */
  hostname?: string
  /** Unix socket path. If set, server listens on socket instead of TCP port.
   * This is more secure for container communication - mount the socket into the container.
   *
   * WARNING: Unix sockets can become unresponsive if bind-mounted into containers
   * (e.g., podman/docker). Enable socketHealthCheck to auto-recover from this. */
  socket?: string
  /** Tools to expose */
  tools: ToolDefinition<ZodRawShape>[]
  /** Permission handler for ask/pin tools */
  onPermission?: (request: ToolPermissionRequest) => Promise<boolean>
  /** Called when a tool is invoked (for logging) */
  onToolCall?: (name: string, args: Record<string, unknown>) => void
  /** Called when a tool completes */
  onToolResult?: (name: string, result: ToolResult) => void
  /**
   * Enable health check for socket mode. When enabled, periodically verifies
   * the socket is still accepting connections and auto-recovers if not.
   *
   * This is STRONGLY RECOMMENDED when mounting the socket into containers,
   * as container bind-mounts can silently kill the socket listener.
   *
   * Default: true for socket mode, ignored for TCP mode
   */
  socketHealthCheck?: boolean
  /** Health check interval in milliseconds. Default: 2000 (2 seconds) */
  socketHealthCheckInterval?: number
  /** Called when socket dies and is recovered. Useful for logging/debugging. */
  onSocketRecovered?: () => void
  /** Called when socket health check fails (before recovery attempt) */
  onSocketDead?: (error: Error) => void
}

/**
 * Running MCP HTTP server handle
 */
export interface McpHttpServer {
  /** Server URL (use this for mcp.add). For socket mode, this is the socket path. */
  url: string
  /** Port number (0 if using socket) */
  port: number
  /** Hostname (empty if using socket) */
  hostname: string
  /** Unix socket path (empty if using TCP) */
  socket: string
  /** Server name */
  name: string
  /** Stop the server (also stops health check timer) */
  stop: () => void
  /** List tool names */
  tools: string[]
  /** Number of times socket has been recovered (0 if never died) */
  recoveryCount: number
}

/**
 * MCP JSON-RPC request
 */
interface McpRequest {
  jsonrpc: "2.0"
  id?: number | string
  method: string
  params?: Record<string, unknown>
}

/**
 * Start a standalone MCP HTTP server
 * 
 * The server implements the MCP protocol over HTTP and can be used with
 * any MCP client including swarm/opencode.
 * 
 * @param options - Server configuration
 * @returns Running server handle with URL and stop method
 * 
 * @example
 * ```typescript
 * // Simple server with one tool
 * const server = await startMcpServer({
 *   name: "weather",
 *   port: 9999,
 *   tools: [
 *     tool("get_weather", "Get weather", { city: z.string() }, 
 *       async (args) => `Sunny in ${args.city}`
 *     ),
 *   ],
 * })
 * 
 * // Server with permission handling
 * const server = await startMcpServer({
 *   name: "admin",
 *   port: 9999,
 *   tools: [
 *     tool("delete_user", "Delete user", { id: z.string() },
 *       async (args) => `Deleted ${args.id}`,
 *       { permission: "ask" }
 *     ),
 *   ],
 *   onPermission: async (req) => {
 *     console.log(`Approve ${req.tool}?`)
 *     return true // or prompt user
 *   },
 * })
 * ```
 */
export async function startMcpServer(options: McpHttpServerOptions): Promise<McpHttpServer> {
  const {
    name,
    version = "1.0.0",
    port = 0,
    hostname = "0.0.0.0",
    socket: socketPath,
    tools,
    onPermission,
    onToolCall,
    onToolResult,
    socketHealthCheck = true, // Default ON for socket mode
    socketHealthCheckInterval = 2000,
    onSocketRecovered,
    onSocketDead,
  } = options
  
  // Debug: log what we received
  console.log(`[MCP] startMcpServer called with onPermission: ${onPermission ? 'DEFINED' : 'UNDEFINED'}`)

  // Build tool map
  const toolMap = new Map<string, ToolDefinition<ZodRawShape>>()
  for (const tool of tools) {
    if (toolMap.has(tool.name)) {
      throw new Error(`Duplicate tool name: ${tool.name}`)
    }
    toolMap.set(tool.name, tool)
  }

  // Tool execution with permission handling
  async function executeTool(
    toolName: string,
    args: Record<string, unknown>
  ): Promise<ToolResult> {
    const tool = toolMap.get(toolName)
    if (!tool) {
      return {
        content: [{ type: "text", text: `Error: Unknown tool "${toolName}"` }],
      }
    }

    onToolCall?.(toolName, args)

    // Debug: log permission state
    console.log(`[MCP] executeTool: ${toolName}, permission: ${tool.permission}, onPermission: ${onPermission ? 'DEFINED' : 'UNDEFINED'}`)

    // Check permission
    switch (tool.permission) {
      case "deny":
        return {
          content: [{ type: "text", text: `Error: Tool "${toolName}" is blocked` }],
        }

      case "ask":
      case "pin":
        if (!onPermission) {
          console.log(`[MCP] ERROR: onPermission handler is not defined for tool ${toolName}`)
          return {
            content: [{
              type: "text",
              text: `Error: Tool "${toolName}" requires ${tool.permission === "pin" ? "PIN" : "approval"} but no handler configured`,
            }],
          }
        }
        const approved = await onPermission({
          tool: toolName,
          permission: tool.permission,
          args,
          description: tool.description,
        })
        if (!approved) {
          return {
            content: [{ type: "text", text: `Error: Tool "${toolName}" was rejected` }],
          }
        }
        break
    }

    // Execute
    try {
      const parsed = tool.schema.parse(args)
      const context: ToolContext = { onPermission }
      const result = await tool.execute(parsed, context)

      const toolResult: ToolResult = typeof result === "string"
        ? { content: [{ type: "text", text: result }] }
        : result

      onToolResult?.(toolName, toolResult)
      return toolResult
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error)
      return {
        content: [{ type: "text", text: `Error: ${message}` }],
      }
    }
  }

  // HTTP request handler
  async function handleRequest(req: Request): Promise<Response> {
    // CORS headers for cross-origin access
    const corsHeaders = {
      "Access-Control-Allow-Origin": "*",
      "Access-Control-Allow-Methods": "POST, GET, OPTIONS",
      "Access-Control-Allow-Headers": "Content-Type",
    }

    if (req.method === "OPTIONS") {
      return new Response(null, { status: 204, headers: corsHeaders })
    }

    if (req.method === "GET") {
      return new Response(JSON.stringify({
        name,
        version,
        protocol: "mcp",
        tools: tools.map(t => t.name),
      }), {
        headers: { ...corsHeaders, "Content-Type": "application/json" },
      })
    }

    if (req.method !== "POST") {
      return new Response("Method not allowed", { status: 405, headers: corsHeaders })
    }

    let body: McpRequest
    try {
      body = await req.json() as McpRequest
    } catch {
      return Response.json(
        { jsonrpc: "2.0", error: { code: -32700, message: "Parse error" } },
        { headers: corsHeaders }
      )
    }

    const { method, params, id } = body

    // MCP Protocol handlers
    switch (method) {
      case "initialize":
        return Response.json({
          jsonrpc: "2.0",
          id,
          result: {
            protocolVersion: "2024-11-05",
            capabilities: { tools: {} },
            serverInfo: { name, version },
          },
        }, { headers: corsHeaders })

      case "notifications/initialized":
        // Client acknowledgment - no response needed
        return new Response(null, { status: 204, headers: corsHeaders })

      case "tools/list":
        return Response.json({
          jsonrpc: "2.0",
          id,
          result: {
            tools: tools.map(t => toolToJsonSchema(t)),
          },
        }, { headers: corsHeaders })

      case "tools/call": {
        const toolName = (params as any)?.name as string
        const toolArgs = (params as any)?.arguments ?? {}
        const result = await executeTool(toolName, toolArgs)
        return Response.json({
          jsonrpc: "2.0",
          id,
          result,
        }, { headers: corsHeaders })
      }

      default:
        return Response.json({
          jsonrpc: "2.0",
          id,
          error: { code: -32601, message: `Method not found: ${method}` },
        }, { headers: corsHeaders })
    }
  }

  // Start HTTP server (using globalThis.Bun for type safety)
  const BunGlobal = globalThis as any
  if (!BunGlobal.Bun?.serve) {
    throw new Error("MCP HTTP server requires Bun runtime")
  }

  // Helper to remove socket file
  async function removeSocketFile() {
    if (!socketPath) return
    try {
      const fs = await import("node:fs")
      if (fs.existsSync(socketPath)) {
        fs.unlinkSync(socketPath)
      }
    } catch {
      // Ignore - file might not exist
    }
  }

  // Helper to create/recreate the server
  function createServer(): BunServer {
    console.log(`[MCP:create] Creating server, socketPath=${socketPath}, port=${port}`)
    const srv = socketPath
      ? BunGlobal.Bun.serve({
          unix: socketPath,
          fetch: handleRequest,
        })
      : BunGlobal.Bun.serve({
          port,
          hostname,
          fetch: handleRequest,
        })
    console.log(`[MCP:create] Server created, port=${srv.port}`)
    return srv
  }

  // Remove existing socket file if present (prevents EADDRINUSE)
  console.log(`[MCP:init] Removing existing socket file if present...`)
  await removeSocketFile()

  // Create initial server
  console.log(`[MCP:init] Creating initial server...`)
  let server: BunServer = createServer()
  console.log(`[MCP:init] Initial server created`)
  let recoveryCount = 0
  let healthCheckTimer: ReturnType<typeof setInterval> | null = null
  let stopped = false

  // Socket health check - tests if socket is still accepting connections
  // Uses subprocess to avoid self-connection issues within same event loop
  async function checkSocketHealth(): Promise<boolean> {
    if (!socketPath || stopped) {
      console.log(`[MCP:health] Skip check: socketPath=${socketPath}, stopped=${stopped}`)
      return true
    }

    const fs = await import("node:fs")

    // Step 1: Check if socket file exists
    const exists = fs.existsSync(socketPath)
    console.log(`[MCP:health] Socket file exists: ${exists}`)
    if (!exists) {
      console.log(`[MCP:health] FAIL - socket file missing`)
      return false
    }

    // Step 2: Check socket file stats
    try {
      const stats = fs.statSync(socketPath)
      console.log(`[MCP:health] Socket stats: isSocket=${stats.isSocket()}, mode=${stats.mode.toString(8)}`)
    } catch (e) {
      console.log(`[MCP:health] FAIL - cannot stat socket: ${e}`)
      return false
    }

    // Step 3: Use curl subprocess to test actual connectivity
    // This avoids any issues with self-connection in same event loop
    try {
      console.log(`[MCP:health] Testing with curl subprocess...`)
      const proc = Bun.spawn(["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "--unix-socket", socketPath, "http://localhost/"], {
        stdout: "pipe",
        stderr: "pipe",
      })

      const exitCode = await proc.exited
      const stdout = await new Response(proc.stdout).text()
      const stderr = await new Response(proc.stderr).text()

      console.log(`[MCP:health] curl exit=${exitCode}, stdout="${stdout}", stderr="${stderr.slice(0, 100)}"`)

      if (exitCode === 0 && stdout.startsWith("2")) {
        console.log(`[MCP:health] SUCCESS - socket responding`)
        return true
      } else {
        console.log(`[MCP:health] FAIL - curl failed or non-2xx response`)
        return false
      }
    } catch (e) {
      console.log(`[MCP:health] FAIL - curl subprocess error: ${e}`)
      return false
    }
  }

  // Recovery function
  async function recoverSocket(): Promise<void> {
    if (stopped) {
      console.log(`[MCP:recover] Skip - stopped=${stopped}`)
      return
    }

    console.log(`[MCP:recover] Starting recovery...`)

    try {
      // Stop old server with force
      console.log(`[MCP:recover] Stopping old server...`)
      try {
        server.stop(true)
        console.log(`[MCP:recover] Old server stopped`)
      } catch (e) {
        console.log(`[MCP:recover] Old server stop failed (expected if already dead): ${e}`)
      }

      // Small delay to let OS release socket
      console.log(`[MCP:recover] Waiting 100ms for OS to release socket...`)
      await new Promise(r => setTimeout(r, 100))

      // Remove stale socket file
      console.log(`[MCP:recover] Removing stale socket file...`)
      await removeSocketFile()

      // Double-check and force remove if needed
      const fs = await import("node:fs")
      if (socketPath && fs.existsSync(socketPath)) {
        console.log(`[MCP:recover] Socket still exists, force removing...`)
        try { fs.unlinkSync(socketPath) } catch (e) {
          console.log(`[MCP:recover] Force remove failed: ${e}`)
        }
      }

      // Create new server
      console.log(`[MCP:recover] Creating new server...`)
      server = createServer()
      console.log(`[MCP:recover] New server created`)

      // Small delay to let server bind
      console.log(`[MCP:recover] Waiting 50ms for server to bind...`)
      await new Promise(r => setTimeout(r, 50))

      // Verify socket file exists
      if (socketPath) {
        const exists = fs.existsSync(socketPath)
        console.log(`[MCP:recover] Socket file exists after create: ${exists}`)
      }

      // Verify by attempting connection
      console.log(`[MCP:recover] Verifying with health check...`)
      const testResult = await checkSocketHealth()
      console.log(`[MCP:recover] Health check result: ${testResult}`)

      if (testResult) {
        recoveryCount++
        console.log(`[MCP:recover] SUCCESS - recovery count: ${recoveryCount}`)
        onSocketRecovered?.()
      } else {
        console.log(`[MCP:recover] FAIL - socket not responding after recovery`)
      }
    } catch (error) {
      console.error(`[MCP:recover] EXCEPTION:`, error)
    }
  }

  // Start health check for socket mode
  if (socketPath && socketHealthCheck) {
    console.log(`[MCP:init] Starting health check timer, interval=${socketHealthCheckInterval}ms`)
    healthCheckTimer = setInterval(async () => {
      if (stopped) {
        console.log(`[MCP:timer] Skip - stopped`)
        return
      }

      console.log(`[MCP:timer] Running health check...`)
      const healthy = await checkSocketHealth()
      console.log(`[MCP:timer] Health check result: ${healthy}`)

      if (!healthy && !stopped) {
        console.log(`[MCP:timer] Socket DEAD - triggering recovery`)
        const error = new Error(`Socket ${socketPath} stopped accepting connections`)
        onSocketDead?.(error)
        await recoverSocket()
        console.log(`[MCP:timer] Recovery complete, count: ${recoveryCount}`)
      } else if (healthy) {
        console.log(`[MCP:timer] Socket healthy, no action needed`)
      }
    }, socketHealthCheckInterval)

    // Don't keep process alive just for health check
    if (healthCheckTimer.unref) {
      healthCheckTimer.unref()
    }
  }

  const actualPort = socketPath ? 0 : server.port
  const url = socketPath
    ? `unix://${socketPath}`
    : `http://${hostname === "0.0.0.0" ? "localhost" : hostname}:${actualPort}`

  // Return handle with recovery count getter
  const handle: McpHttpServer = {
    url,
    port: actualPort,
    hostname: socketPath ? "" : hostname,
    socket: socketPath ?? "",
    name,
    stop: () => {
      stopped = true
      if (healthCheckTimer) {
        clearInterval(healthCheckTimer)
        healthCheckTimer = null
      }
      server.stop()
      // Clean up socket file synchronously
      if (socketPath) {
        try {
          const fs = require("node:fs")
          if (fs.existsSync(socketPath)) {
            fs.unlinkSync(socketPath)
          }
        } catch {
          // Ignore
        }
      }
    },
    tools: tools.map(t => t.name),
    get recoveryCount() {
      return recoveryCount
    },
  }

  return handle
}

/**
 * Get the URL to use for registering MCP server with a containerized agent
 *
 * Containers can't reach "localhost" on the host. Use this to get the right URL.
 *
 * @param server - Running MCP server
 * @param runtime - Container runtime (podman or docker)
 * @returns URL accessible from inside container
 *
 * @example
 * ```typescript
 * const server = await startMcpServer({ name: "tools", port: 9999, tools: [...] })
 *
 * // For non-containerized agent
 * await client.mcp.add({ body: { name: "tools", config: { type: "remote", url: server.url } } })
 *
 * // For containerized agent (bridge network)
 * const containerUrl = getContainerMcpUrl(server, "podman")
 * await client.mcp.add({ body: { name: "tools", config: { type: "remote", url: containerUrl } } })
 * ```
 */
export function getContainerMcpUrl(server: McpHttpServer, runtime: "podman" | "docker" = "podman"): string {
  if (server.socket) {
    // Socket mode - container should mount the socket and use it directly
    // This requires custom transport support in MCP client
    throw new Error("Socket-based MCP requires mounting the socket into the container")
  }
  // Podman: host.containers.internal
  // Docker: host.docker.internal
  const hostAlias = runtime === "podman" ? "host.containers.internal" : "host.docker.internal"
  return `http://${hostAlias}:${server.port}`
}

/**
 * Container network security modes
 */
export type ContainerSecurityMode = "host" | "bridge" | "isolated"

/**
 * Get recommended container options for a security mode
 *
 * @param mode - Security mode
 * @returns Partial container options
 *
 * Modes:
 * - "host": Full network access (dev only)
 * - "bridge": Isolated network, can reach host via host.containers.internal
 * - "isolated": No network except mounted unix socket (most secure, requires socket MCP)
 */
export function getSecurityModeOptions(mode: ContainerSecurityMode): {
  network: "host" | "bridge" | "none"
  mcpNote: string
} {
  switch (mode) {
    case "host":
      return {
        network: "host",
        mcpNote: "MCP accessible via localhost - least secure, use for dev only",
      }
    case "bridge":
      return {
        network: "bridge",
        mcpNote: "MCP accessible via host.containers.internal - container is isolated from localhost",
      }
    case "isolated":
      return {
        network: "none",
        mcpNote: "MCP must use unix socket mounted into container - most secure",
      }
  }
}
