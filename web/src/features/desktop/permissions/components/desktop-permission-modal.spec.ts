import React from 'react'
import test from 'node:test'
import assert from 'node:assert/strict'
import { renderToStaticMarkup } from 'react-dom/server'

import { DesktopPermissionModal, alwaysAllowSessionDeployPolicy, exitPlanExecutionArguments, newPlanLifecycleApprovedArguments } from './desktop-permission-modal'
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

function planLifecyclePermission(requirement: string, toolArguments: Record<string, unknown>, toolName = 'plan_manage'): DesktopPermissionRecord {
  return {
    id: `perm-${requirement}`,
    sessionId: 'session-1',
    runId: 'run-1',
    callId: 'call-1',
    toolName,
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
    onOpenPermissions: () => undefined,
    onResolve: async () => undefined,
  }))
}

test('exit plan modal states automatic execution without a manual mode control', () => {
  const markup = renderPermission(exitPlanPermission(), 'plan')

  assert.match(markup, /Starts automatically after approval/)
  assert.doesNotMatch(markup, /Pause for review after each checkpoint/)
  assert.doesNotMatch(markup, /type="checkbox"/)
  assert.doesNotMatch(markup, /Single run/)
  assert.doesNotMatch(markup, /role="radiogroup"/)
})

test('exit plan modal defaults to automatic mode without presenting an AI suggestion', () => {
  const markup = renderPermission(exitPlanPermission(), 'plan')

  assert.match(markup, /Starts automatically after approval/)
  assert.doesNotMatch(markup, /AI-suggested/)
  assert.doesNotMatch(markup, /AI suggested/)
  assert.doesNotMatch(markup, /Approval will send/)
  assert.doesNotMatch(markup, /execution_granularity=checkpointed/)
  assert.doesNotMatch(markup, /continuation_policy=automatic/)
})

test('exit plan modal ignores top-level backend execution recommendation', () => {
  const markup = renderPermission(exitPlanPermission({}, {
    execution_granularity: 'checkpointed',
    continuation_policy: 'review_each_checkpoint',
    continue_automatically: false,
  }), 'plan')

  assert.match(markup, /Starts automatically after approval/)
  assert.match(markup, /Always allow plan acceptance/)
  assert.doesNotMatch(markup, /AI suggested/)
  assert.doesNotMatch(markup, /execution_granularity=/)
  assert.doesNotMatch(markup, /continuation_policy=automatic/)
})

test('exit plan modal ignores approved argument execution controls when choosing the default', () => {
  const markup = renderPermission(exitPlanPermission({
    execution_granularity: 'checkpointed',
    continuation_policy: 'review_each_checkpoint',
    continue_automatically: false,
  }), 'plan')

  assert.match(markup, /Starts automatically after approval/)
  assert.match(markup, /Always allow plan acceptance/)
  assert.doesNotMatch(markup, /AI suggested/)
  assert.doesNotMatch(markup, /execution_granularity=/)
  assert.doesNotMatch(markup, /continuation_policy=automatic/)
})

test('exit plan approval always maps to automatic checkpoint execution', () => {
  assert.deepEqual(exitPlanExecutionArguments(), {
    execution_granularity: 'checkpointed',
    continue_automatically: true,
    continuation_policy: 'automatic',
  })
})

test('exit plan and new plan approval modals use the same non-split plan layout order', () => {
  const exitPlan = renderPermission(exitPlanPermission(), 'plan')
  const exitReviewIndex = exitPlan.indexOf('Starts automatically after approval')
  const exitDenyIndex = exitPlan.indexOf('>Deny<')
  const exitDetailsIndex = exitPlan.indexOf('Plan details')
  const exitCheckpointsIndex = exitPlan.indexOf('Checkpoints')
  assert(exitReviewIndex >= 0, 'expected exit plan automatic execution state')
  assert(exitReviewIndex < exitDenyIndex, 'expected exit plan automatic execution state to the left of Deny')
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
  const newReviewIndex = newPlan.indexOf('Starts automatically after approval')
  const newDenyIndex = newPlan.indexOf('>Deny<')
  const lifecycleIndex = newPlan.indexOf('Lifecycle action')
  const newDetailsIndex = newPlan.indexOf('Plan details')
  const newCheckpointsIndex = newPlan.indexOf('Checkpoints')
  assert(newReviewIndex >= 0, 'expected new plan automatic execution state')
  assert(newReviewIndex < newDenyIndex, 'expected new plan automatic execution state to the left of Deny')
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
  assert.match(newPlan, /Structured checkpoint/)
  assert.match(newPlan, /Display structured content/)
  assert.match(newPlan, /Expose full structured plan before approval/)
  assert.match(newPlan, /Render checkpoint task/)
  assert.match(newPlan, /Checkpoint content is visible before approval/)
  assert.match(newPlan, /Approve new plan/)
  assert.match(newPlan, /Starts automatically after approval/)
  assert.match(newPlan, /Always allow plan acceptance/)
  assert.doesNotMatch(newPlan, /type="checkbox"/)
  assert.doesNotMatch(newPlan, /Single run/)
  assert.doesNotMatch(newPlan, /No proposed new plan document or plan text was provided/)
  assert.doesNotMatch(newPlan, /Plan update overview/)
})

test('session deploy permission defaults to one selected proposal and separates one-time from Always allow', () => {
  const markup = renderPermission(planLifecyclePermission('session_deploy', {
    action: 'deploy',
    manifest_version: 1,
    manifest_digest: 'digest-1',
    proposals: [
      { id: 'proposal-1', title: 'Primary work', prompt: 'Ship the primary task', mode: 'auto', agent_name: 'swarm', agent_mode: 'primary', workspace_path: '/workspace', workspace_name: 'Workspace', managed_worktree: true, worktree_base_branch: 'dev', worktree_branch: 'agent/primary-work', selected: true },
      { id: 'proposal-2', title: 'Extra work', prompt: 'Investigate an extra task', mode: 'plan', agent_name: 'finder', agent_mode: 'subagent', workspace_path: '/workspace', workspace_name: 'Workspace', selected: true },
    ],
    allowed_workspaces: [
      { id: 'workspace-1', generation: 2, path: '/workspace', name: 'Workspace' },
      { id: 'workspace-2', generation: 1, path: '/another', name: 'Another workspace' },
    ],
    approved_arguments: { action: 'deploy', manifest_version: 1, manifest_digest: 'digest-1' },
  }, 'manage-sessions'))

  assert.match(markup, /Deploy sessions\?/)
  assert.match(markup, /One safe default is selected/)
  assert.equal((markup.match(/type="checkbox"/g) || []).length, 2)
  assert.equal((markup.match(/checked=""/g) || []).length, 1)
  assert.match(markup, /Deploy 1 session/)
  assert.match(markup, /Another workspace/)
  assert.match(markup, /Managed worktree \(recommended\)/)
  assert.match(markup, /Use current workspace/)
  assert.match(markup, /AI branch suggestion/)
  assert.match(markup, /agent\/primary-work/)
  assert.doesNotMatch(markup, /value="\/workspace" readonly/)
  assert.match(markup, />Always allow</)
  assert.match(markup, /Need granular deployment controls\?/)
  assert.match(markup, /Permissions settings/)
  assert.match(markup, /Set ask, bounded automatic limits, or over-limit behavior in Permissions settings/)
  assert.doesNotMatch(markup, /Save policy &amp; deploy/)
  assert.doesNotMatch(markup, /Automatic deployments per parent run/)
  assert.doesNotMatch(markup, /When limit is reached/)
  assert.match(markup, /max-h-\[calc\(100dvh_-_var\(--app-safe-area-top\)_-_var\(--app-safe-area-bottom\)_-_12px\)\]/)
  assert.match(markup, /max-w-\[1100px\]/)
  assert.match(markup, /overflow-x-hidden/)
  assert.match(markup, /overflow-y-auto/)
  assert.match(markup, /max-w-full/)
  assert.match(markup, /text-ellipsis/)
  assert.match(markup, /\[field-sizing:fixed\]/)
  assert.match(markup, /sm:grid-cols-2/)
  assert.match(markup, /sm:grid-cols-3/)
  assert.match(markup, /title="Workspace"/)
})

test('session deploy Always allow uses the explicit account-wide deployment policy', () => {
  assert.deepEqual(alwaysAllowSessionDeployPolicy(), {
    mode: 'always_allow',
    automatic_deployments_per_parent_run: 0,
    over_limit_action: 'ask',
  })
})

test('session commit permission renders exact commits and persistent choices', () => {
  const markup = renderPermission(planLifecyclePermission('session_commit', {
    action: 'commit', manifest: { commits: [{ message: 'Ship exact files', repository: '/workspace', files: [{ path: 'web/src/app.tsx', fingerprint: 'secret' }] }] }, approved_arguments: { action: 'commit', manifest: { version: 1 } },
  }, 'manage_sessions'))
  assert.match(markup, /Commit session changes\?/)
  assert.match(markup, /Ship exact files/)
  assert.match(markup, /web\/src\/app.tsx/)
  assert.match(markup, /Always Allow/)
  assert.match(markup, /Always Deny/)
  assert.doesNotMatch(markup, /fingerprint|secret/)
})

test('session unarchive permission renders human-readable restored sessions and persistent choices', () => {
  const markup = renderPermission(planLifecyclePermission('session_unarchive', {
    action: 'unarchive', sessions: [{ state: 'archived', title: 'Restore session', updated_at: 1783764535576, workspace_name: 'swarm-go' }], approved_arguments: { action: 'unarchive', session_ids: ['opaque-session-id'] },
  }, 'manage_sessions'))
  assert.match(markup, /Unarchive sessions\?/)
  assert.match(markup, /Restore session/)
  assert.match(markup, /Always Allow/)
  assert.match(markup, /Always Deny/)
  assert.doesNotMatch(markup, /opaque-session-id/)
})

test('session archive permission renders polished session cards instead of raw JSON', () => {
  const markup = renderPermission(planLifecyclePermission('session_archive', {
    action: 'archive',
    sessions: [
      { state: 'needs_review', title: 'Review search results', updated_at: 1783764535576, workspace_name: 'swarm-go' },
      { state: 'idle', title: 'Git status and diff', updated_at: 1783775746686, workspace_name: 'swarm-go' },
    ],
    approved_arguments: {
      action: 'archive',
      session_ids: ['opaque-session-id'],
      expected_updated_at_by_id: { 'opaque-session-id': 1783764535576 },
    },
  }, 'manage_sessions'))

  assert.match(markup, /Archive sessions\?/)
  assert.match(markup, /2 sessions selected/)
  assert.match(markup, /Review search results/)
  assert.match(markup, /Git status and diff/)
  assert.match(markup, /swarm-go/)
  assert.match(markup, /Needs Review/)
  assert.match(markup, /Updated/)
  assert.match(markup, /Archive 2 sessions/)
  assert.match(markup, /Always Allow/)
  assert.match(markup, /Always Deny/)
  assert.doesNotMatch(markup, /opaque-session-id/)
  assert.doesNotMatch(markup, /expected_updated_at_by_id/)
  assert.doesNotMatch(markup, /\[\s*\{&quot;state&quot;/)
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
        execution_granularity: 'checkpointed',
        continuation_policy: 'review_each_checkpoint',
        continue_automatically: false,
      },
      diffLines: [],
      priorPlan: '',
      priorTitle: '',
      priorDocument: null,
    }),
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
