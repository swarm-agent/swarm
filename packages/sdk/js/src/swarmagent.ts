/**
 * SwarmAgent Tools for the SDK
 *
 * These tools allow programmatic interaction with the SwarmAgent web dashboard
 * for human-in-the-loop approval workflows.
 *
 * The SDK automatically inherits auth from CLI (auth.json) or environment variables.
 *
 * @example
 * ```typescript
 * import { createOpencode, swarmAgentTools } from "@swarm-ai/sdk"
 *
 * const { spawn } = await createOpencode({
 *   tools: swarmAgentTools,
 * })
 *
 * await spawn("Create an approval task for deploying to production").wait()
 * ```
 */

import { tool } from "./tool.js"
import { z } from "zod"
import { readFile } from "node:fs/promises"
import { homedir } from "node:os"
import { join } from "node:path"

/**
 * SwarmAgent API base URL
 */
export const SWARM_API_BASE = "https://swarmagent.dev/api"

/**
 * Get SwarmAgent API key from CLI auth.json or environment variable
 *
 * Priority:
 * 1. SWARM_AGENT_API_KEY environment variable
 * 2. ~/.local/share/swarm/auth.json (from `swarm auth login`)
 */
export async function getSwarmApiKey(): Promise<string | undefined> {
  // Check environment variable first
  if (process.env.SWARM_AGENT_API_KEY) {
    return process.env.SWARM_AGENT_API_KEY
  }

  // Check CLI auth.json (inherits from `swarm auth login`)
  try {
    const authPath = join(homedir(), ".local/share/swarm/auth.json")
    const authData = JSON.parse(await readFile(authPath, "utf8"))
    if (authData.swarmagent?.type === "api" && authData.swarmagent?.key) {
      return authData.swarmagent.key
    }
  } catch {
    // File doesn't exist or invalid JSON - that's OK
  }

  return undefined
}

/**
 * Check if SwarmAgent is configured (API key available)
 */
export async function isSwarmConfigured(): Promise<boolean> {
  return !!(await getSwarmApiKey())
}

/**
 * Validate SwarmAgent API key by making a test request
 */
export async function validateSwarmApiKey(apiKey: string): Promise<boolean> {
  try {
    const response = await fetch(`${SWARM_API_BASE}/theme`, {
      method: "GET",
      headers: { "X-API-Key": apiKey },
      signal: AbortSignal.timeout(10000),
    })
    return response.ok
  } catch {
    return false
  }
}

/**
 * Create an agentic task for human approval
 *
 * Use this when you need human confirmation before performing sensitive actions.
 */
export const createTaskTool = tool(
  "swarmagent_create_task",
  `Create an agentic task in SwarmAgent dashboard for human approval.

Use this when you need human confirmation before performing sensitive actions
like deployments, sending emails, posting to social media, or executing
destructive operations.

Returns a poll_token to check the task status.

Parameters:
- workspace: Workspace name (auto-created if doesn't exist)
- summary: Brief summary of what needs approval (max 500 chars)
- action_type: Type of action - deploy, tweet, email, code, custom
- urgency: Urgency level - low, normal, high, critical
- detailed_description: Additional context (optional)`,
  {
    workspace: z.string().describe("Workspace name (auto-created if doesn't exist)"),
    summary: z.string().max(500).describe("Brief summary of what needs approval"),
    action_type: z
      .enum(["deploy", "tweet", "email", "code", "custom"])
      .default("custom")
      .describe("Type of action"),
    urgency: z
      .enum(["low", "normal", "high", "critical"])
      .default("normal")
      .describe("Urgency level"),
    detailed_description: z.string().max(10000).optional().describe("Additional context"),
  },
  async (args) => {
    const apiKey = await getSwarmApiKey()
    if (!apiKey) {
      throw new Error(
        "SwarmAgent API key not configured.\n" +
          "Run: swarm auth login → SwarmAgent → paste key\n" +
          "Or set SWARM_AGENT_API_KEY environment variable.\n" +
          "Get your key at: https://swarmagent.dev/dashboard → API Keys"
      )
    }

    const response = await fetch(`${SWARM_API_BASE}/agentic-tasks`, {
      method: "POST",
      headers: {
        "X-API-Key": apiKey,
        "Content-Type": "application/json",
      },
      body: JSON.stringify({
        workspace: args.workspace,
        summary: args.summary,
        action_type: args.action_type,
        urgency: args.urgency,
        detailed_description: args.detailed_description,
      }),
    })

    if (!response.ok) {
      const error = await response.text()
      throw new Error(`Failed to create task (${response.status}): ${error}`)
    }

    const data = await response.json()

    return `Task created successfully!

ID: ${data.id}
Poll Token: ${data.poll_token}
Status: ${data.status}
Workspace: ${data.workspace_name || "default"}
Expires: ${data.expires_at}

Use swarmagent_poll_task with poll_token to check approval status.`
  }
)

/**
 * Poll task status
 *
 * Check the approval status of an agentic task.
 */
export const pollTaskTool = tool(
  "swarmagent_poll_task",
  `Check the approval status of an agentic task.

Returns one of:
- pending_approval: Waiting for human decision
- approved: Human approved - proceed with action
- rejected: Human rejected - do NOT proceed
- expired: Task expired - create new one if needed`,
  {
    poll_token: z.string().describe("Poll token from create_task"),
  },
  async (args) => {
    // Poll endpoint doesn't require API key - token IS the auth
    const response = await fetch(
      `${SWARM_API_BASE}/agentic-tasks/poll/${args.poll_token}`
    )

    if (!response.ok) {
      const error = await response.text()
      throw new Error(`Failed to poll task (${response.status}): ${error}`)
    }

    const data = await response.json()

    const statusMessages: Record<string, string> = {
      pending_approval: "⏳ Task is waiting for human approval. Check again shortly.",
      approved: "✅ Task was APPROVED! Proceed with the action.",
      rejected: "❌ Task was REJECTED. Do NOT proceed with the action.",
      expired: "⏰ Task has expired. Create a new task if still needed.",
    }

    let output = statusMessages[data.status] || `Status: ${data.status}`
    if (data.message) {
      output += `\n\nMessage from reviewer: ${data.message}`
    }

    return output
  }
)

/**
 * Submit task result
 *
 * Submit the outcome after completing an approved action.
 */
export const submitResultTool = tool(
  "swarmagent_submit_result",
  `Submit the result of a completed task.

Use after executing an approved action to record the outcome.
The result is visible in the SwarmAgent dashboard.`,
  {
    poll_token: z.string().describe("Poll token from create_task"),
    success: z.boolean().describe("Whether the action succeeded"),
    message: z.string().optional().describe("Result message or error details"),
    data: z.record(z.string(), z.unknown()).optional().describe("Additional result data (JSON object)"),
  },
  async (args) => {
    // Result endpoint doesn't require API key - token IS the auth
    const response = await fetch(
      `${SWARM_API_BASE}/agentic-tasks/poll/${args.poll_token}/result`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          result_data: {
            success: args.success,
            message: args.message,
            ...(args.data || {}),
          },
        }),
      }
    )

    if (!response.ok) {
      const error = await response.text()
      throw new Error(`Failed to submit result (${response.status}): ${error}`)
    }

    return "✅ Result submitted successfully!"
  }
)

/**
 * Apply theme preset
 *
 * Change the theme of the SwarmAgent dashboard.
 */
export const applyThemeTool = tool(
  "swarmagent_apply_theme",
  `Apply a theme preset to the SwarmAgent dashboard.

Available presets:
- dark: Default dark theme with green accents
- midnight: Deep blue professional theme
- cyberpunk: Neon cyan and magenta futuristic theme
- matrix: Classic green terminal aesthetic
- dracula: Purple and pink dark theme
- nord: Cool blue Scandinavian design
- monokai: Classic code editor color scheme
- solarized: Warm professional solarized dark`,
  {
    preset: z.enum([
      "dark",
      "midnight",
      "cyberpunk",
      "matrix",
      "dracula",
      "nord",
      "monokai",
      "solarized",
    ]).describe("Theme preset to apply"),
  },
  async (args) => {
    const apiKey = await getSwarmApiKey()
    if (!apiKey) {
      throw new Error(
        "SwarmAgent API key not configured.\n" +
          "Run: swarm auth login → SwarmAgent → paste key\n" +
          "Or set SWARM_AGENT_API_KEY environment variable."
      )
    }

    const response = await fetch(
      `${SWARM_API_BASE}/theme/preset/${args.preset}`,
      {
        method: "POST",
        headers: { "X-API-Key": apiKey },
      }
    )

    if (!response.ok) {
      const error = await response.text()
      throw new Error(`Failed to apply theme (${response.status}): ${error}`)
    }

    const descriptions: Record<string, string> = {
      dark: "Default dark theme with green accents",
      midnight: "Deep blue professional theme",
      cyberpunk: "Neon cyan and magenta futuristic theme",
      matrix: "Classic green terminal aesthetic",
      dracula: "Purple and pink dark theme",
      nord: "Cool blue Scandinavian design",
      monokai: "Classic code editor color scheme",
      solarized: "Warm professional solarized dark",
    }

    return `🎨 Theme "${args.preset}" applied successfully!\n\n${descriptions[args.preset]}`
  }
)

/**
 * Get current theme
 *
 * Retrieve the current theme configuration.
 */
export const getThemeTool = tool(
  "swarmagent_get_theme",
  `Get the current theme configuration from SwarmAgent dashboard.

Returns the current colors, effects, and font settings.`,
  {},
  async () => {
    const apiKey = await getSwarmApiKey()
    if (!apiKey) {
      throw new Error(
        "SwarmAgent API key not configured.\n" +
          "Run: swarm auth login → SwarmAgent → paste key"
      )
    }

    const response = await fetch(`${SWARM_API_BASE}/theme`, {
      method: "GET",
      headers: { "X-API-Key": apiKey },
    })

    if (!response.ok) {
      const error = await response.text()
      throw new Error(`Failed to get theme (${response.status}): ${error}`)
    }

    const theme = await response.json()

    return `Current Theme Configuration:

Colors:
  Primary: ${theme.colors?.primary || "default"}
  Background: ${theme.colors?.background || "default"}
  Text: ${theme.colors?.textPrimary || "default"}

Effects:
  Glow: ${theme.effects?.glowEnabled ? "enabled" : "disabled"}
  Glow Intensity: ${theme.effects?.glowIntensity || "medium"}
  Animations: ${theme.effects?.animationsEnabled ? "enabled" : "disabled"}

Fonts:
  Sans: ${theme.fonts?.sans || "Inter"}
  Mono: ${theme.fonts?.mono || "JetBrains Mono"}`
  }
)

/**
 * List pending tasks
 *
 * Get all pending approval tasks for the current user.
 */
export const listTasksTool = tool(
  "swarmagent_list_tasks",
  `List all pending approval tasks for the current user.

Returns a summary of tasks waiting for approval.`,
  {},
  async () => {
    const apiKey = await getSwarmApiKey()
    if (!apiKey) {
      throw new Error(
        "SwarmAgent API key not configured.\n" +
          "Run: swarm auth login → SwarmAgent → paste key"
      )
    }

    const response = await fetch(`${SWARM_API_BASE}/agentic-tasks`, {
      method: "GET",
      headers: { "X-API-Key": apiKey },
    })

    if (!response.ok) {
      const error = await response.text()
      throw new Error(`Failed to list tasks (${response.status}): ${error}`)
    }

    const tasks = await response.json()
    const pending = tasks.filter((t: any) => t.status === "pending_approval")

    if (pending.length === 0) {
      return "No pending approval tasks found."
    }

    const taskList = pending
      .map(
        (t: any) =>
          `• [${t.id}] ${t.summary}\n  Type: ${t.action_type} | Urgency: ${t.urgency} | Workspace: ${t.workspace_name || "default"}\n  Created: ${t.created_at}`
      )
      .join("\n\n")

    return `Found ${pending.length} pending approval task(s):\n\n${taskList}`
  }
)

/**
 * All SwarmAgent tools bundled together
 *
 * @example
 * ```typescript
 * import { createOpencode, swarmAgentTools } from "@swarm-ai/sdk"
 *
 * const { spawn } = await createOpencode({
 *   tools: swarmAgentTools,
 * })
 * ```
 */
export const swarmAgentTools = [
  createTaskTool,
  pollTaskTool,
  submitResultTool,
  applyThemeTool,
  getThemeTool,
  listTasksTool,
]
