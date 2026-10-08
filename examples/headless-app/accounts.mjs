import { createHmac, randomBytes, scrypt as scryptCallback, timingSafeEqual } from 'node:crypto';
import { readFile, rename, rm, writeFile } from 'node:fs/promises';
import { promisify } from 'node:util';
import qrcode from 'qrcode-generator';
import { reject } from './boundary.mjs';

// The owner's sign-in: a username and password, plus an optional authenticator
// code (TOTP, RFC 6238) that password managers such as 1Password can fill.
// One account per installation, stored in one private JSON file (mode 0600)
// that holds only a salted scrypt hash and the authenticator key.
//
// Reusable: give it a file path and call register/verify from your own routes.

const scrypt = promisify(scryptCallback);
const SCRYPT = { N: 1 << 15, r: 8, p: 1, maxmem: 64 * 1024 * 1024, keylen: 64 };
const USERNAME = /^[a-z0-9][a-z0-9._-]{1,31}$/;
const PERIOD = 30, DIGITS = 6;

export const MIN_PASSWORD = 12;

async function hash(password, salt = randomBytes(16)) {
  const key = await scrypt(password.normalize('NFKC'), salt, SCRYPT.keylen, SCRYPT);
  return { salt: salt.toString('base64'), hash: key.toString('base64'), N: SCRYPT.N, r: SCRYPT.r, p: SCRYPT.p };
}
async function matches(password, stored) {
  const salt = Buffer.from(stored.salt, 'base64'), want = Buffer.from(stored.hash, 'base64');
  const got = await scrypt(password.normalize('NFKC'), salt, want.length, { N: stored.N, r: stored.r, p: stored.p, maxmem: SCRYPT.maxmem });
  return timingSafeEqual(got, want);
}

const ALPHABET = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567';
export function base32(bytes) {
  let bits = 0, value = 0, out = '';
  for (const byte of bytes) {
    value = (value << 8) | byte; bits += 8;
    while (bits >= 5) { out += ALPHABET[(value >>> (bits - 5)) & 31]; bits -= 5; }
  }
  if (bits > 0) out += ALPHABET[(value << (5 - bits)) & 31];
  return out;
}
function unbase32(text) {
  let bits = 0, value = 0; const out = [];
  for (const char of text.replace(/=+$/, '')) {
    const index = ALPHABET.indexOf(char);
    if (index < 0) throw new Error('invalid base32');
    value = (value << 5) | index; bits += 5;
    if (bits >= 8) { out.push((value >>> (bits - 8)) & 255); bits -= 8; }
  }
  return Buffer.from(out);
}
export function totp(secret, step) {
  const counter = Buffer.alloc(8); counter.writeBigUInt64BE(BigInt(step));
  const mac = createHmac('sha1', unbase32(secret)).update(counter).digest();
  const offset = mac[mac.length - 1] & 15;
  return String((mac.readUInt32BE(offset) & 0x7fffffff) % 10 ** DIGITS).padStart(DIGITS, '0');
}
function qr(text) {
  const code = qrcode(0, 'M'); code.addData(text); code.make();
  const size = code.getModuleCount(); let cells = '';
  for (let y = 0; y < size; y++) for (let x = 0; x < size; x++) cells += code.isDark(y, x) ? '1' : '0';
  return { size, cells };
}

/**
 * @param {{ file: string, issuer?: string, now?: () => number }} options
 */
export function createAccounts({ file, issuer = 'Swarm', now = Date.now }) {
  let pending = null, lastStep = 0;
  const failures = { count: 0, until: 0, window: 0 };

  async function load() {
    try { return JSON.parse(await readFile(file, 'utf8')); }
    catch (error) { if (error.code === 'ENOENT') return null; throw error; }
  }
  async function save(account) {
    const temp = `${file}.${randomBytes(6).toString('hex')}.tmp`;
    await writeFile(temp, JSON.stringify(account), { mode: 0o600 });
    try { await rename(temp, file); } catch (error) { await rm(temp, { force: true }); throw error; }
  }
  function password(value) {
    if (typeof value !== 'string' || value.length < MIN_PASSWORD) reject(400, `Use a password of at least ${MIN_PASSWORD} characters.`);
    if (value.length > 256) reject(400, 'That password is too long (at most 256 characters).');
    return value;
  }
  // Brute force: ten failures in a minute lock sign-in for five minutes.
  function throttle() {
    if (now() < failures.until) reject(429, 'Too many failed sign-ins. Wait five minutes, then try again.');
    if (now() > failures.window) { failures.count = 0; failures.window = now() + 60_000; }
  }
  function failed(message) {
    if (++failures.count >= 10) failures.until = now() + 5 * 60_000;
    reject(401, message);
  }
  // A code is valid for its 30-second step and one step either side, once.
  function codeMatches(secret, code) {
    if (typeof code !== 'string' || !/^\d{6}$/.test(code.replace(/\s/g, ''))) return false;
    code = code.replace(/\s/g, '');
    const current = Math.floor(now() / 1000 / PERIOD);
    for (const step of [current - 1, current, current + 1]) {
      const want = totp(secret, step);
      if (step > lastStep && timingSafeEqual(Buffer.from(want), Buffer.from(code))) { lastStep = step; return true; }
    }
    return false;
  }

  return {
    // Public: what the sign-in page must show. Nothing that helps guessing.
    async status() {
      const account = await load();
      return { registered: !!account, two_factor: !!account?.totp };
    },
    // For the signed-in owner only.
    async details() {
      const account = await load();
      if (!account) reject(409, 'Create the owner account first.');
      return { username: account.username, two_factor: !!account.totp, claimed_by: account.claimed_by || '', created_at: account.created_at };
    },
    async register({ username, password: value, claimedBy = '' }) {
      if (await load()) reject(409, 'This installation already has an owner. Sign in instead.');
      if (typeof username !== 'string' || !USERNAME.test(username)) reject(400, 'Username: 2–32 lowercase letters, digits, dots, dashes or underscores.');
      const account = { version: 1, username, password: await hash(password(value)), totp: null, claimed_by: claimedBy, created_at: now() };
      await save(account);
      return { username };
    },
    async verify({ username, password: value, code }) {
      throttle();
      const account = await load();
      if (!account) reject(409, 'Create the owner account first.');
      const userOk = typeof username === 'string' && username === account.username;
      const passwordOk = typeof value === 'string' && value.length <= 256 && await matches(value, account.password);
      // Check the code only after the password, so guesses cannot burn the
      // owner's current code; one message for every failure reveals nothing.
      const codeOk = userOk && passwordOk && (!account.totp || codeMatches(account.totp.secret, code));
      if (!codeOk) failed(account.totp ? 'Wrong username, password or code.' : 'Wrong username or password.');
      failures.count = 0;
      return { username: account.username };
    },
    // Two-step 2FA setup: the key is kept in memory until a code proves the
    // authenticator has it, so a half-finished setup cannot lock the owner out.
    async startTwoFactor() {
      const account = await load();
      if (!account) reject(409, 'Create the owner account first.');
      if (account.totp) reject(409, 'Two-factor sign-in is already on.');
      const secret = base32(randomBytes(20));
      const label = encodeURIComponent(`${issuer}:${account.username}`);
      const uri = `otpauth://totp/${label}?secret=${secret}&issuer=${encodeURIComponent(issuer)}&algorithm=SHA1&digits=${DIGITS}&period=${PERIOD}`;
      pending = { secret, expires: now() + 10 * 60_000 };
      return { secret, uri, qr: qr(uri) };
    },
    async confirmTwoFactor(code) {
      const account = await load();
      if (!account) reject(409, 'Create the owner account first.');
      if (!pending || pending.expires < now()) reject(409, 'Start two-factor setup again; the last one expired.');
      if (!codeMatches(pending.secret, code)) reject(400, 'That code does not match. Check the time on your phone and try the next code.');
      account.totp = { secret: pending.secret, enabled_at: now() };
      pending = null;
      await save(account);
      return { two_factor: true };
    },
    async disableTwoFactor({ password: value, code }) {
      throttle();
      const account = await load();
      if (!account?.totp) reject(409, 'Two-factor sign-in is not on.');
      if (typeof value !== 'string' || !(await matches(value, account.password))) failed('Wrong password.');
      if (!codeMatches(account.totp.secret, code)) failed('That code is wrong or already used.');
      account.totp = null;
      await save(account);
      return { two_factor: false };
    },
    async changePassword({ current, next, code }) {
      throttle();
      const account = await load();
      if (!account) reject(409, 'Create the owner account first.');
      if (typeof current !== 'string' || !(await matches(current, account.password))) failed('Your current password is wrong.');
      if (account.totp && !codeMatches(account.totp.secret, code)) failed('Enter a current code from your authenticator app.');
      account.password = await hash(password(next));
      await save(account);
      return { ok: true };
    },
  };
}
