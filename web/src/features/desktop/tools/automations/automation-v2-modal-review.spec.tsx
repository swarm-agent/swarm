import React from 'react'
import test from 'node:test'
import assert from 'node:assert/strict'
import { renderToStaticMarkup } from 'react-dom/server'
import type { DesktopPermissionRecord } from '../../types/realtime'
import { DesktopPermissionModal } from '../../permissions/components/desktop-permission-modal'
import {
  AutomationV2PlanReview,
  AUTOMATION_INTENT_PRESETS,
  getCheckpointClosingState,
  getCheckpointAlertConditions,
} from './automation-v2-plan-review'
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

test('AutomationV2PlanReview renders suggested intent presets with plain-English guidance', () => {
  const proposal = createAutomationProposal()
  const markup = renderToStaticMarkup(
    <AutomationV2PlanReview proposal={proposal} modalMode={true} />
  )

  // Presets container
  assert.match(markup, /data-testid="intent-presets-section"/, 'expected intent presets section')
  assert.match(markup, /Suggested Intent Presets/, 'expected section heading')
  assert.match(markup, /Select an intent preset to configure recommended outcome states/, 'expected explanatory text')

  // All 3 presets present
  assert.match(markup, /data-testid="intent-preset-silent_maintenance"/, 'expected silent maintenance preset')
  assert.match(markup, /Silent maintenance/, 'expected silent maintenance title')
  assert.match(markup, /Calm background/, 'expected silent maintenance tag')
  assert.match(markup, /Runs quietly in the background/, 'expected silent maintenance description')

  assert.match(markup, /data-testid="intent-preset-summary_report"/, 'expected summary report preset')
  assert.match(markup, /Summary report/, 'expected summary report title')
  assert.match(markup, /Periodic status/, 'expected summary report tag')
  assert.match(markup, /Produces a concise summary report/, 'expected summary report description')

  assert.match(markup, /data-testid="intent-preset-deliverable_output"/, 'expected deliverable output preset')
  assert.match(markup, /Deliverable output/, 'expected deliverable output title')
  assert.match(markup, /Deliverables &amp; files|Deliverables & files/, 'expected deliverable output tag')
  assert.match(markup, /Generates or updates project files/, 'expected deliverable output description')
})

test('AutomationV2PlanReview displays closing states and alert conditions configuration', () => {
  const proposal = createAutomationProposal()
  const markup = renderToStaticMarkup(
    <AutomationV2PlanReview proposal={proposal} modalMode={true} />
  )

  // Closing states & alert conditions fieldset
  assert.match(markup, /data-testid="closing-states-config"/, 'expected closing states config fieldset')
  assert.match(markup, /Closing States &amp; Alert Conditions|Closing States & Alert Conditions/, 'expected section legend')
  assert.match(markup, /The AI proposes how to classify completed runs/, 'expected explanation text')

  // Expected outcome select and options
  assert.match(markup, /Expected outcome \(closing state\)/, 'expected closing state label')
  assert.match(markup, /aria-label="Closing state"/, 'expected select with Closing state label')
  assert.match(markup, /Routine clean — Calm minimal status/, 'expected routine_clean option text')
  assert.match(markup, /Deliverable ready — Highlights generated files/, 'expected deliverable_ready option text')
  assert.match(markup, /Attention alert — Raises an alert badge/, 'expected attention_alert option text')
  assert.match(markup, /Blocked — Flags run as waiting on permissions/, 'expected blocked option text')

  // Alert conditions textarea and explanation
  assert.match(markup, /Alert conditions \(when to notify\)/, 'expected alert conditions label')
  assert.match(markup, /aria-label="Alert conditions"/, 'expected alert conditions textarea')
  assert.match(markup, /Swarm evaluates these conditions at completion/, 'expected alert evaluation description')
})

test('AutomationV2PlanReview replaces technical schedule jargon with clear human explanations', () => {
  const proposal = createAutomationProposal()
  const markup = renderToStaticMarkup(
    <AutomationV2PlanReview proposal={proposal} modalMode={true} />
  )

  // Run behavior & policies header
  assert.match(markup, /Run behavior &amp; policies|Run behavior & policies/, 'expected run behavior legend')

  // Missed runs policy in plain English without jargon
  assert.match(markup, /aria-label="Missed runs"/, 'expected accessible Missed runs dropdown')
  assert.match(markup, /Skip missed runs \(resume on next scheduled time\)/, 'expected plain-English skip option')
  assert.match(markup, /Catch up once \(run immediately when back online\)/, 'expected plain-English coalesce option')
  assert.match(markup, /If your computer is sleeping or offline when a run is scheduled/, 'expected human explanation for missed runs')

  // Overlap policy in plain English without jargon
  assert.match(markup, /aria-label="Overlap policy"/, 'expected accessible Overlap policy dropdown')
  assert.match(markup, /Wait for earlier run \(execute one at a time in order\)/, 'expected plain-English serialize option')
  assert.match(markup, /Run concurrently \(execute in parallel without waiting\)/, 'expected plain-English independent option')
  assert.match(markup, /If a previous execution is still running when the next scheduled time arrives/, 'expected human explanation for overlap policy')

  // Ensure raw technical jargon strings are NOT present as bare text
  assert.doesNotMatch(markup, /missed: skip/, 'should not have raw technical jargon missed: skip')
  assert.doesNotMatch(markup, /overlap: serialize/, 'should not have raw technical jargon overlap: serialize')
})

test('AutomationV2PlanReview respects custom proposed closing states and alert conditions', () => {
  const proposal = createAutomationProposal()
  proposal.document.checkpoints[0].closing_state = 'deliverable_ready'
  proposal.document.checkpoints[0].alert_conditions = 'Alert if output PDF generation fails or size is 0 bytes'
  proposal.document.automation_v2.missed = 'coalesce'
  proposal.document.automation_v2.overlap = 'independent'

  const markup = renderToStaticMarkup(
    <AutomationV2PlanReview proposal={proposal} modalMode={true} />
  )

  // Should render deliverable ready explanation
  assert.match(markup, /Deliverable run\. Highlights generated documents or artifacts/, 'expected deliverable ready explanation')
  // Should render the custom alert condition in textarea
  assert.match(markup, /Alert if output PDF generation fails or size is 0 bytes/, 'expected custom alert conditions in markup')
  // Should render coalesce explanation
  assert.match(markup, /Swarm executes a single catch-up run immediately upon reconnecting/, 'expected coalesce explanation')
  // Should render independent explanation
  assert.match(markup, /the new run starts immediately in parallel/, 'expected independent explanation')
})

test('getCheckpointClosingState and getCheckpointAlertConditions helpers classify checkpoints accurately', () => {
  // 1. Explicit closing_state
  assert.equal(getCheckpointClosingState({ closing_state: 'deliverable_ready' }), 'deliverable_ready')
  assert.equal(getCheckpointClosingState({ closing_state: 'attention_alert' }), 'attention_alert')
  assert.equal(getCheckpointClosingState({ closing_state: 'blocked' }), 'blocked')
  assert.equal(getCheckpointClosingState({ closing_state: 'routine_clean' }), 'routine_clean')

  // 2. Inferred from text when not explicit
  assert.equal(getCheckpointClosingState({ title: 'Generate monthly deliverable' }), 'deliverable_ready')
  assert.equal(getCheckpointClosingState({ acceptance_criteria: ['Check alert conditions on error'] }), 'attention_alert')
  assert.equal(getCheckpointClosingState({ tasks: ['Routine database backup'] }), 'routine_clean')

  // 3. Alert conditions helper
  assert.equal(
    getCheckpointAlertConditions({ alert_conditions: 'Alert when CPU exceeds 90%' }),
    'Alert when CPU exceeds 90%'
  )
  assert.equal(
    getCheckpointAlertConditions({ acceptance_criteria: ['Alert if error count > 5', 'Store logs'] }),
    'Alert if error count > 5'
  )
  assert.match(
    getCheckpointAlertConditions({ acceptance_criteria: ['Ping database'] }),
    /Alert if health checks fail/
  )
})
