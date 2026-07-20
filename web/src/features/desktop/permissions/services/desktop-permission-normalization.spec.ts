import assert from 'node:assert/strict'
import test from 'node:test'

import { normalizeDesktopPendingPermissions, normalizeDesktopPermission } from './desktop-permission-normalization'
import { bashPermissionDisplayData } from './permission-payload'

test('normalizes pending Bash display data without changing the exact command', () => {
  const command = "printf '  exact value  '\nfind . -name '*.ts'"
  const permission = normalizeDesktopPermission({
    id: 'permission-1',
    session_id: 'session-1',
    run_id: 'run-1',
    call_id: 'call-1',
    tool_name: 'bash',
    tool_arguments: JSON.stringify({ command, explanation: 'Inspect TypeScript files.' }),
    tool_call_arguments: JSON.stringify({ command, explanation: 'Inspect TypeScript files.' }),
    status: 'pending',
    decision: 'pending',
    authorization_source: 'approval',
    execution_status: 'waiting_approval',
  }, 'session-1')

  assert(permission)
  assert.equal(permission.toolCallArguments, JSON.stringify({ command, explanation: 'Inspect TypeScript files.' }))
  assert.deepEqual(bashPermissionDisplayData(permission), {
    command,
    explanation: 'Inspect TypeScript files.',
    pending: true,
    authorizationSource: 'approval',
    executionStatus: 'waiting_approval',
    statusLabel: 'Approval required',
  })
})

test('normalizes automatic and bypassed Bash history honestly while pending snapshots remain pending-only', () => {
  const automatic = normalizeDesktopPermission({
    id: 'permission-auto',
    session_id: 'session-1',
    tool_name: 'bash',
    tool_arguments: JSON.stringify({ command: 'pwd', explanation: 'Show the current directory.' }),
    status: 'not_required',
    decision: 'approve',
    authorization_source: 'bypass',
    execution_status: 'completed',
    completed_at: 10,
  }, 'session-1')

  assert(automatic)
  assert.equal(bashPermissionDisplayData(automatic)?.statusLabel, 'Permission bypassed · Executed')
  assert.deepEqual(normalizeDesktopPendingPermissions([automatic], 'session-1'), [])
})
