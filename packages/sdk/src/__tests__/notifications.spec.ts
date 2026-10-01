import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import { SwarmClient } from '../client.js';

test('SwarmNotificationsNamespace: submit, list, summary, update, and clear', async () => {
  let submittedPayload: any = null;
  let updatePayload: any = null;
  let clearedQuery: string | null = null;
  let listQuery: string | null = null;

  const mockNotification = {
    id: 'notif_test_1',
    swarm_id: 'test_swarm',
    title: 'Weekly Social Campaign',
    body: '5 tweets drafted and 1 video rendered',
    category: 'inbox',
    kind: 'ai_deliverable',
    severity: 'info',
    status: 'active',
    payload: {
      media_url: 's3://vault/video.mp4',
    },
    actions: [
      {
        id: 'approve',
        label: 'Approve & Schedule',
        action_type: 'publish',
        variant: 'primary',
      },
    ],
    created_at: Date.now(),
    updated_at: Date.now(),
  };

  const server = http.createServer((req, res) => {
    const url = new URL(req.url || '/', 'http://127.0.0.1');

    if (req.method === 'POST' && url.pathname === '/v1/notifications/inbox') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        submittedPayload = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        res.writeHead(201, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            ok: true,
            notification: { ...mockNotification, ...submittedPayload },
          })
        );
      });
      return;
    }

    if (req.method === 'GET' && url.pathname === '/v1/notifications') {
      listQuery = url.search;
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          ok: true,
          notifications: [mockNotification],
        })
      );
      return;
    }

    if (req.method === 'GET' && url.pathname === '/v1/notifications/summary') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          ok: true,
          summary: {
            swarm_id: 'test_swarm',
            total_count: 1,
            unread_count: 1,
            active_count: 1,
            updated_at: Date.now(),
          },
        })
      );
      return;
    }

    if (req.method === 'POST' && url.pathname === '/v1/notifications/notif_test_1') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        updatePayload = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            ok: true,
            notification: { ...mockNotification, read_at: Date.now() },
          })
        );
      });
      return;
    }

    if (req.method === 'POST' && url.pathname === '/v1/notifications/clear') {
      clearedQuery = url.search;
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          ok: true,
          result: { deleted: 1, swarm_id: 'test_swarm' },
        })
      );
      return;
    }

    res.writeHead(404, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ error: 'not found' }));
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const address = server.address() as any;
  const baseUrl = `http://127.0.0.1:${address.port}`;

  try {
    const client = new SwarmClient({ baseUrl, token: 'test_tok' });

    // 1. Submit notification
    const submitted = await client.notifications.submit({
      title: 'Weekly Social Campaign',
      body: '5 tweets drafted and 1 video rendered',
      kind: 'ai_deliverable',
      payload: { media_url: 's3://vault/video.mp4' },
      actions: [
        {
          id: 'approve',
          label: 'Approve & Schedule',
          action_type: 'publish',
          variant: 'primary',
        },
      ],
    });
    assert.equal(submitted.title, 'Weekly Social Campaign');
    assert.equal(submitted.kind, 'ai_deliverable');
    assert.equal(submittedPayload.kind, 'ai_deliverable');

    // 2. Test alias client.inbox
    assert.equal(client.inbox, client.notifications);

    // 3. List notifications
    const list = await client.notifications.list({ limit: 10, swarm_id: 'test_swarm' });
    assert.equal(list.length, 1);
    assert.match(listQuery || '', /limit=10/);
    assert.match(listQuery || '', /swarm_id=test_swarm/);

    // 4. Summary
    const summary = await client.notifications.summary();
    assert.equal(summary.total_count, 1);
    assert.equal(summary.unread_count, 1);

    // 5. Update
    const updated = await client.notifications.update('notif_test_1', { read: true });
    assert.ok(updated.read_at);
    assert.equal(updatePayload.read, true);

    // 6. Clear
    const cleared = await client.notifications.clear('test_swarm');
    assert.equal(cleared.deleted, 1);
    assert.match(clearedQuery || '', /swarm_id=test_swarm/);
  } finally {
    server.close();
  }
});
