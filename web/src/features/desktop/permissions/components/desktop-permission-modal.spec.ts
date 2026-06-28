import React from 'react'
import test from 'node:test'
import assert from 'node:assert/strict'
import { renderToStaticMarkup } from 'react-dom/server'

import { DesktopPermissionModal, exitPlanExecutionArguments } from './desktop-permission-modal'
import type { DesktopPermissionRecord } from '../../types/realtime'

function exitPlanPermission(): DesktopPermissionRecord {
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
      approved_arguments: {},
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

test('exit plan modal renders three clickable execution choices instead of select and checkbox controls', () => {
  const markup = renderToStaticMarkup(React.createElement(DesktopPermissionModal, {
    open: true,
    permission: exitPlanPermission(),
    pendingCount: 1,
    sessionMode: 'plan',
    onOpenChange: () => undefined,
    onResolve: async () => undefined,
  }))

  assert.match(markup, /Continue normally/)
  assert.match(markup, /Continue checkpoint by checkpoint/)
  assert.match(markup, /Automatic mode/)
  assert.match(markup, /role="radiogroup"/)
  assert.match(markup, /role="radio"/)
  assert.doesNotMatch(markup, /<select/)
  assert.doesNotMatch(markup, /type="checkbox"/)
})

test('exit plan execution choices map to approved arguments', () => {
  assert.deepEqual(exitPlanExecutionArguments('run_through'), {
    execution_granularity: 'run_through',
    continue_automatically: true,
    continuation_policy: 'automatic',
  })

  assert.deepEqual(exitPlanExecutionArguments('checkpointed_manual'), {
    execution_granularity: 'checkpointed',
    continue_automatically: false,
    continuation_policy: 'review_each_checkpoint',
  })

  assert.deepEqual(exitPlanExecutionArguments('checkpointed_automatic'), {
    execution_granularity: 'checkpointed',
    continue_automatically: true,
    continuation_policy: 'automatic',
  })
})
