import assert from 'node:assert/strict'
import { test } from 'node:test'
import type { SessionRepository, SessionRepositoriesResponse } from '../git/types'
import { createEmptyDesktopV3CacheState } from './desktop-v3-cache-reducer'
import { repositoryDialogTargetMatches, repositoryOwnerIds, mergeRepositoryRows, repositoryKey, repositoryMutationSupported, repositoryEventInvalidates, scheduleRepositoryRefresh, SessionRepositoryInventory } from './session-repositories'

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

test('refresh cancels stale pages and restarts without replaying history or retargeting', async () => {
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
  assert.deepEqual(calls, ['', 'old', ''])
  assert.equal(inventory.state.items.length, 1)
  assert.equal(inventory.state.selectedKey, repositoryKey(repositoryFixture({ id: 'history' })))
  await inventory.loadMore()
  assert.equal(inventory.state.items[1].lifecycle, 'new')

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
  fail = false; await inventory.refresh()
  assert.equal(inventory.state.stale, false)
  assert.equal(inventory.state.error, '')
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

// Requirement: every retained worker is reachable without unbounded memory or
// automatic fanout. Inventory state is the narrow owner/selection boundary.
test('continued windows reach 460 workers and keep exact unloaded selection', async () => {
  let calls = 0
  const inventory = new SessionRepositoryInventory(async cursor => {
    calls++
    const offset = Number(cursor || 0)
    return page(Array.from({ length: 20 }, (_, i) => repositoryFixture({ id: `worker-${offset + i}`, session_id: `owner-${offset + i}`, workspace_path: `/tree/${offset + i}` })), offset < 440 ? String(offset + 20) : '')
  })
  await inventory.refresh()
  const selected = inventory.state.selectedKey
  for (let i = 1; i < 23; i++) {
    await inventory.loadMore()
    assert.equal(calls, i + 1)
    assert.ok(inventory.state.items.length <= 200)
    assert.equal(inventory.state.selectedKey, selected)
  }
  assert.equal(inventory.state.items[59].id, 'worker-459')
  assert.equal(inventory.state.items.some(row => repositoryKey(row) === selected), false)
  assert.equal(inventory.state.nextCursor, '')
  await inventory.refresh()
  assert.equal(calls, 24)
  assert.equal(inventory.state.items.length, 20)
  assert.equal(inventory.state.selectedKey, selected)
})

// Requirement: only relevant durable changes trigger reads; chatter and unrelated
// sessions cannot keep workspace/Git controls stale. Test the action filter directly.
test('repository invalidation is scoped and ignores streaming chatter', () => {
  const owners = new Set(['parent'])
  const action = (sessionId: string, eventType: string) => ({ type: 'realtime.applyEvent' as const, event: { source: 'realtime' as const, sessionId, eventType, payload: {} } })
  for (const type of ['session.message.appended', 'run.usage.updated', 'session.tool.started']) {
    assert.equal(repositoryEventInvalidates(action('parent', type), owners), false)
  }
  assert.equal(repositoryEventInvalidates(action('other', 'session.tool.completed'), owners), false)
  assert.equal(repositoryEventInvalidates(action('parent', 'session.tool.completed'), owners), true)
  assert.equal(repositoryEventInvalidates(action('parent', 'session.tool.completed'), owners, true), false)
  assert.equal(repositoryEventInvalidates(action('parent', 'session.settings.updated'), owners, true), true)
  assert.equal(repositoryEventInvalidates({ type: 'realtime.statusChanged', status: 'open' }, owners), false)
})

// Requirement: completion, not a timer, drains one queued invalidation. The real
// inventory proves no aborted requests, idle polling, or lost in-flight updates.
test('refresh scheduler drains events without polling and defers hidden reads', async t => {
  t.mock.timers.enable({ apis: ['setTimeout', 'setInterval'] })
  let reads = 0; let visible = true
  const pending: Array<(value: SessionRepositoriesResponse) => void> = []
  const inventory = new SessionRepositoryInventory(() => { reads++; return new Promise(resolve => pending.push(resolve)) })
  const scheduler = scheduleRepositoryRefresh(inventory, () => visible)
  for (let i = 0; i < 100; i++) scheduler.invalidate()
  await Promise.resolve()
  assert.equal(reads, 1)
  scheduler.invalidate()
  t.mock.timers.tick(60_000)
  assert.equal(reads, 1)
  pending.shift()!(page([repositoryFixture()]))
  await Promise.resolve(); await Promise.resolve()
  assert.equal(reads, 2)
  pending.shift()!(page([repositoryFixture()]))
  await Promise.resolve(); await Promise.resolve()
  assert.equal(inventory.state.stale, false)
  t.mock.timers.tick(60_000)
  assert.equal(reads, 2)
  visible = false; scheduler.visibilityChanged(); scheduler.invalidate()
  await Promise.resolve()
  assert.equal(reads, 2)
  visible = true; scheduler.visibilityChanged()
  await Promise.resolve()
  assert.equal(reads, 3)
  scheduler.dispose()
  pending.shift()!(page([repositoryFixture({ id: 'late' })]))
  await Promise.resolve()
  assert.equal(inventory.state.items[0].id, 'row')
})

// Requirement: hydration of a previously unseen child must invalidate its parent's
// paged inventory; unrelated children and token chatter must not trigger reads.
// Canonical cache lineage + the event filter is the narrow discovery boundary.
test('new child hydration discovers inventory owners outside the loaded page', () => {
  const state = createEmptyDesktopV3CacheState()
  state.sessionsById.child = { kind: 'full', needsHydrate: false, session: { id: 'child', metadata: { parent_session_id: 'parent' } } as never }
  state.sessionsById.other = { kind: 'full', needsHydrate: false, session: { id: 'other', metadata: { parent_session_id: 'elsewhere' } } as never }
  const owners = repositoryOwnerIds('parent', [], state)
  assert.deepEqual([...owners], ['parent', 'child'])
  assert.equal(repositoryEventInvalidates({ type: 'hydrate.apply', source: 'hydrate', scopeId: 'scope', requestedSessionIds: ['child'], snapshot: {} as never }, owners), true)
  assert.equal(repositoryEventInvalidates({ type: 'hydrate.apply', source: 'hydrate', scopeId: 'scope', requestedSessionIds: ['other'], snapshot: {} as never }, owners), false)
  assert.equal(repositoryEventInvalidates({ type: 'realtime.applyEvent', event: { source: 'realtime', sessionId: 'child', eventType: 'session.message.appended', payload: {} } }, owners), false)
})

// Requirement: historical or mismatched status bytes cannot authorize an operation,
// even when owner/path/branch labels match. Test the actual mutation predicate.
test('inactive lane and foreign status cannot enable mutations', () => {
  const owner = { id: 'parent', path: '/project/tree', worktree: true }
  assert.equal(repositoryMutationSupported(repositoryFixture({ active: false }), owner, false), false)
  const row = repositoryFixture({ active: true })
  row.status = { ...row.status!, workspace_path: '/other' }
  assert.equal(repositoryMutationSupported(row, owner, false), false)
})

// Requirement: persisted program lane allocation is visible while task execution
// remains in flight, without refreshing on every progress/token delta.
test('only scoped repository allocation progress invalidates inventory', () => {
  const owners = new Set(['parent'])
  const action = (sessionId: string, output: string) => ({ type: 'realtime.applyEvent' as const, event: { source: 'realtime' as const, sessionId, eventType: 'session.tool.delta', payload: { output } } })
  const allocated = JSON.stringify({ tool: 'task', phase: 'repository.allocated' })
  assert.equal(repositoryEventInvalidates(action('parent', allocated), owners), true)
  assert.equal(repositoryEventInvalidates(action('other', allocated), owners), false)
  assert.equal(repositoryEventInvalidates(action('parent', allocated), owners, true), false)
  for (const output of ['partial{', JSON.stringify({ tool: 'task', phase: 'running' }), JSON.stringify({ tool: 'bash', phase: 'repository.allocated' })]) {
    assert.equal(repositoryEventInvalidates(action('parent', output), owners), false)
  }
})

// Requirement: switching sessions rejects late old responses; reload and reconnect
// rebuild selection from the new inventory instead of carrying another owner's key.
test('session switch and reconnect keep independent inventory identities', async () => {
  let finish!: (value: SessionRepositoriesResponse) => void
  const old = new SessionRepositoryInventory(() => new Promise(resolve => { finish = resolve }))
  const pending = old.refresh(); old.dispose()
  const current = new SessionRepositoryInventory(async () => page([repositoryFixture({ id: 'new', session_id: 'new-owner' })]))
  await current.refresh()
  finish(page([repositoryFixture()])); await pending
  assert.equal(old.state.items.length, 0)
  const selected = current.state.selectedKey
  assert.ok(selected.includes('new-owner'))
  assert.equal(repositoryEventInvalidates({ type: 'reconnect.applySnapshot', snapshot: {} as never }, new Set(['new-owner'])), true)
  current.invalidate(); await current.refresh()
  assert.equal(current.state.selectedKey, selected)
})

// Requirement: an open commit dialog cannot outlive selection/authority changes.
// The predicate used by handleGitCommit must reject before the network mutation.
test('commit dialog target is exact and requires current authority', () => {
  const target = { sessionId: 'parent', workspacePath: '/old' }
  assert.equal(repositoryDialogTargetMatches(target, target, true), true)
  assert.equal(repositoryDialogTargetMatches(target, target, false), false)
  assert.equal(repositoryDialogTargetMatches(target, { ...target, workspacePath: '/successor' }, true), false)
  assert.equal(repositoryDialogTargetMatches(target, { ...target, sessionId: 'other' }, true), false)
})

// Requirement: recovery publication repairs both Git inventory and attachment
// consumers, but cannot refresh another session or interpret token payloads as
// authority. Exercise the shared filter used by runtime subscribers and replay.
test('recovery publication invalidates scoped inventory and attachments on live and repair paths', () => {
  const owners = new Set(['parent'])
  for (const eventType of ['session.worktree.reclaimed', 'session.worktree.copied', 'session.worktree.recovery.publish', 'session.worktree.recovery.publish_copy']) {
    const event = { source: 'realtime' as const, sessionId: 'parent', eventType, payload: {} }
    for (const attachmentsOnly of [false, true]) {
      assert.equal(repositoryEventInvalidates({ type: 'realtime.applyEvent', event }, owners, attachmentsOnly), true)
      assert.equal(repositoryEventInvalidates({ type: 'realtime.applyEvent', event: { ...event, sessionId: 'other' } }, owners, attachmentsOnly), false)
      assert.equal(repositoryEventInvalidates({ type: 'liveRun.mergeRepairEvents', sessionId: 'parent', events: [event] } as never, owners, attachmentsOnly), true)
    }
  }
  for (const eventType of ['session.worktree.recovery.reserve', 'session.worktree.recovery.reserve_copy', 'session.message.appended']) {
    assert.equal(repositoryEventInvalidates({ type: 'realtime.applyEvent', event: { source: 'realtime', sessionId: 'parent', eventType, payload: {} } }, owners, true), false)
  }
})

// Requirement: initial/reconnect inventory selects the actual active root even
// when history sorts first with duplicate branch labels. Subsequent recovery must
// preserve explicit inspection (including missing rows), never silently adopt.
test('recovered active root wins initial selection but never retargets inspection', async () => {
  const history = repositoryFixture({ id: 'old', active: false, default: true })
  const current = repositoryFixture({ id: 'recovered', workspace_path: '/project/recovered', active: true, default: false })
  const extra = repositoryFixture({ id: 'extra', kind: 'source', workspace_id: 'extra', source_path: '/extra', workspace_path: '/extra' })
  const terminal = repositoryFixture({ id: 'program', kind: 'lane', workspace_path: '/project/program', lifecycle: 'completed', active: false })
  let rows = [history, extra, terminal, current]
  const inventory = new SessionRepositoryInventory(async () => page(rows))
  await inventory.refresh()
  assert.equal(inventory.state.selectedKey, repositoryKey(current))
  assert.equal(inventory.state.items.length, 4)
  inventory.select(repositoryKey(history))
  const copied = repositoryFixture({ id: 'copy', workspace_path: '/project/copy', active: true })
  rows = [history, extra, terminal, { ...current, active: false }, copied]
  inventory.invalidate()
  assert.equal(inventory.state.selectedKey, repositoryKey(history))
  await inventory.refresh()
  assert.equal(inventory.state.selectedKey, repositoryKey(history))
  assert.equal(inventory.state.items.find(row => row.id === 'old')?.status?.dirty_count, 4)
  rows = [copied]
  await inventory.refresh()
  assert.equal(inventory.state.selectedKey, repositoryKey(history))
  assert.equal(inventory.state.items.some(row => repositoryKey(row) === inventory.state.selectedKey), false)
  inventory.select('unknown')
  assert.equal(inventory.state.selectedKey, repositoryKey(history))
})
