import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import { SwarmDeliverablesNamespace } from '../deliverables.js';
import { SwarmTransport } from '../transport.js';

test('SwarmDeliverablesNamespace: submit, list, get, approve, and dismiss lifecycle', async () => {
  let submittedPayload: any = null;
  let approvedId: string | null = null;
  let dismissedId: string | null = null;
  let listQuery: string | null = null;

  const mockDeliverable = {
    id: 'deliv_test_123',
    account_id: 'acct_1',
    worker_id: 'worker_social',
    title: 'Thread on Swarm Launch',
    kind: 'social_post',
    status: 'pending_review',
    payload: {
      posts: [
        { text: '1/2 Announcement...' },
        { text: '2/2 Swarm is ready.' },
      ],
    },
    action_contract: {
      action: 'publish_x_post',
      target_secret_ref: 'gcp:x_secret',
    },
    created_at: Date.now(),
    updated_at: Date.now(),
  };

  const server = http.createServer((req, res) => {
    const url = new URL(req.url || '/', 'http://127.0.0.1');

    if (req.method === 'POST' && url.pathname === '/v3/deliverables') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        submittedPayload = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        res.writeHead(201, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ deliverable: { ...mockDeliverable, ...submittedPayload } }));
      });
      return;
    }

    if (req.method === 'GET' && url.pathname === '/v3/deliverables') {
      listQuery = url.search;
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ deliverables: [mockDeliverable], count: 1 }));
      return;
    }

    if (req.method === 'GET' && url.pathname === '/v3/deliverables/deliv_test_123') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ deliverable: mockDeliverable }));
      return;
    }

    if (req.method === 'POST' && url.pathname === '/v3/deliverables/deliv_test_123/approve') {
      approvedId = 'deliv_test_123';
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          deliverable: { ...mockDeliverable, status: 'published' },
          action_result: { published_to: 'x', post_count: 2 },
        })
      );
      return;
    }

    if (req.method === 'POST' && url.pathname === '/v3/deliverables/deliv_test_123/dismiss') {
      dismissedId = 'deliv_test_123';
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ deliverable: { ...mockDeliverable, status: 'dismissed' } }));
      return;
    }

    if (req.method === 'DELETE' && url.pathname === '/v3/deliverables/deliv_test_123') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ ok: true, deleted_id: 'deliv_test_123' }));
      return;
    }

    res.writeHead(404);
    res.end();
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({
      baseUrl: `http://127.0.0.1:${port}`,
      token: 'test_token',
      defaultHeaders: {},
      timeoutMs: 5000,
    });
    const deliverables = new SwarmDeliverablesNamespace(transport);

    // 1. Submit
    const created = await deliverables.submit({
      title: 'Thread on Swarm Launch',
      kind: 'social_post',
      worker_id: 'worker_social',
      payload: { posts: [{ text: '1/2...' }, { text: '2/2...' }] },
    });
    assert.equal(created.title, 'Thread on Swarm Launch');
    assert.equal(submittedPayload.title, 'Thread on Swarm Launch');

    // 2. List with filter
    const list = await deliverables.list({ status: 'pending_review', worker_id: 'worker_social' });
    assert.equal(list.length, 1);
    assert.ok(listQuery?.includes('status=pending_review'));
    assert.ok(listQuery?.includes('worker_id=worker_social'));

    // 3. Get
    const fetched = await deliverables.get('deliv_test_123');
    assert.equal(fetched.id, 'deliv_test_123');

    // 4. Approve
    const approveResult = await deliverables.approve('deliv_test_123');
    assert.equal(approvedId, 'deliv_test_123');
    assert.equal(approveResult.deliverable.status, 'published');
    assert.equal(approveResult.action_result?.published_to, 'x');

    // 5. Dismiss
    const dismissed = await deliverables.dismiss('deliv_test_123');
    assert.equal(dismissedId, 'deliv_test_123');
    assert.equal(dismissed.status, 'dismissed');

    // 6. Delete
    await deliverables.delete('deliv_test_123');
  } finally {
    server.close();
  }
});
