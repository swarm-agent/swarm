// Staff console: one page for the team and a narrow proxy to the gateway's ops
// routes. Cloudflare Access (an email login) sits in front of it, and this
// Worker also verifies the Access token itself, so a request that reaches it any
// other way gets nothing. Who you are comes from that token and the STAFF map,
// never from the browser: the page can say "I'm available", not "I'm Sam".
import { PAGE_HTML, PAGE_JS } from './page.js';
import { alertFor, alertHeaders } from '../../gateway/alerts.mjs';

const MAX_BODY = 8192;
const JSON_HEADERS = { 'Content-Type': 'application/json', 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff' };
const PAGE_HEADERS = {
  'Cache-Control': 'no-store',
  'X-Content-Type-Options': 'nosniff',
  'X-Frame-Options': 'DENY',
  'Referrer-Policy': 'no-referrer',
  'Content-Security-Policy': "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
};

// The only gateway routes the console can reach, and the body fields each may carry.
const ROUTES = {
  'GET /ops/events': null,
  'GET /ops/queue': null,
  'GET /ops/conversations': null,
  'GET /ops/transcript': null,
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

/** The staff member for a verified email, from STAFF = {"email": {"name", "role"}}. */
function staffFor(env, email) {
  let staff = {};
  try { staff = JSON.parse(env.STAFF ?? '{}'); } catch { return null; }
  const entry = Object.entries(staff).find(([e]) => e.toLowerCase() === email)?.[1];
  return entry && typeof entry.name === 'string' && typeof entry.role === 'string' ? { name: entry.name.slice(0, 40), role: entry.role.slice(0, 40) } : null;
}

async function staffId(email) {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(email));
  return 'staff_' + [...new Uint8Array(digest).slice(0, 8)].map((b) => b.toString(16).padStart(2, '0')).join('');
}

export async function handle(request, env, { fetchImpl = fetch, now = Date.now } = {}) {
  const email = await verifyAccess(request, env, { fetchImpl, now });
  const staff = email && staffFor(env, email);
  if (!staff) return json(403, { error: 'not a staff member' });

  const url = new URL(request.url);
  if (request.method === 'GET' && url.pathname === '/') return new Response(PAGE_HTML, { headers: { ...PAGE_HEADERS, 'Content-Type': 'text/html; charset=utf-8' } });
  if (request.method === 'GET' && url.pathname === '/console.js') return new Response(PAGE_JS, { headers: { ...PAGE_HEADERS, 'Content-Type': 'text/javascript; charset=utf-8' } });
  if (request.method === 'GET' && url.pathname === '/me') return json(200, staff);

  const route = `${request.method} ${url.pathname}`;
  if (!Object.hasOwn(ROUTES, route)) return json(404, { error: 'not found' });
  // Same-origin only: the page is the only client.
  const origin = request.headers.get('Origin');
  if (origin && origin !== url.origin) return json(403, { error: 'origin not allowed' });

  let body;
  if (request.method === 'POST') {
    const raw = await request.text();
    if (raw.length > MAX_BODY) return json(413, { error: 'too large' });
    let given;
    try { given = JSON.parse(raw || '{}'); } catch { return json(400, { error: 'invalid json' }); }
    body = Object.fromEntries(ROUTES[route].filter((k) => Object.hasOwn(given, k)).map((k) => [k, given[k]]));
    // Identity is added here, from the verified login.
    if (url.pathname === '/ops/presence') Object.assign(body, { id: await staffId(email), name: staff.name, role: staff.role });
    if (['/ops/accept', '/ops/reply', '/ops/resolve', '/ops/block', '/ops/unblock'].includes(url.pathname)) Object.assign(body, { name: staff.name, role: staff.role });
  }
  const search = request.method === 'GET' ? url.search : '';
  try {
    const res = await fetchImpl(env.GATEWAY_URL + url.pathname + search, {
      method: request.method,
      headers: {
        'Content-Type': 'application/json',
        'X-Ops-Key': env.OPS_SECRET ?? '',
        'CF-Access-Client-Id': env.ACCESS_CLIENT_ID ?? '',
        'CF-Access-Client-Secret': env.ACCESS_CLIENT_SECRET ?? '',
      },
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(40_000),
    });
    return new Response(await res.text(), { status: res.status, headers: JSON_HEADERS });
  } catch {
    return json(502, { error: 'gateway unreachable' });
  }
}

/**
 * Phone-alert relay, run by a once-a-minute Cron Trigger: long-polls the
 * gateway's event feed through the tunnel for about a minute and forwards the
 * events a person should act on to the alert service (swarm-watch's event
 * format). The bot's host then needs no outbound web access, and the alert
 * token lives only in this Worker. The feed cursor is kept in KV; a first run
 * starts from now instead of replaying old events.
 */
export async function relayAlerts(env, { fetchImpl = fetch, now = Date.now, budgetMs = 55_000 } = {}) {
  if (!env.ALERTS_URL || !env.ALERTS_AUTH || !env.ALERT_STATE || !env.GATEWAY_URL) return { relayed: 0, skipped: 'not configured' };
  // A Worker cannot fetch another Worker's workers.dev URL on the same account
  // (error 1042); with the alert service bound as ALERTS, call it through that.
  const sendAlert = env.ALERTS ? (url, init) => env.ALERTS.fetch(url, init) : fetchImpl;
  const started = now();
  const headers = { 'X-Ops-Key': env.OPS_SECRET ?? '', 'CF-Access-Client-Id': env.ACCESS_CLIENT_ID ?? '', 'CF-Access-Client-Secret': env.ACCESS_CLIENT_SECRET ?? '' };
  const poll = async (after, wait) => {
    const res = await fetchImpl(`${env.GATEWAY_URL}/ops/events?after=${after}&wait=${wait}`, { headers, signal: AbortSignal.timeout((wait + 10) * 1000) });
    if (!res.ok) throw new Error(`events: ${res.status}`);
    return res.json();
  };
  const stored = await env.ALERT_STATE.get('cursor');
  let cursor = Number(stored) || 0;
  let relayed = 0;
  try {
    if (stored === null) {
      cursor = (await poll(0, 0)).latest ?? 0;
      await env.ALERT_STATE.put('cursor', String(cursor));
    }
    for (;;) {
      const wait = Math.min(25, Math.floor((budgetMs - (now() - started)) / 1000) - 5);
      if (wait < 1) break;
      const data = await poll(cursor, wait);
      // The gateway restarted and its feed began again: everything in it is new.
      if ((data.latest ?? 0) < cursor) { cursor = 0; continue; }
      for (const event of data.events ?? []) {
        cursor = Math.max(cursor, event.seq);
        const alert = alertFor(event.type, event, env.CONSOLE_URL);
        if (!alert) continue;
        const status = await sendAlert(env.ALERTS_URL.replace(/\/$/, '') + '/v1/event', { method: 'POST', headers: alertHeaders(env.ALERTS_AUTH), body: JSON.stringify(alert), signal: AbortSignal.timeout(5000) })
          .then((r) => r.status, (e) => String(e?.message ?? e).slice(0, 100));
        if (status === 200 || status === 201 || status === 202 || status === 204) relayed++;
        else console.error('alert not delivered:', event.type, status);
      }
      if (data.events?.length) await env.ALERT_STATE.put('cursor', String(cursor));
    }
  } catch (error) {
    console.error('alert relay stopped:', String(error?.message ?? error).slice(0, 200));
  }
  return { relayed, cursor };
}

export default {
  fetch: (request, env) => handle(request, env),
  scheduled: (controller, env, ctx) => ctx.waitUntil(relayAlerts(env)),
};
