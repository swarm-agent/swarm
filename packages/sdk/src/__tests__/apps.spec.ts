import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import { SwarmClient } from '../client.js';

// Purpose: apps must preserve caller retry IDs/revisions, use owned conversation
// routes, and propagate ownership/revision rejection without retrying or fallback.
// A bounded loopback HTTP fixture is the narrowest SDK serialization test; backend
// ownership and durability are proved separately by Go tests, not this fixture.
test('apps preserve pinned context and never fall back on rejection', { timeout: 10_000 }, async () => {
  const requests: Array<{ url: string; method: string; body: any }> = [];
  const server = http.createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on('data', (chunk) => chunks.push(chunk));
    req.on('end', () => {
      const body = chunks.length ? JSON.parse(Buffer.concat(chunks).toString()) : undefined;
      requests.push({ url: req.url!, method: req.method!, body });
      res.setHeader('Content-Type', 'application/json');
      if (body?.expected_revision === 9 || req.url?.includes('/foreign')) {
        res.statusCode = body?.expected_revision === 9 ? 409 : 404;
        res.end(JSON.stringify({ error: 'rejected' }));
      } else if (req.url?.includes('?revision=')) {
        res.end(JSON.stringify({ session: { id: 'conversation' } }));
      } else {
        res.end(JSON.stringify({ id: 'editor', revision: 1, ok: true }));
      }
    });
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  try {
    const address = server.address() as { port: number };
    const client = new SwarmClient({ baseUrl: `http://127.0.0.1:${address.port}`, timeoutMs: 1000 });
    await client.apps.put('editor', { name: 'Editor', instructions: 'Draft only', context: 'Style', expected_revision: 0 });
    assert.equal(requests[0].method, 'PUT');
    const input = { revision: 1, client_request_id: 'open-1', workspace_id: 'workspace' };
    await client.apps.createConversation('editor', input);
    await client.apps.createConversation('editor', input);
    assert.deepEqual(requests[1], requests[2]);
    assert.equal(requests[1].url, '/v3/application-agents/editor/conversations?revision=1');
    assert.equal(requests[1].body.client_request_id, 'open-1');
    assert.equal(requests[1].body.instructions, undefined);
    await client.apps.send('editor', 'conversation', { content: 'Draft a response', client_request_id: 'event-1' });
    assert.equal(requests[3].body.role, 'user');
    assert.equal(requests[3].body.client_request_id, 'event-1');
    await assert.rejects(client.apps.put('editor', { name: 'Editor', instructions: '', context: '', expected_revision: 9 }));
    await assert.rejects(client.apps.conversation('editor', 'foreign'));
    assert.equal(requests.length, 6);
    await assert.rejects(client.apps.conversation('editor', '../foreign'));
    assert.equal(requests.length, 6);
  } finally {
    server.closeAllConnections();
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

// Purpose: the app facade must address only server-owned app routes and preserve
// task identity/resource IDs. A transport receipt is sufficient for wire shape;
// Go handler tests own authorization, not this stub.
test('app discovery and task/worker links preserve exact wire contracts', async () => {
  const client = new SwarmClient();
  const calls: Array<{ path: string; options: any }> = [];
  client.transport.request = (async (path: string, options: any) => {
    calls.push({ path, options }); return { data: {}, status: 200 };
  }) as typeof client.transport.request;
  await client.apps.list({ limit: 20, cursor: 'opaque value' });
  await client.apps.conversations('editor');
  await client.apps.tasks('editor');
  await client.apps.createTask('editor', { id: 'delivery-1', title: 'Draft', description: 'Brief' });
  await client.apps.worker('editor', 'worker');
  await client.apps.runs('editor', 'worker');
  assert.deepEqual(calls.map(c => c.path), [
    '/v3/application-agents?limit=20&cursor=opaque+value',
    '/v3/application-agents/editor/conversations',
    '/v3/application-agents/editor/tasks',
    '/v3/application-agents/editor/tasks',
    '/v3/application-agents/editor/workers/worker',
    '/v3/application-agents/editor/workers/worker/runs',
  ]);
  assert.equal(calls[3].options.body.id, 'delivery-1');
  assert.equal(calls[3].options.method, 'POST');
  await assert.rejects(client.apps.worker('editor', '../worker'));
  assert.equal(calls.length, 6);
});

// Purpose: linked configuration and deliveries must preserve CAS/retry identity
// and never fall back to unlinked worker routes. Serialization is the narrow SDK layer.
test('app automation configuration and trigger preserve guards', async () => {
  const client = new SwarmClient(); const calls: Array<{ path: string; options: any }> = [];
  client.transport.request = (async (path: string, options: any) => { calls.push({ path, options }); return { data: {} }; }) as typeof client.transport.request;
  const automation = { name: 'Draft', activation_mode: 'manual', plan_document: { title: 'Draft' } };
  await client.apps.configureAutomation('editor', 'worker', 7, automation, 'job');
  assert.equal(calls[0].path, '/v3/application-agents/editor/workers/worker/automations/job');
  assert.equal(calls[0].options.method, 'PUT');
  assert.equal(calls[0].options.body.expected_worker_revision, 7);
  const input = { worker_id: 'worker', automation_id: 'job', idempotency_key: 'delivery-1', payload: { message: 'Draft' } };
  await client.apps.trigger('editor', input); await client.apps.trigger('editor', input);
  assert.deepEqual(calls[1], calls[2]);
  assert.equal(calls[1].options.body.idempotency_key, 'delivery-1');
  await assert.rejects(client.apps.configureAutomation('editor', 'worker', 0, automation));
  await assert.rejects(client.apps.trigger('editor', { ...input, automation_id: '../job' }));
  assert.equal(calls.length, 3);
});
