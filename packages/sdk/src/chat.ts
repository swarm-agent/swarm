import type { PendingPermissionRecord } from './permissions.js';
import { SwarmPermissionsNamespace, isAskUserPermission, permissionId } from './permissions.js';
import type { PermissionResolver } from './permissions.js';
import type { SessionWatch, SessionWatchState, V3Event, V3Message, V3RunState } from './realtime.js';
import { SwarmRealtimeNamespace } from './realtime.js';
import { SwarmSessionsNamespace } from './sessions.js';
import type { SwarmTransport } from './transport.js';
import type { SessionDetail, SessionRecord } from './types.js';
import { SwarmTimeoutError } from './errors.js';
import { SwarmWebSocketBridge, type SwarmWebSocketBridgeOptions } from './websocket.js';

export interface CreateChatSessionOptions {
  /** Descriptive title for this conversation */
  title?: string;
  /** Agent profile name (defaults to 'swarm') */
  agent_name?: string;
  /** Workspace filesystem path */
  workspace_path?: string;
  /** Workspace catalog identity */
  workspace_id?: string;
  /** Project identity if linked to a project */
  project_id?: string;
  /**
   * Mode:
   * - 'auto' (default): Autonomous execution with tool capabilities.
   * - 'plan': Plan-mode requiring explicit checkpoint plan submission.
   */
  mode?: 'auto' | 'plan';
}

export interface ToolStreamItem {
  id: string;
  name: string;
  identity?: string;
  display?: string;
  arguments?: string;
  output?: string;
  status: 'running' | 'completed' | 'failed' | 'waiting_approval' | 'cancelled';
  error?: string;
  durationMs?: number;
}

export interface ChatRunOptions {
  /** User message to send */
  message: string;
  /** Max milliseconds to wait for response (default: 180,000ms / 3min) */
  timeoutMs?: number;
  /** Polling interval in ms (default: 1,000ms) */
  pollIntervalMs?: number;
  /**
   * Explicit opt-in policy for ordinary tools only. Ask-user always needs a human answer.
   * Prefer stream() and an explicit permission UI.
   */
  autoApprovePermissions?: boolean;
  /** Optional callback invoked whenever a tool execution starts or completes */
  onToolEvent?: (event: ToolStreamItem) => void;
  /** Optional AbortSignal to cancel waiting */
  signal?: AbortSignal;
}

export interface ChatRunResult {
  /** The final text response from the assistant */
  reply: string;
  /** Target session ID */
  sessionId: string;
  /** Active or completed run ID */
  runId?: string;
  /** All messages in the session */
  messages: V3Message[];
  /** Any permissions that were requested during this run */
  pendingPermissions?: PendingPermissionRecord[];
  /** Full session detail */
  session: SessionDetail;
}

export interface ChatStreamOptions {
  signal?: AbortSignal;
  surface?: string;
  socketFactory?: import('./realtime.js').RealtimeSocketFactory;
  /** Authoritative conversation snapshot on hydrate, live change and reconnect. */
  onState?: (state: SessionWatchState) => void;
  /**
   * Incremental and cumulative assistant text per run/stream. Concatenating `delta` values
   * yields exactly `fullText`, including across reconnects (never repeated, never skipped);
   * a turn's final call carries any committed text beyond what streamed.
   */
  onText?: (delta: string, fullText: string) => void;
  /** Incremental and cumulative reasoning text from durable reasoning checkpoints. */
  onReasoning?: (delta: string, fullText: string) => void;
  /** Invoked when a tool call starts running */
  onToolStarted?: (tool: ToolStreamItem) => void;
  /** Invoked with streamed incremental output from a running tool */
  onToolDelta?: (tool: ToolStreamItem, delta: string) => void;
  /** Invoked when a tool call finishes or fails */
  onToolCompleted?: (tool: ToolStreamItem) => void;
  /** Invoked when a tool requires approval before execution */
  onPermissionRequested?: (
    permission: PendingPermissionRecord,
    resolver: PermissionResolver
  ) => void | Promise<void>;
  /** Request no longer pending in a durable snapshot; this does not imply approval. */
  onPermissionRemoved?: (permissionId: string) => void;
  /** Recoverable UI/resolution failure; the watch stays open. Resolver also rejects. */
  onPermissionError?: (error: Error, permission: PendingPermissionRecord) => void;
  /**
   * Once per finished run, including failed, cancelled and empty turns. `run.status`
   * says which (`completed`, `failed`, ...); a failure's reason is in `run.blocked_reason`
   * and its system message in `messages`. The watch stays open for the next turn.
   */
  onComplete?: (messages: V3Message[], run: V3RunState) => void;
  /** Invoked if an unrecoverable streaming error occurs */
  onError?: (err: Error) => void;
}

/**
 * Dedicated high-level Chat namespace.
 * Provides streamlined building blocks for interactive AI chat, streaming responses,
 * live tool call monitoring, and automatic or custom permission handling.
 */
export class SwarmChatNamespace {
  private readonly sessions: SwarmSessionsNamespace;
  private readonly permissions: SwarmPermissionsNamespace;
  private readonly realtime: SwarmRealtimeNamespace;

  constructor(private readonly transport: SwarmTransport) {
    this.sessions = new SwarmSessionsNamespace(transport);
    this.permissions = new SwarmPermissionsNamespace(transport);
    this.realtime = new SwarmRealtimeNamespace(transport);
  }

  /**
   * Attaches a native bidirectional WebSocket bridge to an HTTP or HTTPS server.
   * Enables browsers to stream tokens, live tool events, and send messages directly over WebSockets.
   */
  attachWebSocket(
    server: import('node:http').Server | import('node:https').Server,
    options?: SwarmWebSocketBridgeOptions
  ): SwarmWebSocketBridge {
    return new SwarmWebSocketBridge(server, this, this.permissions, options);
  }

  /**
   * Creates an interactive chat session.
   * Standalone workspace chat uses Swarm. For project orchestration use
   * projects.createConversation(projectId); project membership is not workspace authority.
   */
  async createSession(options: CreateChatSessionOptions = {}): Promise<SessionRecord> {
    if (options.project_id) throw new Error('Use projects.createConversation(projectId) for project orchestrator conversations');
    return this.sessions.create({
      title: options.title ?? 'Chat Session',
      agent_name: options.agent_name ?? 'swarm',
      workspace_path: options.workspace_path,
      workspace_id: options.workspace_id,
      project_id: options.project_id,
      mode: options.mode ?? 'auto',
    });
  }

  /**
   * Appends a message to a chat conversation.
   */
  async sendMessage(sessionId: string, content: string, role: 'user' = 'user', clientRequestId?: string): Promise<any> {
    return this.sessions.sendMessage(sessionId, { content, role, client_request_id: clientRequestId });
  }

  /**
   * Lists messages from a session conversation.
   */
  async listMessages(sessionId: string): Promise<V3Message[]> {
    const detail = await this.sessions.get(sessionId);
    return (detail.messages as V3Message[]) || [];
  }

  /**
   * High-level chat execution: sends a message, monitors execution, handles or surfaces
   * pending permissions, and returns the final assistant reply.
   */
  async run(sessionId: string, options: ChatRunOptions): Promise<ChatRunResult> {
    const id = permissionId(sessionId, 'sessionId');
    await this.sessions.sendMessage(id, { content: options.message, role: 'user' });

    const timeoutMs = options.timeoutMs ?? 180_000;
    const pollIntervalMs = options.pollIntervalMs ?? 1_000;
    const deadline = Date.now() + timeoutMs;
    let lastPendingCount = 0;

    while (Date.now() < deadline) {
      if (options.signal?.aborted) {
        throw new Error('Chat run aborted by caller');
      }

      // Check for pending permissions
      const pending = await this.permissions.listSessionPending(id);

      if (pending.length > 0) {
        lastPendingCount = pending.length;
        if (options.autoApprovePermissions) {
          for (const perm of pending) {
            if (isAskUserPermission(perm)) continue;
            await this.permissions.resolve(id, perm.id, 'allow_once', { reason: 'Approved by explicit application policy' });
          }
        }
      }

      const detail = await this.sessions.get(id);
      const raw = (detail.raw || {}) as Record<string, any>;
      const activeRun = raw.active_run_intent;
      const state = (detail.state || '').toLowerCase();
      const isRunning = state.includes('running') || state.includes('in_progress') || !!activeRun;

      if (!isRunning && pending.length === 0) {
        const msgs = (detail.messages as V3Message[]) || [];
        const assistantMsgs = msgs.filter((m) => m.role === 'assistant');
        const finalMsg = assistantMsgs[assistantMsgs.length - 1];
        let reply = finalMsg?.content;

        if (!reply) {
          const sysMsg = msgs.find(
            (m) =>
              m.role === 'system' &&
              (m.metadata?.message_kind === 'run_failure' || m.content?.includes('[run-failed]'))
          );
          reply = sysMsg ? sysMsg.content : 'No response generated from model.';
        }

        return {
          reply,
          sessionId: id,
          runId: activeRun?.run_id || detail.last_run_id,
          messages: msgs,
          pendingPermissions: pending,
          session: detail,
        };
      }

      await new Promise((r) => setTimeout(r, pollIntervalMs));
    }

    let errorMsg = `Session run did not complete within ${timeoutMs}ms.`;
    if (lastPendingCount > 0) {
      errorMsg += ` Execution is paused waiting for ${lastPendingCount} pending tool permission(s). Render the pending requests and explicitly answer or deny via client.permissions.resolve().`;
    }
    throw new SwarmTimeoutError(errorMsg, timeoutMs);
  }

  /**
   * Establishes a real-time event stream for a chat session.
   * Streams text deltas, reasoning deltas, and live tool start/output/completion events like on Swarm.
   */
  async stream(sessionId: string, options: ChatStreamOptions = {}): Promise<SessionWatch> {
    const id = permissionId(sessionId, 'sessionId');
    const activeTools = new Map<string, ToolStreamItem>();
    const seenEventIds = new Set<string>();
    // Text already reported through onText per run/stream: a high-water mark, never shrunk
    // by a reconnect, so deltas are neither repeated nor lost across reconnects.
    const reportedText = new Map<string, { runId: string; streamId: string; text: string }>();
    const reasoning = new Map<string, string>();
    const completedRuns = new Set<string>();
    const pendingSeen = new Map<string, string>();
    const resolving = new Set<string>();
    const resolved = new Set<string>();
    let permissionsLive = false;
    let initialized = false;
    let observedRun: string | undefined;

    // Only extensions are reported: a shorter text (reconnect before durable catch-up) or a
    // committed message that differs from the streamed prefix is never re-sent as a delta.
    const reportText = (key: string, runId: string, streamId: string, text: string) => {
      const previous = reportedText.get(key)?.text ?? '';
      if (text.length <= previous.length || !text.startsWith(previous)) return;
      reportedText.set(key, { runId, streamId, text });
      observedRun = runId;
      options.onText?.(text.slice(previous.length), text);
    };

    // Applies one durable event. Hydrated history is applied silently: it is neither a new
    // turn nor a new tool call, but running tools and partial reasoning must still be known.
    const applyEvent = (evt: V3Event, notify: boolean) => {
      const payload = evt.payload || {};
      const eventType = evt.event_type;
      const toolId = String(payload.call_id || payload.tool_instance_id || '');
      if (eventType === 'session.tool.started') {
        const toolItem: ToolStreamItem = {
          id: toolId || evt.id,
          name: String(payload.tool_name || 'tool'),
          identity: payload.tool_identity ? String(payload.tool_identity) : undefined,
          display: payload.tool_display ? String(payload.tool_display) : undefined,
          arguments: payload.arguments ? String(payload.arguments) : undefined,
          status: 'running',
        };
        activeTools.set(toolItem.id, toolItem);
        if (notify) options.onToolStarted?.(toolItem);
      } else if (eventType === 'session.tool.delta') {
        const toolItem = activeTools.get(toolId);
        const delta = String(payload.output || '');
        if (toolItem && delta) {
          toolItem.output = (toolItem.output || '') + delta;
          if (notify) options.onToolDelta?.(toolItem, delta);
        }
      } else if (TERMINAL_TOOL_STATUS[eventType]) {
        let toolItem = activeTools.get(toolId);
        if (!toolItem) {
          toolItem = { id: toolId, name: String(payload.tool_name || 'tool'), status: TERMINAL_TOOL_STATUS[eventType] };
        }
        activeTools.delete(toolId);
        toolItem.status = TERMINAL_TOOL_STATUS[eventType];
        if (payload.output) toolItem.output = String(payload.output);
        if (toolItem.status === 'failed') toolItem.error = String(payload.error || 'Tool execution failed');
        else if (payload.error) toolItem.error = String(payload.error);
        if (payload.duration_ms) toolItem.durationMs = Number(payload.duration_ms);
        if (notify) options.onToolCompleted?.(toolItem);
      } else if (eventType === 'session.reasoning.delta' && typeof payload.delta === 'string') {
        const key = JSON.stringify([payload.run_id, payload.reasoning_id ?? payload.reasoning_key]);
        const previous = reasoning.get(key) ?? '';
        const full = payload.delta_mode === 'replace' ? payload.delta : previous + payload.delta;
        if (full === previous) return;
        reasoning.set(key, full);
        if (reasoning.size > 64) reasoning.delete(reasoning.keys().next().value!);
        if (notify) options.onReasoning?.(full.startsWith(previous) ? full.slice(previous.length) : full, full);
      }
    };

    return this.realtime.watchSession(id, {
      signal: options.signal,
      surface: options.surface ?? 'chat',
      socketFactory: options.socketFactory,
      onChange: (state) => {
        permissionsLive = state.status === 'live';
        options.onState?.(state);
        const run = state.snapshot.current_run_state_by_session?.[id]
          ?? state.snapshot.session_views_by_id?.[id]?.current_run_state;
        const sessionEvents = state.snapshot.events_by_session?.[id] || [];
        if (!initialized) {
          initialized = true;
          for (const evt of sessionEvents) { seenEventIds.add(evt.id); applyEvent(evt, false); }
          if (run?.run_id && !run.active) completedRuns.add(run.run_id);
        }
        if (run?.active) observedRun = run.run_id;

        // 1. Assistant text: live text per run/stream, then the committed message's tail.
        const liveKeys = new Set<string>();
        for (const item of state.live || []) {
          const key = JSON.stringify([item.runId, item.streamId]);
          liveKeys.add(key);
          reportText(key, item.runId, item.streamId, item.text);
        }
        for (const [key, entry] of reportedText) {
          if (liveKeys.has(key)) continue;
          const committed = state.messages.find(m => m.role === 'assistant'
            && m.metadata?.run_id === entry.runId && m.metadata?.stream_id === entry.streamId);
          if (committed && typeof committed.content === 'string') {
            reportText(key, entry.runId, entry.streamId, committed.content);
            reportedText.delete(key);
          } else if (state.status === 'live' && !(run?.active && run.run_id === entry.runId)) {
            reportedText.delete(key);
          }
        }

        // 2. Durable events (tools, reasoning), each exactly once and in sequence order.
        for (const evt of sessionEvents) {
          if (seenEventIds.has(evt.id)) continue;
          seenEventIds.add(evt.id);
          applyEvent(evt, true);
          if (seenEventIds.size > 4096) seenEventIds.delete(seenEventIds.values().next().value!);
        }

        const rawPending = state.snapshot.session_views_by_id?.[id]?.pending_permissions;
        const pending = rawPending === null ? [] : rawPending;
        if (permissionsLive && Array.isArray(pending)) {
          if (pending.some(p => !p || typeof p.id !== 'string' || !p.id || p.session_id !== id)) {
            options.onError?.(new Error('Invalid pending permission snapshot; refresh the conversation'));
            return;
          }
          const pendingIds = new Set(pending.map(p => p.id));
          for (const key of pendingSeen.keys()) if (!pendingIds.has(key)) {
            pendingSeen.delete(key);
            resolved.delete(key);
            options.onPermissionRemoved?.(key);
          }
          for (const raw of pending) {
            const permission = raw as PendingPermissionRecord;
            if (permission.session_id !== id || permission.status !== 'pending') continue;
            const fingerprint = JSON.stringify(permission);
            if (pendingSeen.get(permission.id) === fingerprint || resolved.has(permission.id)) continue;
            pendingSeen.set(permission.id, fingerprint);
            const notify = (error: unknown) => {
              const err = error instanceof Error ? error : new Error(String(error));
              if (options.onPermissionError) options.onPermissionError(err, permission);
              else options.onError?.(err);
              return err;
            };
            const resolver: PermissionResolver = async (action, input = {}) => {
              if (!permissionsLive) throw new Error('Permission state is reconnecting; wait for fresh hydration');
              if (pendingSeen.get(permission.id) !== fingerprint || resolved.has(permission.id)) throw new Error('Permission request is stale; refresh the conversation');
              if (resolving.has(permission.id)) throw new Error('Permission resolution already in flight');
              resolving.add(permission.id);
              try {
                const result = await this.permissions.resolve(id, permission.id, action, typeof input === 'string' ? { reason: input } : input);
                resolved.add(permission.id);
                return result;
              } catch (error) { throw notify(error); }
              finally { resolving.delete(permission.id); }
            };
            // Catch both synchronous throws and async callback rejections without closing the watch.
            void Promise.resolve().then(() => options.onPermissionRequested?.(permission, resolver)).catch(notify);
          }
        }
        // Once per durable run, including empty/tool-only/failed turns. Never close the watch.
        if (state.status === 'live' && run?.run_id && !run.active && !completedRuns.has(run.run_id)) {
          completedRuns.add(run.run_id);
          if (completedRuns.size > 4096) completedRuns.delete(completedRuns.values().next().value!);
          if (observedRun === run.run_id || run.completed_at) options.onComplete?.(state.messages, run);
        }
      },
      onError: (err) => {
        options.onError?.(err);
      },
    });
  }
}

const TERMINAL_TOOL_STATUS: Record<string, ToolStreamItem['status']> = {
  'session.tool.completed': 'completed',
  'session.tool.failed': 'failed',
  'session.tool.cancelled': 'cancelled',
  'session.tool.canceled': 'cancelled',
};
