import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { SessionRepository, SessionRepositoriesResponse } from '../git/types'
import { mergeRepositoryRows, repositoryKey, repositoryMutationSupported, repositoryEventInvalidates, SessionRepositoryInventory } from './session-repositories'

// Requirement: canonical inventory is owner/path scoped and retained history must
// survive default changes. Threat: dedup or stale async results target another
// repository. SessionRepositoryInventory is the narrow transport/state boundary.
export const repositoryFixture = (patch: Partial<SessionRepository> = {}): SessionRepository => ({
  id: 'row', session_id: 'parent', workspace_id: 'workspace', workspace_name: 'Project', source_path: '/project', workspace_path: '/project/tree',
  kind: 'parent', attached: true, default: true, branch: 'agent/tree', base_commit: 'base', lifecycle: 'active', retained: true,
  availability: 'available', files_truncated: false,
  status: { workspace_path: '/project/tree', has_git: true, clean: false, dirty_count: 4, staged_count: 1, modified_count: 1, untracked_count: 1, conflict_count: 1,
    ahead_count: 0, behind_count: 0, stash_count: 0, files: [], refreshed_at: '', duration_ms: 0 }, ...patch,
})
const page = (items: SessionRepository[], next_cursor = ''): SessionRepositoriesResponse => ({ ok: true, items, next_cursor, history_coverage: 'retained' })

test('repository identity preserves owner, attachment and path without default retargeting', async () => {
  const first = repositoryFixture()
  const worker = repositoryFixture({ id: 'worker-row', session_id: 'worker', lifecycle: 'failed', default: false })
  assert.equal(mergeRepositoryRows([first], [first, worker]).length, 2)
  let rows = [first, worker]
  const inventory = new SessionRepositoryInventory(async () => page(rows))
  await inventory.refresh()
  inventory.select(repositoryKey(worker))
  rows = [{ ...first, default: false }, { ...worker, default: true, lifecycle: 'integrated' }]
  await inventory.refresh()
  assert.equal(inventory.state.selectedKey, repositoryKey(worker))
  assert.equal(inventory.state.items[1].lifecycle, 'integrated')
  rows = [first]
  await inventory.refresh()
  assert.equal(inventory.state.selectedKey, repositoryKey(worker))
})

test('refresh cancels stale pages, rebuilds opaque cursors and retains loaded history', async () => {
  const calls: string[] = []
  let revision = 'old'
  const inventory = new SessionRepositoryInventory(async cursor => {
    calls.push(cursor)
    return cursor ? page([repositoryFixture({ id: 'history', lifecycle: revision })]) : page([repositoryFixture()], revision)
  })
  await inventory.refresh(); await inventory.loadMore()
  inventory.select(repositoryKey(inventory.state.items[1]))
  revision = 'new'
  inventory.invalidate(); await inventory.refresh()
  assert.deepEqual(calls, ['', 'old', '', 'new'])
  assert.equal(inventory.state.items[1].lifecycle, 'new')
  assert.equal(inventory.state.selectedKey, repositoryKey(inventory.state.items[1]))

  const pending: Array<(value: SessionRepositoriesResponse) => void> = []
  const signals: AbortSignal[] = []
  const racing = new SessionRepositoryInventory((_cursor, signal) => { signals.push(signal); return new Promise(resolve => pending.push(resolve)) })
  const old = racing.refresh(); const fresh = racing.refresh()
  assert.equal(signals[0].aborted, true)
  pending[1](page([repositoryFixture({ id: 'fresh' })])); await fresh
  pending[0](page([repositoryFixture({ id: 'old' })])); await old
  assert.equal(racing.state.items[0].id, 'fresh')
})

test('failure and concurrent live invalidation preserve rows but disable operations', async () => {
  let fail = false
  const inventory = new SessionRepositoryInventory(async () => { if (fail) throw new Error('403 unauthorized'); return page([repositoryFixture()]) })
  await inventory.refresh(); fail = true; await inventory.refresh()
  assert.equal(inventory.state.items.length, 1)
  assert.equal(inventory.state.stale, true)
  assert.match(inventory.state.error, /403/)
  const owner = { id: 'parent', path: '/project/tree', worktree: true }
  assert.equal(repositoryMutationSupported(repositoryFixture(), owner, false), true)
  for (const patch of [{ session_id: 'worker' }, { workspace_path: '/project' }, { kind: 'lane' }, { availability: 'unavailable' }, { files_truncated: true }]) {
    assert.equal(repositoryMutationSupported(repositoryFixture(patch), owner, false), false)
  }
  assert.equal(repositoryMutationSupported(repositoryFixture(), owner, true), false)
  for (const type of ['reconnect.applySnapshot', 'realtime.statusChanged', 'realtime.worksetSessionRemoved', 'mutation.sessionSettingsResult', 'syncStream.applyBatch']) assert.equal(repositoryEventInvalidates(type), true)
  assert.equal(repositoryEventInvalidates('session.select'), false)
})

// Requirement: live changes during an in-flight read cannot certify fresh state;
// non-advancing cursors cannot loop or erase retained rows. State-layer race proof.
test('in-flight invalidation and cursor loops fail closed', async () => {
  let resolve!: (page: SessionRepositoriesResponse) => void
  const inventory = new SessionRepositoryInventory(() => new Promise(done => { resolve = done }))
  const request = inventory.refresh()
  inventory.invalidate()
  resolve(page([repositoryFixture()]))
  await request
  assert.equal(inventory.state.stale, true)
  assert.equal(inventory.state.items.length, 1)
  const looping = new SessionRepositoryInventory(async () => page([repositoryFixture()], 'same'))
  await looping.refresh()
  await looping.loadMore()
  assert.equal(looping.state.stale, true)
  assert.match(looping.state.error, /did not advance/)
  assert.equal(looping.state.items.length, 1)
})
