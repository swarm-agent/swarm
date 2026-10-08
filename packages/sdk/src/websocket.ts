import type { IncomingMessage, Server as HttpServer } from 'node:http';
import type { Server as HttpsServer } from 'node:https';
import { WebSocketServer, WebSocket } from 'ws';
import type { SwarmChatNamespace, ToolStreamItem } from './chat.js';
import type { PendingPermissionRecord, SwarmPermissionsNamespace, PermissionResolver, ResolvePermissionOptions, ResolvePermissionResult } from './permissions.js';
import { isAskUserPermission, permissionId, permissionResolutionBody, PermissionResolutionError } from './permission-ui.js';
import type { SessionWatch, SessionWatchState, V3Message } from './realtime.js';

export interface SwarmWebSocketBridgeOptions {
  path?: string;
  /** Server-owned policy. Browser frames cannot enable auto approval. Questions always require a reply. */
  autoApprovePermissions?: boolean;
  socketFactory?: import('./realtime.js').RealtimeSocketFactory;
  /** Authenticate/authorize upgrades for non-loopback or multi-user applications. */
  authorize?: (request: IncomingMessage) => boolean | Promise<boolean>;
  onPermissionRequested?: (
    permission: PendingPermissionRecord, clientSocket: WebSocket,
    resolve: PermissionResolver
  ) => void | Promise<void>;
}
export interface SwarmWebSocketClientMessage {
  type: 'subscribe' | 'chat_message' | 'resolve_permission' | 'ping' | 'unsubscribe';
  sessionId?: string;
  message?: string;
  requestId?: string;
  permissionId?: string;
  action?: 'allow_once' | 'deny' | 'deny_once' | 'allow_always' | 'deny_always';
  reason?: string;
  approvedArguments?: ResolvePermissionOptions['approvedArguments'];
  /** @deprecated Configure permission policy on the server instead. */
  autoApprovePermissions?: boolean;
}
export interface SwarmWebSocketServerMessage {
  type: 'connected' | 'subscribed' | 'unsubscribed' | 'state' | 'accepted' | 'text' | 'reasoning'
    | 'tool_start' | 'tool_delta' | 'tool_done' | 'tool_permission' | 'permission_resolved' | 'permission_removed' | 'permission_error' | 'done' | 'error' | 'pong';
  sessionId?: string;
  requestId?: string;
  state?: SessionWatchState;
  delta?: string;
  fullText?: string;
  tool?: ToolStreamItem;
  permission?: PendingPermissionRecord;
  result?: ResolvePermissionResult;
  permissionId?: string;
  messages?: V3Message[];
  error?: string;
}

/** Application-owned bridge to canonical V3 realtime. A subscription survives every turn.
 * One socket selects one conversation; reconnect rehydrates that exact conversation.
 * Never expose this privileged bridge publicly without application authentication.
 */
export class SwarmWebSocketBridge {
  private readonly wss: WebSocketServer;
  private readonly activeWatches = new Map<WebSocket, { sessionId: string; watch: SessionWatch }>();

  constructor(
    server: HttpServer | HttpsServer,
    private readonly chat: SwarmChatNamespace,
    private readonly permissions: SwarmPermissionsNamespace,
    private readonly options: SwarmWebSocketBridgeOptions = {}
  ) {
    this.wss = new WebSocketServer({
      server, path: options.path ?? '/ws', maxPayload: 1024 * 1024,
      verifyClient: (info, done) => {
        // Prevent a foreign website from driving a localhost privileged bridge.
        let sameOrigin = true;
        try { if (info.origin) sameOrigin = new URL(info.origin).host === info.req.headers.host; }
        catch { sameOrigin = false; }
        if (!sameOrigin) { done(false, 403); return; }
        Promise.resolve().then(() => options.authorize?.(info.req) ?? true)
          .then(allowed => done(allowed, allowed ? undefined : 403), () => done(false, 403));
      },
    });
    this.wss.on('connection', ws => this.handleConnection(ws));
  }

  private send(ws: WebSocket, message: SwarmWebSocketServerMessage): void {
    if (ws.readyState !== WebSocket.OPEN) return;
    if (ws.bufferedAmount > 8 * 1024 * 1024) { ws.close(1013, 'Slow consumer; reconnect to rehydrate'); return; }
    ws.send(JSON.stringify(message));
  }

  private handleConnection(ws: WebSocket): void {
    this.send(ws, { type: 'connected' });
    // Ordered handling prevents subscribe/send/switch races. Bound queued work.
    let queue = Promise.resolve();
    let queued = 0;
    const permissionRequests = new Set<string>();
    ws.on('message', raw => {
      if (++queued > 64) { ws.close(1008, 'Too many pending requests'); return; }
      queue = queue.then(async () => {
        if (ws.readyState !== WebSocket.OPEN) return;
        let data: SwarmWebSocketClientMessage | undefined;
        try {
          data = JSON.parse(raw.toString()) as SwarmWebSocketClientMessage;
          if (!data || typeof data !== 'object') throw new Error('Expected a message object');
          if (data.type === 'resolve_permission') {
            const requestId = permissionId(data.requestId, 'requestId');
            if (permissionRequests.has(requestId)) throw new Error('Duplicate permission requestId; refresh before retrying');
            // Bound replay memory; close rather than evict and accidentally replay an old decision.
            if (permissionRequests.size >= 4096) { ws.close(1008, 'Reconnect to refresh permission state'); return; }
            permissionRequests.add(requestId);
          }
          await this.handleMessage(ws, data);
        } catch (error) {
          this.send(ws, { type: data?.type === 'resolve_permission' ? 'permission_error' : 'error', permissionId: data?.permissionId, sessionId: data?.sessionId, requestId: data?.requestId,
            result: error instanceof PermissionResolutionError ? error.result : undefined,
            error: error instanceof Error ? error.message : String(error) });
        }
      }).finally(() => { queued--; });
    });
    ws.on('close', () => this.cleanupSocket(ws));
    ws.on('error', () => this.cleanupSocket(ws));
  }

  private async handleMessage(ws: WebSocket, data: SwarmWebSocketClientMessage): Promise<void> {
    switch (data.type) {
      case 'ping': this.send(ws, { type: 'pong' }); return;
      case 'unsubscribe':
        this.cleanupSocket(ws);
        this.send(ws, { type: 'unsubscribed' }); return;
      case 'subscribe':
        await this.subscribeSocket(ws, permissionId(data.sessionId, 'sessionId')); return;
      case 'chat_message': {
        const sessionId = data.sessionId || this.activeWatches.get(ws)?.sessionId;
        // Never guess the newest account session or silently create one.
        if (!sessionId) throw new Error('Select or create a conversation before sending a message');
        if (typeof data.message !== 'string' || !data.message.trim()) throw new Error('message is required');
        await this.subscribeSocket(ws, sessionId);
        if (ws.readyState !== WebSocket.OPEN) return;
        await this.chat.sendMessage(sessionId, data.message, 'user', data.requestId);
        this.send(ws, { type: 'accepted', sessionId, requestId: data.requestId }); return;
      }
      case 'resolve_permission': {
        if (!data.sessionId || data.sessionId !== this.activeWatches.get(ws)?.sessionId)
          throw new Error('Permission reply must target the selected conversation');
        if (!data.permissionId || !['allow_once', 'deny', 'deny_once', 'allow_always', 'deny_always'].includes(data.action ?? ''))
          throw new Error('Valid permissionId and action are required');
        permissionResolutionBody(data.action!, { reason: data.reason, approvedArguments: data.approvedArguments });
        const result = await this.permissions.resolve(data.sessionId, data.permissionId, data.action!, { reason: data.reason, approvedArguments: data.approvedArguments });
        this.send(ws, { type: 'permission_resolved', sessionId: data.sessionId, permissionId: data.permissionId, requestId: data.requestId, result }); return;
      }
      default: throw new Error(`Unknown message type: ${data.type}`);
    }
  }

  private cleanupSocket(ws: WebSocket): void {
    this.activeWatches.get(ws)?.watch.dispose();
    this.activeWatches.delete(ws);
  }

  private async subscribeSocket(ws: WebSocket, sessionId: string): Promise<void> {
    const existing = this.activeWatches.get(ws);
    if (existing?.sessionId === sessionId) {
      await existing.watch.ready;
      this.send(ws, { type: 'subscribed', sessionId });
      return;
    }
    this.cleanupSocket(ws);
    let watch: SessionWatch;
    const send = (message: SwarmWebSocketServerMessage) => {
      if (this.activeWatches.get(ws)?.watch === watch) this.send(ws, { ...message, sessionId });
    };
    watch = await this.chat.stream(sessionId, {
      socketFactory: this.options.socketFactory,
      onState: state => send({ type: 'state', state }),
      onText: (delta, fullText) => send({ type: 'text', delta, fullText }),
      onReasoning: (delta, fullText) => send({ type: 'reasoning', delta, fullText }),
      onToolStarted: tool => send({ type: 'tool_start', tool }),
      onToolDelta: (tool, delta) => send({ type: 'tool_delta', tool, delta }),
      onToolCompleted: tool => send({ type: 'tool_done', tool }),
      onPermissionRequested: async (permission, resolve) => {
        if (this.activeWatches.get(ws)?.watch !== watch) return;
        if (this.options.autoApprovePermissions && !isAskUserPermission(permission)) {
          await resolve('allow_once', 'Approved by application server policy');
        } else if (this.options.onPermissionRequested) {
          await this.options.onPermissionRequested(permission, ws, resolve);
        } else send({ type: 'tool_permission', permission });
      },
      onPermissionRemoved: permissionId => send({ type: 'permission_removed', permissionId }),
      onPermissionError: (error, permission) => send({ type: 'permission_error', permissionId: permission.id, error: error.message }),
      onComplete: messages => send({ type: 'done', messages }),
      onError: error => {
        send({ type: 'error', error: error.message });
        if (this.activeWatches.get(ws)?.watch === watch) {
          this.cleanupSocket(ws);
          ws.close(1011, 'Conversation stream unavailable; reconnect');
        }
      },
    });
    this.activeWatches.set(ws, { sessionId, watch });
    // A failed watch must not leave an apparently subscribed but dead socket.
    void watch.done.catch(() => {}).finally(() => {
      if (this.activeWatches.get(ws)?.watch === watch) this.activeWatches.delete(ws);
    });
    try {
      await watch.ready;
      if (ws.readyState !== WebSocket.OPEN) { this.cleanupSocket(ws); return; }
      send({ type: 'subscribed' });
    } catch (error) {
      if (this.activeWatches.get(ws)?.watch === watch) this.cleanupSocket(ws);
      throw error;
    }
  }

  async close(): Promise<void> {
    for (const ws of this.wss.clients) { this.cleanupSocket(ws); ws.terminate(); }
    await new Promise<void>(resolve => this.wss.close(() => resolve()));
  }
}
export function createSwarmWebSocketBridge(
  server: HttpServer | HttpsServer, chat: SwarmChatNamespace,
  permissions: SwarmPermissionsNamespace, options: SwarmWebSocketBridgeOptions = {}
): SwarmWebSocketBridge {
  return new SwarmWebSocketBridge(server, chat, permissions, options);
}
