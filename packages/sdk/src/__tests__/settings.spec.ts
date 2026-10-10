import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import { SwarmSettingsNamespace } from '../settings.js';
import { SwarmTransport } from '../transport.js';

test('SwarmSettingsNamespace: getRecommendationsForProvider and applyProviderFleet', async () => {
  let patchBody: any = null;

  const server = http.createServer((req, res) => {
    const url = new URL(req.url || '', 'http://127.0.0.1');

    if (req.method === 'GET' && url.pathname === '/v1/model/catalog') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          ok: true,
          provider: 'codex',
          count: 3,
          records: [
            {
              model: 'gpt-6.1-sol',
              default_thinking: 'high',
              recommendations: [
                { role: 'auto', thinking: 'high' },
                { role: 'coder', thinking: 'medium' },
              ],
            },
            {
              model: 'gpt-6-astra',
              default_thinking: 'high',
              recommendations: [
                { role: 'plan', thinking: 'high' },
                { role: 'designer', thinking: 'low' },
              ],
            },
            {
              model: 'gpt-6-luna',
              default_thinking: 'medium',
              recommendations: [
                { role: 'utility', thinking: 'high' },
                { role: 'router', thinking: 'low' },
                { role: 'compact', thinking: 'medium' },
                { role: 'finder', thinking: 'medium' },
              ],
            },
          ],
          catalog_status: { configured: true },
        })
      );
    } else if (req.method === 'PATCH' && url.pathname === '/v1/agent-model-settings') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        patchBody = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            ok: true,
            agent_model_settings: {
              account_scope_id: 'acct_1',
              updated_at: 5000,
              swarm: patchBody.swarm,
              system_agents: patchBody.system_agents,
            },
            roles: [],
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
    const transport = new SwarmTransport({ baseUrl: `http://127.0.0.1:${port}`, defaultHeaders: {}, timeoutMs: 5000 });
    const settings = new SwarmSettingsNamespace(transport);

    const recs = await settings.getRecommendationsForProvider('codex');
    assert.equal(recs.swarm.action.model, 'gpt-6.1-sol');
    assert.equal(recs.swarm.action.thinking, 'high');
    assert.equal(recs.swarm.plan.model, 'gpt-6-astra');
    assert.equal(recs.swarm.plan.thinking, 'high');
    assert.equal(recs.system_agents.coder.model, 'gpt-6.1-sol');
    assert.equal(recs.system_agents.coder.thinking, 'medium');
    assert.equal(recs.system_agents.designer.model, 'gpt-6-astra');
    assert.equal(recs.system_agents.designer.thinking, 'low');
    assert.equal(recs.system_agents.router.model, 'gpt-6-luna');
    assert.equal(recs.system_agents.router.thinking, 'low');
    assert.equal(recs.system_agents.finder.model, 'gpt-6-luna');
    assert.equal(recs.system_agents.compact.model, 'gpt-6-luna');

    const result = await settings.applyProviderFleet('codex');
    assert.equal(result.ok, true);
    assert.equal(patchBody.swarm.action.model, 'gpt-6.1-sol');
    assert.equal(patchBody.system_agents.router.model, 'gpt-6-luna');
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmSettingsNamespace: restoreDefaults fetches current updated_at and restores', async () => {
  let expectedUpdatedAtSent: number | null = null;

  const server = http.createServer((req, res) => {
    const url = new URL(req.url || '', 'http://127.0.0.1');

    if (req.method === 'GET' && url.pathname === '/v1/agent-model-settings') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          ok: true,
          agent_model_settings: {
            account_scope_id: 'acct_1',
            updated_at: 7777,
            swarm: { action: { provider: 'codex', model: 'old', thinking: 'off' }, plan: { provider: 'codex', model: 'old', thinking: 'off' } },
            system_agents: {},
          },
          roles: [],
        })
      );
    } else if (req.method === 'POST' && url.pathname === '/v1/agent-model-settings/restore-defaults') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        const body = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        expectedUpdatedAtSent = body.expected_updated_at;
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            ok: true,
            agent_model_settings: {
              account_scope_id: 'acct_1',
              updated_at: 8888,
              swarm: { action: { provider: 'codex', model: 'gpt-6.1-sol', thinking: 'high' } },
              system_agents: {},
            },
            roles: [],
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
    const transport = new SwarmTransport({ baseUrl: `http://127.0.0.1:${port}`, defaultHeaders: {}, timeoutMs: 5000 });
    const settings = new SwarmSettingsNamespace(transport);

    const restored = await settings.restoreDefaults();
    assert.equal(restored.ok, true);
    assert.equal(expectedUpdatedAtSent, 7777);
    assert.equal(restored.agent_model_settings.updated_at, 8888);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

// Purpose: on an account whose provider is connected but whose model settings
// were never created, the daemon answers the settings read with 404. Before,
// restoreDefaults threw that 404 and never asked the daemon to set the models
// up, so the headless setup app stayed stuck on "agent model settings not
// found". It must restore from revision 0 instead, and still surface any other
// read failure. Boundary: SwarmSettingsNamespace over a real HTTP transport.
test('SwarmSettingsNamespace: restoreDefaults sets up models on a fresh account', async () => {
  let expectedUpdatedAtSent: number | null = null;
  let readStatus = 404;
  const server = http.createServer((req, res) => {
    const url = new URL(req.url || '', 'http://127.0.0.1');
    if (req.method === 'GET' && url.pathname === '/v1/agent-model-settings') {
      res.writeHead(readStatus, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ ok: false, error: readStatus === 404 ? 'agent model settings not found' : 'boom' }));
    } else if (req.method === 'POST' && url.pathname === '/v1/agent-model-settings/restore-defaults') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        expectedUpdatedAtSent = JSON.parse(Buffer.concat(chunks).toString('utf8')).expected_updated_at;
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true, agent_model_settings: { account_scope_id: 'acct_1', updated_at: 1, swarm: {}, system_agents: {} }, roles: [] }));
      });
    } else {
      res.writeHead(404);
      res.end();
    }
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  const port = (server.address() as any).port;
  try {
    const settings = new SwarmSettingsNamespace(new SwarmTransport({ baseUrl: `http://127.0.0.1:${port}`, defaultHeaders: {}, timeoutMs: 5000 }));
    const restored = await settings.restoreDefaults();
    assert.equal(restored.ok, true);
    assert.equal(expectedUpdatedAtSent, 0);

    expectedUpdatedAtSent = null;
    readStatus = 500;
    await assert.rejects(settings.restoreDefaults());
    assert.equal(expectedUpdatedAtSent, null, 'a failed read must not restore');
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});
