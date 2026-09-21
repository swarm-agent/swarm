import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import { SwarmAuthNamespace } from '../auth.js';
import { SwarmTransport } from '../transport.js';

test('SwarmAuthNamespace: bootstrapDesktopSession passes same-origin headers and saves token', async () => {
  let capturedHeaders: http.IncomingHttpHeaders | null = null;

  const server = http.createServer((req, res) => {
    if (req.url === '/v1/auth/desktop/session') {
      capturedHeaders = req.headers;
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          ok: true,
          token: 'jwt_master_attach_token_123',
          user_id: 'user_local_tester',
          account_scope_id: 'acct_primary_1',
        })
      );
    } else {
      res.writeHead(404);
      res.end();
    }
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({
      baseUrl: `http://127.0.0.1:${port}`,
      defaultHeaders: {},
      timeoutMs: 5000,
    });

    const auth = new SwarmAuthNamespace(transport);
    const session = await auth.bootstrapDesktopSession();

    assert.equal(session.token, 'jwt_master_attach_token_123');
    assert.equal(session.user_id, 'user_local_tester');
    assert.equal(session.account_scope_id, 'acct_primary_1');
    assert.equal(capturedHeaders?.['sec-fetch-site'], 'same-origin');
    assert.equal(capturedHeaders?.['origin'], `http://127.0.0.1:${port}`);

    // Verify transport token was automatically updated
    assert.equal(transport.getConfig().token, 'jwt_master_attach_token_123');
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmAuthNamespace: createScopedToken formats payload and parses result', async () => {
  let requestBody: any = null;

  const server = http.createServer((req, res) => {
    if (req.method === 'POST' && req.url === '/v3/auth/tokens') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        requestBody = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            ok: true,
            token: 'swk_live_test_token_abc123',
            record: {
              id: 'tok_0123456789',
              name: requestBody.name,
              scopes: requestBody.scopes,
              token_hint: 'swk_...c123',
              created_at: 1789990000,
            },
          })
        );
      });
    } else {
      res.writeHead(404);
      res.end();
    }
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({
      baseUrl: `http://127.0.0.1:${port}`,
      token: 'jwt_admin_token',
      defaultHeaders: {},
      timeoutMs: 5000,
    });

    const auth = new SwarmAuthNamespace(transport);
    const result = await auth.createScopedToken({
      name: 'CI Trigger Token',
      scopes: ['automations:trigger', 'sessions:read'],
      expires_in_seconds: 3600,
    });

    assert.equal(result.ok, true);
    assert.equal(result.token, 'swk_live_test_token_abc123');
    assert.equal(result.record.id, 'tok_0123456789');
    assert.equal(result.record.name, 'CI Trigger Token');
    assert.deepEqual(result.record.scopes, ['automations:trigger', 'sessions:read']);

    assert.equal(requestBody.name, 'CI Trigger Token');
    assert.deepEqual(requestBody.scopes, ['automations:trigger', 'sessions:read']);
    assert.equal(requestBody.expires_in_seconds, 3600);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmAuthNamespace: listScopedTokens, revokeScopedToken and deleteScopedToken', async () => {
  const server = http.createServer((req, res) => {
    if (req.method === 'GET' && req.url === '/v3/auth/tokens') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          ok: true,
          tokens: [
            {
              id: 'tok_1',
              name: 'Trigger Token',
              token_hint: 'swk_...1111',
              scopes: ['automations:trigger'],
              created_at: 1789990000,
            },
          ],
        })
      );
    } else if (req.method === 'POST' && req.url === '/v3/auth/tokens/tok_1/revoke') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          ok: true,
          record: {
            id: 'tok_1',
            name: 'Trigger Token',
            token_hint: 'swk_...1111',
            scopes: ['automations:trigger'],
            created_at: 1789990000,
            revoked: true,
            revoked_at: 1789990100,
          },
        })
      );
    } else if (req.method === 'DELETE' && req.url === '/v3/auth/tokens/tok_1?purge=true') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ ok: true, deleted: true }));
    } else {
      res.writeHead(404);
      res.end();
    }
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({
      baseUrl: `http://127.0.0.1:${port}`,
      token: 'jwt_admin_token',
      defaultHeaders: {},
      timeoutMs: 5000,
    });

    const auth = new SwarmAuthNamespace(transport);

    const tokens = await auth.listScopedTokens();
    assert.equal(tokens.length, 1);
    assert.equal(tokens[0].id, 'tok_1');

    const revoked = await auth.revokeScopedToken('tok_1');
    assert.equal(revoked.revoked, true);

    const deleted = await auth.deleteScopedToken('tok_1', { purge: true });
    assert.equal(deleted.ok, true);
    assert.equal(deleted.deleted, true);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});
