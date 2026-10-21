import assert from 'node:assert/strict';
import { test } from 'node:test';
import { SwarmAuthNamespace } from '../auth.js';
import type { SwarmTransport } from '../transport.js';

function fixture() {
  const calls: Array<[string, string, Record<string, unknown> | undefined]> = [];
  const transport = { request: async (path: string, options: { method?: string; body?: Record<string, unknown> } = {}) => {
    calls.push([options.method ?? 'GET', path, options.body]);
    return { data: { ok: true, token: 'swk_x', record: { id: 'tok_1' }, tokens: [{ id: 'tok_1', key_kind: 'app', state: 'active' }] } };
  } } as unknown as SwarmTransport;
  return { calls, auth: new SwarmAuthNamespace(transport) };
}

// Purpose: keys need no lifetime (they work while used and stop after 90 days
// unused), so every mint takes expires_in_seconds only as an optional fixed
// end date and sends it as given. Boundary: SwarmAuthNamespace over a
// transport fixture; the daemon's rule is covered by Go tests.
test('keys are minted without a lifetime unless a fixed end date is given', async () => {
  const { calls, auth } = fixture();
  await auth.createScopedToken({ name: 'ci', scopes: ['automations:trigger'] });
  await auth.createAIKey({ name: 'assistant', access: 'read' });
  await auth.createAIKey({ name: 'temp', access: 'read', expires_in_seconds: 1800 });
  assert.deepEqual(calls[0], ['POST', '/v3/auth/tokens', { name: 'ci', scopes: ['automations:trigger'], worker_id: undefined, worker_name: undefined, expires_in_seconds: undefined }]);
  assert.deepEqual(calls[1], ['POST', '/v3/auth/tokens', { name: 'assistant', ai_access: 'read', expires_in_seconds: undefined }]);
  assert.deepEqual(calls[2], ['POST', '/v3/auth/tokens', { name: 'temp', ai_access: 'read', expires_in_seconds: 1800 }]);
});

// Purpose: the three kinds of key go through one call (createKey) with kind
// and its options; listKeys and revokeKey use the key routes. Boundary:
// SwarmAuthNamespace wire shape.
test('createKey, listKeys and revokeKey use the key routes', async () => {
  const { calls, auth } = fixture();
  await auth.createKey({ kind: 'app', name: 'watch', monitor: true });
  await auth.createKey({ kind: 'ai', name: 'claude', access: 'write' });
  await auth.createKey({ kind: 'bot', name: 'frontdesk', agent_name: 'frontdesk', operator: true, expires_in_seconds: 86400 });
  const keys = await auth.listKeys();
  await auth.revokeKey(' tok_1 ');
  assert.deepEqual(calls[0], ['POST', '/v3/auth/tokens', { kind: 'app', name: 'watch', monitor: true }]);
  assert.deepEqual(calls[1], ['POST', '/v3/auth/tokens', { kind: 'ai', name: 'claude', ai_access: 'write' }]);
  assert.deepEqual(calls[2], ['POST', '/v3/auth/tokens', { kind: 'bot', name: 'frontdesk', agent_name: 'frontdesk', operator: true, expires_in_seconds: 86400 }]);
  assert.deepEqual(calls[3], ['GET', '/v3/auth/tokens', undefined]);
  assert.equal(keys[0]?.key_kind, 'app');
  assert.deepEqual(calls[4], ['POST', '/v3/auth/tokens/tok_1/revoke', undefined]);
});
