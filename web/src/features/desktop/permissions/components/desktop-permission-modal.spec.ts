import React from 'react'
import test from 'node:test'
import assert from 'node:assert/strict'
import { renderToStaticMarkup } from 'react-dom/server'

import { DesktopPermissionModal, exitPlanExecutionArguments, newPlanLifecycleApprovedArguments } from './desktop-permission-modal'
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

  const automaticIndex = markup.indexOf('Automatic checkpointed')
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
  assert.match(markup, /Automatic checkpointed is selected by default\./)
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

  assert.match(markup, /Automatic checkpointed is selected by default\./)
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

  assert.match(markup, /Automatic checkpointed is selected by default\./)
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

test('exit plan and new plan approval modals use the same non-split plan layout order', () => {
  const exitPlan = renderPermission(exitPlanPermission(), 'plan')
  const exitRunModeIndex = exitPlan.indexOf('Run mode')
  const exitDetailsIndex = exitPlan.indexOf('Plan details')
  const exitCheckpointsIndex = exitPlan.indexOf('Checkpoints')
  assert(exitRunModeIndex >= 0, 'expected exit plan run mode selector')
  assert(exitDetailsIndex > exitRunModeIndex, 'expected exit plan details below run mode')
  assert(exitCheckpointsIndex > exitDetailsIndex, 'expected exit plan checkpoints below plan details')
  assert.doesNotMatch(exitPlan, /min-\[901px\]:grid-cols/)
  assert.doesNotMatch(exitPlan, /min-\[901px\]:border-l/)

  const newPlan = renderPermission(planLifecyclePermission('plan_new_request', {
    action: 'request_new_plan',
    title: 'Review shared plan look',
    update_summary: 'Use the exit plan approval look for auto-mode plan proposals.',
    document: {
      title: 'Review shared plan look',
      info: { goal: 'Display structured content', decisions: ['Expose full structured plan before approval'] },
      checkpoints: [{
        id: 'cp-new',
        title: 'Structured checkpoint',
        status: 'pending',
        tasks: ['Render checkpoint task'],
        acceptance_criteria: ['Checkpoint content is visible before approval'],
      }],
    },
  }))
  const newRunModeIndex = newPlan.indexOf('Run mode')
  const lifecycleIndex = newPlan.indexOf('Lifecycle action')
  const newDetailsIndex = newPlan.indexOf('Plan details')
  const newCheckpointsIndex = newPlan.indexOf('Checkpoints')
  assert(newRunModeIndex >= 0, 'expected new plan run mode selector')
  assert(lifecycleIndex > newRunModeIndex, 'expected lifecycle context below run mode')
  assert(newDetailsIndex > lifecycleIndex, 'expected plan details below lifecycle context')
  assert(newCheckpointsIndex > newDetailsIndex, 'expected checkpoints below plan details')
  assert.match(newPlan, /rounded-3xl/)
  assert.match(newPlan, /Copy/)
  assert.doesNotMatch(newPlan, /min-\[901px\]:grid-cols/)
  assert.doesNotMatch(newPlan, /min-\[901px\]:border-l/)
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
    document: {
      title: 'Review new plan',
      info: { goal: 'Display structured content', decisions: ['Expose full structured plan before approval'] },
      checkpoints: [{
        id: 'cp-new',
        title: 'Structured checkpoint',
        status: 'pending',
        tasks: ['Render checkpoint task'],
        acceptance_criteria: ['Checkpoint content is visible before approval'],
      }],
    },
  }))
  assert.match(newPlan, /Review new plan/)
  const automaticIndex = newPlan.indexOf('Automatic checkpointed')
  const singleRunIndex = newPlan.indexOf('Single run')
  const manualIndex = newPlan.indexOf('Manual checkpoint review')
  assert.match(newPlan, /Structured checkpoint/)
  assert.match(newPlan, /Display structured content/)
  assert.match(newPlan, /Expose full structured plan before approval/)
  assert.match(newPlan, /Render checkpoint task/)
  assert.match(newPlan, /Checkpoint content is visible before approval/)
  assert.match(newPlan, /Approve new plan/)
  assert.match(newPlan, /Run mode/)
  assert(automaticIndex >= 0, 'expected automatic checkpointed choice')
  assert(singleRunIndex > automaticIndex, 'expected single run after automatic checkpointed')
  assert(manualIndex > singleRunIndex, 'expected manual checkpoint review after single run')
  assert.match(newPlan, /Automatic checkpointed is selected by default\./)
  assert.match(newPlan, /aria-checked="true"/)
  assert.doesNotMatch(newPlan, /No proposed new plan document or plan text was provided/)
  assert.doesNotMatch(newPlan, /Plan update overview/)
})

test('new plan lifecycle approval merges automatic checkpointed execution controls with payload approved arguments', () => {
  assert.deepEqual(
    newPlanLifecycleApprovedArguments({
      action: 'request_new_plan',
      planId: 'plan-1',
      title: 'Review new plan',
      plan: '# New plan',
      document: null,
      changeRequest: '',
      checkpointTitle: '',
      tasks: [],
      acceptanceCriteria: [],
      notes: '',
      updateSummary: '',
      updateScope: '',
      updateKind: '',
      checkpoint: false,
      followupCheckpointPolicy: '',
      policyEffective: '',
      approvalRequired: true,
      runQueued: false,
      revision: 0,
      baseRevision: 0,
      currentRevision: 0,
      planAmendmentDelta: null,
      approvedArguments: {
        action: 'request_new_plan',
        plan_id: 'plan-1',
        title: 'Review new plan',
        execution_granularity: 'run_through',
        continuation_policy: 'automatic',
        continue_automatically: true,
      },
      diffLines: [],
      priorPlan: '',
      priorTitle: '',
      priorDocument: null,
    }, 'checkpointed_automatic'),
    {
      action: 'request_new_plan',
      plan_id: 'plan-1',
      title: 'Review new plan',
      approval_confirmed: true,
      execution_granularity: 'checkpointed',
      continuation_policy: 'automatic',
      continue_automatically: true,
    },
  )
})
