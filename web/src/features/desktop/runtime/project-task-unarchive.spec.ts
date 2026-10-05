import test from 'node:test'
import assert from 'node:assert/strict'
import { unarchiveProjectTask } from './project-task-unarchive'
import { DesktopProjectsRuntime } from './desktop-projects'
import { reduceDesktopProjectsState, type DesktopProjectsState } from '../state/desktop-projects-state'

// Purpose: the unarchive HTTP boundary must send exact revisions and reject
// malformed/stale receipts and changed account lifetimes before cache mutation.
// Injected transport tests are the narrowest deterministic request/receipt proof.
test('unarchive validates request, receipt and lifetime', async () => {
  const row = { id: 'a/b', revision: 4 }
  let calls = 0
  const request = (async (path: string, options: any) => {
    calls++
    assert.equal(path, '/v3/projects/p%2Fq/tasks/a%2Fb/unarchive')
    assert.deepEqual(JSON.parse(options.body), { revision: 4 })
    return { task: { id: row.id, revision: 5, archived: false, status: 'needs_review' } }
  }) as any
  assert.equal((await unarchiveProjectTask('p/q', row, () => true, request))?.status, 'needs_review')
  await assert.rejects(unarchiveProjectTask('p/q', { id: row.id }, () => true, request), /revision/)
  await assert.rejects(unarchiveProjectTask('p/q', row, () => false, request), /changed/)
  assert.equal(calls, 1)
  for (const task of [{ id: row.id, revision: 4, archived: false, status: 'needs_review' }, { id: row.id, revision: 5, archived: true, status: 'needs_review' }, { id: 'other', revision: 5, archived: false, status: 'needs_review' }]) {
    await assert.rejects(unarchiveProjectTask('p/q', row, () => true, (async () => ({ task })) as any), /Invalid/)
  }
  await assert.rejects(unarchiveProjectTask('p/q', row, () => true, (async () => { throw new Error('stale revision') }) as any), /stale/)
  let current = true
  assert.equal(await unarchiveProjectTask('p/q', row, () => current, (async () => { current = false; return {} }) as any), undefined)
})

// Purpose: canonical runtime/reducer receipts must restore the preserved status
// and fence older archive events and snapshots, without polling or a second cache.
test('unarchive receipt restores canonical task above archive revision fence', async () => {
  let state: DesktopProjectsState = {}
  const runtime = new DesktopProjectsRuntime({
    getState: () => state, dispatch: action => { state = reduceDesktopProjectsState(state, action) }, subscribe: () => () => {},
    fetchTasks: async () => ({ tasks: [{ id: 'task', revision: 1, status: 'needs_review' }] }),
    fetchMedia: async () => ({ media: [] }), fetchTask: async () => ({}),
  })
  const lease = runtime.acquire('p')
  await lease.ready
  runtime.archiveReceipt('p', { id: 'task', revision: 2, archived: true })
  assert.equal(state.p.tasks.length, 0)
  runtime.unarchiveReceipt('p', { id: 'task', revision: 3, archived: false, status: 'needs_review' })
  assert.equal(state.p.tasks[0].status, 'needs_review')
  runtime.archiveReceipt('p', { id: 'task', revision: 2, archived: true })
  await runtime.refresh('p', false)
  assert.equal(state.p.tasks[0].revision, 3)
  lease.release()
})
