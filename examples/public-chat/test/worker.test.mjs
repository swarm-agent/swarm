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
    if (String(url).startsWith('https://challenges.cloudflare.com/')) {
      const token = init.body.get('response');
      if (token === 'good-token') return Response.json({ success: true, hostname: 'www.example.com', action: 'chat' });
      if (token === 'other-site-token') return Response.json({ success: true, hostname: 'evil.example', action: 'chat' });
      return Response.json({ success: false });
    }
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

test('Turnstile tokens from another site are refused, and side routes need the ticket', async () => {
  const secrets = { TURNSTILE_SECRET: 'ts', TICKET_SECRET: 'ticket-secret' };
  const gw = gatewayStub();
  assert.equal((await handle(post({ visitor_id: VISITOR, message: 'hi', turnstile_token: 'other-site-token' }), env(secrets), gw)).status, 403);
  const side = (path, body) => new Request(`https://chat.example.com${path}`, { method: 'POST', headers: { Origin: SITE, 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  const forwarded = () => gw.seen.filter(({ url }) => String(url).startsWith('https://gw.example.com')).length;
  const before = forwarded();
  assert.equal((await handle(side('/api/chat/person', { visitor_id: VISITOR, accept: true }), env(secrets), gw)).status, 403);
  assert.equal((await handle(side('/api/chat/contact', { visitor_id: VISITOR, email: 'a@example.com' }), env(secrets), gw)).status, 403);
  assert.equal(forwarded(), before, 'nothing reaches the gateway without a ticket');
  const ticket = await issueTicket('ticket-secret', VISITOR);
  const yes = await handle(side('/api/chat/person', { visitor_id: VISITOR, accept: true, ticket }), env(secrets), gw);
  assert.equal(yes.status, 200);
  assert.deepEqual(JSON.parse(gw.seen.at(-1).init.body), { visitor_id: VISITOR, accept: true }, 'the ticket is not forwarded');
});

test('REQUIRE_TURNSTILE without its secrets pauses the chat instead of skipping the check', async () => {
  const gw = gatewayStub();
  const res = await handle(post({ visitor_id: VISITOR, message: 'hi' }), env({ REQUIRE_TURNSTILE: 'true' }), gw);
  assert.deepEqual([res.status, await res.json()], [503, { status: 'unavailable', fallback: true }]);
  assert.equal(gw.seen.length, 0);
});

test('the gateway gets a keyed hash of the visitor network, never the address, and the card routes keep only their fields', async () => {
  const gw = gatewayStub({ status: 'saved' });
  await handle(post({ visitor_id: VISITOR, message: 'hi' }), env({ NET_SECRET: 'net-secret' }), gw);
  const net = gw.seen.at(-1).init.headers['X-Visitor-Net'];
  assert.match(net, /^[A-Za-z0-9_-]{22}$/);
  assert.ok(!JSON.stringify(gw.seen.at(-1).init).includes('203.0.113.9'), 'the IP itself is not forwarded');
  // Same network, same token; another network, another token; no secret, no header.
  await handle(post({ visitor_id: VISITOR, message: 'again' }), env({ NET_SECRET: 'net-secret' }), gw);
  assert.equal(gw.seen.at(-1).init.headers['X-Visitor-Net'], net);
  const elsewhere = new Request('https://chat.example.com/api/chat', { method: 'POST', headers: { Origin: SITE, 'Content-Type': 'application/json', 'CF-Connecting-IP': '198.51.100.7' }, body: JSON.stringify({ visitor_id: VISITOR, message: 'hi' }) });
  await handle(elsewhere, env({ NET_SECRET: 'net-secret' }), gw);
  assert.notEqual(gw.seen.at(-1).init.headers['X-Visitor-Net'], net);
  await handle(post({ visitor_id: VISITOR, message: 'hi' }), env(), gw);
  assert.equal(gw.seen.at(-1).init.headers['X-Visitor-Net'], undefined);

  const side = (path, body) => new Request('https://chat.example.com' + path, { method: 'POST', headers: { Origin: SITE, 'Content-Type': 'application/json', 'CF-Connecting-IP': '203.0.113.9' }, body: JSON.stringify(body) });
  await handle(side('/api/chat/contact', { visitor_id: VISITOR, email: 'a@b.co', name: 'Dana', note: 'intake', from_proposal: true, extra: 'x' }), env(), gw);
  assert.deepEqual(JSON.parse(gw.seen.at(-1).init.body), { visitor_id: VISITOR, email: 'a@b.co', name: 'Dana', note: 'intake', from_proposal: true });
  await handle(side('/api/chat/proposal', { visitor_id: VISITOR, accept: false, note: 'x' }), env(), gw);
  assert.equal(gw.seen.at(-1).url, 'https://gw.example.com/chat/proposal');
  assert.deepEqual(JSON.parse(gw.seen.at(-1).init.body), { visitor_id: VISITOR, accept: false });
  assert.equal((await handle(side('/api/chat/proposal', { visitor_id: VISITOR }), env(), gw)).status, 400);
});

test('the replies poll passes only checked wait, typing and state; typing needs the ticket but not the IP limit', async () => {
  const gw = gatewayStub({ replies: [] });
  const get = (qs) => new Request('https://chat.example.com/api/chat/replies?' + qs, { headers: { Origin: SITE, 'CF-Connecting-IP': '203.0.113.9' } });
  await handle(get(`visitor_id=${VISITOR}&after=3&wait=99&typing=Sam&state=joined&x=1`), env(), gw);
  const u = new URL(gw.seen.at(-1).url);
  assert.deepEqual(Object.fromEntries(u.searchParams), { visitor_id: VISITOR, after: '3', wait: '25', typing: 'Sam', state: 'joined' });
  assert.equal((await handle(get(`visitor_id=${VISITOR}&state=hacked`), env(), gw)).status, 400);
  assert.equal((await handle(get(`visitor_id=${VISITOR}&typing=%3Cscript%3E`), env(), gw)).status, 400);

  let limited = 0;
  const RATE_LIMITER = { limit: async () => { limited++; return { success: false }; } };
  const typing = new Request('https://chat.example.com/api/chat/typing', { method: 'POST', headers: { Origin: SITE, 'Content-Type': 'application/json', 'CF-Connecting-IP': '203.0.113.9' }, body: JSON.stringify({ visitor_id: VISITOR, extra: 1 }) });
  const res = await handle(typing, env({ RATE_LIMITER }), gw);
  assert.equal(res.status, 200);
  assert.equal(limited, 0, 'typing skips the per-IP limit');
  assert.deepEqual(JSON.parse(gw.seen.at(-1).init.body), { visitor_id: VISITOR });
});
