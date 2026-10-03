import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { randomBytes, timingSafeEqual } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { SwarmClient } from '@swarmagent/sdk';
import { operations } from './operations.mjs';
import { onboarding } from './onboarding.mjs';

export function appHandler(sdk, { origin, accessToken }) {
  const url = new URL(origin);
  if (url.origin !== origin || url.hostname !== '127.0.0.1' || url.protocol !== 'http:') throw new Error('Use exact loopback HTTP origin; remote access requires a private tunnel');
  if (typeof accessToken !== 'string' || accessToken.length < 32) throw new Error('APP_ACCESS_TOKEN must contain at least 32 characters');
  const run = operations(sdk), sessions = new Map();
  let attempts = 0, reset = 0, inflight = 0, streams = 0;
  const equal = (a, b) => typeof a === 'string' && Buffer.byteLength(a) === Buffer.byteLength(b) && timingSafeEqual(Buffer.from(a), Buffer.from(b));
  return async (req, res) => {
    res.setHeader('Cache-Control', 'no-store');
    res.setHeader('X-Content-Type-Options', 'nosniff');
    res.setHeader('Referrer-Policy', 'no-referrer');
    res.setHeader('Content-Security-Policy', "default-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'");
    const json = (status, data) => { res.writeHead(status, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(data)); };
    if (req.headers.host !== url.host || (req.headers.origin && req.headers.origin !== origin) || (req.headers['sec-fetch-site'] && !['same-origin', 'none'].includes(req.headers['sec-fetch-site']))) return json(403, { error: 'Invalid origin' });
    if (inflight >= 16) return json(429, { error: 'Busy' });
    inflight++;
    try {
      const files = { '/': ['index.html', 'text/html'], '/app.js': ['app.js', 'text/javascript'], '/style.css': ['style.css', 'text/css'] };
      if (req.method === 'GET' && Object.hasOwn(files, req.url)) {
        const [name, type] = files[req.url];
        const bytes = await readFile(new URL(`public/${name}`, import.meta.url));
        res.writeHead(200, { 'Content-Type': type }); res.end(bytes); return;
      }
      if (req.method !== 'POST' || req.headers.origin !== origin || req.headers['content-type'] !== 'application/json') return json(403, { error: 'JSON same-origin POST required' });
      const chunks = []; let size = 0;
      for await (const chunk of req) { size += chunk.length; if (size > 65536) return json(413, { error: 'Request too large' }); chunks.push(chunk); }
      const b = JSON.parse(Buffer.concat(chunks).toString());
      if (req.url === '/login') {
        if (Date.now() > reset) { attempts = 0; reset = Date.now() + 60000; }
        if (++attempts > 10) return json(429, { error: 'Try again later' });
        if (!equal(b.token, accessToken)) return json(401, { error: 'Invalid app access token' });
        for (const [id, session] of sessions) if (session.expires < Date.now()) sessions.delete(id);
        if (sessions.size >= 8) return json(429, { error: 'Login limit reached' });
        const id = randomBytes(32).toString('hex'), csrf = randomBytes(32).toString('hex');
        sessions.set(id, { csrf, expires: Date.now() + 8 * 3600000, streams: new Set() });
        res.setHeader('Set-Cookie', `hub=${id}; Path=/; HttpOnly; SameSite=Strict; Max-Age=28800`);
        return json(200, { csrf });
      }
      const id = /(?:^|;\s*)hub=([a-f0-9]{64})(?:;|$)/.exec(req.headers.cookie ?? '')?.[1], session = sessions.get(id);
      if (!session || session.expires <= Date.now()) {
        if (session) { session.login = undefined; session.loginAttempt = undefined; for (const stop of session.streams) stop(); sessions.delete(id); }
        return json(401, { error: 'Unlock this app' });
      }
      if (!equal(req.headers['x-csrf-token'], session.csrf)) return json(403, { error: 'Invalid CSRF token' });
      if (req.url === '/logout') { session.login = undefined; session.loginAttempt = undefined; for (const stop of session.streams) stop(); sessions.delete(id); res.setHeader('Set-Cookie', 'hub=; Path=/; HttpOnly; SameSite=Strict; Max-Age=0'); return json(200, { ok: true }); }
      if (req.url === '/provider') {
        const result = await onboarding(sdk, session)(b.op, b);
        if (!sessions.has(id) || session.expires <= Date.now()) return json(401, { error: 'Unlock this app' });
        return json(200, result);
      }
      if (req.url === '/stream') {
        if (session.streams.size >= 2 || streams >= 8) return json(429, { error: 'Stream limit reached' });
        if (typeof b.id !== 'string' || !b.id || b.id.length > 128 || (b.session_id !== undefined && (typeof b.session_id !== 'string' || !b.session_id || b.session_id.length > 128))) return json(400, { error: 'Valid agent and conversation required' });
        const controller = new AbortController();
        let watch;
        const stop = () => { if (controller.signal.aborted) return; controller.abort(); watch?.dispose(); res.end(); };
        streams++;
        session.streams.add(stop);
        res.on('close', stop);
        const expiry = setTimeout(stop, Math.max(0, session.expires - Date.now()));
        try {
          // SDK performs ownership checks before emitting data. No raw daemon frames escape.
          const emit = data => {
            if (controller.signal.aborted || session.expires <= Date.now() || !sessions.has(id)) return stop();
            const line = JSON.stringify(data) + '\n';
            if (Buffer.byteLength(line) > 1024 * 1024) return stop();
            if (!res.headersSent) res.writeHead(200, { 'Content-Type': 'application/x-ndjson' });
            if (!res.write(line)) stop(); // Bound slow-client buffering; reconnect rehydrates.
          };
          watch = b.session_id
            ? await sdk.apps.watchConversation(b.id, b.session_id, { signal: controller.signal, onChange: state => emit({ conversation: { messages: state.messages, live: state.live, status: state.status } }) })
            : sdk.apps.watchResults(b.id, { signal: controller.signal, onChange: results => emit({ results }) });
          await watch.done;
        } catch {
          if (!controller.signal.aborted) {
            if (!res.headersSent) json(400, { error: 'Stream unavailable. Check permissions.' });
            else res.end();
          }
        } finally { streams--; clearTimeout(expiry); session.streams.delete(stop); res.removeListener('close', stop); stop(); }
        return;
      }
      if (req.url !== '/api') return json(404, { error: 'Unknown route' });
      return json(200, await run(b.op, b));
    } catch (error) {
      // Never reflect credentials, SDK response payloads or filesystem details.
      const status = [400, 401, 403, 404, 409, 429].includes(error?.status) ? error.status : 400;
      return json(status, { error: status === 409 ? 'Revision changed; reload before saving.' : 'Operation failed. Check configuration and permissions.' });
    } finally { inflight--; }
  };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const port = Number(process.env.PORT || 8787), origin = `http://127.0.0.1:${port}`;
  if (!process.env.SWARM_SOCKET_PATH && !process.env.SWARM_AUTH_TOKEN) throw new Error('Configure private SDK transport first');
  const server = createServer({ maxHeaderSize: 8192 }, appHandler(new SwarmClient(), { origin, accessToken: process.env.APP_ACCESS_TOKEN }));
  server.requestTimeout = 35000; server.headersTimeout = 10000; server.maxConnections = 32;
  server.on('upgrade', (_req, socket) => socket.destroy());
  server.listen(port, '127.0.0.1', () => console.log(`Agent hub: ${origin}`));
}
