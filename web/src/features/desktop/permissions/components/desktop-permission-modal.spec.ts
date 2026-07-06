import React from 'react'
import test from 'node:test'
import assert from 'node:assert/strict'
import { renderToStaticMarkup } from 'react-dom/server'

import { DesktopPermissionModal, exitPlanExecutionArguments } from './desktop-permission-modal'
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

test('exit plan modal renders three clickable execution choices in deterministic order instead of select and checkbox controls', () => {
  const markup = renderPermission(exitPlanPermission(), 'plan')

  const automaticIndex = markup.indexOf('Automatic mode')
  const singleRunIndex = markup.indexOf('Single run')
  const manualIndex = markup.indexOf('Manual checkpoint review')
  assert(automaticIndex >= 0, 'expected automatic choice')
  assert(singleRunIndex > automaticIndex, 'expected single run after automatic')
  assert(manualIndex > singleRunIndex, 'expected manual checkpoint review after single run')
  assert.match(markup, /role="radiogroup"/)
  assert.match(markup, /role="radio"/)
  assert.doesNotMatch(markup, /<select/)
  assert.doesNotMatch(markup, /type="checkbox"/)
})

test('exit plan modal defaults to automatic mode without presenting an AI suggestion', () => {
  const markup = renderPermission(exitPlanPermission(), 'plan')

  assert.match(markup, /Run mode/)
  assert.match(markup, /Automatic mode is selected by default\./)
  assert.match(markup, /Choose a different run mode before approving if needed\./)
  assert.doesNotMatch(markup, /AI-suggested/)
  assert.doesNotMatch(markup, /AI suggested/)
  assert.doesNotMatch(markup, /Approval will send/)
  assert.doesNotMatch(markup, /execution_granularity=checkpointed/)
  assert.doesNotMatch(markup, /continuation_policy=automatic/)
  assert.match(markup, /aria-checked="true"/)
})

test('exit plan modal ignores top-level backend execution recommendation', () => {
  const markup = renderPermission(exitPlanPermission({}, {
    execution_granularity: 'run_through',
    continuation_policy: 'automatic',
    continue_automatically: true,
  }), 'plan')

  assert.match(markup, /Automatic mode is selected by default\./)
  assert.doesNotMatch(markup, /AI suggested/)
  assert.doesNotMatch(markup, /execution_granularity=run_through/)
  assert.doesNotMatch(markup, /continuation_policy=automatic/)
})

test('exit plan modal ignores approved argument execution controls when choosing the default', () => {
  const markup = renderPermission(exitPlanPermission({
    execution_granularity: 'run_through',
    continuation_policy: 'automatic',
    continue_automatically: true,
  }), 'plan')

  assert.match(markup, /Automatic mode is selected by default\./)
  assert.doesNotMatch(markup, /AI suggested/)
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

test('DesktopPermissionModal routes typed plan lifecycle approvals away from generic plan update modal', () => {
  const followup = renderPermission(planLifecyclePermission('plan_followup_request', {
    action: 'request_followup_checkpoint',
    title: 'Session checkpoint: audit',
    change_request: 'Add an audit note before final review.',
    checkpoint_title: 'Session checkpoint: audit',
    tasks: ['Add an audit note before final review.'],
    acceptance_criteria: ['The fresh checkpoint has enough context to run independently.'],
    notes: 'Relevant files and validation expectations for the fresh run.',
  }))
  assert.match(followup, /Session checkpoint: audit/)
  assert.match(followup, /ordered checkpoint to the active session chain/)
  assert.match(followup, /Handoff context/)
  assert.match(followup, /Relevant files and validation expectations/)
  assert.doesNotMatch(followup, /Plan update overview/)

  const revision = renderPermission(planLifecyclePermission('plan_revision_request', {
    action: 'request_plan_revision',
    title: 'Review revised plan',
    plan: '# Revised plan',
  }))
  assert.match(revision, /Review revised plan/)
  assert.match(revision, /Approve revision/)
  assert.doesNotMatch(revision, /Plan update overview/)

  const amendment = renderPermission(planLifecyclePermission('plan_amendment_request', {
    action: 'amend_plan',
    title: 'Review amendment',
    plan_id: 'plan-1',
    current_revision: 3,
    base_revision: 3,
    plan_amendment_delta: {
      reason: 'Replace future work',
      base_revision: 3,
      current_revision: 3,
      replace_from_checkpoint_id: 'cp-2',
      preserved_checkpoints: [{ id: 'cp-1', title: 'Completed setup', status: 'completed' }],
      replaced_checkpoints: [{ id: 'cp-2', title: 'Old future', status: 'pending' }],
      replacement_checkpoints: [{ id: 'cp-2', title: 'Deploy authority', status: 'pending' }],
      next_checkpoint: { id: 'cp-2', title: 'Deploy authority', status: 'pending' },
      bullets: ['cp-1 remains completed and preserved.', 'Replacing pending future work from cp-2 (Old future).', 'Next checkpoint becomes cp-2 (Deploy authority).', 'Reason: Replace future work'],
    },
    document: { id: 'plan-1', title: 'Review amendment', checkpoints: [{ id: 'cp-2', title: 'Deploy authority', status: 'pending' }] },
    approved_arguments: { action: 'amend_plan', plan_id: 'plan-1', base_revision: 3, override_stale: true, replace_from_checkpoint_id: 'cp-2' },
  }))
  assert.match(amendment, /Amendment delta/)
  assert.match(amendment, /Completed setup/)
  assert.match(amendment, /Deploy authority/)
  assert.match(amendment, /Reason: Replace future work/)
  assert.match(amendment, /Approve amendment/)
  assert.doesNotMatch(amendment, /Plan update overview/)

  const newPlan = renderPermission(planLifecyclePermission('plan_new_request', {
    action: 'request_new_plan',
    title: 'Review new plan',
    plan: '# New plan',
  }))
  assert.match(newPlan, /Review new plan/)
  assert.match(newPlan, /Approve new plan/)
  assert.doesNotMatch(newPlan, /Plan update overview/)
})
