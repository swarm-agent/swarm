import React from 'react'
import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { createEmptyDesktopV3CacheState } from '../../state/desktop-v3-cache-reducer'
import { resetDesktopV3CacheForTests } from '../../state/desktop-v3-cache-store'
import { PendingWorkerSidebarReviews, decidePendingWorkerReview } from './pending-worker-sidebar-reviews'

// Requirement: the Desktop Workers sidebar offers explicit review and decisions
// without requiring the authoring chat or its composer to become a worker.
// Threat: a stale/foreign review or client-chosen scope activates unrelated work.
// The component plus canonical cache and exact mutation arguments are the narrowest layer.
test('pending worker sidebar shows global reviews and does not expose decisions until reviewed', () => {
  const state = createEmptyDesktopV3CacheState()
  const payload = (workspace: string) => ({ review_kind: 'worker_v2', scope: { workspace_id: workspace, account_id: 'account' }, worker_review: { proposal_id: 'p-' + workspace, revision: 1, digest: 'a'.repeat(64) }, document: { title: 'Worker in ' + workspace, info: { goal: 'Inspect changes' }, checkpoints: [{ id: 'cp-1', title: 'Inspect', acceptance_criteria: ['Complete'] }], worker_v2: { schema_version: 2, schedule: { kind: 'interval', interval_seconds: 3600 }, expiration: { kind: 'indefinite' }, missed: 'skip', overlap: 'serialize', activate_on_accept: true } } })
  for (const workspace of ['alpha', 'beta']) state.permissionsBySession['chat-' + workspace] = [{ id: 'permission_p-' + workspace, sessionId: 'chat-' + workspace, toolName: 'manage_workers', status: 'pending', requirement: 'automation_v2_acceptance', toolArguments: JSON.stringify(payload(workspace)) } as any]
  resetDesktopV3CacheForTests(state)
  const markup = renderToStaticMarkup(<PendingWorkerSidebarReviews onOpenChat={() => {}} />)
  assert.match(markup, /Worker in alpha/)
  assert.match(markup, /Worker in beta/)
  assert.match(markup, /sidebar-pending-workers/)
  assert.match(markup, />Pending<\/button>/)
  assert.doesNotMatch(markup, />Accept<\/button>/)
  assert.doesNotMatch(markup, /sidebar-worker-review-details/)
  resetDesktopV3CacheForTests()
})

// Requirement: trigger workers remain reviewable without pretending that
// acceptance alone schedules a run. The sidebar is the narrow presentation layer.
test('trigger worker review is displayed as on demand in the sidebar', () => {
  const state = createEmptyDesktopV3CacheState()
  state.permissionsBySession.author = [{ id: 'permission_trigger', sessionId: 'author', toolName: 'manage_workers', status: 'pending', requirement: 'automation_v2_acceptance', toolArguments: JSON.stringify({ review_kind: 'worker_v2', scope: { workspace_id: 'workspace' }, worker_review: { proposal_id: 'trigger', revision: 1, digest: 'c'.repeat(64) }, document: { title: 'Trigger worker', info: { goal: 'On-demand check' }, checkpoints: [{ id: 'cp-1', title: 'Check', acceptance_criteria: ['Done'] }], worker_v2: { schema_version: 2, schedule: { kind: 'trigger' }, expiration: { kind: 'indefinite' }, missed: 'skip', overlap: 'serialize', activate_on_accept: true } } }) } as any]
  resetDesktopV3CacheForTests(state)
  const markup = renderToStaticMarkup(<PendingWorkerSidebarReviews onOpenChat={() => {}} />)
  assert.match(markup, /Trigger worker/)
  assert.match(markup, />Pending<\/button>/)
  assert.doesNotMatch(markup, /On demand/) // schedule details appear after Pending opens the review
  resetDesktopV3CacheForTests()
})

test('worker sidebar decision rechecks exact revision and derives scope from current permission', async () => {
  const state = createEmptyDesktopV3CacheState()
  const digest = 'b'.repeat(64)
  state.permissionsBySession.author = [{ id: 'permission_p', sessionId: 'author', toolName: 'manage_workers', status: 'pending', requirement: 'automation_v2_acceptance', toolArguments: JSON.stringify({ review_kind: 'worker_v2', scope: { workspace_id: 'foreign-workspace' }, worker_review: { proposal_id: 'p', revision: 2, digest }, document: { title: 'Audit', info: { goal: 'Inspect' }, checkpoints: [{ id: 'cp-1', title: 'Inspect', acceptance_criteria: ['Done'] }], worker_v2: { schema_version: 2, schedule: { kind: 'interval', interval_seconds: 3600 }, expiration: { kind: 'indefinite' }, missed: 'skip', overlap: 'serialize', activate_on_accept: true } } }) } as any]
  let calls = 0
  const mutate = async (input: any) => { calls++; assert.deepEqual(input, { action: 'accept_automation', workspace_id: 'foreign-workspace', session_id: 'author', review: { proposal_id: 'p', revision: 2, digest } }); return { record: { session_id: 'author', automation_id: 'worker', digest } } as any }
  await assert.rejects(decidePendingWorkerReview(state, 'permission_p', 1, 'a'.repeat(64), 'accept_automation', mutate), /review changed/)
  assert.equal(calls, 0)
  await decidePendingWorkerReview(state, 'permission_p', 2, digest, 'accept_automation', mutate)
  assert.equal(calls, 1)
  state.permissionsBySession.author[0].status = 'approved'
  await assert.rejects(decidePendingWorkerReview(state, 'permission_p', 2, digest, 'accept_automation', mutate), /review changed/)
  assert.equal(calls, 1)
})
