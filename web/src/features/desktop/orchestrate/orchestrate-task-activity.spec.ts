import test from 'node:test'
import assert from 'node:assert/strict'
import { aggregateTaskLiveState } from './orchestrate-task-helpers'
import type { RunningTask } from './orchestrate-types'

// Purpose: Ended attempts must stop task-card timers without requiring a Git commit.
// Threat: stale lifecycle/view/job running projections mask canonical terminal intent.
// Boundary: aggregateTaskLiveState consumes V3 run intents and TaskProgram attempts;
// this selector test is the narrowest deterministic layer for status and counts.
test('terminal intent reconciles direct and multi-session task activity', () => {
  const direct = { id: 'direct-end', status: 'running', sessionId: 'direct-session', isIntegrated: false, unintegratedCommits: 0 } as RunningTask
  const ended = { 'direct-session': {
    intent: { status: 'completed', duration_ms: 4000 },
    view: { current_run_state: { status: 'running' } },
    sessionRecord: { kind: 'full', session: { id: 'direct-session', lifecycle: { active: true } } },
  } }
  const directResult = aggregateTaskLiveState(direct, ended)
  assert.equal(directResult.status, 'needs_review')
  assert.equal(directResult.sessionSummary?.runningSessions, 0)
  assert.equal(directResult.sessionSummary?.completedSessions, 1)
  assert.equal(aggregateTaskLiveState(direct, { 'direct-session': { intent: { status: 'failed' } } }).status, 'failed')

  const cohort = { id: 'cohort-end', status: 'running', sessionId: 'coordinator', taskProgramStatus: {
    state: 'running', jobs: [
      { job_id: 'one', state: 'running', child_session_id: 'child-one' },
      { job_id: 'two', state: 'running', child_session_id: 'child-two' },
    ],
  } } as RunningTask
  const sessions = {
    coordinator: { intent: { status: 'completed' } },
    'child-one': { intent: { status: 'completed' } },
    'child-two': { intent: { status: 'running' } },
  }
  const mixed = aggregateTaskLiveState(cohort, sessions)
  assert.equal(mixed.status, 'running')
  assert.equal(mixed.sessionSummary?.runningSessions, 1)
  assert.equal(mixed.sessionSummary?.completedSessions, 1)
  const allDone = aggregateTaskLiveState(cohort, { ...sessions, 'child-two': { intent: { status: 'completed' } } })
  assert.equal(allDone.status, 'needs_review')
  assert.equal(allDone.sessionSummary?.runningSessions, 0)
  assert.equal(allDone.sessionSummary?.completedSessions, 2)
})
