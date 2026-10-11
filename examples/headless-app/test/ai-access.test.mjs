import assert from 'node:assert/strict';
import { test } from 'node:test';
import { operations } from '../operations.mjs';

// Purpose: the AI access page lists only live AI keys (never other tokens),
// creates keys only at the two AI levels with a bounded lifetime, shows the
// full key once at creation, and revokes only AI keys.
// Boundary: operations().run with an SDK fixture; the daemon's own enforcement
// of key levels is covered by TestAIKeysLimitSwarmControlTools.
function fakeSdk() {
  const calls = [];
  const now = Date.now();
  let tokens = [
    { id: 'ai1', name: 'claude', scopes: ['swarm:read', 'sessions:read'], token_hint: 'swk_…a1', created_at: now, expires_at: now + 1e9 },
    { id: 'ai2', name: 'writer', scopes: ['swarm:read', 'swarm:write', 'sessions:write'], token_hint: 'swk_…a2', created_at: now, expires_at: now + 1e9, last_used_at: now },
    { id: 'gone', name: 'old', scopes: ['swarm:read'], revoked: true },
    { id: 'expired', name: 'old', scopes: ['swarm:read'], expires_at: now - 1 },
    { id: 'sdk', name: 'gateway', scopes: ['sessions:read', 'sessions:write'] },
  ];
  return {
    calls,
    onboarding: { get: async () => ({ identity: { bootstrapped: true } }) },
    auth: {
      listScopedTokens: async () => tokens,
      createAIKey: async input => { calls.push(['create', input]); const record = { id: 'new', name: input.name, scopes: input.access === 'write' ? ['swarm:read', 'swarm:write'] : ['swarm:read'], token_hint: 'swk_…nw', created_at: now, expires_at: now + input.expires_in_seconds * 1000 }; tokens = [...tokens, record]; return { token: 'swk_secret', record }; },
      revokeScopedToken: async id => { calls.push(['revoke', id]); tokens = tokens.map(t => t.id === id ? { ...t, revoked: true } : t); return {}; },
    },
  };
}
const url = 'https://box.tail1234.ts.net:8444/mcp';

test('AI access lists, creates and revokes only AI keys', async () => {
  const sdk = fakeSdk(), ops = operations(sdk, '/project', { aiUrl: url });
  const listed = await ops.run('ai-keys', {});
  assert.equal(listed.url, url);
  assert.deepEqual(listed.keys.map(k => [k.id, k.access]), [['ai1', 'read'], ['ai2', 'write']]);
  assert.ok(listed.keys.every(k => !('token' in k)), 'listing never includes a key');

  const created = await ops.run('ai-key-create', { name: 'laptop', access: 'read', days: 30 });
  assert.equal(created.token, 'swk_secret');
  assert.equal(created.url, url);
  assert.deepEqual(sdk.calls.at(-1), ['create', { name: 'laptop', access: 'read', expires_in_seconds: 30 * 86400 }]);

  for (const bad of [{ name: 'x', access: 'admin', days: 30 }, { name: 'x', access: 'read', days: 10000 }, { name: 'x', access: 'read' }]) {
    await assert.rejects(ops.run('ai-key-create', bad), e => e.status === 400);
  }
  await assert.rejects(ops.run('ai-key-revoke', { id: 'sdk' }), e => e.status === 404, 'other tokens cannot be revoked here');
  const after = await ops.run('ai-key-revoke', { id: 'ai2' });
  assert.deepEqual(after.keys.map(k => k.id), ['ai1', 'new']);
});

test('AI access without a published gateway reports no address', async () => {
  const listed = await operations(fakeSdk(), '/project', {}).run('ai-keys', {});
  assert.equal(listed.url, '');
});
