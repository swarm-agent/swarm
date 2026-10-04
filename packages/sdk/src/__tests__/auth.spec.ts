import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import { SwarmAuthNamespace } from '../auth.js';
import { SwarmOnboardingNamespace } from '../provider-auth.js';
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

test('SwarmCodexAuthNamespace: loginDevice handles start, onCode callback, and polls to success', async () => {
  let pollCount = 0;
  const server = http.createServer((req, res) => {
    if (req.method === 'POST' && req.url === '/v1/auth/codex/oauth/start') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          session_id: 'codex_sess_123',
          provider: 'codex',
          method: 'device',
          active: true,
          verification_url: 'https://openai.com/device',
          user_code: 'ABCD-1234',
          expires_at: Date.now() + 900_000,
          status: 'waiting',
        })
      );
    } else if (req.method === 'GET' && req.url?.startsWith('/v1/auth/codex/oauth/status')) {
      pollCount++;
      res.writeHead(200, { 'Content-Type': 'application/json' });
      if (pollCount === 1) {
        res.end(
          JSON.stringify({
            session_id: 'codex_sess_123',
            provider: 'codex',
            method: 'device',
            active: true,
            status: 'waiting',
          })
        );
      } else {
        res.end(
          JSON.stringify({
            session_id: 'codex_sess_123',
            provider: 'codex',
            method: 'device',
            active: true,
            status: 'success',
            credential: {
              id: 'cred_codex_1',
              provider: 'codex',
              active: true,
              auth_type: 'oauth',
              created_at: Date.now(),
              updated_at: Date.now(),
              storage_mode: 'vault',
            },
          })
        );
      }
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

    let receivedCode: any = null;
    const result = await auth.codex.loginDevice({
      intervalMs: 50,
      onCode: (code) => {
        receivedCode = code;
      },
    });

    assert.ok(receivedCode);
    assert.equal(receivedCode.verification_url, 'https://openai.com/device');
    assert.equal(receivedCode.user_code, 'ABCD-1234');
    assert.equal(result.status, 'success');
    assert.equal(result.credential?.id, 'cred_codex_1');
    assert.ok(pollCount >= 2);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmCodexAuthNamespace: isAuthenticated and getCredential check provider status', async () => {
  const server = http.createServer((req, res) => {
    if (req.method === 'GET' && req.url === '/v1/auth/credentials?provider=codex') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          provider: 'codex',
          total: 1,
          records: [
            {
              id: 'cred_active_1',
              provider: 'codex',
              active: true,
              auth_type: 'oauth',
              created_at: 1000,
              updated_at: 1000,
              storage_mode: 'vault',
              connection: { connected: true },
            },
          ],
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

    const isAuthed = await auth.codex.isAuthenticated();
    assert.equal(isAuthed, true);

    const cred = await auth.codex.getCredential();
    assert.equal(cred?.id, 'cred_active_1');
    assert.equal(cred?.provider, 'codex');
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmOnboardingNamespace: ensureBootstrapped automatically initializes unconfigured daemon', async () => {
  let updateBody: any = null;
  let getCalls = 0;
  const server = http.createServer((req, res) => {
    if (req.method === 'GET' && req.url === '/v1/onboarding') {
      getCalls++;
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          ok: true,
          needs_onboarding: true,
          identity: { bootstrapped: false },
          heuristics: { missing_swarm_name: true, credential_count: 0, agent_count: 0, saved_workspace_count: 0, vault_configured: false },
          config: { swarm_name: '', desktop_onboarding_complete: false, mode: 'local', port: 5555, desktop_port: 0, advertise_port: 0, peer_transport_port: 0 },
          tailscale: {},
        })
      );
    } else if (req.method === 'POST' && req.url === '/v1/onboarding') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        updateBody = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            ok: true,
            needs_onboarding: false,
            identity: { bootstrapped: true, user_id: 'user_1', username: updateBody.username },
            heuristics: { missing_swarm_name: false, credential_count: 0, agent_count: 0, saved_workspace_count: 0, vault_configured: true },
            config: { swarm_name: updateBody.swarm_name, desktop_onboarding_complete: true, mode: 'local', port: 5555, desktop_port: 0, advertise_port: 0, peer_transport_port: 0 },
            tailscale: {},
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
      defaultHeaders: {},
      timeoutMs: 5000,
    });
    const onboarding = new SwarmOnboardingNamespace(transport);

    const result = await onboarding.ensureBootstrapped({ username: 'testuser', swarm_name: 'Custom Swarm' });
    assert.equal(result.needs_onboarding, false);
    assert.equal(result.identity.bootstrapped, true);
    assert.equal(updateBody?.username, 'testuser');
    assert.equal(updateBody?.swarm_name, 'Custom Swarm');
    assert.equal(updateBody?.desktop_onboarding_complete, true);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmAuthNamespace: autoConfigure detects env keys, registers credentials and applies fleet recommendations', async () => {
  let savedCredential: any = null;
  let patchedFleet: any = null;

  const server = http.createServer((req, res) => {
    const url = new URL(req.url || '', 'http://127.0.0.1');

    if (req.method === 'POST' && url.pathname === '/v1/auth/credentials') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        savedCredential = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ id: 'cred_openai', provider: 'openai', active: true }));
      });
    } else if (req.method === 'GET' && url.pathname === '/v1/auth/credentials') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ records: [] }));
    } else if (req.method === 'GET' && url.pathname === '/v1/model/catalog') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          ok: true,
          records: [
            {
              model: 'gpt-4o',
              default_thinking: 'high',
              recommendations: [
                { role: 'auto', thinking: 'high' },
                { role: 'router', thinking: 'low' },
              ],
            },
          ],
        })
      );
    } else if (req.method === 'PATCH' && url.pathname === '/v1/agent-model-settings') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        patchedFleet = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true, agent_model_settings: {} }));
      });
    } else {
      res.writeHead(404);
      res.end();
    }
  });

  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;

  try {
    const transport = new SwarmTransport({ baseUrl: `http://127.0.0.1:${port}`, defaultHeaders: {}, timeoutMs: 5000 });
    const auth = new SwarmAuthNamespace(transport);

    const autoRes = await auth.autoConfigure({
      env: { OPENAI_API_KEY: 'sk-test-secret-12345' },
    });

    assert.equal(autoRes.source, 'env');
    assert.deepEqual(autoRes.configuredProviders, ['openai']);
    assert.equal(autoRes.primaryProvider, 'openai');
    assert.equal(autoRes.fleetApplied, true);
    assert.equal(savedCredential.provider, 'openai');
    assert.equal(savedCredential.api_key, 'sk-test-secret-12345');
    assert.equal(patchedFleet.swarm.action.model, 'gpt-4o');
    assert.equal(patchedFleet.system_agents.router.model, 'gpt-4o');
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});
