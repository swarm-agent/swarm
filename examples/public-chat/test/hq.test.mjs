// Hermetic tests for Bots HQ: real RS256 Access tokens signed with a throwaway
// key, fake VPC bindings standing in for each bot's gateway, an in-memory KV.
// They prove routing, identity, hours and alert labelling, not a deployed setup.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { handle, applyHours, relayAlerts, watchBots, botsFrom, staffId } from '../hq/src/index.js';
import { cleanHours, cleanSchedule, onCall, scheduleFor, localTime } from '../hq/src/hours.mjs';

const TEAM = 'team.example.com';
const AUD = 'hq-aud';
const alg = { name: 'RSASSA-PKCS1-v1_5', modulusLength: 2048, publicExponent: new Uint8Array([1, 0, 1]), hash: 'SHA-256' };
const keys = await crypto.subtle.generateKey(alg, true, ['sign', 'verify']);
const jwk = { ...(await crypto.subtle.exportKey('jwk', keys.publicKey)), kid: 'k1' };
const b64 = (bytes) => Buffer.from(bytes).toString('base64url');
async function token(email = 'sam@example.com') {
  const head = b64(JSON.stringify({ alg: 'RS256', kid: 'k1' }));
  const body = b64(JSON.stringify({ iss: `https://${TEAM}`, aud: [AUD], email, exp: Math.floor(Date.now() / 1000) + 600 }));
  const sig = await crypto.subtle.sign('RSASSA-PKCS1-v1_5', keys.privateKey, new TextEncoder().encode(`${head}.${body}`));
  return `${head}.${body}.${b64(new Uint8Array(sig))}`;
}
const fetchImpl = async (url, init) => {
  if (String(url) === `https://${TEAM}/cdn-cgi/access/certs`) return Response.json({ keys: [jwk] });
  if (String(url).startsWith('https://status.example.com')) return Response.json({ up: true, chats_today: 4, auth: init.headers.Authorization });
  if (String(url).startsWith('https://alerts.example.com')) { alertsSent.push(JSON.parse(init.body)); return new Response(null, { status: 202 }); }
  return new Response('unexpected ' + url, { status: 500 });
};
let alertsSent = [];

function gatewayBinding(name, { events = [], latest = events.length } = {}) {
  const seen = [];
  return {
    seen,
    fetch: async (url, init = {}) => {
      seen.push({ url: String(url), method: init.method ?? 'GET', headers: init.headers, body: init.body && JSON.parse(init.body) });
      const path = new URL(url).pathname;
      if (path === '/ops/summary') return Response.json({ agent: name, conversations: { total: 3, today: 1, waiting_for_person: name === 'swarm' ? 2 : 0 }, spend: { today_usd: 0.01, cap_usd: 2, cap_on: true, over: false } });
      if (path === '/ops/events') { const after = Number(new URL(url).searchParams.get('after')); return Response.json({ events: events.filter((e) => e.seq > after), latest }); }
      return Response.json({ ok: true, path });
    },
  };
}

function kv() {
  const m = new Map();
  return { get: async (k) => (m.has(k) ? m.get(k) : null), put: async (k, v) => { m.set(k, v); }, map: m };
}

function makeEnv(extra = {}) {
  const shop = gatewayBinding('frontdesk');
  const swarm = gatewayBinding('swarm');
  return {
    ACCESS_TEAM_DOMAIN: TEAM, ACCESS_AUD: AUD,
    STAFF: JSON.stringify({ 'sam@example.com': { name: 'Sam', role: 'founder', on_call: true }, 'ana@example.com': { name: 'Ana', role: 'support' } }),
    BOTS: JSON.stringify([
      { id: 'shop-prod', name: 'shop.example.com', env: 'production', binding: 'GW_SHOP', ops_secret: 'OPS_SHOP' },
      { id: 'docs-staging', name: 'docs.example.com', env: 'staging', binding: 'GW_SWARM', ops_secret: 'OPS_SWARM' },
      { id: 'desk-staging', name: 'Help desk', env: 'staging', kind: 'status', url: 'https://status.example.com/hq', key_secret: 'DESK_KEY' },
      { id: 'BAD id', name: 'dropped' },
    ]),
    GW_SHOP: shop, GW_SWARM: swarm, OPS_SHOP: 'ops-shop', OPS_SWARM: 'ops-s', DESK_KEY: 'desk-key',
    HQ_STATE: kv(), ...extra,
  };
}

const req = async (path, { method = 'GET', body, email = 'sam@example.com', origin } = {}) => new Request(`https://hq.example.com${path}`, {
  method, body: body === undefined ? undefined : JSON.stringify(body),
  headers: { 'Cf-Access-Jwt-Assertion': await token(email), 'Content-Type': 'application/json', ...(origin ? { Origin: origin } : {}) },
});

test('bots are read from config; malformed entries are dropped', () => {
  const bots = botsFrom(makeEnv());
  assert.deepEqual(bots.map((b) => b.id), ['shop-prod', 'docs-staging', 'desk-staging']);
  assert.equal(bots[2].kind, 'status');
});

test('only a listed staff login gets in, and ops calls go to the chosen bot with its own key', async () => {
  const env = makeEnv();
  assert.equal((await handle(await req('/bots', { email: 'stranger@example.com' }), env, { fetchImpl })).status, 403);
  const list = await (await handle(await req('/bots'), env, { fetchImpl })).json();
  assert.equal(list.bots.length, 3);
  assert.equal(list.bots[0].binding, undefined, 'bindings and secret names stay server-side');
  const q = await handle(await req('/ops/queue?bot=docs-staging'), env, { fetchImpl });
  assert.equal(q.status, 200);
  assert.equal(env.GW_SWARM.seen[0].url, 'http://gateway:8080/ops/queue');
  assert.equal(env.GW_SWARM.seen[0].headers['X-Ops-Key'], 'ops-s');
  assert.equal(env.GW_SHOP.seen.length, 0);
  const reply = await handle(await req('/ops/reply', { method: 'POST', body: { bot: 'shop-prod', session_id: 's1', content: 'hi', name: 'Spoofed' } }), env, { fetchImpl });
  assert.equal(reply.status, 200);
  assert.deepEqual(env.GW_SHOP.seen[0].body, { session_id: 's1', content: 'hi', name: 'Sam', role: 'founder' });
  assert.equal((await handle(await req('/ops/queue?bot=nope'), env, { fetchImpl })).status, 404);
  assert.equal((await handle(await req('/ops/queue?bot=desk-staging'), env, { fetchImpl })).status, 400);
  assert.equal((await handle(await req('/ops/queue?bot=docs-staging', { origin: 'https://evil.example' }), env, { fetchImpl })).status, 403);
});

test('the overview reads every bot; a status-only bot uses its bearer key', async () => {
  const data = await (await handle(await req('/bots/summary'), makeEnv(), { fetchImpl })).json();
  assert.equal(data.bots.length, 3);
  assert.equal(data.bots[1].summary.conversations.waiting_for_person, 2);
  assert.equal(data.bots[2].summary.auth, 'Bearer desk-key');
  assert.ok(data.bots.every((b) => b.up));
  const old = makeEnv();
  old.GW_SHOP = { fetch: async () => new Response('not found', { status: 404 }) };
  const o = await (await handle(await req('/bots/summary'), old, { fetchImpl })).json();
  assert.equal(o.bots[0].up, true);
  assert.equal(o.bots[0].summary, null);
  assert.match(o.bots[0].note, /too old/);
});

test('hours: validated, stored, one schedule for all or a bot its own', async () => {
  const env = makeEnv();
  const bad = await handle(await req('/hours', { method: 'POST', body: { all: { mode: 'hours', tz: 'Mars/Base', week: {} } } }), env, { fetchImpl });
  assert.equal(bad.status, 400);
  const week = { mon: [['09:00', '17:00']], tue: [], wed: [], thu: [], fri: [], sat: [], sun: [] };
  const ok = await handle(await req('/hours', { method: 'POST', body: { all: { mode: 'hours', tz: 'Asia/Jerusalem', week }, bots: { 'docs-staging': { own: true, schedule: { mode: 'always', tz: 'UTC', week: {} } } } } }), env, { fetchImpl });
  assert.equal(ok.status, 200);
  const got = await (await handle(await req('/hours'), env, { fetchImpl })).json();
  assert.equal(got.hours.all.mode, 'hours');
  assert.equal(got.hours.bots['docs-staging'].own, true);
  assert.equal(got.hours.bots['shop-prod'].own, false);
  assert.equal(got.on_call_now['docs-staging'], true);
});

test('schedules: ranges in the schedule time zone, off and always', () => {
  const s = cleanSchedule({ mode: 'hours', tz: 'Asia/Jerusalem', week: { mon: [['09:00', '17:00']] } });
  // Monday 2026-10-19 07:30 UTC is 10:30 in Jerusalem (UTC+3); 15:30 UTC is 18:30.
  assert.equal(localTime(Date.parse('2026-10-19T07:30:00Z'), 'Asia/Jerusalem').day, 'mon');
  assert.equal(onCall(s, Date.parse('2026-10-19T07:30:00Z')), true);
  assert.equal(onCall(s, Date.parse('2026-10-19T15:30:00Z')), false);
  assert.equal(onCall(s, Date.parse('2026-10-20T07:30:00Z')), false, 'Tuesday has no hours');
  assert.equal(onCall({ mode: 'always' }), true);
  assert.equal(onCall({ mode: 'off' }), false);
  assert.throws(() => cleanSchedule({ mode: 'hours', tz: 'UTC', week: { mon: [['17:00', '09:00']] } }), /bad range/);
  const hours = cleanHours({ all: { mode: 'always', tz: 'UTC', week: {} }, bots: { b: { own: true, schedule: { mode: 'off', tz: 'UTC', week: {} } } } }, ['a', 'b']);
  assert.equal(scheduleFor(hours, 'a').mode, 'always');
  assert.equal(scheduleFor(hours, 'b').mode, 'off');
});

test('applyHours signs on-call staff in during hours and out only where it signed them in', async () => {
  const env = makeEnv();
  await env.HQ_STATE.put('hours', JSON.stringify({ all: { mode: 'always', tz: 'UTC', week: {} }, bots: { 'docs-staging': { own: true, schedule: { mode: 'off', tz: 'UTC', week: {} } } } }));
  await applyHours(env, { fetchImpl });
  const samId = await staffId('sam@example.com');
  assert.deepEqual(env.GW_SHOP.seen.map((s) => s.body), [{ id: samId, name: 'Sam', role: 'founder', available: true }], 'only on_call staff, only where on call');
  assert.equal(env.GW_SWARM.seen.length, 0);
  await env.HQ_STATE.put('hours', JSON.stringify({ all: { mode: 'off', tz: 'UTC', week: {} } }));
  await applyHours(env, { fetchImpl });
  assert.equal(env.GW_SHOP.seen[1].body.available, false);
  await applyHours(env, { fetchImpl });
  assert.equal(env.GW_SHOP.seen.length, 2, 'nothing more once signed out');
});

test('alerts: first run starts from now, then actionable events go out titled with the bot', async () => {
  alertsSent = [];
  const events = [{ seq: 1, type: 'handoff.requested', session_id: 'sess-9', summary: 'wants pricing', staff_online: true }, { seq: 2, type: 'visitor.message', session_id: 'sess-9' }];
  const env = makeEnv({ ALERTS_URL: 'https://alerts.example.com', ALERTS_AUTH: 'Authorization: Bearer x', CONSOLE_URL: 'https://hq.example.com' });
  env.GW_SWARM = gatewayBinding('swarm', { events: [], latest: 0 });
  await relayAlerts(env, { fetchImpl });
  assert.equal(alertsSent.length, 0, 'first run only records where the feed is');
  env.GW_SWARM = gatewayBinding('swarm', { events, latest: 2 });
  await relayAlerts(env, { fetchImpl });
  assert.equal(alertsSent.length, 1);
  assert.match(alertsSent[0].title, /^\[docs\.example\.com staging\] A visitor wants to talk now/);
  assert.match(alertsSent[0].body, /Open: https:\/\/hq\.example\.com\/\?bot=docs-staging&session=sess-9/);
  await relayAlerts(env, { fetchImpl });
  assert.equal(alertsSent.length, 1, 'the cursor moved past what was sent');
  const quiet = makeEnv({ ALERTS_URL: 'https://alerts.example.com', ALERTS_AUTH: 'Authorization: Bearer x', BOTS: JSON.stringify([{ id: 'docs-staging', name: 'docs.example.com', env: 'staging', binding: 'GW_SWARM', ops_secret: 'OPS_SWARM', alerts: false }]) });
  await relayAlerts(quiet, { fetchImpl });
  assert.equal(quiet.GW_SWARM.seen.length, 0, 'alerts: false skips the bot entirely');
});

test('bot health: spend, cap, errors and down become states, sent on change only', async () => {
  const posted = [];
  const watchFetch = async (url, init) => { posted.push({ url: String(url), ...JSON.parse(init.body) }); return new Response(null, { status: 202 }); };
  const summary = { spend: { today_usd: 1.7, cap_usd: 2, cap_on: true, over: false }, errors_today: 12 };
  let down = false;
  const binding = { fetch: async (url) => (down ? Promise.reject(new Error('connection refused')) : new URL(url).pathname === '/ops/summary' ? Response.json(summary) : Response.json({})) };
  let t = 1_000_000;
  const env = makeEnv({
    ALERTS_URL: 'https://alerts.example.com', ALERTS_AUTH: 'Authorization: Bearer x', CONSOLE_URL: 'https://hq.example.com',
    BOTS: JSON.stringify([{ id: 'shop-prod', name: 'shop.example.com', env: 'production', binding: 'GW_SHOP', ops_secret: 'OPS_SHOP' }]), GW_SHOP: binding,
  });
  const run = () => watchBots(env, { fetchImpl: watchFetch, now: () => t });
  await run();
  const by = (key) => posted.filter((p) => p.key === key).at(-1);
  assert.ok(posted.every((p) => p.url === 'https://alerts.example.com/v1/state'));
  assert.deepEqual([by('bot.shop-prod.spend').state, by('bot.shop-prod.spend').severity], ['failing', 'warning'], '85% of the cap warns');
  assert.match(by('bot.shop-prod.spend').fix, /HQ: https:\/\/hq\.example\.com\/\?bot=shop-prod$/);
  assert.equal(by('bot.shop-prod.errors').state, 'failing');
  assert.equal(by('bot.shop-prod.cap').state, 'ok');
  assert.equal(by('bot.shop-prod.down').state, 'ok');

  const count = posted.length;
  t += 60_000; await run();
  assert.equal(posted.length, count, 'nothing changed: nothing sent');

  summary.spend = { today_usd: 2, cap_usd: 2, cap_on: true, over: true };
  t += 60_000; await run();
  assert.deepEqual([by('bot.shop-prod.spend').state, by('bot.shop-prod.spend').severity], ['failing', 'critical'], 'at the cap: critical');
  summary.spend = { today_usd: 0, cap_usd: 0, cap_on: false, over: false };
  t += 60_000; await run();
  assert.equal(by('bot.shop-prod.cap').state, 'failing', 'a cap switched off warns');

  down = true;
  for (let i = 0; i < 2; i++) { t += 60_000; await run(); }
  assert.equal(by('bot.shop-prod.down').state, 'ok', 'two misses are not down yet');
  t += 60_000; await run();
  assert.deepEqual([by('bot.shop-prod.down').state, by('bot.shop-prod.down').severity], ['failing', 'critical'], 'production down is critical');
  down = false;
  t += 60_000; await run();
  assert.equal(by('bot.shop-prod.down').state, 'ok', 'back up resolves');

  const sent = posted.length;
  t += 31 * 60_000; await run();
  assert.ok(posted.length > sent, 'states are re-sent every 30 minutes');
});
