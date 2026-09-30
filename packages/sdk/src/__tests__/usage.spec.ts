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
