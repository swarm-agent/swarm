import React from 'react'
import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import type { AutomationV2Proposal, AutomationV2Record } from '../../state/desktop-automation-v2-api'
import { automationV2PageKey, selectPendingAutomationV2Proposals } from '../../state/desktop-automation-v2-state'
import { dispatchDesktopV3Cache, getDesktopV3CacheSnapshot } from '../../state/desktop-v3-cache-store'
import {
  AutomationV2Workspace,
  PendingAutomationCard,
} from './automation-v2-workspace'
import { AutomationV2Sidecar } from './automation-v2-sidecar'

const sampleProposal: AutomationV2Proposal = {
  proposal_id: 'prop-health-1',
  revision: 1,
  digest: 'a'.repeat(64),
  account_id: 'acct-1',
  workspace_id: 'ws-pending-test',
  session_id: 'session-pending-agent-1',
  document: {
    title: 'Daily Health Audit',
    info: { goal: 'Inspect repository status and uncommitted files each morning' },
    checkpoints: [
      {
        id: 'cp-1',
        title: 'Check Git Status',
        tasks: ['Run git status --porcelain', 'Check branch alignment'],
        acceptance_criteria: ['Report routine_clean if clean or attention_alert on drift'],
      },
      {
        id: 'cp-2',
        title: 'Check Build',
        tasks: ['Run fast check scripts'],
        acceptance_criteria: ['Report routine_clean when passing'],
      },
    ],
    automation_v2: {
      schema_version: 2,
      schedule: { kind: 'cron', cron: '0 9 * * *', timezone: 'UTC' },
      missed: 'skip',
      overlap: 'serialize',
      activate_on_accept: true,
      expiration: { kind: 'indefinite' },
    },
  },
}

test('PendingAutomationCard renders base details on the outside in a pending state', () => {
  let askedChanges = false
  let toggled = false

  const markup = renderToStaticMarkup(
    <PendingAutomationCard
      proposal={sampleProposal}
      workspaceId="ws-pending-test"
      isSelected={false}
      isExpanded={false}
      onToggleExpand={() => { toggled = true }}
      onAskForChanges={() => { askedChanges = true }}
      workspaceSlug="test-slug"
    />
  )

  // 1. Pending state indicator
  assert.match(markup, /data-testid="automation-status-pending"/)
  assert.match(markup, />Pending<\/span>/)
  assert.match(markup, /Awaiting deployment/)
  assert.match(markup, /data-pending-card="true"/)

  // 2. Base details on outside
  assert.match(markup, /Daily Health Audit/)
  assert.match(markup, /Inspect repository status and uncommitted files each morning/)
  assert.match(markup, /2 steps · Rev 1/)
  assert.match(markup, /Daily at 09:00/)
  assert.match(markup, /\(UTC\)/)
  assert.match(markup, /Repeats indefinitely/)
  assert.match(markup, /Not yet activated/)

  // 3. Action buttons
  assert.match(markup, /Ask the worker agent for any changes/)
  assert.match(markup, /data-testid="decline-automation-button"/)
  assert.match(markup, />Decline<\/span>/)
  assert.match(markup, /data-testid="accept-automation-button"/)
  assert.match(markup, /Deploy Worker/)
  assert.match(markup, /View details/)

  // 4. Collapsed: expanded detail is not shown
  assert.doesNotMatch(markup, /data-testid="automation-pending-expanded-detail"/)
})

test('PendingAutomationCard expands to open it up with full plan review and accept / ask-for-changes actions', () => {
  const markup = renderToStaticMarkup(
    <PendingAutomationCard
      proposal={sampleProposal}
      workspaceId="ws-pending-test"
      isSelected={true}
      isExpanded={true}
      onToggleExpand={() => {}}
      onAskForChanges={() => {}}
      workspaceSlug="test-slug"
    />
  )

  // Expand trigger updates
  assert.match(markup, /Hide details/)

  // Expanded detail container renders
  assert.match(markup, /data-testid="automation-pending-expanded-detail"/)
  assert.match(markup, /data-testid="automation-v2-plan-review"/)

  // Full details rendered
  assert.match(markup, /Check Git Status/)
  assert.match(markup, /Run git status --porcelain/)
  assert.match(markup, /Check Build/)
  assert.match(markup, /Suggested Intent Presets/)
  assert.match(markup, /Closing States &amp; Alert Conditions/)
  assert.match(markup, /Execution Schedule/)

  // Both ways to act inside expanded review: Accept and Ask for changes, plus Decline
  assert.match(markup, /data-testid="ask-for-changes-button"/)
  assert.match(markup, /Ask the worker agent for any changes/)
  assert.match(markup, /data-testid="reject-automation-button"/)
  assert.match(markup, />Decline<\/button>/)
  assert.match(markup, />Deploy Worker<\/button>/)
})

test('AutomationV2Workspace surfaces pending automation proposals created by sidebar agent', () => {
  const listKey = automationV2PageKey({ action: 'list', workspace_id: 'ws-pending-test' })
  dispatchDesktopV3Cache({
    type: 'automationV2.begin',
    key: listKey,
    input: { action: 'list', workspace_id: 'ws-pending-test' },
    requestId: 'req-list-empty',
  })
  dispatchDesktopV3Cache({
    type: 'automationV2.finish',
    key: listKey,
    requestId: 'req-list-empty',
    generation: 0,
    data: { records: [] },
  })

  // Simulate sidebar agent creating the pending automation permission in cache
  const permissionPayload = {
    path_id: 'permission.automation-v2-plan.v2',
    review_kind: 'worker_v2',
    action: 'propose',
    title: sampleProposal.document.title,
    document: sampleProposal.document,
    proposal_revision: sampleProposal.revision,
    worker_review: {
      proposal_id: sampleProposal.proposal_id,
      revision: sampleProposal.revision,
      digest: sampleProposal.digest,
    },
    scope: {
      account_id: sampleProposal.account_id,
      workspace_id: sampleProposal.workspace_id,
    },
  }

  // Inject pending permission into cache
  getDesktopV3CacheSnapshot().permissionsBySession[sampleProposal.session_id] = [
    {
      id: 'permission_' + sampleProposal.proposal_id,
      sessionId: sampleProposal.session_id,
      toolName: 'manage_workers',
      toolArguments: JSON.stringify(permissionPayload),
      proposalRevision: sampleProposal.revision,
      requirement: 'automation_v2_acceptance',
      mode: 'plan',
      status: 'pending',
      executionStatus: 'waiting_approval',
      createdAt: 1000,
      updatedAt: 1000,
      permissionRequestedAt: 1000,
    } as any,
  ]

  const markup = renderToStaticMarkup(
    <AutomationV2Workspace
      workspaceId="ws-pending-test"
      workspacePath="/path/to/work"
      workspaceName="Test Workspace"
      workspaceSlug="test-slug"
    />
  )

  // 1. Pending proposal card appears in the list!
  assert.match(markup, /data-testid="automations-flat-overview"/)
  assert.match(markup, /data-pending-card="true"/)
  assert.match(markup, /Daily Health Audit/)
  assert.match(markup, /data-testid="automation-status-pending"/)
  assert.match(markup, /Awaiting deployment/)

  // 2. Base details on outside
  assert.match(markup, /2 steps · Rev 1/)
  assert.match(markup, /Daily at 09:00/)
  assert.match(markup, /\(UTC\)/)
  assert.match(markup, /Ask the worker agent for any changes/)
  assert.match(markup, /Deploy Worker/)

  // 3. Summary strip displays Pending count
  assert.match(markup, /data-testid="summary-strip-pending"/)
  assert.match(markup, /<div class="text-\[10px\] font-semibold uppercase tracking-wider text-\[var\(--app-warning\)\]">Pending<\/div>/)

  // 4. Status filter tab for Pending exists
  assert.match(markup, /data-testid="filter-pending"/)
  assert.match(markup, /Pending \(1\)/)

  // 5. Does NOT show "No accepted automations on this page" empty state because pending proposal is visible!
  assert.doesNotMatch(markup, /No accepted automations on this page/)
})

test('AutomationV2Workspace displays pending revision on existing record and links to sidecar', () => {
  const existingRecord: AutomationV2Record = {
    ...sampleProposal,
    automation_id: 'auto-existing-1',
    generation: 1,
    enabled: true,
    cancelled: false,
    accepted_at: 1000,
    authorization: { kind: 'indefinite' },
    session_id: 'session-existing-1',
  }

  const listKey = automationV2PageKey({ action: 'list', workspace_id: 'ws-pending-test-2' })
  dispatchDesktopV3Cache({
    type: 'automationV2.begin',
    key: listKey,
    input: { action: 'list', workspace_id: 'ws-pending-test-2' },
    requestId: 'req-list-existing',
  })
  dispatchDesktopV3Cache({
    type: 'automationV2.finish',
    key: listKey,
    requestId: 'req-list-existing',
    generation: 0,
    data: { records: [existingRecord] },
  })

  // Pending revision 2 on the existing session
  const revisionProposalPayload = {
    path_id: 'permission.automation-v2-plan.v2',
    review_kind: 'automation_v2',
    action: 'request_new_plan',
    title: 'Daily Health Audit (Updated)',
    document: {
      ...existingRecord.document,
      title: 'Daily Health Audit (Updated)',
    },
    plan_id: 'prop-health-1',
    proposal_revision: 2,
    automation_review: {
      proposal_id: 'prop-health-1',
      revision: 2,
      digest: 'b'.repeat(64),
    },
    scope: {
      account_id: 'acct-1',
      workspace_id: 'ws-pending-test-2',
    },
  }

  getDesktopV3CacheSnapshot().permissionsBySession['session-existing-1'] = [
    {
      id: 'permission_prop-health-1',
      sessionId: 'session-existing-1',
      toolName: 'plan_manage',
      toolArguments: JSON.stringify(revisionProposalPayload),
      proposalRevision: 2,
      requirement: 'automation_v2_acceptance',
      mode: 'plan',
      status: 'pending',
      executionStatus: 'waiting_approval',
      createdAt: 2000,
      updatedAt: 2000,
      permissionRequestedAt: 2000,
    } as any,
  ]

  const markup = renderToStaticMarkup(
    <AutomationV2Workspace
      workspaceId="ws-pending-test-2"
      workspacePath="/path/to/work"
      workspaceName="Test Workspace"
      workspaceSlug="test-slug"
    />
  )

  // Card shows Pending review status and Revision 2 pending approval badge
  assert.match(markup, />Pending review<\/span>/)
  assert.match(markup, /Revision 2 pending approval/)
  assert.match(markup, /Ask the worker agent for any changes/)
})

test('AutomationV2Sidecar integrates pending proposals into switcher dropdown', () => {
  const markup = renderToStaticMarkup(
    <AutomationV2Sidecar
      workspaceId="ws-pending-test"
      workspacePath="/path/to/work"
      pendingProposals={[sampleProposal]}
    />
  )

  assert.match(markup, /Pending worker proposals/)
  assert.match(markup, /Daily Health Audit \(Pending\)/)
})

test('AutomationV2Workspace always includes Pending in the filter row even when pendingCount is 0', () => {
  const archivedRecord: AutomationV2Record = {
    ...sampleProposal,
    automation_id: 'auto-archived-1',
    generation: 1,
    enabled: false,
    cancelled: false,
    archived: true,
    accepted_at: 1000,
    authorization: { kind: 'indefinite' },
    session_id: 'session-archived-1',
  }
  const listKey = automationV2PageKey({ action: 'list', workspace_id: 'ws-pending-empty-test' })
  const archivedKey = automationV2PageKey({ action: 'list', workspace_id: 'ws-pending-empty-test', archived_mode: 'only' })
  dispatchDesktopV3Cache({
    type: 'automationV2.begin',
    key: listKey,
    input: { action: 'list', workspace_id: 'ws-pending-empty-test' },
    requestId: 'req-list-empty',
  })
  dispatchDesktopV3Cache({
    type: 'automationV2.finish',
    key: listKey,
    requestId: 'req-list-empty',
    generation: 0,
    data: { records: [] },
  })
  dispatchDesktopV3Cache({
    type: 'automationV2.begin',
    key: archivedKey,
    input: { action: 'list', workspace_id: 'ws-pending-empty-test', archived_mode: 'only' },
    requestId: 'req-archived-1',
  })
  dispatchDesktopV3Cache({
    type: 'automationV2.finish',
    key: archivedKey,
    requestId: 'req-archived-1',
    generation: 0,
    data: { records: [archivedRecord] },
  })

  const markup = renderToStaticMarkup(
    <AutomationV2Workspace
      workspaceId="ws-pending-empty-test"
      workspacePath="/path/to/work"
      workspaceName="Empty Pending Workspace"
      workspaceSlug="empty-pending-slug"
    />
  )

  // Filter row includes All, Pending, Active, Paused, Archived unconditionally
  assert.match(markup, /data-testid="filter-all"/)
  assert.match(markup, /All \(0\)/)
  assert.match(markup, /data-testid="filter-pending"/)
  assert.match(markup, /Pending \(0\)/)
  assert.match(markup, /data-testid="filter-enabled"/)
  assert.match(markup, /Active \(0\)/)
  assert.match(markup, /data-testid="filter-paused"/)
  assert.match(markup, /Paused \(0\)/)
  assert.match(markup, /data-testid="filter-archived"/)
  assert.match(markup, /Archived \(1\)/)

  // Summary strip has 5 columns including Pending
  assert.match(markup, /data-testid="automations-summary-strip"/)
  assert.match(markup, /data-testid="summary-strip-pending"/)
  assert.match(markup, /<div class="mt-1 text-lg font-semibold text-\[var\(--app-text\)\]">0<\/div>/)
})

test('selectPendingAutomationV2Proposals excludes proposals whose permission is denied', () => {
  const cacheState = getDesktopV3CacheSnapshot()
  const deniedPermission = {
    id: 'permission_prop-denied-1',
    sessionId: 'session-denied-1',
    toolName: 'plan_manage',
    toolArguments: JSON.stringify({
      review_kind: 'automation_v2',
      document: sampleProposal.document,
      automation_review: {
        proposal_id: 'prop-denied-1',
        revision: 1,
        digest: 'c'.repeat(64),
      },
      scope: { workspace_id: 'ws-denied-test' },
    }),
    proposalRevision: 1,
    requirement: 'automation_v2_acceptance',
    mode: 'plan',
    status: 'denied',
    executionStatus: 'completed',
    createdAt: 1000,
    updatedAt: 2000,
  }

  cacheState.permissionsBySession['session-denied-1'] = [deniedPermission as any]

  // Add review page in automationV2Pages as well
  const reviewKey = automationV2PageKey({ action: 'review', workspace_id: 'ws-denied-test', session_id: 'session-denied-1' })
  cacheState.automationV2Pages[reviewKey] = {
    input: { action: 'review', workspace_id: 'ws-denied-test', session_id: 'session-denied-1' },
    generation: 1,
    loading: false,
    stale: false,
    data: {
      proposal: {
        proposal_id: 'prop-denied-1',
        revision: 1,
        digest: 'c'.repeat(64),
        account_id: 'acct-1',
        workspace_id: 'ws-denied-test',
        session_id: 'session-denied-1',
        document: sampleProposal.document,
      },
    },
  }

  const proposals = selectPendingAutomationV2Proposals(cacheState, 'ws-denied-test')
  assert.equal(proposals.some(p => p.proposal_id === 'prop-denied-1'), false)
})
