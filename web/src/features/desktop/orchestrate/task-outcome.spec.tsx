import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { readFileSync } from 'node:fs'
import { mapBackendTask } from '../state/desktop-projects-state'
import { taskOutcome } from './task-outcome'
import { TaskCardSummary } from './task-card-summary'
import { TaskOutcomeDetails, ProjectTaskAttention } from './task-outcome-view'
import { aggregateTaskLiveState } from './orchestrate-task-helpers'
import { integrationFailure, repairUnavailable } from './integration-recovery'
import { createTaskIntegrationController, taskIntegrationFailureIdentity, taskIntegrationKey, taskIntegrationPhase } from './task-integration-operation'
import { createTaskReopenController, taskReopenKey } from './task-reopen-operation'
import type { RunningTask } from './orchestrate-types'

const raw = { id: 'task', title: 'Retained work', agent: 'coder', status: 'needs_review', revision: 4,
  session_id: 'owner', active_attempt_id: 'attempt', worktree_branch: 'agent/work', base_branch: 'release',
  source_workspace: { workspace_id: 'workspace', path: '/repo' },
  integration: { state: 'conflict', session_id: 'owner', attempt_id: 'attempt', operation_id: 'preflight',
    source_head: 'source-sha', previous_target_head: 'target-sha', error: 'Preflight conflict in viewer.tsx; token=hidden' } }

// Requirement: task hydration must restore preflight failure after reload. Authority:
// mapBackendTask -> taskOutcome -> summary/details/project attention. Server rendering
// is the narrowest proof of visible text without a provider; it does not prove layout.
test('hydrated preflight conflict survives reload across all attention surfaces', () => {
  for (const record of [raw, JSON.parse(JSON.stringify(raw))]) {
    const task = mapBackendTask(record)
    const outcome = taskOutcome(task)
    assert.equal(outcome.blocker?.title, 'Integration conflict')
    assert.equal(outcome.needsAttention, true)
    assert.match(outcome.delivery || '', /Delivery to release not verified/)
    const html = renderToStaticMarkup(<><TaskCardSummary task={task} /><TaskOutcomeDetails task={task} /><ProjectTaskAttention tasks={[task]} onOpen={() => {}} /></>)
    assert.match(html, /Integration conflict/)
    assert.match(html, /Project task attention/)
    assert.match(html, /source-sha/)
    assert.match(html, /target-sha/)
    assert.match(html, /Preflight conflict in viewer.tsx/)
    assert.doesNotMatch(html, /token=hidden|Integrated into release/)
  }
})

// Requirement: failed current owner execution must outrank stale task completion and
// program assembly; interrupted validation is not a pass. Production live aggregator
// consumes durable current_run_state even when lifecycle and task snapshots lag.
test('canceled validation outranks completion and never becomes verified delivery', () => {
  const task = { ...mapBackendTask({ ...raw, status: 'completed', integration: undefined }),
    taskProgramStatus: { parent_session_id: 'owner', program_id: 'program', state: 'completed', definition: { stages: [], jobs: [] }, jobs: [] } }
  const projected = aggregateTaskLiveState(task, { owner: { view: { current_run_state: {
    run_id: 'validation', status: 'cancelled', event_seq: 8, blocked_reason: 'executor context canceled',
  } } } } as any)
  assert.equal(projected.status, 'failed')
  assert.equal(projected.currentRunStatus, 'cancelled')
  const outcome = taskOutcome(projected)
  assert.equal(outcome.blocker?.title, 'Execution interrupted')
  assert.match(outcome.verification, /incomplete/)
  assert.match(outcome.delivery || '', /not verified/)
  assert.equal(outcome.needsAttention, true)
})

// Requirement: scheduler assembly, handoff prose and completed checklists cannot
// establish tests or captured-branch delivery; only independent Git authority may.
test('assembled jobs and test prose never imply verification or promotion', () => {
  const task: RunningTask = { ...mapBackendTask({ ...raw, integration: undefined }), status: 'completed',
    whatDidDo: ['Tests passed'], taskProgramStatus: { parent_session_id: 'owner', program_id: 'p', state: 'completed',
      definition: { stages: [], jobs: [] }, jobs: [{ job_id: 'code', stage_id: 'one', state: 'integrated' }] } }
  assert.equal(taskOutcome(task).execution, 'Program assembled')
  assert.match(taskOutcome(task).verification, /not established/)
  assert.match(taskOutcome(task).delivery || '', /not verified/)
  assert.equal(taskOutcome(task).needsAttention, false, 'missing Git evidence is unknown, not an action request')
  assert.match(taskOutcome({ ...task, isIntegrated: true, gitStatus: 'stale' }).delivery || '', /not verified/)
  assert.match(taskOutcome({ ...task, isIntegrated: true, gitStatus: 'clean', isDirty: true }).delivery || '', /not verified/)
  assert.equal(taskOutcome({ ...task, isIntegrated: true, gitStatus: 'clean' }).delivery, 'Integrated into release')
  assert.match(taskOutcome({ ...task, isIntegrated: true, gitStatus: 'clean' }).verification, /not established/)
})

// Requirement: old attempts/program generations must not poison a running repair;
// durable recovery lineage supplies the same Open repair session after remount.
test('repair navigation comes from current durable attempt, not old failure memory', () => {
  const task = mapBackendTask({ ...raw, session_id: 'repair', active_attempt_id: 'repair-attempt', status: 'in_progress',
    attempts: [{ id: 'attempt', session_id: 'owner', role: 'initial', status: 'failed' },
      { id: 'repair-attempt', session_id: 'repair', role: 'repair', status: 'in_progress', launch_state: 'launched', recovery: { session_id: 'owner' } }],
    task_program_status: { parent_session_id: 'owner', program_id: 'old', state: 'blocked', blocker: { code: 'old', message: 'Old program error' }, jobs: [] },
  })
  const outcome = taskOutcome(task)
  assert.equal(outcome.repairSessionId, 'repair')
  assert.equal(outcome.integrationFailed, false)
  assert.equal(outcome.blocker, undefined)
  assert.equal(taskOutcome(JSON.parse(JSON.stringify(task))).repairSessionId, 'repair')
})

// Requirement: dismissing diagnostic text cannot hide actionable durable failure.
// Execute the production recovery renderer (not a duplicate UI) with fresh controllers
// to model remount/reload; user actions remain reachable until canonical resolution.
test('dismiss hides only diagnostics; reload restores recovery and durable repair link', { timeout: 5000 }, async () => {
  const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  const closure = source.slice(source.indexOf('const renderIntegrationRecovery'), source.indexOf('const launchIntegrationRepair'))
  const ts = await import('typescript')
  const compiled = ts.default.transpileModule(`${closure}; return renderIntegrationRecovery`, {
    fileName: 'task-outcome-renderer.tsx',
    compilerOptions: { target: ts.default.ScriptTarget.ES2020, jsx: ts.default.JsxEmit.React },
  }).outputText
  const task = mapBackendTask(raw)
  const project = { id: 'project', name: 'Project' }
  for (const dismiss of [true, false]) {
    const controller = createTaskIntegrationController()
    const operation = { phase: 'ready' as const }
    if (dismiss) controller.dismiss(taskIntegrationKey(project.id, task), taskIntegrationFailureIdentity(task, operation))
    const scope = { React, taskOutcome, integrationForTask: () => operation, integrationFailure, selectedProject: project,
      taskReopenOperations: createTaskReopenController(), taskReopenKey, taskIntegrationKey, taskIntegrationFailureIdentity,
      taskIntegrationOperations: controller, taskIntegrationPhase, repairUnavailable,
      handleReopenTask: () => {}, launchIntegrationRepair: () => {}, handleIntegrateTask: () => {},
      setActiveTaskId: () => {}, setSelectedTaskId: () => {}, setActiveSessionId: () => {}, setWorkerChatOpen: () => {},
    }
    const render = new Function(...Object.keys(scope), compiled)(...Object.values(scope))
    const html = renderToStaticMarkup(render(task))
    assert.match(html, /Integration failed/)
    assert.match(html, /Launch repair session/)
    assert.match(html, /Retry integration to refresh verified receipt/)
    assert.equal(html.includes('<pre>'), !dismiss)
    const repaired = { ...task, sessionId: 'repair', activeAttemptId: 'repair', attempts: [
      { id: 'repair', session_id: 'repair', role: 'repair', status: 'running', launch_state: 'launched', recovery: { session_id: 'owner' } },
    ] }
    const repairedHTML = renderToStaticMarkup(render(repaired))
    assert.match(repairedHTML, /Open repair session/)
    assert.doesNotMatch(repairedHTML, /Launch repair session/)
  }
})

// Requirement: taskOutcome must distinguish absent/background Git evidence from
// actionable facts. This pure projection test checks every transient state without
// caching a false zero or upgrading stale evidence into verified delivery.
test('checking delivery does not manufacture attention; errors and explicit work remain actionable', () => {
  const task = mapBackendTask({ ...raw, status: 'completed', integration: undefined })
  for (const gitStatus of [undefined, 'unknown', 'stale', 'clean']) {
    const outcome = taskOutcome({ ...task, gitStatus, isIntegrated: true })
    assert.equal(outcome.needsAttention, false)
    if (gitStatus !== 'clean') assert.match(outcome.delivery || '', /not verified/)
  }
  assert.equal(taskOutcome(task).needsAttention, false)
  assert.equal(taskOutcome({ ...task, gitStatus: 'unknown', syncWarning: 'Inspection failed' }).attentionReason, 'Git inspection failed')
  assert.equal(taskOutcome({ ...task, gitStatus: 'clean', isIntegrated: false }).needsAttention, false, 'no produced commits is not proof of pending integration')
  for (const gitStatus of ['diverged', 'stale']) {
    assert.equal(taskOutcome({ ...task, gitStatus, unintegratedCommits: 2 }).attentionReason, 'Unintegrated commits')
  }
  assert.equal(taskOutcome({ ...task, isDirty: true }).attentionReason, 'Changes pending commit')
  assert.equal(taskOutcome({ ...task, status: 'needs_review' }).attentionReason, 'Review requested')
  assert.equal(taskOutcome({ ...task, status: 'pending_approval' }).attentionReason, 'Approval requested')
  assert.equal(taskOutcome(mapBackendTask(raw)).attentionReason, 'Integration conflict')
})
