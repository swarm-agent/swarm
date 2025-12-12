/**
 * Simple spawn API for running agents
 *
 * Usage:
 *   // Simple - uses 'edit' profile by default (no bash, safe file edits)
 *   const handle = spawn("fix the tests")
 *   await handle.wait()
 *
 *   // With profile
 *   const handle = spawn({ prompt: "analyze code", profile: "analyze" })
 *
 *   // With agent type
 *   const handle = spawn({ prompt: "plan feature", agent: "plan" })
 *
 *   // With tool overrides
 *   const handle = spawn({ prompt: "run tests", tools: { bash: true } })
 *
 *   // Stream events:
 *   for await (const event of handle.stream()) {
 *     console.log(event)
 *   }
 *
 * Note: Call either stream() OR wait(), not both.
 *
 * Profiles:
 *   - analyze: read-only, no modifications
 *   - edit: file edits allowed, no shell (DEFAULT)
 *   - full: all tools, bash requires approval
 *   - yolo: all tools, no restrictions (DANGER!)
 */

import type { OpencodeClient } from "./gen/sdk.gen.js"
import type {
  Part,
  EventMessagePartUpdated,
  EventSessionCompleted,
  EventSessionAborted,
  EventTodoUpdated,
  EventPermissionUpdated,
  Todo,
  ToolPart,
} from "./gen/types.gen.js"
import { type ProfileName, getProfile } from "./profiles.js"

export type SpawnEvent =
  | { type: "text"; text: string; delta?: string }
  | { type: "tool.start"; name: string; input: Record<string, unknown> }
  | { type: "tool.end"; name: string; output: string }
  | { type: "todo"; todos: Todo[] }
  | { type: "permission"; id: string; title: string; metadata: Record<string, unknown> }
  | { type: "completed" }
  | { type: "aborted" }
  | { type: "error"; error: Error }

export interface SpawnResult {
  sessionId: string
  success: boolean
  error?: Error
}

export interface SpawnOptions {
  /** The prompt/task for the agent */
  prompt: string
  
  /** Working directory (defaults to server cwd) - not yet implemented */
  cwd?: string
  
  /**
   * Predefined permission profile
   * - analyze: read-only, no modifications
   * - edit: file edits allowed, no shell (DEFAULT)
   * - full: all tools, bash requires approval
   * - yolo: all tools, no restrictions (DANGER!)
   */
  profile?: ProfileName
  
  /**
   * Agent type to use
   * - "build": default coding agent
   * - "plan": planning/analysis agent
   * - "general": general purpose agent
   * - Or any custom agent name from your config
   */
  agent?: string
  
  /**
   * Tool overrides - merged on top of profile settings
   * Use to enable/disable specific tools
   * @example { bash: true } to enable bash even with 'edit' profile
   */
  tools?: Record<string, boolean>
  
  /** Called when complete (for fire-and-forget) */
  onComplete?: (result: SpawnResult) => void
}

export interface SpawnHandle {
  /** Session ID (available after init) */
  readonly sessionId: Promise<string>
  /** Wait for completion (no events) */
  wait(): Promise<SpawnResult>
  /** Stream events as they happen */
  stream(): AsyncGenerator<SpawnEvent, SpawnResult, unknown>
  /** Abort the session */
  abort(): Promise<void>
}

export function createSpawn(client: OpencodeClient) {
  return function spawn(promptOrOptions: string | SpawnOptions): SpawnHandle {
    const options: SpawnOptions =
      typeof promptOrOptions === "string" ? { prompt: promptOrOptions } : promptOrOptions

    // Get profile tools (default to 'edit' profile for safety)
    const profileName = options.profile ?? "edit"
    const profile = getProfile(profileName)

    // Merge profile tools with any explicit overrides
    const tools: Record<string, boolean> = {
      ...profile.tools,
      ...(options.tools ?? {}),
    }

    let abortRequested = false
    let _sessionId: string | undefined
    let _sessionIdResolve!: (id: string) => void
    let _sessionIdReject!: (err: Error) => void
    const sessionIdPromise = new Promise<string>((resolve, reject) => {
      _sessionIdResolve = resolve
      _sessionIdReject = reject
    })

    // Eagerly subscribe to SSE and create session
    // This allows sessionId to be available before stream() is called
    let ssePromise: ReturnType<typeof client.event.subscribe> | undefined
    let sessionPromise: ReturnType<typeof client.session.create> | undefined

    const initPromise = (async () => {
      try {
        // 1. Subscribe to SSE FIRST (before creating session)
        ssePromise = client.event.subscribe({
          headers: {
            Accept: "text/event-stream",
          },
        })
        const sseResult = await ssePromise

        // 2. Create session
        sessionPromise = client.session.create()
        const session = await sessionPromise
        if (!session.data) throw new Error("Failed to create session")
        const id = session.data.id
        _sessionId = id
        _sessionIdResolve(id)
        return { sse: sseResult.stream, id }
      } catch (err) {
        const error = err instanceof Error ? err : new Error(String(err))
        _sessionIdReject(error)
        throw error
      }
    })()

    // Internal stream implementation
    async function* createStream(): AsyncGenerator<SpawnEvent, SpawnResult, unknown> {
      // Wait for eager initialization to complete
      const { sse, id } = await initPromise

      // 3. Build prompt parts
      const parts: Array<{ type: "text"; text: string } | { type: "agent"; name: string; prompt: string }> = []
      
      // If agent specified, add agent part
      if (options.agent) {
        parts.push({
          type: "agent",
          name: options.agent,
          prompt: options.prompt,
        })
      } else {
        parts.push({
          type: "text",
          text: options.prompt,
        })
      }

      // 4. Send the prompt with tools (fire and forget - don't await, so we catch SSE events)
      // The 'tools' field is merged LAST in resolveTools(), so it WINS over config/agent defaults
      client.session.prompt({
        path: { id },
        body: {
          parts,
          tools,  // Path B: Pass tools directly in request body
        },
      }).catch(() => {}) // errors will come via SSE

      try {
        for await (const event of sse) {
          if (abortRequested) break

          // Event is directly the typed Event object (or needs parsing if string)
          const data = typeof event === "string" ? JSON.parse(event) : event
          if (!data?.type || !data.properties) continue

          const props = data.properties

          // Handle different event types (filter by our session)
          switch (data.type) {
            case "message.part.updated": {
              const partProps = props as EventMessagePartUpdated["properties"]
              const part = partProps.part
              const delta = partProps.delta
              if (part.sessionID !== id) continue

              if (part.type === "text") {
                yield { type: "text", text: part.text, delta }
              } else if (part.type === "tool") {
                const toolPart = part as ToolPart
                if (toolPart.state.status === "running") {
                  yield {
                    type: "tool.start",
                    name: toolPart.tool,
                    input: toolPart.state.input,
                  }
                } else if (toolPart.state.status === "completed") {
                  yield {
                    type: "tool.end",
                    name: toolPart.tool,
                    output: toolPart.state.output,
                  }
                }
              }
              break
            }

            case "todo.updated": {
              const todoProps = props as EventTodoUpdated["properties"]
              if (todoProps.sessionID !== id) continue
              yield { type: "todo", todos: todoProps.todos }
              break
            }

            case "permission.updated": {
              const perm = props as EventPermissionUpdated["properties"]
              if (perm.sessionID !== id) continue
              yield {
                type: "permission",
                id: perm.id,
                title: perm.title,
                metadata: perm.metadata,
              }
              break
            }

            case "session.completed": {
              const completed = props as EventSessionCompleted["properties"]
              if (completed.sessionID !== id) continue
              yield { type: "completed" }
              const result = { sessionId: id, success: true }
              options.onComplete?.(result)
              return result
            }

            case "session.aborted": {
              const abortedProps = props as EventSessionAborted["properties"]
              if (abortedProps.sessionID !== id) continue
              yield { type: "aborted" }
              const result = { sessionId: id, success: false }
              options.onComplete?.(result)
              return result
            }
          }
        }
      } catch (error) {
        const err = error instanceof Error ? error : new Error(String(error))
        yield { type: "error", error: err }
        const result = { sessionId: id, success: false, error: err }
        options.onComplete?.(result)
        return result
      }

      // If we get here, SSE closed without completing
      const result = { sessionId: id, success: !abortRequested }
      options.onComplete?.(result)
      return result
    }

    return {
      get sessionId() {
        return sessionIdPromise
      },

      stream: createStream,

      async wait(): Promise<SpawnResult> {
        const gen = createStream()
        let result: SpawnResult | undefined
        while (true) {
          const next = await gen.next()
          if (next.done) {
            result = next.value
            break
          }
        }
        return result ?? { sessionId: await sessionIdPromise, success: false }
      },

      async abort(): Promise<void> {
        abortRequested = true
        const id = await sessionIdPromise
        await client.session.abort({ path: { id } })
      },
    }
  }
}
