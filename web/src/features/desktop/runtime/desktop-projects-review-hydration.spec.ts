import assert from 'node:assert/strict'
import test from 'node:test'
import { DesktopProjectsRuntime } from './desktop-projects'
import { mapBackendTask, reduceDesktopProjectsState, type DesktopProjectsState } from '../state/desktop-projects-state'

const flush = async () => { for (let i = 0; i < 16; i++) await Promise.resolve() }
const row = (revision: number) => ({ id: 'task', agent: 'swarm', title: 'Review', status: 'pending_approval', revision,
  session_id: 'owner', plan_binding: { session_id: 'owner', plan_id: 'plan', definition_revision: revision },
  board_summary: { plan: { id: 'plan', session_id: 'owner', version: revision, status: 'pending_approval', approval_state: 'pending' } },
})

// Purpose: DesktopProjectsRuntime.inspectTask must coalesce identical review
// reads while still trailing a replacement identity; an old success/failure must
// not enable approval or suppress retry on the new revision. Real mapping and
// reducer with deferred transport promises is the narrowest race boundary.
test('review inspections coalesce and fence superseded failures', { timeout: 5000 }, async () => {
  let state: DesktopProjectsState = {}
  let reads = 0
  const pending: Array<{ resolve: (response: any) => void; reject: (error: Error) => void }> = []
  const runtime = new DesktopProjectsRuntime({ getState: () => state,
    dispatch: action => { state = reduceDesktopProjectsState(state, action) },
    subscribe: () => () => {}, subscribeGit: () => () => {},
    fetchTasks: async () => ({ tasks: [row(2)] }), fetchMedia: async () => ({ media: [] }),
    fetchTask: async () => { reads++; return new Promise<{ task?: any }>((resolve, reject) => pending.push({ resolve, reject })) },
  })
  const lease = runtime.acquire('project')
  try {
    await lease.ready
    runtime.inspectTask('project', 'task')
    runtime.inspectTask('project', 'task')
    runtime.inspectTask('project', 'task')
    assert.equal(reads, 1)
    runtime.setOptimisticTasks('project', () => [mapBackendTask(row(3))])
    runtime.inspectTask('project', 'task')
    pending.shift()!.reject(new Error('Old revision failed'))
    await flush()
    assert.equal(reads, 2)
    assert.equal(state.project.tasks[0].detailError, undefined)
    assert.equal(state.project.tasks[0].detailLoaded, false)
    pending.shift()!.reject(new Error('Current revision failed'))
    await flush()
    assert.equal(state.project.tasks[0].detailError, 'Current revision failed')
    assert.equal(state.project.tasks[0].detailLoaded, false)
    assert.equal(reads, 2, 'failure must wait for explicit retry, not poll')
    runtime.inspectTask('project', 'task')
    runtime.inspectTask('project', 'task')
    assert.equal(reads, 3)
    const { board_summary: _, ...detail } = row(3)
    const document = { id: 'plan', title: 'Current exact plan' }
    pending.shift()!.resolve({ task: { ...detail, plan_document: document } })
    await flush()
    assert.equal(reads, 3)
    assert.equal(state.project.tasks[0].detailLoaded, true)
    assert.equal(state.project.tasks[0].detailError, undefined)
    assert.equal(state.project.tasks[0].planDocument, document)
    assert.equal(state.project.tasks[0].status, 'pending_approval')
    assert.equal(state.project.tasks[0].planBinding?.definitionRevision, 3)
    runtime.setOptimisticTasks('project', () => [mapBackendTask(row(4))])
    runtime.inspectTask('project', 'task')
    runtime.setOptimisticTasks('project', () => [mapBackendTask(row(5))])
    runtime.inspectTask('project', 'task')
    const { board_summary: oldSummary, ...oldDetail } = row(4)
    pending.shift()!.resolve({ task: { ...oldDetail, plan_document: document } })
    await flush()
    assert.equal(reads, 5)
    assert.equal(state.project.tasks[0].planBinding?.definitionRevision, 5)
    assert.equal(state.project.tasks[0].planDocument, undefined, 'old successful review cannot become current')
    assert.equal(state.project.tasks[0].detailLoaded, false)
    const { board_summary: newSummary, ...newDetail } = row(5)
    pending.shift()!.resolve({ task: { ...newDetail, plan_document: { ...document, title: 'Replacement review' } } })
    await flush()
    assert.equal(state.project.tasks[0].detailLoaded, true)
    assert.equal(state.project.tasks[0].planDocument?.title, 'Replacement review')
  } finally { lease.release() }
})
