import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp, readFile, rm, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createAccounts, totp } from '../accounts.mjs';

async function fixture(t) {
  const dir = await mkdtemp(join(tmpdir(), 'workshop-accounts-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  let clock = 1_800_000_000_000;
  const file = join(dir, 'account.json');
  const accounts = createAccounts({ file, now: () => clock });
  return { accounts, file, tick: ms => { clock += ms; }, step: () => Math.floor(clock / 30_000) };
}
const owner = { username: 'roy', password: 'correct horse battery' };

// Purpose: one owner account, stored only as a private scrypt hash; sign-in
// needs the exact username and password; status reveals nothing useful for
// guessing. Boundary: createAccounts with a temporary file and a fake clock.
test('owner registers once and signs in with username and password', async t => {
  const { accounts, file } = await fixture(t);
  assert.deepEqual(await accounts.status(), { registered: false, two_factor: false });
  await assert.rejects(accounts.verify(owner), /Create the owner/);
  await assert.rejects(accounts.register({ username: 'Roy!', password: owner.password }), /Username/);
  await assert.rejects(accounts.register({ username: 'roy', password: 'short' }), /at least 12/);
  await accounts.register({ ...owner, claimedBy: 'roy@example.com' });
  await assert.rejects(accounts.register({ username: 'other', password: owner.password }), /already has an owner/);
  assert.deepEqual(await accounts.status(), { registered: true, two_factor: false });
  assert.equal((await accounts.details()).claimed_by, 'roy@example.com');

  const stored = await readFile(file, 'utf8');
  assert.ok(!stored.includes(owner.password));
  assert.equal((await stat(file)).mode & 0o777, 0o600);

  assert.deepEqual(await accounts.verify(owner), { username: 'roy' });
  await assert.rejects(accounts.verify({ ...owner, password: 'correct horse battery!' }), /Wrong username or password/);
  await assert.rejects(accounts.verify({ ...owner, username: 'root' }), /Wrong username or password/);
});

// Purpose: guessing is bounded: ten failures lock sign-in for five minutes,
// even for the right password, then it opens again.
test('failed sign-ins lock for five minutes', async t => {
  const { accounts, tick } = await fixture(t);
  await accounts.register(owner);
  for (let i = 0; i < 10; i++) await assert.rejects(accounts.verify({ ...owner, password: 'not the password' }), /Wrong/);
  await assert.rejects(accounts.verify(owner), /Too many failed/);
  tick(5 * 60_000 + 1);
  assert.deepEqual(await accounts.verify(owner), { username: 'roy' });
});

// Purpose: 2FA turns on only after a code proves the authenticator has the key;
// then sign-in needs a current code, each code works once, and a wrong password
// never consumes the owner's code. Turning it off needs password and code.
test('two-factor codes are required, single-use and confirmed before use', async t => {
  const { accounts, tick, step } = await fixture(t);
  await accounts.register(owner);
  const setup = await accounts.startTwoFactor();
  assert.match(setup.uri, /^otpauth:\/\/totp\/Swarm%3Aroy\?secret=[A-Z2-7]+&issuer=Swarm/);
  assert.equal(setup.qr.cells.length, setup.qr.size ** 2);
  assert.deepEqual(await accounts.status(), { registered: true, two_factor: false });
  await assert.rejects(accounts.confirmTwoFactor('000000'), /does not match/);
  await accounts.confirmTwoFactor(totp(setup.secret, step()));
  assert.deepEqual(await accounts.status(), { registered: true, two_factor: true });

  tick(30_000);
  await assert.rejects(accounts.verify(owner), /Wrong username, password or code/);
  const code = totp(setup.secret, step());
  await assert.rejects(accounts.verify({ ...owner, password: 'wrong password here', code }), /Wrong username, password or code/);
  assert.deepEqual(await accounts.verify({ ...owner, code }), { username: 'roy' });
  await assert.rejects(accounts.verify({ ...owner, code }), /Wrong username, password or code/);

  tick(30_000);
  await assert.rejects(accounts.changePassword({ current: owner.password, next: 'a brand new password' }), /authenticator/);
  await accounts.changePassword({ current: owner.password, next: 'a brand new password', code: totp(setup.secret, step()) });
  tick(30_000);
  await assert.rejects(accounts.disableTwoFactor({ password: owner.password, code: totp(setup.secret, step()) }), /Wrong password/);
  await accounts.disableTwoFactor({ password: 'a brand new password', code: totp(setup.secret, step()) });
  assert.deepEqual(await accounts.verify({ username: 'roy', password: 'a brand new password' }), { username: 'roy' });
});

// Purpose: the TOTP implementation matches RFC 6238's SHA-1 test vector.
test('totp matches the RFC 6238 test vector', () => {
  // Base32 of the ASCII key "12345678901234567890"; time 59 s -> 94287082 (8 digits), 287082 (6).
  assert.equal(totp('GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ', Math.floor(59 / 30)), '287082');
});
