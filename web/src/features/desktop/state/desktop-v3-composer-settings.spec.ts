import assert from 'node:assert/strict'
import test from 'node:test'
import { applyRealtimeFrame, applySessionCreateMutationResult, createEmptyDesktopV3CacheState, desktopV3CacheReducer } from './desktop-v3-cache-reducer'
import type { DesktopV3AgenticSettings, DesktopV3ComposerSettingsTuple, SessionCreateMutationResponse, SessionSnapshot } from './desktop-v3-cache-types'

const preference = (model: string) => ({ provider: 'provider', model, thinking: 'medium', serviceTier: '', contextMode: '', updatedAt: 1 })
const tuple = (mode: 'auto' | 'plan', model: string): DesktopV3ComposerSettingsTuple => ({
  mode,
  agentName: 'swarm',
  resolvedAgentName: 'swarm',
  runtimeMode: 'plan_auto',
  storedPreference: preference(model),
  effectivePreference: preference(model),
  agentModelPolicy: null,
  contextWindow: 100,
  maxOutputTokens: 10,
})
const canonical = (mode: 'auto' | 'plan', model: string, seq: number): DesktopV3AgenticSettings => ({
  mode,
  agent_name: 'swarm',
  resolved_agent_name: 'swarm',
  runtime_mode: 'plan_auto',
  stored_preference: preference(model),
  effective_preference: preference(model),
  projection_seq: seq,
})

function reduce(state: ReturnType<typeof createEmptyDesktopV3CacheState>, action: Parameters<typeof desktopV3CacheReducer>[1]) {
  return desktopV3CacheReducer(state, action)
}

function observeTuple(state: ReturnType<typeof createEmptyDesktopV3CacheState>, sessionId: string): string {
  const settings = state.composerSettingsBySession[sessionId]
  if (!settings?.tuple) return 'loading'
  return `${settings.tuple.mode}:${settings.tuple.agentName}:${settings.tuple.effectivePreference.model}`
}

function assertOnlyCoherentTuples(observed: string[], allowed: string[]): void {
  assert.ok(observed.length > 0)
  for (const rendered of observed) assert.ok(rendered === 'loading' || allowed.includes(rendered), `incoherent rendered tuple: ${rendered}`)
}

test('create priming atomically installs Plan split-model settings and authoritative empty tail', () => {
  const state = createEmptyDesktopV3CacheState()
  const session: SessionSnapshot = { id: 'created', workspace_path: '/workspace', workspace_name: 'workspace', title: 'Created', mode: 'plan', created_at: 1, updated_at: 1, message_count: 0, last_message_at: 0, metadata: { agent_name: 'swarm', resolved_agent_name: 'swarm' } }
  const response: SessionCreateMutationResponse = {
    ok: true,
    session_id: session.id,
    session,
    projection: { session_id: session.id, last_event_seq: 1, projection_high_watermark_seq: 1, updated_at: 1 },
    agentic_settings: canonical('plan', 'plan-model', 1),
    messages: [],
    mutation: {},
    realtime_outbox: null,
  }
  applySessionCreateMutationResult(state, response, 'scope')
  assert.equal(state.composerSettingsBySession[session.id].tuple?.mode, 'plan')
  assert.equal(state.composerSettingsBySession[session.id].tuple?.effectivePreference.model, 'plan-model')
  assert.equal(state.messagesBySession[session.id].knownFull, true)
  assert.deepEqual(state.messagesBySession[session.id].items, [])
})

test('discovery prefers the durable event session and partial equal-projection shells cannot clear settings', () => {
  const state = createEmptyDesktopV3CacheState()
  const durable: SessionSnapshot = { id: 'discovered', workspace_path: '/workspace', workspace_name: 'workspace', title: 'Durable', mode: 'auto', preference: { provider: 'provider', model: 'auto-model', thinking: 'medium' }, created_at: 1, updated_at: 1, message_count: 0, last_message_at: 0, metadata: { agent_name: 'swarm', resolved_agent_name: 'swarm', runtime_mode: 'plan_auto' } }
  applyRealtimeFrame(state, { frame: {
    kind: 'workset.session.discovered', workset_id: 'scope', session_id: durable.id,
    projection: { session_id: durable.id, last_event_seq: 1, projection_high_watermark_seq: 1, updated_at: 1 },
    session: { ...durable, mode: '', preference: undefined, metadata: {} },
    event: { id: 'created', session_id: durable.id, seq: 1, event_type: 'session.created', payload: { session: durable }, ts_unix_ms: 1 },
  } })
  const installed = state.sessionsById[durable.id]
  assert.equal(installed?.kind === 'full' ? installed.session.mode : '', 'auto')
  assert.deepEqual(installed?.kind === 'full' ? installed.session.preference : undefined, durable.preference)
  assert.deepEqual(installed?.kind === 'full' ? installed.session.metadata : undefined, durable.metadata)
})

test('pending requests never replace the rendered server tuple', () => {
  const state = createEmptyDesktopV3CacheState()
  reduce(state, { type: 'composerSettings.installCanonical', sessionId: 's', settings: canonical('auto', 'auto-model', 1), projectionSeq: 1 })
  reduce(state, { type: 'composerSettings.requestStarted', sessionId: 's', mutationId: 'm1', createdAt: 1 })
  assert.equal(observeTuple(state, 's'), 'auto:swarm:auto-model')
  assert.deepEqual(state.composerSettingsBySession.s.tuple, state.composerSettingsBySession.s.canonicalTuple)
  assert.equal(state.composerSettingsBySession.s.pending.length, 1)
})

test('only a fresh server projection changes settings while a request is pending', () => {
  const state = createEmptyDesktopV3CacheState()
  const observed = [observeTuple(state, 's')]
  reduce(state, { type: 'composerSettings.installCanonical', sessionId: 's', settings: canonical('auto', 'auto-model', 1), projectionSeq: 1 })
  observed.push(observeTuple(state, 's'))
  reduce(state, { type: 'composerSettings.requestStarted', sessionId: 's', mutationId: 'm1', createdAt: 1 })
  observed.push(observeTuple(state, 's'))
  reduce(state, { type: 'composerSettings.installCanonical', sessionId: 's', settings: canonical('auto', 'stale-model', 0), projectionSeq: 0 })
  observed.push(observeTuple(state, 's'))
  reduce(state, { type: 'composerSettings.installCanonical', sessionId: 's', settings: canonical('plan', 'plan-model', 2), projectionSeq: 2 })
  observed.push(observeTuple(state, 's'))
  reduce(state, { type: 'composerSettings.requestFinished', sessionId: 's', mutationId: 'm1' })
  assert.deepEqual(observed, [
    'loading',
    'auto:swarm:auto-model',
    'auto:swarm:auto-model',
    'auto:swarm:auto-model',
    'plan:swarm:plan-model',
  ])
  assertOnlyCoherentTuples(observed, ['auto:swarm:auto-model', 'plan:swarm:plan-model'])
  assert.equal(state.composerSettingsBySession.s.pending.length, 0)
})

test('mutation rejection preserves canonical settings and exposes the failure', () => {
  const state = createEmptyDesktopV3CacheState()
  reduce(state, { type: 'composerSettings.installCanonical', sessionId: 's', settings: canonical('auto', 'auto-model', 1), projectionSeq: 1 })
  reduce(state, { type: 'composerSettings.requestStarted', sessionId: 's', mutationId: 'm1', createdAt: 1 })
  reduce(state, { type: 'composerSettings.reject', sessionId: 's', mutationId: 'm1', error: 'server rejected update' })
  assert.equal(observeTuple(state, 's'), 'auto:swarm:auto-model')
  assert.equal(state.composerSettingsBySession.s.error, 'server rejected update')
  assert.equal(state.composerSettingsBySession.s.pending.length, 0)
})

test('missing canonical inputs remain absent rather than inventing Auto settings', () => {
  const state = createEmptyDesktopV3CacheState()
  reduce(state, { type: 'composerSettings.installCanonical', sessionId: 's', settings: { ...canonical('auto', 'auto-model', 1), mode: '' } })
  assert.equal(state.composerSettingsBySession.s, undefined)
})
