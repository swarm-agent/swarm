import React from 'react'
import test from 'node:test'
import assert from 'node:assert/strict'
import { renderToStaticMarkup } from 'react-dom/server'
import type { DesktopPermissionRecord } from '../../types/realtime'
import { DesktopPermissionModal } from '../../permissions/components/desktop-permission-modal'
import { AutomationV2PlanReview } from './automation-v2-plan-review'
import {
  isAutomationPermission,
  isPlanProposalPermission,
  permissionKind,
  permissionRequiresApproval,
} from '../../permissions/services/permission-payload'
import type { AutomationV2Proposal } from '../../state/desktop-automation-v2-api'

function createAutomationProposal(): AutomationV2Proposal {
  return {
    proposal_id: 'prop_test_123',
    revision: 1,
    digest: 'a'.repeat(64),
    account_id: 'acct_test',
    workspace_id: 'ws_test',
    session_id: 'sess_test',
    document: {
      title: 'Nightly Health Check',
      info: { goal: 'Verify service status and database connectivity' },
      checkpoints: [
        {
          id: 'cp-check-health',
          title: 'Health and DB Inspection',
          tasks: ['Inspect http health check', 'Verify postgres connection'],
          acceptance_criteria: ['All health endpoints return 200', 'Database responds to ping'],
        },
      ],
      automation_v2: {
        schema_version: 2,
        schedule: { kind: 'interval', interval_seconds: 3600 },
        expiration: { kind: 'indefinite' },
        missed: 'skip',
        overlap: 'serialize',
        activate_on_accept: true,
      },
    },
  }
}

function createAutomationPermission(proposal: AutomationV2Proposal): DesktopPermissionRecord {
  return {
    id: 'permission_' + proposal.proposal_id,
    sessionId: proposal.session_id,
    runId: 'run_test',
    callId: 'call_test',
    toolName: 'plan_manage',
    requirement: 'automation_v2_acceptance',
    toolArguments: JSON.stringify({
      review_kind: 'automation_v2',
      action: 'request_new_plan',
      title: proposal.document.title,
      plan_id: proposal.proposal_id,
      proposal_revision: proposal.revision,
      document: proposal.document,
      automation_review: {
        proposal_id: proposal.proposal_id,
        revision: proposal.revision,
        digest: proposal.digest,
      },
      scope: {
        account_id: proposal.account_id,
        workspace_id: proposal.workspace_id,
      },
    }),
    status: 'pending',
    decision: '',
    reason: '',
    mode: 'auto',
    createdAt: Date.now(),
    updatedAt: Date.now(),
    resolvedAt: 0,
    permissionRequestedAt: Date.now(),
  }
}

test('Automation V2 permission is classified as modal review and not inline chat plan card', () => {
  const proposal = createAutomationProposal()
  const permission = createAutomationPermission(proposal)

  // Must be recognized as an automation permission
  assert.equal(isAutomationPermission(permission), true)
  assert.equal(permissionKind(permission), 'automation-v2-acceptance')

  // Must NOT be classified as an inline plan proposal card
  assert.equal(isPlanProposalPermission(permission), false)

  // Must require approval across all session modes
  for (const mode of ['plan', 'auto', 'yolo']) {
    assert.equal(permissionRequiresApproval(permission, mode), true)
  }
})

test('DesktopPermissionModal renders very big modal with full automation details and embedded AI sidebar', () => {
  const proposal = createAutomationProposal()
  const permission = createAutomationPermission(proposal)

  const markup = renderToStaticMarkup(
    <DesktopPermissionModal
      open={true}
      permission={permission}
      pendingCount={1}
      sessionMode="auto"
      onOpenChange={() => undefined}
      onResolve={async () => undefined}
    />
  )

  // Modal shell sizing and style
  assert.match(markup, /role="dialog"/, 'expected modal dialog')
  assert.match(markup, /Nightly Health Check/, 'expected automation title in modal')
  assert.match(markup, /max-w-\[1600px\]/, 'expected very big modal width')

  // Left pane: Full automation review details
  assert.match(markup, /data-testid="automation-v2-plan-review"/, 'expected plan review component')
  assert.match(markup, /Recurring Automation Plan/, 'expected recurring automation badge')
  assert.match(markup, /Verify service status and database connectivity/, 'expected goal callout')
  assert.match(markup, /Execution Schedule/, 'expected schedule section')
  assert.match(markup, /Every hour/, 'expected human-readable interval cadence')
  assert.match(markup, /What Swarm will do/, 'expected execution checkpoints section')
  assert.match(markup, /Health and DB Inspection/, 'expected checkpoint title')
  assert.match(markup, /Inspect http health check/, 'expected checkpoint task')
  assert.match(markup, /All health endpoints return 200/, 'expected acceptance criterion')
  assert.match(markup, /Full plan/, 'expected full plan badge in modal mode')

  // Run behavior and expiration
  assert.match(markup, /Expiration/, 'expected expiration section')
  assert.match(markup, /Until I stop it/, 'expected indefinite option')
  assert.match(markup, /Missed runs:/, 'expected missed run policy')
  assert.match(markup, /Overlap policy:/, 'expected overlap policy')

  // Action buttons
  assert.match(markup, />Reject</, 'expected Reject button')
  assert.match(markup, />Accept automation</, 'expected Accept automation button')

  // Right pane: Embedded AI sidebar
  assert.match(markup, /data-testid="automation-modal-ai-sidebar"/, 'expected embedded AI sidebar column')
  assert.match(markup, /data-modal-inline="true"/, 'expected modal-inline flag on sidecar')
  assert.match(markup, /Swarm Plan AI Sidebar/, 'expected AI sidebar header')
  assert.match(markup, /Live Editing/, 'expected live editing badge in sidebar')
  assert.match(markup, /Ask Swarm to adjust schedule, tasks, or acceptance criteria/, 'expected live edit prompt hint')
  assert.match(markup, /Ask Swarm to change this automation…/, 'expected automation change composer placeholder')
})

test('AutomationV2PlanReview in modalMode renders open checkpoints and criteria', () => {
  const proposal = createAutomationProposal()

  const markup = renderToStaticMarkup(
    <AutomationV2PlanReview
      proposal={proposal}
      modalMode={true}
    />
  )

  // Should NOT hide steps in a collapsed details by default in modalMode
  assert.match(markup, /What Swarm will do · 1 step/, 'expected steps heading')
  assert.match(markup, /Full plan/, 'expected full plan indicator')
  assert.match(markup, /Health and DB Inspection/, 'expected checkpoint title visible')
  assert.match(markup, /All health endpoints return 200/, 'expected criterion visible')
  assert.match(markup, /Database responds to ping/, 'expected second criterion visible')
  assert.match(markup, /sticky bottom-0/, 'expected sticky action footer')
})

test('AutomationV2PlanReview non-modal mode preserves compact details accordion', () => {
  const proposal = createAutomationProposal()

  const markup = renderToStaticMarkup(
    <AutomationV2PlanReview
      proposal={proposal}
      modalMode={false}
    />
  )

  // Non-modal uses details element for steps
  assert.match(markup, /<details[^>]*><summary[^>]*>What Swarm will do/, 'expected collapsed details in non-modal mode')
})
