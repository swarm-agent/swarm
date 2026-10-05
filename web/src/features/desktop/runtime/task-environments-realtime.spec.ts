// Purpose: environment.updated must invalidate only demanded relevant projects,
// revoke browser checks immediately and retain a trailing refresh during fetch.
// DesktopProjectsRuntime + canonical reducer is the narrow no-poll cache boundary;
// controlled promises model HTTP ordering, not benchmark/provider execution.
import test from 'node:test'
import assert from 'node:assert/strict'
import { DesktopProjectsRuntime } from './desktop-projects'
import { realtimeFrameToActions } from '../state/desktop-v3-cache-wire'
import type { RealtimeMessage } from '../state/desktop-v3-cache-types'
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
  const emit = (workspace_id: string) => {
    // Use the production wire admission before the runtime, not a direct shortcut.
    const frame = { protocol: 'v3.realtime', protocol_version: 1, kind: 'environment.updated',
      endpoint_cursor: 'opaque-environment-cursor', session_id: '__environment__:fixture',
      event: { session_id: '__environment__:fixture', event_type: 'environment.updated', payload: {
        workspace_id, task_environment_workspace_invalidated: true,
      } } } as RealtimeMessage
    assert.deepEqual(realtimeFrameToActions(frame), [{ type: 'realtime.control', frame }])
    runtime.acceptFrame(frame)
  }
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

// Purpose: malformed environment resource frames must fail at wire admission,
// before project cache mutation; no session authority may be smuggled in a frame.
test('environment wire admission rejects missing scope, cursor and session payload', () => {
  const frame = { protocol: 'v3.realtime', protocol_version: 1, kind: 'environment.updated',
    endpoint_cursor: 'opaque', event: { event_type: 'environment.updated', payload: { workspace_id: 'w' } } } as RealtimeMessage
  for (const invalid of [
    { ...frame, endpoint_cursor: '' },
    { ...frame, event: { ...frame.event, payload: {} } },
    { ...frame, event: { ...frame.event, event_type: 'session.updated' } },
    { ...frame, session: { id: 'session-fixture' } },
    { ...frame, protocol_version: 2 },
  ]) assert.throws(() => realtimeFrameToActions(invalid as RealtimeMessage), /protocol invalid/)
})
