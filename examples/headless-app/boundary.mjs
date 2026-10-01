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
export function createBoundary(origin, secret) {
  const url = new URL(origin);
  if (url.protocol !== 'https:' || url.hostname !== '127.0.0.1' || url.origin !== origin) throw new Error('APP_ORIGIN must be an exact HTTPS IPv4 loopback origin');
  const sessions = new Map();
  let attempts = 0, reset = 0;
  return {
    check(req, sensitive = true) {
      if (req.headers.host !== url.host) reject(403, 'Invalid host.');
      if (req.headers.origin && req.headers.origin !== origin) reject(403, 'Invalid origin.');
      if (req.headers['sec-fetch-site'] && !['same-origin', 'none'].includes(req.headers['sec-fetch-site'])) reject(403, 'Cross-site request rejected.');
      if (sensitive && req.headers.origin !== origin) reject(403, 'Exact origin required.');
    },
    login(value) {
      if (Date.now() > reset) { attempts = 0; reset = Date.now() + 60_000; }
      if (++attempts > 10) reject(429, 'Too many login attempts. Wait one minute.');
      if (!equal(value, secret)) reject(401, 'Invalid installation secret.');
      for (const [id, s] of sessions) if (s.expires < Date.now()) sessions.delete(id);
      if (sessions.size >= 8) reject(429, 'Too many app sessions. Restart the app to revoke all sessions.');
      const id = randomBytes(32).toString('hex');
      const session = { csrf: randomBytes(32).toString('hex'), expires: Date.now() + 8 * 3600_000, streams: new Set() };
      sessions.set(id, session);
      return { id, session };
    },
    authenticate(req) {
      const id = /(?:^|;\s*)__Host-swapp=([a-f0-9]{64})(?:;|$)/.exec(req.headers.cookie ?? '')?.[1];
      const session = sessions.get(id);
      if (!session || session.expires < Date.now()) reject(401, 'Sign in to this installation.');
      if (!equal(req.headers['x-csrf-token'], session.csrf)) reject(403, 'Invalid CSRF token.');
      return session;
    },
    logout(session) {
      for (const [id, s] of sessions) if (s === session) sessions.delete(id);
      for (const close of session.streams) close();
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
