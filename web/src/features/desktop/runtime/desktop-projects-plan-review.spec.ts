import assert from 'node:assert/strict'
import test from 'node:test'
import { DesktopProjectsRuntime } from './desktop-projects'
import { reduceDesktopProjectsState, type DesktopProjectsState } from '../state/desktop-projects-state'
import { createEmptyDesktopV3CacheState } from '../state/desktop-v3-cache-reducer'
import type { DesktopV3CacheMutation } from '../state/desktop-v3-cache-store'
import { selectTaskPlanDocument } from '../orchestrate/orchestrate-plan-authority'
import { aggregateTaskLiveState, buildTaskAcceptancePayload } from '../orchestrate/orchestrate-task-helpers'

// Requirement: durable session.plan events refresh the original task's exact review
// document through the task API, including replay/reconnect, without polling.
// Threat: completed planning leaves only summary markdown or a newer session
// snapshot silently changes what the user accepts. DesktopProjectsRuntime plus
// real API mapping/reducer/acceptance helpers is the narrowest cache boundary.
test('plan review survives live submission, replay and reload with exact acceptance identity', async () => {
  let state: DesktopProjectsState = {}
  let reads = 0
  let current: any = { id: 'task', session_id: 'session', title: 'Work', agent: 'plan', outcome_type: 'plan_spec', status: 'planning', revision: 1 }
  const runtime = new DesktopProjectsRuntime({
    getState: () => state,
    dispatch: action => { state = reduceDesktopProjectsState(state, action) },
    subscribe: () => () => {},
    fetchTasks: async () => ({ tasks: [current] }),
    fetchMedia: async () => ({ media: [] }),
    fetchTask: async () => { reads++; return { task: current } },
  })
  const flush = async () => { for (let i = 0; i < 12; i++) await Promise.resolve() }
  const lease = runtime.acquire('project')
  await lease.ready
  await flush()
  const baseline = reads
  current = { ...current, status: 'pending_approval', revision: 2, full_plan_markdown: '# Title only',
    plan_binding: { plan_id: 'plan', session_id: 'session', definition_revision: 1, receipt: 'definition-receipt' },
    plan_document: { id: 'plan', title: 'Implementation', checkpoints: [
      { id: 'cp-1', title: 'Implement', tasks: ['Write handler'], acceptance_criteria: ['Reject invalid input'] },
      { id: 'cp-2', title: 'Verify', tasks: ['Test recovery'], acceptance_criteria: ['No duplicate run'] },
    ] },
  }
  const cache = createEmptyDesktopV3CacheState()
  const emit = (sessionId: string, eventType: string) => runtime.acceptSessionMutation({
    action: { type: 'realtime.applyEvent', event: { source: 'realtime', sessionId, eventType, payload: {} } },
    previousState: cache, nextState: cache, durationMS: 0,
  } as DesktopV3CacheMutation)
  emit('other', 'session.plan.saved')
  emit('session', 'session.message.delta')
  assert.equal(reads, baseline)
  emit('session', 'session.plan.saved')
  await flush()
  const assertReview = () => {
    const task = state.project.tasks[0]
    assert.equal(task.status, 'pending_approval')
    assert.deepEqual(task.planDocument, current.plan_document)
    assert.deepEqual(buildTaskAcceptancePayload(task), { session_id: 'session', plan_id: 'plan', definition_revision: 1 })
    assert.equal(selectTaskPlanDocument(task, { id: 'plan', version: 2, document: { checkpoints: [] } }), task.planDocument)
  }
  assertReview()
  runtime.acceptFrame({ kind: 'rehydrate.required' })
  await flush()
  assertReview()
  lease.release()
  const reopened = runtime.acquire('project')
  await reopened.ready
  await flush()
  assertReview()
  reopened.release()
})

// Requirement: accepted durable admission is queued until the executor starts;
// aggregation must not label pending_executor as running or ask for approval.
test('accepted plan continuation is queued until canonical executor starts', () => {
  const task: any = { id: 'task', sessionId: 'session', agentType: 'swarm', status: 'running', outcomeType: 'plan_spec' }
  const data: any = { view: { current_run_state: { run_id: 'continuation', status: 'pending_executor' } } }
  assert.equal(aggregateTaskLiveState(task, { session: data }).status, 'queued')
  data.view.current_run_state.status = 'running'
  assert.equal(aggregateTaskLiveState(task, { session: data }).status, 'running')
})
