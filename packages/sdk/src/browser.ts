import type { ToolStreamItem } from './chat.js';
import type { PendingPermissionRecord, ResolvePermissionResult } from './permissions.js';
import { confirmPermissionResolution, permissionId as validatePermissionId, permissionResolutionBody, PermissionResolutionError } from './permission-ui.js';
import type { PermissionAction, ResolvePermissionOptions } from './permission-ui.js';
export * from './permission-ui.js';
import type { SessionWatchState, V3Message } from './realtime.js';
import type { SwarmWebSocketClientMessage, SwarmWebSocketServerMessage } from './websocket.js';

export interface SwarmBrowserChatOptions {
  /** WebSocket URL, e.g. ws://localhost:3456/ws. Defaults to ws(s)://${location.host}/ws */
  url?: string;
  /** Whether to automatically connect on construction (default: true) */
  autoConnect?: boolean;
  /** Whether to automatically reconnect on drop (default: true) */
  autoReconnect?: boolean;
  /** Max reconnection attempts before giving up (default: 10) */
  maxReconnectAttempts?: number;
  /** Bounds subscription acknowledgement waits (default 45 seconds). */
  subscribeTimeoutMs?: number;
  /** Lost acknowledgements reject with an uncertain outcome; decisions are never replayed. */
  permissionTimeoutMs?: number;
  /** Optional custom WebSocket constructor (useful in Node.js test environments) */
  webSocketClass?: any;
}

type EventMap = {
  state: (state: SessionWatchState) => void;
  accepted: (requestId: string) => void;
  connected: () => void;
  disconnected: () => void;
  subscribed: (sessionId: string) => void;
  unsubscribed: () => void;
  text: (delta: string, fullText: string) => void;
  reasoning: (delta: string, fullText: string) => void;
  tool_start: (tool: ToolStreamItem) => void;
  tool_delta: (tool: ToolStreamItem, delta: string) => void;
  tool_done: (tool: ToolStreamItem) => void;
  tool_permission: (permission: PendingPermissionRecord) => void;
  permission_resolved: (permissionId: string, result: ResolvePermissionResult) => void;
  permission_removed: (permissionId: string) => void;
  permission_error: (permissionId: string, error: Error) => void;
  permissions: (pending: PendingPermissionRecord[]) => void;
  done: (messages: V3Message[]) => void;
  error: (error: string) => void;
};

/**
 * Universal browser-side WebSocket client for Swarm.
 * Hooks directly into the Swarm WebSocket Bridge with typed callbacks for live
 * assistant tokens, model reasoning deltas, real-time tool events, and permission approvals.
 */
export class SwarmBrowserChat {
  private ws: any = null;
  private currentSessionId: string | null = null;
  private isDisposed = false;
  private reconnectAttempts = 0;
  private reconnectTimer: ReturnType<typeof setTimeout> | undefined;
  private subscribedSessionId: string | null = null;
  private permissionsLive = false;
  private cancelSubscription?: (reason: string) => void;
  private connectPromise: Promise<void> | null = null;
  private readonly listeners = new Map<keyof EventMap, Set<Function>>();
  private readonly pendingPermissions = new Map<string, PendingPermissionRecord>();
  private readonly decisions = new Map<string, {
    permissionId: string; sessionId: string; action: PermissionAction; options: ResolvePermissionOptions;
    resolve: (result: ResolvePermissionResult) => void; reject: (error: Error) => void;
    timer: ReturnType<typeof setTimeout>;
  }>();
  get permissions(): PendingPermissionRecord[] { return [...this.pendingPermissions.values()]; }

  private rejectDecisions(message: string): void {
    for (const [key, decision] of this.decisions) {
      clearTimeout(decision.timer); this.decisions.delete(key);
      const error = new Error(message);
      decision.reject(error); this.emit('permission_error', decision.permissionId, error);
    }
  }
  private removePermission(id: string): void {
    if (this.pendingPermissions.delete(id)) {
      this.emit('permission_removed', id);
      this.emit('permissions', this.permissions);
    }
  }

  constructor(private readonly options: SwarmBrowserChatOptions = {}) {
    if (options.autoConnect !== false) {
      void this.connect().catch(() => {});
    }
  }

  get sessionId(): string | null { return this.currentSessionId; }
  get isSubscribed(): boolean { return this.isConnected && !!this.currentSessionId && this.subscribedSessionId === this.currentSessionId; }

  get isConnected(): boolean {
    return this.ws !== null && this.ws.readyState === 1; // 1 = OPEN
  }

  private resolveUrl(): string {
    if (this.options.url) return this.options.url;
    if (typeof location !== 'undefined') {
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      return `${proto}//${location.host}/ws`;
    }
    return 'ws://localhost:3456/ws';
  }

  /**
   * Connects to the Swarm WebSocket Bridge. Returns a Promise that resolves when connected.
   */
  async connect(): Promise<void> {
    if (this.isDisposed) throw new Error('Chat client disposed');
    if (this.isConnected) return;
    if (this.connectPromise) return this.connectPromise;

    this.connectPromise = new Promise<void>((resolve, reject) => {
      try {
        const targetUrl = this.resolveUrl();
        const WsClass = this.options.webSocketClass || (typeof WebSocket !== 'undefined' ? WebSocket : null);
        if (!WsClass) {
          throw new Error('No WebSocket implementation available. In Node.js environments, pass webSocketClass in options.');
        }

        const socket = new WsClass(targetUrl);
        this.ws = socket;

        let hasOpened = false;

        socket.onopen = () => {
          hasOpened = true;
          this.reconnectAttempts = 0;
          if (this.currentSessionId) {
            this.send({ type: 'subscribe', sessionId: this.currentSessionId });
          }
          resolve();
        };

        socket.onmessage = (event: any) => {
          if (this.ws !== socket) return;
          try {
            const raw = typeof event.data === 'string' ? event.data : event.data?.toString('utf8');
            const msg = JSON.parse(raw) as SwarmWebSocketServerMessage;
            this.handleServerMessage(msg);
          } catch (error) { this.emit('error', error instanceof Error ? error.message : 'Malformed server frame'); }
        };

        socket.onclose = () => {
          if (this.ws !== socket) return;
          this.ws = null;
          this.subscribedSessionId = null;
          this.permissionsLive = false;
          this.rejectDecisions('Disconnected before permission acknowledgement; outcome unknown. Reconnect and review pending requests before retrying.');
          this.emit('disconnected');
          if (!hasOpened) {
            reject(new Error('WebSocket closed before opening'));
          }
          this.maybeReconnect();
        };

        socket.onerror = (err: any) => {
          this.emit('disconnected');
          if (!hasOpened) {
            reject(err instanceof Error ? err : new Error('WebSocket connection error'));
          }
        };
      } catch (err: any) {
        this.emit('error', err.message);
        this.maybeReconnect();
        reject(err);
      }
    }).finally(() => {
      this.connectPromise = null;
    });

    return this.connectPromise;
  }

  private maybeReconnect(): void {
    if (this.isDisposed || this.options.autoReconnect === false || this.reconnectTimer) return;
    const max = this.options.maxReconnectAttempts ?? 10;
    if (this.reconnectAttempts >= max) return;

    const delay = Math.min(500 * Math.pow(1.5, this.reconnectAttempts++), 10_000);
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = undefined;
      if (!this.isDisposed) {
        void this.connect().catch(() => {});
      }
    }, delay);
  }

  private handleServerMessage(msg: SwarmWebSocketServerMessage): void {
    if (msg.sessionId && msg.sessionId !== this.currentSessionId) return;
    switch (msg.type) {
      case 'connected':
        this.emit('connected');
        break;
      case 'state':
        if (msg.state) {
          this.permissionsLive = msg.state.status === 'live';
          const rawPending = msg.state.snapshot.session_views_by_id?.[this.currentSessionId!]?.pending_permissions;
          const pending = rawPending === null ? [] : rawPending;
          if (this.permissionsLive && Array.isArray(pending)) {
            if (pending.some(p => !p || typeof p.id !== 'string' || !p.id || p.session_id !== this.currentSessionId)) {
              this.permissionsLive = false;
              throw new Error('Invalid pending permission snapshot; refresh the conversation');
            }
            const records = pending.filter(p => p && p.session_id === this.currentSessionId && p.status === 'pending') as PendingPermissionRecord[];
            const ids = new Set(records.map(p => p.id));
            for (const id of this.pendingPermissions.keys()) if (!ids.has(id)) this.removePermission(id);
            for (const p of records) this.pendingPermissions.set(p.id, p);
            this.emit('permissions', this.permissions);
          }
          this.emit('state', msg.state);
        }
        break;
      case 'accepted':
        this.emit('accepted', msg.requestId ?? '');
        break;
      case 'subscribed':
        if (msg.sessionId) {
          this.subscribedSessionId = msg.sessionId;
          this.emit('subscribed', msg.sessionId);
        }
        break;
      case 'unsubscribed':
        this.emit('unsubscribed');
        break;
      case 'text':
        this.emit('text', msg.delta || '', msg.fullText || '');
        break;
      case 'reasoning':
        this.emit('reasoning', msg.delta || '', msg.fullText || '');
        break;
      case 'tool_start':
        if (msg.tool) this.emit('tool_start', msg.tool);
        break;
      case 'tool_delta':
        if (msg.tool) this.emit('tool_delta', msg.tool, msg.delta || '');
        break;
      case 'tool_done':
        if (msg.tool) this.emit('tool_done', msg.tool);
        break;
      case 'tool_permission':
        if (msg.permission && msg.permission.session_id === this.currentSessionId && msg.permission.status === 'pending') {
          this.pendingPermissions.set(msg.permission.id, msg.permission);
          this.emit('tool_permission', msg.permission);
          this.emit('permissions', this.permissions);
        }
        break;
      case 'permission_resolved': {
        const decision = msg.requestId ? this.decisions.get(msg.requestId) : undefined;
        if (!decision || msg.sessionId !== decision.sessionId || msg.permissionId !== decision.permissionId) break;
        clearTimeout(decision.timer); this.decisions.delete(msg.requestId!);
        try {
          const result = confirmPermissionResolution(msg.result!, decision.sessionId, decision.permissionId, decision.action, decision.options);
          this.removePermission(decision.permissionId);
          decision.resolve(result);
          this.emit('permission_resolved', decision.permissionId, result);
        } catch (error) {
          const err = error instanceof Error ? error : new Error(String(error));
          decision.reject(err); this.emit('permission_error', decision.permissionId, err);
        }
        break;
      }
      case 'permission_removed':
        if (msg.permissionId) this.removePermission(msg.permissionId);
        break;
      case 'permission_error': {
        const decision = msg.requestId ? this.decisions.get(msg.requestId) : undefined;
        const result = msg.result?.session_id === this.currentSessionId && msg.result?.permission?.id === msg.permissionId ? msg.result : undefined;
        const error = new PermissionResolutionError(msg.error || 'Permission resolution failed; refresh before retrying', result);
        if (decision && msg.sessionId === decision.sessionId && msg.permissionId === decision.permissionId) {
          clearTimeout(decision.timer); this.decisions.delete(msg.requestId!); decision.reject(error);
        }
        this.emit('permission_error', msg.permissionId ?? '', error);
        break;
      }
      case 'done':
        this.emit('done', msg.messages || []);
        break;
      case 'error':
        this.emit('error', msg.error || 'Unknown error');
        break;
    }
  }

  private send(msg: SwarmWebSocketClientMessage): void {
    if (!this.isConnected) throw new Error('Chat disconnected; reconnect and subscribe before sending');
    this.ws.send(JSON.stringify(msg));
  }

  /**
   * Subscribes to real-time events for a specific session.
   * Returns a promise that resolves once the session subscription is acknowledged.
   */
  async subscribe(sessionId: string, autoApprovePermissions?: boolean): Promise<string> {
    if (typeof sessionId !== 'string' || !sessionId.trim()) {
      throw new Error('sessionId must be a non-empty string; create or select a conversation before subscribing');
    }
    sessionId = sessionId.trim();
    this.cancelSubscription?.('Subscription superseded');
    this.rejectDecisions('Subscription changed before permission acknowledgement; outcome unknown');
    if (this.currentSessionId !== sessionId) { this.permissionsLive = false; this.pendingPermissions.clear(); this.emit('permissions', []); }
    this.currentSessionId = sessionId;
    this.subscribedSessionId = null;
    await this.connect();
    if (this.currentSessionId !== sessionId) throw new Error('Subscription superseded');
    return new Promise<string>((resolve, reject) => {
      const cleanup = () => { clearTimeout(timer); unsub(); offError(); offClose(); this.cancelSubscription = undefined; };
      const fail = (error: string) => { cleanup(); reject(new Error(error)); };
      const unsub = this.on('subscribed', (subId) => {
        if (subId === sessionId) { cleanup(); resolve(subId); }
      });
      const offError = this.on('error', fail);
      const offClose = this.on('disconnected', () => fail('Disconnected before subscription acknowledgement'));
      this.cancelSubscription = fail;
      const timer = setTimeout(() => fail('Subscription acknowledgement timed out'), this.options.subscribeTimeoutMs ?? 45_000);
      try { this.send({ type: 'subscribe', sessionId, autoApprovePermissions }); }
      catch (error) { fail(error instanceof Error ? error.message : String(error)); }
    });
  }

  /** Unsubscribes from active session streaming */
  unsubscribe(): void {
    this.cancelSubscription?.('Unsubscribed');
    this.rejectDecisions('Unsubscribed before permission acknowledgement; outcome unknown');
    this.pendingPermissions.clear(); this.emit('permissions', []);
    this.currentSessionId = null;
    this.subscribedSessionId = null;
    if (this.isConnected) this.send({ type: 'unsubscribe' });
  }

  /** Sends a chat message over the WebSocket */
  sendMessage(message: string, sessionId?: string, autoApprovePermissions?: boolean, requestId?: string): string {
    const targetSession = sessionId || this.currentSessionId;
    if (!targetSession) {
      throw new Error('No active session. Call subscribe(sessionId) or provide sessionId');
    }
    if (!this.isSubscribed || targetSession !== this.currentSessionId) throw new Error('Await subscribe(sessionId) before sending');
    const id = requestId ?? globalThis.crypto.randomUUID();
    this.send({
      type: 'chat_message',
      requestId: id,
      sessionId: targetSession,
      message,
      autoApprovePermissions,
    });
    return id;
  }

  /** Resolve only after a user gesture. Await confirmation; on failure keep the form recoverable. */
  async resolvePermission(
    permissionId: string,
    action: PermissionAction,
    options: ResolvePermissionOptions & { sessionId?: string } = {}
  ): Promise<ResolvePermissionResult> {
    permissionId = validatePermissionId(permissionId);
    permissionResolutionBody(action, options);
    const targetSession = options.sessionId === undefined ? this.currentSessionId : validatePermissionId(options.sessionId, 'sessionId');
    if (!this.isSubscribed || !targetSession || targetSession !== this.currentSessionId) throw new Error('Await subscribe(sessionId) before resolving permissions');
    if (!this.permissionsLive) throw new Error('Permission state is reconnecting; wait for fresh hydration');
    if (!this.pendingPermissions.has(permissionId)) throw new Error('Permission is not pending in this conversation; refresh before replying');
    if ([...this.decisions.values()].some(d => d.permissionId === permissionId)) throw new Error('Permission resolution already in flight');
    const requestId = globalThis.crypto.randomUUID();
    return new Promise<ResolvePermissionResult>((resolve, reject) => {
      const timer = setTimeout(() => {
        this.decisions.delete(requestId);
        this.permissionsLive = false;
        const error = new Error('Permission acknowledgement timed out; outcome unknown. Refresh before retrying.');
        reject(error); this.emit('permission_error', permissionId, error);
      }, this.options.permissionTimeoutMs ?? 45_000);
      this.decisions.set(requestId, { permissionId, sessionId: targetSession, action, options, resolve, reject, timer });
      try { this.send({ type: 'resolve_permission', sessionId: targetSession, permissionId, action, requestId,
        reason: options.reason, approvedArguments: options.approvedArguments }); }
      catch (error) { clearTimeout(timer); this.decisions.delete(requestId); reject(error); }
    });
  }

  on<K extends keyof EventMap>(event: K, handler: EventMap[K]): () => void {
    if (!this.listeners.has(event)) {
      this.listeners.set(event, new Set());
    }
    this.listeners.get(event)!.add(handler);

    // If already connected and listener is for connected, fire immediately
    if (event === 'connected' && this.isConnected) {
      setTimeout(() => (handler as any)(), 0);
    }

    return () => this.off(event, handler);
  }

  off<K extends keyof EventMap>(event: K, handler: EventMap[K]): void {
    const handlers = this.listeners.get(event);
    if (handlers) handlers.delete(handler);
  }

  private emit<K extends keyof EventMap>(event: K, ...args: Parameters<EventMap[K]>): void {
    const handlers = this.listeners.get(event);
    if (handlers) {
      for (const h of handlers) {
        try {
          (h as any)(...args);
        } catch {}
      }
    }
  }

  /** Closes and disposes the WebSocket connection */
  dispose(): void {
    this.isDisposed = true;
    this.rejectDecisions('Client disposed before permission acknowledgement; outcome unknown');
    clearTimeout(this.reconnectTimer);
    this.emit('disconnected');
    if (this.ws) {
      try {
        if (typeof this.ws.terminate === 'function') {
          this.ws.terminate();
        } else {
          this.ws.close();
        }
      } catch {}
      this.ws = null;
    }
    this.listeners.clear();
  }
}
