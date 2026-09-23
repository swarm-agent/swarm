import test from 'node:test'
import assert from 'node:assert/strict'
import { permissionRequiresApproval, isPlanProposalPermission, permissionKind } from './permission-payload'
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
