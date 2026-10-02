// Purpose: workflow review and child blockers must not suppress live execution.
// Threat: the Running filter drops a repairing parent and TaskCardActivity vanishes.
// Boundaries: aggregateTaskLiveState, isTaskRunning (the card/filter predicate),
// and the actual activity renderer. Selector + SSR is the narrowest observable
// layer for activity/navigation evidence; no daemon or simulated benchmark needed.
import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { aggregateTaskLiveState, isTaskRunning } from './orchestrate-task-helpers'
import { TaskCardActivity } from './task-card-activity'
import type { RunningTask } from './orchestrate-types'

const task = {
  id: 'task', title: 'Repair child dependency', status: 'needs_review', sessionId: 'parent',
  sessionIds: ['parent', 'child'], actionNeeded: 'Review child blocker',
  taskProgramStatus: { state: 'blocked', jobs: [{ job_id: 'job', current_session_id: 'child', state: 'handoff_ready' }] },
} as unknown as RunningTask
const run = (status: string) => ({ view: { current_run_state: { run_id: 'current', status } } })

test('running parent remains in Running and exposes activity and session while program needs review', () => {
  for (const state of ['blocked', 'completed', 'failed']) {
    const input = { ...task, taskProgramStatus: { ...task.taskProgramStatus, state } } as RunningTask
    const live = aggregateTaskLiveState(input, { parent: run('running'), child: run('completed') })
    assert.deepEqual([live].filter(isTaskRunning).map(t => t.id), ['task'])
    assert.match(renderToStaticMarkup(<TaskCardActivity task={live} />), /role="status"/)
    assert.ok(live.sessionSummary?.sessionStates.some(s => s.sessionId === 'parent' && s.status === 'running'))
    assert.equal(live.actionNeeded, 'Review child blocker')
    assert.equal(input.status, 'needs_review', 'workflow input must not be mutated')
  }
})

test('running child defeats stale handoff; terminal and queued evidence remove activity', () => {
  const live = aggregateTaskLiveState(task, { parent: run('completed'), child: run('running') })
  assert.equal(isTaskRunning(live), true)
  assert.ok(live.sessionSummary?.sessionStates.some(s => s.sessionId === 'child' && s.status === 'running'))
  for (const status of ['completed', 'failed', 'pending_executor']) {
    const child = { ...run(status), sessionRecord: { kind: 'full', session: { lifecycle: { active: true, phase: 'running' } } },
      liveRun: { runId: 'current', assistantDraft: { content: 'old text', updatedAt: 1 } } }
    const ended = aggregateTaskLiveState(task, { parent: run('completed'), child } as any)
    assert.equal(isTaskRunning(ended), false)
    assert.equal(renderToStaticMarkup(<TaskCardActivity task={ended} />), '')
    assert.equal(ended.liveAssistantText, undefined)
  }
})

test('partial hydration retains known parent execution but never invents execution from old jobs', () => {
  assert.equal(isTaskRunning(aggregateTaskLiveState(task, { parent: run('running') })), true)
  const stale = { ...task, taskProgramStatus: { state: 'running', jobs: [{ job_id: 'job', child_session_id: 'old', current_session_id: 'child', state: 'running' }] }, sessionIds: ['parent', 'child', 'old'] } as unknown as RunningTask
  const live = aggregateTaskLiveState(stale, { parent: run('completed'), old: run('running') })
  assert.equal(isTaskRunning(live), false)
  assert.equal(live.sessionSummary?.sessionStates.find(s => s.sessionId === 'child')?.status, 'unknown')
  assert.ok(!live.sessionSummary?.sessionStates.some(s => s.sessionId === 'old'))
})
