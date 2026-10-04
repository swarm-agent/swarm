import React from 'react'
import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { TaskAttention } from './task-attention'
import { DesktopInlinePermission } from '../permissions/components/desktop-permission-modal'
import type { DesktopPermissionRecord } from '../types/realtime'

// Purpose: the persistent TaskAttention panel must enumerate questions and
// approvals even before a card is expanded or a chat selected. Server rendering
// is the narrowest layer proving visible labels/actions without modal side effects.
test('attention panel shows every question and command with explicit actions, without auto-opening dialogs', () => {
  const base: DesktopPermissionRecord = { id: 'question', sessionId: 'grandchild', runId: 'run', callId: 'call', toolName: 'ask_user', toolArguments: '{"questions":[{"id":"q","question":"Which direction?","options":[{"label":"One"},{"label":"Two"}]}]}', status: 'pending', decision: '', reason: '', requirement: '', mode: 'auto', createdAt: 1, updatedAt: 1, resolvedAt: 0, permissionRequestedAt: 1 }
  const html = renderToStaticMarkup(<TaskAttention attention={{ permissions: [base, { ...base, id: 'command', toolName: 'bash', toolArguments: '{"command":"git status"}' }], unresolvedCount: 2, error: '', retry: () => {} }} />)
  assert.match(html, /Waiting for you/)
  assert.match(html, /2 pending/)
  assert.match(html, /Which direction/)
  assert.match(html, /git status/)
  for (const text of ['One', 'Two', 'Custom response', 'Submit response', 'Approve', 'Deny']) assert.ok(html.includes(text), text)
  assert.doesNotMatch(html, /role="dialog"/)
})

// Purpose: card review delegates to the existing structured multi-question UI,
// preserving choices and Custom response rather than flattening an answer form.
test('canonical ask-user review preserves multiple questions and custom response controls', () => {
  const permission = { id: 'questions', sessionId: 'child', toolName: 'ask_user', mode: 'auto', toolArguments: JSON.stringify({ questions: [
    { id: 'first', question: 'First question?', options: [{ label: 'Alpha', value: 'alpha' }, { label: 'Beta', value: 'beta' }] },
    { id: 'second', question: 'Second question?', options: [{ label: 'Gamma', value: 'gamma' }, { label: 'Delta', value: 'delta' }] },
  ] }) } as DesktopPermissionRecord
  const html = renderToStaticMarkup(<DesktopInlinePermission permission={permission} pendingCount={1} sessionMode="auto" onResolve={async () => { throw new Error('Rendering is not consent') }} />)
  for (const text of ['First question?', 'Second question?', 'Alpha', 'Beta', 'Gamma', 'Delta', 'Custom response']) assert.ok(html.includes(text), text)
})

// Purpose: tool-declared blockers are not permissions. The collapsed-card attention
// surface must retain their reason and offer explicit supply/resume, without
// inventing a pending permission or resuming merely because it rendered.
test('collapsed attention shows a durable blocker with a supply input action', () => {
  const html = renderToStaticMarkup(<TaskAttention attention={{
    permissions: [], unresolvedCount: 0, error: '', retry: () => {},
    blocker: { task: { id: 'task', status: 'blocked', revision: 3 } as import('./orchestrate-types').RunningTask,
      projectId: 'project', reason: 'required prototype missing' },
  }} />)
  assert.match(html, /required prototype missing/)
  assert.match(html, />Supply input and resume</)
  assert.doesNotMatch(html, /0 pending|Review permission|role="dialog"/)
})

// Purpose: TaskAttention must use permission payload classification, not the tool
// name or a cached accepted plan. SSR proves malformed proposals retain controls
// and non-plan operations stay ordinary requests without fabricating a plan.
test('partial plan proposals remain actionable and non-plan operations stay generic', () => {
  const base = { id: 'partial', sessionId: 'child', runId: 'run', callId: 'call', toolName: 'plan_manage', status: 'pending', decision: '', reason: '', mode: 'auto', createdAt: 1, updatedAt: 1, resolvedAt: 0, permissionRequestedAt: 1 } as DesktopPermissionRecord
  const html = renderToStaticMarkup(<TaskAttention attention={{ permissions: [
    { ...base, requirement: 'plan_new_request', toolArguments: '{broken' },
    { ...base, id: 'ordinary', requirement: 'tool_approval', toolArguments: '{"action":"get-active"}' },
  ], unresolvedCount: 2, error: '', retry: () => {} }} />)
  assert.equal((html.match(/data-testid="desktop-inline-plan-review"/g) || []).length, 1)
  assert.match(html, /Review this plan proposal/)
  assert.match(html, />Reject</)
  assert.match(html, />Accept once</)
  assert.equal((html.match(/data-testid="desktop-inline-permission"/g) || []).length, 1)
  assert.match(html, />Approve</)
  assert.doesNotMatch(html, /role="dialog"/)
})

// Purpose: hydration failure must not hide already-known actionable requests.
// TaskAttention owns the loading/retry presentation; SSR is sufficient to prove
// coexistence, while the browser tests exercise recovery and submission.
test('loading failure retains question controls and a visible retry action', () => {
  const permission = { id: 'ask', sessionId: 'child', toolName: 'ask_user', mode: 'auto', toolArguments: JSON.stringify({ questions: [{ id: 'q', question: 'Continue?', options: ['Yes', 'No'] }] }) } as DesktopPermissionRecord
  const html = renderToStaticMarkup(<TaskAttention attention={{ permissions: [permission], unresolvedCount: 2, error: 'Hydration unavailable', retry: () => {} }} />)
  for (const text of ['Continue?', 'Yes', 'No', 'Custom response', 'Submit response', 'Loading pending requests', 'Hydration unavailable', 'Retry loading requests']) assert.ok(html.includes(text), text)
  assert.doesNotMatch(html, /role="dialog"/)
})
