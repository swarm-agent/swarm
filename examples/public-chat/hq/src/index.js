// Bots HQ: one staff console for several public-chat bots (and their staging
// and production sets). Cloudflare Access (an email login) sits in front of it,
// and this Worker verifies the Access token itself too. Per bot it proxies the
// gateway's ops routes, shows a monitoring summary, applies online hours and
// relays phone alerts, each labelled with the bot's name.
//
// BOTS (a JSON var) lists the bots, in display order:
//   [{ "id": "arrange-prod", "name": "arrange.digital", "env": "production",
//      "binding": "GW_ARRANGE_PROD", "ops_secret": "OPS_ARRANGE_PROD" }, ...]
//   binding    a Workers VPC service binding that reaches the gateway privately
//              (no public hostname); or "url" with ACCESS_CLIENT_ID/SECRET for a
//              gateway behind a Cloudflare Tunnel hostname protected by Access
//   ops_secret the name of the Worker secret holding that gateway's OPS_SECRET
//   alerts     false to skip this bot's phone alerts (e.g. while an older console still relays them)
//   kind       "gateway" (default) or "status": a bot with only a read-only status
//              URL ("url") and a bearer key secret ("key_secret"), shown in the overview
// STAFF = {"email": {"name", "role", "on_call": true}}: who may sign in; on_call
// staff are made available on each bot during its online hours (hours.mjs).
import { PAGE_HTML, PAGE_JS } from './page.js';
import { alertFor, alertHeaders } from '../../gateway/alerts.mjs';
import { cleanHours, onCall, scheduleFor } from './hours.mjs';

const MAX_BODY = 8192;
const BOT_ID = /^[a-z0-9][a-z0-9-]{0,40}$/;
const JSON_HEADERS = { 'Content-Type': 'application/json', 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff' };
const PAGE_HEADERS = {
  'Cache-Control': 'no-store',
  'X-Content-Type-Options': 'nosniff',
  'X-Frame-Options': 'DENY',
  'Referrer-Policy': 'no-referrer',
  'Content-Security-Policy': "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
};

// The only gateway routes HQ can reach, and the body fields each may carry.
const ROUTES = {
  'GET /ops/events': null,
  'GET /ops/queue': null,
  'GET /ops/conversations': null,
  'GET /ops/transcript': null,
  'GET /ops/summary': null,
  'POST /ops/presence': ['available'],
  'POST /ops/accept': ['session_id'],
  'POST /ops/reply': ['session_id', 'content'],
  'POST /ops/takeover': ['session_id'],
  'POST /ops/handback': ['session_id'],
  'POST /ops/reopen': ['session_id'],
  'POST /ops/resolve': ['session_id'],
  'POST /ops/block': ['session_id'],
  'GET /ops/blocks': null,
  'POST /ops/unblock': ['net'],
  'POST /ops/typing': ['session_id'],
};

const json = (status, data) => new Response(JSON.stringify(data), { status, headers: JSON_HEADERS });

function b64url(text) {
  const s = text.replace(/-/g, '+').replace(/_/g, '/');
  return Uint8Array.from(atob(s + '='.repeat((4 - (s.length % 4)) % 4)), (c) => c.charCodeAt(0));
}

let certs = { at: 0, keys: [] };

/** Verifies the Cf-Access-Jwt-Assertion header: RS256 signature, issuer, audience, expiry. */
export async function verifyAccess(request, env, { fetchImpl = fetch, now = Date.now } = {}) {
  const token = request.headers.get('Cf-Access-Jwt-Assertion') ?? '';
  const [h, p, s] = token.split('.');
  if (!h || !p || !s || !env.ACCESS_TEAM_DOMAIN || !env.ACCESS_AUD) return null;
  let header, payload;
  try {
    header = JSON.parse(new TextDecoder().decode(b64url(h)));
    payload = JSON.parse(new TextDecoder().decode(b64url(p)));
  } catch { return null; }
  if (header.alg !== 'RS256') return null;
  if (now() - certs.at > 3_600_000 || !certs.keys.some((k) => k.kid === header.kid)) {
    const res = await fetchImpl(`https://${env.ACCESS_TEAM_DOMAIN}/cdn-cgi/access/certs`).catch(() => undefined);
    const body = res?.ok ? await res.json().catch(() => ({})) : {};
    if (Array.isArray(body.keys)) certs = { at: now(), keys: body.keys };
  }
  const jwk = certs.keys.find((k) => k.kid === header.kid);
  if (!jwk) return null;
  const key = await crypto.subtle.importKey('jwk', { kty: jwk.kty, n: jwk.n, e: jwk.e, alg: 'RS256', ext: true }, { name: 'RSASSA-PKCS1-v1_5', hash: 'SHA-256' }, false, ['verify']);
  const valid = await crypto.subtle.verify('RSASSA-PKCS1-v1_5', key, b64url(s), new TextEncoder().encode(`${h}.${p}`));
  if (!valid) return null;
  const aud = Array.isArray(payload.aud) ? payload.aud : [payload.aud];
  if (payload.iss !== `https://${env.ACCESS_TEAM_DOMAIN}` || !aud.includes(env.ACCESS_AUD)) return null;
  if (!(Number(payload.exp) * 1000 > now())) return null;
  return typeof payload.email === 'string' ? payload.email.toLowerCase() : null;
}

function staffMap(env) {
  try { return JSON.parse(env.STAFF ?? '{}'); } catch { return {}; }
}

/** The staff member for a verified email. */
function staffFor(env, email) {
  const entry = Object.entries(staffMap(env)).find(([e]) => e.toLowerCase() === email)?.[1];
  return entry && typeof entry.name === 'string' && typeof entry.role === 'string' ? { name: entry.name.slice(0, 40), role: entry.role.slice(0, 40), on_call: entry.on_call === true } : null;
}

export async function staffId(email) {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(email));
  return 'staff_' + [...new Uint8Array(digest).slice(0, 8)].map((b) => b.toString(16).padStart(2, '0')).join('');
}

/** The configured bots, checked. Unknown or malformed entries are dropped. */
export function botsFrom(env) {
  let list = [];
  try { list = JSON.parse(env.BOTS ?? '[]'); } catch { return []; }
  if (!Array.isArray(list)) return [];
  return list.filter((b) => b && BOT_ID.test(String(b.id)) && typeof b.name === 'string').map((b) => ({
    id: b.id, name: b.name.slice(0, 60), env: b.env === 'production' ? 'production' : 'staging', kind: b.kind === 'status' ? 'status' : 'gateway',
    binding: typeof b.binding === 'string' ? b.binding : '', url: typeof b.url === 'string' ? b.url : '',
    ops_secret: typeof b.ops_secret === 'string' ? b.ops_secret : '', key_secret: typeof b.key_secret === 'string' ? b.key_secret : '',
    alerts: b.alerts !== false,
  }));
}

const publicBot = (b) => ({ id: b.id, name: b.name, env: b.env, kind: b.kind });

/** Calls one bot's gateway (or status URL) privately; resolves a Response. */
export function botFetch(env, bot, path, init = {}, { fetchImpl = fetch } = {}) {
  if (bot.kind === 'status') {
    if (!bot.url) return Promise.reject(new Error('no status url'));
    return fetchImpl(bot.url, { headers: { Authorization: `Bearer ${env[bot.key_secret] ?? ''}` }, signal: init.signal });
  }
  const headers = { 'Content-Type': 'application/json', 'X-Ops-Key': env[bot.ops_secret] ?? '' };
  const binding = bot.binding && env[bot.binding];
  if (binding && typeof binding.fetch === 'function') {
    return binding.fetch(`http://${env.GATEWAY_HOST || 'gateway:8080'}${path}`, { ...init, headers });
  }
  if (!bot.url) return Promise.reject(new Error('bot has no binding or url'));
  Object.assign(headers, { 'CF-Access-Client-Id': env.ACCESS_CLIENT_ID ?? '', 'CF-Access-Client-Secret': env.ACCESS_CLIENT_SECRET ?? '' });
  return fetchImpl(bot.url.replace(/\/$/, '') + path, { ...init, headers });
}

/** One bot's monitoring summary, or why it could not be read. */
async function summaryOf(env, bot, deps) {
  const started = Date.now();
  try {
    const res = await botFetch(env, bot, '/ops/summary', { method: 'GET', signal: AbortSignal.timeout(8000) }, deps);
    const data = await res.json().catch(() => ({}));
    // An older gateway without /ops/summary still answers its other routes: up, but no numbers.
    if (res.status === 404 && bot.kind === 'gateway') return { ...publicBot(bot), up: true, ms: Date.now() - started, summary: null, note: 'gateway too old for a summary (update it)' };
    if (!res.ok) return { ...publicBot(bot), up: false, error: `HTTP ${res.status}` };
    return { ...publicBot(bot), up: true, ms: Date.now() - started, summary: data };
  } catch (error) {
    return { ...publicBot(bot), up: false, error: String(error?.message ?? error).slice(0, 120) };
  }
}

async function readHours(env, bots) {
  const raw = env.HQ_STATE ? await env.HQ_STATE.get('hours') : null;
  let stored = null;
  try { stored = raw ? JSON.parse(raw) : null; } catch { stored = null; }
  try { return cleanHours(stored ?? {}, bots.map((b) => b.id)); } catch { return cleanHours({}, bots.map((b) => b.id)); }
}

export async function handle(request, env, { fetchImpl = fetch, now = Date.now } = {}) {
  const email = await verifyAccess(request, env, { fetchImpl, now });
  const staff = email && staffFor(env, email);
  if (!staff) return json(403, { error: 'not a staff member' });

  const url = new URL(request.url);
  if (request.method === 'GET' && url.pathname === '/') return new Response(PAGE_HTML, { headers: { ...PAGE_HEADERS, 'Content-Type': 'text/html; charset=utf-8' } });
  if (request.method === 'GET' && url.pathname === '/hq.js') return new Response(PAGE_JS, { headers: { ...PAGE_HEADERS, 'Content-Type': 'text/javascript; charset=utf-8' } });
  // Same-origin only: the page is the only client.
  const origin = request.headers.get('Origin');
  if (origin && origin !== url.origin) return json(403, { error: 'origin not allowed' });

  const bots = botsFrom(env);
  if (request.method === 'GET' && url.pathname === '/me') return json(200, { name: staff.name, role: staff.role, on_call: staff.on_call });
  if (request.method === 'GET' && url.pathname === '/bots') return json(200, { bots: bots.map(publicBot) });
  if (request.method === 'GET' && url.pathname === '/bots/summary') {
    return json(200, { at: now(), bots: await Promise.all(bots.map((b) => summaryOf(env, b, { fetchImpl }))) });
  }
  if (url.pathname === '/hours') {
    if (request.method === 'GET') {
      const hours = await readHours(env, bots);
      return json(200, { hours, on_call_now: Object.fromEntries(bots.map((b) => [b.id, onCall(scheduleFor(hours, b.id), now())])) });
    }
    if (request.method === 'POST') {
      if (!env.HQ_STATE) return json(503, { error: 'hours store not configured' });
      const raw = await request.text();
      if (raw.length > MAX_BODY) return json(413, { error: 'too large' });
      let hours;
      try { hours = cleanHours(JSON.parse(raw || '{}'), bots.map((b) => b.id)); } catch (error) { return json(400, { error: String(error?.message ?? error).slice(0, 120) }); }
      await env.HQ_STATE.put('hours', JSON.stringify({ ...hours, updated_by: staff.name, updated_at: now() }));
      return json(200, { hours });
    }
  }

  const route = `${request.method} ${url.pathname}`;
  if (!Object.hasOwn(ROUTES, route)) return json(404, { error: 'not found' });
  let body;
  let botId = url.searchParams.get('bot') ?? '';
  if (request.method === 'POST') {
    const raw = await request.text();
    if (raw.length > MAX_BODY) return json(413, { error: 'too large' });
    let given;
    try { given = JSON.parse(raw || '{}'); } catch { return json(400, { error: 'invalid json' }); }
    if (typeof given.bot === 'string') botId = given.bot;
    body = Object.fromEntries(ROUTES[route].filter((k) => Object.hasOwn(given, k)).map((k) => [k, given[k]]));
    // Identity is added here, from the verified login.
    if (url.pathname === '/ops/presence') Object.assign(body, { id: await staffId(email), name: staff.name, role: staff.role });
    if (['/ops/accept', '/ops/reply', '/ops/resolve', '/ops/block', '/ops/unblock', '/ops/typing'].includes(url.pathname)) Object.assign(body, { name: staff.name, role: staff.role });
  }
  const bot = bots.find((b) => b.id === botId);
  if (!bot) return json(404, { error: 'unknown bot' });
  if (bot.kind !== 'gateway') return json(400, { error: 'this bot has a status page only' });
  const search = new URLSearchParams(request.method === 'GET' ? url.search : '');
  search.delete('bot');
  const qs = search.toString();
  try {
    const res = await botFetch(env, bot, url.pathname + (qs ? `?${qs}` : ''), {
      method: request.method, body: body === undefined ? undefined : JSON.stringify(body), signal: AbortSignal.timeout(40_000),
    }, { fetchImpl });
    return new Response(await res.text(), { status: res.status, headers: JSON_HEADERS });
  } catch {
    return json(502, { error: 'gateway unreachable' });
  }
}

/**
 * Online hours, once a minute: on every gateway bot whose schedule says "on
 * call now", each on_call staff member is marked available (the gateway's
 * presence lasts 90 s, so the next run renews it); when the schedule ends, HQ
 * signs them out again, but only on bots where it signed them in, so a person
 * who switched themselves on in the page is not switched off by the schedule.
 */
export async function applyHours(env, { fetchImpl = fetch, now = Date.now } = {}) {
  const bots = botsFrom(env).filter((b) => b.kind === 'gateway');
  if (!bots.length || !env.HQ_STATE) return { applied: 0 };
  const hours = await readHours(env, bots);
  const onCallStaff = await Promise.all(Object.entries(staffMap(env)).filter(([, s]) => s?.on_call === true && typeof s.name === 'string')
    .map(async ([email, s]) => ({ id: await staffId(email.toLowerCase()), name: s.name.slice(0, 40), role: String(s.role ?? '').slice(0, 40) || 'team' })));
  let signedIn = {};
  try { signedIn = JSON.parse((await env.HQ_STATE.get('hours_signed_in')) ?? '{}'); } catch { signedIn = {}; }
  let applied = 0;
  for (const bot of bots) {
    const on = onCall(scheduleFor(hours, bot.id), now());
    if (!on && !signedIn[bot.id]) continue;
    for (const person of onCallStaff) {
      const res = await botFetch(env, bot, '/ops/presence', { method: 'POST', body: JSON.stringify({ ...person, available: on }), signal: AbortSignal.timeout(8000) }, { fetchImpl })
        .then((r) => r.ok, () => false);
      if (res) applied++;
    }
    if (on) signedIn[bot.id] = true; else delete signedIn[bot.id];
  }
  await env.HQ_STATE.put('hours_signed_in', JSON.stringify(signedIn));
  return { applied };
}

/**
 * Phone alerts for every gateway bot, once a minute: reads each bot's event
 * feed after the cursor kept in KV (a first run starts from now) and forwards
 * the events a person should act on to the alert service, titled with the bot.
 */
export async function relayAlerts(env, { fetchImpl = fetch } = {}) {
  if (!env.ALERTS_URL || !env.ALERTS_AUTH || !env.HQ_STATE) return { relayed: 0, skipped: 'not configured' };
  const sendAlert = env.ALERTS ? (u, init) => env.ALERTS.fetch(u, init) : fetchImpl;
  let relayed = 0;
  for (const bot of botsFrom(env).filter((b) => b.kind === 'gateway' && b.alerts)) {
    const key = `cursor:${bot.id}`;
    try {
      const stored = await env.HQ_STATE.get(key);
      let cursor = Number(stored) || 0;
      const res = await botFetch(env, bot, `/ops/events?after=${stored === null ? 0 : cursor}&wait=0`, { method: 'GET', signal: AbortSignal.timeout(10_000) }, { fetchImpl });
      if (!res.ok) throw new Error(`events: ${res.status}`);
      const data = await res.json();
      if (stored === null || (data.latest ?? 0) < cursor) {
        // First run, or the gateway restarted: start from now / from its new feed.
        await env.HQ_STATE.put(key, String(stored === null ? (data.latest ?? 0) : 0));
        if (stored === null) continue;
        cursor = 0;
      }
      for (const event of data.events ?? []) {
        if (event.seq <= cursor) continue;
        cursor = Math.max(cursor, event.seq);
        const alert = alertFor(event.type, event, '');
        if (!alert) continue;
        const link = env.CONSOLE_URL && event.session_id ? `\nOpen: ${env.CONSOLE_URL.replace(/\/$/, '')}/?bot=${encodeURIComponent(bot.id)}&session=${encodeURIComponent(event.session_id)}` : '';
        const labelled = { ...alert, title: `[${bot.name}${bot.env === 'staging' ? ' staging' : ''}] ${alert.title}`.slice(0, 200), body: (alert.body + link).slice(0, 2000), source: bot.id };
        const status = await sendAlert(env.ALERTS_URL.replace(/\/$/, '') + '/v1/event', { method: 'POST', headers: alertHeaders(env.ALERTS_AUTH), body: JSON.stringify(labelled), signal: AbortSignal.timeout(5000) })
          .then((r) => r.status, (e) => String(e?.message ?? e).slice(0, 100));
        if ([200, 201, 202, 204].includes(status)) relayed++;
        else console.error('alert not delivered:', bot.id, event.type, status);
      }
      await env.HQ_STATE.put(key, String(cursor));
    } catch (error) {
      console.error('alert relay:', bot.id, String(error?.message ?? error).slice(0, 200));
    }
  }
  return { relayed };
}

export default {
  fetch: (request, env) => handle(request, env),
  scheduled: (controller, env, ctx) => ctx.waitUntil(Promise.all([applyHours(env), relayAlerts(env)])),
};
