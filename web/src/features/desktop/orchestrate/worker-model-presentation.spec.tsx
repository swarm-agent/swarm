import React from 'react'
import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { TaskCardSummary } from './task-card-summary'
import { PendingWorkerCard } from './pending-worker-card'
import type { RunningTask } from './orchestrate-types'
import type { WorkerRecord } from '../state/desktop-workers-api'

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
  const worker: WorkerRecord = { id: 'worker', account_scope_id: 'account', name: 'Named worker', instructions: 'Review', lifecycle_state: 'pending', revision: 1, created_at: 1, updated_at: 1, model_profile: { source: 'temporary', action: { provider: 'fixture', model: 'action', thinking: 'high' }, plan: { provider: 'fixture', model: 'planning' } } }
  const review = renderToStaticMarkup(<PendingWorkerCard worker={worker} accountScopeId="account" workspaceCatalog={{ accountScopeId: 'account', workspaces: [] }} />)
  assert.match(review, /fixture\/action/)
  assert.match(review, /Planning: fixture\/planning/)
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
