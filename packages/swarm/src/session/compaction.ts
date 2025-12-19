import { streamText, type ModelMessage, type StreamTextResult, type Tool as AITool } from "ai"
import { Session } from "."
import { Identifier } from "../id/id"
import { Instance } from "../project/instance"
import { Provider } from "../provider/provider"
import { defer } from "../util/defer"
import { MessageV2 } from "./message-v2"
import { SystemPrompt } from "./system"
import { Bus } from "../bus"
import z from "zod"
import type { ModelsDev } from "../provider/models"
import { SessionPrompt } from "./prompt"
import { Flag } from "../flag/flag"
import { Token } from "../util/token"
import { Log } from "../util/log"
import { SessionLock } from "./lock"
import { ProviderTransform } from "@/provider/transform"
import { SessionRetry } from "./retry"
import { Config } from "@/config/config"
import { Todo } from "./todo"
import { Storage } from "../storage/storage"
import { Snapshot } from "@/snapshot"
import { $ } from "bun"
import path from "path"

export namespace SessionCompaction {
  const log = Log.create({ service: "session.compaction" })

  // Types for structured resume context
  interface FileActivity {
    path: string
    reads: number
    edits: number
  }

  interface GitState {
    branch: string
    uncommitted: string[]
    staged: string[]
  }

  interface ResumeContext {
    originalRequest: string
    pendingTodos: Todo.Info[]
    filesExamined: FileActivity[]
    sessionChanges: Snapshot.FileDiff[]
    gitState: GitState
  }

  // Capture current git state
  async function captureGitState(): Promise<GitState> {
    const branch = await $`git branch --show-current`.cwd(Instance.directory).quiet().nothrow().text()
    const status = await $`git status --porcelain`.cwd(Instance.directory).quiet().nothrow().text()

    const uncommitted: string[] = []
    const staged: string[] = []

    for (const line of status.trim().split("\n").filter(Boolean)) {
      const file = line.slice(3)
      const index = line[0]
      const worktree = line[1]
      if (index === "M" || index === "A" || index === "D") {
        staged.push(file)
      }
      if (worktree === "M" || worktree === "?" || worktree === "D") {
        uncommitted.push(file)
      }
    }

    return { branch: branch.trim(), uncommitted, staged }
  }

  // Extract file activity from tool calls in messages
  function extractFileActivity(messages: MessageV2.WithParts[]): FileActivity[] {
    const activity = new Map<string, { reads: number; edits: number }>()

    for (const msg of messages) {
      for (const part of msg.parts) {
        if (part.type !== "tool") continue
        if (part.state.status !== "completed" && part.state.status !== "running") continue

        const input = part.state.input as Record<string, unknown>
        let filePath: string | undefined

        if (part.tool === "read" && input.filePath) {
          filePath = path.relative(Instance.worktree, String(input.filePath))
          const existing = activity.get(filePath) ?? { reads: 0, edits: 0 }
          existing.reads++
          activity.set(filePath, existing)
        }

        if ((part.tool === "edit" || part.tool === "write" || part.tool === "patch") && input.filePath) {
          filePath = path.relative(Instance.worktree, String(input.filePath))
          const existing = activity.get(filePath) ?? { reads: 0, edits: 0 }
          existing.edits++
          activity.set(filePath, existing)
        }
      }
    }

    return Array.from(activity.entries())
      .map(([p, a]) => ({ path: p, reads: a.reads, edits: a.edits }))
      .sort((a, b) => b.reads + b.edits - (a.reads + a.edits))
  }

  // Build structured context for resume
  async function buildResumeContext(sessionID: string, messages: MessageV2.WithParts[]): Promise<ResumeContext> {
    const [todos, diffs] = await Promise.all([
      Todo.get(sessionID),
      Storage.read<Snapshot.FileDiff[]>(["session_diff", sessionID]).then((x) => x ?? []),
    ])

    const originalRequest =
      messages
        .find((m) => m.info.role === "user")
        ?.parts.find((p): p is MessageV2.TextPart => p.type === "text" && !p.synthetic)
        ?.text?.slice(0, 500) ?? ""

    const filesExamined = extractFileActivity(messages)
    const gitState = await captureGitState()

    return {
      originalRequest,
      pendingTodos: todos.filter((t) => t.status !== "completed"),
      filesExamined,
      sessionChanges: diffs,
      gitState,
    }
  }

  // Format the structured resume message
  function formatResumeMessage(ctx: ResumeContext, summary: string): string {
    const sections: string[] = []

    if (ctx.originalRequest) {
      sections.push(`<original-request>\n${ctx.originalRequest}\n</original-request>`)
    }

    if (ctx.gitState.branch) {
      sections.push(
        `<git-state branch="${ctx.gitState.branch}">\nUncommitted: ${ctx.gitState.uncommitted.join(", ") || "none"}\nStaged: ${ctx.gitState.staged.join(", ") || "none"}\n</git-state>`,
      )
    }

    if (ctx.pendingTodos.length) {
      const todoLines = ctx.pendingTodos
        .map((t) => `- [${t.status === "in_progress" ? "→" : " "}] ${t.content} (${t.priority})`)
        .join("\n")
      sections.push(`<pending-todos>\n${todoLines}\n</pending-todos>`)
    }

    if (ctx.filesExamined.length) {
      const fileLines = ctx.filesExamined
        .slice(0, 15)
        .map((f) => `${f.path} (read ${f.reads}x${f.edits ? `, edited ${f.edits}x` : ""})`)
        .join("\n")
      const more = ctx.filesExamined.length > 15 ? `\n... and ${ctx.filesExamined.length - 15} more` : ""
      sections.push(`<files-examined count="${ctx.filesExamined.length}">\n${fileLines}${more}\n</files-examined>`)
    }

    if (ctx.sessionChanges.length) {
      const adds = ctx.sessionChanges.reduce((s, f) => s + f.additions, 0)
      const dels = ctx.sessionChanges.reduce((s, f) => s + f.deletions, 0)
      const changeLines = ctx.sessionChanges.map((f) => `${f.file} (+${f.additions}/-${f.deletions})`).join("\n")
      sections.push(`<session-changes +${adds} -${dels}>\n${changeLines}\n</session-changes>`)
    }

    return `<session-context>\n${sections.join("\n\n")}\n</session-context>\n\n<summary>\n${summary}\n</summary>\n\nResume from here. The structured data above is authoritative - don't re-read files you've already examined unless their content may have changed.`
  }

  export const Event = {
    Compacted: Bus.event(
      "session.compacted",
      z.object({
        sessionID: z.string(),
      }),
    ),
    Progress: Bus.event(
      "session.compacting.progress",
      z.object({
        sessionID: z.string(),
        step: z.enum(["started", "streaming", "context", "done"]),
        data: z
          .object({
            messagesCount: z.number().optional(),
            tokensInput: z.number().optional(),
            filesCount: z.number().optional(),
            todosCount: z.number().optional(),
            gitFiles: z.number().optional(),
          })
          .optional(),
      }),
    ),
  }

  export function isOverflow(input: { tokens: MessageV2.Assistant["tokens"]; model: ModelsDev.Model }) {
    if (Flag.SWARM_DISABLE_AUTOCOMPACT) return false
    const context = input.model.limit.context
    if (context === 0) return false
    const count = input.tokens.input + input.tokens.cache.read + input.tokens.output
    const output = Math.min(input.model.limit.output, SessionPrompt.OUTPUT_TOKEN_MAX) || SessionPrompt.OUTPUT_TOKEN_MAX
    const usable = context - output
    return count > usable
  }

  export const PRUNE_MINIMUM = 20_000
  export const PRUNE_PROTECT = 40_000
  const MAX_RETRIES = 10

  // goes backwards through parts until there are 40_000 tokens worth of tool
  // calls. then erases output of previous tool calls. idea is to throw away old
  // tool calls that are no longer relevant.
  export async function prune(input: { sessionID: string }) {
    if (Flag.SWARM_DISABLE_PRUNE) return
    log.info("pruning")
    const msgs = await Session.messages({ sessionID: input.sessionID })
    let total = 0
    let pruned = 0
    const toPrune = []
    let turns = 0

    loop: for (let msgIndex = msgs.length - 1; msgIndex >= 0; msgIndex--) {
      const msg = msgs[msgIndex]
      if (msg.info.role === "user") turns++
      if (turns < 2) continue
      if (msg.info.role === "assistant" && msg.info.summary) break loop
      for (let partIndex = msg.parts.length - 1; partIndex >= 0; partIndex--) {
        const part = msg.parts[partIndex]
        if (part.type === "tool")
          if (part.state.status === "completed") {
            if (part.state.time.compacted) break loop
            const estimate = Token.estimate(part.state.output)
            total += estimate
            if (total > PRUNE_PROTECT) {
              pruned += estimate
              toPrune.push(part)
            }
          }
      }
    }
    log.info("found", { pruned, total })
    if (pruned > PRUNE_MINIMUM) {
      for (const part of toPrune) {
        if (part.state.status === "completed") {
          part.state.time.compacted = Date.now()
          await Session.updatePart(part)
        }
      }
      log.info("pruned", { count: toPrune.length })
    }
  }

  export async function run(input: { sessionID: string; providerID: string; modelID: string; signal?: AbortSignal }) {
    log.info("compaction starting", {
      sessionID: input.sessionID,
      providerID: input.providerID,
      modelID: input.modelID,
    })
    if (!input.signal) SessionLock.assertUnlocked(input.sessionID)
    await using lock = input.signal === undefined ? SessionLock.acquire({ sessionID: input.sessionID }) : undefined
    const signal = input.signal ?? lock!.signal

    log.info("setting time.compacting", { sessionID: input.sessionID })
    await Session.update(input.sessionID, (draft) => {
      draft.time.compacting = Date.now()
    })
    await using _ = defer(async () => {
      await Session.update(input.sessionID, (draft) => {
        draft.time.compacting = undefined
      })
    })
    const toSummarize = await MessageV2.filterCompacted(MessageV2.stream(input.sessionID))
    const model = await Provider.getModel(input.providerID, input.modelID)

    // Calculate REAL token count from messages being compressed
    const inputTokens = toSummarize.reduce((sum, m) => {
      if (m.info.role === "assistant" && "tokens" in m.info) {
        return sum + (m.info.tokens?.input ?? 0) + (m.info.tokens?.output ?? 0)
      }
      return sum
    }, 0)

    // Publish REAL progress: started with actual message/token counts
    Bus.publish(Event.Progress, {
      sessionID: input.sessionID,
      step: "started",
      data: {
        messagesCount: toSummarize.length,
        tokensInput: inputTokens,
      },
    })

    const system = [
      ...SystemPrompt.summarize(model.providerID),
      ...(await SystemPrompt.environment()),
      ...(await SystemPrompt.custom()),
    ]

    const msg = (await Session.updateMessage({
      id: Identifier.ascending("message"),
      role: "assistant",
      parentID: toSummarize.findLast((m) => m.info.role === "user")?.info.id!,
      sessionID: input.sessionID,
      mode: "build",
      path: {
        cwd: Instance.directory,
        root: Instance.worktree,
      },
      summary: true,
      cost: 0,
      tokens: {
        output: 0,
        input: 0,
        reasoning: 0,
        cache: { read: 0, write: 0 },
      },
      modelID: input.modelID,
      providerID: model.providerID,
      time: {
        created: Date.now(),
      },
    })) as MessageV2.Assistant

    const part = (await Session.updatePart({
      type: "text",
      sessionID: input.sessionID,
      messageID: msg.id,
      id: Identifier.ascending("part"),
      text: "",
      time: {
        start: Date.now(),
      },
    })) as MessageV2.TextPart

    // Publish streaming started
    Bus.publish(Event.Progress, {
      sessionID: input.sessionID,
      step: "streaming",
    })

    // Build simple messages directly - bypass SDK's convertToModelMessages validation
    const simpleMessages: ModelMessage[] = []
    for (const msg of toSummarize) {
      if (msg.info.role === "user") {
        const text = msg.parts
          .filter((p): p is MessageV2.TextPart => p.type === "text" && !!p.text)
          .map((p) => p.text)
          .join("\n")
        if (text) simpleMessages.push({ role: "user", content: text })
      }
      if (msg.info.role === "assistant") {
        const textParts = msg.parts.filter((p): p is MessageV2.TextPart => p.type === "text" && !!p.text)
        const toolParts = msg.parts.filter(
          (p): p is MessageV2.ToolPart => p.type === "tool" && p.state.status === "completed",
        )
        // Build content: text + tool summaries
        const content: string[] = []
        for (const t of textParts) content.push(t.text)
        for (const t of toolParts) {
          if (t.state.status === "completed") {
            content.push(`[Used ${t.tool}: ${t.state.title}]`)
          }
        }
        if (content.length) simpleMessages.push({ role: "assistant", content: content.join("\n") })
      }
    }

    const allMessages = [
      ...system.map((x): ModelMessage => ({ role: "system", content: x })),
      ...simpleMessages,
      {
        role: "user" as const,
        content:
          "Provide a detailed but concise summary of our conversation above. Focus on information that would be helpful for continuing the conversation, including what we did, what we're doing, which files we're working on, and what we're going to do next.",
      },
    ]

    const doStream = () =>
      streamText({
        // set to 0, we handle loop
        maxRetries: 0,
        model: model.language,
        providerOptions: ProviderTransform.providerOptions(model.npm, model.providerID, model.info.options),
        headers: model.info.headers,
        abortSignal: signal,
        onError(error) {
          log.error("stream error", {
            error,
          })
        },
        tools: model.info.tool_call ? {} : undefined,
        messages: allMessages,
      })

    // TODO: reduce duplication between compaction.ts & prompt.ts
    const process = async (
      stream: StreamTextResult<Record<string, AITool>, never>,
      retries: { count: number; max: number },
    ) => {
      let shouldRetry = false
      try {
        for await (const value of stream.fullStream) {
          signal.throwIfAborted()
          switch (value.type) {
            case "text-delta":
              part.text += value.text
              if (value.providerMetadata) part.metadata = value.providerMetadata
              if (part.text)
                await Session.updatePart({
                  part,
                  delta: value.text,
                })
              continue
            case "text-end": {
              part.text = part.text.trimEnd()
              part.time = {
                start: Date.now(),
                end: Date.now(),
              }
              if (value.providerMetadata) part.metadata = value.providerMetadata
              await Session.updatePart(part)
              continue
            }
            case "finish-step": {
              const usage = Session.getUsage({
                model: model.info,
                usage: value.usage,
                metadata: value.providerMetadata,
              })
              msg.cost += usage.cost
              msg.tokens = usage.tokens
              await Session.updateMessage(msg)
              continue
            }
            case "error":
              throw value.error
            default:
              continue
          }
        }
      } catch (e) {
        log.error("compaction error", {
          error: e,
        })
        const error = MessageV2.fromError(e, { providerID: input.providerID })
        if (retries.count < retries.max && MessageV2.APIError.isInstance(error) && error.data.isRetryable) {
          shouldRetry = true
          await Session.updatePart({
            id: Identifier.ascending("part"),
            messageID: msg.id,
            sessionID: msg.sessionID,
            type: "retry",
            attempt: retries.count + 1,
            time: {
              created: Date.now(),
            },
            error,
          })
        } else {
          msg.error = error
          Bus.publish(Session.Event.Error, {
            sessionID: msg.sessionID,
            error: msg.error,
          })
        }
      }

      const parts = await MessageV2.parts(msg.id)
      return {
        info: msg,
        parts,
        shouldRetry,
      }
    }

    let stream = doStream()
    const cfg = await Config.get()
    const maxRetries = cfg.experimental?.chatMaxRetries ?? MAX_RETRIES
    let result = await process(stream, {
      count: 0,
      max: maxRetries,
    })
    if (result.shouldRetry) {
      const start = Date.now()
      for (let retry = 1; retry < maxRetries; retry++) {
        const lastRetryPart = result.parts.findLast((p): p is MessageV2.RetryPart => p.type === "retry")

        if (lastRetryPart) {
          const delayMs = SessionRetry.getBoundedDelay({
            error: lastRetryPart.error,
            attempt: retry,
            startTime: start,
          })
          if (!delayMs) {
            break
          }

          log.info("retrying with backoff", {
            attempt: retry,
            delayMs,
            elapsed: Date.now() - start,
          })

          const stop = await SessionRetry.sleep(delayMs, signal)
            .then(() => false)
            .catch((error) => {
              if (error instanceof DOMException && error.name === "AbortError") {
                const err = new MessageV2.AbortedError(
                  { message: error.message },
                  {
                    cause: error,
                  },
                ).toObject()
                result.info.error = err
                Bus.publish(Session.Event.Error, {
                  sessionID: result.info.sessionID,
                  error: result.info.error,
                })
                return true
              }
              throw error
            })

          if (stop) break
        }

        stream = doStream()
        result = await process(stream, {
          count: retry,
          max: maxRetries,
        })
        if (!result.shouldRetry) {
          break
        }
      }
    }

    msg.time.completed = Date.now()

    if (
      !msg.error ||
      (MessageV2.AbortedError.isInstance(msg.error) &&
        result.parts.some((part): part is MessageV2.TextPart => part.type === "text" && part.text.length > 0))
    ) {
      msg.summary = true

      // Build structured resume context and create enhanced resume message
      const summaryText = part.text
      const resumeCtx = await buildResumeContext(input.sessionID, toSummarize)

      // Publish REAL context data
      Bus.publish(Event.Progress, {
        sessionID: input.sessionID,
        step: "context",
        data: {
          filesCount: resumeCtx.filesExamined.length,
          todosCount: resumeCtx.pendingTodos.length,
          gitFiles: resumeCtx.gitState.uncommitted.length + resumeCtx.gitState.staged.length,
        },
      })

      const resumeText = formatResumeMessage(resumeCtx, summaryText)

      // Create the resume user message with structured context
      const resumeMsgID = Identifier.ascending("message")
      await Session.updateMessage({
        id: resumeMsgID,
        role: "user",
        sessionID: input.sessionID,
        time: { created: Date.now() },
      })

      await Session.updatePart({
        type: "text",
        sessionID: input.sessionID,
        messageID: resumeMsgID,
        id: Identifier.ascending("part"),
        text: resumeText,
        time: { start: Date.now(), end: Date.now() },
        synthetic: true,
      })

      Bus.publish(Event.Progress, {
        sessionID: input.sessionID,
        step: "done",
      })

      Bus.publish(Event.Compacted, {
        sessionID: input.sessionID,
      })
    }
    await Session.updateMessage(msg)

    return {
      info: msg,
      parts: result.parts,
    }
  }
}
