import test from 'node:test';
import assert from 'node:assert/strict';
import { onboarding } from '../onboarding.mjs';

// Purpose: onboarding's ephemeral session boundary must not accept a browser-supplied
// flow ID, expose credentials/errors or request activation. SDK receipts isolate
// BFF behavior without real credentials, provider traffic or host-setting changes.
test('sign-in binds the flow, allowlists output and requires consent', async () => {
  const calls = [], session = { expires: 100000 }, secret = 'never-return-this';
  const sdk = { auth: { codex: {
    start: async input => { calls.push(input); return { session_id: 'owned', status: 'waiting', auth_url: 'https://auth.openai.com/oauth/authorize?state=test', access_token: secret, error: secret }; },
    status: async id => { calls.push(id); return { status: 'success', credential: { id: secret, access_token: secret, auto_defaults: { applied: true } } }; },
    complete: async (id, callback) => { calls.push([id, callback]); return { status: 'success' }; },
  } } };
  const run = onboarding(sdk, session, () => 100);
  await assert.rejects(run('start', { method: 'manual' })); assert.equal(calls.length, 0);
  const start = await run('start', { method: 'manual', consent: true, active: true });
  assert.deepEqual(calls[0], { method: 'manual', active: false });
  assert.doesNotMatch(JSON.stringify(start), /owned|never-return-this/);
  const status = await run('status', { session_id: 'foreign' });
  assert.equal(calls[1], 'owned'); assert.equal(status.defaults_applied, true);
  assert.doesNotMatch(JSON.stringify(status), /never-return-this/);
  await run('complete', { session_id: 'foreign', callback: 'callback-code' });
  assert.deepEqual(calls[2], ['owned', 'callback-code']);
  await assert.rejects(onboarding(sdk, { expires: 100000 }, () => 100)('status', { session_id: 'owned' }));
  assert.equal(calls.length, 3);
});

// Purpose: cancellation/disconnect/expiry must stop hub access, not pretend to
// revoke daemon OAuth. In-flight status must not republish forgotten credentials.
test('forget and expiry reject further work and suppress in-flight results', async () => {
  let now = 10, finish, calls = 0;
  const session = { expires: 100 };
  const sdk = { auth: { codex: {
    start: async () => ({ session_id: 'owned', status: 'authorizing', expires_at: 90, verification_url: 'https://auth.openai.com/codex/device', user_code: 'ABCD-EFGH' }),
    status: async () => { calls++; return new Promise(resolve => { finish = resolve; }); },
  } } };
  const run = onboarding(sdk, session, () => now);
  assert.equal((await run('start', { method: 'device', consent: true })).user_code, 'ABCD-EFGH');
  await assert.rejects(run('complete', { callback: 'not-device' }));
  const pending = run('status', {});
  assert.deepEqual(await run('cancel', {}), { status: 'forgotten' });
  finish({ status: 'success' }); await assert.rejects(pending, /cancelled/);
  await assert.rejects(run('status', {})); assert.equal(calls, 1);
  await run('start', { method: 'device', consent: true }); now = 90;
  await assert.rejects(run('status', {}), /expired/); assert.equal(calls, 1);
});

// Purpose: provider-returned URLs are untrusted; reject unsafe schemes, userinfo,
// ports and lookalike hosts before a browser can navigate from the trusted hub.
test('provider link allowlist fails closed', async () => {
  for (const auth_url of ['javascript:alert(1)', 'http://auth.openai.com/oauth/authorize', 'https://auth.openai.com.evil.test/oauth/authorize', 'https://user@auth.openai.com/oauth/authorize', 'https://auth.openai.com:444/oauth/authorize', 'https://auth.openai.com/unexpected']) {
    const run = onboarding({ auth: { codex: { start: async () => ({ session_id: 'owned', status: 'waiting', auth_url }) } } }, { expires: 1000 }, () => 10);
    await assert.rejects(run('start', { method: 'manual', consent: true }));
  }
});

// Purpose: cancelling while start awaits provider I/O must not resurrect a flow
// after the response arrives. An injected promise proves the correlation race.
test('cancel also invalidates an in-flight start', async () => {
  let finish;
  const session = { expires: 1000 };
  const run = onboarding({ auth: { codex: { start: () => new Promise(resolve => { finish = resolve; }) } } }, session, () => 10);
  const pending = run('start', { method: 'manual', consent: true });
  await run('cancel', {});
  finish({ session_id: 'late', status: 'waiting' });
  await assert.rejects(pending, /cancelled/); assert.equal(session.login, undefined);
});
