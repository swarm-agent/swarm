import test from 'node:test'
import assert from 'node:assert/strict'
import { permissionRequiresApproval, isPlanProposalPermission, permissionKind } from './permission-payload'
import { automationV2PermissionProposal } from '../../state/desktop-automation-v2-api'
import type { DesktopPermissionRecord } from '../../types/realtime'

// Requirement: pending Automation V2 reviews must survive the full conversation
// approval filter in every mode as a modal review, not an inline chat plan card.
// Threat: routing to inline plan cards buries automation details in chat and
// disconnects the AI sidebar from review. Authority: the two selectors
// consumed by DesktopV3ExistingConversationPane, the narrowest filtering boundary.
test('Automation acceptance stays visible in Plan, Auto and permission bypass as modal review', () => {
  for (const mode of ['plan', 'auto', 'yolo']) {
    for (const toolName of ['manage_workers', 'plan_manage']) {
      const permission = { toolName, requirement: 'automation_v2_acceptance', mode } as DesktopPermissionRecord
      assert.equal(permissionKind(permission), 'automation-v2-acceptance')
      assert.equal(permissionRequiresApproval(permission, mode), true)
      assert.equal(isPlanProposalPermission(permission), false)
    }
  }
  assert.equal(permissionRequiresApproval({toolName:'plan_manage',requirement:'draft_only',mode:'auto'}), false)
})

test('automationV2PermissionProposal parses job-free specialist workers without checkpoints', () => {
  const proposalPayload = {
    path_id: 'permission.automation-v2-plan.v2',
    review_kind: 'worker_v2',
    action: 'propose',
    title: 'Worker review',
    document: {
      title: 'Swarm Social Specialist',
      info: { goal: 'Audit and author social content' },
      worker_v2: {
        schema_version: 2,
        workspace_id: 'ws_social',
        activate_on_accept: true,
        missed: 'skip',
        overlap: 'serialize',
        schedule: { kind: 'trigger' },
      },
    },
    scope: { account_id: 'acct_1', workspace_id: 'ws_social' },
    worker_review: {
      proposal_id: 'av2_test',
      revision: 1,
      digest: 'e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855',
    },
  }

  const permission = {
    id: 'perm_1',
    sessionId: 'sess_1',
    toolName: 'manage_workers',
    toolArguments: JSON.stringify(proposalPayload),
    requirement: 'automation_v2_acceptance',
    mode: 'auto',
    status: 'pending',
  } as DesktopPermissionRecord

  const proposal = automationV2PermissionProposal(permission)
  assert.notEqual(proposal, null, 'Proposal should not be null for job-free worker')
  assert.equal(proposal?.document.title, 'Swarm Social Specialist')
  assert.equal(proposal?.workspace_id, 'ws_social')
  assert.equal(proposal?.session_id, 'sess_1')
  assert.deepEqual(proposal?.document.checkpoints, [])
})
