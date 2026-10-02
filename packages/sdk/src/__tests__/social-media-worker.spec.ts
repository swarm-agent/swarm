import assert from 'node:assert/strict';
import { test } from 'node:test';
import { observeSocialMediaWorker } from '../../examples/social-media-worker.js';
import type { SwarmClient } from '../client.js';

// Purpose: the observer must preserve actual backend records/receipts, never
// generate work or approve publication. This unit seam tests only consumer logic,
// not provider execution or live publication; mismatched lineage fails closed.
test('observer returns records unchanged and rejects unrelated output', async () => {
  const run = { id: 'run', worker_id: 'worker', session_id: 'session', status: 'failed' };
  const output = { id: 'output', worker_id: 'worker', session_id: 'session', status: 'pending_review', action_result: undefined as unknown };
  const calls: string[] = [];
  const client = {
    workers: { get: async () => { calls.push('worker'); return { id: 'worker' }; }, getRun: async () => { calls.push('run'); return run; } },
    deliverables: { get: async () => { calls.push('output'); return output; } },
  } as unknown as SwarmClient;
  const ids = { workerId: 'worker', runId: 'run', deliverableId: 'output' };
  const result = await observeSocialMediaWorker(client, ids);
  assert.equal(result.run, run);
  assert.equal(result.deliverable, output);
  assert.equal(result.publicationReceipt, null);
  assert.deepEqual(calls, ['worker', 'run', 'output']);
  output.action_result = { receipt: 'backend-receipt' };
  assert.equal((await observeSocialMediaWorker(client, ids)).publicationReceipt, output.action_result);
  output.session_id = 'unrelated';
  await assert.rejects(observeSocialMediaWorker(client, ids), /does not belong/);
  run.worker_id = 'unrelated';
  await assert.rejects(observeSocialMediaWorker(client, ids), /mismatched/);
});
