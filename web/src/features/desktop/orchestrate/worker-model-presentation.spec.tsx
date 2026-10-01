import React from 'react'
import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { TaskCardSummary } from './task-card-summary'
import { PendingWorkerCard } from './pending-worker-card'
import type { RunningTask } from './orchestrate-types'
import type { WorkerRecord } from '../state/desktop-workers-api'
import { ensureDesktopSession } from '../../../app/api'

// SSR model disclosure requires the same authenticated account as the mounted worker.
test.before(async () => {
  const original = globalThis.fetch
  globalThis.fetch = async () => new Response(JSON.stringify({ user_id: 'owner', account_scope_id: 'account' }), { headers: { 'Content-Type': 'application/json' } })
  try { await ensureDesktopSession(true) } finally { globalThis.fetch = original }
})

// Requirement: only durable worker-linked tasks have Worker presentation, and
// review discloses the saved action/planning selection. Threat: changing runtime
// agent identity or accidentally relabeling ordinary Swarm sessions. SSR at the
// shared task summary and review card is the narrowest observable render layer.
test('worker identity and pinned model review remain scoped to workers', () => {
  const task = { id: 'task', title: 'Job title', agentType: 'swarm', status: 'queued', activeAgent: 'Swarm' } as RunningTask
  const ordinary = renderToStaticMarkup(<TaskCardSummary task={task} />)
  assert.match(ordinary, /Job title/)
  assert.match(ordinary, /Swarm/)
  assert.doesNotMatch(ordinary, />Worker/)
  const linked = renderToStaticMarkup(<TaskCardSummary task={{ ...task, worker_id: 'worker', worker_name: 'Named worker' }} />)
  assert.match(linked, /Named worker/)
  assert.match(linked, />Worker/)
  assert.doesNotMatch(linked, />Swarm/)
  const nameOnly = renderToStaticMarkup(<TaskCardSummary task={{ ...task, worker_name: 'Unlinked name' }} />)
  assert.match(nameOnly, /Job title/)
  assert.match(nameOnly, />Swarm/)
  assert.doesNotMatch(nameOnly, />Worker|Unlinked name/)
  assert.equal(task.activeAgent, 'Swarm') // presentation never changes runtime identity
  const worker: WorkerRecord = { id: 'worker', account_scope_id: 'account', name: 'Named worker', instructions: 'Review', lifecycle_state: 'pending', revision: 1, created_at: 1, updated_at: 1, model_profile: { source: 'temporary', action: { provider: 'fixture', model: 'action', thinking: 'high' }, plan: { provider: 'fixture', model: 'planning' } } }
  const review = renderToStaticMarkup(<PendingWorkerCard worker={worker} accountScopeId="account" workspaceCatalog={{ accountScopeId: 'account', workspaces: [] }} />)
  assert.match(review, /title="action"/)
  assert.match(review, /title="planning"/)
  assert.equal((review.match(/aria-label="Execution model"/g) || []).length, 1)
  assert.equal((review.match(/aria-label="Planning model"/g) || []).length, 1)
  assert.match(review, /Nothing runs while this worker is pending/)
})

// Requirement: action-only edits must not silently replace an explicit Plan model.
// Threat: the picker mutates both roles. The pure picker transformation is the
// narrowest layer proving preservation without providers or browser automation.
test('action model edits preserve the independent Plan selection', async () => {
  const { selectWorkerActionModel } = await import('./worker-model-picker')
  const plan = { provider: 'fixture', model: 'plan', thinking: 'high' }
  const profile = { source: 'account_default', action: { provider: 'fixture', model: 'old' }, plan }
  const next = selectWorkerActionModel(profile, { provider: 'fixture', model: 'new' })
  assert.deepEqual(next.plan, plan)
  assert.equal(next.action.model, 'new')
  assert.equal(next.use_account_default, false)
  assert.equal(profile.action.model, 'old')
})

// Requirement: active workers expose staged jobs with review on the same identity;
// unresolved or foreign workspace names must never masquerade as authorized.
// Authority: PendingWorkerCard/proposalWorkspaces; SSR proves consent wording and
// candidate rendering, not execution. Two jobs include the 300-second hello case.
test('active worker update review discloses candidates, approved work and authorized path', () => {
  const base: WorkerRecord = { id: 'stable-worker', account_scope_id: 'account', name: 'Stable', instructions: 'Approved instructions', lifecycle_state: 'active', revision: 4, created_at: 1, updated_at: 4, local_bindings: { primary: 'workspace' }, authorized_workspaces: { primary: { workspace_id: 'workspace', available: true, name: 'Project', path: '/projects/example' } } }
  const job = { id: 'hello', worker_id: base.id, name: 'Hello every five minutes', activation_mode: 'interval' as const, schedule: { kind: 'interval' as const, interval_seconds: 300 }, enabled: true, revision: 1, created_at: 4, updated_at: 4, plan_document: { title: 'Hello' } }
  const worker = { ...base, pending_review: { ...base, instructions: 'Proposed instructions', proposed_bindings: { primary: 'workspace' }, automations: [job, { ...job, id: 'second', name: 'Second job' }] } }
  const markup = renderToStaticMarkup(<PendingWorkerCard worker={worker} accountScopeId="account" workspaceCatalog={{ accountScopeId: 'account', workspaces: [] }} />)
  for (const text of ['Update to the same worker', 'Accept changes', 'Hello every five minutes', 'Second job', 'Project — /projects/example', 'Approved jobs: None', 'Proposed changes cannot run before acceptance']) assert.ok(markup.includes(text), text)
  assert.doesNotMatch(markup, />Accept worker</)
  const stale = renderToStaticMarkup(<PendingWorkerCard worker={worker} accountScopeId="account" workspaceCatalog={{ accountScopeId: 'account', workspaces: [] }} stale />)
  assert.match(stale, /disabled=""[^>]*data-testid="accept-pending-worker"/)
  const foreign = renderToStaticMarkup(<PendingWorkerCard worker={worker} accountScopeId="foreign" workspaceCatalog={{ accountScopeId: 'foreign', workspaces: [] }} />)
  assert.doesNotMatch(foreign, /Project — \/projects\/example/)
  assert.match(foreign, /Acceptance is blocked/)
})

// Requirement: inherited slots stay visible, while legacy pinned settings must
// not be mislabeled as following defaults. WorkerModelPicker SSR is the narrowest
// observable layer proving both roles and truthful policy labels.
test('legacy captured Swarm settings remain explicit until reset', async () => {
  const { WorkerModelPicker } = await import('./worker-model-picker')
  const html = renderToStaticMarkup(<WorkerModelPicker accountScopeId="account" profile={{ source: 'swarm_settings', action: { provider: 'fixture', model: 'action' }, plan: { provider: 'fixture', model: 'plan' } }} disabled onChange={() => { throw new Error('render changed selection') }} />)
  assert.match(html, /aria-label="Execution model"/)
  assert.match(html, /aria-label="Planning model"/)
  assert.equal((html.match(/Worker override/g) || []).length, 2)
  assert.match(html, /title="action"/)
  assert.match(html, /title="plan"/)
  assert.equal((html.match(/aria-label="Change (Execution|Planning) model"/g) || []).length, 2)
  assert.doesNotMatch(html, /Select model/)
})

// Requirement: override/reset is slot-local and does not edit account settings.
// Threat: selecting one role pins the other inherited role; reset leaves stale
// overrides active. Pure picker transformations are the narrowest policy layer.
test('inherited slots survive overrides and reset independently', async () => {
  const { selectWorkerActionModel, resetWorkerModelSlot, workerSlotInherited, WorkerModelPicker } = await import('./worker-model-picker')
  const profile = { source: 'swarm_settings', use_account_default: true, action: { provider: 'fixture', model: 'old' }, plan: { provider: 'fixture', model: 'old-plan' } }
  const override = selectWorkerActionModel(profile, { provider: 'fixture', model: 'override' })
  assert.equal(workerSlotInherited(override, 'action'), false)
  assert.equal(workerSlotInherited(override, 'plan'), true)
  const reset = resetWorkerModelSlot(override, 'action')
  assert.equal(workerSlotInherited(reset, 'action'), true)
  assert.equal(workerSlotInherited(reset, 'plan'), true)
  assert.equal(profile.use_account_default, true)
  assert.equal(profile.action.model, 'old')
  const html = renderToStaticMarkup(<WorkerModelPicker accountScopeId="account" disabled onChange={() => assert.fail('render mutated')} />)
  assert.equal((html.match(/Account default/g) || []).length, 2)
  assert.equal((html.match(/Loading…/g) || []).length, 2)
  assert.match(html, /aria-label="Execution model"/)
  assert.match(html, /aria-label="Planning model"/)
})

// Requirement: top-level settings show pending draft separately from approval;
// rendering must neither authorize nor activate a worker. SSR is the narrowest
// WorkerSettingsReview consent/presentation boundary (backend tests own CAS).
test('execution and models are visible without disclosure and pending acceptance stays explicit', async () => {
  const { WorkerSettingsReview } = await import('./worker-settings-review')
  const worker: WorkerRecord = { id: 'worker_fixture', account_scope_id: 'account', name: 'Fixture', instructions: 'Review', lifecycle_state: 'active', revision: 2, created_at: 1, updated_at: 2 }
  const html = renderToStaticMarkup(<WorkerSettingsReview worker={{ ...worker, pending_review: { ...worker, execution_mode: 'plan' } }} accountScopeId="account" disabled />)
  assert.match(html, /Planning model · plans first/)
  assert.match(html, />Swarm</)
  assert.match(html, />Plan</)
  assert.match(html, /Pending approval · approved settings remain in effect/)
  assert.match(html, /Execution<\/span>: Swarm → Plan/)
  assert.doesNotMatch(html, /Propose changes|Local draft/)
  assert.doesNotMatch(html, />Accept|>Activate/)
})

// Requirement: the dashboard makes models, source and thinking legible without
// opening redundant selectors; approved and pending values cannot look live at
// the same time. WorkerSettingsReview/WorkerModelPicker SSR is the narrowest
// hierarchy/consent layer; it does not claim pixel or browser-interaction proof.
test('dashboard presents compact execution and secondary planning and distinguishes approved from pending', async () => {
  const { WorkerSettingsReview } = await import('./worker-settings-review')
  const profile = { source: 'temporary', action: { provider: 'fixture', model: 'identifiable-long-action-model-name', thinking: 'high', service_tier: 'priority', context_mode: 'extended' }, plan: { provider: 'fixture', model: 'planning-model', thinking: 'low' } }
  const base: WorkerRecord = { id: 'dashboard', account_scope_id: 'account', name: 'Fixture', instructions: 'Work', lifecycle_state: 'active', revision: 2, created_at: 1, updated_at: 2, model_profile: profile }
  const approved = renderToStaticMarkup(<WorkerSettingsReview worker={base} accountScopeId="account" disabled />)
  assert.match(approved, />Approved</)
  assert.match(approved, /aria-label="Execution mode"/)
  assert.match(approved, /aria-pressed="true"[^>]*>Swarm</)
  assert.match(approved, /title="identifiable-long-action-model-name"/)
  assert.match(approved, /title="planning-model"/)
  assert.match(approved, /Thinking · high/)
  assert.match(approved, /Tier · priority/)
  assert.match(approved, /Context · extended/)
  assert.match(approved, /only used in Plan mode/)
  assert.match(approved, /aria-label="Change Execution model"/)
  assert.equal((approved.match(/Worker override/g) || []).length, 2)
  assert.doesNotMatch(approved, /Select model|Propose changes|Approved model policy|Pending approval/)
  const candidate = { ...base, execution_mode: 'plan' as const, model_profile: { ...profile, action: { ...profile.action, model: 'proposed-action' } } }
  const pending = renderToStaticMarkup(<WorkerSettingsReview worker={{ ...base, revision: 3, pending_review: candidate }} accountScopeId="account" disabled />)
  assert.match(pending, />Pending approval</)
  assert.match(pending, /approved settings remain in effect/)
  assert.match(pending, /identifiable-long-action-model-name.*→.*proposed-action/)
  assert.match(pending, /aria-pressed="true"[^>]*>Plan</)
  assert.doesNotMatch(pending, />Accept|>Activate|Propose changes/)
  const unaccepted = renderToStaticMarkup(<WorkerSettingsReview worker={{ ...base, lifecycle_state: 'pending' }} accountScopeId="account" disabled />)
  assert.match(unaccepted, /Not yet accepted · nothing runs/)
  assert.doesNotMatch(unaccepted, />Approved</)
})

// Requirement: all pending review entry points reuse the model-first dashboard;
// hidden detail controls must not permit acceptance of unsaved external edits.
// Threat: the old picker remains on review cards or consent bypasses draft state.
// PendingWorkerCard SSR is the narrowest component wiring/disabled-state proof;
// interactive and server tests separately own payload and persistence assertions.
test('pending review shares execution dashboard and honors external draft guard', () => {
  const worker: WorkerRecord = { id: 'review-fixture', account_scope_id: 'account', name: 'Review', instructions: 'Review', lifecycle_state: 'paused', execution_mode: 'auto', revision: 3, created_at: 1, updated_at: 1, model_profile: { source: 'temporary', action: { provider: 'fixture', model: 'approved', thinking: 'high' }, plan: { provider: 'fixture', model: 'planning' } } }
  worker.pending_review = { ...worker, lifecycle_state: 'pending', execution_mode: 'plan', model_profile: { ...worker.model_profile!, action: { provider: 'fixture', model: 'candidate', thinking: 'medium' } } }
  const props = { worker, accountScopeId: 'account', workspaceCatalog: { accountScopeId: 'account', workspaces: [] } }
  const review = renderToStaticMarkup(<PendingWorkerCard {...props} />)
  assert.equal((review.match(/aria-label="Execution and model settings"/g) || []).length, 1)
  assert.match(review, /aria-pressed="true"[^>]*>Plan</)
  assert.match(review, /approved.*→.*candidate/)
  assert.match(review, /Thinking · medium/)
  assert.match(review, />Accept changes</)
  const detail = renderToStaticMarkup(<PendingWorkerCard {...props} showModelControls={false} acceptanceBlocked />)
  assert.doesNotMatch(detail, /aria-label="Worker models"/)
  assert.match(detail, /disabled=""[^>]*data-testid="accept-pending-worker"/)
  assert.match(detail, /Propose or discard your local model edits/)
})
