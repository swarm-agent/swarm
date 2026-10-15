import assert from 'node:assert/strict';
import test from 'node:test';
import { SwarmLimitsNamespace } from '../limits.js';
import type { SwarmTransport } from '../transport.js';

// Purpose: SDK limits methods preserve expected_revision and all limits on the
// canonical route. The transport seam is the narrowest wire-shape regression test.
test('limits uses revisioned V3 read and replacement', async () => {
  const calls: unknown[] = [];
  const limits = { worker_job_concurrency: 2, job_tokens: 0, job_cost_usd: 0, delegation_waves: 5, active_children: 5, swarm_children: 5 };
  const transport = { request: async (path: string, options: unknown) => {
    calls.push([path, options]); return { data: { ...limits, revision: 1 } };
  } } as unknown as SwarmTransport;
  const api = new SwarmLimitsNamespace(transport);
  assert.equal((await api.get()).revision, 1);
  await api.set(1, limits);
  assert.deepEqual(calls, [ ['/v3/limits', { method: 'GET' }], ['/v3/limits', { method: 'PUT', body: { expected_revision: 1, limits } }] ]);
});
