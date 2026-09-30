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
