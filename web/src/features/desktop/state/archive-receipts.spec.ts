import assert from 'node:assert/strict'
import test from 'node:test'
import { DesktopDesignState } from './desktop-design-state'
import { designReadyItems } from '../orchestrate/design-media-task'
import { mapBackendTask, reduceDesktopProjectsState } from './desktop-projects-state'
import type { ProjectDesign } from '../session-v3/design-api'

// Purpose: DesktopDesignState.acceptArchive owns receipt reconciliation. Prove
// targeted removal, immutable history and candidate indexes survive stale reads;
// another session and account reset must not inherit artifact visibility.
test('design archive receipts preserve history and suppress stale catalog candidates', async () => {
  const ref = { artifact_id: 'html', revision: 1, sha256: 'digest' }
  const revision = { ref, kind: 'html', attempt: { number: 1, state: 'succeeded' } }
  const row: ProjectDesign = { project_id: 'p', title: 'Design', request: { id: 'r', parent_session_id: 's', state: 'succeeded', candidates: [
    { spec: { artifact_id: 'other', kind: 'html' }, state: 'failed' },
    { spec: { artifact_id: 'html', kind: 'html' }, state: 'succeeded', attempts: [{ number: 1, state: 'succeeded', result: ref }] },
  ] } }
  let reads = 0
  const state = new DesktopDesignState({ project: async () => { reads++; return { designs: [row], next_cursor: '' } }, catalog: async () => ({ requests: [], next_cursor: '' }), history: async () => ({ artifact: { id: 'html', kind: 'html', revision_count: 1, selection_version: 0 }, revisions: [revision] }) })
  const project = state.project('p'); const history = state.history('s', 'html')
  await project.refresh(); await history.refresh()
  assert.equal(designReadyItems(project.getSnapshot().data!.designs)[0].variantIndex, 2)
  const artifact = { id: 'html', kind: 'html', revision_count: 1, selection_version: 0, archive_version: 1, archived: true }
  state.acceptArchive('unrelated', artifact)
  assert.equal(designReadyItems(project.getSnapshot().data!.designs).length, 1)
  state.acceptArchive('s', artifact)
  assert.equal(reads, 1)
  assert.equal(designReadyItems(project.getSnapshot().data!.designs).length, 0)
  assert.deepEqual(history.getSnapshot().data!.revisions, [revision])
  await project.refresh(); await history.refresh()
  assert.equal(designReadyItems(project.getSnapshot().data!.designs).length, 0)
  assert.equal(history.getSnapshot().data!.artifact.archived, true)
  state.reset(); await project.refresh()
  assert.equal(designReadyItems(project.getSnapshot().data!.designs).length, 1)
})

// Purpose: reduceDesktopProjectsState is the canonical task cache boundary.
// A collection response begun before acknowledgement cannot resurrect an archive;
// unrelated cards remain, and a newer restored revision is allowed.
test('task receipt fences stale reads without invalidating the project', () => {
  const task = mapBackendTask({ id: 'a', revision: 1 })
  const other = mapBackendTask({ id: 'b', revision: 1 })
  let state = reduceDesktopProjectsState({}, { type: 'projects.beginLoad', projectId: 'p', requestId: 'initial' })
  state = reduceDesktopProjectsState(state, { type: 'projects.loadSuccess', projectId: 'p', requestId: 'initial', generation: 0, tasks: [task, other], media: [] })
  state = reduceDesktopProjectsState(state, { type: 'projects.beginLoad', projectId: 'p', requestId: 'late' })
  state = reduceDesktopProjectsState(state, { type: 'projects.updateTasks', projectId: 'p', tasks: tasks => tasks, archivedReceipt: { id: 'a', revision: 2 } })
  assert.deepEqual(state.p.tasks.map(row => row.id), ['b'])
  state = reduceDesktopProjectsState(state, { type: 'projects.loadSuccess', projectId: 'p', requestId: 'late', generation: 0, tasks: [task, other], media: [] })
  assert.deepEqual(state.p.tasks.map(row => row.id), ['b'])
  assert.equal(state.p.stale, false)
  state = reduceDesktopProjectsState(state, { type: 'projects.updateTasks', projectId: 'p', tasks: [{ ...task, revision: 3 }, other] })
  assert.deepEqual(state.p.tasks.map(row => row.id), ['a', 'b'])
})

// Purpose: beginLoad must retain acknowledged archive fences, including when
// archive precedes refresh. The reducer is the narrowest owner of stale detail
// rejection, newer revision acceptance, project isolation and eviction reset.
test('archive fences survive later refresh and stale detail but not eviction', () => {
  const task = mapBackendTask({ id: 'a', revision: 1 })
  let state = reduceDesktopProjectsState({}, { type: 'projects.beginLoad', projectId: 'p', requestId: 'one' })
  state = reduceDesktopProjectsState(state, { type: 'projects.loadSuccess', projectId: 'p', requestId: 'one', generation: 0, tasks: [task] })
  state = reduceDesktopProjectsState(state, { type: 'projects.updateTasks', projectId: 'p', tasks: tasks => tasks, archivedReceipt: { id: 'a', revision: 2 } })
  state = reduceDesktopProjectsState(state, { type: 'projects.beginLoad', projectId: 'p', requestId: 'two' })
  state = reduceDesktopProjectsState(state, { type: 'projects.loadSuccess', projectId: 'p', requestId: 'two', generation: 0, tasks: [task] })
  state = reduceDesktopProjectsState(state, { type: 'projects.updateTasks', projectId: 'p', tasks: [task], inspectedTaskId: 'a' })
  assert.deepEqual(state.p.tasks, [])
  state = reduceDesktopProjectsState(state, { type: 'projects.beginLoad', projectId: 'q', requestId: 'other' })
  state = reduceDesktopProjectsState(state, { type: 'projects.loadSuccess', projectId: 'q', requestId: 'other', generation: 0, tasks: [task] })
  assert.equal(state.q.tasks.length, 1)
  state = reduceDesktopProjectsState(state, { type: 'projects.updateTasks', projectId: 'p', tasks: [{ ...task, revision: 3 }] })
  state = reduceDesktopProjectsState(state, { type: 'projects.updateTasks', projectId: 'p', tasks: [task] })
  assert.equal(state.p.tasks[0].revision, 3)
  state = reduceDesktopProjectsState(state, { type: 'projects.evict', projectId: 'p' })
  state = reduceDesktopProjectsState(state, { type: 'projects.beginLoad', projectId: 'p', requestId: 'new-account' })
  state = reduceDesktopProjectsState(state, { type: 'projects.loadSuccess', projectId: 'p', requestId: 'new-account', generation: 0, tasks: [task] })
  assert.equal(state.p.tasks[0].revision, 1)
})
