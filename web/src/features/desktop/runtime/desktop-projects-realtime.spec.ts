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
