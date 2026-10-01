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

// Purpose: the authenticated streaming boundary must deny CSRF before opening an
// SDK watch, redact upstream failures and dispose every stream on logout.
// Request/response objects isolate this BFF lifecycle without provider execution.
test('hub stream authorization and logout dispose SDK resources', { timeout: 2000 }, async () => {
  const { EventEmitter } = await import('node:events');
  const token = 'b'.repeat(64), origin = 'http://127.0.0.1:8787';
  let opened = 0, disposed = 0, emit;
  const handler = appHandler({ apps: { watchResults: (_id, options) => {
    opened++; emit = options.onChange;
    let finish; const done = new Promise(resolve => { finish = resolve; });
    options.signal.addEventListener('abort', () => { disposed++; finish(); }, { once: true });
    return { done, dispose() {}, ready: Promise.resolve() };
  } } }, { origin, accessToken: token });
  function invoke(path, body, headers = {}) {
    const req = Readable.from([Buffer.from(JSON.stringify(body))]);
    Object.assign(req, { url: path, method: 'POST', headers: { host: '127.0.0.1:8787', origin, 'content-type': 'application/json', ...headers } });
    const res = new EventEmitter();
    Object.assign(res, { headers: {}, chunks: [], setHeader(k, v) { this.headers[k] = v; }, writeHead(status, headers) { this.status = status; this.headersSent = true; Object.assign(this.headers, headers); }, write(chunk) { this.chunks.push(chunk); return true; }, end(body) { if (body) this.body = JSON.parse(body); } });
    return { res, done: handler(req, res) };
  }
  const login = invoke('/login', { token }); await login.done;
  const auth = { cookie: login.res.headers['Set-Cookie'].split(';')[0], 'x-csrf-token': login.res.body.csrf };
  const denied = invoke('/stream', { id: 'editor' }, { ...auth, 'x-csrf-token': '' }); await denied.done;
  assert.equal(denied.res.status, 403); assert.equal(opened, 0);
  const stream = invoke('/stream', { id: 'editor' }, auth); await new Promise(setImmediate);
  assert.equal(opened, 1); emit({ tasks: [] }); assert.equal(stream.res.chunks.length, 1);
  const logout = invoke('/logout', {}, auth); await logout.done; await stream.done;
  assert.equal(disposed, 1); emit({ tasks: ['late'] }); assert.equal(stream.res.chunks.length, 1);
});

// Purpose: configuration must preserve an existing plan/input contract and reject
// stale revisions without writes; the BFF operation is the narrow mapping boundary.
test('configuration preserves plan and rejects stale writes', async () => {
  const writes = [], plan = { title: 'Approved job' };
  const run = operations({ apps: {
    worker: async () => ({ worker: { revision: 4, automations: [{ id: 'job', name: 'Draft', enabled: true, plan_document: plan }] } }),
    configureAutomation: async (...args) => { writes.push(args); return {}; },
  } });
  const input = { id: 'editor', worker_id: 'worker', automation_id: 'job', revision: 4, mode: 'interval', seconds: 3600 };
  await assert.rejects(run('configure', { ...input, revision: 3 }));
  await assert.rejects(run('configure', { ...input, seconds: 0 }));
  assert.equal(writes.length, 0);
  await run('configure', input);
  assert.equal(writes[0][2], 4); assert.deepEqual(writes[0][3].plan_document, plan);
  assert.deepEqual(writes[0][3].schedule, { kind: 'interval', interval_seconds: 3600 });
});
