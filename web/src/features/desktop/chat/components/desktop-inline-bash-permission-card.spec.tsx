import assert from 'node:assert/strict'
import test from 'node:test'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'

import { DesktopInlineBashPermissionCard } from './desktop-v3-existing-conversation-pane'
import type { DesktopPermissionRecord } from '../../types/realtime'

function bashPermission(overrides: Partial<DesktopPermissionRecord> = {}): DesktopPermissionRecord {
  return {
    id: 'permission-1',
    sessionId: 'session-1',
    runId: 'run-1',
    callId: 'call-1',
    toolName: 'bash',
    toolArguments: JSON.stringify({
      command: "printf 'exact <value>' && pwd",
      explanation: 'Print the exact value and current directory.',
    }),
    status: 'pending',
    decision: 'pending',
    authorizationSource: 'approval',
    reason: '',
    requirement: 'bash',
    mode: 'auto',
    createdAt: 1,
    updatedAt: 1,
    resolvedAt: 0,
    permissionRequestedAt: 1,
    executionStatus: 'waiting_approval',
    ...overrides,
  }
}

function render(permission: DesktopPermissionRecord): string {
  return renderToStaticMarkup(React.createElement(DesktopInlineBashPermissionCard, {
    permission,
    onResolve: async () => undefined,
  }))
}

test('pending Bash approval renders inline with explanation, exact command, controls, and bounded expansion', () => {
  const markup = render(bashPermission())
  assert.match(markup, /data-testid="desktop-inline-bash-permission-card"/)
  assert.match(markup, /Approval required/)
  assert.match(markup, /Print the exact value and current directory\./)
  assert.match(markup, /Model context/)
  assert.match(markup, /printf &#x27;exact &lt;value&gt;&#x27; &amp;&amp; pwd/)
  assert.match(markup, /\bExpand\b/)
  assert.match(markup, /max-h-\[50vh\]/)
  assert.match(markup, /max-h-44/)
  assert.match(markup, /overflow-y-auto/)
  assert.match(markup, />Deny</)
  assert.match(markup, />Approve</)
})

test('automatically executed Bash history renders honest status without approval controls', () => {
  const markup = render(bashPermission({
    status: 'not_required',
    decision: 'approve',
    authorizationSource: 'bypass',
    executionStatus: 'completed',
    completedAt: 2,
  }))
  assert.match(markup, /Permission bypassed · Executed/)
  assert.doesNotMatch(markup, /data-testid="desktop-inline-bash-controls"/)
  assert.doesNotMatch(markup, />Approve</)
  assert.doesNotMatch(markup, />Deny</)
})
