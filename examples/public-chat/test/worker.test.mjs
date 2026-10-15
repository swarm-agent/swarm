// Hermetic tests for the edge Worker's request handling under Node (fetch,
// Request and Web Crypto are the same APIs Workers provide). They prove what the
// edge admits and forwards, not a deployed Cloudflare configuration.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { handle, issueTicket, ticketValid } from '../worker/src/index.js';

const SITE = 'https://www.example.com';
const VISITOR = 'visitor_abcdefghijklmnop';
const env = (extra = {}) => ({ ALLOWED_ORIGINS: `${SITE},https://example.com`, GATEWAY_URL: 'https://gw.example.com', GATEWAY_SECRET: 'gw-secret', ACCESS_CLIENT_ID: 'cid', ACCESS_CLIENT_SECRET: 'csecret', ...extra });
const post = (body, { origin = SITE, raw } = {}) => new Request('https://chat.example.com/api/chat', {
  method: 'POST', headers: { Origin: origin, 'Content-Type': 'application/json', 'CF-Connecting-IP': '203.0.113.9' }, body: raw ?? JSON.stringify(body),
});

function gatewayStub(response = { status: 'answered', reply: 'hi' }, status = 200) {
  const seen = [];
  const fetchImpl = async (url, init) => {
    seen.push({ url, init });
    if (String(url).startsWith('https://challenges.cloudflare.com/')) return Response.json({ success: init.body.get('response') === 'good-token' });
    return Response.json(response, { status });
  };
  return { seen, fetchImpl };
}

test('only the site origins may call the chat, and preflight gets exact CORS', async () => {
  const gw = gatewayStub();
  const denied = await handle(post({ visitor_id: VISITOR, message: 'hi' }, { origin: 'https://evil.example' }), env(), gw);
  assert.equal(denied.status, 403);
  assert.equal(gw.seen.length, 0);
  const preflight = await handle(new Request('https://chat.example.com/api/chat', { method: 'OPTIONS', headers: { Origin: SITE } }), env(), gw);
  assert.equal(preflight.status, 204);
  assert.equal(preflight.headers.get('Access-Control-Allow-Origin'), SITE);
});

test('a same-origin replies poll without an Origin header is allowed only for a listed origin', async () => {
  const poll = (host, site) => new Request(`${host}/api/chat/replies?visitor_id=${VISITOR}&after=0`, { headers: { 'Sec-Fetch-Site': site } });
  const gw = gatewayStub({ replies: [], with_person: true });
  assert.equal((await handle(poll(SITE, 'same-origin'), env(), gw)).status, 200);
  assert.equal((await handle(poll(SITE, 'cross-site'), env(), gw)).status, 403);
  assert.equal((await handle(poll('https://chat.example.com', 'same-origin'), env(), gw)).status, 403);
  assert.equal(gw.seen.length, 1);
});

test('a valid message is forwarded with the gateway key and Access service token only', async () => {
  const gw = gatewayStub();
  const res = await handle(post({ visitor_id: VISITOR, message: 'How much?', extra: 'dropped' }), env(), gw);
  assert.equal(res.status, 200);
  assert.deepEqual(await res.json(), { status: 'answered', reply: 'hi' });
  const { url, init } = gw.seen[0];
  assert.equal(url, 'https://gw.example.com/chat');
  assert.deepEqual(JSON.parse(init.body), { visitor_id: VISITOR, message: 'How much?' });
  assert.equal(init.headers['X-Gateway-Key'], 'gw-secret');
  assert.equal(init.headers['CF-Access-Client-Id'], 'cid');
  assert.equal(init.headers['CF-Access-Client-Secret'], 'csecret');
});

test('staff status and the Yes button are forwarded with only the reviewed fields', async () => {
  const gw = gatewayStub({ staff_online: true, staff: [{ name: 'Sam', role: 'founder' }] });
  const status = await handle(new Request('https://chat.example.com/api/chat/status', { headers: { Origin: SITE } }), env(), gw);
  assert.equal(status.status, 200);
  assert.equal(gw.seen[0].url, 'https://gw.example.com/chat/status');
  const yes = (body) => new Request('https://chat.example.com/api/chat/person', { method: 'POST', headers: { Origin: SITE, 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  assert.equal((await handle(yes({ visitor_id: VISITOR, accept: 'yes' }), env(), gw)).status, 400);
  assert.equal((await handle(yes({ visitor_id: VISITOR, accept: true, session_id: 'other' }), env(), gw)).status, 200);
  const { url, init } = gw.seen.at(-1);
  assert.equal(url, 'https://gw.example.com/chat/person');
  assert.deepEqual(JSON.parse(init.body), { visitor_id: VISITOR, accept: true });

  const contact = new Request('https://chat.example.com/api/chat/contact', { method: 'POST', headers: { Origin: SITE, 'Content-Type': 'application/json' }, body: JSON.stringify({ visitor_id: VISITOR, email: 'pat@example.com', note: 'hi', session_id: 'other' }) });
  await handle(contact, env(), gw);
  assert.equal(gw.seen.at(-1).url, 'https://gw.example.com/chat/contact');
  assert.deepEqual(JSON.parse(gw.seen.at(-1).init.body), { visitor_id: VISITOR, email: 'pat@example.com', note: 'hi' });
  assert.equal((await handle(new Request(`https://chat.example.com/api/chat/history?visitor_id=bad`, { headers: { Origin: SITE } }), env(), gw)).status, 400);
  await handle(new Request(`https://chat.example.com/api/chat/history?visitor_id=${VISITOR}`, { headers: { Origin: SITE } }), env(), gw);
  assert.equal(gw.seen.at(-1).url, `https://gw.example.com/chat/history?visitor_id=${VISITOR}`);
});

test('bad input, oversize bodies, the rate limit and the kill switch stop at the edge', async () => {
  const gw = gatewayStub();
  for (const req of [
    post({ visitor_id: 'short', message: 'hi' }),
    post({ visitor_id: VISITOR, message: 'x'.repeat(2001) }),
    post(undefined, { raw: '{not json' }),
    post(undefined, { raw: JSON.stringify({ visitor_id: VISITOR, message: 'y'.repeat(5000) }) }),
  ]) {
    assert.ok([400, 413].includes((await handle(req, env(), gw)).status));
  }
  const limited = await handle(post({ visitor_id: VISITOR, message: 'hi' }), env({ RATE_LIMITER: { limit: async ({ key }) => ({ success: key !== '203.0.113.9' }) } }), gw);
  assert.equal(limited.status, 429);
  const paused = await handle(post({ visitor_id: VISITOR, message: 'hi' }), env({ CONTROL: { get: async () => 'false' } }), gw);
  assert.deepEqual([paused.status, await paused.json()], [503, { status: 'paused', fallback: true }]);
  assert.equal(gw.seen.length, 0, 'nothing reached the gateway');
});

test('an unreachable gateway tells the widget to fall back; busy does not', async () => {
  const down = await handle(post({ visitor_id: VISITOR, message: 'hi' }), env(), { fetchImpl: async () => { throw new Error('tunnel down'); } });
  assert.deepEqual([down.status, await down.json()], [503, { status: 'unavailable', fallback: true }]);
  const busy = await handle(post({ visitor_id: VISITOR, message: 'hi' }), env(), gatewayStub({ status: 'busy' }, 503));
  assert.deepEqual([busy.status, await busy.json()], [503, { status: 'busy', fallback: false }]);
});

test('with Turnstile on, a visitor proves once and then uses a signed 24 h ticket', async () => {
  const secrets = { TURNSTILE_SECRET: 'ts', TICKET_SECRET: 'ticket-secret' };
  const gw = gatewayStub();
  const refused = await handle(post({ visitor_id: VISITOR, message: 'hi', turnstile_token: 'bad' }), env(secrets), gw);
  assert.equal(refused.status, 403);
  const first = await handle(post({ visitor_id: VISITOR, message: 'hi', turnstile_token: 'good-token' }), env(secrets), gw);
  const { ticket } = await first.json();
  assert.ok(await ticketValid('ticket-secret', VISITOR, ticket));
  assert.ok(!(await ticketValid('ticket-secret', 'another_visitor_12345', ticket)), 'a ticket is bound to its visitor');
  assert.ok(!(await ticketValid('ticket-secret', VISITOR, await issueTicket('ticket-secret', VISITOR, Date.now() - 2 * 86_400_000))), 'expired tickets fail');
  const verifies = () => gw.seen.filter(({ url }) => String(url).includes('turnstile')).length;
  const before = verifies();
  const second = await handle(post({ visitor_id: VISITOR, message: 'again', ticket }), env(secrets), gw);
  assert.equal(second.status, 200);
  assert.equal(verifies(), before, 'a valid ticket skips the human check');
});
