// Purpose: environment.updated must invalidate only demanded relevant projects,
// revoke browser checks immediately and retain a trailing refresh during fetch.
// DesktopProjectsRuntime + canonical reducer is the narrow no-poll cache boundary;
// controlled promises model HTTP ordering, not benchmark/provider execution.
import test from 'node:test'
import assert from 'node:assert/strict'
import { DesktopProjectsRuntime } from './desktop-projects'
import { reduceDesktopProjectsState, type DesktopProjectsState } from '../state/desktop-projects-state'

test('task environment invalidation coalesces and detach survives stale responses', async () => {
  let state: DesktopProjectsState = {}
  const reads: Array<(value: { tasks: any[] }) => void> = []
  const raw = { id: 't', source_workspace_id: 'w', environment_attachments: [{ id: 'a', source: { workspace_id: 'w' } }] }
  const runtime = new DesktopProjectsRuntime({ getState: () => state,
    dispatch: action => { state = reduceDesktopProjectsState(state, action) }, subscribe: () => () => {},
    fetchTasks: () => new Promise(resolve => reads.push(resolve)), fetchMedia: async () => ({ media: [] }) })
  const lease = runtime.acquire('p')
  reads[0]({ tasks: [raw] })
  await lease.ready
  const emit = (workspace_id: string) => runtime.acceptFrame({ kind: 'environment.updated', event: { payload: {
    workspace_id, task_environment_workspace_invalidated: true,
  } } })
  emit('unrelated')
  assert.equal(reads.length, 1)
  emit('w')
  assert.equal(state.p.tasks[0].environmentsStale, true)
  for (let i = 0; i < 10; i++) emit('w')
  assert.equal(reads.length, 2)
  reads[1]({ tasks: [raw] })
  for (let i = 0; i < 12; i++) await Promise.resolve()
  assert.equal(reads.length, 3, 'one completion-triggered trailing read')
  reads[2]({ tasks: [{ ...raw, environment_attachments: [] }] })
  for (let i = 0; i < 12; i++) await Promise.resolve()
  assert.deepEqual(state.p.tasks[0].environmentAttachments, [])
  assert.equal(state.p.tasks[0].environmentsStale, false)
  lease.release()
  assert.equal(state.p, undefined)
})

// Purpose: broad invalidation can arrive before any task identity. Exercise the
// runtime/reducer boundary to prove generation fencing and one trailing read.
test('broad initial-load invalidation fences unknown attachments without polling', async () => {
  let state: DesktopProjectsState = {}
  const reads: Array<(value: { tasks: any[] }) => void> = []
  const runtime = new DesktopProjectsRuntime({ getState: () => state,
    dispatch: action => { state = reduceDesktopProjectsState(state, action) }, subscribe: () => () => {},
    fetchTasks: () => new Promise(resolve => reads.push(resolve)), fetchMedia: async () => ({ media: [] }) })
  const lease = runtime.acquire('p')
  for (let i = 0; i < 4; i++) runtime.acceptFrame({ kind: 'environment.updated', payload: { workspace_id: 'w', task_environment_workspace_invalidated: true } })
  assert.equal(reads.length, 1)
  reads[0]({ tasks: [{ id: 'stale' }] })
  await lease.ready
  assert.deepEqual(state.p.tasks, [])
  assert.equal(reads.length, 2)
  reads[1]({ tasks: [{ id: 'fresh' }] })
  for (let i = 0; i < 12; i++) await Promise.resolve()
  assert.equal(state.p.tasks[0].id, 'fresh')
  runtime.acceptFrame({ kind: 'event', event_type: 'session.usage.updated' })
  assert.equal(reads.length, 2)
  lease.release()
})
