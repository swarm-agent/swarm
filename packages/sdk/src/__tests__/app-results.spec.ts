import assert from 'node:assert/strict';
import { test } from 'node:test';
import { SwarmClient } from '../client.js';
import type { RealtimeSocket } from '../realtime.js';
class Socket implements RealtimeSocket {
  sent: string[] = []; closed = false;
  listeners = new Map<string, ((event: { data: unknown }) => void)[]>();
  send(data: string) { this.sent.push(data); }
  close() { this.closed = true; }
  addEventListener(type: string, fn: (event: { data: unknown }) => void) { this.listeners.set(type, [...(this.listeners.get(type) ?? []), fn]); }
  frame(frame: object) { for (const fn of this.listeners.get('message') ?? []) fn({ data: JSON.stringify({ protocol: 'v3.realtime', protocol_version: 1, ...frame }) }); }
}
// Purpose: watchResults must subscribe from a pre-read durable cursor, ignore
// unrelated project/session traffic, reauthorize reads and stop emitting on disposal.
// Transport/socket fixtures prove SDK behavior only, not live daemon delivery.
test('app results use durable resume and authorized refresh without polling', { timeout: 3000 }, async () => {
  const client = new SwarmClient(), sockets: Socket[] = [], emitted: unknown[] = [];
  let reads = 0, bootstraps = 0;
  client.transport.request = (async (path: string) => {
    if (path === '/v3/sync/bootstrap') return { data: { realtime: { resume: { protocol: 'v3.realtime', protocol_version: 1, kind: 'resume', endpoint_cursor: `opaque-${++bootstraps}`, worksets: [{ workset_id: 'scope' }], subscriptions: [] } } } };
    if (path.endsWith('/tasks')) return { data: { tasks: [{ title: 'Result' }] } };
    reads++; return { data: { id: 'editor', project_id: 'project', worker_ids: [] } };
  }) as typeof client.transport.request;
  const watch = client.apps.watchResults('editor', { onChange: state => emitted.push(state), socketFactory: async () => { const s = new Socket(); sockets.push(s); return s; } });
  try {
    await new Promise(setImmediate);
    sockets[0].frame({ kind: 'hello', endpoint_cursor: 'later' }); await watch.ready;
    assert.equal(JSON.parse(sockets[0].sent[0]).endpoint_cursor, 'opaque-1');
    assert.deepEqual(JSON.parse(sockets[0].sent[0]).worksets, [{ workset_id: 'scope' }]);
    const before = reads;
    sockets[0].frame({ kind: 'project.updated', project_id: 'foreign' });
    sockets[0].frame({ kind: 'event', session_id: 'foreign' });
    await new Promise(setImmediate); assert.equal(reads, before);
    sockets[0].frame({ kind: 'project.updated', project_id: 'project' });
    await new Promise(setImmediate); assert.equal(reads, before + 1);
    sockets[0].frame({ kind: 'cursor.error' });
    await new Promise(resolve => setTimeout(resolve, 300));
    assert.equal(sockets[0].closed, true); assert.equal(bootstraps, 2);
    sockets[1].frame({ kind: 'hello', endpoint_cursor: 'later' });
    assert.equal(JSON.parse(sockets[1].sent[0]).endpoint_cursor, 'opaque-2');
  } finally { watch.dispose(); await watch.done; }
  const count = emitted.length;
  sockets.at(-1)!.frame({ kind: 'project.updated', project_id: 'project' });
  assert.equal(emitted.length, count); assert.equal(sockets.at(-1)!.closed, true);
});
// Purpose: rejected app ownership must never open a stream or emit a snapshot.
// The SDK gate is tested independently of backend ownership tests.
test('app watches fail closed on ownership rejection', { timeout: 1000 }, async () => {
  const client = new SwarmClient(); let sockets = 0, changes = 0;
  client.transport.request = (async () => { throw Object.assign(new Error('Denied'), { status: 403 }); }) as typeof client.transport.request;
  const options = { onChange: () => { changes++; }, socketFactory: async () => { sockets++; return new Socket(); } };
  const watch = client.apps.watchResults('foreign', options);
  await assert.rejects(watch.done, /Denied/);
  await assert.rejects(client.apps.watchConversation('foreign', 'session', options), /Denied/);
  assert.equal(sockets, 0); assert.equal(changes, 0);
});
