// Public edge for the chat: the only internet-facing piece. It admits browser
// requests from the site's own origins, rate-limits per IP, caps sizes, checks
// a Turnstile once per visitor (then a signed 24 h ticket), and forwards to the
// gateway through a Cloudflare Tunnel protected by an Access service token.
// When the gateway is unreachable or the chat is paused, it says so and the
// widget falls back to its built-in answers.

const MAX_BODY = 4096;
const MAX_MESSAGE = 2000;
const VISITOR_ID = /^[A-Za-z0-9_-]{16,64}$/;
const TICKET_TTL_MS = 24 * 60 * 60 * 1000;

const encoder = new TextEncoder();

async function hmac(secret, text) {
  const key = await crypto.subtle.importKey('raw', encoder.encode(secret), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  const sig = new Uint8Array(await crypto.subtle.sign('HMAC', key, encoder.encode(text)));
  return btoa(String.fromCharCode(...sig)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

export async function issueTicket(secret, visitorId, now = Date.now()) {
  const expires = now + TICKET_TTL_MS;
  return `${expires}.${await hmac(secret, `${visitorId}.${expires}`)}`;
}

export async function ticketValid(secret, visitorId, ticket, now = Date.now()) {
  if (typeof ticket !== 'string') return false;
  const [expires, sig] = ticket.split('.');
  if (!sig || !(Number(expires) > now)) return false;
  const expected = await hmac(secret, `${visitorId}.${expires}`);
  if (expected.length !== sig.length) return false;
  // Workers provide a constant-time compare; plain equality is the fallback elsewhere (tests).
  return crypto.subtle.timingSafeEqual ? crypto.subtle.timingSafeEqual(encoder.encode(expected), encoder.encode(sig)) : expected === sig;
}

function allowedOrigins(env) {
  return String(env.ALLOWED_ORIGINS ?? '').split(',').map((s) => s.trim()).filter(Boolean);
}

export async function handle(request, env, { fetchImpl = fetch, now = Date.now } = {}) {
  const url = new URL(request.url);
  if (url.pathname === '/api/health') return Response.json({ ok: true });
  if (!url.pathname.startsWith('/api/chat')) return new Response('not found', { status: 404 });

  // Browsers omit Origin on same-origin GETs (the replies poll when the widget is
  // served by this Worker); Sec-Fetch-Site is browser-set and cannot be scripted.
  const origin = request.headers.get('Origin') ?? (request.headers.get('Sec-Fetch-Site') === 'same-origin' ? url.origin : '');
  if (!allowedOrigins(env).includes(origin)) return Response.json({ error: 'origin not allowed' }, { status: 403 });
  const cors = { 'Access-Control-Allow-Origin': origin, Vary: 'Origin', 'Access-Control-Allow-Methods': 'GET, POST, OPTIONS', 'Access-Control-Allow-Headers': 'Content-Type', 'Cache-Control': 'no-store' };
  const reply = (status, data) => Response.json(data, { status, headers: cors });
  if (request.method === 'OPTIONS') return new Response(null, { status: 204, headers: cors });

  // Kill switch: a var for deploy-time pauses, or a KV key flipped from the hub.
  if (env.CHAT_ENABLED === 'false' || (env.CONTROL && (await env.CONTROL.get('chat_enabled')) === 'false')) {
    return reply(503, { status: 'paused', fallback: true });
  }
  const ip = request.headers.get('CF-Connecting-IP') ?? 'unknown';
  if (env.RATE_LIMITER) {
    const { success } = await env.RATE_LIMITER.limit({ key: ip });
    if (!success) return reply(429, { status: 'slow_down' });
  }

  const gatewayHeaders = {
    'Content-Type': 'application/json',
    'X-Gateway-Key': env.GATEWAY_SECRET ?? '',
    'CF-Access-Client-Id': env.ACCESS_CLIENT_ID ?? '',
    'CF-Access-Client-Secret': env.ACCESS_CLIENT_SECRET ?? '',
  };
  const forward = async (path, init) => {
    try {
      const res = await fetchImpl(env.GATEWAY_URL + path, { ...init, headers: gatewayHeaders, signal: AbortSignal.timeout(55_000) });
      const data = await res.json().catch(() => ({}));
      if (res.status >= 500) return reply(503, { status: data.status ?? 'unavailable', fallback: data.status !== 'busy' && data.status !== 'resting' });
      return reply(res.status, data);
    } catch {
      return reply(503, { status: 'unavailable', fallback: true });
    }
  };

  if (request.method === 'GET' && url.pathname === '/api/chat/replies') {
    const visitorId = url.searchParams.get('visitor_id') ?? '';
    const after = Number(url.searchParams.get('after') ?? 0);
    if (!VISITOR_ID.test(visitorId) || !Number.isFinite(after)) return reply(400, { error: 'invalid query' });
    return forward(`/chat/replies?visitor_id=${visitorId}&after=${after}`, { method: 'GET' });
  }
  // Whether someone from the team is online, for the widget's header.
  if (request.method === 'GET' && url.pathname === '/api/chat/status') return forward('/chat/status', { method: 'GET' });
  if (request.method === 'GET' && url.pathname === '/api/chat/history') {
    // The widget restoring a conversation after a refresh.
    const visitorId = url.searchParams.get('visitor_id') ?? '';
    if (!VISITOR_ID.test(visitorId)) return reply(400, { error: 'invalid query' });
    return forward(`/chat/history?visitor_id=${visitorId}`, { method: 'GET' });
  }
  const SIDE_ROUTES = { '/api/chat/person': ['visitor_id', 'accept'], '/api/chat/contact': ['visitor_id', 'email', 'name', 'note'] };
  if (request.method !== 'POST' || (url.pathname !== '/api/chat' && !Object.hasOwn(SIDE_ROUTES, url.pathname))) return reply(404, { error: 'not found' });
  if (Number(request.headers.get('Content-Length') ?? 0) > MAX_BODY) return reply(413, { error: 'too large' });
  if (Object.hasOwn(SIDE_ROUTES, url.pathname)) {
    // The Yes / No thanks buttons and the email form: forwarded with only their own fields.
    const raw = await request.text();
    if (raw.length > MAX_BODY) return reply(413, { error: 'too large' });
    let body;
    try { body = JSON.parse(raw); } catch { return reply(400, { error: 'invalid json' }); }
    if (!VISITOR_ID.test(String(body?.visitor_id ?? ''))) return reply(400, { error: 'invalid visitor_id' });
    if (url.pathname === '/api/chat/person' && typeof body.accept !== 'boolean') return reply(400, { error: 'accept is required' });
    const fields = Object.fromEntries(SIDE_ROUTES[url.pathname].filter((k) => Object.hasOwn(body, k)).map((k) => [k, body[k]]));
    return forward(url.pathname.replace('/api', ''), { method: 'POST', body: JSON.stringify(fields) });
  }
  const raw = await request.text();
  if (raw.length > MAX_BODY) return reply(413, { error: 'too large' });
  let body;
  try { body = JSON.parse(raw); } catch { return reply(400, { error: 'invalid json' }); }
  if (!VISITOR_ID.test(String(body?.visitor_id ?? ''))) return reply(400, { error: 'invalid visitor_id' });
  if (typeof body.message !== 'string' || !body.message.trim() || body.message.length > MAX_MESSAGE) return reply(400, { error: `message must be 1-${MAX_MESSAGE} characters` });

  let ticket;
  if (env.TURNSTILE_SECRET) {
    if (!(await ticketValid(env.TICKET_SECRET, body.visitor_id, body.ticket, now()))) {
      const form = new FormData();
      form.append('secret', env.TURNSTILE_SECRET);
      form.append('response', String(body.turnstile_token ?? ''));
      form.append('remoteip', ip);
      const check = await fetchImpl('https://challenges.cloudflare.com/turnstile/v0/siteverify', { method: 'POST', body: form }).then((r) => r.json()).catch(() => ({}));
      if (!check.success) return reply(403, { status: 'verify', error: 'human check required' });
      ticket = await issueTicket(env.TICKET_SECRET, body.visitor_id, now());
    }
  }
  const res = await forward('/chat', { method: 'POST', body: JSON.stringify({ visitor_id: body.visitor_id, message: body.message }) });
  if (!ticket) return res;
  const data = await res.json();
  return reply(res.status, { ...data, ticket });
}

export default { fetch: (request, env) => handle(request, env) };
