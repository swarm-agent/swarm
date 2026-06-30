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
        executionPolicy: { mode: 'automatic', shape: 'checkpointed', followupCheckpointPolicy: '' },
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
  base.plan.document.executionPolicy = { mode: 'automatic', shape: 'single_run', followupCheckpointPolicy: '' }
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
  assert.match(markup, /Archive plan/)
  assert.match(markup, /Archive this plan when you no longer need the chat in your active workspace/)
  assert.doesNotMatch(markup, /Active checkpoint/)
  assert.doesNotMatch(markup, /plan-run/)
  assert.doesNotMatch(markup, /Run approved plan/)
  assert.doesNotMatch(markup, /No remaining checkpoint/)
})

test('automatic checkpointed mode sidebar actions card explains continuation and exposes archive', () => {
  const markup = renderToStaticMarkup(<DesktopPlanExecutionSidebar view={view()} onAction={() => undefined} onEditPlan={() => undefined} />)

  assert.match(markup, /Automatic mode on/)
  assert.match(markup, /Execution will continue automatically through checkpoint, pause the chat at any time to change agent settings and continue/)
  assert.match(markup, /Archive plan/)
  assert.match(markup, /Archive this plan when you no longer need the chat in your active workspace/)
  assert.doesNotMatch(markup, /Continue checkpoint/)
  assert.doesNotMatch(markup, /Accept this checkpoint/)
  assert.doesNotMatch(markup, /Restart/)
  assert.match(markup, /Using default/)
  assert.match(markup, /Current setting:/)
  assert.match(markup, /Ask \(Default\)/)
  assert.match(markup, /Auto-add .* start/)
  assert.doesNotMatch(markup, /Use default/)
  assert.doesNotMatch(markup, /disabled=""[^>]*>Use Ask \(Default\)/)
  assert.doesNotMatch(markup, /Inherit global default/)
  assert.doesNotMatch(markup, /Auto-add only/)
  assert.doesNotMatch(markup, /role="switch"/)
})

test('plan override different from default exposes concrete override and return-to-default choices', () => {
  const base = view()
  base.plan.document.executionPolicy.followupCheckpointPolicy = 'auto_start'

  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar
      view={base}
      followupCheckpointPolicyDefault="require_approval"
      onAction={() => undefined}
      onEditPlan={() => undefined}
    />,
  )

  assert.match(markup, /Current plan override/)
  assert.match(markup, /Current setting:/)
  assert.match(markup, /Auto-add .* start \(Override\)/)
  assert.match(markup, /Ask \(Default\)/)
  assert.match(markup, /Change Default/)
  assert.match(markup, /Use Ask \(Default\)/)
  assert.doesNotMatch(markup, /Override Default/)
  assert.doesNotMatch(markup, /Make Auto-add .* start the global default and clear this plan override/)
  assert.doesNotMatch(markup, /Use Auto-add .* start only for this plan and keep the global default unchanged/)
  assert.doesNotMatch(markup, /Inherit global default/)
})

test('sidebar renders a non-Ask backend follow-up default after refresh', () => {
  const base = view()
  base.plan.document.executionPolicy.followupCheckpointPolicy = ''

  const markup = renderToStaticMarkup(
    <DesktopPlanExecutionSidebar
      view={base}
      followupCheckpointPolicyDefault="auto_start"
      onAction={() => undefined}
      onEditPlan={() => undefined}
    />,
  )

  assert.match(markup, /Using default/)
  assert.match(markup, /Current setting:/)
  assert.match(markup, /Auto-add .* start \(Default\)/)
  assert.doesNotMatch(markup, /Ask \(Default\)/)
  assert.doesNotMatch(markup, /Change Default/)
  assert.doesNotMatch(markup, /\(Override\)/)
  assert.doesNotMatch(markup, /Use Auto-add .* start \(Default\)/)
})

test('manual review mode keeps the start-next review button visible and enabled when more checkpoints remain', () => {
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

  assert.match(markup, /Actions/)
  assert.match(markup, /Review Mode/)
  assert.match(markup, /Accept &amp; start next checkpoint/)
  assert.match(markup, /Accepting review starts the next checkpoint/)
  assert.match(markup, /ask the AI to add or adjust checkpoints/)
  assert.match(markup, /Archive plan/)
  assert.match(markup, /Move this plan to Archived without starting another checkpoint/)
  assert.doesNotMatch(markup, /Actions \/ Plan Mode/)
  assert.doesNotMatch(markup, /Manual review mode/)
  assert.doesNotMatch(markup, /Restart/)
  assert.doesNotMatch(markup, /Edit plan/)
  assert.doesNotMatch(markup, /disabled="">Accept &amp; start next checkpoint/)
  assert.doesNotMatch(markup, /Accept this checkpoint/)
  assert.doesNotMatch(markup, /Accept &amp; move to next/)
})

test('manual review mode exposes only the enabled accept-and-archive review action on the final checkpoint', () => {
  const markup = renderToStaticMarkup(<DesktopPlanExecutionSidebar view={view({ policyMode: 'review_each_checkpoint', reviewRequired: true, status: 'waiting_review' })} onAction={() => undefined} onEditPlan={() => undefined} />)

  assert.match(markup, /Review Mode/)
  assert.match(markup, /You can keep chatting and ask the AI to continue work or add checkpoints/)
  assert.match(markup, /Accept &amp; archive plan/)
  assert.match(markup, /moves the completed plan to Archived/)
  assert.match(markup, /Accept and archive the completed plan when you’re done/)
  assert.doesNotMatch(markup, /Archive plan<\/button>/)
  assert.doesNotMatch(markup, /Move this plan to Archived without starting another checkpoint/)
  assert.doesNotMatch(markup, /disabled="">Accept &amp; archive plan/)
  assert.doesNotMatch(markup, /Manual review mode/)
  assert.doesNotMatch(markup, /Restart/)
  assert.doesNotMatch(markup, /Edit plan/)
  assert.doesNotMatch(markup, /Accept this checkpoint/)
  assert.doesNotMatch(markup, /Accept &amp; move to next/)
})

test('manual review mode keeps finish-plan action clickable when all checkpoints are complete but review is still pending', () => {
  const base = view({ policyMode: 'review_each_checkpoint', reviewRequired: true, status: 'waiting_review', completed: true })
  base.activeCheckpoint = { ...base.activeCheckpoint!, status: 'completed', review: { status: 'pending', reviewerId: '', reviewerType: '', result: '', notes: '', reviewedAt: 0 } }
  base.plan.document.checkpoints = [base.activeCheckpoint]

  const markup = renderToStaticMarkup(<DesktopPlanExecutionSidebar view={base} onAction={() => undefined} onEditPlan={() => undefined} />)

  assert.match(markup, /Accept &amp; archive plan/)
  assert.doesNotMatch(markup, /disabled="">Accept &amp; archive plan/)
})
