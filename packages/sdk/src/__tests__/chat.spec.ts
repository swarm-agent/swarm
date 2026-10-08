import assert from 'node:assert/strict';
import http from 'node:http';
import { test } from 'node:test';
import { SwarmChatNamespace } from '../chat.js';
import { SwarmTransport } from '../transport.js';

test('SwarmChatNamespace: createSession, sendMessage, and listMessages', async () => {
  let createdBody: any = null;
  let sentBody: any = null;

  const server = http.createServer((req, res) => {
    const url = new URL(req.url || '', 'http://127.0.0.1');

    if (req.method === 'POST' && url.pathname === '/v3/sessions') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        createdBody = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            ok: true,
            session: {
              id: 'sess_chat_100',
              title: createdBody.title,
              mode: createdBody.mode,
              agent_name: createdBody.agent_name,
              created_at: 1000,
              updated_at: 1000,
            },
          })
        );
      });
    } else if (req.method === 'POST' && url.pathname === '/v3/sessions/sess_chat_100/messages') {
      const chunks: Buffer[] = [];
      req.on('data', (c) => chunks.push(c));
      req.on('end', () => {
        sentBody = JSON.parse(Buffer.concat(chunks).toString('utf8'));
        res.writeHead(200, { 'Content-Type': 'application/json' });
        res.end(
          JSON.stringify({
            ok: true,
            message: {
              id: 'msg_1',
              session_id: 'sess_chat_100',
              role: sentBody.role,
              content: sentBody.content,
              created_at: 1050,
            },
          })
        );
      });
    } else if (req.method === 'GET' && url.pathname === '/v3/sessions/sess_chat_100') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(
        JSON.stringify({
          session: {
            id: 'sess_chat_100',
            title: 'Chat Session',
            state: 'idle',
            created_at: 1000,
            updated_at: 1100,
          },
          messages: [
            { id: 'msg_user_1', role: 'user', content: 'Hello' },
            { id: 'msg_asst_1', role: 'assistant', content: 'Hi there! How can I help?' },
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
    const transport = new SwarmTransport({ baseUrl: `http://127.0.0.1:${port}`, defaultHeaders: {}, timeoutMs: 5000 });
    const chat = new SwarmChatNamespace(transport);

    const session = await chat.createSession({ title: 'My Chat' });
    assert.equal(session.id, 'sess_chat_100');
    assert.equal(createdBody.mode, 'auto');
    assert.equal(createdBody.title, 'My Chat');

    await chat.sendMessage('sess_chat_100', 'Hello');
    assert.equal(sentBody.content, 'Hello');
    assert.equal(sentBody.role, 'user');

    const messages = await chat.listMessages('sess_chat_100');
    assert.equal(messages.length, 2);
    assert.equal(messages[1].role, 'assistant');
    assert.equal(messages[1].content, 'Hi there! How can I help?');
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

// Requirement: explicit auto approval never invents a user answer, even when the session is idle.
// Threat: first-option selection silently authorizes a question. Boundary: chat.run + HTTP adapter.
test('SwarmChatNamespace: auto approval leaves ask-user pending with actionable timeout', { timeout: 2000 }, async () => {
  let pendingCount = 1;
  let resolvedPermissionId: string | null = null;
  let getSessionCalls = 0;

  const server = http.createServer((req, res) => {
    const url = new URL(req.url || '', 'http://127.0.0.1');

    if (req.method === 'POST' && url.pathname === '/v3/sessions/sess_chat_200/messages') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ ok: true, message: { id: 'msg_sent' } }));
    } else if (req.method === 'GET' && url.pathname === '/v3/sessions/sess_chat_200/permissions') {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      if (pendingCount > 0) {
        res.end(
          JSON.stringify({
            ok: true,
            count: 1,
            permissions: [
              {
                id: 'perm_ask_1',
                session_id: 'sess_chat_200',
                tool_name: 'ask-user',
                tool_arguments: JSON.stringify({ question: 'Proceed?', options: ['Yes', 'No'] }),
                status: 'pending',
              },
            ],
          })
        );
      } else {
        res.end(JSON.stringify({ ok: true, count: 0, permissions: [] }));
      }
    } else if (req.method === 'POST' && url.pathname === '/v3/sessions/sess_chat_200/permissions/perm_ask_1/resolve') {
      pendingCount = 0;
      resolvedPermissionId = 'perm_ask_1';
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ ok: true, session_id: 'sess_chat_200' }));
    } else if (req.method === 'GET' && url.pathname === '/v3/sessions/sess_chat_200') {
      getSessionCalls++;
      res.writeHead(200, { 'Content-Type': 'application/json' });
      if (getSessionCalls === 1) {
        res.end(
          JSON.stringify({
            session: { id: 'sess_chat_200', state: 'running' },
            active_run_intent: { run_id: 'run_1' },
            messages: [{ role: 'user', content: 'Run command' }],
          })
        );
      } else {
        res.end(
          JSON.stringify({
            session: { id: 'sess_chat_200', state: 'idle' },
            messages: [
              { role: 'user', content: 'Run command' },
              { role: 'assistant', content: 'Executed successfully with option Yes.' },
            ],
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
    const transport = new SwarmTransport({ baseUrl: `http://127.0.0.1:${port}`, defaultHeaders: {}, timeoutMs: 5000 });
    const chat = new SwarmChatNamespace(transport);

    await assert.rejects(chat.run('sess_chat_200', {
      message: 'Run command', timeoutMs: 100, pollIntervalMs: 10, autoApprovePermissions: true,
    }), /explicitly answer or deny/);
    assert.equal(resolvedPermissionId, null);
    assert.equal(pendingCount, 1);
  } finally {
    await new Promise<void>((resolve) => server.close(() => resolve()));
  }
});

// Requirement: permission failures remain actionable and no wait helper answers ask-user aliases.
// Threat: swallowed list/resolve errors appear as completion, or underscore alias is auto-approved.
// Authority: chat.run and sessions.waitForRun; direct transport stubs prove no mutation occurs.
test('wait helpers propagate permission failures and never answer ask_user', { timeout: 2000 }, async () => {
  const { SwarmSessionsNamespace } = await import('../sessions.js');
  const transport = new SwarmTransport({ baseUrl: 'http://localhost' });
  const chat = new SwarmChatNamespace(transport);
  const sessions = new SwarmSessionsNamespace(transport);
  let mutations = 0;
  let listFails = true;
  transport.request = async (path: string, options: any) => {
    if (path.endsWith('/messages')) return { data: { ok: true } } as any;
    if (path.includes('/resolve')) { mutations++; throw new Error('resolve failed'); }
    if (path.includes('/permissions?')) {
      if (listFails) throw new Error('list failed');
      return { data: { ok: true, permissions: [{ id: 'p', session_id: 's', tool_name: 'ask_user', status: 'pending' }] } } as any;
    }
    return { data: { id: 's', state: 'idle' } } as any;
  };
  await assert.rejects(chat.run('s', { message: 'Hi' }), /list failed/);
  await assert.rejects(sessions.waitForRun('s'), /list failed/);
  listFails = false;
  await assert.rejects(sessions.waitForRun('s', { autoApprovePermissions: true, timeoutMs: 20, pollIntervalMs: 1 }), /explicitly answer or deny/);
  assert.equal(mutations, 0);
  transport.request = async (path: string) => {
    if (path.includes('/permissions?')) return { data: { ok: true, permissions: [{ id: 'p', session_id: 's', tool_name: 'bash', status: 'pending' }] } } as any;
    if (path.includes('/resolve')) throw new Error('resolve failed');
    return { data: { ok: true } } as any;
  };
  await assert.rejects(chat.run('s', { message: 'Hi', autoApprovePermissions: true }), /resolve failed/);
  await assert.rejects(sessions.waitForRun('s', { autoApprovePermissions: true }), /resolve failed/);
});
