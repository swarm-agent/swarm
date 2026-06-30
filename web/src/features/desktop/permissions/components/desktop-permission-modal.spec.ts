import React from 'react'
import test from 'node:test'
import assert from 'node:assert/strict'
import { renderToStaticMarkup } from 'react-dom/server'

import { DesktopPermissionModal, exitPlanExecutionArguments, exitPlanExecutionChoiceFromApprovedArguments } from './desktop-permission-modal'
import type { DesktopPermissionRecord } from '../../types/realtime'

function exitPlanPermission(approvedArguments: Record<string, unknown> = {}, payloadOverrides: Record<string, unknown> = {}): DesktopPermissionRecord {
  return {
    id: 'perm-1',
    sessionId: 'session-1',
    runId: 'run-1',
    callId: 'call-1',
    toolName: 'exit_plan_mode',
    toolArguments: JSON.stringify({
      title: 'Plan: Modal test',
      plan_id: 'plan-1',
      plan: '# Plan: Modal test',
      document: {
        id: 'plan-1',
        title: 'Plan: Modal test',
        info: { goal: 'Test the modal' },
        checkpoints: [{ id: 'cp-1', title: 'Build it', status: 'pending' }],
      },
      approved_arguments: approvedArguments,
      ...payloadOverrides,
    }),
    status: 'pending',
    decision: '',
    reason: '',
    requirement: '',
    mode: 'plan',
    createdAt: 1,
    updatedAt: 1,
    resolvedAt: 0,
    permissionRequestedAt: 1,
  }
}

function planLifecyclePermission(requirement: string, toolArguments: Record<string, unknown>): DesktopPermissionRecord {
  return {
    id: `perm-${requirement}`,
    sessionId: 'session-1',
    runId: 'run-1',
    callId: 'call-1',
    toolName: 'plan_manage',
    toolArguments: JSON.stringify(toolArguments),
    status: 'pending',
    decision: '',
    reason: '',
    requirement,
    mode: 'auto',
    createdAt: 1,
    updatedAt: 1,
    resolvedAt: 0,
    permissionRequestedAt: 1,
  }
}

function renderPermission(permission: DesktopPermissionRecord, sessionMode = 'auto'): string {
  return renderToStaticMarkup(React.createElement(DesktopPermissionModal, {
    open: true,
    permission,
    pendingCount: 1,
    sessionMode,
    onOpenChange: () => undefined,
    onResolve: async () => undefined,
  }))
}

test('exit plan modal renders three clickable execution choices instead of select and checkbox controls', () => {
  const markup = renderPermission(exitPlanPermission(), 'plan')

  assert.match(markup, /Continue normally/)
  assert.match(markup, /Continue checkpoint by checkpoint/)
  assert.match(markup, /Automatic mode/)
  assert.match(markup, /role="radiogroup"/)
  assert.match(markup, /role="radio"/)
  assert.doesNotMatch(markup, /<select/)
  assert.doesNotMatch(markup, /type="checkbox"/)
})

test('exit plan modal defaults to checkpointed automatic and describes it as an editable AI suggestion', () => {
  const markup = renderPermission(exitPlanPermission(), 'plan')

  assert.match(markup, /AI-suggested run mode/)
  assert.match(markup, /AI suggested Automatic mode for this run\./)
  assert.match(markup, /Feel free to choose a different run mode before approving\./)
  assert.doesNotMatch(markup, /Approval will send/)
  assert.doesNotMatch(markup, /execution_granularity=checkpointed/)
  assert.doesNotMatch(markup, /continuation_policy=automatic/)
  assert.match(markup, /aria-checked="true"/)
})

test('exit plan modal initializes from top-level backend recommendation when AI chose one run', () => {
  const markup = renderPermission(exitPlanPermission({}, {
    execution_granularity: 'run_through',
    continuation_policy: 'automatic',
    continue_automatically: true,
  }), 'plan')

  assert.match(markup, /AI suggested Continue normally for this run\./)
  assert.doesNotMatch(markup, /execution_granularity=run_through/)
  assert.doesNotMatch(markup, /continuation_policy=automatic/)
})

test('exit plan modal falls back to approved arguments when top-level recommendation is absent', () => {
  const markup = renderPermission(exitPlanPermission({
    execution_granularity: 'run_through',
    continuation_policy: 'automatic',
    continue_automatically: true,
  }), 'plan')

  assert.match(markup, /AI suggested Continue normally for this run\./)
  assert.doesNotMatch(markup, /execution_granularity=run_through/)
  assert.doesNotMatch(markup, /continuation_policy=automatic/)
})

test('exit plan execution choices map to approved arguments', () => {
  assert.deepEqual(exitPlanExecutionArguments('run_through'), {
    execution_granularity: 'run_through',
    continue_automatically: true,
    continuation_policy: 'automatic',
  })

  assert.deepEqual(exitPlanExecutionArguments('checkpointed_manual'), {
    execution_granularity: 'checkpointed',
    continue_automatically: false,
    continuation_policy: 'review_each_checkpoint',
  })

  assert.deepEqual(exitPlanExecutionArguments('checkpointed_automatic'), {
    execution_granularity: 'checkpointed',
    continue_automatically: true,
    continuation_policy: 'automatic',
  })
})

test('exit plan execution choice parser respects approved arguments and defaults to automatic checkpointed', () => {
  assert.equal(exitPlanExecutionChoiceFromApprovedArguments({}), 'checkpointed_automatic')
  assert.equal(exitPlanExecutionChoiceFromApprovedArguments({ execution_granularity: 'run_through' }), 'run_through')
  assert.equal(exitPlanExecutionChoiceFromApprovedArguments({ execution_granularity: 'checkpointed', continuation_policy: 'review_each_checkpoint' }), 'checkpointed_manual')
  assert.equal(exitPlanExecutionChoiceFromApprovedArguments({ execution_granularity: 'checkpointed', continue_automatically: true }), 'checkpointed_automatic')
})

test('DesktopPermissionModal routes typed plan lifecycle approvals away from generic plan update modal', () => {
  const followup = renderPermission(planLifecyclePermission('plan_followup_request', {
    action: 'request_followup_checkpoint',
    title: 'Follow-up: audit',
    change_request: 'Add an audit note before final review.',
    checkpoint_title: 'Follow-up: audit',
    tasks: ['Add an audit note before final review.'],
  }))
  assert.match(followup, /Follow-up: audit/)
  assert.match(followup, /append-only/)
  assert.doesNotMatch(followup, /Plan update overview/)

  const revision = renderPermission(planLifecyclePermission('plan_revision_request', {
    action: 'request_plan_revision',
    title: 'Review revised plan',
    plan: '# Revised plan',
  }))
  assert.match(revision, /Review revised plan/)
  assert.match(revision, /Approve revision/)
  assert.doesNotMatch(revision, /Plan update overview/)

  const newPlan = renderPermission(planLifecyclePermission('plan_new_request', {
    action: 'request_new_plan',
    title: 'Review new plan',
    plan: '# New plan',
  }))
  assert.match(newPlan, /Review new plan/)
  assert.match(newPlan, /Approve new plan/)
  assert.doesNotMatch(newPlan, /Plan update overview/)
})
