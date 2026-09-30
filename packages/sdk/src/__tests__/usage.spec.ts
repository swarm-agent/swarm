import assert from 'node:assert/strict';
import { test } from 'node:test';
import { SwarmUsageNamespace } from '../usage.js';
import type { SwarmTransport } from '../transport.js';

// Purpose: SDK scope hydration must preserve incompleteness and use a bounded
// canonical point read, not infer zero usage or poll. Boundary: usage.scope.
// A transport fixture is the narrowest wire-contract layer, not live telemetry.
test('usage scope preserves unrecorded incomplete history and encoded identity', async () => {
  const calls: string[] = [];
  const data = { usage: { kind: 'task', id: 'task/a', history_complete: false }, recorded: false };
  const transport = { request: async (path: string) => { calls.push(path); return { data }; } } as unknown as SwarmTransport;
  const usage = new SwarmUsageNamespace(transport);
  assert.deepEqual(await usage.scope({ kind: 'task', project_id: 'project', id: 'task/a' }), data);
  assert.deepEqual(calls, ['/v3/usage/scope?kind=task&id=task%2Fa&project_id=project']);
  await assert.rejects(usage.scope({ kind: 'task', id: 'task' }));
  await assert.rejects(usage.scope({ kind: 'worker', id: 'worker', project_id: 'project' }));
  assert.equal(calls.length, 1);
});

// Purpose: UTC window hydration must issue one bounded read and reject invalid
// dates before transport, preserving genuine billed components/provenance.
// Boundary: SwarmUsageNamespace.scope; this is a wire contract, not telemetry.
test('usage scope UTC day validates dates and preserves billed breakdown', async () => {
  const calls: string[] = [];
  const data = { usage: { kind: 'worker', id: 'worker', total_tokens: 100,
    input_tokens: 40, output_tokens: 20, cache_read_tokens: 10,
    cache_write_tokens: 15, thinking_tokens: 15, media_cost_usd: 2,
    provider_cost_usd: 1, catalog_cost_usd: 2, coverage: 'observed_receipts_only',
    history_complete: false }, recorded: true };
  const transport = { request: async (path: string) => { calls.push(path); return { data }; } } as unknown as SwarmTransport;
  const usage = new SwarmUsageNamespace(transport);
  assert.deepEqual(await usage.scope({ kind: 'worker', id: 'worker' }, '2026-09-30'), data);
  assert.deepEqual(calls, ['/v3/usage/scope?kind=worker&id=worker&date=2026-09-30']);
  for (const date of ['2026-02-30', '../../other', '2026-9-30', '']) {
    await assert.rejects(usage.scope({ kind: 'worker', id: 'worker' }, date));
  }
  assert.equal(calls.length, 1);
});
