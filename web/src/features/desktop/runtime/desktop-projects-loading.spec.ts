import test from 'node:test'
import assert from 'node:assert/strict'
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
test('tasks publish before media, media failure stays independent, refresh callers share detail work', async () => {
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
  assert.equal(detailReads, 1)
  details[0].resolve({ task: row })
  media[0].reject(new Error('media unavailable'))
  await lease.ready
  assert.equal(state.p.error, undefined)
  assert.equal(state.p.mediaError, 'media unavailable')
  assert.equal(state.p.tasks.length, 1)
  const first = runtime.refresh('p')
  const second = runtime.refresh('p')
  assert.equal(first, second)
  assert.equal(state.p.tasks.length, 1, 'existing rows stay visible')
  tasks[1].resolve({ tasks: [row] })
  await flush()
  assert.equal(detailReads, 2, 'collection cannot queue a duplicate of the refresh detail')
  details[1].resolve({ task: row })
  media[1].resolve({ media: [] })
  await first
  await flush()
  assert.deepEqual([lists, mediaReads, detailReads], [2, 2, 2])
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
