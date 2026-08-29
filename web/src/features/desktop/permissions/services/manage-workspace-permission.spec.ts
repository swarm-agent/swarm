import assert from 'node:assert/strict'
import test from 'node:test'
import type { DesktopPermissionRecord } from '../../types/realtime'
import { parseWorkspaceMutationPermission, permissionKind } from './permission-payload'

function record(requirement: string, toolArguments: Record<string, unknown>): DesktopPermissionRecord {
  return {
    id: `permission-${requirement}`,
    sessionId: 'session-1',
    runId: 'run-1',
    callId: 'call-1',
    toolName: 'manage_workspace',
    toolArguments: JSON.stringify(toolArguments),
    requirement,
    mode: 'auto',
    status: 'pending',
    decision: '',
    reason: '',
    resolvedAt: 0,
    createdAt: 1,
    updatedAt: 1,
  }
}

test('manage_workspace permission metadata is action-specific and explains safe switching', () => {
  const update = record('workspace_update', {
    action: 'update',
    intent: 'Rename the workspace.',
    target: { workspace_id: 'ws-1', workspace_name: 'Current', workspace_path: '/workspace/current' },
    requested_changes: { workspace_name: 'Renamed' },
    safety: { session_switch_required: true, restore_after_mutation: true, filesystem_contents_changed: false },
    approved_arguments: { secret: 'must stay hidden from the dedicated parser' },
  })

  assert.equal(permissionKind(update), 'workspace-mutation')
  const payload = parseWorkspaceMutationPermission(update)
  assert.equal(payload.action, 'update')
  assert.equal(payload.workspaceName, 'Current')
  assert.deepEqual(payload.changes, [{ label: 'New name', value: 'Renamed' }])
  assert.match(payload.safetySummary, /switch to another authorized workspace/i)
  assert.match(payload.safetySummary, /restore this session/i)
  assert.equal(payload.permissionSummary, 'Always allow applies only to workspace update requests; it does not approve the other workspace actions.')
  assert.doesNotMatch(JSON.stringify(payload), /secret/)
})

test('manage_workspace delete metadata says files are preserved and the session remains safe', () => {
  const deletion = record('workspace_delete', {
    action: 'delete',
    intent: 'Unlink this saved workspace.',
    target: { workspace_id: 'ws-2', workspace_name: 'Old', workspace_path: '/workspace/old' },
    safety: { session_switch_required: true, remain_in_safe_workspace: true, filesystem_contents_changed: false },
  })

  const payload = parseWorkspaceMutationPermission(deletion)
  assert.equal(payload.action, 'delete')
  assert.match(payload.safetySummary, /before unlinking/i)
  assert.match(payload.safetySummary, /will remain there/i)
  assert.match(payload.permissionSummary, /only to workspace delete requests/i)
})

test('workspace create, update, and delete permissions remain distinct kinds by requirement', () => {
  for (const requirement of ['workspace_create', 'workspace_update', 'workspace_delete']) {
    assert.equal(permissionKind(record(requirement, { action: requirement.replace('workspace_', '') })), 'workspace-mutation')
  }
  assert.equal(permissionKind(record('manage_workspace', { action: 'inspect' })), 'generic')
})
