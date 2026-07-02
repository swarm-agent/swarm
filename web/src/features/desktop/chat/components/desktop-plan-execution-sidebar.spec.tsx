import React from 'react'
import type { ReactElement, ReactNode } from 'react'
import test from 'node:test'
import assert from 'node:assert/strict'
import { renderToStaticMarkup } from 'react-dom/server'

import { DesktopPlanExecutionSidebar } from './desktop-plan-execution-sidebar'
import type { DesktopPlanExecutionSidebarActionInput } from './desktop-plan-execution-sidebar'
import type { DesktopPlanExecutionView } from '../../state/desktop-v3-cache-selectors'

type HostElement = ReactElement<{
  children?: ReactNode
  disabled?: boolean
  onClick?: (event?: unknown) => void
}, string>

function textContent(node: ReactNode): string {
  if (node === null || node === undefined || typeof node === 'boolean') return ''
  if (typeof node === 'string' || typeof node === 'number' || typeof node === 'bigint') return String(node)
  if (Array.isArray(node)) return node.map(textContent).join('')
  if (React.isValidElement(node)) return textContent((node.props as { children?: ReactNode }).children)
  return ''
}

function collectHostElements(node: ReactNode, elements: HostElement[] = []): HostElement[] {
  if (node === null || node === undefined || typeof node === 'boolean') return elements
  if (Array.isArray(node)) {
    for (const child of node) collectHostElements(child, elements)
    return elements
  }
  if (!React.isValidElement(node)) return elements

  const element = node as ReactElement<{ children?: ReactNode }>
  const elementType = element.type as unknown
  if (typeof elementType === 'function') return collectHostElements(elementType(element.props), elements)
  if (typeof elementType === 'object' && elementType && '$$typeof' in elementType && (elementType as { $$typeof?: symbol }).$$typeof === Symbol.for('react.memo')) {
    const memoType = (elementType as { type?: unknown }).type
    if (typeof memoType === 'function') return collectHostElements(memoType(element.props), elements)
  }

  if (typeof element.type === 'string') elements.push(element as HostElement)
  collectHostElements(element.props.children, elements)
  return elements
}

function findSidebarButton(element: ReactElement, label: string): HostElement {
  const button = collectHostElements(element).find((candidate) => candidate.type === 'button' && textContent(candidate.props.children).replace(/\s+/g, ' ').trim() === label)
  assert.ok(button, `expected ${label} button`)
  return button
}

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
  assert.match(markup, /open \/plan and restore a plan revision/)
  assert.match(markup, /Run-through recovery is managed from \/plan revision restore/)
  assert.match(markup, /Archive plan/)
  assert.match(markup, /Archive this plan when you no longer need the chat in your active workspace/)
  assert.doesNotMatch(markup, /Active checkpoint/)
  assert.doesNotMatch(markup, /plan-run/)
  assert.doesNotMatch(markup, /Run approved plan/)
  assert.doesNotMatch(markup, /No remaining checkpoint/)
})

test('automatic checkpointed mode sidebar actions card explains continuation and exposes backend policy toggle', () => {
  const markup = renderToStaticMarkup(<DesktopPlanExecutionSidebar view={view()} onAction={() => undefined} onEditPlan={() => undefined} />)

  assert.match(markup, /Automatic mode on/)
  assert.match(markup, /Backend policy is automatic/)
  assert.match(markup, /Switch to checkpoint-by-checkpoint/)
  assert.match(markup, /next checkpoint completion pauses for review/)
  assert.match(markup, /Archive plan/)
  assert.match(markup, /Archive this plan when you no longer need the chat in your active workspace/)
  assert.doesNotMatch(markup, /Continue checkpoint/)
  assert.doesNotMatch(markup, /Accept this checkpoint/)
  assert.doesNotMatch(markup, /Restart/)
  assert.doesNotMatch(markup, /Follow-ups/)
  assert.doesNotMatch(markup, /Using default/)
  assert.doesNotMatch(markup, /Current setting:/)
  assert.doesNotMatch(markup, /Auto-add .* start \(Default\)/)
  assert.doesNotMatch(markup, /Ask first/)
  assert.doesNotMatch(markup, /Save as default/)
  assert.doesNotMatch(markup, /Inherit global default/)
  assert.doesNotMatch(markup, /Auto-add only/)
  assert.doesNotMatch(markup, /role="switch"/)
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
  assert.match(markup, /Backend policy is checkpoint-by-checkpoint/)
  assert.match(markup, /Switch to automatic/)
  assert.match(markup, /next checkpoint completion can auto-start/)
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
  assert.match(markup, /Backend policy is checkpoint-by-checkpoint/)
  assert.match(markup, /Accept &amp; archive plan/)
  assert.match(markup, /moves the completed plan to Archived without running checkpoint acceptance first/)
  assert.match(markup, /Switch to automatic/)
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

test('final accept-and-archive review action dispatches archive without checkpoint acceptance', () => {
  const actions: DesktopPlanExecutionSidebarActionInput[] = []
  const button = findSidebarButton(
    <DesktopPlanExecutionSidebar
      view={view({ policyMode: 'review_each_checkpoint', reviewRequired: true, status: 'waiting_review' })}
      onAction={(input) => { actions.push(input) }}
      onEditPlan={() => undefined}
    />,
    'Accept & archive plan',
  )

  assert.equal(button.props.disabled, false)
  button.props.onClick?.()

  assert.deepEqual(actions, [{ action: 'archive_plan' }])
})

test('automatic mode toggle dispatches checkpointed policy action', () => {
  const actions: DesktopPlanExecutionSidebarActionInput[] = []
  const button = findSidebarButton(
    <DesktopPlanExecutionSidebar
      view={view()}
      onAction={(input) => { actions.push(input) }}
      onEditPlan={() => undefined}
    />,
    'Switch to checkpoint-by-checkpoint',
  )

  assert.equal(button.props.disabled, false)
  button.props.onClick?.()

  assert.deepEqual(actions, [{ action: 'resume_checkpointed' }])
})

test('checkpoint-by-checkpoint mode toggle dispatches automatic policy action', () => {
  const actions: DesktopPlanExecutionSidebarActionInput[] = []
  const button = findSidebarButton(
    <DesktopPlanExecutionSidebar
      view={view({ policyMode: 'review_each_checkpoint', reviewRequired: true, status: 'waiting_review' })}
      onAction={(input) => { actions.push(input) }}
      onEditPlan={() => undefined}
    />,
    'Switch to automatic',
  )

  assert.equal(button.props.disabled, false)
  button.props.onClick?.()

  assert.deepEqual(actions, [{ action: 'resume_automatic' }])
})

test('non-final review action dispatches checkpoint acceptance to start the next checkpoint', () => {
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
  const actions: DesktopPlanExecutionSidebarActionInput[] = []
  const button = findSidebarButton(
    <DesktopPlanExecutionSidebar
      view={base}
      onAction={(input) => { actions.push(input) }}
      onEditPlan={() => undefined}
    />,
    'Accept & start next checkpoint',
  )

  assert.equal(button.props.disabled, false)
  button.props.onClick?.()

  assert.deepEqual(actions, [{ action: 'accept_checkpoint', checkpointId: 'cp-1' }])
})
