import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import ts from 'typescript'
import { integrationFailure, repairUnavailable } from './integration-recovery'
import { createTaskIntegrationController, taskIntegrationFailureIdentity, taskIntegrationKey, taskIntegrationPhase } from './task-integration-operation'
import type { ProjectSummary, RunningTask } from './orchestrate-types'

// Purpose: acknowledging an integration/repair alert is presentation-only. The
// regression is a retained failure recreating the panel after dismissal, or an
// acknowledgment hiding another lane/new failure. Controller tests plus the actual
// renderIntegrationRecovery closure are the narrowest layer proving visibility,
// retained history/diagnostics, and button propagation without a daemon or E2E.
const project = { id: 'project-a', name: 'Project A' } as ProjectSummary
const task = {
  id: 'task-a', title: 'Task A', sessionId: 'session-a', activeAttemptId: 'attempt-a',
  sourceWorkspaceId: 'workspace-a', sourceWorkspacePath: '/repo',
  worktreeBranch: 'agent/a', baseBranch: 'dev', status: 'needs_review', isIntegrated: false,
  revision: 5, integration: { state: 'conflict', error: 'Retained conflict', operation_id: 'operation-a', attempt_id: 'attempt-a' },
} as RunningTask

function renderer(controller: ReturnType<typeof createTaskIntegrationController>, selectedProject = project) {
  const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  const closure = source.slice(source.indexOf('const renderIntegrationRecovery ='), source.indexOf('const launchIntegrationRepair ='))
  const compiled = ts.transpileModule(`${closure}; return renderIntegrationRecovery`, {
    fileName: 'integration-recovery.tsx',
    compilerOptions: { target: ts.ScriptTarget.ES2020, jsx: ts.JsxEmit.React },
  }).outputText
  const scope = {
    React, selectedProject, integrationFailure, repairUnavailable,
    taskIntegrationKey, taskIntegrationFailureIdentity, taskIntegrationOperations: controller,
    integrationForTask: (row: RunningTask) => controller.get(taskIntegrationKey(selectedProject.id, row)),
    TaskAttemptHistory: () => React.createElement('section', { 'aria-label': 'Task session history' }, React.createElement('button', { type: 'button' }, 'View task history')),
    repairStates: {}, handleReopenTask: () => {}, launchIntegrationRepair: () => {}, handleIntegrateTask: () => {},
    setSelectedTaskId: () => {}, setActiveTaskId: () => {}, setActiveSessionId: () => {}, setWorkerChatOpen: () => {},
  }
  return new Function(...Object.keys(scope), compiled)(...Object.values(scope)) as (row: RunningTask) => React.ReactElement
}

function dismissButton(element: React.ReactNode): React.ReactElement<{ onClick: (event: { stopPropagation: () => void }) => void }> | undefined {
  if (!React.isValidElement<{ children?: React.ReactNode; 'aria-label'?: string }>(element)) return
  if (element.props['aria-label'] === 'Dismiss integration error') return element as ReturnType<typeof dismissButton>
  for (const child of React.Children.toArray(element.props.children)) {
    const found = dismissButton(child)
    if (found) return found
  }
}

function acknowledge(render: ReturnType<typeof renderer>, row: RunningTask) {
  const button = dismissButton(render(row))
  assert.ok(button, 'visible alert has an accessible dismiss button')
  let stopped = false
  button.props.onClick({ stopPropagation: () => { stopped = true } })
  assert.equal(stopped, true, 'dismiss never selects the containing card')
}

function assertHidden(render: ReturnType<typeof renderer>, row: RunningTask) {
  const html = renderToStaticMarkup(render(row))
  assert.doesNotMatch(html, /class="integration-recovery"|Integration failed|Dismiss integration error/)
  assert.match(html, /View task history/, 'history remains outside the dismissed alert')
}

function assertVisible(render: ReturnType<typeof renderer>, row: RunningTask) {
  const html = renderToStaticMarkup(render(row))
  assert.match(html, /class="integration-recovery" role="alert"/)
  assert.match(html, /Dismiss integration error/)
  assert.match(html, /Retry integration to refresh verified receipt/)
  assert.match(html, /Launch repair session/)
}

test('retained receipt dismissal removes the whole panel across rerenders, refresh and view recreation', () => {
  const controller = createTaskIntegrationController()
  const render = renderer(controller)
  const before = JSON.stringify(task)
  let notifications = 0
  controller.subscribe(() => { notifications++ })
  assertVisible(render, task)
  acknowledge(render, task)
  assert.equal(notifications, 1, 'ready-phase retained failures still notify the view')
  assertHidden(render, task)
  const refreshed = { ...task, revision: 19, title: 'Updated title', integration: { ...task.integration! } }
  assertHidden(render, refreshed)
  assertHidden(renderer(controller), refreshed)
  assert.equal(JSON.stringify(task), before, 'dismiss must not alter durable task state or diagnostics')
  assert.equal(taskIntegrationPhase(task, controller.get(taskIntegrationKey(project.id, task))), 'error', 'retry/error state is not success')
})

test('new integration operation, changed diagnostic, task, project and attempt resurface independently', () => {
  const controller = createTaskIntegrationController()
  const render = renderer(controller)
  acknowledge(render, task)
  assertVisible(render, { ...task, integration: { ...task.integration!, operation_id: 'operation-b' } })
  assertVisible(render, { ...task, integration: { ...task.integration!, error: 'New conflict' } })
  assertVisible(render, { ...task, id: 'task-b' })
  assertVisible(renderer(controller, { ...project, id: 'project-b' }), task)
  assertVisible(render, { ...task, activeAttemptId: 'attempt-b' })
  assertHidden(render, task)
})

test('retained repair launch failure dismisses without losing history or the incomplete follow-up retry', () => {
  const controller = createTaskIntegrationController()
  const render = renderer(controller)
  const repairTask = { ...task, activeAttemptId: 'repair-a', attempts: [{
    id: 'repair-a', session_id: 'repair-session', role: 'repair', status: 'failed',
    launch_state: 'launch_failed', recovery: { session_id: 'repair-session' },
    client_request_id: 'repair-request', request_revision: 5, request: 'Repair conflict', last_error: 'Launch unavailable',
  }] } as RunningTask
  acknowledge(render, repairTask)
  assertHidden(render, { ...repairTask, revision: 20, attempts: repairTask.attempts!.map(attempt => ({ ...attempt })) })
  assert.match(renderToStaticMarkup(render(repairTask)), /Retry incomplete follow-up/)
  const nextAttempt = { ...repairTask.attempts![0], id: 'repair-b', client_request_id: 'repair-request-b' }
  assertVisible(render, { ...repairTask, activeAttemptId: nextAttempt.id, attempts: [nextAttempt] })
  assertHidden(render, repairTask)
})

test('transient failure dismissal preserves diagnostics and backend fallback; identical-error retry resurfaces', { timeout: 5000 }, async () => {
  const controller = createTaskIntegrationController()
  const render = renderer(controller)
  const fail = async () => { throw new Error('Transport conflict') }
  await controller.run(project, task, fail, () => { assert.fail('failure cannot refresh as success') })
  const key = taskIntegrationKey(project.id, task)
  const operation = controller.get(key)
  acknowledge(render, task)
  assertHidden(render, task)
  assert.equal(controller.get(key), operation, 'dismiss keeps the complete local failure receipt')
  assertHidden(render, { ...task, revision: 21, integration: { ...task.integration! } })
  await controller.run(project, task, fail, () => {})
  assertVisible(render, task)
  assert.notEqual(controller.get(key), operation, 'explicit retry has a new failure generation even with identical text')
  const transientOnly = { ...task, id: 'transient-only', integration: undefined }
  await controller.run(project, transientOnly, fail, () => {})
  acknowledge(render, transientOnly)
  assertHidden(render, transientOnly)
})

// Purpose: a repair alert can coexist with an in-flight integration. Dismissing
// that visible alert must not cancel/unlock the mutation or hide its later failure.
test('dismissal during a pending integration leaves the lock intact and its new failure visible', { timeout: 5000 }, async () => {
  const controller = createTaskIntegrationController()
  const render = renderer(controller)
  const repairTask = { ...task, attempts: [{
    id: task.activeAttemptId!, session_id: 'repair-session', role: 'repair', status: 'failed',
    launch_state: 'launch_failed', recovery: { session_id: 'repair-session' }, last_error: 'Launch unavailable',
  }] } as RunningTask
  let reject!: (error: Error) => void
  let calls = 0
  const mutate = () => { calls++; return new Promise<never>((_, no) => { reject = no }) }
  const pending = controller.run(project, repairTask, mutate, () => {})
  acknowledge(render, repairTask)
  assertHidden(render, repairTask)
  assert.equal(controller.get(taskIntegrationKey(project.id, repairTask)).phase, 'pending')
  await controller.run(project, repairTask, mutate, () => {})
  assert.equal(calls, 1, 'dismissal cannot admit duplicate mutations')
  reject(new Error('New integration failure'))
  await pending
  assertVisible(render, repairTask)
})
