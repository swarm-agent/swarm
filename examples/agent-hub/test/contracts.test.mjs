import test from 'node:test';
import assert from 'node:assert/strict';
import { Readable } from 'node:stream';
import { appHandler } from '../server.mjs';
import { operations } from '../operations.mjs';

// Purpose: a custom UX must call supported SDK methods without a second state
// authority. Operation-level SDK receipts are the narrowest serialization proof.
test('hub composes conversations, tasks and worker results with stable retry IDs', async () => {
  const calls = [];
  const apps = new Proxy({}, { get: (_, method) => async (...args) => { calls.push([method, ...args]); return { id: 'result' }; } });
  const run = operations({ apps });
  await run('open', { id: 'editor', revision: 2, workspace_id: 'workspace', title: 'Review', request_id: 'same-open' });
  await run('message', { id: 'editor', session_id: 'session', content: 'Draft', request_id: 'same-event' });
  await run('task', { id: 'editor', title: 'Draft', content: 'Brief', request_id: 'same-task' });
  await run('runs', { id: 'editor', worker_id: 'worker' });
  assert.deepEqual(calls.map(c => c[0]), ['createConversation', 'send', 'createTask', 'runs']);
  assert.equal(calls[0][2].client_request_id, 'same-open');
  assert.equal(calls[1][3].client_request_id, 'same-event');
  assert.equal(calls[2][2].id, 'same-task');
  await assert.rejects(run('deploy-worker', {}));
  await assert.rejects(run('save', { id: 'editor', expected_revision: -1 }));
  assert.equal(calls.length, 4);
});

// Purpose: appHandler must reject unauthenticated/cross-origin/CSRF requests
// before any SDK effects. In-process HTTP request objects avoid live listeners.
test('hub authentication, CSRF, origin and error redaction fail closed', async () => {
  const token = 'a'.repeat(64), origin = 'http://127.0.0.1:8787'; let effects = 0;
  const handler = appHandler({ apps: { list: async () => { effects++; return { agents: [] }; }, get: async () => { throw new Error('private-provider-secret'); } } }, { origin, accessToken: token });
  async function invoke(path, data, headers = {}) {
    const req = Readable.from([Buffer.from(JSON.stringify(data))]);
    Object.assign(req, { url: path, method: 'POST', headers: { host: '127.0.0.1:8787', origin, 'content-type': 'application/json', ...headers } });
    const res = { headers: {}, setHeader(k, v) { this.headers[k] = v; }, writeHead(status, headers) { this.status = status; Object.assign(this.headers, headers); }, end(body) { this.body = JSON.parse(body); } };
    await handler(req, res); return res;
  }
  assert.equal((await invoke('/api', { op: 'agents' })).status, 401);
  assert.equal((await invoke('/login', { token: 'bad' })).status, 401);
  const login = await invoke('/login', { token }); assert.equal(login.status, 200);
  const auth = { cookie: login.headers['Set-Cookie'].split(';')[0], 'x-csrf-token': login.body.csrf };
  for (const bad of [{ origin: 'https://foreign.invalid' }, { host: 'foreign.invalid' }, { 'x-csrf-token': '' }, { 'sec-fetch-site': 'cross-site' }]) assert.equal((await invoke('/api', { op: 'agents' }, { ...auth, ...bad })).status, 403);
  assert.equal(effects, 0);
  assert.equal((await invoke('/api', { op: 'agents' }, auth)).status, 200); assert.equal(effects, 1);
  const failure = await invoke('/api', { op: 'agent', id: 'editor' }, auth);
  assert.doesNotMatch(JSON.stringify(failure.body), /private-provider-secret/);
  await invoke('/logout', {}, auth);
  assert.equal((await invoke('/api', { op: 'agents' }, auth)).status, 401); assert.equal(effects, 1);
  assert.throws(() => appHandler({}, { origin: 'http://0.0.0.0:8787', accessToken: token }));
});
