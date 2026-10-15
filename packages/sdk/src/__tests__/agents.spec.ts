import assert from 'node:assert/strict';
import { test } from 'node:test';
import { SwarmAgentsNamespace } from '../agents.js';
import type { SwarmTransport } from '../transport.js';

// Purpose: the gateway and operator keys must always be minted with an expiry
// and the operator flag only where intended, and takeover / operator replies
// must go to their own routes with an idempotency key, so a retried reply is
// recorded once. Boundary: SwarmAgentsNamespace over a transport fixture; the
// daemon side (who may call these routes) is covered by Go tests.
test('gateway, operator and takeover calls use the reviewed wire shape', async () => {
  const calls: Array<[string, string, Record<string, unknown> | undefined]> = [];
  const transport = { request: async (path: string, options: { method?: string; body?: Record<string, unknown> } = {}) => {
    calls.push([options.method ?? 'GET', path, options.body]);
    if (path.endsWith('/takeover')) return { data: { ok: true, takeover: { state: options.method === 'POST' && options.body?.action === 'start' ? 'human' : 'agent' } } };
    if (path.endsWith('/operator-messages')) return { data: { ok: true, message: { role: 'assistant' } } };
    return { data: { token: 'swk_x', record: {} } };
  } } as unknown as SwarmTransport;
  const agents = new SwarmAgentsNamespace(transport);

  await agents.createGatewayToken('frontdesk', { expiresInSeconds: 3600, maxConcurrentRuns: 2 });
  await agents.createOperatorToken('frontdesk', { expiresInSeconds: 3600 });
  assert.deepEqual(calls[0], ['POST', '/v3/auth/tokens', { name: 'frontdesk gateway', agent_name: 'frontdesk', expires_in_seconds: 3600, max_concurrent_runs: 2 }]);
  assert.deepEqual(calls[1], ['POST', '/v3/auth/tokens', { name: 'frontdesk operator', agent_name: 'frontdesk', operator: true, expires_in_seconds: 3600 }]);

  assert.equal((await agents.takeOver('s/1', { clientRequestId: 'take-1' })).state, 'human');
  await agents.replyAsOperator('s/1', 'Hi, a person here.', { clientRequestId: 'reply-1' });
  assert.equal((await agents.handBack('s/1')).state, 'agent');
  assert.deepEqual(calls[2], ['POST', '/v3/sessions/s%2F1/takeover', { action: 'start', client_request_id: 'take-1' }]);
  assert.deepEqual(calls[3], ['POST', '/v3/sessions/s%2F1/operator-messages', { content: 'Hi, a person here.', client_request_id: 'reply-1' }]);
  assert.equal(calls[4][1], '/v3/sessions/s%2F1/takeover');
  assert.equal(calls[4][2]?.action, 'end');
  assert.match(String(calls[4][2]?.client_request_id), /^sdk-operator-/);
});
