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
        executionPolicy: { mode: 'automatic', shape: 'single_run' },
        executionState: { status: 'in_progress', activeAttemptId: 'attempt-1', parentSessionId: 'session-1', currentSessionId: 'session-1', currentRunId: 'run-1', lastCheckpointId: '', lastAttemptId: '', lastOutcome: '', startedAt: 1, updatedAt: 1, completedAt: 0 },
        checkpoints: [checkpoint],
        activeCheckpointId: 'cp-1',
        renderedText: '',
        displayText: '',
      },
    },
    activeCheckpoint: checkpoint,
    activeCheckpointId: 'cp-1',
    status: 'in_progress',
    policyMode: 'automatic',
    policyShape: 'single_run',
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

test('automatic mode sidebar actions card is informational with no manual controls', () => {
  const markup = renderToStaticMarkup(<DesktopPlanExecutionSidebar view={view()} onAction={() => undefined} onEditPlan={() => undefined} />)

  assert.match(markup, /Automatic mode on/)
  assert.match(markup, /Execution will continue until the plan completes/)
  assert.doesNotMatch(markup, /Continue checkpoint/)
  assert.doesNotMatch(markup, /Accept this checkpoint/)
  assert.doesNotMatch(markup, /Restart/)
  assert.doesNotMatch(markup, /role="switch"/)
})

test('manual review mode still exposes review controls', () => {
  const markup = renderToStaticMarkup(<DesktopPlanExecutionSidebar view={view({ policyMode: 'review_each_checkpoint', reviewRequired: true, status: 'waiting_review' })} onAction={() => undefined} onEditPlan={() => undefined} />)

  assert.match(markup, /Manual review mode/)
  assert.match(markup, /Accept this checkpoint/)
  assert.match(markup, /Accept &amp; move to next/)
})
