import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer, request } from 'node:http';
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
test('installation origin is loopback only and failed logins are bounded', () => {
  for (const invalid of ['http://example.com:8443', 'http://localhost:8443', 'http://127.0.0.1:8443/', 'http://user@127.0.0.1:8443', 'ftp://127.0.0.1:8443']) {
    assert.throws(() => createBoundary(invalid, secret));
  }
  assert.ok(createBoundary('http://127.0.0.1:8443', secret));
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
  const { SwarmNotFoundError } = await import('@swarmagent/sdk');
  const sdk = { onboarding: { get: async () => ({ identity: { bootstrapped: true } }) },
    settings: { providers: async () => [{ id: 'codex', ready: false }], agentModels: async () => { throw new SwarmNotFoundError('not configured'); } },
    auth: { credentials: { list: async () => ({ records: [] }) } } };
  const result = await operations(sdk).run('settings', {});
  assert.equal(result.settings, null);
  assert.equal(result.providers[0].id, 'codex');
  sdk.settings.agentModels = async () => { throw new Error('backend failure'); };
  await assert.rejects(operations(sdk).run('settings', {}), /backend failure/);
});

// Purpose: warning-free loopback HTTP must use a browser-accepted host-only cookie
// while appHandler/createBoundary retain exact Origin, Host, CSRF and logout
// authority. A real HTTP listener plus SDK call counter proves forbidden requests
// have no privileged effects; this is not provider-backed chat evidence.
test('loopback HTTP login, authenticated calls and logout retain browser protections', { timeout: 5000 }, async t => {
  let calls = 0;
  const server = createServer();
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  t.after(() => { server.closeAllConnections(); server.close(); });
  const origin = `http://127.0.0.1:${server.address().port}`;
  server.on('request', appHandler({ onboarding: { get: async () => { calls++; return {}; } } }, { origin, secret }));
  const post = (path, data, headers = {}) => fetch(origin + path, { method: 'POST', headers: { Origin: origin, 'Content-Type': 'application/json', ...headers }, body: JSON.stringify(data) });
  assert.equal((await fetch(origin)).status, 200);
  const login = await post('/login', { secret });
  assert.equal(login.status, 200);
  const cookie = login.headers.get('set-cookie');
  assert.match(cookie, /^swapp-loopback=[a-f0-9]{64}; Path=\/; HttpOnly; SameSite=Strict;/);
  assert.doesNotMatch(cookie, /Secure|Domain|__Host-/);
  const auth = { Cookie: cookie.split(';')[0], 'X-CSRF-Token': (await login.json()).csrf };
  for (const extra of [{ Origin: 'http://evil.invalid' }, { Origin: '' }, { Host: 'evil.invalid' }, { 'X-CSRF-Token': '' }, { 'Sec-Fetch-Site': 'cross-site' }]) {
    // Raw HTTP preserves the deliberately invalid Host/Fetch-Metadata headers;
    // fetch implementations may replace these before sending the request.
    const status = await new Promise((resolve, reject) => {
      const req = request(origin + '/api', { method: 'POST', headers: { Origin: origin, 'Content-Type': 'application/json', ...auth, ...extra } }, res => { res.resume(); resolve(res.statusCode); });
      req.on('error', reject); req.end(JSON.stringify({ op: 'onboarding' }));
    });
    assert.equal(status, 403, JSON.stringify(extra));
  }
  assert.equal(calls, 0);
  assert.equal((await post('/api', { op: 'onboarding' }, auth)).status, 200);
  assert.equal(calls, 1);
  const logout = await post('/logout', {}, auth);
  assert.match(logout.headers.get('set-cookie'), /^swapp-loopback=;.*Max-Age=0$/);
  assert.equal((await post('/api', { op: 'onboarding' }, auth)).status, 401);
  assert.equal(calls, 1);
});

// Purpose: createBoundary must let an external link open only the public login
// document, never an embedded page, subresource or privileged operation. Direct
// boundary calls exercise real policy without a browser, daemon or network.
test('external links may navigate to the public shell without authorizing cross-site operations', () => {
  for (const origin of ['http://127.0.0.1:8443', 'https://127.0.0.1:8443']) {
    const boundary = createBoundary(origin, secret);
    const { id, session } = boundary.login(secret);
    for (const site of ['cross-site', 'same-site']) {
      const navigation = { method: 'GET', url: '/', headers: {
        host: new URL(origin).host, 'sec-fetch-site': site,
        'sec-fetch-mode': 'navigate', 'sec-fetch-dest': 'document',
      } };
      assert.doesNotThrow(() => boundary.check(navigation, false));
      assert.throws(() => boundary.authenticate(navigation), /Sign in/);
      assert.throws(() => boundary.check(navigation, true), /Exact origin/);
      for (const url of ['/app.js', '/style.css', '/api', '/login', '/watch', '/logout', '/?op=onboarding']) {
        assert.throws(() => boundary.check({ ...navigation, url }, false), /Cross-site/);
      }
      for (const headers of [
        { 'sec-fetch-mode': 'cors' }, { 'sec-fetch-mode': 'no-cors' },
        { 'sec-fetch-mode': '' }, { 'sec-fetch-dest': 'iframe' },
        { 'sec-fetch-dest': 'image' }, { 'sec-fetch-dest': '' },
        { 'sec-fetch-site': 'unknown' }, { host: 'attacker.invalid' },
        { origin: 'https://attacker.invalid' }, { origin: 'null' },
      ]) {
        assert.throws(() => boundary.check({ ...navigation, headers: { ...navigation.headers, ...headers } }, false));
      }
      for (const method of ['POST', 'PUT', 'DELETE', 'HEAD', 'OPTIONS']) {
        // Even valid credentials/CSRF and a forged exact Origin cannot turn the
        // navigation exception into authority for an unsafe cross-site request.
        assert.throws(() => boundary.check({ ...navigation, method, headers: {
          ...navigation.headers, origin, cookie: boundary.cookie(id), 'x-csrf-token': session.csrf,
        } }), /Cross-site/);
      }
    }
    // Rejections must not revoke or alter the legitimate user's session.
    const sameOrigin = { method: 'POST', url: '/api', headers: {
      host: new URL(origin).host, origin, 'sec-fetch-site': 'same-origin',
      cookie: boundary.cookie(id), 'x-csrf-token': session.csrf,
    } };
    assert.doesNotThrow(() => boundary.check(sameOrigin));
    assert.equal(boundary.authenticate(sameOrigin), session);
  }
});

// Purpose: appHandler must actually serve the public shell for link navigation
// while rejecting authenticated cross-site API requests before SDK effects. A
// direct handler invocation is sufficient; no live listener or daemon is needed.
test('link navigation serves HTML but cannot reach privileged SDK operations', { timeout: 5000 }, async () => {
  let calls = 0;
  const handler = appHandler({ onboarding: { get: async () => { calls++; return {}; } } }, {
    origin: 'http://127.0.0.1:8443', secret,
  });
  const invoke = async (url, method = 'GET', extra = {}) => {
    const req = { url, method, headers: { host: '127.0.0.1:8443',
      'sec-fetch-site': 'cross-site', 'sec-fetch-mode': 'navigate', 'sec-fetch-dest': 'document', ...extra } };
    const res = { writeHead(status, headers) { this.status = status; this.headers = headers; },
      end(body) { this.body = String(body); } };
    await handler(req, res);
    return res;
  };
  const page = await invoke('/');
  assert.equal(page.status, 200);
  assert.equal(page.headers['Content-Type'], 'text/html');
  assert.match(page.body, /<!doctype html>/i);
  assert.match(page.headers['Content-Security-Policy'], /frame-ancestors 'none'/);
  assert.equal(page.headers['Cache-Control'], 'no-store');
  assert.equal(page.headers['Set-Cookie'], undefined);
  for (const route of ['/api', '/login', '/logout', '/watch']) {
    const res = await invoke(route, 'POST', { origin: 'http://127.0.0.1:8443' });
    assert.equal(res.status, 403);
    assert.deepEqual(JSON.parse(res.body), { error: 'Cross-site request rejected.' });
  }
  assert.equal((await invoke('/', 'GET', { 'sec-fetch-dest': 'iframe' })).status, 403);
  assert.equal((await invoke('/app.js')).status, 403);
  assert.equal(calls, 0);
});
