import React from 'react'
import test from 'node:test'
import assert from 'node:assert/strict'
import { renderToStaticMarkup } from 'react-dom/server'

import { DesktopPlanExecutionSidebar } from './desktop-plan-execution-sidebar'
import type { DesktopPlanExecutionView } from '../../state/desktop-v3-cache-selectors'

function view(overrides: Partial<DesktopPlanExecutionView> = {}): DesktopPlanExecutionView {
  const checkpoint = {
    id: 'cp-1',
    title: 'Build UI',
    status: 'in_progress',
    objective: '',
    tasks: [],
    acceptanceCriteria: [],
    notes: '',
    report: '',
    result: '',
    changedFiles: [],
    validation: [],
    attemptId: 'attempt-1',
    runId: 'run-1',
    sessionId: 'session-1',
    startedAt: 1,
    completedAt: 0,
    review: null,
    attempts: [],
    order: 1,
  }
  const checkpoints = [checkpoint]
  return {
    plan: {
      id: 'plan-1',
      title: 'Plan',
      plan: '# Plan',
      status: 'approved',
      approvalState: 'approved',
      updatedAt: 1,
      document: {
        id: 'plan-1',
        title: 'Plan',
        status: 'approved',
        schemaVersion: '',
        revisionId: '',
        info: { goal: '', scope: '', context: '', decisions: [], constraints: [], assumptions: [], openQuestions: [], relevantFiles: [], successCriteria: [], validationStrategy: '' },
        executionPolicy: { mode: 'automatic', shape: 'checkpointed' },
        executionState: { status: 'in_progress', activeAttemptId: 'attempt-1', parentSessionId: 'session-1', currentSessionId: 'session-1', currentRunId: 'run-1', lastCheckpointId: '', lastAttemptId: '', lastOutcome: '', startedAt: 1, updatedAt: 1, completedAt: 0 },
        checkpoints,
        activeCheckpointId: 'cp-1',
        renderedText: '',
        displayText: '',
      },
    },
    activeCheckpoint: checkpoint,
    activeCheckpointId: 'cp-1',
    status: 'in_progress',
    policyMode: 'automatic',
    policyShape: 'checkpointed',
    currentRunId: 'run-1',
    currentSessionId: 'session-1',
    freshContext: true,
    reviewRequired: false,
    blocked: false,
    failed: false,
    completed: false,
    attemptCount: 0,
    ...overrides,
  }
}

test('normal run-through sidebar shows the plan title instead of the synthetic checkpoint', () => {
  const base = view({ policyShape: 'single_run' })
  base.plan.title = 'Plan: ship sidebar fix'
  base.plan.document.title = 'Plan: ship sidebar fix'
  base.plan.document.executionPolicy = { mode: 'automatic', shape: 'single_run' }
  base.plan.document.activeCheckpointId = 'plan-run'
  base.activeCheckpointId = 'plan-run'
  base.activeCheckpoint = {
    ...base.activeCheckpoint!,
    id: 'plan-run',
    title: 'Run approved plan',
  }
  base.plan.document.checkpoints = [base.activeCheckpoint]

  const markup = renderToStaticMarkup(<DesktopPlanExecutionSidebar view={base} onAction={() => undefined} onEditPlan={() => undefined} />)

  assert.match(markup, /Plan execution/)
  assert.match(markup, /Plan: ship sidebar fix/)
  assert.match(markup, /Continue normally/)
  assert.match(markup, /Running the approved plan normally/)
  assert.doesNotMatch(markup, /Active checkpoint/)
  assert.doesNotMatch(markup, /plan-run/)
  assert.doesNotMatch(markup, /Run approved plan/)
  assert.doesNotMatch(markup, /No remaining checkpoint/)
})

test('automatic checkpointed mode sidebar actions card is informational with no manual controls', () => {
  const markup = renderToStaticMarkup(<DesktopPlanExecutionSidebar view={view()} onAction={() => undefined} onEditPlan={() => undefined} />)

  assert.match(markup, /Automatic mode on/)
  assert.match(markup, /Execution will continue until the plan completes/)
  assert.doesNotMatch(markup, /Continue checkpoint/)
  assert.doesNotMatch(markup, /Accept this checkpoint/)
  assert.doesNotMatch(markup, /Restart/)
  assert.doesNotMatch(markup, /role="switch"/)
})

test('manual review mode keeps the start-next review button visible but disabled when more checkpoints remain', () => {
  const base = view({ policyMode: 'review_each_checkpoint', reviewRequired: true, status: 'waiting_review' })
  base.plan.document.checkpoints.push({
    ...base.plan.document.checkpoints[0],
    id: 'cp-2',
    title: 'Follow-up',
    status: 'pending',
    attemptId: '',
    runId: '',
    sessionId: '',
    startedAt: 0,
  })
  const markup = renderToStaticMarkup(<DesktopPlanExecutionSidebar view={base} onAction={() => undefined} onEditPlan={() => undefined} />)

  assert.match(markup, /Manual review mode/)
  assert.match(markup, /Accept &amp; start next checkpoint/)
  assert.match(markup, /Manual accept-and-continue is disabled/)
  assert.match(markup, /disabled="">Accept &amp; start next checkpoint/)
  assert.doesNotMatch(markup, /Accept this checkpoint/)
  assert.doesNotMatch(markup, /Accept &amp; move to next/)
})

test('manual review mode exposes a finish-plan review action on the final checkpoint', () => {
  const markup = renderToStaticMarkup(<DesktopPlanExecutionSidebar view={view({ policyMode: 'review_each_checkpoint', reviewRequired: true, status: 'waiting_review' })} onAction={() => undefined} onEditPlan={() => undefined} />)

  assert.match(markup, /Manual review mode/)
  assert.match(markup, /Accept &amp; finish plan/)
  assert.match(markup, /marks the plan complete/)
  assert.doesNotMatch(markup, /Accept this checkpoint/)
  assert.doesNotMatch(markup, /Accept &amp; move to next/)
})
