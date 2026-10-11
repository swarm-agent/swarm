import assert from 'node:assert/strict';
import { test } from 'node:test';
import { SwarmRealtimeNamespace, V3LiveText, type SessionWatchState, type V3LivePatch, type V3SyncSnapshot, type RealtimeSocket } from '../realtime.js';
import { SwarmTransport } from '../transport.js';

const patch: V3LivePatch = { session_id: 's', run_id: 'r', stream_id: 't', stream_kind: 'assistant_text', operation: 'append', step: 1, step_id: 'step', live_seq_start: 1, live_seq_end: 1, offset_start: 0, offset_end: 2, text: 'é', recorded_at: 1 };
const delta = (seq: number, start: number, end: number, text: string) => ({ id: `e${seq}`, session_id: 's', seq,
  event_type: 'session.assistant.delta', ts_unix_ms: 1, payload: { run_id: 'r', stream_id: 't', offset_start: start, offset_end: end, delta: text } });
// Purpose: V3LiveText enforces the daemon's UTF-8 byte-offset contract at the narrow reducer
// boundary. Requirement: text is never repeated, never shown with a hole, and never lost when
// live patches run ahead of durable checkpoints (the daemon persists deltas up to 250ms/2KB
// behind live output, so every mid-turn join or reconnect sees such a gap). Threat: the former
// reducer rejected the gap and tore the socket down, reconnecting into the same gap until the
// watch died. Malformed ranges still fail closed. Committed streams stay retired.
test('live text deduplicates, holds gaps until durable fill, trims overlaps, and tombstones', () => {
  const text = new V3LiveText();
  assert.equal(text.accept(patch), true);
  assert.equal(text.accept(patch), true);
  assert.equal(text.values()[0].text, 'é');
  // A range beyond the prefix is held, not shown, and not a protocol failure.
  assert.equal(text.accept({ ...patch, live_seq_start: 3, live_seq_end: 3, offset_start: 3, offset_end: 5, text: 'cd' }), true);
  assert.equal(text.values()[0].text, 'é');
  // A later adjoining live range extends the held range.
  assert.equal(text.accept({ ...patch, live_seq_start: 4, live_seq_end: 4, offset_start: 5, offset_end: 6, text: 'e' }), true);
  // A durable checkpoint overlapping the prefix and reaching the held range releases it.
  text.durable([delta(1, 0, 3, 'éb')]);
  assert.equal(text.values()[0].text, 'ébcde');
  // Duplicate and overlapping ranges are trimmed by offset.
  text.durable([delta(2, 0, 5, 'ébcd')]);
  assert.equal(text.accept({ ...patch, offset_start: 4, offset_end: 7, text: 'def' }), true);
  assert.equal(text.values()[0].text, 'ébcdef');
  // Malformed ranges fail closed without changing text.
  assert.equal(text.accept({ ...patch, offset_start: 7, offset_end: 9, text: 'x' }), false);
  assert.equal(text.accept({ ...patch, operation: 'replace', offset_start: 7, offset_end: 8, text: 'x' }), false);
  assert.equal(text.values()[0].text, 'ébcdef');
  text.reconcile([{ id: 'm', session_id: 's', global_seq: 2, role: 'assistant', content: 'é', created_at: 1, metadata: { run_id: 'r', stream_id: 't' } }]);
  assert.deepEqual(text.values(), []);
  assert.equal(text.accept(patch), true);
  assert.deepEqual(text.values(), []);
});

class Socket implements RealtimeSocket {
  sent: string[] = []; closed = false;
  listeners = new Map<string, ((event: { data: unknown }) => void)[]>();
  send(data: string) { this.sent.push(data); }
  close() { this.closed = true; }
  addEventListener(type: string, fn: (event: { data: unknown }) => void) { this.listeners.set(type, [...(this.listeners.get(type) ?? []), fn]); }
  frame(frame: object) { for (const fn of this.listeners.get('message') ?? []) fn({ data: JSON.stringify({ protocol: 'v3.realtime', protocol_version: 1, ...frame }) }); }
}
const snapshot = (cursor: string): V3SyncSnapshot => ({ ok: true, rev: 5, snapshot_endpoint_cursor: cursor,
  sessions_by_id: { s: { id: 's' } }, projections_by_session: { s: { session_id: 's', last_event_seq: 5, projection_high_watermark_seq: 5, updated_at: 1 } },
  messages_by_session: { s: [] }, events_by_session: { s: [] }, omissions: [], pagination: {},
  realtime: { stream_path: '/v3/realtime/stream', resume: { protocol: 'v3.realtime', protocol_version: 1, kind: 'resume', endpoint_cursor: cursor, subscriptions: [] } } });
async function fixture() {
  const transport = new SwarmTransport({ baseUrl: 'http://127.0.0.1', defaultHeaders: {}, timeoutMs: 500 });
  const api = new SwarmRealtimeNamespace(transport);
  let boots = 0, hydrates = 0;
  transport.request = async (path, options) => {
    assert.equal(path, '/v3/sync/hydrate', 'targeted session_view requires hydrate, never bootstrap');
    assert.equal((options?.body as { resources: { session_view: boolean } }).resources.session_view, true);
    hydrates++;
    return { status: 200, headers: {}, rawText: '', data: snapshot(`opaque-${++boots}`) } as never;
  };
  const sockets: Socket[] = [];
  const states: string[] = [];
  let last: SessionWatchState | undefined;
  const watcher = api.watchSession('s', { onChange: (s) => { last = s; states.push(s.live.map((l) => l.text).join('')); }, socketFactory: async () => { const socket = new Socket(); sockets.push(socket); return socket; } });
  await new Promise(setImmediate);
  return { api, sockets, states, watcher, counts: () => ({ boots, hydrates }), lastState: () => last };
}
// Purpose: SwarmRealtimeNamespace must close the hydration/subscription race using
// the server snapshot cursor, filter foreign sessions, negotiate live patches,
// repair invalid cursors, and cancel. A deterministic socket boundary proves SDK
// framing without claiming daemon/provider integration results.
test('watch uses snapshot resume, filters foreign sessions, repairs cursor and disposes', { timeout: 3000 }, async () => {
  const f = await fixture();
  try {
    const socket = f.sockets[0];
    socket.frame({ kind: 'hello', endpoint_cursor: 'later-head', capabilities: ['live_patch_v1'] });
    const resume = JSON.parse(socket.sent[0]);
    assert.equal(resume.endpoint_cursor, 'opaque-1');
    assert.deepEqual(resume.capabilities, ['live_patch_v1']);
    assert.equal(resume.subscriptions[0].session_id, 's');
    assert.equal(resume.after_seq, undefined);
    socket.frame({ kind: 'replay.complete', session_id: 's', subscription_id: 'sdk:session:s' });
    await f.watcher.ready;
    await new Promise(setImmediate);
    const count = f.counts().hydrates;
    socket.frame({ kind: 'event', session_id: 'foreign', event: { session_id: 'foreign', seq: 900 } });
    socket.frame({ kind: 'live.patch', session_id: 'foreign', live: { ...patch, session_id: 'foreign' } });
    assert.equal(f.counts().hydrates, count);
    socket.frame({ kind: 'live.patch', session_id: 's', live: patch });
    socket.frame({ kind: 'live.patch', session_id: 's', live: patch });
    assert.equal(f.states.at(-1), 'é');
    socket.frame({ kind: 'cursor.error', error_code: 'endpoint_cursor_gap', bootstrap_required: true });
    await new Promise((resolve) => setTimeout(resolve, 300));
    assert.equal(socket.closed, true);
    assert.equal(f.counts().boots, 3);
    f.sockets[1].frame({ kind: 'hello', endpoint_cursor: 'head' });
    assert.equal(JSON.parse(f.sockets[1].sent[0]).endpoint_cursor, 'opaque-3');
  } finally { f.watcher.dispose(); await f.watcher.done; }
  assert.equal(f.sockets.at(-1)?.closed, true);
});
// Purpose: auth/protocol failures must terminate rather than retry forever or
// silently render an unauthenticated stream. This is the narrow framing boundary.
test('watch rejects authentication denial without reconnect', { timeout: 1000 }, async () => {
  const f = await fixture();
  const done = assert.rejects(f.watcher.done, /denied/);
  f.sockets[0].frame({ kind: 'auth.denied', error: 'denied' });
  await done;
  assert.equal(f.counts().boots, 1);
  assert.equal(f.sockets[0].closed, true);
});
// Purpose: cancellation at admission prevents any snapshot or socket request.
test('pre-aborted watch performs no I/O', async () => {
  const transport = new SwarmTransport({ baseUrl: 'http://127.0.0.1', defaultHeaders: {}, timeoutMs: 500 });
  const api = new SwarmRealtimeNamespace(transport);
  api.hydrate = async () => { throw new Error('unexpected I/O'); };
  const abort = new AbortController(); abort.abort();
  const watch = api.watchSession('s', { signal: abort.signal, onChange: () => assert.fail('unexpected update') });
  await watch.done;
  await assert.rejects(watch.ready, /disposed/);
});

// Purpose: the real Node ws adapter must upgrade over an isolated Unix socket,
// carry credentials in headers (never URL), and propagate AbortSignal into HTTP.
// This uses real local transports, but no daemon, credentials or provider calls.
test('Node Unix socket realtime and HTTP cancellation', { timeout: 5000 }, async () => {
  const { default: http } = await import('node:http');
  const { mkdtemp, rm } = await import('node:fs/promises');
  const { join } = await import('node:path');
  const { once } = await import('node:events');
  const moduleName = 'ws';
  const { WebSocketServer } = await import(moduleName);
  const root = process.env.TMPDIR;
  assert.ok(root, 'TMPDIR is required for socket scratch');
  const directory = await mkdtemp(join(root, 'sdk-ws-'));
  const path = join(directory, 'daemon.sock');
  const server = http.createServer(() => {});
  const wss = new WebSocketServer({ server });
  let requestURL = '', authorization: string | undefined;
  wss.on('connection', (socket: { send(data: string): void }, request: http.IncomingMessage) => {
    requestURL = request.url!; authorization = request.headers.authorization;
    socket.send(JSON.stringify({ protocol: 'v3.realtime', protocol_version: 1, kind: 'hello', endpoint_cursor: 'opaque' }));
  });
  server.listen(path); await once(server, 'listening');
  const transport = new SwarmTransport({ baseUrl: 'http://localhost', socketPath: path, token: 'fixture-token', defaultHeaders: {}, timeoutMs: 1000 });
  let socket: RealtimeSocket | undefined;
  try {
    socket = await new SwarmRealtimeNamespace(transport).socket('/v3/realtime/stream?surface=desktop');
    const hello = await new Promise<string>((resolve) => socket!.addEventListener('message', ({ data }) => resolve(String(data))));
    assert.equal(JSON.parse(hello).endpoint_cursor, 'opaque');
    assert.equal(requestURL, '/v3/realtime/stream?surface=desktop');
    assert.equal(authorization, 'Bearer fixture-token');
    const abort = new AbortController();
    const pending = transport.request('/never-completes', { signal: abort.signal });
    abort.abort();
    await assert.rejects(pending, /aborted/);
  } finally {
    socket?.close();
    for (const client of wss.clients) client.terminate();
    await new Promise<void>((resolve) => wss.close(() => resolve()));
    server.closeAllConnections();
    await new Promise<void>((resolve) => server.close(() => resolve()));
    await rm(directory, { recursive: true, force: true });
  }
});

// Purpose: completed durable events must retire the live draft before asynchronous
// snapshot refresh; delayed transient frames must not resurrect committed output.
test('completion event tombstones before snapshot refresh', () => {
  const text = new V3LiveText(); text.accept(patch);
  text.durable([{ id: 'completed', session_id: 's', seq: 7, ts_unix_ms: 1,
    event_type: 'session.assistant.completed', payload: { run_id: 'r', stream_id: 't' } }]);
  assert.deepEqual(text.values(), []);
  text.accept({ ...patch, live_seq_start: 2, live_seq_end: 2, offset_start: 2, offset_end: 4 });
  assert.deepEqual(text.values(), []);
});

const frameEvent = (event: object) => ({ kind: 'event', session_id: 's', subscription_id: 'sdk:session:s', event });
// Purpose: watch.ready must include post-replay authority, and joining or reconnecting while a
// turn streams must not tear the socket down. Requirement: live output that starts beyond the
// durable prefix (normal: durable checkpoints lag live text) is held and released by the next
// durable checkpoint on the same socket. Threat: the former SDK closed the socket on this gap,
// re-hydrated into the same gap and exhausted its reconnect budget mid-answer. Socket and
// transport fixtures isolate the lifecycle; this is not provider-backed evidence.
test('ready waits for replay hydration and a mid-turn live gap is filled without reconnecting', { timeout: 3000 }, async () => {
  const f = await fixture();
  try {
    let release!: (s: V3SyncSnapshot) => void;
    f.api.hydrate = () => new Promise(resolve => { release = resolve; });
    let isReady = false; void f.watcher.ready.then(() => { isReady = true; });
    f.sockets[0].frame({ kind: 'hello', endpoint_cursor: 'head', capabilities: ['live_patch_v1'] });
    f.sockets[0].frame({ kind: 'replay.complete', session_id: 's' });
    await new Promise(setImmediate); assert.equal(isReady, false);
    release(snapshot('repaired')); await f.watcher.ready;
    f.api.hydrate = async () => snapshot('rehydrated');
    // Live output after subscribing starts at byte 3; durable checkpoints have nothing yet.
    f.sockets[0].frame({ kind: 'live.patch', session_id: 's', live: { ...patch, live_seq_start: 7, live_seq_end: 7, offset_start: 3, offset_end: 5, text: 'cd' } });
    assert.equal(f.sockets[0].closed, false);
    assert.equal(f.states.at(-1), '');
    f.sockets[0].frame(frameEvent(delta(6, 0, 3, 'éb')));
    assert.equal(f.states.at(-1), 'ébcd');
    f.sockets[0].frame({ kind: 'live.patch', session_id: 's', live: { ...patch, live_seq_start: 8, live_seq_end: 8, offset_start: 5, offset_end: 6, text: 'e' } });
    assert.equal(f.states.at(-1), 'ébcde');
    await new Promise(resolve => setTimeout(resolve, 50));
    assert.equal(f.sockets.length, 1);
    assert.equal(f.sockets[0].closed, false);
  } finally { f.watcher.dispose(); await f.watcher.done; }
});

// Purpose: maxReconnects bounds one outage, not the lifetime of a long-lived watch.
// Threat: the counter was never reset, so a conversation left open survived only a fixed
// number of ordinary disconnects in total, then failed permanently. Boundary: watch lifecycle.
test('reconnect budget resets after every replayed connection', { timeout: 5000 }, async () => {
  const transport = new SwarmTransport({ baseUrl: 'http://127.0.0.1', defaultHeaders: {}, timeoutMs: 500 });
  const api = new SwarmRealtimeNamespace(transport);
  let hydrates = 0;
  api.hydrate = async () => snapshot(`cursor-${++hydrates}`);
  const sockets: Socket[] = [];
  const errors: string[] = [];
  const watch = api.watchSession('s', { maxReconnects: 1, onChange: () => {}, onError: e => errors.push(e.message),
    socketFactory: async () => { const socket = new Socket(); sockets.push(socket); return socket; } });
  const replayOn = async (index: number) => {
    while (sockets.length <= index) await new Promise(resolve => setTimeout(resolve, 5));
    sockets[index].frame({ kind: 'hello', endpoint_cursor: 'head' });
    sockets[index].frame({ kind: 'replay.complete', session_id: 's' });
    await new Promise(resolve => setTimeout(resolve, 5));
  };
  try {
    for (let i = 0; i < 4; i++) {
      await replayOn(i);
      for (const fn of sockets[i].listeners.get('close') ?? []) fn({ data: undefined });
    }
    await replayOn(4);
    assert.equal(sockets[4].closed, false);
    assert.deepEqual(errors, []);
  } finally { watch.dispose(); await watch.done; }
});

// Purpose: every durable event delivered on the socket reaches the consumer immediately and
// exactly once, even when a later bounded hydration tail does not contain it. Threat: tool and
// permission events were visible only through re-hydration, so a burst beyond the 200-event
// tail was silently lost and every token checkpoint triggered a full re-hydration.
test('socket events merge into state and streaming checkpoints do not re-hydrate', { timeout: 3000 }, async () => {
  const f = await fixture();
  try {
    let hydrates = 0;
    f.api.hydrate = async () => { hydrates++; return snapshot('tail-without-socket-events'); };
    const events: number[][] = [];
    f.sockets[0].frame({ kind: 'hello', endpoint_cursor: 'head' });
    f.sockets[0].frame({ kind: 'replay.complete', session_id: 's' });
    await f.watcher.ready;
    const before = hydrates;
    f.sockets[0].frame(frameEvent(delta(6, 0, 1, 'a')));
    f.sockets[0].frame(frameEvent(delta(7, 1, 2, 'b')));
    await new Promise(resolve => setTimeout(resolve, 20));
    assert.equal(hydrates, before, 'assistant checkpoints must not trigger hydration');
    f.sockets[0].frame(frameEvent({ id: 'tool', session_id: 's', seq: 8, ts_unix_ms: 1, event_type: 'session.tool.started', payload: { call_id: 'c', tool_name: 'bash' } }));
    await new Promise(resolve => setTimeout(resolve, 20));
    assert.equal(hydrates, before + 1, 'a non-checkpoint event refreshes authority');
    events.push((f.lastState()?.snapshot.events_by_session.s ?? []).map(e => e.seq));
    assert.deepEqual(events.at(-1), [6, 7, 8]);
  } finally { f.watcher.dispose(); await f.watcher.done; }
});
