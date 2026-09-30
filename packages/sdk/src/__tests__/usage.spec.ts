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

// Purpose: explicit repair must preserve bounded cursors/incomplete coverage and
// reject unbounded requests before transport. Owner SwarmUsageNamespace.repair;
// this transport seam proves SDK/API parity, not historical completeness.
test('usage repair is bounded explicit maintenance', async () => {
  const calls: unknown[] = [];
  const data = { scanned: 1, repaired: 1, unresolved: 0, next_cursor: 'opaque', history_complete: false };
  const transport = { request: async (path: string, options: unknown) => {
    calls.push([path, options]); return { data };
  } } as unknown as SwarmTransport;
  const usage = new SwarmUsageNamespace(transport);
  assert.deepEqual(await usage.repair('opaque', 1), data);
  assert.deepEqual(calls, [['/v3/usage/scopes/repair', { method: 'POST', body: { cursor: 'opaque', limit: 1 } }]]);
  for (const limit of [0, 101, 1.5, NaN]) await assert.rejects(usage.repair('', limit));
  assert.equal(calls.length, 1);
});

// Purpose: SDK must carry exact user policy revision, reject invalid limits
// before HTTP and never imply a policy edit resets billing. Owners workerBudget
// and setWorkerBudget; transport fixture is the narrowest SDK wire layer,
// including UTC status, account inflight state and honest coverage/limitations.
test('worker budget preserves revision and rejects invalid policies before transport', async () => {
  const calls: unknown[] = [];
  const data = { account_scope_id: 'account', worker_id: 'worker', revision: 1,
    daily_cost_limit_usd: 1, daily_tokens_limit: 100, updated_at: 1,
    date: '2026-01-01', usage: { coverage: 'observed_receipts_only' },
    blocked: true, blocked_reason: 'unsettled operation', inflight: true,
    remaining_cost_usd: 1, remaining_tokens: 100,
    account_policy: { enabled: true }, account_usage: { total_tokens: 0 },
    account_inflight: true, account_coverage: 'observed_receipts_only',
    account_remaining_cost_usd: null, account_remaining_tokens: null,
    limitations: 'not an invoice-hard cap' };
  const transport = { request: async (path: string, options: unknown) => {
    calls.push([path, options]); return { data };
  } } as unknown as SwarmTransport;
  const usage = new SwarmUsageNamespace(transport);
  assert.deepEqual(await usage.workerBudget('worker'), data);
  const policy = { expected_revision: 0, daily_cost_limit_usd: 1, daily_tokens_limit: 100 };
  assert.deepEqual(await usage.setWorkerBudget('worker', policy), data);
  assert.deepEqual(calls, [
    ['/v3/usage/worker-budget?worker_id=worker', { method: 'GET' }],
    ['/v3/usage/worker-budget?worker_id=worker', { method: 'PUT', body: policy }],
  ]);
  for (const value of [-1, NaN, Infinity]) {
    await assert.rejects(usage.setWorkerBudget('worker', { ...policy, daily_cost_limit_usd: value }));
  }
  await assert.rejects(usage.setWorkerBudget('worker', { ...policy, expected_revision: -1 }));
  await assert.rejects(usage.setWorkerBudget('worker', { ...policy, daily_tokens_limit: 1.5 }));
  await assert.rejects(usage.workerBudget('../worker'));
  assert.equal(calls.length, 2);
});

// Purpose: policy-only or malformed responses cannot masquerade as canonical
// status. Owner workerBudget; transport fixture is the narrowest SDK boundary.
test('worker budget rejects missing canonical status', async () => {
  const transport = { request: async () => ({ data: {
    account_scope_id: 'account', worker_id: 'worker', revision: 1,
    daily_cost_limit_usd: 0, daily_tokens_limit: 0, updated_at: 1,
  } }) } as unknown as SwarmTransport;
  await assert.rejects(new SwarmUsageNamespace(transport).workerBudget('worker'));
});
