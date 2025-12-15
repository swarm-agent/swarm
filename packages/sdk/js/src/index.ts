export * from "./client.js"
export * from "./server.js"
export * from "./spawn.js"
export * from "./profiles.js"
export * from "./tool.js"
export * from "./mcp-server.js"
export * from "./env.js"

import { createOpencodeClient } from "./client.js"
import { createOpencodeServer } from "./server.js"
import type { ServerOptions } from "./server.js"
import { createSpawn, type SpawnHandle, type SpawnOptions, type SpawnResult, type SpawnEvent, type PermissionRequest, type PermissionResponse } from "./spawn.js"
import type { ProfileName, Profile, CustomProfile, CustomProfileConfig } from "./profiles.js"
import { createSwarmProfile } from "./profiles.js"
import { tool, type ToolDefinition, type ToolContext, type ToolResult } from "./tool.js"
import { createSwarmMcpServer, type SwarmMcpServer, type SwarmMcpServerOptions, type McpServers } from "./mcp-server.js"
import { loadEnvFile, loadEnvString, injectEnvFile, createEnvLoader, resolveEnvVars } from "./env.js"

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
  SwarmMcpServer,
  SwarmMcpServerOptions,
  McpServers,
}

export {
  // Profiles
  createSwarmProfile,
  
  // Tools
  tool,
  createSwarmMcpServer,
  
  // Env loading
  loadEnvFile,
  loadEnvString,
  injectEnvFile,
  createEnvLoader,
  resolveEnvVars,
}

export async function createOpencode(options?: ServerOptions) {
  const server = await createOpencodeServer({
    ...options,
  })

  const client = createOpencodeClient({
    baseUrl: server.url,
  })

  const spawn = createSpawn(client)

  return {
    client,
    server,
    spawn,
  }
}
