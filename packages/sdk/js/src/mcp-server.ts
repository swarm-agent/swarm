/**
 * In-Process MCP Server for Custom Tools
 * 
 * Create MCP servers that run in-process with the SDK, allowing you to
 * define custom tools that Claude can use without external processes.
 * 
 * @example
 * ```typescript
 * import { createSwarmMcpServer, tool } from "@opencode-ai/sdk"
 * import { z } from "zod"
 * 
 * const server = createSwarmMcpServer({
 *   name: "my-tools",
 *   version: "1.0.0",
 *   tools: [
 *     tool("get_weather", "Get weather for a city", {
 *       city: z.string().describe("City name"),
 *     }, async (args) => `Weather in ${args.city}: Sunny, 72°F`),
 *   ],
 * })
 * 
 * // Use in spawn
 * await spawn({
 *   prompt: "What's the weather in NYC?",
 *   mcpServers: { "my-tools": server },
 * })
 * ```
 */

import { type ToolDefinition, type ToolContext, type ToolResult, toolToJsonSchema } from "./tool.js"
import type { ZodRawShape } from "zod"

/**
 * Options for creating an in-process MCP server
 */
export interface SwarmMcpServerOptions {
  /** Server name (used in tool namespacing: mcp__{name}__{tool}) */
  name: string
  /** Server version */
  version?: string
  /** Custom tools to expose */
  tools: ToolDefinition<ZodRawShape>[]
}

/**
 * In-process MCP server instance
 */
export interface SwarmMcpServer {
  /** Server name */
  name: string
  /** Server version */
  version: string
  /** List available tools */
  listTools(): Array<{
    name: string
    description: string
    inputSchema: Record<string, unknown>
  }>
  /** Execute a tool by name */
  executeTool(
    toolName: string, 
    args: Record<string, unknown>,
    context: ToolContext
  ): Promise<ToolResult>
  /** Get a specific tool definition */
  getTool(name: string): ToolDefinition<ZodRawShape> | undefined
}

/**
 * Create an in-process MCP server with custom tools
 * 
 * The server runs in the same process as the SDK and provides
 * tools that Claude can call during execution.
 * 
 * Tool names follow MCP naming: mcp__{serverName}__{toolName}
 * 
 * @param options - Server configuration
 * @returns MCP server instance
 * 
 * @example
 * ```typescript
 * const apiServer = createSwarmMcpServer({
 *   name: "company-api",
 *   version: "1.0.0",
 *   tools: [
 *     tool("create_ticket", "Create a support ticket", {
 *       title: z.string(),
 *       priority: z.enum(["low", "medium", "high"]),
 *     }, async (args) => {
 *       const ticket = await api.createTicket(args)
 *       return `Created ticket #${ticket.id}: ${ticket.title}`
 *     }),
 *     tool("list_tickets", "List recent tickets", {
 *       status: z.enum(["open", "closed", "all"]).optional(),
 *     }, async (args) => {
 *       const tickets = await api.listTickets(args.status)
 *       return tickets.map(t => `#${t.id}: ${t.title}`).join("\n")
 *     }),
 *   ],
 * })
 * ```
 */
export function createSwarmMcpServer(options: SwarmMcpServerOptions): SwarmMcpServer {
  const { name, version = "1.0.0", tools } = options

  // Validate server name
  if (!/^[a-zA-Z0-9_-]+$/.test(name)) {
    throw new Error(`Invalid server name "${name}": must be alphanumeric with underscores/hyphens only`)
  }

  // Build tool map for fast lookup
  const toolMap = new Map<string, ToolDefinition<ZodRawShape>>()
  for (const tool of tools) {
    if (toolMap.has(tool.name)) {
      throw new Error(`Duplicate tool name "${tool.name}" in server "${name}"`)
    }
    toolMap.set(tool.name, tool)
  }

  return {
    name,
    version,

    listTools() {
      return tools.map(tool => toolToJsonSchema(tool))
    },

    async executeTool(toolName: string, args: Record<string, unknown>, context: ToolContext): Promise<ToolResult> {
      const tool = toolMap.get(toolName)
      if (!tool) {
        return {
          content: [{
            type: "text",
            text: `Error: Unknown tool "${toolName}" in server "${name}"`,
          }],
        }
      }

      try {
        // Validate and parse args with Zod schema
        const parsed = tool.schema.parse(args)
        
        // Execute the tool
        const result = await tool.execute(parsed, context)
        
        // Normalize result to ToolResult format
        if (typeof result === "string") {
          return {
            content: [{
              type: "text",
              text: result,
            }],
          }
        }
        return result
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error)
        return {
          content: [{
            type: "text",
            text: `Error executing tool "${toolName}": ${message}`,
          }],
        }
      }
    },

    getTool(toolName: string) {
      return toolMap.get(toolName)
    },
  }
}

/**
 * Type for MCP servers dictionary passed to spawn()
 */
export type McpServers = Record<string, SwarmMcpServer>
