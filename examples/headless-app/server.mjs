import { createServer } from 'node:https';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { SwarmClient } from '@swarm/sdk';
import { body, createBoundary, reject, safeError, text } from './boundary.mjs';
import { operations } from './operations.mjs';

const headers = {
  'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff', 'Referrer-Policy': 'no-referrer',
  'Content-Security-Policy': "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
};
export function appHandler(sdk, { origin, secret, project = '/project' }) {
  const boundary = createBoundary(origin, secret), ops = operations(sdk, project);
  let active = 0;
  const json = (res, status, value) => { res.writeHead(status, { ...headers, 'Content-Type': 'application/json' }); res.end(JSON.stringify(value)); };
  return async (req, res) => {
    let counted = false;
    try {
      boundary.check(req, req.method !== 'GET');
      if (active >= 16) reject(429, 'Too many concurrent operations.');
      active++; counted = true;
      const url = new URL(req.url, origin);
      if (url.search) reject(400, 'Query parameters are not supported.');
      if (req.method === 'GET') {
        const asset = { '/': ['index.html', 'text/html'], '/app.js': ['app.js', 'text/javascript'], '/style.css': ['style.css', 'text/css'] }[url.pathname];
        if (!asset) reject(404, 'Not found.');
        const content = await readFile(new URL(`./public/${asset[0]}`, import.meta.url));
        res.writeHead(200, { ...headers, 'Content-Type': asset[1] }); res.end(content); return;
      }
      if (req.method !== 'POST') reject(405, 'POST required.');
      const b = await body(req);
      if (url.pathname === '/login') {
        const { id, session } = boundary.login(b.secret);
        res.setHeader('Set-Cookie', `__Host-swapp=${id}; Path=/; Secure; HttpOnly; SameSite=Strict; Max-Age=28800`);
        json(res, 200, { csrf: session.csrf }); return;
      }
      const auth = boundary.authenticate(req);
      if (url.pathname === '/logout') {
        boundary.logout(auth);
        res.setHeader('Set-Cookie', '__Host-swapp=; Path=/; Secure; HttpOnly; SameSite=Strict; Max-Age=0');
        json(res, 200, { ok: true }); return;
      }
      if (url.pathname === '/watch') {
        if (auth.streams.size >= 2) reject(429, 'Close another session stream first.');
        const id = text(b.id); await ops.session(id);
        res.writeHead(200, { ...headers, 'Content-Type': 'application/x-ndjson' });
        res.flushHeaders();
        let watch, ended = false;
        const close = () => { if (ended) return; ended = true; watch?.dispose(); clearTimeout(expiry); auth.streams.delete(close); res.end(); };
        const expiry = setTimeout(close, Math.max(0, auth.expires - Date.now()));
        const send = value => {
          if (ended) return;
          const line = JSON.stringify(value) + '\n';
          // No unbounded queue for a paused/slow browser. Reconnect repairs via SDK.
          if (Buffer.byteLength(line) > 8 * 1024 * 1024 || res.writableLength > 1024 * 1024) { close(); return; }
          res.write(line);
        };
        auth.streams.add(close); res.on('close', close);
        watch = sdk.realtime.watchSession(id, {
          onChange: state => send({ state }),
          onError: () => send({ error: 'Realtime connection failed. Reconnect to repair from durable history.' }),
        });
        if (ended) watch.dispose();
        try { await watch.done; } catch { /* Error was redacted by onError. */ } finally { close(); }
        return;
      }
      if (url.pathname !== '/api') reject(404, 'Unknown application route.');
      json(res, 200, await ops.run(text(b.op), b));
    } catch (error) {
      const safe = safeError(error);
      if (!res.headersSent) json(res, safe.status, { error: safe.error }); else res.end();
    } finally { if (counted) active--; }
  };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    const config = '/etc/swarmd/headless-app';
    const secret = (await readFile(`${config}/login-secret`, 'utf8')).trim();
    if (!/^[a-f0-9]{64}$/.test(secret)) throw new Error('Invalid secret');
    const sdk = new SwarmClient({ socketPath: '/var/lib/swarmd/local-transport/api.sock', timeoutMs: 30_000 });
    // Only private local-transport identity; never inherit an injected SDK token.
    sdk.setToken('');
    const server = createServer({ key: await readFile(`${config}/tls.key`), cert: await readFile(`${config}/tls.crt`), maxHeaderSize: 8192 },
      appHandler(sdk, { origin: process.env.APP_ORIGIN || 'https://127.0.0.1:8443', secret }));
    server.requestTimeout = 35_000; server.headersTimeout = 10_000; server.maxConnections = 32;
    // No browser WebSocket proxy: only CSRF-protected POST streaming is supported.
    server.on('upgrade', (_req, socket) => socket.destroy());
    server.on('error', () => { console.error('App listener failed.'); process.exit(1); });
    server.listen(8443, '0.0.0.0'); // Container-only; launch must publish to host loopback.
    const shutdown = () => { server.close(); server.closeAllConnections(); };
    process.on('SIGTERM', shutdown); process.on('SIGINT', shutdown);
  } catch { console.error('App startup failed. Verify the private installation files.'); process.exitCode = 1; }
}
