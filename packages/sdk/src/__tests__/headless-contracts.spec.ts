import assert from 'node:assert/strict';
import { test } from 'node:test';
import http from 'node:http';
import { once } from 'node:events';
import { SwarmClient } from '../client.js';

// Purpose: typed administrative namespaces must preserve the canonical daemon
// routes, request fields and envelopes; failures must not trigger alternate APIs.
// A loopback HTTP fixture is the narrow transport-contract layer, not live auth proof.
test('headless settings/auth/workspace requests preserve canonical wire contracts', { timeout: 5000 }, async () => {
  const requests: { path: string; method: string; body: Record<string, unknown> }[] = [];
  const server = http.createServer(async (req, res) => {
    let raw = ''; for await (const chunk of req) raw += chunk;
    const body = raw ? JSON.parse(raw) : {};
    requests.push({ path: req.url!, method: req.method!, body });
    res.setHeader('Content-Type', 'application/json');
    if (req.url === '/v1/auth/credentials/active') { res.writeHead(403); res.end('{"error":"forbidden"}'); return; }
    const data = req.url === '/v1/providers' ? { providers: [{ id: 'catalog-provider', auth_methods: [{ id: 'device', label: 'Device' }] }] }
      : req.url?.startsWith('/v1/model/catalog?') ? { records: [{ model: 'catalog-model' }] }
      : req.url === '/v1/workspace/folders/create' ? { folder: { path: '/workspace/app' } }
      : req.url === '/v1/workspace/add' ? { workspace: { workspace_path: '/workspace/app' } }
      : req.url === '/v1/agent-model-settings' ? { agent_model_settings: { swarm: body.swarm } }
      : req.url?.includes('/oauth/') ? { session_id: 'login', status: 'waiting', method: body.method }
      : { id: 'credential', auth_type: 'api_key' };
    res.end(JSON.stringify(data));
  });
  server.listen(0, '127.0.0.1'); await once(server, 'listening');
  const address = server.address(); assert.ok(address && typeof address !== 'string');
  const client = new SwarmClient({ baseUrl: `http://127.0.0.1:${address.port}` });
  try {
    assert.equal((await client.settings.providers())[0].id, 'catalog-provider');
    assert.equal((await client.settings.models('provider with space')).records[0].model, 'catalog-model');
    await client.settings.setSwarmModel('action', { provider: 'chosen', model: 'chosen-model', thinking: 'chosen-level' });
    assert.deepEqual(requests.at(-1)?.body, { swarm: { action: { provider: 'chosen', model: 'chosen-model', thinking: 'chosen-level' } } });
    await client.auth.credentials.save({ provider: 'chosen', type: 'api_key', api_key: 'fixture-not-a-secret', active: true });
    assert.equal(requests.at(-1)?.path, '/v1/auth/credentials');
    for (const method of ['device', 'manual', 'browser'] as const) {
      assert.equal((await client.auth.codex.start({ method, active: true })).method, method);
      assert.deepEqual(requests.at(-1)?.body, { provider: 'codex', method, active: true });
    }
    await client.auth.codex.complete('login', 'callback-fixture');
    assert.deepEqual(requests.at(-1)?.body, { session_id: 'login', callback_input: 'callback-fixture' });
    await client.auth.codex.status('login & encoded');
    assert.match(requests.at(-1)!.path, /session_id=login\+%26\+encoded/);
    assert.equal((await client.workspaces.createFolder('/workspace', 'app')).path, '/workspace/app');
    await client.workspaces.setupRepository('/workspace/app', '/workspace/app');
    assert.deepEqual(requests.at(-1)?.body, { path: '/workspace/app', expected_resolved_path: '/workspace/app' });
    assert.equal((await client.workspaces.add({ path: '/workspace/app', make_current: false })).workspace_path, '/workspace/app');
    const before = requests.length;
    await assert.rejects(client.auth.credentials.activate('chosen', 'credential'), /forbidden/);
    assert.equal(requests.length, before + 1);
  } finally { server.closeAllConnections(); await new Promise<void>((resolve) => server.close(() => resolve())); }
});
