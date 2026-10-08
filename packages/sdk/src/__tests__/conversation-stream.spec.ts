import assert from 'node:assert/strict';
import { test } from 'node:test';
import { SwarmChatNamespace } from '../chat.js';
import { SwarmBrowserChat } from '../browser.js';
import { SwarmProjectsNamespace } from '../projects.js';
import { SwarmTransport } from '../transport.js';
import type { SessionWatchState } from '../realtime.js';

// Requirement: one watch handles every turn and durable reconnect without history replay.
// Threat: a shorter second response is suppressed by prior length, idle emits duplicate done,
// or a tool-only failure leaves the composer locked. Boundary: chat.stream reducer;
// deterministic callback injection is the narrowest layer (not a live-agent benchmark).
test('stream tracks each run/stream and completes empty turns once', async () => {
  const chat = new SwarmChatNamespace(new SwarmTransport({ baseUrl: 'http://localhost' }));
  let update!: (state: SessionWatchState) => void;
  (chat as any).realtime.watchSession = (_id: string, options: any) => {
    update = options.onChange;
    return { ready: Promise.resolve(), done: new Promise(() => {}), dispose() {} };
  };
  const texts: string[] = [];
  let completed = 0;
  await chat.stream('conversation', { onText: delta => texts.push(delta), onComplete: () => completed++ });
  const snapshot: any = { sessions_by_id: {}, events_by_session: {}, current_run_state_by_session: {}, session_views_by_id: {} };
  const state: SessionWatchState = { sessionId: 'conversation', snapshot, messages: [], live: [], status: 'live' };
  const run = (id: string, active: boolean) => {
    snapshot.current_run_state_by_session.conversation = { run_id: id, active, completed_at: active ? undefined : 1 };
  };
  run('historical', false); update(state); assert.equal(completed, 0);
  run('first', true); update(state);
  state.live = [{ runId: 'first', streamId: 'text', text: 'A long first response' }]; update(state);
  state.live = []; run('first', false); update(state); update(state);
  assert.equal(completed, 1);
  run('second', true);
  state.live = [{ runId: 'second', streamId: 'text', text: 'OK' }]; update(state); update(state);
  assert.deepEqual(texts, ['A long first response', 'OK']);
  state.live = []; run('second', false); update(state);
  run('failed-without-text', true); update(state);
  run('failed-without-text', false); update(state); update(state);
  assert.equal(completed, 3);
});

// Requirement: project conversations retain canonical project identity and never use a
// workspace chat or task session as fallback. Boundary: project route adapter wire contract.
test('project conversation helpers unwrap lists, create separately, and reject wrong agents', async () => {
  const transport = new SwarmTransport({ baseUrl: 'http://localhost' });
  const calls: any[] = [];
  transport.request = async (path: string, options: any) => {
    calls.push({ path, ...options });
    return { status: 200, data: options.method === 'GET'
      ? { sessions: [{ session: { id: 'existing', archived: false }, projection: { state: 'idle' } }] }
      : { session: { id: 'new', agent_name: 'system-orchestrator' } } } as any;
  };
  const projects = new SwarmProjectsNamespace(transport);
  assert.equal((await projects.getOrchestratorSession('project')).id, 'existing');
  assert.equal(calls.length, 1);
  assert.equal((await projects.createConversation('project', { clientRequestId: 'retry-key' })).id, 'new');
  assert.equal(calls[1].path, '/v3/projects/project/sessions');
  assert.equal(calls[1].body.agent_name, 'system-orchestrator');
  assert.equal(calls[1].body.client_request_id, 'retry-key');
  assert.equal(calls[1].body.workspace_path, undefined);
  await assert.rejects(projects.createConversation('project', { agentName: 'swarm' }), /system-orchestrator/);
  assert.equal(calls.length, 2);
});

class BrowserSocket {
  static instances: BrowserSocket[] = [];
  readyState = 0;
  sent: any[] = [];
  onopen?: () => void; onclose?: () => void; onmessage?: (e: any) => void; onerror?: (e: any) => void;
  constructor(_url: string) { BrowserSocket.instances.push(this); queueMicrotask(() => { this.readyState = 1; this.onopen?.(); }); }
  send(raw: string) { this.sent.push(JSON.parse(raw)); }
  close() { this.readyState = 3; this.onclose?.(); }
  frame(value: object) { this.onmessage?.({ data: JSON.stringify(value) }); }
}

// Requirement: switching cannot route old messages into a new conversation; disconnected
// sends and failed subscriptions fail visibly. Boundary: browser client protocol only.
test('browser filters stale sessions and bounds subscription failures', { timeout: 2000 }, async () => {
  const chat = new SwarmBrowserChat({ webSocketClass: BrowserSocket, autoConnect: false, autoReconnect: false, subscribeTimeoutMs: 50 });
  await chat.connect();
  const socket = BrowserSocket.instances.at(-1)!;
  const pending = chat.subscribe('new');
  await new Promise(resolve => setImmediate(resolve));
  socket.frame({ type: 'subscribed', sessionId: 'new' });
  await pending;
  const text: string[] = [];
  chat.on('text', delta => text.push(delta));
  socket.frame({ type: 'text', sessionId: 'old', delta: 'wrong' });
  socket.frame({ type: 'text', sessionId: 'new', delta: 'right' });
  assert.deepEqual(text, ['right']);
  const id = chat.sendMessage('hello', undefined, undefined, 'retry');
  assert.equal(id, 'retry');
  assert.equal(socket.sent.at(-1).requestId, 'retry');
  await assert.rejects(chat.subscribe('unavailable'), /timed out/);
  assert.throws(() => chat.sendMessage('must not disappear'), /subscribe/);
  socket.close();
  assert.throws(() => chat.sendMessage('offline'), /subscribe/);
  chat.dispose();
});

// Requirement: reconnect reselects the same durable conversation without resending input.
// Threat: a dropped connection creates a new session, duplicates a turn, or accepts stale frames.
// Boundary: SwarmBrowserChat transport lifecycle; controlled sockets keep this hermetic.
test('browser reconnect reselects identity without replaying messages', { timeout: 2000 }, async () => {
  const chat = new SwarmBrowserChat({ webSocketClass: BrowserSocket, autoConnect: false, subscribeTimeoutMs: 100 });
  try {
    await chat.connect();
    const first = BrowserSocket.instances.at(-1)!;
    const ready = chat.subscribe('retained');
    await new Promise(resolve => setImmediate(resolve));
    first.frame({ type: 'subscribed', sessionId: 'retained' });
    await ready;
    chat.sendMessage('send once', undefined, undefined, 'once');
    const reconnected = new Promise<void>(resolve => chat.on('subscribed', () => resolve()));
    first.close();
    assert.equal(chat.isSubscribed, false);
    await new Promise(resolve => setTimeout(resolve, 650));
    const second = BrowserSocket.instances.at(-1)!;
    assert.notEqual(first, second);
    assert.deepEqual(second.sent, [{ type: 'subscribe', sessionId: 'retained' }]);
    second.frame({ type: 'subscribed', sessionId: 'retained' });
    await reconnected;
    assert.equal(chat.sessionId, 'retained');
    assert.equal(chat.isSubscribed, true);
    assert.equal(first.sent.filter(m => m.type === 'chat_message').length, 1);
    assert.equal(second.sent.filter(m => m.type === 'chat_message').length, 0);
  } finally { chat.dispose(); }
});

// Requirement: JavaScript callers must get an actionable validation error for missing or
// malformed IDs, without opening a socket or disturbing their selected conversation.
// Threat: subscribe(null) throws a trim TypeError after failed app initialization.
// Boundary: SwarmBrowserChat.subscribe, exercised directly with a controlled transport.
test('browser rejects invalid session IDs before transport or subscription mutation', { timeout: 2000 }, async () => {
  const chat = new SwarmBrowserChat({ webSocketClass: BrowserSocket, autoConnect: false, autoReconnect: false });
  const invalid = [null, undefined, '', '   ', 42, {}, []];
  const initialSockets = BrowserSocket.instances.length;
  try {
    for (const id of invalid) await assert.rejects(chat.subscribe(id as any), /sessionId must be a non-empty string/);
    assert.equal(BrowserSocket.instances.length, initialSockets);
    assert.equal(chat.sessionId, null);
    const ready = chat.subscribe('selected');
    await new Promise(resolve => setImmediate(resolve));
    const socket = BrowserSocket.instances.at(-1)!;
    socket.frame({ type: 'subscribed', sessionId: 'selected' });
    await ready;
    const sent = socket.sent.length;
    for (const id of invalid) await assert.rejects(chat.subscribe(id as any), /sessionId must be a non-empty string/);
    assert.equal(chat.sessionId, 'selected');
    assert.equal(chat.isSubscribed, true);
    assert.equal(socket.sent.length, sent);
    chat.sendMessage('still usable', undefined, undefined, 'validated');
    assert.equal(socket.sent.at(-1).sessionId, 'selected');
  } finally { chat.dispose(); }
});

// Requirement: permission cards hydrate/reconcile without replaying decisions; resolvers stay
// recoverable after failures and reject stale closures. Authority: chat.stream durable-view reducer.
// Injecting snapshots is the narrowest deterministic layer for reconnect and changed proposals.
test('stream hydrates permissions, guards duplicates and stale closures, and surfaces recoverable errors', async () => {
  const chat = new SwarmChatNamespace(new SwarmTransport({ baseUrl: 'http://localhost' }));
  let update!: (state: SessionWatchState) => void;
  (chat as any).realtime.watchSession = (_id: string, options: any) => {
    update = options.onChange; return { ready: Promise.resolve(), done: new Promise(() => {}), dispose() {} };
  };
  const callbacks: any[] = []; const errors: string[] = []; const removed: string[] = [];
  await chat.stream('s', {
    onPermissionRequested: (p, resolve) => { callbacks.push({ p, resolve }); if (p.id === 'throws') throw new Error('render failed'); },
    onPermissionRemoved: id => removed.push(id), onPermissionError: e => errors.push(e.message),
  });
  const p = { id: 'p', session_id: 's', status: 'pending', tool_name: 'ask-user', updated_at: 1 };
  const state: any = { sessionId: 's', snapshot: { session_views_by_id: { s: { pending_permissions: [p] } } }, live: [], messages: [], status: 'live' };
  const tick = () => new Promise(resolve => setImmediate(resolve));
  state.status = 'syncing'; update(state); await tick(); assert.equal(callbacks.length, 0);
  state.status = 'live'; update(state); update(state); await tick(); assert.equal(callbacks.length, 1);
  state.status = 'reconnecting'; update(state);
  await assert.rejects(callbacks[0].resolve('deny'), /reconnecting/);
  state.status = 'live'; update(state);
  let calls = 0; let finish!: (value: any) => void;
  (chat as any).permissions.resolve = async (...args: any[]) => { calls++; assert.deepEqual(args[3], { reason: '{"answers":{"q_1":"No"}}' }); return new Promise(resolve => { finish = resolve; }); };
  const first = callbacks[0].resolve('allow_once', { reason: '{"answers":{"q_1":"No"}}' });
  await assert.rejects(callbacks[0].resolve('deny'), /in flight/); assert.equal(calls, 1);
  finish({ permission: { ...p, status: 'approved' } }); await first;
  await assert.rejects(callbacks[0].resolve('deny'), /stale/);
  state.snapshot.session_views_by_id.s.pending_permissions = null; update(state);
  assert.deepEqual(removed, ['p']);
  state.snapshot.session_views_by_id.s.pending_permissions = [{ ...p, id: 'retry' }]; update(state); await tick();
  (chat as any).permissions.resolve = async () => { throw new Error('denied by backend'); };
  await assert.rejects(callbacks[1].resolve('deny'), /denied by backend/);
  assert.ok(errors.includes('denied by backend'));
  (chat as any).permissions.resolve = async () => ({ ok: true });
  await callbacks[1].resolve('deny');
  state.snapshot.session_views_by_id.s.pending_permissions = [{ ...p, id: 'changed' }]; update(state); await tick();
  const stale = callbacks.at(-1).resolve;
  state.snapshot.session_views_by_id.s.pending_permissions = [{ ...p, id: 'changed', updated_at: 2 }]; update(state); await tick();
  await assert.rejects(stale('deny'), /stale/);
  state.snapshot.session_views_by_id.s.pending_permissions = [{ ...p, id: 'throws' }]; update(state); await tick();
  assert.ok(errors.includes('render failed'));
});

// Requirement: a UI awaits an exact acknowledgement, keeps failures editable, and never replays
// uncertain decisions on disconnect. Authority: SwarmBrowserChat frame/lifecycle boundary.
// Controlled sockets exercise identity, duplicate, null input and reconnect without a live daemon.
test('browser permission replies confirm results and recover after failures without replay', { timeout: 2000 }, async () => {
  const chat = new SwarmBrowserChat({ webSocketClass: BrowserSocket, autoConnect: false, autoReconnect: false, permissionTimeoutMs: 30 });
  const tick = () => new Promise(resolve => setImmediate(resolve));
  try {
    const ready = chat.subscribe('s'); await tick();
    const socket = BrowserSocket.instances.at(-1)!;
    socket.frame({ type: 'subscribed', sessionId: 's' }); await ready;
    const p = { id: 'p', session_id: 's', tool_name: 'ask-user', status: 'pending' };
    const hydrate = (permissions: any) => socket.frame({ type: 'state', sessionId: 's', state: { status: 'live', snapshot: { session_views_by_id: { s: { pending_permissions: permissions } } } } });
    hydrate([p]); assert.equal(chat.permissions.length, 1);
    const sent = socket.sent.length;
    await assert.rejects(chat.resolvePermission(null as any, 'deny'), /permissionId/);
    await assert.rejects(chat.resolvePermission('p', 'deny', null as any), /options/);
    await assert.rejects(chat.resolvePermission('p', 'deny', { sessionId: 'foreign' }), /subscribe/);
    assert.equal(socket.sent.length, sent);
    let success = 0; chat.on('permission_resolved', () => success++);
    const first = chat.resolvePermission('p', 'allow_once', { reason: 'User answer', approvedArguments: { x: 1 } });
    const frame = socket.sent.at(-1);
    assert.deepEqual(frame.approvedArguments, { x: 1 });
    await assert.rejects(chat.resolvePermission('p', 'deny'), /in flight/);
    socket.frame({ type: 'permission_resolved', sessionId: 'foreign', permissionId: 'p', requestId: frame.requestId });
    socket.frame({ type: 'permission_error', sessionId: 's', permissionId: 'p', requestId: frame.requestId, error: 'backend rejected' });
    await assert.rejects(first, /backend rejected/); assert.equal(success, 0); assert.equal(chat.permissions.length, 1);
    const second = chat.resolvePermission('p', 'deny');
    const request = socket.sent.at(-1);
    socket.frame({ type: 'permission_resolved', sessionId: 's', permissionId: 'p', requestId: request.requestId,
      result: { ok: true, session_id: 's', permission: { ...p, status: 'denied', decision: 'deny' } } });
    assert.equal((await second).permission.status, 'denied'); assert.equal(success, 1); assert.equal(chat.permissions.length, 0);
    await assert.rejects(chat.resolvePermission('p', 'deny'), /not pending/);
    hydrate([p]);
    const conflict = chat.resolvePermission('p', 'allow_once');
    socket.frame({ type: 'permission_resolved', sessionId: 's', permissionId: 'p', requestId: socket.sent.at(-1).requestId,
      result: { ok: true, session_id: 's', permission: { ...p, status: 'denied', decision: 'deny' } } });
    await assert.rejects(conflict, /not confirmed/); assert.equal(chat.permissions.length, 1);
    await assert.rejects(chat.resolvePermission('p', 'deny'), /timed out/);
    await assert.rejects(chat.resolvePermission('p', 'deny'), /reconnecting/);
    hydrate([p]);
    const uncertain = chat.resolvePermission('p', 'deny'); socket.close();
    await assert.rejects(uncertain, /outcome unknown/);
    await chat.connect(); const next = BrowserSocket.instances.at(-1)!;
    assert.deepEqual(next.sent, [{ type: 'subscribe', sessionId: 's' }]);
    next.frame({ type: 'subscribed', sessionId: 's' });
    await assert.rejects(chat.resolvePermission('p', 'deny'), /reconnecting/);
    next.frame({ type: 'state', sessionId: 's', state: { status: 'live', snapshot: { session_views_by_id: { s: { pending_permissions: null } } } } });
    assert.equal(chat.permissions.length, 0);
    socket.frame({ type: 'tool_permission', sessionId: 's', permission: p });
    assert.equal(chat.permissions.length, 0);
  } finally { chat.dispose(); }
});
