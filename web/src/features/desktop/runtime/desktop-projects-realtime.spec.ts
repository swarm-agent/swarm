import assert from 'node:assert/strict'
import test from 'node:test'
import { DesktopProjectsRuntime } from './desktop-projects'
import { reduceDesktopProjectsState, type DesktopProjectsState } from '../state/desktop-projects-state'
import { createEmptyDesktopV3CacheState } from '../state/desktop-v3-cache-reducer'
import type { DesktopV3CacheMutation } from '../state/desktop-v3-cache-store'

// Purpose: task cards consume the Changes box's scoped durable session events,
// without reloading their collection or writing on reads. Exercise the runtime
// boundary directly: unrelated/token events, bursts, failed reads and late
// responses must not cause storms, hide errors or resurrect released state.
test('task Git refresh reuses scoped session events and coalesces without collection churn', async () => {
  let state: DesktopProjectsState = {}
  let collections = 0
  const reads: Array<{ resolve: (value: { task: any }) => void; reject: (error: Error) => void }> = []
  const task = { id: 'task', session_id: 'session', title: 'Work', revision: 1, agent: 'coder', status: 'needs_review' }
  const runtime = new DesktopProjectsRuntime({
    getState: () => state,
    dispatch: action => { state = reduceDesktopProjectsState(state, action) },
    fetchTasks: async () => { collections++; return { tasks: [task] } },
    fetchMedia: async () => ({ media: [] }),
    fetchTask: () => new Promise((resolve, reject) => reads.push({ resolve, reject })),
  })
  const emit = (sessionId: string, eventType: string) => {
    const cache = createEmptyDesktopV3CacheState()
    runtime.acceptSessionMutation({
      action: { type: 'realtime.applyEvent', event: { source: 'realtime', sessionId, eventType, payload: {} } },
      previousState: cache, nextState: cache, durationMS: 0,
    } as DesktopV3CacheMutation)
  }
  const flush = async () => { for (let i = 0; i < 8; i++) await Promise.resolve() }
  const lease = runtime.acquire('project')
  await lease.ready
  assert.equal(state.project.loading, false)
  assert.equal(state.project.tasks.length, 1)
  assert.equal(reads.length, 1)
  reads[0].resolve({ task: { ...task, git_status: 'dirty', is_dirty: true, dirty_count: 2 } })
  await flush()
  assert.equal(state.project.tasks[0].dirtyCount, 2)
  emit('other', 'session.tool.completed')
  emit('session', 'session.message.delta')
  assert.equal(reads.length, 1)
  emit('session', 'session.tool.completed')
  for (let i = 0; i < 10; i++) emit('session', 'session.worktree.updated')
  assert.equal(reads.length, 2)
  reads[1].resolve({ task: { ...task, git_status: 'clean' } })
  await flush()
  assert.equal(reads.length, 3)
  reads[2].reject(new Error('repository unavailable'))
  await flush()
  assert.equal(state.project.tasks[0].gitStatus, 'unknown')
  assert.equal(state.project.tasks[0].syncWarning, 'repository unavailable')
  assert.equal(collections, 1)
  assert.equal(reads.length, 3)
  emit('session', 'session.run.completed')
  assert.equal(reads.length, 4)
  lease.release()
  reads[3].resolve({ task: { ...task, is_integrated: true } })
  await flush()
  assert.equal(state.project, undefined)
  assert.equal(reads.length, 4)
})

test('desktop projects runtime subscribes to cache mutations on demand and unsubscribes when released', () => {
  let subscriptions = 0
  let unsubscriptions = 0
  const runtime = new DesktopProjectsRuntime({
    getState: () => ({}),
    dispatch: () => {},
    fetchTasks: async () => ({ tasks: [] }),
    fetchMedia: async () => ({ media: [] }),
    fetchTask: async () => ({ task: null }),
    subscribe: () => {
      subscriptions++
      return () => {
        unsubscriptions++
      }
    },
  })
  assert.equal(subscriptions, 0)
  const lease1 = runtime.acquire('project1')
  assert.equal(subscriptions, 1)
  const lease2 = runtime.acquire('project2')
  assert.equal(subscriptions, 1)
  lease1.release()
  assert.equal(subscriptions, 1)
  assert.equal(unsubscriptions, 0)
  lease2.release()
  assert.equal(unsubscriptions, 1)
})

// Purpose: a visible terminal card must adopt a canonical follow-up's new session
// from project.updated, even though no event belongs to its retained old session.
// Rehydrate must repair a missed event. The runtime/reducer layer is the narrow
// task-card cache authority; no manual reload or alternate session cache is used.
// mapBackendTask normalizes the wire in_progress status to the UI running status.
test('completed card refreshes to follow-up and repairs missed reopen on reconnect', async () => {
  let state: DesktopProjectsState = {}
  let task = { id: 'task', session_id: 'old-session', title: 'Work', revision: 1, agent: 'swarm', status: 'completed' }
  const runtime = new DesktopProjectsRuntime({
    getState: () => state,
    dispatch: action => { state = reduceDesktopProjectsState(state, action) },
    fetchTasks: async () => ({ tasks: [task] }),
    fetchMedia: async () => ({ media: [] }),
    fetchTask: async () => ({ task }),
  })
  const flush = async () => { for (let i = 0; i < 20; i++) await Promise.resolve() }
  const lease = runtime.acquire('project')
  await lease.ready
  await flush()
  assert.equal(state.project.tasks[0].status, 'completed')
  task = { ...task, revision: 2, status: 'in_progress', session_id: 'follow-up' }
  runtime.acceptFrame({ kind: 'project.updated', project_id: 'project' })
  await flush()
  assert.equal(state.project.tasks[0].status, 'running')
  assert.equal(state.project.tasks[0].sessionId, 'follow-up')
  task = { ...task, revision: 3, status: 'completed' }
  runtime.acceptFrame({ kind: 'project.updated', project_id: 'project' })
  await flush()
  task = { ...task, revision: 4, status: 'in_progress', session_id: 'reconnected-follow-up' }
  runtime.acceptFrame({ kind: 'rehydrate.required' })
  await flush()
  assert.equal(state.project.tasks[0].status, 'running')
  assert.equal(state.project.tasks[0].sessionId, 'reconnected-follow-up')
  lease.release()
})

// Purpose: durable project task_updated frames must refresh only their named card,
// not reload tasks/media and rerun unrelated Git reads. DesktopProjectsRuntime's
// frame/queue boundary is the narrowest layer proving request counts, coalescing,
// revision/session safety, visible failures, membership repair and reconnect repair.
test('durable task updates are card-scoped, coalesced and safe across session replacement', { timeout: 5000 }, async () => {
  let state: DesktopProjectsState = {}
  let collections = 0
  let mediaReads = 0
  let notifications = 0
  const tasks = [
    { id: 'first', session_id: 'one', revision: 1, title: 'First', agent: 'coder', status: 'needs_review' },
    { id: 'second', session_id: 'two', revision: 1, title: 'Second', agent: 'coder', status: 'needs_review' },
    { id: 'queued', revision: 1, title: 'Queued', agent: 'coder', status: 'queued' },
  ]
  const reads: Array<{ id: string; resolve: (value: { task: any }) => void; reject: (error: Error) => void }> = []
  const runtime = new DesktopProjectsRuntime({
    getState: () => state,
    dispatch: action => { state = reduceDesktopProjectsState(state, action) },
    subscribe: () => () => {},
    fetchTasks: async () => { collections++; return { tasks } },
    fetchMedia: async () => { mediaReads++; return { media: [] } },
    fetchTask: (_project, id) => new Promise((resolve, reject) => reads.push({ id, resolve, reject })),
  })
  const unsubscribe = runtime.onProjectUpdate(() => { notifications++ })
  const emit = (id: string, action = 'task_updated', project = 'project') => runtime.acceptFrame({
    kind: 'project.updated', project_id: project, event: { payload: { project_id: project, task_id: id, action } },
  })
  const flush = async () => { for (let i = 0; i < 16; i++) await Promise.resolve() }
  const lease = runtime.acquire('project')
  await lease.ready
  assert.deepEqual(reads.map(read => read.id), ['first', 'second'])
  reads[0].resolve({ task: tasks[0] })
  reads[1].resolve({ task: tasks[1] })
  await flush()
  const unrelated = state.project.tasks[1]
  emit('first')
  for (let i = 0; i < 20; i++) emit('first')
  assert.deepEqual(reads.map(read => read.id), ['first', 'second', 'first'])
  assert.equal(collections, 1)
  assert.equal(mediaReads, 1)
  assert.equal(state.project.loading, false)
  assert.equal(state.project.stale, false)
  reads[2].resolve({ task: { ...tasks[0], revision: 2, session_id: 'replacement', title: 'Updated' } })
  await flush()
  assert.equal(state.project.tasks[0].sessionId, 'one', 'superseded read is not published')
  assert.equal(state.project.tasks[0].gitStatus, 'stale')
  assert.equal(state.project.tasks[1], unrelated)
  assert.equal(reads.length, 4) // One completion-coalesced trailing read.
  reads[3].resolve({ task: { ...tasks[0], revision: 2, session_id: 'replacement', title: 'Updated' } })
  await flush()
  assert.equal(state.project.tasks[0].revision, 2)
  assert.equal(state.project.tasks[0].sessionId, 'replacement')
  assert.equal(state.project.tasks[0].title, 'Updated')
  assert.equal(state.project.tasks[0].isIntegrated, false)
  emit('queued') // Undeployed cards must also receive durable updates.
  reads[4].resolve({ task: { ...tasks[2], revision: 2, session_id: 'deployed', status: 'in_progress' } })
  await flush()
  assert.equal(state.project.tasks[2].sessionId, 'deployed')
  assert.equal(state.project.tasks[2].status, 'running')
  emit('first')
  reads[5].reject(new Error('inspection unavailable'))
  await flush()
  assert.equal(state.project.tasks[0].gitStatus, 'unknown')
  assert.equal(state.project.tasks[0].isIntegrated, false)
  assert.equal(state.project.tasks[0].syncWarning, 'inspection unavailable')
  emit('queued')
  reads[6].resolve({ task: { ...tasks[2], revision: 3, session_id: 'deployed', archived: true } })
  await flush()
  assert.deepEqual(state.project.tasks.map(task => task.id), ['first', 'second'])
  const idleReads = reads.length
  emit('first', 'task_updated', 'foreign')
  await flush()
  assert.equal(reads.length, idleReads)
  assert.equal(collections, 1)
  assert.equal(mediaReads, 1)
  assert.equal(notifications, 1) // Foreign fallback only; no task-update theme churn.
  emit('second', 'task_deleted')
  await flush()
  assert.equal(collections, 2)
  assert.equal(mediaReads, 2)
  // Resolve the collection's Git observations before triggering reconnect repair.
  for (const read of reads.slice(idleReads)) read.resolve({ task: tasks.find(task => task.id === read.id) })
  await flush()
  runtime.acceptFrame({ kind: 'rehydrate.required' })
  await flush()
  assert.equal(collections, 3)
  assert.equal(mediaReads, 3)
  lease.release()
  for (const read of reads.slice(idleReads)) read.resolve({ task: tasks.find(task => task.id === read.id) })
  await flush()
  assert.equal(state.project, undefined)
  unsubscribe()
})
