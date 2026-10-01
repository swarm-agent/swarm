import assert from 'node:assert/strict';
import { test } from 'node:test';
import { SwarmRealtimeNamespace, V3LiveText, type V3LivePatch, type V3SyncSnapshot, type RealtimeSocket } from '../realtime.js';
import { SwarmTransport } from '../transport.js';

const patch: V3LivePatch = { session_id: 's', run_id: 'r', stream_id: 't', stream_kind: 'assistant_text', operation: 'append', step: 1, step_id: 'step', live_seq_start: 1, live_seq_end: 1, offset_start: 0, offset_end: 2, text: 'é', recorded_at: 1 };
// Purpose: V3LiveText enforces the daemon's UTF-8 offset/sequence contract at the
// narrow reducer boundary, preventing repeated replay text and post-commit overlays.
test('live text deduplicates, rejects gaps, and tombstones committed streams', () => {
  const text = new V3LiveText();
  assert.equal(text.accept(patch), true);
  assert.equal(text.accept(patch), true);
  assert.equal(text.values()[0].text, 'é');
  assert.equal(text.accept({ ...patch, live_seq_start: 3, live_seq_end: 3, offset_start: 2, offset_end: 4 }), false);
  assert.equal(text.values()[0].text, 'é');
  text.durable([{ id: 'e', session_id: 's', seq: 1, event_type: 'session.assistant.delta', ts_unix_ms: 1,
    payload: { run_id: 'r', stream_id: 't', offset_start: 0, offset_end: 2, delta: 'é' } }]);
  assert.equal(text.values()[0].text, 'é');
  text.durable([{ id: 'e2', session_id: 's', seq: 2, event_type: 'session.assistant.delta', ts_unix_ms: 1,
    payload: { run_id: 'r', stream_id: 't', offset_start: 0, offset_end: 3, delta: 'é!' } }]);
  assert.equal(text.values()[0].text, 'é!');
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
  api.bootstrap = async () => snapshot(`opaque-${++boots}`);
  api.hydrate = async () => { hydrates++; return snapshot('hydrate'); };
  const sockets: Socket[] = [];
  const states: string[] = [];
  const watcher = api.watchSession('s', { onChange: (s) => states.push(s.live.map((l) => l.text).join('')), socketFactory: async () => { const socket = new Socket(); sockets.push(socket); return socket; } });
  await new Promise(setImmediate);
  return { api, sockets, states, watcher, counts: () => ({ boots, hydrates }) };
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
    assert.equal(f.counts().boots, 2);
    f.sockets[1].frame({ kind: 'hello', endpoint_cursor: 'head' });
    assert.equal(JSON.parse(f.sockets[1].sent[0]).endpoint_cursor, 'opaque-2');
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
  api.bootstrap = async () => { throw new Error('unexpected I/O'); };
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
