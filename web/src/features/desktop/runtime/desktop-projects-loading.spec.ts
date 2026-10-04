import test from 'node:test'
import assert from 'node:assert/strict'
import { projectStartupState } from '../orchestrate/project-startup'
import { DesktopProjectsRuntime } from './desktop-projects'
import { reduceDesktopProjectsState, type DesktopProjectsState } from '../state/desktop-projects-state'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: Error) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
const flush = () => new Promise<void>(resolve => setImmediate(resolve))

// Purpose: DesktopProjectsRuntime.refresh + the canonical reducer must publish
// task rows without waiting for media. Controlled promises prove ordering and
// failure isolation, not latency or live-provider performance.
test('tasks publish before media, acquisition does not inspect Git, explicit refresh shares detail work', async () => {
  let state: DesktopProjectsState = {}
  const tasks = [deferred<{ tasks: any[] }>(), deferred<{ tasks: any[] }>()]
  const media = [deferred<{ media: any[] }>(), deferred<{ media: any[] }>()]
  const details = [deferred<{ task: any }>(), deferred<{ task: any }>()]
  let lists = 0, mediaReads = 0, detailReads = 0
  const row = { id: 'task', revision: 1, session_id: 'session', status: 'completed' }
  const runtime = new DesktopProjectsRuntime({
    getState: () => state, dispatch: action => { state = reduceDesktopProjectsState(state, action) },
    subscribe: () => () => {},
    fetchTasks: () => tasks[lists++].promise,
    fetchMedia: () => media[mediaReads++].promise,
    fetchTask: () => details[detailReads++].promise,
  })
  const lease = runtime.acquire('p')
  tasks[0].resolve({ tasks: [row] })
  await flush()
  assert.equal(state.p.tasks.length, 1)
  assert.equal(state.p.loading, false)
  assert.equal(state.p.mediaLoading, true)
  assert.equal(detailReads, 0, 'acquisition must not inspect individual tasks')
  await lease.ready
  assert.equal(state.p.mediaLoading, true, 'readiness resolves while optional media remains pending')
  media[0].reject(new Error('media unavailable'))
  await flush()
  assert.equal(state.p.error, undefined)
  assert.equal(state.p.mediaError, 'media unavailable')
  assert.equal(state.p.tasks.length, 1)
  const first = runtime.refresh('p')
  const second = runtime.refresh('p')
  assert.equal(first, second)
  assert.equal(state.p.tasks.length, 1, 'existing rows stay visible')
  tasks[1].resolve({ tasks: [row] })
  await flush()
  assert.equal(detailReads, 1, 'collection cannot queue a duplicate of the explicit refresh detail')
  details[0].resolve({ task: row })
  media[1].resolve({ media: [] })
  await first
  await flush()
  assert.deepEqual([lists, mediaReads, detailReads], [2, 2, 1])
  assert.equal(state.p.mediaError, undefined)
  lease.release()
})

// Purpose: refresh identity, release and mediaRequestId are the narrow authority
// against late cross-lifetime responses and overwriting optimistic media updates.
test('old lifetime responses and media errors cannot overwrite reacquired project or optimistic media', async () => {
  let state: DesktopProjectsState = {}
  const oldTasks = deferred<{ tasks: any[] }>(), oldMedia = deferred<{ media: any[] }>()
  let reads = 0
  const runtime = new DesktopProjectsRuntime({
    getState: () => state, dispatch: action => { state = reduceDesktopProjectsState(state, action) },
    subscribe: () => () => {}, fetchTask: async () => ({ task: undefined }),
    fetchTasks: () => ++reads === 1 ? oldTasks.promise : Promise.resolve({ tasks: [{ id: 'new', revision: 3 }] }),
    fetchMedia: () => reads === 1 ? oldMedia.promise : Promise.resolve({ media: [] }),
  })
  const old = runtime.acquire('p')
  old.release()
  const fresh = runtime.acquire('p')
  await fresh.ready
  runtime.setOptimisticMedia('p', [{ id: 'upload' } as any])
  oldTasks.resolve({ tasks: [{ id: 'old', revision: 1 }] })
  oldMedia.reject(new Error('old account error'))
  await old.ready
  assert.deepEqual(state.p.tasks.map(task => task.id), ['new'])
  assert.equal(state.p.media[0].id, 'upload')
  assert.equal(state.p.mediaError, undefined)
  fresh.release()
})

// Purpose: durable archive event receipts and HTTP receipts must commute without
// extra list/media/detail reads. Unknown frames still repair authoritatively;
// a receipt cannot hide a newer restored revision or another project's card.
test('archive event and HTTP response commute without retrieval amplification', async () => {
  for (const eventFirst of [true, false]) {
    let state: DesktopProjectsState = {}
    let lists = 0, media = 0, details = 0
    const runtime = new DesktopProjectsRuntime({
      getState: () => state, dispatch: action => { state = reduceDesktopProjectsState(state, action) }, subscribe: () => () => {},
      fetchTasks: async () => { lists++; return { tasks: [{ id: 'a', revision: 1 }, { id: 'b', revision: 1 }] } },
      fetchMedia: async () => { media++; return { media: [] } },
      fetchTask: async () => { details++; return {} },
    })
    const p = runtime.acquire('p'), q = runtime.acquire('q')
    await Promise.all([p.ready, q.ready])
    const frame = { kind: 'project.updated', project_id: 'p', event: { payload: { project_id: 'p', task_id: 'a', action: 'task_updated', revision: 2, archived: true } } }
    const http = () => runtime.archiveReceipt('p', { id: 'a', revision: 2, archived: true })
    if (eventFirst) { runtime.acceptFrame(frame); http() } else { http(); runtime.acceptFrame(frame) }
    assert.deepEqual(state.p.tasks.map(task => task.id), ['b'])
    assert.deepEqual(state.q.tasks.map(task => task.id), ['a', 'b'])
    assert.deepEqual([lists, media, details], [2, 2, 0])
    await runtime.refresh('p', false)
    assert.deepEqual(state.p.tasks.map(task => task.id), ['b'], 'stale collection cannot resurrect archived task')
    runtime.setOptimisticTasks('p', [...state.p.tasks, { ...state.q.tasks[0], revision: 3 }])
    runtime.acceptFrame(frame)
    assert.equal(state.p.tasks.find(task => task.id === 'a')?.revision, 3)
    runtime.acceptFrame({ kind: 'project.updated', project_id: 'p', event: { payload: { action: 'unknown' } } })
    await runtime.refresh('p', false)
    assert.equal(lists, 4, 'unknown frame must not be discarded')
    p.release(); q.release()
  }
})

// Purpose: projectStartupState must reveal admitted chat independently of task
// acquisition. DesktopProjectsRuntime retains inline failure/retry and zero
// per-task Git reads. Deferred collection reads prove this without browser timing.
test('empty and large collections settle retry and retain readiness through failed background refresh', async () => {
  for (const count of [0, 1, 250]) {
    let state: DesktopProjectsState = {}
    let lists = 0, details = 0
    const responses = Array.from({ length: 3 }, () => deferred<{ tasks: any[] }>())
    const signals: AbortSignal[] = []
    const runtime = new DesktopProjectsRuntime({
      getState: () => state, dispatch: action => { state = reduceDesktopProjectsState(state, action) }, subscribe: () => () => {},
      fetchTasks: (_id, signal) => { signals.push(signal!); return responses[lists++].promise },
      fetchMedia: () => new Promise(() => {}),
      fetchTask: async () => { details++; return {} },
    })
    const phase = () => projectStartupState({ catalogLoaded: true, catalogError: '', routeError: '', projectId: 'p', tasksObserved: state.p?.lastObservedAt !== undefined, tasksError: state.p?.error })
    const lease = runtime.acquire('p')
    assert.equal(phase().phase, 'ready', 'pending tasks cannot block chat')
    responses[0].reject(new Error('essential unavailable'))
    await lease.ready
    assert.equal(phase().phase, 'ready', 'failed tasks cannot block chat')
    assert.equal(state.p.error, 'essential unavailable', 'task failure remains visible inline')
    const retry = runtime.refresh('p', false)
    assert.equal(phase().phase, 'ready', 'pending tasks cannot block chat')
    responses[1].resolve({ tasks: Array.from({ length: count }, (_, n) => ({ id: `t-${n}`, status: 'completed', revision: 1, session_id: `s-${n}` })) })
    await retry
    assert.equal(phase().phase, 'ready')
    assert.equal(state.p.tasks.length, count)
    assert.equal(details, 0)
    const refresh = runtime.refresh('p', false)
    assert.equal(phase().phase, 'ready', 'background fetch cannot re-own initial reveal')
    responses[2].reject(new Error('background failure'))
    await refresh
    assert.equal(phase().phase, 'ready')
    assert.equal(state.p.tasks.length, count)
    lease.release()
    assert.ok(signals.every(signal => signal.aborted))
  }
})

// Purpose: release/reset in DesktopProjectsRuntime must cancel transport and
// reject stale collection writes. Controlled promises deliberately ignore abort
// to prove identity guards protect both route and authentication boundaries.
test('route release and authentication reset reject late responses without cross-project population', async () => {
  let state: DesktopProjectsState = {}
  const responses = new Map(['p', 'q'].map(id => [id, deferred<{ tasks: any[] }>()]))
  const signals: AbortSignal[] = []
  const runtime = new DesktopProjectsRuntime({
    getState: () => state, dispatch: action => { state = reduceDesktopProjectsState(state, action) }, subscribe: () => () => {},
    fetchTasks: (id, signal) => { signals.push(signal!); return responses.get(id)!.promise },
    fetchMedia: async () => ({ media: [] }), fetchTask: async () => ({}),
  })
  const old = runtime.acquire('p')
  old.release()
  const current = runtime.acquire('q')
  responses.get('p')!.resolve({ tasks: [{ id: 'wrong-project', revision: 1 }] })
  await old.ready
  assert.equal(state.p, undefined)
  assert.deepEqual(state.q.tasks, [])
  runtime.reset()
  responses.get('q')!.resolve({ tasks: [{ id: 'old-account', revision: 1 }] })
  await current.ready
  assert.ok(signals.every(signal => signal.aborted))
  assert.equal(state.q.tasks.length, 0, 'reset cannot commit late account data')
  current.release()
})
