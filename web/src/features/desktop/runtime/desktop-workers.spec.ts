import assert from 'node:assert/strict'
import test from 'node:test'
import { DesktopWorkersRuntime } from './desktop-workers'
import { workerPageKey, reduceWorkerPages, type WorkerPages } from '../state/desktop-workers-state'
import type { WorkerReadResult, WorkerMutationResult, WorkerRecord } from '../state/desktop-workers-api'

// Requirement: workers are account-owned durable resources, not session-derived projections.
// Threat: a stale, foreign, or out-of-order response overwrites worker state after an event,
// account switch or remount. DesktopWorkersRuntime + canonical worker page reducer are the
// narrowest layer proving request ownership and event-driven repair without a live daemon.
const worker = (revision: number, lifecycle_state: WorkerRecord['lifecycle_state'] = 'active'): WorkerRecord => ({
  id: 'worker-1', account_scope_id: 'account-1', name: 'Specialist', instructions: 'Work',
  lifecycle_state, revision, created_at: 1, updated_at: revision,
})
function harness() {
  let pages: WorkerPages = {}
  let account = 'account-1'
  const reads: Array<{ resolve: (value: WorkerReadResult) => void; reject: (reason: Error) => void }> = []
  let mutate: () => Promise<WorkerMutationResult> = async () => ({ worker: worker(2, 'paused') })
  const runtime = new DesktopWorkersRuntime({
    pages: () => pages,
    dispatch: action => { pages = reduceWorkerPages(pages, action) },
    account: () => account,
    read: () => new Promise<WorkerReadResult>((resolve, reject) => reads.push({ resolve, reject })),
    mutate: () => mutate(),
  })
  return { runtime, reads, pages: () => pages, setAccount: (id: string) => { account = id }, setMutate: (fn: () => Promise<WorkerMutationResult>) => { mutate = fn } }
}
const detail = { kind: 'detail' as const, workerId: 'worker-1', accountScopeId: 'account-1' }
const list = { kind: 'list' as const, accountScopeId: 'account-1', limit: 20 }

test('worker event burst repairs after in-flight request; unrelated frames do not fetch', { timeout: 1000 }, async () => {
  const h = harness()
  const lease = h.runtime.acquire(detail)
  await Promise.resolve()
  assert.equal(h.reads.length, 1)
  h.runtime.acceptFrame({ kind: 'event', worker_id: 'worker-1' })
  h.runtime.acceptFrame({ kind: 'automation.updated' })
  h.runtime.acceptFrame({ kind: 'worker.updated', worker_id: 'other', account_scope_id: 'account-1' })
  assert.equal(h.reads.length, 1)
  h.runtime.acceptFrame({ kind: 'worker.updated', account_scope_id: 'account-1' })
  h.runtime.acceptFrame({ kind: 'worker.updated', account_scope_id: 'account-1' })
  h.reads[0].resolve({ worker: worker(1) })
  await lease.ready
  assert.equal(h.reads.length, 2)
  assert.equal(h.pages()[workerPageKey(detail)].data, undefined, 'obsolete result must be discarded')
  h.reads[1].resolve({ worker: worker(2) })
  await h.runtime.refresh(detail)
  assert.equal((h.pages()[workerPageKey(detail)].data as { worker: WorkerRecord }).worker.revision, 2)
  assert.equal(h.pages()[workerPageKey(detail)].stale, false)
  lease.release()
})

test('failed stop never manufactures paused state, and failed read retains stale truth', { timeout: 1000 }, async () => {
  const h = harness()
  const lease = h.runtime.acquire(detail)
  await Promise.resolve()
  h.reads[0].resolve({ worker: worker(1) })
  await lease.ready
  h.setMutate(async () => { throw new Error('stop not acknowledged') })
  await assert.rejects(h.runtime.mutate({ action: 'pause', workerId: 'worker-1', expected_revision: 1 }, 'account-1'), /stop not acknowledged/)
  const key = workerPageKey(detail)
  assert.equal((h.pages()[key].data as { worker: WorkerRecord }).worker.lifecycle_state, 'active')
  assert.equal(h.pages()[key].mutationError, 'stop not acknowledged')
  assert.equal(h.pages()[key].stale, true)
  await Promise.resolve()
  h.reads[1].reject(new Error('read unavailable'))
  await h.runtime.refresh(detail)
  assert.equal((h.pages()[key].data as { worker: WorkerRecord }).worker.lifecycle_state, 'active')
  assert.equal(h.pages()[key].error, 'read unavailable')
  assert.equal(h.pages()[key].stale, true)
  lease.release()
})

test('reconnect repair and account mismatch reject foreign reads without losing loaded data', { timeout: 1000 }, async () => {
  const h = harness()
  const lease = h.runtime.acquire(list)
  await Promise.resolve()
  h.reads[0].resolve({ workers: [worker(1)], next_cursor: 'page-2' })
  await lease.ready
  h.runtime.acceptFrame({ kind: 'worker.updated', account_scope_id: 'other' })
  assert.equal(h.reads.length, 1)
  h.runtime.acceptFrame({ kind: 'cursor.error' })
  await Promise.resolve()
  assert.equal(h.reads.length, 2)
  h.reads[1].resolve({ workers: [{ ...worker(2), account_scope_id: 'other' }] })
  await h.runtime.refresh(list)
  assert.equal(h.pages()[workerPageKey(list)].error, 'Worker response scope mismatch')
  assert.equal((h.pages()[workerPageKey(list)].data as { workers: WorkerRecord[] }).workers[0].revision, 1)
  const pending = h.runtime.refresh(list)
  await Promise.resolve()
  h.setAccount('other')
  h.reads[2].resolve({ workers: [worker(3)] })
  await pending
  assert.equal((h.pages()[workerPageKey(list)].data as { workers: WorkerRecord[] }).workers[0].revision, 1)
  assert.equal(h.pages()[workerPageKey(list)].stale, true)
  lease.release()
})

test('confirmed mutation invalidates list and detail only after acknowledgement', { timeout: 1000 }, async () => {
  const h = harness()
  const listLease = h.runtime.acquire(list)
  const detailLease = h.runtime.acquire(detail)
  await Promise.resolve()
  h.reads[0].resolve({ workers: [worker(1)] })
  h.reads[1].resolve({ worker: worker(1) })
  await Promise.all([listLease.ready, detailLease.ready])
  h.setMutate(async () => ({ worker: worker(2, 'paused') }))
  await h.runtime.mutate({ action: 'pause', workerId: 'worker-1', expected_revision: 1 }, 'account-1')
  assert.equal(h.pages()[workerPageKey(list)].stale, true)
  assert.equal(h.pages()[workerPageKey(detail)].stale, true)
  assert.equal((h.pages()[workerPageKey(detail)].data as { worker: WorkerRecord }).worker.lifecycle_state, 'active', 'read repair owns committed state')
  await Promise.resolve()
  assert.equal(h.reads.length, 4)
  h.reads[2].resolve({ workers: [worker(2, 'paused')] })
  h.reads[3].resolve({ worker: worker(2, 'paused') })
  await Promise.all([h.runtime.refresh(list), h.runtime.refresh(detail)])
  assert.equal((h.pages()[workerPageKey(detail)].data as { worker: WorkerRecord }).worker.lifecycle_state, 'paused')
  listLease.release()
  detailLease.release()
})

// Requirement: an account-scoped summary is independent of paged history and must
// reject a wrong worker/date/zone rather than rendering foreign or stale counts.
// Boundary: DesktopWorkersRuntime.verify and the canonical worker page reducer.
test('summary accepts exact scope and rejects foreign worker or day', { timeout: 1000 }, async () => {
  const h = harness()
  const input = { kind: 'summary' as const, accountScopeId: 'account-1', workerId: 'worker-1', date: '2025-03-09', timezone: 'UTC' }
  const lease = h.runtime.acquire(input)
  await Promise.resolve()
  const runs = { active: [], active_truncated: false, date: input.date, timezone: input.timezone, day_start_at: 0, day_end_at: 1, daily_runs: 17, daily_success: 16, daily_failed: 1, daily_cancelled: 0, active_runs: 2, scanned_runs: 17, truncated: false }
  h.reads[0].resolve({ worker_id: 'worker-2', runs, next_scheduled_at: 0 })
  await lease.ready
  assert.equal(h.pages()[workerPageKey(input)].error, 'Worker response kind mismatch')
  const pending = h.runtime.refresh(input)
  await Promise.resolve()
  h.reads[1].resolve({ worker_id: 'worker-1', runs, next_scheduled_at: 0 })
  await pending
  assert.equal(h.pages()[workerPageKey(input)].error, undefined)
  assert.equal((h.pages()[workerPageKey(input)].data as { runs: typeof runs }).runs.daily_runs, 17)
  lease.release()
})

test('released response cannot overwrite remounted page with matching generation', { timeout: 1000 }, async () => {
  const h = harness()
  const first = h.runtime.acquire(detail)
  await Promise.resolve()
  first.release()
  const next = h.runtime.acquire(detail)
  await Promise.resolve()
  h.reads[0].resolve({ worker: worker(1) })
  await first.ready
  assert.equal(h.pages()[workerPageKey(detail)].data, undefined)
  h.reads[1].resolve({ worker: worker(2) })
  await next.ready
  assert.equal((h.pages()[workerPageKey(detail)].data as { worker: WorkerRecord }).worker.revision, 2)
  next.release()
})

// Requirement: worker accept mutation enforces account isolation, invalidates cache upon success,
// and records mutationError on stale revision conflict without forging active state.
test('worker accept mutation requires matching account and invalidates on confirmed transition', { timeout: 1000 }, async () => {
  const h = harness()
  const detailLease = h.runtime.acquire(detail)
  await Promise.resolve()
  h.reads[0].resolve({ worker: worker(1, 'pending') })
  await detailLease.ready
  assert.equal((h.pages()[workerPageKey(detail)].data as { worker: WorkerRecord }).worker.lifecycle_state, 'pending')

  // Threat: foreign account attempts acceptance
  await assert.rejects(
    h.runtime.mutate({ action: 'accept', workerId: 'worker-1', expected_revision: 1 }, 'other-account'),
    /Worker account scope changed/
  )

  // Success: confirmed acceptance transitions to active
  h.setMutate(async () => ({ worker: worker(2, 'active') }))
  const res = await h.runtime.mutate({ action: 'accept', workerId: 'worker-1', expected_revision: 1 }, 'account-1')
  assert.ok('worker' in res)
  assert.equal(res.worker.lifecycle_state, 'active')
  assert.equal(h.pages()[workerPageKey(detail)].stale, true, 'detail cache must be invalidated')

  // Threat: stale revision conflict records error and does not forge active state
  h.setMutate(async () => { throw new Error('stale revision: expected 1, current is 2') })
  await assert.rejects(
    h.runtime.mutate({ action: 'accept', workerId: 'worker-1', expected_revision: 1 }, 'account-1'),
    /stale revision/
  )
  assert.equal(h.pages()[workerPageKey(detail)].mutationError, 'stale revision: expected 1, current is 2')
  detailLease.release()
})

// Requirement: nested pending candidates cannot cross account or stable identity.
// Threat: a valid outer worker wraps foreign review content. Runtime verification
// is the narrowest boundary before canonical cache admission; rejection must leave
// approved data intact and explicitly stale, not admit the nested record.
test('foreign pending review is rejected without replacing approved data', { timeout: 1000 }, async () => {
  const h = harness()
  const lease = h.runtime.acquire(detail)
  await Promise.resolve()
  h.reads[0].resolve({ worker: worker(1) })
  await lease.ready
  const refresh = h.runtime.refresh(detail)
  await Promise.resolve()
  h.reads[1].resolve({ worker: { ...worker(2), pending_review: { ...worker(2), account_scope_id: 'foreign' } } })
  await refresh
  const page = h.pages()[workerPageKey(detail)]
  assert.match(page.error || '', /scope mismatch/)
  assert.equal((page.data as { worker: WorkerRecord }).worker.revision, 1)
  assert.equal(page.stale, true)
  lease.release()
})
