import test from 'node:test'
import assert from 'node:assert/strict'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { applyCacheEvent, applyDesktopV3LivePatchBatch, createEmptyDesktopV3CacheState, upsertCommittedMessage, upsertRunIntent } from './desktop-v3-cache-reducer'
import { selectRenderedSessionMessages } from './desktop-v3-cache-selectors'
import { buildDesktopV3ConversationRenderItems, DesktopV3RenderItemView, desktopV3RenderItemKey } from '../chat/components/desktop-v3-existing-conversation-pane'
import type { CacheEvent, DesktopV3CacheState } from './desktop-v3-cache-types'

// Requirement: a thinking activity keeps its causal position and stops appearing
// active after answer output or a terminal run, including delayed durable delivery.
// Regression: completion re-keys/reorders the row beneath text, and live patches
// or terminal intents leave pulsing thinking tags. Exercise the canonical reducer,
// selectors and real row renderer; no daemon, credentials or browser timing needed.
const sessionId = 'thinking-session'
const runId = 'thinking-run'
const identity = { run_id: runId, step: 1, step_id: 'step-1', reasoning_id: 'reason-1', reasoning_key: 'summary-1' }
function event(state: DesktopV3CacheState, type: string, seq: number, extra: Record<string, unknown> = {}) {
  const payload = { ...identity, recorded_at: seq * 100, ...extra }
  applyCacheEvent(state, {
    source: 'realtime', sessionId, eventType: type, payload,
    sessionEvent: { id: `event-${seq}`, session_id: sessionId, seq, event_type: type, payload, ts_unix_ms: seq * 100 },
  } as CacheEvent)
}
function start() {
  const state = createEmptyDesktopV3CacheState()
  event(state, 'session.reasoning.started', 10)
  event(state, 'session.reasoning.delta', 11, { delta: 'Consider the result.' })
  return state
}
function rows(state: DesktopV3CacheState) {
  return buildDesktopV3ConversationRenderItems(selectRenderedSessionMessages(state, sessionId)).filter((item) => item.type !== 'live-working')
}
function patch(state: DesktopV3CacheState, step = 1) {
  return applyDesktopV3LivePatchBatch(state, [{ session_id: sessionId, run_id: runId, stream_id: `assistant:${runId}:step:${step}`, stream_kind: 'assistant_text', operation: 'append', step, step_id: `step-${step}`, live_seq_start: 1, live_seq_end: 1, offset_start: 0, offset_end: 6, text: 'Answer', recorded_at: 2000 }])
}
function assertSettled(state: DesktopV3CacheState) {
  const thinking = rows(state).filter((item) => item.type === 'live-reasoning')
  assert.ok(thinking.length > 0)
  assert.ok(thinking.every((item) => item.state !== 'running'), 'no thinking row may remain running')
  const html = thinking.map((item, index) => renderToStaticMarkup(createElement(DesktopV3RenderItemView, { item, index, thinkingTagsEnabled: false }))).join('')
  assert.doesNotMatch(html, /pulse_3s/)
  assert.doesNotMatch(html, />Thinking<\/span>/)
}

test('thinking completion preserves row key and causal position before the answer', () => {
  const state = start()
  const initial = rows(state)[0]
  event(state, 'session.assistant.delta', 12, { delta: 'Answer', stream_id: 'answer', offset_start: 0, offset_end: 6 })
  event(state, 'session.reasoning.completed', 13, { summary: 'Consider the result.' })
  event(state, 'session.reasoning.completed', 13, { summary: 'Consider the result.' })
  upsertCommittedMessage(state, sessionId, { id: 'answer-message', session_id: sessionId, global_seq: 14, role: 'assistant', content: 'Answer', created_at: 1400, metadata: { run_id: runId, stream_id: 'answer', stream_start_seq: 12 } })
  const result = rows(state)
  assert.deepEqual(result.map((item) => item.type === 'message' ? item.message.role : item.type), ['reasoning', 'assistant'])
  assert.equal(result[0].timelineSeq, initial.timelineSeq)
  assert.equal(desktopV3RenderItemKey(result[0]), desktopV3RenderItemKey(initial))
  assert.equal(state.messagesBySession[sessionId].items.find((item) => item.role === 'reasoning')?.global_seq, 13, 'render ordering must not rewrite the canonical sequence')
})

test('live answer patches settle thinking without mutating the previous cache snapshot', () => {
  const before = start()
  const after = patch(before)
  assertSettled(after)
  assert.equal(before.liveRunsBySession[sessionId][runId].reasoning?.state, 'running')
  assert.equal(rows(after)[0].timelineSeq, 10)
  assert.equal(after.liveRunsBySession[sessionId][runId].assistantDraft?.content, 'Answer')
})

test('delayed reasoning deltas cannot restart thinking after same-step answer output', () => {
  const state = patch(start())
  event(state, 'session.reasoning.delta', 12, { delta: 'Consider the complete result.' })
  assertSettled(state)
  assert.equal(state.liveRunsBySession[sessionId][runId].reasoning?.text, 'Consider the complete result.')
})

test('late earlier-step answer patches do not settle current thinking', () => {
  const state = start()
  event(state, 'session.reasoning.started', 12, { step: 2, step_id: 'step-2', reasoning_id: 'reason-2', reasoning_key: 'summary-2' })
  const after = patch(state)
  assert.equal(after.liveRunsBySession[sessionId][runId].reasoning?.state, 'running')
  assert.equal(after.liveRunsBySession[sessionId][runId].reasoning?.step, 2)
})

for (const status of ['completed', 'failed', 'cancelled', 'interrupted', 'expired'] as const) {
  test(`terminal ${status} settles every outstanding thinking row, including replay`, () => {
    const state = start()
    event(state, 'session.reasoning.started', 12, { reasoning_id: 'reason-2', reasoning_key: 'summary-2' })
    upsertRunIntent(state, sessionId, { run_id: runId, session_id: sessionId, status, event_seq: 20, created_at: 100, updated_at: 2000, completed_at: 2000 })
    assertSettled(state)
    event(state, 'session.reasoning.delta', 13, { delta: 'Late retained reasoning.' })
    assertSettled(state)
    assert.equal(state.liveRunsBySession[sessionId][runId].status, status)
  })
}

test('tool construction settles preceding thinking but not newer reasoning during replay', () => {
  const state = start()
  event(state, 'session.provider_tool_call.started', 12, { call_id: 'call-1', tool_name: 'read' })
  assertSettled(state)
  event(state, 'session.reasoning.started', 14, { step: 2, step_id: 'step-2', reasoning_id: 'reason-2', reasoning_key: 'summary-2' })
  event(state, 'session.tool.completed', 13, { call_id: 'call-1', tool_name: 'read' })
  assert.equal(state.liveRunsBySession[sessionId][runId].reasoning?.state, 'running')
  event(state, 'session.tool.completed', 15, { call_id: 'call-1', tool_name: 'read' })
  assert.equal(state.liveRunsBySession[sessionId][runId].reasoning?.state, 'running', 'older-step tool output must not stop current reasoning')
})

test('late reasoning after terminal overlay cleanup inherits the durable terminal status', () => {
  const state = createEmptyDesktopV3CacheState()
  upsertRunIntent(state, sessionId, { run_id: runId, session_id: sessionId, status: 'completed', event_seq: 20, created_at: 100, updated_at: 2000, completed_at: 2000 })
  assert.equal(state.liveRunsBySession[sessionId], undefined)
  event(state, 'session.reasoning.delta', 11, { delta: 'Late retained reasoning.' })
  assertSettled(state)
})

test('a committed terminal answer settles retained thinking even without a run-intent frame', () => {
  const state = start()
  upsertCommittedMessage(state, sessionId, { id: 'terminal-answer', session_id: sessionId, global_seq: 20, role: 'assistant', content: 'Answer', created_at: 2000 }, runId, 'completed')
  assertSettled(state)
})

test('reasoning identities from different runs never share a render key', () => {
  const state = start()
  event(state, 'session.reasoning.delta', 12, { run_id: 'another-run', delta: 'Different thought.' })
  const keys = rows(state).map(desktopV3RenderItemKey)
  assert.equal(new Set(keys).size, keys.length)
})

test('late reasoning for an unseen identity is settled by already visible same-step text', () => {
  const state = patch(createEmptyDesktopV3CacheState())
  event(state, 'session.reasoning.delta', 11, { delta: 'Delayed thought.' })
  assertSettled(state)
})
