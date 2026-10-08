import { randomBytes, timingSafeEqual } from 'node:crypto';

export class AppError extends Error {
  constructor(status, message) { super(message); this.status = status; }
}
export function reject(status, message) { throw new AppError(status, message); }
export function text(value, max = 200) {
  if (typeof value !== 'string' || !value.trim() || value.length > max) reject(400, 'Invalid or missing input.');
  return value;
}
export function equal(a, b) {
  return typeof a === 'string' && typeof b === 'string' && Buffer.byteLength(a) === Buffer.byteLength(b) && timingSafeEqual(Buffer.from(a), Buffer.from(b));
}
export function safeError(error) {
  return error instanceof AppError ? { status: error.status, error: error.message } :
    { status: 502, error: 'Daemon operation failed. Check setup, provider readiness and selected model; then refresh. No automatic retry was made.' };
}
// A Tailscale Serve origin: HTTPS on a tailnet MagicDNS name, default port.
// Tailscale terminates TLS on the host and proxies to the loopback-published
// listener, so the name is reachable only from the owner's tailnet.
export function isTailscaleServeOrigin(origin) {
  try {
    const url = new URL(origin);
    return url.protocol === 'https:' && !url.port && url.origin === origin &&
      /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+\.ts\.net$/.test(url.hostname);
  } catch { return false; }
}
// Browser sessions after a successful sign-in (see accounts.mjs): HttpOnly,
// SameSite=Strict cookies, a CSRF token per session, exact Host and Origin.
export function createBoundary(origin) {
  const url = new URL(origin);
  const loopback = ['http:', 'https:'].includes(url.protocol) && url.hostname === '127.0.0.1' && url.origin === origin;
  if (!loopback && !isTailscaleServeOrigin(origin)) throw new Error('APP_ORIGIN must be an exact HTTP(S) IPv4 loopback origin or an HTTPS Tailscale Serve (*.ts.net) origin');
  // HTTP is supported only for host-loopback publication, never LAN/remote access.
  const secure = url.protocol === 'https:';
  const cookieName = secure ? '__Host-swapp' : 'swapp-loopback';
  const cookiePattern = new RegExp(`(?:^|;\\s*)${cookieName}=([a-f0-9]{64})(?:;|$)`);
  const sessions = new Map(), lifetime = 12 * 3600;
  return {
    cookie(id, maxAge = lifetime) {
      return `${cookieName}=${id}; Path=/; ${secure ? 'Secure; ' : ''}HttpOnly; SameSite=Strict; Max-Age=${maxAge}`;
    },
    check(req, sensitive = true) {
      if (req.headers.host !== url.host) reject(403, 'Invalid host.');
      if (req.headers.origin && req.headers.origin !== origin) reject(403, 'Invalid origin.');
      // A link from another site may open the public login shell. This exception
      // cannot authorize subresources, embedded pages, API reads or mutations.
      const publicNavigation = req.method === 'GET' && req.url === '/' &&
        req.headers['sec-fetch-mode'] === 'navigate' && req.headers['sec-fetch-dest'] === 'document';
      if (req.headers['sec-fetch-site'] && !['same-origin', 'none'].includes(req.headers['sec-fetch-site']) &&
        !(['same-site', 'cross-site'].includes(req.headers['sec-fetch-site']) && publicNavigation)) reject(403, 'Cross-site request rejected.');
      if (sensitive && req.headers.origin !== origin) reject(403, 'Exact origin required.');
    },
    // Call only after the account verified the sign-in.
    open(username) {
      for (const [id, s] of sessions) if (s.expires < Date.now()) sessions.delete(id);
      // Keep the newest sessions: signing in on a ninth device ends the oldest.
      while (sessions.size >= 8) this.logout(sessions.values().next().value);
      const id = randomBytes(32).toString('hex');
      const session = { username, csrf: randomBytes(32).toString('hex'), expires: Date.now() + lifetime * 1000, streams: new Set() };
      sessions.set(id, session);
      return { id, session };
    },
    authenticate(req) {
      const id = cookiePattern.exec(req.headers.cookie ?? '')?.[1];
      const session = sessions.get(id);
      if (!session || session.expires < Date.now()) reject(401, 'Sign in to continue.');
      if (!equal(req.headers['x-csrf-token'], session.csrf)) reject(403, 'Invalid CSRF token.');
      return session;
    },
    logout(session) {
      for (const [id, s] of sessions) if (s === session) sessions.delete(id);
      for (const close of session.streams) close();
    },
    // After a password or 2FA change: sign out every other browser.
    logoutOthers(keep) {
      for (const session of [...sessions.values()]) if (session !== keep) this.logout(session);
    },
  };
}
export async function body(req) {
  if (req.headers['content-type'] !== 'application/json') reject(415, 'JSON required.');
  let size = 0; const chunks = [];
  await new Promise((resolve, fail) => {
    req.on('data', chunk => {
      size += chunk.length;
      if (size > 65536) { fail(new AppError(413, 'Request exceeds 64 KiB.')); return; }
      chunks.push(chunk);
    });
    req.once('end', resolve);
    req.once('error', fail);
    req.once('aborted', () => fail(new AppError(400, 'Request aborted.')));
  });
  try {
    const value = JSON.parse(Buffer.concat(chunks).toString());
    if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error();
    return value;
  } catch { reject(400, 'Invalid JSON object.'); }
}
