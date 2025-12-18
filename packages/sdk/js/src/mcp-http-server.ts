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
  /** Port to listen on (0 = auto-assign) */
  port?: number
  /** Hostname to bind to (default: "0.0.0.0" for container access) */
  hostname?: string
  /** Tools to expose */
  tools: ToolDefinition<ZodRawShape>[]
  /** Permission handler for ask/pin tools */
  onPermission?: (request: ToolPermissionRequest) => Promise<boolean>
  /** Called when a tool is invoked (for logging) */
  onToolCall?: (name: string, args: Record<string, unknown>) => void
  /** Called when a tool completes */
  onToolResult?: (name: string, result: ToolResult) => void
}

/**
 * Running MCP HTTP server handle
 */
export interface McpHttpServer {
  /** Server URL (use this for mcp.add) */
  url: string
  /** Port number */
  port: number
  /** Hostname */
  hostname: string
  /** Server name */
  name: string
  /** Stop the server */
  stop: () => void
  /** List tool names */
  tools: string[]
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
    tools,
    onPermission,
    onToolCall,
    onToolResult,
  } = options

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

    // Check permission
    switch (tool.permission) {
      case "deny":
        return {
          content: [{ type: "text", text: `Error: Tool "${toolName}" is blocked` }],
        }

      case "ask":
      case "pin":
        if (!onPermission) {
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
  
  const server: BunServer = BunGlobal.Bun.serve({
    port,
    hostname,
    fetch: handleRequest,
  })

  const actualPort = server.port
  const url = `http://${hostname === "0.0.0.0" ? "localhost" : hostname}:${actualPort}`

  return {
    url,
    port: actualPort,
    hostname,
    name,
    stop: () => server.stop(),
    tools: tools.map(t => t.name),
  }
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
 * // For containerized agent
 * const containerUrl = getContainerMcpUrl(server, "podman")
 * await client.mcp.add({ body: { name: "tools", config: { type: "remote", url: containerUrl } } })
 * ```
 */
export function getContainerMcpUrl(server: McpHttpServer, runtime: "podman" | "docker" = "podman"): string {
  // Podman: host.containers.internal
  // Docker: host.docker.internal
  const hostAlias = runtime === "podman" ? "host.containers.internal" : "host.docker.internal"
  return `http://${hostAlias}:${server.port}`
}
