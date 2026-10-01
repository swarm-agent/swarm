import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { mkdtemp, mkdir, symlink, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { appHandler } from '../server.mjs';
import { operations } from '../operations.mjs';
import { createBoundary } from '../boundary.mjs';

const origin = 'https://127.0.0.1:8443', secret = 'a'.repeat(64);
// Purpose: appHandler/createBoundary must reject browser-origin, identity and CSRF
// violations before any privileged SDK invocation. In-process HTTP is the narrowest
// observable route boundary; SDK spies here are security fixtures, not live-agent proof.
test('browser boundary rejects unauthenticated, foreign-origin and CSRF requests without SDK effects', { timeout: 5000 }, async t => {
  let calls = 0;
  const sdk = { onboarding: { get: async () => { calls++; return { identity: {} }; } } };
  const server = createServer();
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const origin = `https://127.0.0.1:${server.address().port}`;
  server.on('request', appHandler(sdk, { origin, secret }));
  t.after(() => { server.closeAllConnections(); server.close(); });
  const address = `http://127.0.0.1:${server.address().port}`;
  const post = (path, data, headers = {}) => fetch(address + path, { method: 'POST', headers: {
    Origin: origin, 'Content-Type': 'application/json', ...headers,
  }, body: JSON.stringify(data) });
  assert.equal((await post('/api', { op: 'onboarding' })).status, 401);
  assert.equal((await post('/login', { secret }, { Origin: 'https://attacker.invalid' })).status, 403);
  assert.throws(() => createBoundary(origin, secret).check({ headers: { host: 'attacker.invalid', origin } }), /Invalid host/);
  assert.equal(calls, 0);
  const login = await post('/login', { secret });
  const cookie = login.headers.get('set-cookie');
  assert.match(cookie, /Secure; HttpOnly; SameSite=Strict/);
  const { csrf } = await login.json();
  const auth = { Cookie: cookie.split(';')[0], 'X-CSRF-Token': csrf };
  assert.equal((await post('/api', { op: 'onboarding' }, { Cookie: auth.Cookie })).status, 403);
  assert.equal((await post('/watch', { id: 'session' }, { ...auth, Origin: 'https://attacker.invalid' })).status, 403);
  assert.equal(calls, 0);
  assert.equal((await post('/api', { op: 'onboarding' }, auth)).status, 200);
  assert.equal(calls, 1);
  assert.equal((await post('/v1/auth/tokens', {}, auth)).status, 404);
  assert.equal(calls, 1);
  await post('/logout', {}, auth);
  assert.equal((await post('/api', { op: 'onboarding' }, auth)).status, 401);
  assert.equal(calls, 1);
});

// Purpose: appHandler must not reflect provider/SDK exception bodies or oversized
// secret submissions. This tests wire output and zero downstream side effects.
test('errors are redacted and oversized inputs rejected', { timeout: 5000 }, async t => {
  const privateValue = 'fixture-provider-secret-do-not-return'; let calls = 0;
  const server = createServer();
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const origin = `https://127.0.0.1:${server.address().port}`;
  server.on('request', appHandler({ onboarding: { get: async () => { calls++; throw new Error(privateValue); } } }, { origin, secret }));
  t.after(() => { server.closeAllConnections(); server.close(); });
  const address = `http://127.0.0.1:${server.address().port}`;
  const headers = { Origin: origin, 'Content-Type': 'application/json' };
  const login = await fetch(address + '/login', { method: 'POST', headers, body: JSON.stringify({ secret }) });
  headers.Cookie = login.headers.get('set-cookie').split(';')[0]; headers['X-CSRF-Token'] = (await login.json()).csrf;
  const res = await fetch(address + '/api', { method: 'POST', headers, body: JSON.stringify({ op: 'onboarding' }) });
  assert.equal(res.status, 502); assert.ok(!(await res.text()).includes(privateValue)); assert.equal(calls, 1);
  const huge = await fetch(address + '/api', { method: 'POST', headers, body: JSON.stringify({ op: 'credential', key: 'x'.repeat(70000) }) });
  assert.equal(huge.status, 413); assert.equal(calls, 1);
});

// Purpose: operations.workspace must fail closed on traversal and symlink escapes;
// exact permission resolution must reject a foreign/stale ID without calling resolve.
// Temporary directories plus typed-service spies isolate this app-owned policy.
test('workspace confinement and exact pending permission checks', { timeout: 5000 }, async t => {
  const root = await mkdtemp(join(tmpdir(), 'workshop-security-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const project = join(root, 'project'), outside = join(root, 'outside'), inside = join(project, 'app');
  await mkdir(project); await mkdir(outside); await mkdir(inside); await symlink(outside, join(project, 'escape'));
  let writes = 0;
  const sdk = {
    onboarding: { get: async () => ({ identity: { bootstrapped: true } }) },
    workspaces: { add: async () => { writes++; }, list: async () => [{ workspace_id: 'workspace', path: inside }] },
    sessions: { get: async () => ({ workspace_id: 'workspace', workspace_path: inside }), approvePermissionOnce: async () => { writes++; return { ok: true }; } },
    realtime: { hydrate: async () => ({ session_views_by_id: { session: { pending_permissions: [{ id: 'pending', session_id: 'session' }] } } }) },
  };
  const ops = operations(sdk, project);
  for (const path of [outside, join(project, '../outside'), join(project, 'escape')]) {
    await assert.rejects(ops.run('register', { path }), /inside the project volume/);
  }
  for (const permission_id of ['foreign', 'stale']) await assert.rejects(ops.run('permission', { id: 'session', permission_id, action: 'allow_once' }), /no longer pending/);
  assert.equal(writes, 0);
  assert.deepEqual(await ops.run('permission', { id: 'session', permission_id: 'pending', action: 'allow_once' }), { ok: true });
  assert.equal(writes, 1);
});

// Purpose: generated-secret authentication must be bounded and forbid non-loopback
// deployments; createBoundary owns this policy and is the narrowest test layer.
test('installation origin is loopback HTTPS only and failed logins are bounded', () => {
  assert.throws(() => createBoundary('http://127.0.0.1:8443', secret));
  assert.throws(() => createBoundary('https://0.0.0.0:8443', secret));
  const boundary = createBoundary(origin, secret);
  for (let i = 0; i < 10; i++) assert.throws(() => boundary.login('wrong'), /Invalid installation/);
  assert.throws(() => boundary.login(secret), /Too many login attempts/);
});

// Purpose: /watch must forward only SDK watcher snapshots, not construct a second
// realtime protocol, and must dispose the watcher on browser cancellation. This
// route-level fixture proves lifecycle wiring, not real daemon replay correctness.
test('authenticated stream forwards watcher state and disposes on disconnect', { timeout: 5000 }, async t => {
  const root = await mkdtemp(join(tmpdir(), 'workshop-stream-'));
  await mkdir(join(root, 'source'));
  let disposed = false, observed;
  const state = { sessionId: 'session', status: 'live', messages: [], live: [], snapshot: {} };
  const sdk = {
    sessions: { get: async () => ({ workspace_id: 'workspace' }) },
    workspaces: { list: async () => [{ workspace_id: 'workspace', path: join(root, 'source') }] },
    realtime: { watchSession: (id, options) => {
      observed = id; let finish;
      const done = new Promise(resolve => { finish = resolve; });
      queueMicrotask(() => options.onChange(state));
      return { ready: Promise.resolve(), done, dispose() { disposed = true; finish(); } };
    } },
  };
  const server = createServer();
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const origin = `https://127.0.0.1:${server.address().port}`;
  server.on('request', appHandler(sdk, { origin, secret, project: root }));
  t.after(async () => { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); await rm(root, { recursive: true, force: true }); });
  const address = `http://127.0.0.1:${server.address().port}`;
  const headers = { Origin: origin, 'Content-Type': 'application/json' };
  const login = await fetch(address + '/login', { method: 'POST', headers, body: JSON.stringify({ secret }) });
  headers.Cookie = login.headers.get('set-cookie').split(';')[0]; headers['X-CSRF-Token'] = (await login.json()).csrf;
  const controller = new AbortController();
  const response = await fetch(address + '/watch', { method: 'POST', headers, body: JSON.stringify({ id: 'session' }), signal: controller.signal });
  const reader = response.body.getReader();
  assert.deepEqual(JSON.parse(new TextDecoder().decode((await reader.read()).value)), { state });
  assert.equal(observed, 'session');
  // Logout closes existing streams synchronously through the same authority.
  const logout = await fetch(address + '/logout', { method: 'POST', headers, body: '{}' });
  assert.equal(logout.status, 200);
  assert.equal(disposed, true);
  controller.abort();
});

// Purpose: the real daemon returns 404 before first model setup. The onboarding
// screen must remain usable while preserving all other settings failures.
test('fresh model settings absence does not hide provider onboarding', async () => {
  const { SwarmNotFoundError } = await import('@swarm/sdk');
  const sdk = { onboarding: { get: async () => ({ identity: { bootstrapped: true } }) },
    settings: { providers: async () => [{ id: 'codex', ready: false }], agentModels: async () => { throw new SwarmNotFoundError('not configured'); } },
    auth: { credentials: { list: async () => ({ records: [] }) } } };
  const result = await operations(sdk).run('settings', {});
  assert.equal(result.settings, null);
  assert.equal(result.providers[0].id, 'codex');
  sdk.settings.agentModels = async () => { throw new Error('backend failure'); };
  await assert.rejects(operations(sdk).run('settings', {}), /backend failure/);
});
