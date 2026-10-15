// Hermetic tests for the staff console Worker: real RS256 tokens signed with a
// throwaway key, a stubbed certs endpoint and gateway. They prove who gets in
// and what reaches the gateway, not a deployed Access configuration.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { handle } from '../console/src/index.js';

const TEAM = 'team.example.com';
const AUD = 'aud-tag-123';
const env = { ACCESS_TEAM_DOMAIN: TEAM, ACCESS_AUD: AUD, GATEWAY_URL: 'https://gw.example.com', OPS_SECRET: 'ops-secret', ACCESS_CLIENT_ID: 'cid', ACCESS_CLIENT_SECRET: 'csecret', STAFF: JSON.stringify({ 'sam@example.com': { name: 'Sam', role: 'founder' } }) };
const alg = { name: 'RSASSA-PKCS1-v1_5', modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: 'SHA-256' };
const keys = await crypto.subtle.generateKey(alg, true, ['sign', 'verify']);
const other = await crypto.subtle.generateKey(alg, true, ['sign', 'verify']);
const jwk = { ...(await crypto.subtle.exportKey('jwk', keys.publicKey)), kid: 'k1' };
const b64 = (bytes) => Buffer.from(bytes).toString('base64url');

async function token(claims = {}, signer = keys.privateKey) {
  const head = b64(JSON.stringify({ alg: 'RS256', kid: 'k1' }));
  const body = b64(JSON.stringify({ iss: `https://${TEAM}`, aud: [AUD], email: 'sam@example.com', exp: Math.floor(Date.now() / 1000) + 600, ...claims }));
  const sig = await crypto.subtle.sign('RSASSA-PKCS1-v1_5', signer, new TextEncoder().encode(`${head}.${body}`));
  return `${head}.${body}.${b64(new Uint8Array(sig))}`;
}

function stubs(response = { ok: true }) {
  const seen = [];
  const fetchImpl = async (url, init) => {
    if (String(url) === `https://${TEAM}/cdn-cgi/access/certs`) return Response.json({ keys: [jwk] });
    seen.push({ url: String(url), init });
    return Response.json(response);
  };
  return { seen, fetchImpl };
}

const req = (path, { jwt, method = 'GET', body, origin } = {}) => new Request(`https://staff.example.com${path}`, {
  method, body: body === undefined ? undefined : JSON.stringify(body),
  headers: { ...(jwt ? { 'Cf-Access-Jwt-Assertion': jwt } : {}), ...(origin ? { Origin: origin } : {}), 'Content-Type': 'application/json' },
});

test('only a verified Access login for a listed staff email gets in', async () => {
  const s = stubs();
  const refused = [
    undefined,
    'not.a.jwt',
    await token({ aud: ['another-app'] }),
    await token({ iss: 'https://evil.example.com' }),
    await token({ exp: Math.floor(Date.now() / 1000) - 5 }),
    await token({ email: 'someone@example.com' }),
    await token({}, other.privateKey),
  ];
  for (const jwt of refused) assert.equal((await handle(req('/ops/queue', { jwt }), env, s)).status, 403);
  assert.equal(s.seen.length, 0, 'nothing reached the gateway');

  const jwt = await token();
  const page = await handle(req('/', { jwt }), env, s);
  assert.equal(page.status, 200);
  assert.match(page.headers.get('Content-Security-Policy'), /script-src 'self'/);
  assert.deepEqual(await (await handle(req('/me', { jwt }), env, s)).json(), { name: 'Sam', role: 'founder' });
});

test('identity comes from the login, bodies are cut to reviewed fields, and other routes are closed', async () => {
  const s = stubs();
  const jwt = await token();
  await handle(req('/ops/presence', { jwt, method: 'POST', body: { available: true, name: 'Mallory', role: 'ceo', id: 'spoof' } }), env, s);
  const presence = s.seen.at(-1);
  assert.equal(presence.url, 'https://gw.example.com/ops/presence');
  const sent = JSON.parse(presence.init.body);
  assert.deepEqual([sent.available, sent.name, sent.role], [true, 'Sam', 'founder']);
  assert.match(sent.id, /^staff_[0-9a-f]{16}$/);
  assert.equal(presence.init.headers['X-Ops-Key'], 'ops-secret');
  assert.equal(presence.init.headers['CF-Access-Client-Id'], 'cid');

  await handle(req('/ops/reply', { jwt, method: 'POST', body: { session_id: 's1', content: 'hi', role: 'assistant', metadata: {} } }), env, s);
  assert.deepEqual(JSON.parse(s.seen.at(-1).init.body), { session_id: 's1', content: 'hi' });
  await handle(req('/ops/accept', { jwt, method: 'POST', body: { session_id: 's1', name: 'Mallory' } }), env, s);
  assert.deepEqual(JSON.parse(s.seen.at(-1).init.body), { session_id: 's1', name: 'Sam', role: 'founder' });

  const before = s.seen.length;
  for (const [path, method] of [['/ops/conversations', 'POST'], ['/ops/presence', 'GET'], ['/chat', 'POST'], ['/ops/anything', 'GET']]) {
    assert.equal((await handle(req(path, { jwt, method, body: method === 'POST' ? {} : undefined }), env, s)).status, 404);
  }
  assert.equal((await handle(req('/ops/reply', { jwt, method: 'POST', origin: 'https://evil.example.com', body: { session_id: 's1', content: 'x' } }), env, s)).status, 403);
  assert.equal(s.seen.length, before);
});
