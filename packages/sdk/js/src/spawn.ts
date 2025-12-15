/**
 * Simple spawn API for running agents
 *
 * Usage:
 *   // Simple - noninteractive mode (auto-approves all permissions)
 *   const handle = spawn("fix the tests")
 *   await handle.wait()
 *
 *   // Interactive mode with permission callback
 *   const handle = spawn({
 *     prompt: "run the tests",
 *     mode: "interactive",
 *     onPermission: async (p) => {
 *       console.log(`Permission: ${p.title}`)
 *       return "approve"  // or "reject", "always", { pin: "1234" }
 *     }
 *   })
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
 * Modes:
 *   - noninteractive (default): Auto-approves all "ask" permissions, fails on "pin"
 *   - interactive: Uses onPermission callback for each permission request
 *
 * Profiles (optional, for convenience):
 *   - analyze: read-only, no modifications
 *   - edit: file edits allowed, no shell
 *   - full: all tools enabled
 *   - yolo: all tools, noninteractive (DANGER!)
 */

import type { OpencodeClient } from "./gen/sdk.gen.js"
import type {
  EventMessagePartUpdated,
  EventSessionCompleted,
  EventSessionAborted,
  EventTodoUpdated,
  EventPermissionUpdated,
  Todo,
  ToolPart,
  Permission,
} from "./gen/types.gen.js"
import { type ProfileName, type CustomProfile, getProfile, isCustomProfile } from "./profiles.js"
import type { McpServers, SwarmMcpServer } from "./mcp-server.js"
import type { ToolContext } from "./tool.js"

// ============================================================================
// Types
// ============================================================================

export type SpawnEvent =
  | { type: "text"; text: string; delta?: string }
  | { type: "tool.start"; name: string; input: Record<string, unknown> }
  | { type: "tool.end"; name: string; output: string }
  | { type: "todo"; todos: Todo[] }
  | { type: "permission"; id: string; permissionType: string; title: string; metadata: Record<string, unknown> }
  | { type: "permission.handled"; id: string; response: PermissionResponse }
  | { type: "completed" }
  | { type: "aborted" }
  | { type: "error"; error: Error }

export interface SpawnResult {
  sessionId: string
  success: boolean
  error?: Error
}

/**
 * Permission request received from the server
 */
export interface PermissionRequest {
  /** Unique permission ID */
  id: string
  /** Permission type: "bash", "edit", "pin", "background_agent", etc. */
  permissionType: string
  /** Human-readable title/description */
  title: string
  /** Additional metadata (command, file path, etc.) */
  metadata: Record<string, unknown>
}

/**
 * Response to send back for a permission request
 */
export type PermissionResponse =
  | "approve"              // Approve once
  | "always"               // Approve and remember for session
  | "reject"               // Reject
  | { reject: string }     // Reject with message
  | { pin: string }        // PIN verification

export interface SpawnOptions {
  /** The prompt/task for the agent */
  prompt: string
  
  /** Session title (defaults to truncated prompt) */
  title?: string
  
  /** Working directory (defaults to server cwd) - not yet implemented */
  cwd?: string
  
  /**
   * Container profile name - run this session inside a container
   * Profile must be defined in opencode.json under container.profiles
   * 
   * @example
   * ```typescript
   * // Run in a pre-configured container
   * spawn({
   *   prompt: "...",
   *   containerProfile: "twitter-bot",
   * })
   * ```
   */
  containerProfile?: string
  
  /**
   * Execution mode
   * - "noninteractive" (default): Auto-approves "ask" permissions, fails on "pin"
   * - "interactive": Uses onPermission callback for each permission
   */
  mode?: "interactive" | "noninteractive"
  
  /**
   * Permission handler (required for interactive mode)
   * Called when a permission request is received
   * Return the response to send back to the server
   */
  onPermission?: (request: PermissionRequest) => Promise<PermissionResponse>
  
  /**
   * Permission profile - predefined or custom
   * 
   * Predefined profiles:
   * - analyze: read-only, no modifications
   * - edit: file edits allowed, no shell
   * - full: all tools enabled
   * - yolo: all tools, forces noninteractive mode
   * 
   * Custom profiles (from createSwarmProfile):
   * - Include custom MCP tools
   * - Per-profile env files
   * - Tool and permission overrides
   * 
   * @example
   * ```typescript
   * // Predefined
   * spawn({ prompt: "...", profile: "full" })
   * 
   * // Custom
   * const myProfile = createSwarmProfile({ ... })
   * spawn({ prompt: "...", profile: myProfile })
   * ```
   */
  profile?: ProfileName | CustomProfile
  
  /**
   * In-process MCP servers with custom tools
   * Merged with profile mcpServers (explicit wins)
   * 
   * @example
   * ```typescript
   * spawn({
   *   prompt: "...",
   *   mcpServers: {
   *     "my-tools": createSwarmMcpServer({
   *       name: "my-tools",
   *       tools: [tool("...", "...", {}, async () => "...")],
   *     }),
   *   },
   * })
   * ```
   */
  mcpServers?: McpServers
  
  /**
   * Agent type to use
   * - "build": default coding agent
   * - "plan": planning/analysis agent
   * - "general": general purpose agent
   * - Or any custom agent name from your config
   */
  agent?: string
  
  /**
   * Tool overrides - merged on top of profile/agent settings
   * Use to enable/disable specific tools
   * @example { bash: true } to enable bash
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
  /** Respond to a permission request manually (for custom handling) */
  respondToPermission(permissionId: string, response: PermissionResponse): Promise<void>
}

// ============================================================================
// Helper: Map SDK response to server response format
// ============================================================================

// The server accepts PIN responses but the generated SDK types may not include it
// We use 'any' for the PIN case as a workaround until types are regenerated
type ServerPermissionResponse = 
  | "once" 
  | "always" 
  | "reject" 
  | { type: "reject"; message: string }
  | { type: "pin"; pin: string }

function mapPermissionResponse(response: PermissionResponse): ServerPermissionResponse {
  if (response === "approve") return "once"
  if (response === "always") return "always"
  if (response === "reject") return "reject"
  if (typeof response === "object" && "reject" in response) {
    return { type: "reject", message: response.reject }
  }
  if (typeof response === "object" && "pin" in response) {
    return { type: "pin", pin: response.pin }
  }
  return "once" // fallback
}

// ============================================================================
// Main spawn factory
// ============================================================================

export function createSpawn(client: OpencodeClient) {
  return function spawn(promptOrOptions: string | SpawnOptions): SpawnHandle {
    const options: SpawnOptions =
      typeof promptOrOptions === "string" ? { prompt: promptOrOptions } : promptOrOptions

    // Handle custom vs predefined profiles
    const customProfile = options.profile && isCustomProfile(options.profile) ? options.profile : null
    const predefinedProfileName = !customProfile 
      ? (typeof options.profile === "string" ? options.profile : "full")
      : null

    // Determine mode
    // - yolo profile forces noninteractive
    // - default is noninteractive for SDK use (automation-friendly)
    const isYolo = predefinedProfileName === "yolo"
    const mode = isYolo ? "noninteractive" : (options.mode ?? "noninteractive")
    
    // Validate: interactive mode should have onPermission callback
    if (mode === "interactive" && !options.onPermission) {
      console.warn("[spawn] Interactive mode without onPermission callback - permissions will block forever")
    }

    // Get profile tools
    const baseProfile = customProfile ?? getProfile(predefinedProfileName as ProfileName)

    // Merge profile tools with any explicit overrides
    const tools: Record<string, boolean> = {
      ...baseProfile.tools,
      ...(options.tools ?? {}),
    }

    // Merge MCP servers (profile + explicit)
    const mcpServers: McpServers = {
      ...(customProfile?.mcpServers ?? {}),
      ...(options.mcpServers ?? {}),
    }

    let abortRequested = false
    let _sessionId: string | undefined
    let _sessionIdResolve!: (id: string) => void
    let _sessionIdReject!: (err: Error) => void
    const sessionIdPromise = new Promise<string>((resolve, reject) => {
      _sessionIdResolve = resolve
      _sessionIdReject = reject
    })

    // Helper to respond to permissions via API
    async function respondToPermission(sessionId: string, permissionId: string, response: PermissionResponse) {
      const mappedResponse = mapPermissionResponse(response)
      await client.postSessionIdPermissionsPermissionId({
        path: { id: sessionId, permissionID: permissionId },
        // Cast needed because generated types may not include all server-supported response types
        body: { response: mappedResponse as any },
      })
    }

    // Eagerly subscribe to SSE and create session
    // This allows sessionId to be available before stream() is called
    let ssePromise: ReturnType<typeof client.event.subscribe> | undefined
    let sessionPromise: ReturnType<typeof client.session.create> | undefined

    const initPromise = (async () => {
      try {
        // 0. Load env from custom profile (if configured)
        if (customProfile) {
          await customProfile.injectEnv()
        }

        // 1. Subscribe to SSE FIRST (before creating session)
        ssePromise = client.event.subscribe({
          headers: {
            Accept: "text/event-stream",
          },
        })
        const sseResult = await ssePromise

        // 2. Create session with title (and optional containerProfile)
        const defaultTitle = `SDK: ${options.prompt.slice(0, 60)}${options.prompt.length > 60 ? "..." : ""}`
        sessionPromise = client.session.create({
          body: {
            title: options.title ?? defaultTitle,
            ...(options.containerProfile ? { containerProfile: options.containerProfile } : {}),
          },
        })
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
              
              const permRequest: PermissionRequest = {
                id: perm.id,
                permissionType: perm.type,
                title: perm.title,
                metadata: perm.metadata,
              }
              
              // Yield the permission event first (for visibility/logging)
              yield {
                type: "permission",
                id: perm.id,
                permissionType: perm.type,
                title: perm.title,
                metadata: perm.metadata,
              }
              
              // Handle permission based on mode
              if (mode === "noninteractive") {
                // Non-interactive: auto-approve (server already filtered by allow/deny)
                // PIN permissions will fail since we can't provide a PIN
                if (perm.type === "pin") {
                  // Can't auto-approve PIN - reject with explanation
                  await respondToPermission(id, perm.id, { 
                    reject: "PIN verification required but running in non-interactive mode" 
                  })
                  yield { type: "permission.handled", id: perm.id, response: "reject" }
                } else {
                  // Auto-approve other permission types
                  await respondToPermission(id, perm.id, "approve")
                  yield { type: "permission.handled", id: perm.id, response: "approve" }
                }
              } else if (options.onPermission) {
                // Interactive with callback: let user decide
                try {
                  const response = await options.onPermission(permRequest)
                  await respondToPermission(id, perm.id, response)
                  yield { type: "permission.handled", id: perm.id, response }
                } catch (err) {
                  // Callback threw - reject the permission
                  const message = err instanceof Error ? err.message : "Permission callback failed"
                  await respondToPermission(id, perm.id, { reject: message })
                  yield { type: "permission.handled", id: perm.id, response: "reject" }
                }
              }
              // else: Interactive but no callback - permission stays pending (blocks)
              // User was warned at spawn() time
              
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

      async respondToPermission(permissionId: string, response: PermissionResponse): Promise<void> {
        const id = await sessionIdPromise
        await respondToPermission(id, permissionId, response)
      },
    }
  }
}
