import test from 'node:test'
import assert from 'node:assert/strict'
import { permissionRequiresApproval, isPlanProposalPermission } from './permission-payload'
import type { DesktopPermissionRecord } from '../../types/realtime'

// Requirement: pending Automation V2 reviews must survive the full conversation
// approval filter in every mode. Threat: a hidden card makes exact acceptance
// unreachable even while isolated card tests pass. Authority: the two selectors
// consumed by DesktopV3ExistingConversationPane, the narrowest filtering boundary.
test('Automation acceptance stays visible in Plan, Auto and permission bypass', () => {
  for (const mode of ['plan', 'auto', 'yolo']) {
    const permission = {toolName:'plan_manage',requirement:'automation_v2_acceptance',mode} as DesktopPermissionRecord
    assert.equal(permissionRequiresApproval(permission, mode), true)
    assert.equal(isPlanProposalPermission(permission), true)
  }
  assert.equal(permissionRequiresApproval({toolName:'plan_manage',requirement:'draft_only',mode:'auto'}), false)
})
