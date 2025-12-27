import z from "zod"
import { Tool } from "./tool"
import DESCRIPTION from "./swarm-task.txt"
import { getSwarmApiKey, SWARM_API_BASE } from "./swarm-common"

/**
 * Agentic task status values
 */
type TaskStatus = "pending_approval" | "approved" | "rejected" | "expired"

/**
 * Response from creating a task
 */
interface CreateTaskResponse {
  id: number
  poll_token: string
  status: TaskStatus
  expires_at: string
  workspace_id?: number
  workspace_name?: string
  has_webhook?: boolean
}

/**
 * Response from polling a task
 */
interface PollTaskResponse {
  status: TaskStatus
  message?: string
  result_data?: Record<string, unknown>
}

/**
 * Response from listing tasks
 */
interface ListTasksResponse {
  id: number
  summary: string
  status: TaskStatus
  action_type: string
  urgency: string
  workspace_name?: string
  created_at: string
  expires_at?: string
}

export const SwarmTaskTool: Tool.Info = Tool.define("swarm-task", {
  description: DESCRIPTION,
  parameters: z.object({
    action: z
      .enum(["create", "poll", "result", "list"])
      .describe("Action to perform: create, poll, result, or list"),

    // For create
    summary: z
      .string()
      .max(500)
      .optional()
      .describe("Brief summary of what needs approval (required for create)"),
    workspace: z
      .string()
      .optional()
      .describe("Workspace name (auto-created if doesn't exist)"),
    action_type: z
      .enum(["deploy", "tweet", "email", "code", "custom"])
      .optional()
      .describe("Type of action (default: custom)"),
    urgency: z
      .enum(["low", "normal", "high", "critical"])
      .optional()
      .describe("Urgency level (default: normal)"),
    detailed_description: z
      .string()
      .max(10000)
      .optional()
      .describe("Additional context for the task"),

    // For poll/result
    poll_token: z
      .string()
      .optional()
      .describe("Poll token from create action (required for poll/result)"),

    // For result
    result_data: z
      .record(z.string(), z.unknown())
      .optional()
      .describe("Result data object (required for result action)"),
  }),

  async execute(params, ctx) {
    const apiKey = await getSwarmApiKey()

    switch (params.action) {
      case "create": {
        if (!apiKey) {
          throw new Error(
            "SwarmAgent API key not configured.\n" +
              "Run: swarm auth login → Other → swarmagent → paste key\n" +
              "Or set SWARM_AGENT_API_KEY environment variable.\n" +
              "Get your key at: https://swarmagent.dev/dashboard → API Keys"
          )
        }

        if (!params.summary) {
          throw new Error("summary is required for create action")
        }

        const response = await fetch(`${SWARM_API_BASE}/agentic-tasks`, {
          method: "POST",
          headers: {
            "X-API-Key": apiKey,
            "Content-Type": "application/json",
          },
          body: JSON.stringify({
            summary: params.summary,
            workspace: params.workspace,
            action_type: params.action_type || "custom",
            urgency: params.urgency || "normal",
            detailed_description: params.detailed_description,
          }),
          signal: ctx.abort,
        })

        if (!response.ok) {
          const error = await response.text()
          throw new Error(`Failed to create task (${response.status}): ${error}`)
        }

        const data: CreateTaskResponse = await response.json()

        return {
          title: `Created task: ${params.summary?.slice(0, 50)}...`,
          output: `Task created successfully!

ID: ${data.id}
Poll Token: ${data.poll_token}
Status: ${data.status}
Workspace: ${data.workspace_name || "default"}
Expires: ${data.expires_at}

Use action: poll with poll_token to check approval status.`,
          metadata: {
            id: data.id,
            poll_token: data.poll_token,
            status: data.status,
            workspace_name: data.workspace_name,
          },
        }
      }

      case "poll": {
        if (!params.poll_token) {
          throw new Error("poll_token is required for poll action")
        }

        // Poll endpoint doesn't require API key - token IS the auth
        const response = await fetch(
          `${SWARM_API_BASE}/agentic-tasks/poll/${params.poll_token}`,
          {
            method: "GET",
            signal: ctx.abort,
          }
        )

        if (!response.ok) {
          const error = await response.text()
          throw new Error(`Failed to poll task (${response.status}): ${error}`)
        }

        const data: PollTaskResponse = await response.json()

        const statusEmoji: Record<TaskStatus, string> = {
          pending_approval: "⏳",
          approved: "✅",
          rejected: "❌",
          expired: "⏰",
        }

        const statusMessage: Record<TaskStatus, string> = {
          pending_approval: "Task is waiting for human approval. Check again shortly.",
          approved: "Task was APPROVED! Proceed with the action.",
          rejected: "Task was REJECTED. Do NOT proceed with the action.",
          expired: "Task has expired. Create a new task if still needed.",
        }

        return {
          title: `Status: ${statusEmoji[data.status]} ${data.status}`,
          output: `${statusEmoji[data.status]} ${statusMessage[data.status]}${data.message ? `\n\nMessage from reviewer: ${data.message}` : ""}`,
          metadata: {
            status: data.status,
            message: data.message,
          },
        }
      }

      case "result": {
        if (!params.poll_token) {
          throw new Error("poll_token is required for result action")
        }
        if (!params.result_data) {
          throw new Error("result_data is required for result action")
        }

        // Result endpoint doesn't require API key - token IS the auth
        const response = await fetch(
          `${SWARM_API_BASE}/agentic-tasks/poll/${params.poll_token}/result`,
          {
            method: "POST",
            headers: {
              "Content-Type": "application/json",
            },
            body: JSON.stringify({
              result_data: params.result_data,
            }),
            signal: ctx.abort,
          }
        )

        if (!response.ok) {
          const error = await response.text()
          throw new Error(`Failed to submit result (${response.status}): ${error}`)
        }

        return {
          title: "Result submitted",
          output: "✅ Result submitted successfully!",
          metadata: {
            submitted: true,
          },
        }
      }

      case "list": {
        if (!apiKey) {
          throw new Error(
            "SwarmAgent API key not configured.\n" +
              "Run: swarm auth login → Other → swarmagent → paste key"
          )
        }

        const response = await fetch(`${SWARM_API_BASE}/agentic-tasks`, {
          method: "GET",
          headers: {
            "X-API-Key": apiKey,
          },
          signal: ctx.abort,
        })

        if (!response.ok) {
          const error = await response.text()
          throw new Error(`Failed to list tasks (${response.status}): ${error}`)
        }

        const tasks: ListTasksResponse[] = await response.json()
        const pending = tasks.filter((t) => t.status === "pending_approval")

        if (pending.length === 0) {
          return {
            title: "No pending tasks",
            output: "No pending approval tasks found.",
            metadata: {
              count: 0,
            },
          }
        }

        const taskList = pending
          .map(
            (t) =>
              `• [${t.id}] ${t.summary}\n  Type: ${t.action_type} | Urgency: ${t.urgency} | Workspace: ${t.workspace_name || "default"}\n  Created: ${t.created_at}`
          )
          .join("\n\n")

        return {
          title: `${pending.length} pending task(s)`,
          output: `Found ${pending.length} pending approval task(s):\n\n${taskList}`,
          metadata: {
            count: pending.length,
            tasks: pending.map((t) => ({ id: t.id, summary: t.summary })),
          },
        }
      }

      default:
        throw new Error(`Unknown action: ${params.action}`)
    }
  },
})
