import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import { SwarmSessionsNamespace } from '../sessions.js';
import { SwarmTransport } from '../transport.js';

test('SwarmSessionsNamespace: creates session with generated client_request_id and default agent', async () => {
  let requestBody: any = null;

  const server = http.createServer((req, res) => {
    if (req.method === 'POST' && req.url === '/v3/sessions') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        requestBody = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            ok: true,
            session: {
              id: 'sess_created_9999',
              title: requestBody.title,
              agent_name: requestBody.agent_name,
              mode: requestBody.mode,
              workspace_path: requestBody.workspace_path,
              created_at: 1789990000,
              updated_at: 1789990000,
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

    const sessions = new SwarmSessionsNamespace(transport);
    const session = await sessions.create({
      title: 'Analyze Code Reliability',
      workspace_path: '/home/roy/swarm-go',
    });

    assert.equal(session.id, 'sess_created_9999');
    assert.equal(session.title, 'Analyze Code Reliability');
    assert.equal(requestBody.agent_name, 'swarm');
    assert.equal(requestBody.mode, 'auto');
    assert.ok(requestBody.client_request_id.startsWith('sdk-session-'));
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmSessionsNamespace: list, get, archive, unarchive, delete, sendMessage and cancelRuns', async () => {
  let lastMethod = '';
  let lastUrl = '';
  let lastBody: any = null;

  const server = http.createServer((req, res) => {
    lastMethod = req.method || '';
    lastUrl = req.url || '';
    const chunks: Buffer[] = [];
    req.on('data', (c) => chunks.push(c));
    req.on('end', () => {
      const bodyText = Buffer.concat(chunks).toString('utf8');
      try {
        lastBody = bodyText ? JSON.parse(bodyText) : null;
      } catch {
        lastBody = bodyText;
      }

      if (lastMethod === 'GET' && lastUrl.startsWith('/v3/sessions?')) {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            ok: true,
            sessions: [
              {
                id: 'sess_1',
                title: 'Test Session',
                state: 'idle',
                created_at: 1789990000,
                updated_at: 1789990000,
              },
            ],
          })
        );
      } else if (lastMethod === 'GET' && lastUrl === '/v3/sessions/sess_1') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            id: 'sess_1',
            title: 'Test Session',
            state: 'idle',
            created_at: 1789990000,
            updated_at: 1789990000,
          })
        );
      } else if (lastMethod === 'POST' && lastUrl === '/v3/sessions/sess_1/archive') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true }));
      } else if (lastMethod === 'POST' && lastUrl === '/v3/sessions:unarchive') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true }));
      } else if (lastMethod === 'DELETE' && lastUrl === '/v3/sessions/sess_1') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true }));
      } else if (lastMethod === 'POST' && lastUrl === '/v3/sessions/sess_1/messages') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true, message_id: 'msg_123' }));
      } else if (lastMethod === 'POST' && lastUrl === '/v3/sessions/sess_1/run/stop') {
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ ok: true }));
      } else {
        res.writeHead(404);
        res.end();
      }
    });
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

    const sessions = new SwarmSessionsNamespace(transport);

    // List
    const list = await sessions.list({ limit: 10, category: 'active_chats' });
    assert.equal(list.length, 1);
    assert.equal(list[0].id, 'sess_1');
    assert.ok(lastUrl.includes('limit=10'));
    assert.ok(lastUrl.includes('category=active_chats'));

    // Get
    const session = await sessions.get('sess_1');
    assert.equal(session.id, 'sess_1');

    // Messages
    const msg = await sessions.sendMessage('sess_1', { content: 'hello agent' });
    assert.equal(msg.ok, true);
    assert.equal(lastBody.content, 'hello agent');

    // Run stop
    const stopped = await sessions.stopRun('sess_1', { run_id: 'run_123' });
    assert.equal(stopped, true);
    assert.equal(lastBody.run_id, 'run_123');
    assert.equal(lastBody.target_swarm_id, 'self');

    // Archive
    const archived = await sessions.archive('sess_1');
    assert.equal(archived, true);

    // Unarchive
    const unarchived = await sessions.unarchive('sess_1');
    assert.equal(unarchived, true);
    assert.equal(lastBody.session_id, 'sess_1');

    // Delete
    const deleted = await sessions.delete('sess_1');
    assert.equal(deleted, true);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

test('SwarmSessionsNamespace: waitForRun polls until completion', async () => {
  let pollCount = 0;

  const server = http.createServer((req, res) => {
    if (req.method === 'GET' && req.url === '/v3/sessions/sess_running') {
      pollCount++;
      const state = pollCount < 3 ? 'in_progress' : 'completed';
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          id: 'sess_running',
          title: 'Poll Test Session',
          state,
          created_at: 1789990000,
          updated_at: 1789990000,
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
      token: 'jwt_admin_token',
      defaultHeaders: {},
      timeoutMs: 5000,
    });

    const sessions = new SwarmSessionsNamespace(transport);
    const completed = await sessions.waitForRun('sess_running', {
      timeoutMs: 3000,
      pollIntervalMs: 50,
    });

    assert.equal(completed.state, 'completed');
    assert.ok(pollCount >= 3);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});
