export * from "./client.js"
export * from "./server.js"
export * from "./spawn.js"
export * from "./profiles.js"
export * from "./tool.js"
export * from "./mcp-server.js"
export * from "./mcp-http-server.js"
export * from "./env.js"
export * from "./container.js"

// Re-export Zod so users get the same instance used internally
// This prevents "keyValidator._parse is not a function" errors
// when schemas are created with a different Zod version
export { z } from "zod"
export type { ZodRawShape, ZodObject, ZodType } from "zod"

import { execSync } from "node:child_process"
import { createOpencodeClient } from "./client.js"
import { createOpencodeServer } from "./server.js"
import type { ServerOptions } from "./server.js"
import { createSpawn, type SpawnHandle, type SpawnOptions, type SpawnResult, type SpawnEvent, type PermissionRequest, type PermissionResponse } from "./spawn.js"
import type { ProfileName, Profile, CustomProfile, CustomProfileConfig } from "./profiles.js"
import { createSwarmProfile } from "./profiles.js"
import { tool, type ToolDefinition, type ToolContext, type ToolResult, type ToolPermission, type ToolPermissionRequest, type ToolOptions } from "./tool.js"
import { createSwarmMcpServer, type SwarmMcpServer, type SwarmMcpServerOptions, type McpServers } from "./mcp-server.js"
import { startMcpServer, getContainerMcpUrl, getSecurityModeOptions, type McpHttpServer, type McpHttpServerOptions, type ContainerSecurityMode } from "./mcp-http-server.js"
import { loadEnvFile, loadEnvString, injectEnvFile, createEnvLoader, resolveEnvVars } from "./env.js"
import type { ZodRawShape } from "zod"

export type { 
  SpawnHandle, 
  SpawnOptions, 
  SpawnResult, 
  SpawnEvent, 
  PermissionRequest, 
  PermissionResponse, 
  ProfileName, 
  Profile,
  CustomProfile,
  CustomProfileConfig,
  ToolDefinition,
  ToolContext,
  ToolResult,
  ToolPermission,
  ToolPermissionRequest,
  ToolOptions,
  SwarmMcpServer,
  SwarmMcpServerOptions,
  McpServers,
  McpHttpServer,
  McpHttpServerOptions,
  ContainerSecurityMode,
}

export {
  // Profiles
  createSwarmProfile,
  
  // Tools
  tool,
  createSwarmMcpServer,
  
  // MCP HTTP Server (for advanced users who want manual control)
  startMcpServer,
  getContainerMcpUrl,
  getSecurityModeOptions,
  
  // Env loading
  loadEnvFile,
  loadEnvString,
  injectEnvFile,
  createEnvLoader,
  resolveEnvVars,
}

/**
 * Detect container runtime (podman or docker)
 */
function detectRuntime(): "podman" | "docker" {
  try {
    execSync("podman --version", { stdio: "ignore" })
    return "podman"
  } catch {
    return "docker" // fallback
  }
}

/**
 * Prompt block injected into system prompt for all agents
 */
export interface PromptBlock {
  /** The prompt content to inject */
  content: string
  /**
   * Agent types to apply this block to.
   * If not specified, applies to all agent types.
   */
  agents?: Array<"primary" | "subagent" | "background">
}

/**
 * Options for createOpencode()
 */
export interface CreateOpencodeOptions extends ServerOptions {
  /** System prompt applied to all sessions */
  system?: string
  /** 
   * Custom tools to expose via MCP
   * 
   * When provided, the SDK automatically:
   * 1. Starts an MCP HTTP server with these tools
   * 2. Registers it with swarm using the correct URL (container-aware)
   * 3. Cleans up on server.close()
   * 
   * @example
   * ```typescript
   * const { spawn } = await createOpencode({
   *   tools: [
   *     tool("greet", "Say hello", { name: z.string() }, 
   *       async (args) => `Hello ${args.name}!`
   *     ),
   *   ],
   * })
   * ```
   */
  tools?: ToolDefinition<ZodRawShape>[]
  /** Name for the MCP server (default: "sdk-tools") */
  mcpServerName?: string
  /** Container runtime for URL detection (auto-detected if not specified) */
  containerRuntime?: "podman" | "docker"
  /**
   * Prompt blocks injected into the system prompt for ALL agents.
   * Set once at startup, automatically applies to every agent (primary, subagent, background).
   * 
   * @example
   * ```typescript
   * const { spawn } = await createOpencode({
   *   promptBlocks: [
   *     { content: "Always respond concisely for speech output." },
   *     { content: "Use code-review style for subagents.", agents: ["subagent"] },
   *   ],
   * })
   * ```
   */
  promptBlocks?: PromptBlock[]
}

/**
 * Create an opencode instance with automatic tool registration
 * 
 * @example
 * ```typescript
 * // Simple usage
 * const { spawn } = await createOpencode()
 * await spawn("Hello").wait()
 * 
 * // With custom tools (auto-registered)
 * const { spawn, server } = await createOpencode({
 *   tools: [
 *     tool("search", "Search docs", { query: z.string() },
 *       async (args) => `Results for: ${args.query}`
 *     ),
 *   ],
 * })
 * 
 * await spawn("Search for 'typescript'").wait()
 * server.close()
 * 
 * // With container profile (tools accessible from inside container)
 * const { spawn, server } = await createOpencode({
 *   profile: "my-container",
 *   tools: [myTool],
 * })
 * ```
 */
export async function createOpencode(options?: CreateOpencodeOptions) {
  let mcpServer: McpHttpServer | undefined
  
  // 1. Start MCP HTTP server if tools provided
  if (options?.tools?.length) {
    try {
      mcpServer = await startMcpServer({
        name: options.mcpServerName ?? "sdk-tools",
        port: 0, // auto-assign
        hostname: "0.0.0.0", // accessible from containers
        tools: options.tools,
      })
    } catch (err) {
      throw new Error(`Failed to start MCP server for custom tools: ${err instanceof Error ? err.message : err}`)
    }
  }
  
  // 2. Start swarm server
  let swarmServer: { url: string; close: () => void }
  try {
    swarmServer = await createOpencodeServer(options)
  } catch (err) {
    // Clean up MCP server if swarm fails to start
    mcpServer?.stop()
    throw err
  }

  const client = createOpencodeClient({
    baseUrl: swarmServer.url,
  })

  // 3. Set prompt blocks via config (if provided)
  if (options?.promptBlocks?.length) {
    try {
      await client.config.update({
        body: {
          promptBlocks: options.promptBlocks,
        },
      })
    } catch (err) {
      // Clean up servers if config update fails
      mcpServer?.stop()
      swarmServer.close()
      throw new Error(`Failed to set prompt blocks: ${err instanceof Error ? err.message : err}`)
    }
  }

  // 4. Register MCP server with swarm (using correct URL for containers)
  if (mcpServer) {
    try {
      // Determine the correct URL based on whether we're using a container profile
      const runtime = options?.containerRuntime ?? detectRuntime()
      const mcpUrl = options?.profile 
        ? getContainerMcpUrl(mcpServer, runtime)
        : mcpServer.url
      
      await client.mcp.add({
        body: {
          name: mcpServer.name,
          config: { type: "remote", url: mcpUrl },
        },
      })
    } catch (err) {
      // Clean up both servers if registration fails
      mcpServer.stop()
      swarmServer.close()
      throw new Error(`Failed to register MCP server with swarm: ${err instanceof Error ? err.message : err}`)
    }
  }

  // 5. Create spawn function
  const spawn = createSpawn(client, { system: options?.system })

  // 6. Return with wrapped close() that cleans up both servers
  return {
    client,
    spawn,
    server: {
      /** Swarm server URL */
      url: swarmServer.url,
      /** MCP server URL (if tools were provided) */
      mcpUrl: mcpServer?.url,
      /** MCP server port (if tools were provided) */
      mcpPort: mcpServer?.port,
      /** Stop all servers */
      close() {
        try {
          mcpServer?.stop()
        } catch {
          // Ignore MCP cleanup errors
        }
        swarmServer.close()
      },
    },
  }
}
