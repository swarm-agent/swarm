import { Tool } from "./tool"
import DESCRIPTION from "./background-agent.txt"
import z from "zod"
import { Agent } from "../agent/agent"
import { BackgroundAgent } from "../background-agent"
import { Permission } from "../permission"
import { Config } from "../config/config"
import { Session } from "../session"

export const BackgroundAgentTool = Tool.define("background-agent", async () => {
  const agents = await Agent.list().then((x) => x.filter((a) => a.mode !== "primary"))
  const description = DESCRIPTION.replace(
    "{agents}",
    agents.map((a) => `- ${a.name}: ${a.description ?? "No description available."}`).join("\n"),
  )

  return {
    description,
    parameters: z.object({
      description: z.string().describe("A short description of the background task (shown in UI)"),
      prompt: z.string().describe("Detailed instructions for the background agent to execute"),
      agent: z
        .string()
        .optional()
        .describe("Agent type to use (defaults to parent session's agent). See available agents above."),
      tools: z
        .record(z.string(), z.boolean())
        .optional()
        .describe("Override tool availability for the background agent"),
    }),

    async execute(params, ctx) {
      // Check permission before spawning background agent
      const cfg = await Config.get()
      const permission = cfg.permission?.background_agent ?? "ask"

      if (permission === "deny") {
        throw new Error("Background agents are disabled by configuration")
      }

      // Resolve agent type: explicit param > parent session's agent > "build"
      let resolvedAgent = params.agent
      if (!resolvedAgent) {
        const parentMsgs = await Session.messages({ sessionID: ctx.sessionID, limit: 5 })
        const parentAssistantMsg = parentMsgs.find((m) => m.info.role === "assistant")
        resolvedAgent = parentAssistantMsg?.info.role === "assistant" ? parentAssistantMsg.info.mode : "build"
      }

      if (permission === "ask") {
        await Permission.ask({
          type: "background_agent",
          title: `Spawn background agent: ${params.description}`,
          sessionID: ctx.sessionID,
          messageID: ctx.messageID,
          callID: ctx.callID,
          metadata: {
            description: params.description,
            agent: resolvedAgent,
          },
        })
      }

      const info = await BackgroundAgent.spawn({
        parentSessionID: ctx.sessionID,
        description: params.description,
        prompt: params.prompt,
        agent: resolvedAgent,
        tools: params.tools,
      })

      ctx.metadata({
        title: `Spawned: ${params.description}`,
        metadata: {
          backgroundAgentId: info.id,
          sessionId: info.sessionID,
          agent: info.agent,
        },
      })

      const runningCount = BackgroundAgent.count()
      const maxCount = BackgroundAgent.maxConcurrent()

      return {
        title: `Spawned: ${params.description}`,
        metadata: {
          backgroundAgentId: info.id,
          sessionId: info.sessionID,
          agent: info.agent,
        },
        output:
          `Background agent "${params.description}" started successfully.\n\n` +
          `- Agent ID: ${info.id}\n` +
          `- Session ID: ${info.sessionID}\n` +
          `- Agent type: ${info.agent}\n` +
          `- Running background agents: ${runningCount}/${maxCount}\n\n` +
          `The agent is now working independently. You can continue with other tasks.\n` +
          `Progress will be shown in the background agents indicator, and you'll receive a notification when complete.\n` +
          `To view the background agent's work, navigate to its child session.`,
      }
    },
  }
})
