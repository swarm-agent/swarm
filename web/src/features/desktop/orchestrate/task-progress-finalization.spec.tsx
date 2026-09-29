import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { applyBootstrapSnapshot, applyCacheEvent, applyHydrateSnapshot, createEmptyDesktopV3CacheState, upsertRunIntent } from '../state/desktop-v3-cache-reducer'
import { snapshotFixture, hydrateSnapshotFixture, sessionA, runIntentA } from '../state/desktop-v3-cache.backend-fixtures'
import { aggregateTaskLiveState } from './orchestrate-task-helpers'
import { TaskCardSummary } from './task-card-summary'
import type { RunningTask } from './orchestrate-types'

// Requirement: durable checklist events and terminal run receipts must reach the card,
// not be masked by running hydration. This reducer/selector/render test is the narrowest
// layer exercising the real cache boundary without a provider or a browser transport.
// It does not claim live-provider, integration, or deployment validation.
test('durable progress renders checklist and final review without stale activity', () => {
  const state = applyBootstrapSnapshot(createEmptyDesktopV3CacheState(), snapshotFixture())
  const task: RunningTask = { id: 'task', title: 'Fix', agentType: 'coder', status: 'running',
    sessionId: sessionA.id, workspaceTarget: 'workspace', elapsed: '', subtasks: [] }
  const running = { ...runIntentA, status: 'running', event_seq: 10 }
  upsertRunIntent(state, sessionA.id, running)
  const card = () => aggregateTaskLiveState(task, { [sessionA.id]: {
    sessionRecord: state.sessionsById[sessionA.id], view: state.sessionViewsById[sessionA.id],
    intent: state.currentRunIntentBySession[sessionA.id],
  } })
  const update = (seq: number, status: string) => applyCacheEvent(state, {
    source: 'realtime', sessionId: sessionA.id, eventType: 'session.updated',
    projection: { session_id: sessionA.id, last_event_seq: seq, projection_high_watermark_seq: seq, updated_at: seq },
    payload: { session: { ...sessionA, metadata: { task_todos: [{ id: 'one', title: 'Implement fix', status }] } } },
    sessionEvent: { id: `progress-${seq}`, session_id: sessionA.id, seq, event_type: 'session.updated', ts_unix_ms: seq,
      payload: { session: { ...sessionA, metadata: { task_todos: [{ id: 'one', title: 'Implement fix', status }] } } } },
  })
  update(11, 'pending')
  assert.equal(card().taskTodos?.[0].status, 'pending')
  update(12, 'in_progress')
  assert.equal(card().activeTodo, 'Implement fix')
  assert.match(renderToStaticMarkup(<TaskCardSummary task={card()} />), /Implement fix/)
  update(13, 'completed')
  assert.equal(card().subtasks[0].completed, true)
  assert.equal(card().status, 'running', 'checklist completion is not run completion')
  const terminal = { ...running, status: 'completed', event_seq: 14 }
  applyCacheEvent(state, { source: 'realtime', sessionId: sessionA.id, eventType: 'session.run.completed',
    payload: { run_intent: terminal }, sessionEvent: { id: 'terminal', session_id: sessionA.id,
      seq: 14, event_type: 'session.run.completed', ts_unix_ms: 14, payload: { run_intent: terminal } } })
  for (const replay of [terminal, running]) upsertRunIntent(state, sessionA.id, replay)
  assert.equal(state.sessionViewsById[sessionA.id]?.current_run_state?.status, 'completed')
  applyHydrateSnapshot(state, hydrateSnapshotFixture({
    sessions_by_id: {}, projections_by_session: {}, messages_by_session: {}, session_order: [sessionA.id],
    selector: { kind: 'session_ids', session_ids: [sessionA.id] },
    sync_scope: { surface: 'desktop', stream_kind: 'v3.sync.snapshot', selector_filter_hash: 'progress', resource_set: 'current_run_state' },
    scope_id: 'progress:current_run_state',
    session_views_by_id: { [sessionA.id]: { current_run_state: { ...running, active: true } } },
    current_run_state_by_session: { [sessionA.id]: { ...running, active: true } },
    run_intents_by_session: { [sessionA.id]: [running] },
  }), [sessionA.id])
  assert.equal(card().status, 'needs_review')
  assert.equal(card().activeTodo, undefined)
  const html = renderToStaticMarkup(<TaskCardSummary task={card()} />)
  assert.match(html, /Agent checklist/)
  assert.doesNotMatch(html, /Live session activity|Working/)
  assert.notEqual(card().status, 'completed')
})

// Requirement: late completion of an older attempt must not stop a successor;
// failure/cancellation/blocking must remain distinct from review in the same cache path.
test('run evidence protects successor and preserves unsuccessful terminal states', () => {
  for (const [status, expected] of [['failed', 'failed'], ['cancelled', 'failed'], ['dispatch_blocked', 'blocked']]) {
    const state = applyBootstrapSnapshot(createEmptyDesktopV3CacheState(), snapshotFixture())
    const newer = { ...runIntentA, run_id: 'successor', status: 'running', created_at: runIntentA.created_at + 100, event_seq: 20 }
    upsertRunIntent(state, sessionA.id, newer)
    upsertRunIntent(state, sessionA.id, { ...runIntentA, status: 'completed', event_seq: 21 })
    assert.equal(state.currentRunIntentBySession[sessionA.id]?.run_id, 'successor')
    assert.equal(state.sessionViewsById[sessionA.id]?.current_run_state?.status, 'running')
    upsertRunIntent(state, sessionA.id, { ...newer, status, event_seq: 22 })
    const result = aggregateTaskLiveState({ id: 'task', title: 'Fix', agentType: 'coder', status: 'running',
      sessionId: sessionA.id, workspaceTarget: 'workspace', elapsed: '', subtasks: [] }, {
      [sessionA.id]: { sessionRecord: state.sessionsById[sessionA.id], view: state.sessionViewsById[sessionA.id] },
    })
    assert.equal(result.status, expected)
    assert.equal(result.activeTodo, undefined)
  }
})
