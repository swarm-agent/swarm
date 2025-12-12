/**
 * Tool Profiles - Predefined permission sets for common use cases
 *
 * Profiles map to Config.permission + tools settings.
 * Use these for quick setup, or provide your own config for full control.
 */

import type { Config } from "./gen/types.gen.js"

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
