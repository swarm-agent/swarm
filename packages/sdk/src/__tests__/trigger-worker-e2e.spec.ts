import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import { SwarmClient } from '../client.js';

test('E2E Worker Trigger, Token Association, Session Route, and Mailbox Delivery', async () => {
  let createdTokenPayload: any = null;
  let triggerPayload: any = null;
  let deliverablePayload: any = null;

  const mockTokens = [
    {
      id: 'tok_test123',
      name: 'CI Trigger Token',
      token_hint: 'swk_test...89ab',
      scopes: ['automations:trigger'],
      worker_id: 'av2_mailbox_notifier',
      worker_name: 'Trigger Worker: Mailbox Notifier',
      created_at: 1789990000,
      expires_at: 1792582000,
      revoked: false,
    },
  ];

  const mockDeliverables = [
    {
      id: 'deliv_occ_82f15a8329cdcc8c',
      account_id: 'acct_main',
      workspace_id: 'ws_test',
      worker_id: 'av2_mailbox_notifier',
      occurrence_id: '82f15a8329cdcc8c',
      session_id: 'av2-execution-82f15a8329cdcc8c',
      title: 'Worker Trigger Deliverable: Mailbox Test',
      kind: 'report',
      status: 'pending_review',
      summary: 'Trigger job executed and verified clean delivery.',
      media_refs: [],
      payload: {
        closing_state: 'deliverable_ready',
        report: 'Mailbox delivery verified end to end.',
        result: 'done',
      },
      created_at: 1789991000,
    },
  ];

  const server = http.createServer((req, res) => {
    const url = new URL(req.url ?? '/', `http://${req.headers.host}`);

    if (req.method === 'POST' && url.pathname === '/v3/auth/tokens') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        createdTokenPayload = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            ok: true,
            token: 'swk_live_test_secret_key_1234567890',
            record: {
              id: 'tok_live_01',
              name: createdTokenPayload.name,
              token_hint: 'swk_live...7890',
              scopes: createdTokenPayload.scopes,
              worker_id: createdTokenPayload.worker_id,
              worker_name: createdTokenPayload.worker_name,
              created_at: Date.now(),
              expires_at: Date.now() + 2592000000,
              revoked: false,
            },
          })
        );
      });
      return;
    }

    if (req.method === 'GET' && url.pathname === '/v3/auth/tokens') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ ok: true, tokens: mockTokens }));
      return;
    }

    if (req.method === 'POST' && url.pathname === '/v3/automations/v2/trigger') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        triggerPayload = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            ok: true,
            occurrence: {
              id: 'occ_82f15a8329cdcc8c',
              session_id: 'av2-execution-82f15a8329cdcc8c',
              state: 'running',
              due_at: Date.now(),
              admitted_at: Date.now(),
              accepted: {
                automation_id: triggerPayload.worker_id,
                generation: 1,
              },
            },
          })
        );
      });
      return;
    }

    if (req.method === 'GET' && url.pathname === '/v3/deliverables') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ ok: true, deliverables: mockDeliverables }));
      return;
    }

    res.writeHead(404);
    res.end();
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const client = new SwarmClient({
      baseUrl: `http://127.0.0.1:${port}`,
      token: 'admin_bootstrap_token',
    });

    // 1. Mint a scoped token tied to a specific worker
    const mintRes = await client.auth.createScopedToken({
      name: 'Test Trigger Key',
      scopes: ['automations:trigger'],
      worker_id: 'av2_mailbox_notifier',
      worker_name: 'Trigger Worker: Mailbox Notifier',
      expires_in_seconds: 2592000,
    });

    assert.equal(mintRes.ok, true);
    assert.equal(mintRes.record.name, 'Test Trigger Key');
    assert.equal(mintRes.record.worker_id, 'av2_mailbox_notifier');
    assert.equal(mintRes.record.worker_name, 'Trigger Worker: Mailbox Notifier');
    assert.equal(createdTokenPayload.worker_id, 'av2_mailbox_notifier');
    assert.equal(createdTokenPayload.worker_name, 'Trigger Worker: Mailbox Notifier');

    // 2. List scoped tokens and verify tied job metadata is exposed
    const tokens = await client.auth.listScopedTokens();
    assert.equal(tokens.length, 1);
    assert.equal(tokens[0].worker_id, 'av2_mailbox_notifier');
    assert.equal(tokens[0].worker_name, 'Trigger Worker: Mailbox Notifier');

    // 3. Trigger worker via client.automations.trigger
    const triggerRes = await client.automations.trigger({
      workspace_id: 'ws_test',
      worker_id: 'av2_mailbox_notifier',
      context: {
        caller: 'github_actions',
        action: 'test_mailbox_delivery',
      },
    });

    assert.equal(triggerRes.ok, true);
    assert.equal(triggerRes.occurrence.accepted?.automation_id, 'av2_mailbox_notifier');
    assert.equal(triggerRes.occurrence.session_id, 'av2-execution-82f15a8329cdcc8c');
    assert.equal(triggerPayload.worker_id, 'av2_mailbox_notifier');
    assert.equal(triggerPayload.context.action, 'test_mailbox_delivery');

    // 4. Verify deliverables exist in mailbox
    const deliverables = await client.deliverables.list();
    assert.equal(deliverables.length, 1);
    assert.equal(deliverables[0].worker_id, 'av2_mailbox_notifier');
    assert.equal(deliverables[0].session_id, 'av2-execution-82f15a8329cdcc8c');
    assert.equal(deliverables[0].payload.closing_state, 'deliverable_ready');
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});
