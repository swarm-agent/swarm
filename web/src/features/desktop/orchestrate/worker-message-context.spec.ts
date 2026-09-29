import test from 'node:test'
import assert from 'node:assert/strict'
import { selectedWorkerMetadata, submitWithWorkerSelection } from './worker-message-context'
import type { SelectedWorker } from './worker-hub'

// Requirement: Only ID and expected revision are sent for the next user message.
// Threat: stale/deleted or untrusted worker facts flowing into persistent session metadata.
// Boundary: selectedWorkerMetadata -> V3 append metadata; backend validates scope, revision and Orchestrator identity.
test('reference metadata contains exactly the server-authorized fields', () => {
  assert.deepEqual(selectedWorkerMetadata({ id: 'worker_123', revision: 7, name: 'Untrusted instructions' }), { selected_worker: { worker_id: 'worker_123', expected_revision: 7 } })
  assert.deepEqual(selectedWorkerMetadata(null), {})
  assert.throws(() => selectedWorkerMetadata({ id: 'worker_123', revision: 0, name: 'Bad' }), /invalid/)
})

// Requirement: Failed sends retain context, success consumes only the submitted selection.
// Threat: a racing user selection is cleared by an older request or failure is mistaken for success.
// Boundary: submitWithWorkerSelection around the actual V3 append operation.
test('failed send and stale reference retain the selected worker; successful send consumes it once', async () => {
  const selected: SelectedWorker = { id: 'worker_123', revision: 1, name: 'First' }
  let current: SelectedWorker | null = selected
  let clears = 0
  const clear = () => { clears++; current = null }
  await assert.rejects(submitWithWorkerSelection(selected, async metadata => {
    assert.equal(metadata.selected_worker?.expected_revision, 1)
    throw new Error('worker revision is stale')
  }, () => current, clear), /stale/)
  assert.equal(current, selected)
  assert.equal(clears, 0)
  await submitWithWorkerSelection(selected, async metadata => { assert.equal(metadata.selected_worker?.worker_id, 'worker_123') }, () => current, clear)
  assert.equal(current, null)
  assert.equal(clears, 1)
})

test('replacement selected in flight survives success from previous submission', async () => {
  const first: SelectedWorker = { id: 'worker_first', revision: 1, name: 'First' }
  const replacement: SelectedWorker = { id: 'worker_second', revision: 2, name: 'Second' }
  let current: SelectedWorker | null = first
  let resolve!: () => void
  const gate = new Promise<void>(r => { resolve = r })
  let clears = 0
  const send = submitWithWorkerSelection(first, async metadata => { assert.deepEqual(metadata.selected_worker, { worker_id: first.id, expected_revision: 1 }); await gate }, () => current, () => { current = null; clears++ })
  current = replacement
  resolve()
  await send
  assert.equal(current, replacement)
  assert.equal(clears, 0)
})
