import z from "zod"
import { Bus } from "../bus"
import { Identifier } from "../id/id"
import { Instance } from "../project/instance"
import { Log } from "../util/log"
import { Session } from "../session"
import { SessionLock } from "../session/lock"
import { SessionPrompt } from "../session/prompt"
import { MessageV2 } from "../session/message-v2"
import { Agent } from "../agent/agent"
import { Config } from "../config/config"

export namespace BackgroundAgent {
  const log = Log.create({ service: "background-agent" })

  const MAX_CONCURRENT = 5

  export const Progress = z.object({
    currentTool: z.string().optional(),
    toolCount: z.number(),
    startTime: z.number(),
    endTime: z.number().optional(),
  })
  export type Progress = z.infer<typeof Progress>

  export const Info = z
    .object({
      id: z.string(),
      sessionID: Identifier.schema("session"),
      parentSessionID: Identifier.schema("session"),
      description: z.string(),
      agent: z.string(),
      status: z.enum(["running", "completed", "failed", "aborted"]),
      progress: Progress,
      error: z.string().optional(),
    })
    .meta({
      ref: "BackgroundAgent",
    })
  export type Info = z.infer<typeof Info>

  export const Event = {
    Spawned: Bus.event(
      "background-agent.spawned",
      z.object({
        info: Info,
      }),
    ),
    Updated: Bus.event(
      "background-agent.updated",
      z.object({
        info: Info,
      }),
    ),
    Completed: Bus.event(
      "background-agent.completed",
      z.object({
        info: Info,
      }),
    ),
    Failed: Bus.event(
      "background-agent.failed",
      z.object({
        info: Info,
        error: z.string(),
      }),
    ),
  }

  const state = Instance.state(
    () => {
      const agents = new Map<string, Info>()
      return { agents }
    },
    async (current) => {
      // Cleanup: abort all running background agents on dispose
      for (const [id, info] of current.agents) {
        if (info.status === "running") {
          log.info("disposing background agent", { id })
          SessionLock.abort(info.sessionID)
        }
      }
      current.agents.clear()
    },
  )

  export const SpawnInput = z.object({
    parentSessionID: Identifier.schema("session"),
    description: z.string(),
    prompt: z.string(),
    agent: z.string().optional(),
    tools: z.record(z.string(), z.boolean()).optional(),
    model: z
      .object({
        providerID: z.string(),
        modelID: z.string(),
      })
      .optional(),
  })
  export type SpawnInput = z.infer<typeof SpawnInput>

  /**
   * Spawn a background agent that runs independently.
   * Returns immediately - does NOT block on completion.
   */
  export async function spawn(input: SpawnInput): Promise<Info> {
    // 1. Check concurrent limit
    const running = Array.from(state().agents.values()).filter((a) => a.status === "running")
    if (running.length >= MAX_CONCURRENT) {
      throw new Error(`Maximum ${MAX_CONCURRENT} background agents allowed. Wait for one to complete.`)
    }

    // 2. Get parent session info for agent/model inheritance
    const parentMsgs = await Session.messages({ sessionID: input.parentSessionID, limit: 5 })
    const parentAssistantMsg = parentMsgs.find((m) => m.info.role === "assistant")
    const parentModelInfo =
      parentAssistantMsg?.info.role === "assistant"
        ? { modelID: parentAssistantMsg.info.modelID, providerID: parentAssistantMsg.info.providerID }
        : undefined
    // Inherit agent from parent session if not explicitly specified
    const parentAgentName = parentAssistantMsg?.info.role === "assistant" ? parentAssistantMsg.info.mode : undefined

    // 3. Resolve agent with background-specific permissions (inherit from parent or use specified)
    const agentName = input.agent ?? parentAgentName ?? "build"
    const agentInfo = await Agent.get(agentName, { isBackground: true })
    if (!agentInfo) {
      throw new Error(`Unknown agent type: ${agentName}`)
    }

    // 4. Load background agent config for prompt prepending
    const cfg = await Config.get()
    const backgroundConfig = cfg.backgroundAgent

    // 5. Create child session
    const session = await Session.create({
      parentID: input.parentSessionID,
      title: `[BG] ${input.description} (@${agentInfo.name})`,
    })

    // 6. Create tracking info
    const info: Info = {
      id: Identifier.ascending("bgagent"),
      sessionID: session.id,
      parentSessionID: input.parentSessionID,
      description: input.description,
      agent: agentInfo.name,
      status: "running",
      progress: {
        toolCount: 0,
        startTime: Date.now(),
      },
    }

    // 7. Store in state and publish spawn event
    state().agents.set(info.id, info)
    log.info("spawned background agent", { id: info.id, description: input.description, agent: agentInfo.name })
    Bus.publish(Event.Spawned, { info })

    // 8. Subscribe to progress updates
    const unsub = Bus.subscribe(MessageV2.Event.PartUpdated, async (evt) => {
      if (evt.properties.part.sessionID !== session.id) return
      if (evt.properties.part.type !== "tool") return

      const current = state().agents.get(info.id)
      if (!current || current.status !== "running") return

      // Update progress
      current.progress.toolCount++
      current.progress.currentTool = evt.properties.part.tool

      Bus.publish(Event.Updated, { info: current })
    })

    // 9. Resolve model (inherit from parent or use agent-specific)
    const model = input.model ??
      agentInfo.model ??
      parentModelInfo ?? {
        modelID: "",
        providerID: "",
      }

    // 10. Fire and forget - DON'T await!
    // Prepend background-specific prompt if configured
    const prompt = backgroundConfig?.prompt ? `${backgroundConfig.prompt}\n\n---\n\n${input.prompt}` : input.prompt
    const promptParts = await SessionPrompt.resolvePromptParts(prompt)

    SessionPrompt.prompt({
      sessionID: session.id,
      model: {
        modelID: model.modelID,
        providerID: model.providerID,
      },
      agent: agentInfo.name,
      tools: {
        todowrite: false,
        todoread: false,
        task: false,
        "background-agent": false, // Prevent nested background agents
        ...input.tools,
        ...agentInfo.tools,
      },
      parts: promptParts,
    })
      .then(() => {
        // Success handler
        const current = state().agents.get(info.id)
        if (!current) return

        current.status = "completed"
        current.progress.endTime = Date.now()
        current.progress.currentTool = undefined

        log.info("background agent completed", {
          id: info.id,
          description: input.description,
          duration: current.progress.endTime - current.progress.startTime,
        })

        Bus.publish(Event.Completed, { info: current })
        unsub()
      })
      .catch((error) => {
        // Error handler
        const current = state().agents.get(info.id)
        if (!current) return

        const errorMessage = error instanceof Error ? error.message : String(error)
        current.status = "failed"
        current.progress.endTime = Date.now()
        current.progress.currentTool = undefined
        current.error = errorMessage

        log.error("background agent failed", {
          id: info.id,
          description: input.description,
          error: errorMessage,
        })

        Bus.publish(Event.Failed, { info: current, error: errorMessage })
        unsub()
      })

    // 10. Return immediately
    return info
  }

  /**
   * Abort a running background agent
   */
  export function abort(id: string): boolean {
    const info = state().agents.get(id)
    if (!info) {
      log.warn("abort: background agent not found", { id })
      return false
    }
    if (info.status !== "running") {
      log.warn("abort: background agent not running", { id, status: info.status })
      return false
    }

    log.info("aborting background agent", { id })
    SessionLock.abort(info.sessionID)

    info.status = "aborted"
    info.progress.endTime = Date.now()
    info.progress.currentTool = undefined
    info.error = "Aborted by user"

    Bus.publish(Event.Failed, { info, error: "Aborted by user" })
    return true
  }

  /**
   * List all tracked background agents
   */
  export function list(): Info[] {
    return Array.from(state().agents.values())
  }

  /**
   * Get a specific background agent by ID
   */
  export function get(id: string): Info | undefined {
    return state().agents.get(id)
  }

  /**
   * Get count of currently running background agents
   */
  export function count(): number {
    return Array.from(state().agents.values()).filter((a) => a.status === "running").length
  }

  /**
   * Get the maximum number of concurrent background agents
   */
  export function maxConcurrent(): number {
    return MAX_CONCURRENT
  }

  /**
   * Remove a completed/failed background agent from tracking
   */
  export function remove(id: string): boolean {
    const info = state().agents.get(id)
    if (!info) return false
    if (info.status === "running") {
      log.warn("remove: cannot remove running background agent", { id })
      return false
    }
    state().agents.delete(id)
    return true
  }

  /**
   * Clear all completed/failed background agents
   */
  export function clearFinished(): number {
    let cleared = 0
    for (const [id, info] of state().agents) {
      if (info.status !== "running") {
        state().agents.delete(id)
        cleared++
      }
    }
    return cleared
  }
}
