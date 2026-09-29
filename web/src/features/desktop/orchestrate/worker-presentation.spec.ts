import test from 'node:test'
import assert from 'node:assert/strict'
import { workerLifecycleLabel, workerRunCategory, workerDetailHref } from './worker-presentation'
import type { WorkerRun } from '../state/desktop-workers-api'

// Requirement: worker enablement is not execution; run categories reflect actual receipts.
// Regression: partial output on a failed/cancelled run must not hide the failure.
// Authority: WorkerRecord.lifecycle_state / WorkerRun.status via worker-presentation.
// Pure presentation functions are the narrowest layer; this is not executor evidence.
test('enabled workers are not labelled running and failure outranks outputs', () => {
  assert.equal(workerLifecycleLabel('active'), 'Enabled')
  const run: WorkerRun = { id: 'run_fixture', account_scope_id: 'account_fixture', worker_id: 'worker_fixture', worker_revision: 1, request_source: 'schedule', created_at: 1, status: 'failed', deliverables: [{ label: 'Partial output' }] }
  assert.equal(workerRunCategory(run), 'Failed')
  assert.equal(workerRunCategory({ ...run, status: 'cancelled' }), 'Cancelled')
  assert.equal(workerRunCategory({ ...run, status: 'admitted' }), 'In progress')
  assert.equal(workerRunCategory({ ...run, status: 'running' }), 'In progress')
  assert.equal(workerRunCategory({ ...run, status: 'succeeded' }), 'Deliverables')
  assert.equal(workerRunCategory({ ...run, status: 'succeeded', deliverables: [] }), 'Succeeded')
  assert.equal(workerDetailHref('worker_fixture', 'workspace name'), '/workspace%20name/workers/worker_fixture')
})
