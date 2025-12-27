/**
 * Custom Tool Definition API
 * 
 * Create type-safe custom tools for use with the Swarm SDK.
 * Similar to Claude Agent SDK's tool() helper.
 * 
 * @example
 * ```typescript
 * import { tool } from "@swarm-ai/sdk"
 * import { z } from "zod"
 * 
 * const weatherTool = tool(
 *   "get_weather",
 *   "Get current weather for a location",
 *   {
 *     city: z.string().describe("City name"),
 *     units: z.enum(["celsius", "fahrenheit"]).optional().default("celsius"),
 *   },
 *   async (args) => {
 *     const response = await fetch(`https://api.weather.com?city=${args.city}`)
 *     const data = await response.json()
 *     return `Temperature in ${args.city}: ${data.temp}°${args.units === "celsius" ? "C" : "F"}`
 *   }
 * )
 * ```
 */

import { z, type ZodRawShape, type ZodObject } from "zod"

/**
 * Permission levels for tools (same as opencode's bash permissions)
 */
export type ToolPermission = "allow" | "ask" | "deny" | "pin"

/**
 * Permission request passed to onPermission callback
 */
export interface ToolPermissionRequest {
  /** Tool name */
  tool: string
  /** Permission level required */
  permission: "ask" | "pin"
  /** Arguments being passed to the tool */
  args: Record<string, unknown>
  /** Tool description */
  description: string
}

/**
 * Context provided to tool execute function
 */
export interface ToolContext {
  /** Current session ID */
  sessionID?: string
  /** Current message ID */
  messageID?: string
  /** Agent name */
  agent?: string
  /** Abort signal for cancellation */
  abort?: AbortSignal
  /** Permission callback for ask/pin tools */
  onPermission?: (request: ToolPermissionRequest) => Promise<boolean>
}

/**
 * Result returned from tool execution
 */
export interface ToolResult {
  content: Array<{
    type: "text"
    text: string
  }>
}

/**
 * Options for tool definition
 */
export interface ToolOptions {
  /**
   * Permission level for this tool
   * - "allow" (default): Execute immediately without asking
   * - "ask": Prompt user for approval before executing
   * - "deny": Block execution, return error
   * - "pin": Require PIN verification before executing
   */
  permission?: ToolPermission
}

/**
 * Tool definition with typed parameters
 */
export interface ToolDefinition<T extends ZodRawShape = ZodRawShape> {
  /** Tool name (used in mcp__{server}__{name} format) */
  name: string
  /** Description shown to the agent */
  description: string
  /** Zod schema for parameters */
  schema: ZodObject<T>
  /** Raw shape for accessing parameter definitions */
  shape: T
  /** Permission level */
  permission: ToolPermission
  /** Execute function */
  execute: (args: z.infer<ZodObject<T>>, context: ToolContext) => Promise<ToolResult | string>
}

/**
 * Create a custom tool definition
 * 
 * @param name - Unique tool name (alphanumeric, underscores, hyphens)
 * @param description - Description for the agent (be detailed!)
 * @param parameters - Zod schema object for parameters
 * @param execute - Function to execute when tool is called
 * @param options - Optional settings including permission level
 * 
 * @example
 * ```typescript
 * // Basic tool (permission defaults to "allow")
 * const searchDocs = tool(
 *   "search_docs",
 *   "Search internal documentation.",
 *   { query: z.string() },
 *   async (args) => `Results for: ${args.query}`
 * )
 * 
 * // Tool that requires approval
 * const deleteRecord = tool(
 *   "delete_record",
 *   "Delete a database record.",
 *   { id: z.string() },
 *   async (args) => `Deleted: ${args.id}`,
 *   { permission: "ask" }
 * )
 * 
 * // Tool that requires PIN
 * const transferFunds = tool(
 *   "transfer_funds",
 *   "Transfer money between accounts.",
 *   { amount: z.number(), to: z.string() },
 *   async (args) => `Transferred $${args.amount}`,
 *   { permission: "pin" }
 * )
 * 
 * // Blocked tool
 * const dangerous = tool(
 *   "dangerous_action",
 *   "This tool is disabled.",
 *   {},
 *   async () => "never runs",
 *   { permission: "deny" }
 * )
 * ```
 */
export function tool<T extends ZodRawShape>(
  name: string,
  description: string,
  parameters: T,
  execute: (args: z.infer<ZodObject<T>>, context: ToolContext) => Promise<ToolResult | string>,
  options?: ToolOptions
): ToolDefinition<T> {
  // Validate name format
  if (!/^[a-zA-Z0-9_-]+$/.test(name)) {
    throw new Error(`Invalid tool name "${name}": must be alphanumeric with underscores/hyphens only`)
  }

  return {
    name,
    description,
    schema: z.object(parameters),
    shape: parameters,
    permission: options?.permission ?? "allow",
    execute,
  }
}

/**
 * Helper schemas for common parameter types
 * Use with tool() for type-safe parameter definitions
 */
tool.schema = {
  /** String parameter */
  string: () => z.string(),
  /** Number parameter */
  number: () => z.number(),
  /** Boolean parameter */
  boolean: () => z.boolean(),
  /** Enum parameter */
  enum: <T extends readonly [string, ...string[]]>(values: T) => z.enum(values),
  /** Array parameter */
  array: <T extends z.ZodType>(itemSchema: T) => z.array(itemSchema),
  /** Object parameter */
  object: <T extends ZodRawShape>(shape: T) => z.object(shape),
  /** Record/dictionary parameter */
  record: <V extends z.ZodType>(valueSchema: V) => z.record(z.string(), valueSchema),
  /** Optional wrapper */
  optional: <T extends z.ZodType>(schema: T) => schema.optional(),
  /** Any type (use sparingly) - uses unknown for JSON schema compatibility */
  any: () => z.unknown(),
}

/**
 * Convert ToolDefinition to JSON Schema format (for MCP)
 */
export function toolToJsonSchema<T extends ZodRawShape>(def: ToolDefinition<T>): {
  name: string
  description: string
  inputSchema: Record<string, unknown>
} {
  // Convert Zod schema to JSON Schema
  const zodToJsonSchema = (schema: z.ZodType): Record<string, unknown> => {
    if (schema instanceof z.ZodString) {
      return { type: "string", description: schema.description }
    }
    if (schema instanceof z.ZodNumber) {
      return { type: "number", description: schema.description }
    }
    if (schema instanceof z.ZodBoolean) {
      return { type: "boolean", description: schema.description }
    }
    if (schema instanceof z.ZodEnum) {
      return { type: "string", enum: schema._def.values, description: schema.description }
    }
    if (schema instanceof z.ZodArray) {
      return { type: "array", items: zodToJsonSchema(schema._def.type), description: schema.description }
    }
    if (schema instanceof z.ZodOptional) {
      return zodToJsonSchema(schema._def.innerType)
    }
    if (schema instanceof z.ZodDefault) {
      const inner = zodToJsonSchema(schema._def.innerType)
      return { ...inner, default: schema._def.defaultValue() }
    }
    if (schema instanceof z.ZodObject) {
      const shape = schema._def.shape()
      const properties: Record<string, unknown> = {}
      const required: string[] = []
      
      for (const [key, value] of Object.entries(shape)) {
        properties[key] = zodToJsonSchema(value as z.ZodType)
        // Check if required (not optional, not default)
        if (!(value instanceof z.ZodOptional) && !(value instanceof z.ZodDefault)) {
          required.push(key)
        }
      }
      
      return { 
        type: "object", 
        properties, 
        required: required.length > 0 ? required : undefined,
        description: schema.description,
      }
    }
    // Fallback
    return { type: "string" }
  }

  return {
    name: def.name,
    description: def.description,
    inputSchema: zodToJsonSchema(def.schema),
  }
}
