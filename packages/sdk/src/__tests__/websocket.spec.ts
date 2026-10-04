import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import { WebSocket } from 'ws';
import { SwarmBrowserChat } from '../browser.js';
import { SwarmChatNamespace } from '../chat.js';
import { SwarmPermissionsNamespace } from '../permissions.js';
import type { RealtimeSocket, V3SyncSnapshot } from '../realtime.js';
import { SwarmTransport } from '../transport.js';
import { SwarmWebSocketBridge } from '../websocket.js';

class MockSocket implements RealtimeSocket {
  sent: string[] = [];
  closed = false;
  listeners = new Map<string, ((event: { data: unknown }) => void)[]>();

  send(data: string) {
    this.sent.push(data);
  }

  close() {
    this.closed = true;
  }

  addEventListener(type: string, fn: (event: { data: unknown }) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), fn]);
  }

  frame(frame: object) {
    for (const fn of this.listeners.get('message') ?? []) {
      fn({ data: JSON.stringify({ protocol: 'v3.realtime', protocol_version: 1, ...frame }) });
    }
  }
}

// Requirement: the bridge waits for V3 replay before acknowledging or sending.
// Regression: an apparently connected browser could send into a dead/unhydrated watch.
// Boundary: SwarmWebSocketBridge + SwarmBrowserChat; hermetic transport, real loopback WS.
test('SwarmWebSocketBridge and SwarmBrowserChat: connects, subscribes, streams tokens and tools over WebSocket', { timeout: 5000 }, async () => {
  // Application HTTP Server with SwarmWebSocketBridge attached
  const appHttp = http.createServer((req, res) => {
    res.writeHead(200);
    res.end('OK');
  });

  await new Promise<void>((r) => appHttp.listen(0, '127.0.0.1', () => r()));
  const appPort = (appHttp.address() as any).port;

  const transport = new SwarmTransport({ baseUrl: 'http://127.0.0.1', defaultHeaders: {}, timeoutMs: 1000 });
  const chat = new SwarmChatNamespace(transport);
  const permissions = new SwarmPermissionsNamespace(transport);

  const mockEvents: any[] = [];

  // Mock transport.request for hydrate & messages
  transport.request = async (path: string) => {
    if (path === '/v3/sync/hydrate') {
      const snap: V3SyncSnapshot = {
        ok: true,
        rev: 1,
        snapshot_endpoint_cursor: 'cursor-1',
        sessions_by_id: { sess_ws_test: { id: 'sess_ws_test' } },
        projections_by_session: { sess_ws_test: { session_id: 'sess_ws_test', last_event_seq: mockEvents.length, projection_high_watermark_seq: mockEvents.length, updated_at: 100 } },
        messages_by_session: { sess_ws_test: [] },
        events_by_session: { sess_ws_test: [...mockEvents] },
        omissions: [],
        pagination: {},
        realtime: {
          stream_path: '/v3/realtime/stream',
          resume: { protocol: 'v3.realtime', protocol_version: 1, kind: 'resume', endpoint_cursor: 'cursor-1', subscriptions: [] },
        },
      };
      return { status: 200, headers: {}, rawText: '', data: snap } as any;
    }
    if (path === '/v3/sessions/sess_ws_test/messages') {
      return { status: 200, headers: {}, rawText: '', data: { ok: true, message: { id: 'msg_1' } } } as any;
    }
    return { status: 200, headers: {}, rawText: '', data: { ok: true } } as any;
  };

  const mockSockets: MockSocket[] = [];
  const bridge = new SwarmWebSocketBridge(appHttp, chat, permissions, {
    path: '/ws',
    autoApprovePermissions: true,
    socketFactory: async () => {
      const sock = new MockSocket();
      mockSockets.push(sock);
      return sock;
    },
  });

  const browserClient = new SwarmBrowserChat({
    url: `ws://127.0.0.1:${appPort}/ws`,
    webSocketClass: WebSocket,
    autoConnect: false,
    autoReconnect: false,
  });

  try {
    const receivedEvents: string[] = [];
    let textReceived = '';
    let toolStarted: any = null;
    let toolCompleted: any = null;

    browserClient.on('connected', () => receivedEvents.push('connected'));
    browserClient.on('subscribed', (sid) => receivedEvents.push(`subscribed:${sid}`));
    browserClient.on('text', (delta) => {
      receivedEvents.push('text');
      textReceived += delta;
    });
    browserClient.on('tool_start', (t) => {
      receivedEvents.push('tool_start');
      toolStarted = t;
    });
    browserClient.on('tool_done', (t) => {
      receivedEvents.push('tool_done');
      toolCompleted = t;
    });

    await browserClient.connect();
    assert.equal(browserClient.isConnected, true);

    // Subscribe to session
    const subscriptionReady = browserClient.subscribe('sess_ws_test');
    assert.ok(receivedEvents.includes('connected'));

    while (mockSockets.length === 0) {
      await new Promise((r) => setTimeout(r, 10));
    }
    const mockSocket = mockSockets[0];
    assert.ok(mockSocket, 'mockSocket should be created');

    // Send daemon hello frame
    mockSocket.frame({
      kind: 'hello',
      endpoint_cursor: 'cursor-head',
      capabilities: ['live_patch_v1'],
    });

    // Send daemon replay.complete frame
    mockSocket.frame({
      kind: 'replay.complete',
      session_id: 'sess_ws_test',
      subscription_id: 'sdk:session:sess_ws_test',
      endpoint_cursor: 'cursor-1',
    });

    await subscriptionReady;
    assert.ok(receivedEvents.includes('subscribed:sess_ws_test'));
    // Re-selecting the same conversation must always acknowledge, never hang.
    await browserClient.subscribe('sess_ws_test');
    // Send chat message over WebSocket
    browserClient.sendMessage('Run test command', 'sess_ws_test');

    // Emit live text patch from daemon
    mockSocket.frame({
      kind: 'live.patch',
      session_id: 'sess_ws_test',
      subscription_id: 'sdk:session:sess_ws_test',
      live: {
        session_id: 'sess_ws_test',
        run_id: 'run_1',
        stream_id: 'stream_1',
        stream_kind: 'assistant_text',
        operation: 'append',
        step: 1,
        step_id: 'step_1',
        live_seq_start: 1,
        live_seq_end: 1,
        offset_start: 0,
        offset_end: 5,
        text: 'Hello',
        recorded_at: Date.now(),
      },
    });

    // Emit tool events from daemon
    const evt1 = {
      id: 'evt_1',
      session_id: 'sess_ws_test',
      seq: 2,
      event_type: 'session.tool.started',
      payload: { call_id: 'call_1', tool_name: 'bash', arguments: '{"command":"ls"}' },
      ts_unix_ms: Date.now(),
    };
    mockEvents.push(evt1);

    mockSocket.frame({
      kind: 'event',
      session_id: 'sess_ws_test',
      subscription_id: 'sdk:session:sess_ws_test',
      event: evt1,
      projection: { session_id: 'sess_ws_test', last_event_seq: 2, projection_high_watermark_seq: 2, updated_at: Date.now() },
    });

    const evt2 = {
      id: 'evt_2',
      session_id: 'sess_ws_test',
      seq: 3,
      event_type: 'session.tool.completed',
      payload: { call_id: 'call_1', tool_name: 'bash', output: 'file.txt', duration_ms: 25 },
      ts_unix_ms: Date.now(),
    };
    mockEvents.push(evt2);

    mockSocket.frame({
      kind: 'event',
      session_id: 'sess_ws_test',
      subscription_id: 'sdk:session:sess_ws_test',
      event: evt2,
      projection: { session_id: 'sess_ws_test', last_event_seq: 3, projection_high_watermark_seq: 3, updated_at: Date.now() },
    });

    // Wait for events to propagate through bridge to browserClient
    await new Promise((r) => setTimeout(r, 100));

    assert.equal(textReceived, 'Hello');
    assert.equal(toolStarted?.name, 'bash');
    assert.equal(toolCompleted?.output, 'file.txt');
    assert.equal(toolCompleted?.durationMs, 25);
  } finally {
    browserClient.dispose();
    await bridge.close();
    try { (appHttp as any).closeAllConnections?.(); } catch {}
    await new Promise<void>((r) => appHttp.close(() => r()));
  }
});

// Requirement: terminal upstream failure must invalidate browser subscription state.
// Threat: a socket stays 'subscribed' after its daemon watch dies and silently loses turns.
// Boundary: bridge watch lifecycle with a controlled failing upstream, real loopback socket.
test('bridge closes a failed watch instead of leaving a usable-looking subscription', { timeout: 3000 }, async () => {
  const server = http.createServer();
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve));
  const transport = new SwarmTransport({ baseUrl: 'http://localhost' });
  const chat = new SwarmChatNamespace(transport);
  let fail!: (error: Error) => void;
  let disposed = 0;
  chat.stream = async (_id, options) => {
    fail = options!.onError!;
    return { ready: Promise.resolve(), done: new Promise(() => {}), dispose() { disposed++; } };
  };
  const bridge = chat.attachWebSocket(server);
  const client = new SwarmBrowserChat({ url: `ws://127.0.0.1:${(server.address() as any).port}/ws`, webSocketClass: WebSocket, autoConnect: false, autoReconnect: false });
  try {
    await client.subscribe('retained');
    const closed = new Promise<void>(resolve => client.on('disconnected', resolve));
    fail(new Error('upstream unavailable'));
    await closed;
    assert.equal(client.isSubscribed, false);
    assert.throws(() => client.sendMessage('must not disappear'), /subscribe/);
    assert.equal(disposed, 1);
  } finally {
    client.dispose(); await bridge.close();
    await new Promise<void>(resolve => server.close(() => resolve()));
  }
});

// Requirement: a foreign website cannot command a local privileged bridge.
// Boundary: upgrade origin check; assert rejected upgrade never allocates a watch.
test('bridge rejects foreign-origin upgrades before session access', { timeout: 3000 }, async () => {
  const server = http.createServer();
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve));
  const chat = new SwarmChatNamespace(new SwarmTransport({ baseUrl: 'http://localhost' }));
  let watches = 0;
  chat.stream = async () => { watches++; throw new Error('must not allocate'); };
  const bridge = chat.attachWebSocket(server);
  const ws = new WebSocket(`ws://127.0.0.1:${(server.address() as any).port}/ws`, { origin: 'https://foreign.invalid' });
  try {
    const error = await new Promise<Error>(resolve => ws.once('error', resolve));
    assert.match(error.message, /403/);
    assert.equal(watches, 0);
  } finally {
    ws.terminate(); await bridge.close();
    await new Promise<void>(resolve => server.close(() => resolve()));
  }
});

// Requirement: browser decisions carry structured replies and receive confirmed, correlated results.
// Threat: success without a record, duplicate frames, wrong-session mutations or recoverable failures
// tearing down the stream. Authority: bridge handleMessage + permissions.resolve; real loopback WS,
// stub HTTP authority is the smallest test of the wire boundary (not live agent qualification).
test('bridge correlates permission results, refuses duplicate frames and preserves failed requests', { timeout: 3000 }, async () => {
  const server = http.createServer();
  await new Promise<void>(resolve => server.listen(0, '127.0.0.1', resolve));
  const transport = new SwarmTransport({ baseUrl: 'http://localhost' });
  const chat = new SwarmChatNamespace(transport);
  let callbacks: any;
  chat.stream = async (_id, options) => { callbacks = options; return { ready: Promise.resolve(), done: new Promise(() => {}), dispose() {} }; };
  let calls = 0; let body: any; let fail = false;
  const p = { id: 'p', session_id: 's', tool_name: 'ask_user', status: 'pending' };
  transport.request = async (_path, options) => {
    calls++; body = options?.body;
    if (fail) throw new Error('backend rejected');
    return { data: { ok: true, session_id: 's', permission: { ...p, status: 'approved', decision: body.action, reason: body.reason, approved_arguments: JSON.stringify(body.approved_arguments) } } } as any;
  };
  const bridge = chat.attachWebSocket(server, { autoApprovePermissions: true });
  const ws = new WebSocket(`ws://127.0.0.1:${(server.address() as any).port}/ws`);
  const frames: any[] = [];
  ws.on('message', raw => frames.push(JSON.parse(raw.toString())));
  const next = (predicate: (m: any) => boolean) => new Promise<any>(resolve => {
    const existing = frames.find(predicate); if (existing) { resolve(existing); return; }
    const listener = (raw: any) => { const msg = JSON.parse(raw.toString()); if (predicate(msg)) { ws.off('message', listener); resolve(msg); } };
    ws.on('message', listener);
  });
  try {
    await new Promise<void>(resolve => ws.once('open', resolve));
    ws.send(JSON.stringify({ type: 'subscribe', sessionId: 's', autoApprovePermissions: true }));
    await next(m => m.type === 'subscribed');
    let autoAnswers = 0;
    await callbacks.onPermissionRequested(p, async () => { autoAnswers++; });
    await next(m => m.type === 'tool_permission'); assert.equal(autoAnswers, 0);
    const request = { type: 'resolve_permission', sessionId: 's', permissionId: 'p', action: 'allow_once', requestId: 'reply', reason: '{"answers":{"q_1":"No"}}', approvedArguments: { reviewed: true } };
    ws.send(JSON.stringify(request));
    const result = await next(m => m.type === 'permission_resolved');
    assert.equal(result.requestId, 'reply'); assert.equal(result.result.permission.id, 'p');
    assert.deepEqual(body.approved_arguments, { reviewed: true }); assert.equal(body.reason, request.reason);
    ws.send(JSON.stringify(request));
    assert.match((await next(m => m.type === 'permission_error' && m.requestId === 'reply')).error, /Duplicate/);
    assert.equal(calls, 1);
    ws.send(JSON.stringify({ ...request, requestId: 'foreign', sessionId: 'other' }));
    assert.match((await next(m => m.requestId === 'foreign')).error, /selected conversation/); assert.equal(calls, 1);
    fail = true; ws.send(JSON.stringify({ ...request, requestId: 'failure' }));
    assert.match((await next(m => m.requestId === 'failure')).error, /backend rejected/);
    assert.equal(ws.readyState, WebSocket.OPEN);
    assert.equal(frames.filter(m => m.type === 'permission_resolved').length, 1);
    fail = false; ws.send(JSON.stringify({ ...request, requestId: 'retry' }));
    assert.equal((await next(m => m.requestId === 'retry')).type, 'permission_resolved');
    callbacks.onPermissionError(new Error('render failure'), p);
    await next(m => m.type === 'permission_error' && m.error === 'render failure');
    assert.equal(ws.readyState, WebSocket.OPEN);
  } finally { ws.terminate(); await bridge.close(); await new Promise<void>(resolve => server.close(() => resolve())); }
});
