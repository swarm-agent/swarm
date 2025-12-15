/**
 * Tool Profiles - Predefined permission sets for common use cases
 *
 * Profiles map to Config.permission + tools settings.
 * Use these for quick setup, or provide your own config for full control.
 * 
 * Custom profiles can include:
 * - Custom MCP tools (via createSwarmMcpServer)
 * - Per-profile env files for secrets
 * 
 * @example
 * ```typescript
 * import { createSwarmProfile, tool, createSwarmMcpServer } from "@opencode-ai/sdk"
 * 
 * const myProfile = createSwarmProfile({
 *   name: "backend-dev",
 *   base: "full",
 *   envFile: ".env.backend",
 *   mcpServers: {
 *     "company-api": createSwarmMcpServer({
 *       name: "company-api",
 *       tools: [
 *         tool("create_ticket", "Create a JIRA ticket", { ... }, async (args) => { ... }),
 *       ],
 *     }),
 *   },
 * })
 * ```
 */

import type { Config } from "./gen/types.gen.js"
import type { SwarmMcpServer, McpServers } from "./mcp-server.js"
import { createEnvLoader } from "./env.js"

export type ProfileName = "analyze" | "edit" | "full" | "yolo"

export interface Profile {
  name: ProfileName
  description: string
  /** Tools to enable/disable */
  tools: Record<string, boolean>
  /** Permission settings */
  permission: NonNullable<Config["permission"]>
}

/**
 * Custom profile configuration
 */
export interface CustomProfileConfig {
  /** Profile name (for identification) */
  name: string
  /** Description of what this profile is for */
  description?: string
  /** Base permission profile to extend */
  base?: ProfileName
  /** 
   * Path to .env file for this profile's secrets
   * Can be absolute or relative to cwd
   * Set to null/undefined to skip env loading
   */
  envFile?: string | null
  /**
   * Environment variables to set (merged with envFile)
   * Useful for non-secret config or overrides
   */
  env?: Record<string, string>
  /** In-process MCP servers with custom tools */
  mcpServers?: McpServers
  /** Additional tool enable/disable overrides */
  tools?: Record<string, boolean>
  /** Permission overrides */
  permission?: Partial<NonNullable<Config["permission"]>>
  /** Default agent to use */
  agent?: string
  /** Default model to use */
  model?: string
}

/**
 * Custom profile instance with tools and env
 */
export interface CustomProfile {
  /** Profile name */
  name: string
  /** Description */
  description: string
  /** Resolved tools config */
  tools: Record<string, boolean>
  /** Resolved permissions */
  permission: NonNullable<Config["permission"]>
  /** MCP servers */
  mcpServers: McpServers
  /** Environment variables (loaded from file + explicit) */
  env: Record<string, string>
  /** Whether env has been loaded */
  envLoaded: boolean
  /** Load env file (if configured) */
  loadEnv(): Promise<Record<string, string>>
  /** Inject env into process.env */
  injectEnv(): Promise<void>
  /** Default agent */
  agent?: string
  /** Default model */
  model?: string
}

/**
 * Read-only analysis - no modifications allowed
 * Use for: code review, understanding, documentation
 */
const analyze: Profile = {
  name: "analyze",
  description: "Read-only analysis - no modifications allowed",
  tools: {
    read: true,
    glob: true,
    grep: true,
    list: true,
    webfetch: true,
    // Disable modification tools
    edit: false,
    write: false,
    bash: false,
    patch: false,
    "background-agent": false,
  },
  permission: {
    edit: "deny",
    bash: { "*": "deny" },
    webfetch: "allow",
  },
}

/**
 * Safe editing - file modifications allowed, no shell access
 * Use for: refactoring, fixing bugs, adding features
 */
const edit: Profile = {
  name: "edit",
  description: "Safe editing - file modifications, no shell",
  tools: {
    read: true,
    glob: true,
    grep: true,
    list: true,
    edit: true,
    write: true,
    patch: true,
    webfetch: true,
    // No shell or background agents
    bash: false,
    "background-agent": false,
  },
  permission: {
    edit: "allow",
    bash: { "*": "deny" },
    webfetch: "allow",
  },
}

/**
 * Full access with oversight - all tools, bash asks for permission
 * Use for: complex tasks needing shell, with human approval
 */
const full: Profile = {
  name: "full",
  description: "Full access - all tools, bash requires approval",
  tools: {
    // All tools enabled
    read: true,
    glob: true,
    grep: true,
    list: true,
    edit: true,
    write: true,
    patch: true,
    bash: true,
    webfetch: true,
    task: true,
    "background-agent": true,
  },
  permission: {
    edit: "allow",
    bash: { "*": "ask" }, // Requires approval
    webfetch: "allow",
  },
}

/**
 * YOLO mode - all tools, no restrictions
 * WARNING: Only use in CI/automation with trusted prompts!
 */
const yolo: Profile = {
  name: "yolo",
  description: "DANGER: All tools, no restrictions - for CI/automation only",
  tools: {
    // All tools enabled
    read: true,
    glob: true,
    grep: true,
    list: true,
    edit: true,
    write: true,
    patch: true,
    bash: true,
    webfetch: true,
    task: true,
    "background-agent": true,
  },
  permission: {
    edit: "allow",
    bash: { "*": "allow" }, // No approval needed!
    webfetch: "allow",
  },
}

export const PROFILES: Record<ProfileName, Profile> = {
  analyze,
  edit,
  full,
  yolo,
}

/**
 * Get a profile by name
 */
export function getProfile(name: ProfileName): Profile {
  const profile = PROFILES[name]
  if (!profile) {
    throw new Error(`Unknown profile: ${name}. Available: ${Object.keys(PROFILES).join(", ")}`)
  }
  return profile
}

/**
 * Convert a profile to Config format for passing to server
 */
export function profileToConfig(profile: Profile): Partial<Config> {
  return {
    permission: profile.permission,
    tools: profile.tools,
  }
}

/**
 * Merge profile config with user overrides
 * User config takes precedence
 */
export function mergeConfigs(
  base: Partial<Config>,
  override: Partial<Config> | undefined,
): Partial<Config> {
  if (!override) return base

  return {
    ...base,
    ...override,
    permission: {
      ...base.permission,
      ...override.permission,
      bash:
        typeof override.permission?.bash === "object" && typeof base.permission?.bash === "object"
          ? { ...base.permission.bash, ...override.permission.bash }
          : override.permission?.bash ?? base.permission?.bash,
    },
    tools: {
      ...base.tools,
      ...override.tools,
    },
  }
}

/**
 * Create a custom profile with tools and environment
 * 
 * @param config - Profile configuration
 * @returns Custom profile instance
 * 
 * @example
 * ```typescript
 * import { createSwarmProfile, tool, createSwarmMcpServer } from "@opencode-ai/sdk"
 * import { z } from "zod"
 * 
 * // Simple profile with just env file
 * const simpleProfile = createSwarmProfile({
 *   name: "api-dev",
 *   base: "full",
 *   envFile: ".env.development",
 * })
 * 
 * // Profile with custom tools
 * const advancedProfile = createSwarmProfile({
 *   name: "backend-dev",
 *   base: "full",
 *   envFile: ".env.backend",
 *   mcpServers: {
 *     "internal-api": createSwarmMcpServer({
 *       name: "internal-api",
 *       tools: [
 *         tool("search_docs", "Search internal docs", {
 *           query: z.string(),
 *         }, async (args) => {
 *           const response = await fetch(`${process.env.DOCS_API}/search?q=${args.query}`)
 *           return await response.text()
 *         }),
 *       ],
 *     }),
 *   },
 * })
 * 
 * // Use in spawn
 * await spawn({
 *   prompt: "Search our docs for authentication",
 *   profile: advancedProfile,
 * })
 * ```
 */
export function createSwarmProfile(config: CustomProfileConfig): CustomProfile {
  // Get base profile
  const base = config.base ? getProfile(config.base) : full

  // Merge tools
  const tools: Record<string, boolean> = {
    ...base.tools,
    ...(config.tools ?? {}),
  }

  // Merge permissions
  const permission: NonNullable<Config["permission"]> = {
    ...base.permission,
    ...(config.permission ?? {}),
    bash: mergePermissionBash(base.permission.bash, config.permission?.bash),
  }

  // Create env loader if envFile specified
  const envLoader = config.envFile ? createEnvLoader(config.envFile) : null
  let loadedEnv: Record<string, string> = {}
  let envLoaded = false

  return {
    name: config.name,
    description: config.description ?? `Custom profile: ${config.name}`,
    tools,
    permission,
    mcpServers: config.mcpServers ?? {},
    env: config.env ?? {},
    envLoaded,
    agent: config.agent,
    model: config.model,

    async loadEnv(): Promise<Record<string, string>> {
      if (envLoaded) return loadedEnv

      // Load from file if configured
      if (envLoader && envLoader.exists()) {
        loadedEnv = await envLoader.load({ throwOnMissing: false })
      }

      // Merge with explicit env (explicit wins)
      loadedEnv = { ...loadedEnv, ...(config.env ?? {}) }
      envLoaded = true

      return loadedEnv
    },

    async injectEnv(): Promise<void> {
      const env = await this.loadEnv()
      for (const [key, value] of Object.entries(env)) {
        if (!(key in process.env)) {
          process.env[key] = value
        }
      }
    },
  }
}

/**
 * Helper to merge bash permission settings
 */
function mergePermissionBash(
  base: NonNullable<Config["permission"]>["bash"],
  override: NonNullable<Config["permission"]>["bash"] | undefined
): NonNullable<Config["permission"]>["bash"] {
  if (!override) return base
  if (typeof override === "string") return override
  if (typeof base === "string") return override
  return { ...base, ...override }
}

/**
 * Check if a profile is a CustomProfile (vs ProfileName string)
 */
export function isCustomProfile(profile: ProfileName | CustomProfile): profile is CustomProfile {
  return typeof profile === "object" && "mcpServers" in profile
}
