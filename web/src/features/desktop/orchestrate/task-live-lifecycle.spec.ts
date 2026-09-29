// Purpose: Tasks view consumes V3 child-card current_run_state even without chat
// selection or run_intents history. Threat: terminal hydration leaves a stale running
// badge, and a new run inherits old text. Boundary: aggregateTaskLiveState selector;
// this is the narrowest observable projection of task card status and activity.
import assert from 'node:assert/strict'
import test from 'node:test'
import { aggregateTaskLiveState } from './orchestrate-task-helpers'
import type { RunningTask } from './orchestrate-types'

const task = { id: 'task', title: 'Task', sessionId: 'session', status: 'in_progress' } as RunningTask
const state = (runId: string, status: string, text?: string) => ({
  view: { current_run_state: { run_id: runId, status } },
  liveRun: text ? { runId, assistantSegments: [{ content: text, timelineSeq: 1, updatedAt: 1 }] } : undefined,
})

test('no-chat current run hydrates starting, text, needs review, and resume without old activity', () => {
  const starting = aggregateTaskLiveState(task, { session: state('first', 'running') })
  assert.equal(starting.status, 'running')
  assert.equal(starting.liveAssistantText, undefined)
  const streaming = aggregateTaskLiveState(task, { session: state('first', 'running', 'First output') })
  assert.equal(streaming.liveAssistantText, 'First output')
  const ended = aggregateTaskLiveState(task, { session: state('first', 'completed', 'First output') })
  assert.equal(ended.status, 'needs_review')
  assert.equal(ended.liveAssistantText, undefined)
  const resumed = aggregateTaskLiveState({ ...task, status: 'needs_review' }, {
    session: { ...state('first', 'completed', 'First output'), intent: { run_id: 'second', status: 'running' } },
  })
  assert.equal(resumed.status, 'running')
  assert.equal(resumed.liveAssistantText, undefined)
  const stale = aggregateTaskLiveState({ ...task, status: 'needs_review' }, {
    session: { ...state('second', 'running', 'Second output'), intent: { run_id: 'first', status: 'completed' } },
  })
  assert.equal(stale.status, 'running', 'prior-run terminal event cannot regress the current run')
  assert.equal(stale.liveAssistantText, 'Second output')
  assert.equal(aggregateTaskLiveState({ ...task, status: 'needs_review' }, { session: state('second', 'running', 'Second output') }).liveAssistantText, 'Second output')
})

test('terminal failure and unfinished plan do not become successful review', () => {
  assert.equal(aggregateTaskLiveState(task, { session: state('first', 'failed') }).status, 'failed')
  const unfinished = { ...task, planDocument: { checkpoints: [{ id: 'cp', status: 'in_progress' }] } } as RunningTask
  assert.notEqual(aggregateTaskLiveState(unfinished, { session: state('first', 'completed') }).status, 'needs_review')
})
