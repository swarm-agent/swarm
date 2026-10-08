import { createServer as createHTTPS } from 'node:https';
import { createServer as createHTTP } from 'node:http';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { SwarmClient } from '@swarm-agent/sdk';
import { createAccounts } from './accounts.mjs';
import { body, createBoundary, isTailscaleServeOrigin, reject, safeError, text } from './boundary.mjs';
import { operations } from './operations.mjs';

const headers = {
  'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff', 'Referrer-Policy': 'no-referrer',
  'Content-Security-Policy': "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
};
// Who may create the first (owner) account. Behind Tailscale Serve, only a
// signed-in tailnet user: Serve sets Tailscale-User-Login and strips any copy a
// client sends, and the app listens on host loopback only. On a loopback origin
// only this machine can reach the page at all.
export function claimant(req, origin) {
  if (!isTailscaleServeOrigin(origin)) return { allowed: true, login: '' };
  const login = req.headers['tailscale-user-login'];
  return typeof login === 'string' && /^[^\s]{1,200}$/.test(login) ? { allowed: true, login } : { allowed: false, login: '' };
}

export function appHandler(sdk, { origin, accounts, project = '/project', relayUrl = '', deviceName = '', aiUrl = '', aiIp = '', settingsFile = '' }) {
  const boundary = createBoundary(origin), ops = operations(sdk, project, { relayUrl, deviceName, aiUrl, aiIp, settingsFile });
  const signIn = (res, username) => {
    const { id, session } = boundary.open(username);
    res.setHeader('Set-Cookie', boundary.cookie(id));
    return { csrf: session.csrf, username };
  };
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
      // Sign-in API (public): status, first-owner sign-up, sign-in.
      if (url.pathname === '/auth/status') {
        json(res, 200, { ...(await accounts.status()), can_register: claimant(req, origin).allowed }); return;
      }
      if (url.pathname === '/auth/register') {
        const who = claimant(req, origin);
        if (!who.allowed) reject(403, 'Open this page from your own device on your tailnet to create the owner account.');
        const username = typeof b.username === 'string' ? b.username.trim().toLowerCase() : '';
        await accounts.register({ username, password: b.password, claimedBy: who.login });
        await ops.ensureOwner(username);
        json(res, 200, signIn(res, username)); return;
      }
      if (url.pathname === '/auth/login') {
        const username = typeof b.username === 'string' ? b.username.trim().toLowerCase() : '';
        const user = await accounts.verify({ username, password: b.password, code: typeof b.code === 'string' ? b.code : '' });
        json(res, 200, signIn(res, user.username)); return;
      }
      const auth = boundary.authenticate(req);
      if (url.pathname === '/auth/logout') {
        boundary.logout(auth);
        res.setHeader('Set-Cookie', boundary.cookie('', 0));
        json(res, 200, { ok: true }); return;
      }
      if (url.pathname.startsWith('/auth/')) {
        const done = () => { boundary.logoutOthers(auth); };
        switch (url.pathname) {
          case '/auth/me': json(res, 200, await accounts.details()); return;
          case '/auth/2fa/start': json(res, 200, await accounts.startTwoFactor()); return;
          case '/auth/2fa/confirm': { const r = await accounts.confirmTwoFactor(b.code); done(); json(res, 200, r); return; }
          case '/auth/2fa/disable': { const r = await accounts.disableTwoFactor({ password: b.password, code: b.code }); done(); json(res, 200, r); return; }
          case '/auth/password': { const r = await accounts.changePassword({ current: b.current, next: b.next, code: b.code }); done(); json(res, 200, r); return; }
          default: reject(404, 'Unknown sign-in route.');
        }
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
      json(res, 200, await ops.run(text(b.op), b, auth.username));
    } catch (error) {
      const safe = safeError(error);
      if (!res.headersSent) json(res, safe.status, { error: safe.error }); else res.end();
    } finally { if (counted) active--; }
  };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    const config = '/etc/swarmd/headless-app';
    const sdk = new SwarmClient({ socketPath: '/var/lib/swarmd/local-transport/api.sock', timeoutMs: 30_000 });
    // Only private local-transport identity; never inherit an injected SDK token.
    sdk.setToken('');
    const origin = process.env.APP_ORIGIN || 'http://127.0.0.1:8443';
    // Installer defaults for "Connect to Claude"; the owner can edit both.
    const relayUrl = process.env.APP_RELAY_URL || '', deviceName = process.env.APP_DEVICE_NAME || '';
    // Tailnet address of the AI gateway, set by the installer when it publishes one.
    const aiUrl = /^https:\/\/[a-z0-9.-]+\.ts\.net:\d+\/mcp$/.test(process.env.APP_AI_URL || '') ? process.env.APP_AI_URL : '';
    const aiIp = /^100\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(process.env.APP_AI_IP || '') ? process.env.APP_AI_IP : '';
    const accounts = createAccounts({ file: `${config}/account.json`, issuer: `Swarm ${deviceName || 'app'}` });
    const handler = appHandler(sdk, { origin, accounts, relayUrl, deviceName, aiUrl, aiIp, settingsFile: `${config}/settings.json` }); // Validate before listening.
    // Tailscale Serve terminates TLS on the host; this listener stays plain HTTP
    // and must be published to host loopback only.
    const server = origin.startsWith('https:') && !isTailscaleServeOrigin(origin)
      ? createHTTPS({ key: await readFile(`${config}/tls.key`), cert: await readFile(`${config}/tls.crt`), maxHeaderSize: 8192 }, handler)
      : createHTTP({ maxHeaderSize: 8192 }, handler);
    server.requestTimeout = 35_000; server.headersTimeout = 10_000; server.maxConnections = 32;
    // No browser WebSocket proxy: only CSRF-protected POST streaming is supported.
    server.on('upgrade', (_req, socket) => socket.destroy());
    server.on('error', () => { console.error('App listener failed.'); process.exit(1); });
    server.listen(8443, '0.0.0.0'); // Container-only; launch must publish to host loopback.
    const shutdown = () => { server.close(); server.closeAllConnections(); };
    process.on('SIGTERM', shutdown); process.on('SIGINT', shutdown);
  } catch { console.error('App startup failed. Verify the private installation files.'); process.exitCode = 1; }
}
