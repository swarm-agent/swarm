import React from 'react'
import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import type { WorkerAutomation, WorkerRecord } from '../state/desktop-workers-api'
import { PendingWorkerCard } from './pending-worker-card'
import { proposalGoal, proposalWorkspaces } from './worker-proposal-presentation'
import { proposalJobTiming } from './worker-schedule'

// Requirement: proposals disclose factual goals, authorized targets, distinct job
// timing, access and exact instructions before exact-revision human acceptance.
// Threat: unknown targets look approved, cross-account names leak, mixed jobs look
// identical or long plans bury consent. Authority: PendingWorkerCard, canonical
// WorkerRecord and workspace overview catalog; pure presentation + SSR is the
// narrowest layer for deterministic summaries. Browser tests prove interaction.
const worker: WorkerRecord = {
  id: 'worker_review', account_scope_id: 'account', name: 'Repository reviewer',
  description: 'Review changes when asked', instructions: 'Full instructions.\nDo not modify files.',
  lifecycle_state: 'pending', revision: 3, created_at: 1, updated_at: 1,
  proposed_bindings: { primary: 'ws_alpha' }, workspace_requirements: [{ role: 'primary', required: true }],
  requested_capabilities: [{ type: 'fs', name: 'read', description: 'Read repository files', required: true }],
}
const catalog = { accountScopeId: 'account', workspaces: [{ workspaceId: 'ws_alpha', path: '/projects/alpha', workspaceName: 'Alpha' }] }
const job: WorkerAutomation = {
  id: 'job_audit', worker_id: worker.id, name: 'Dependency audit', description: 'Find vulnerabilities',
  activation_mode: 'cron', schedule: { kind: 'cron', cron: '0 9 * * *', timezone: 'Europe/Paris' },
  enabled: true, revision: 1, created_at: 1, updated_at: 1,
  plan_document: { title: 'Full audit plan', info: { goal: 'Produce findings', notes: 'Retain extra plan fields' }, checkpoints: [{ id: 'cp-1', title: 'Inspect', tasks: ['Read the entire lockfile'], acceptance_criteria: ['Every finding has evidence'], notes: 'Retain checkpoint notes' }] },
  input_requirements: [{ name: 'threshold', kind: 'string', required: true }],
  deliverable_requirements: [{ name: 'report', kind: 'markdown', required: true }],
}
const render = (changes: Partial<WorkerRecord> = {}, expanded = false, stale = false, mutationError?: string) => renderToStaticMarkup(<PendingWorkerCard worker={{ ...worker, ...changes }} accountScopeId="account" workspaceCatalog={catalog} initialExpanded={expanded} stale={stale} mutationError={mutationError} workspaceSlug="demo" />)

test('collapsed proposal contains goal, named target, access, wait behavior and discoverable consent', () => {
  const html = render()
  for (const text of ['Pending approval', 'Review changes when asked', 'Alpha', 'Proposed; not approved', 'Runs locally', 'No job attached; waits for a task after acceptance', 'Read repository files', 'not permission grants', 'View instructions and job plan', 'Accept worker']) assert.ok(html.includes(text), text)
  assert.ok(!html.includes('Full instructions.'))
  assert.ok(!html.includes('Approved Local Bindings'))
  assert.ok(!html.includes('revision 3'))
  assert.ok(!html.includes('pending-worker-expanded'))
  assert.match(render({ automations: [] }), /No job attached/)
  assert.match(render({ automations: null }), /No job attached/)
})

test('summary keeps each job purpose and start behavior distinct including disabled and unknown configuration', () => {
  const jobs = [job, { ...job, id: 'manual', name: 'Review on request', activation_mode: 'manual' as const, schedule: null }, { ...job, id: 'trigger', name: 'Event audit', activation_mode: 'external_trigger' as const, trigger: { trigger_kind: 'webhook' }, schedule: null }, { ...job, id: 'disabled', name: 'Paused audit', enabled: false }]
  const html = render({ automations: jobs })
  for (const text of ['Dependency audit', 'Find vulnerabilities', 'daily at 09:00 (Europe/Paris)', 'Review on request', 'On demand', 'not recurring', 'External trigger', 'webhook', 'Disabled; will not run']) assert.ok(html.includes(text), text)
  assert.ok(!html.includes('Full audit plan'))
  assert.ok(!html.includes('No job attached'))
  assert.match(proposalJobTiming({ ...job, activation_mode: 'interval', schedule: { kind: 'interval', interval_seconds: 3600, timezone: 'UTC' } }), /Recurring · every 1 hour \(UTC\)/)
  assert.match(proposalJobTiming({ ...job, activation_mode: 'interval', schedule: null }), /timing unavailable/)
  assert.match(proposalJobTiming({ ...job, activation_mode: 'unknown' as WorkerAutomation['activation_mode'] }), /configuration unavailable/)
})

test('workspace resolution handles multiple names, duplicate names, missing roles and account isolation without replacement', () => {
  const multiple = { ...catalog, workspaces: [...catalog.workspaces, { workspaceId: 'ws_beta', workspaceName: 'Alpha', path: '/projects/beta' }] }
  const record = { ...worker, proposed_bindings: { primary: 'ws_alpha', docs: 'ws_beta', deleted: 'ws_gone' }, workspace_requirements: [{ role: 'required', required: true }] }
  const targets = proposalWorkspaces(record, 'account', multiple)
  assert.deepEqual(targets.map(target => target.label), ['Alpha — /projects/alpha', 'Alpha — /projects/beta', 'Unresolved workspace (deleted)', 'Workspace required (required) — not assigned'])
  assert.ok(targets.slice(2).every(target => target.unresolved))
  const multipleHtml = renderToStaticMarkup(<PendingWorkerCard worker={record} accountScopeId="account" workspaceCatalog={multiple} />)
  for (const text of ['Workspaces', 'Alpha — /projects/alpha', 'Alpha — /projects/beta', 'Unresolved workspace (deleted)', 'not assigned']) assert.ok(multipleHtml.includes(text), text)
  assert.ok(proposalWorkspaces(worker, 'account', { ...catalog, accountScopeId: 'other' }).every(target => target.unresolved))
  assert.ok(proposalWorkspaces({ ...worker, account_scope_id: 'other' }, 'account', catalog).every(target => target.unresolved))
  assert.match(render({ proposed_bindings: { primary: 'ws_gone' } }), /Unresolved workspace/)
  assert.match(render({ proposed_bindings: {}, local_bindings: {} }), /not assigned/)
  assert.match(render({ proposed_bindings: {}, local_bindings: { primary: 'ws_alpha' } }), /Previously approved/)
})

test('expanded instructions and job plan retain unabridged text and secondary exact definitions', () => {
  const html = render({ automations: [job] }, true)
  for (const text of ['Full instructions.\nDo not modify files.', 'View full job plan', 'Full audit plan', 'Read the entire lockfile', 'Every finding has evidence', 'Retain extra plan fields', 'Retain checkpoint notes', 'threshold (string, required)', 'report (markdown, required)', 'Technical details and workspace roles', '/projects/alpha', 'revision 3']) assert.ok(html.includes(text), text)
  assert.match(html, /href="\/demo\/workers\/worker_review"/)
  const long = 'Detailed goal '.repeat(40)
  assert.equal(proposalGoal({ ...worker, description: long }).length, 198)
  assert.ok(render({ description: long }, true).includes(long))
})

test('stale, account mismatch and failed acceptance remain visible even collapsed', () => {
  assert.match(render({}, false, true), /disabled=""[^>]*data-testid="accept-pending-worker"/)
  assert.match(render({}, false, true), /Acceptance is blocked on stale revisions/)
  assert.match(render({}, false, false, 'Revision conflict'), /role="alert"[^>]*>Revision conflict/)
  assert.match(render({ account_scope_id: 'other' }), /disabled=""[^>]*data-testid="accept-pending-worker"/)
})
