import assert from 'node:assert/strict'
import test from 'node:test'
import { DesktopProjectsRuntime } from './desktop-projects'
import { mapBackendTask, reduceDesktopProjectsState, type DesktopProjectsState } from '../state/desktop-projects-state'
import { taskCardFacts } from '../orchestrate/task-card-summary'
import { createEmptyDesktopV3CacheState } from '../state/desktop-v3-cache-reducer'
import type { DesktopV3CacheMutation } from '../state/desktop-v3-cache-store'

const flush = async () => { for (let i = 0; i < 24; i++) await Promise.resolve() }
const record = (id: string) => ({ id, title: id, agent: 'coder', session_id: id, revision: 1,
  status: 'needs_review', worktree_branch: `agent/${id}`, base_branch: 'dev', base_commit: 'fork',
  source_workspace: { workspace_id: 'workspace', workspace_generation: 1, path: '/fixture/repo' },
  git_status: 'stale', is_integrated: false, unintegrated_commits: 0 })
function harness() {
  let state: DesktopProjectsState = {}
  let tasks = [record('integrated'), record('pending')]
  let collections = 0
  const publications: DesktopProjectsState[] = []
  const reads: Array<{ id: string; resolve: (value: { task: any }) => void; reject: (error: Error) => void }> = []
  const lists: Array<(value: { tasks: any[] }) => void> = []
  let holdList = false
  const runtime = new DesktopProjectsRuntime({
    getState: () => state,
    dispatch: action => { state = reduceDesktopProjectsState(state, action); publications.push(state) },
    subscribe: () => () => {},
    fetchTasks: async () => { collections++; return holdList ? new Promise(resolve => lists.push(resolve)) : { tasks } },
    fetchMedia: async () => ({ media: [] }),
    fetchTask: (_project, id) => new Promise((resolve, reject) => reads.push({ id, resolve, reject })),
  })
  const emit = (sessionId: string, eventType: string) => {
    const cache = createEmptyDesktopV3CacheState()
    runtime.acceptSessionMutation({ action: { type: 'realtime.applyEvent', event: {
      source: 'realtime', sessionId, eventType, payload: {},
    } }, previousState: cache, nextState: cache, durationMS: 0 } as DesktopV3CacheMutation)
  }
  const update = (id: string) => runtime.acceptFrame({ kind: 'project.updated', project_id: 'project',
    event: { payload: { action: 'task_updated', task_id: id } } })
  return { runtime, reads, lists, publications, emit, update, get state() { return state },
    get tasks() { return tasks }, set tasks(value) { tasks = value },
    get collections() { return collections }, set holdList(value: boolean) { holdList = value } }
}
// Initialize inspected facts through explicit detail disclosure, not acquisition.
async function hydrate(h: ReturnType<typeof harness>) {
  const lease = h.runtime.acquire('project')
  await lease.ready
  assert.equal(h.reads.length, 0, 'entry is collection-only')
  for (const task of h.tasks) h.runtime.inspectTask('project', task.id)
  h.reads[0].resolve({ task: { ...h.tasks[0], git_status: 'clean', is_integrated: true, status: 'completed' } })
  h.reads[1].resolve({ task: { ...h.tasks[1], git_status: 'diverged', unintegrated_commits: 3 } })
  await flush()
  return lease
}

// Purpose: collection GET is non-inspecting; DesktopProjectsRuntime and the
// canonical Projects reducer must retain inspected detail facts across identical
// list/membership refreshes. Recording every publication proves there is no
// transient label/status/action-input regression, not merely a final-state fix.
test('collection and membership refreshes retain inspected labels without all-card Git fanout', { timeout: 5000 }, async () => {
  const h = harness()
  const lease = await hydrate(h)
  const start = h.publications.length
  h.runtime.acceptFrame({ kind: 'project.updated', project_id: 'project' })
  await flush()
  assert.equal(h.collections, 2)
  assert.equal(h.reads.length, 2)
  for (const state of h.publications.slice(start)) {
    assert.deepEqual(state.project.tasks.map(task => task.id), ['integrated', 'pending'])
    assert.equal(taskCardFacts(state.project.tasks[0]).git, 'Integrated')
    assert.equal(state.project.tasks[0].status, 'completed')
    assert.equal(taskCardFacts(state.project.tasks[1]).git, '3 unintegrated commits')
    assert.equal(state.project.tasks[1].status, 'needs_review')
  }
  h.tasks = [...h.tasks, record('new')]
  h.runtime.acceptFrame({ kind: 'project.updated', project_id: 'project', event: { payload: { action: 'task_created' } } })
  await flush()
  assert.equal(h.collections, 3)
  assert.equal(h.reads.length, 2, 'membership alone must not inspect new cards')
  h.runtime.inspectTask('project', 'new')
  assert.deepEqual(h.reads.map(read => read.id), ['integrated', 'pending', 'new'])
  h.reads[2].resolve({ task: { ...h.tasks[2], git_status: 'dirty', is_dirty: true, dirty_count: 2 } })
  await flush()
  assert.equal(taskCardFacts(h.state.project.tasks[2]).git, 'Changes pending commit')
  const secondLease = h.runtime.acquire('project') // Drilldown shares the canonical board.
  await secondLease.ready
  assert.equal(h.collections, 3)
  assert.equal(h.reads.length, 3)
  secondLease.release()
  lease.release()
})

// Purpose: the runtime is event-driven. Fake clock advancement plus unrelated,
// token and usage chatter must issue zero reads; genuine Git changes must mark
// freshness honestly, coalesce bursts on completion and reject superseded success.
// Deferred HTTP responses exercise this boundary without a daemon/provider.
test('idle clock and chatter never poll; Git bursts reject old success and revalidate once', { timeout: 5000 }, async t => {
  t.mock.timers.enable({ apis: ['setTimeout', 'setInterval', 'Date'] })
  const h = harness()
  const lease = await hydrate(h)
  t.mock.timers.tick(3_600_000)
  h.emit('foreign', 'session.tool.completed')
  h.emit('integrated', 'session.message.delta')
  h.emit('integrated', 'session.usage.updated')
  await flush()
  assert.equal(h.collections, 1)
  assert.equal(h.reads.length, 2)
  h.emit('pending', 'session.worktree.updated')
  assert.equal(taskCardFacts(h.state.project.tasks[1]).git, 'Git: last known state')
  for (let i = 0; i < 20; i++) h.emit('pending', 'session.worktree.updated')
  assert.equal(h.reads.length, 3)
  h.reads[2].resolve({ task: { ...h.tasks[1], git_status: 'clean', is_integrated: true } })
  await flush()
  assert.equal(h.reads.length, 4)
  assert.equal(h.state.project.tasks[1].gitStatus, 'stale', 'superseded success is never published')
  h.reads[3].resolve({ task: { ...h.tasks[1], git_status: 'diverged', unintegrated_commits: 5 } })
  await flush()
  assert.equal(taskCardFacts(h.state.project.tasks[1]).git, '5 unintegrated commits')
  assert.equal(taskCardFacts(h.state.project.tasks[0]).git, 'Integrated')
  h.emit('pending', 'session.tool.failed')
  h.reads[4].reject(new Error('inspection failed'))
  await flush()
  assert.equal(h.state.project.tasks[1].gitStatus, 'unknown')
  assert.equal(h.state.project.tasks[1].isIntegrated, false)
  assert.equal(h.state.project.tasks[1].syncWarning, 'inspection failed')
  t.mock.timers.tick(3_600_000)
  await flush()
  assert.equal(h.reads.length, 5, 'no failure retry timer')
  assert.equal(h.collections, 1)
  lease.release()
})

// Purpose: task_updated stays card-scoped even during collection reads. A late
// list cannot regress the newer revision; identity replacement cannot inherit
// inspected ancestry. Runtime response guards and reducer provenance are the
// narrowest layer proving no cross-owner reuse or fabricated integration.
test('in-flight list updates stay scoped and execution identity replacement requires inspection', { timeout: 5000 }, async () => {
  const h = harness()
  const lease = await hydrate(h)
  h.holdList = true
  h.runtime.acceptFrame({ kind: 'project.updated', project_id: 'project' })
  h.update('pending')
  assert.equal(h.collections, 2)
  assert.equal(h.reads.length, 3)
  h.reads[2].resolve({ task: { ...h.tasks[1], revision: 2, title: 'Live update', git_status: 'diverged', unintegrated_commits: 4 } })
  await flush()
  h.lists[0]({ tasks: h.tasks })
  await flush()
  assert.equal(h.state.project.tasks[1].revision, 2)
  assert.equal(h.state.project.tasks[1].title, 'Live update')
  assert.equal(h.reads.length, 3)
  h.holdList = false
  h.tasks = [h.tasks[0], { ...h.tasks[1], revision: 3, session_id: 'replacement', base_commit: 'new-fork' }]
  h.runtime.acceptFrame({ kind: 'project.updated', project_id: 'project' })
  await flush()
  assert.equal(h.reads.length, 3, 'replacement remains uninspected until explicitly requested')
  h.runtime.inspectTask('project', 'pending')
  assert.equal(h.reads.length, 4)
  assert.equal(h.reads[3].id, 'pending')
  assert.equal(h.state.project.tasks[1].isIntegrated, false)
  assert.equal(h.state.project.tasks[1].gitStatus, 'stale')
  h.reads[3].resolve({ task: { ...h.tasks[1], git_status: 'unknown' } })
  await flush()
  assert.equal(taskCardFacts(h.state.project.tasks[1]).git, 'Git: not inspected')
  lease.release()
})

// Purpose: explicit refresh and durable gap repair must still inspect active
// cards, not reuse pre-gap success. Assert request counts and visible stale/failed
// verification through the same canonical runtime used by the board.
test('explicit refresh and reconnect invalidate Git observations without feedback loops', { timeout: 5000 }, async () => {
  const h = harness()
  const lease = await hydrate(h)
  await h.runtime.refresh('project')
  assert.equal(h.collections, 2)
  assert.equal(h.reads.length, 4)
  assert.equal(h.state.project.tasks[0].gitStatus, 'stale')
  h.reads[2].resolve({ task: { ...h.tasks[0], git_status: 'unknown' } })
  h.reads[3].resolve({ task: { ...h.tasks[1], git_status: 'diverged', unintegrated_commits: 1 } })
  await flush()
  assert.equal(h.state.project.tasks[0].isIntegrated, false)
  h.runtime.acceptFrame({ kind: 'rehydrate.required' })
  await flush()
  assert.equal(h.collections, 3)
  assert.equal(h.reads.length, 6)
  h.reads[4].resolve({ task: { ...h.tasks[0], git_status: 'clean', is_integrated: true } })
  h.reads[5].resolve({ task: { ...h.tasks[1], git_status: 'clean', is_integrated: false } })
  await flush()
  assert.equal(taskCardFacts(h.state.project.tasks[0]).git, 'Integrated')
  assert.equal(taskCardFacts(h.state.project.tasks[1]).git, 'Integration not verified')
  assert.equal(h.collections, 3)
  assert.equal(h.reads.length, 6)
  lease.release()
})

// Purpose: observation reuse is strictly bounded by execution and repository
// identity, not card ID or workspace cleanliness. Exercise each identity axis
// independently against collection/detail reducer semantics, including failure
// observations which must not be upgraded by a non-inspecting collection.
test('canonical observation provenance rejects owner, attempt, branch, fork and source changes', () => {
  const { tasks: initial } = harness()
  const variants = [
    { session_id: 'different-owner' }, { active_attempt_id: 'different-attempt' },
    { worktree_branch: 'agent/different' }, { base_branch: 'other-target' }, { base_commit: 'different-fork' },
    { source_workspace: { ...initial[0].source_workspace, workspace_generation: 2 } },
    { source_workspace: { ...initial[0].source_workspace, workspace_id: 'different-workspace' } },
    { source_workspace: { ...initial[0].source_workspace, path: '/fixture/other' } },
    { integration: { state: 'in_progress', source_head: 'new-head' } }, { revision: 2 },
  ]
  for (const replacement of variants) {
    let state: DesktopProjectsState = {}
    state = reduceDesktopProjectsState(state, { type: 'projects.beginLoad', projectId: 'project', requestId: 'initial' })
    state = reduceDesktopProjectsState(state, { type: 'projects.loadSuccess', projectId: 'project', requestId: 'initial',
      generation: 0, tasks: [mapBackendTask(initial[0])], media: [] })
    state = reduceDesktopProjectsState(state, { type: 'projects.updateTasks', projectId: 'project', inspectedTaskId: 'integrated',
      tasks: [mapBackendTask({ ...initial[0], git_status: 'clean', is_integrated: true })] })
    state = reduceDesktopProjectsState(state, { type: 'projects.beginLoad', projectId: 'project', requestId: 'replacement' })
    state = reduceDesktopProjectsState(state, { type: 'projects.loadSuccess', projectId: 'project', requestId: 'replacement',
      generation: 0, tasks: [mapBackendTask({ ...initial[0], ...replacement })], media: [] })
    assert.equal(state.project.tasks[0].gitStatus, 'stale', JSON.stringify(replacement))
    assert.equal(state.project.tasks[0].isIntegrated, false)
    assert.equal(state.project.gitObservations?.integrated, undefined)
  }
})

// Purpose: replacing execution identity during an active detail read must queue
// the new owner after completion, never publish the old owner's success, and
// bound concurrent detail reads to four. Deferred promises expose race ordering
// directly at DesktopProjectsRuntime rather than relying on wall-clock timing.
test('identity replacement during inspection rejects old ancestry and bounded queue hydrates the new owner', { timeout: 5000 }, async () => {
  const h = harness()
  h.tasks = Array.from({ length: 7 }, (_, i) => record(`card-${i}`))
  const lease = h.runtime.acquire('project')
  await lease.ready
  assert.equal(h.reads.length, 0)
  for (const task of h.tasks) h.runtime.inspectTask('project', task.id)
  assert.equal(h.reads.length, 4)
  h.tasks = h.tasks.map((task, i) => i === 0 ? { ...task, revision: 2, session_id: 'new-owner' } : task)
  h.runtime.acceptFrame({ kind: 'project.updated', project_id: 'project' })
  await flush()
  assert.equal(h.reads.length, 4)
  h.reads[0].resolve({ task: { ...record('card-0'), git_status: 'clean', is_integrated: true } })
  await flush()
  assert.equal(h.state.project.tasks[0].sessionId, 'new-owner')
  assert.equal(h.state.project.tasks[0].isIntegrated, false)
  assert.equal(h.reads.length, 5)
  for (const read of h.reads.slice(1, 4)) read.resolve({ task: { ...h.tasks.find(task => task.id === read.id), git_status: 'clean' } })
  await flush()
  assert.equal(h.reads.length, 8) // Seven initial owners plus one replacement; never duplicate unaffected owners.
  const replacement = h.reads.slice(4).find(read => read.id === 'card-0')!
  replacement.resolve({ task: { ...h.tasks[0], git_status: 'unknown' } })
  for (const read of h.reads.slice(4).filter(read => read !== replacement)) {
    read.resolve({ task: { ...h.tasks.find(task => task.id === read.id), git_status: 'clean' } })
  }
  await flush()
  assert.equal(h.reads.length, 8)
  assert.equal(h.state.project.tasks[0].isIntegrated, false)
  assert.equal(h.state.project.tasks[0].gitStatus, 'unknown')
  lease.release()
})

// Requirement: card remount hydration is cache enrichment, not a repository change.
// DesktopProjectsRuntime owns explicit Git reads and durable invalidations; exercising
// its mutation boundary proves filter-driven card mounts cannot fan out detail GETs.
test('card hydration does not invalidate inspected project Git', { timeout: 5000 }, async () => {
  const h = harness()
  const lease = await hydrate(h)
  const before = h.state.project.tasks
  const cache = createEmptyDesktopV3CacheState()
  for (const ids of [['integrated'], ['pending'], ['foreign'], ['integrated', 'pending']]) {
    h.runtime.acceptSessionMutation({ action: { type: 'hydrate.apply', requestedSessionIds: ids },
      previousState: cache, nextState: cache, durationMS: 0 } as DesktopV3CacheMutation)
  }
  await flush()
  assert.equal(h.reads.length, 2)
  assert.equal(h.collections, 1)
  assert.equal(h.state.project.tasks, before)
  h.emit('integrated', 'session.worktree.updated')
  assert.equal(h.reads.length, 3, 'real repository events must still inspect')
  h.reads[2].resolve({ task: { ...h.tasks[0], status: 'completed', git_status: 'clean', is_integrated: true } })
  await flush()
  lease.release()
})
