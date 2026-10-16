import assert from 'node:assert/strict';
import { test } from 'node:test';
import { SwarmAuthNamespace } from '../auth.js';
import type { SwarmTransport } from '../transport.js';

// Purpose: every key the SDK mints must carry an explicit expiry, because the
// daemon refuses a mint without one (no default lifetime). The expiry is a
// required parameter in the types (the @ts-expect-error lines fail `tsc` if it
// becomes optional again) and is sent as given. Boundary: SwarmAuthNamespace
// over a transport fixture; the daemon's refusal is covered by Go tests.
test('scoped and AI keys are minted with the caller\'s explicit expiry', async () => {
  const calls: Array<[string, string, Record<string, unknown> | undefined]> = [];
  const transport = { request: async (path: string, options: { method?: string; body?: Record<string, unknown> } = {}) => {
    calls.push([options.method ?? 'GET', path, options.body]);
    return { data: { ok: true, token: 'swk_x', record: { id: 'tok_1' } } };
  } } as unknown as SwarmTransport;
  const auth = new SwarmAuthNamespace(transport);

  await auth.createScopedToken({ name: 'ci', scopes: ['automations:trigger'], expires_in_seconds: 86400 });
  await auth.createAIKey({ name: 'assistant', access: 'read', expires_in_seconds: 1800 });
  assert.deepEqual(calls[0], ['POST', '/v3/auth/tokens', { name: 'ci', scopes: ['automations:trigger'], worker_id: undefined, worker_name: undefined, expires_in_seconds: 86400 }]);
  assert.deepEqual(calls[1], ['POST', '/v3/auth/tokens', { name: 'assistant', ai_access: 'read', expires_in_seconds: 1800 }]);

  // Type-level only: omitting the expiry does not compile.
  const typeChecks = false;
  if (typeChecks) {
    // @ts-expect-error expires_in_seconds is required
    await auth.createScopedToken({ name: 'ci', scopes: ['automations:trigger'] });
    // @ts-expect-error expires_in_seconds is required
    await auth.createAIKey({ name: 'assistant', access: 'read' });
  }
  assert.equal(calls.length, 2);
});
