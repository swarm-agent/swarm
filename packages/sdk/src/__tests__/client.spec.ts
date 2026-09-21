import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import { createSwarmClient, SwarmClient } from '../client.js';

test('SwarmClient: initializes namespaces and aliases properly', () => {
  const client = createSwarmClient({
    baseUrl: 'http://127.0.0.1:18080',
    token: 'swk_my_test_token',
    timeoutMs: 15000,
  });

  assert.ok(client instanceof SwarmClient);
  assert.equal(client.config.baseUrl, 'http://127.0.0.1:18080');
  assert.equal(client.config.token, 'swk_my_test_token');
  assert.equal(client.config.timeoutMs, 15000);

  // Check namespaces
  assert.ok(client.auth);
  assert.ok(client.automations);
  assert.ok(client.workers);
  assert.equal(client.workers, client.automations); // workers is an alias
  assert.ok(client.sessions);
  assert.ok(client.workspaces);
  assert.ok(client.system);

  // Update token
  client.setToken('swk_new_token_456');
  assert.equal(client.config.token, 'swk_new_token_456');
  assert.equal(client.transport.getConfig().token, 'swk_new_token_456');
});

test('SwarmClient: workspaces and system health methods work end-to-end', async () => {
  const server = http.createServer((req, res) => {
    if (req.method === 'GET' && req.url?.startsWith('/v1/workspace/list')) {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          ok: true,
          workspaces: [
            {
              workspace_id: 'ws_test_1',
              name: 'swarm-go',
              path: '/home/roy/swarm-go',
              is_default: true,
            },
          ],
        })
      );
    } else if (req.method === 'GET' && req.url === '/health') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ ok: true, status: 'healthy', version: '0.1.0' }));
    } else {
      res.writeHead(404);
      res.end();
    }
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const client = new SwarmClient({
      baseUrl: `http://127.0.0.1:${port}`,
      token: 'swk_token_123',
    });

    const workspaces = await client.workspaces.list();
    assert.equal(workspaces.length, 1);
    assert.equal(workspaces[0].workspace_id, 'ws_test_1');
    assert.equal(workspaces[0].name, 'swarm-go');

    const health = await client.system.health();
    assert.equal(health.ok, true);
    assert.equal(health.status, 'healthy');
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});
