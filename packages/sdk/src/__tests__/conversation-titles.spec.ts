import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import { SwarmSessionsNamespace } from '../sessions.js';
import { SwarmProjectsNamespace } from '../projects.js';
import { SwarmChatNamespace } from '../chat.js';
import { SwarmTransport } from '../transport.js';

async function withServer(handler: (req: http.IncomingMessage, body: any, res: http.ServerResponse) => void, run: (transport: SwarmTransport) => Promise<void>) {
  const server = http.createServer((req, res) => {
    const chunks: Buffer[] = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      const text = Buffer.concat(chunks).toString('utf8');
      handler(req, text ? JSON.parse(text) : null, res);
    });
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', () => resolve()));
  try {
    const port = (server.address() as any).port;
    await run(new SwarmTransport({ baseUrl: `http://127.0.0.1:${port}`, token: 'test-token', timeoutMs: 5000 }));
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
}

const json = (res: http.ServerResponse, status: number, body: unknown) => {
  res.writeHead(status, { 'Content-Type': 'application/json' });
  res.end(JSON.stringify(body));
};

// Requirement: apps can rename a conversation through Swarm instead of storing their own
// labels. sessions.rename / projects.renameConversation call the owner-scoped
// POST /v3/sessions/{id}/title (sessions_v3_primary.go handleSessionV3PrimaryTitle) with a
// trimmed title and an idempotency key, and reject empty or over-long titles locally
// without a request. Boundary: SDK request shaping against a recording HTTP server.
test('sessions.rename and projects.renameConversation post the owner title route', async () => {
  const requests: Array<{ method?: string; url?: string; body: any }> = [];
  await withServer((req, body, res) => {
    requests.push({ method: req.method, url: req.url, body });
    if (req.method === 'POST' && req.url?.endsWith('/title')) {
      return json(res, 200, { ok: true, session_id: decodeURIComponent(req.url.split('/')[3]), title: body.title });
    }
    json(res, 404, { ok: false, error: 'not found' });
  }, async (transport) => {
    const sessions = new SwarmSessionsNamespace(transport);
    const renamed = await sessions.rename('sess 1', '  Launch plan  ', { clientRequestId: 'rename-1' });
    assert.deepEqual(renamed, { ok: true, session_id: 'sess 1', title: 'Launch plan' });
    assert.deepEqual(requests[0], { method: 'POST', url: '/v3/sessions/sess%201/title', body: { title: 'Launch plan', client_request_id: 'rename-1' } });

    const projects = new SwarmProjectsNamespace(transport);
    const viaProject = await projects.renameConversation('conv_2', 'Ideas');
    assert.equal(viaProject.title, 'Ideas');
    assert.equal(requests[1].url, '/v3/sessions/conv_2/title');
    assert.ok(String(requests[1].body.client_request_id).startsWith('sdk-session-'));

    await assert.rejects(sessions.rename('s', '   '), /title is required/);
    await assert.rejects(sessions.rename('s', 'x'.repeat(201)), /200 characters/);
    await assert.rejects(sessions.rename(' ', 'ok'), /sessionId is required/);
    assert.equal(requests.length, 2, 'invalid renames must not reach the daemon');
  });
});

// Requirement: a conversation created without a title must let Swarm name it from the
// first message. Threat: the SDK sent placeholder titles ("Orchestrator AI", "Chat Session")
// that disabled Swarm's titling, so every app stored its own labels. listConversations
// must return the daemon's title. Boundary: SDK request shaping.
test('conversations are created without placeholder titles and list their titles', async () => {
  const bodies: any[] = [];
  await withServer((req, body, res) => {
    if (req.method === 'POST' && req.url === '/v3/projects/p1/sessions') {
      bodies.push(body);
      return json(res, 200, { ok: true, session: { id: 'c1', title: 'New Session', agent_name: 'system-orchestrator' } });
    }
    if (req.method === 'POST' && req.url === '/v3/sessions') {
      bodies.push(body);
      return json(res, 200, { ok: true, session: { id: 's1', title: 'New Session' } });
    }
    if (req.method === 'GET' && req.url === '/v3/projects/p1/sessions') {
      return json(res, 200, { sessions: [{ session: { id: 'c1', title: 'Plan the launch checklist' }, projection: { session_id: 'c1' } }] });
    }
    json(res, 404, { ok: false });
  }, async (transport) => {
    const projects = new SwarmProjectsNamespace(transport);
    await projects.createConversation('p1');
    await new SwarmChatNamespace(transport).createSession();
    assert.equal('title' in bodies[0] ? bodies[0].title : undefined, undefined);
    assert.equal('title' in bodies[1] ? bodies[1].title : undefined, undefined);
    await projects.createConversation('p1', { title: 'Mine' });
    assert.equal(bodies[2].title, 'Mine');
    const listed = await projects.listConversations('p1');
    assert.equal(listed[0].title, 'Plan the launch checklist');
  });
});
