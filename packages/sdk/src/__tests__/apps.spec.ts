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
