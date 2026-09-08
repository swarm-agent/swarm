import test from 'node:test'
import assert from 'node:assert/strict'
import { applyCacheEvent, applyRealtimeFrame, applyDesktopV3LivePatchBatch, createEmptyDesktopV3CacheState } from './desktop-v3-cache-reducer'
import { selectRenderedSessionMessages } from './desktop-v3-cache-selectors'
import { buildDesktopV3ConversationRenderItems, desktopV3RenderItemKey } from '../chat/components/desktop-v3-existing-conversation-pane'
import type { CacheEvent, RealtimeMessage } from './desktop-v3-cache-types'
import { DesktopV3LivePatchCoordinator } from '../realtime/v3-live-patch-coordinator'

// Requirement: durable reconciliation fixes a speculative position once, then
// freezes it through later chunks, tools and commit. Prevent moving text to the
// latest event or losing its row key. Reducer + selector + renderer is the narrow
// deterministic integration layer; malformed overlap must pause without moving
// the row or publishing corrupt text.
function event(seq: number, eventType: string, payload: CacheEvent['payload']): CacheEvent {
  return {
    source: 'realtime', sessionId: 'session-1', eventType,
    payload: { run_id: 'run-1', ...payload },
    sessionEvent: { id: `event-${seq}`, session_id: 'session-1', seq, event_type: eventType, ts_unix_ms: seq, payload },
  }
}
const stream = 'assistant:run-1:step:1'

test('assistant stream keeps its first durable slot across tool boundaries and final commit', () => {
  const state = applyDesktopV3LivePatchBatch(createEmptyDesktopV3CacheState(), [{
    session_id: 'session-1', run_id: 'run-1', stream_id: stream,
    stream_kind: 'assistant_text', operation: 'append', step: 1, step_id: 'step-1',
    live_seq_start: 1, live_seq_end: 1, offset_start: 0, offset_end: 3, text: 'one', recorded_at: 1,
  }])
  const visible = () => buildDesktopV3ConversationRenderItems(selectRenderedSessionMessages(state, 'session-1'))
    .filter((item) => item.type !== 'live-working')
  const key = desktopV3RenderItemKey(visible()[0])
  assert.equal(state.liveRunsBySession['session-1']['run-1'].assistantDraft?.timelineSeq, 1)
  applyCacheEvent(state, event(10, 'session.assistant.delta', { stream_id: stream, delta: 'one', offset_start: 0, offset_end: 3 }))
  assert.equal(visible()[0].timelineSeq, 10)
  applyCacheEvent(state, event(20, 'session.provider_tool_call.started', { call_id: 'call-edit', tool_name: 'edit', step: 1 }))
  applyCacheEvent(state, event(30, 'session.assistant.delta', { stream_id: stream, delta: ' two', offset_start: 3, offset_end: 7 }))
  assert.deepEqual(visible().map(desktopV3RenderItemKey), [key, 'live-tool:call-edit'])
  assert.equal(visible()[0].timelineSeq, 10)
  assert.equal(state.liveRunsBySession['session-1']['run-1'].assistantSegments?.[0].content, 'one two')
  applyCacheEvent(state, event(40, 'session.message.appended', { message: {
    id: 'message-1', session_id: 'session-1', role: 'assistant', content: 'one two', created_at: 40, global_seq: 40,
    metadata: { run_id: 'run-1', stream_id: stream, stream_start_seq: 10 },
  } }))
  assert.deepEqual(visible().map(desktopV3RenderItemKey), [key, 'live-tool:call-edit'])
  assert.equal(visible()[0].timelineSeq, 10)
  assert.equal(visible()[0].type, 'message')
})

test('conflicting durable overlap pauses the stream without changing text or its anchor', () => {
  const state = createEmptyDesktopV3CacheState()
  applyCacheEvent(state, event(10, 'session.assistant.delta', { stream_id: stream, delta: 'one', offset_start: 0, offset_end: 3 }))
  applyCacheEvent(state, event(20, 'session.assistant.delta', { stream_id: stream, delta: 'bad', offset_start: 0, offset_end: 3 }))
  const draft = state.liveRunsBySession['session-1']['run-1'].assistantDraft
  assert.equal(draft?.content, 'one')
  assert.equal(draft?.timelineSeq, 10)
  assert.equal(draft?.livePaused, true)
})

test('unidentified assistant deltas also retain their first event position', () => {
  const state = createEmptyDesktopV3CacheState()
  applyCacheEvent(state, event(10, 'session.assistant.delta', { delta: 'one' }))
  applyCacheEvent(state, event(20, 'session.assistant.delta', { delta: ' two' }))
  const draft = state.liveRunsBySession['session-1']['run-1'].assistantDraft
  assert.equal(draft?.timelineSeq, 10)
  assert.equal(draft?.content, 'one two')
})

// Requirement: independent live and durable lanes may race, batch, or replay,
// but each lane keeps its own causal order. Enumerate every legal interleaving
// of this bounded trace instead of relying on random sleeps. Assert every
// intermediate render, not just the final state, plus a cold hydrated render.
// This exercises the live coordinator and validated durable frames -> reducer
// -> selector -> render builder; it does not claim DOM or browser scroll coverage.
test('all 124 live/durable batching and replay schedules preserve the assistant slot', () => {
  const fullText = 'one two three'
  const message = {
    id: 'message-1', session_id: 'session-1', role: 'assistant' as const,
    content: fullText, created_at: 40, global_seq: 40,
    metadata: { run_id: 'run-1', stream_id: stream, stream_start_seq: 10 },
  }
  const durable = [
    event(10, 'session.assistant.delta', { stream_id: stream, delta: 'one', offset_start: 0, offset_end: 3 }),
    event(20, 'session.provider_tool_call.started', { call_id: 'call-edit', tool_name: 'edit', step: 1 }),
    event(30, 'session.assistant.delta', { stream_id: stream, delta: ' two three', offset_start: 3, offset_end: 13 }),
    event(35, 'session.provider_tool_call.started', { call_id: 'call-read', tool_name: 'read', step: 1 }),
    event(40, 'session.message.appended', { message }),
  ]
  function schedules(live: number, saved: number, prefix: string[] = []): string[][] {
    if (!live && !saved) return [prefix]
    return [
      ...(live ? schedules(live - 1, saved, [...prefix, 'L']) : []),
      ...(saved ? schedules(live, saved - 1, [...prefix, 'D']) : []),
    ]
  }
  let checked = 0
  for (const chunks of [['one', ' two', ' three'], [fullText]]) {
    for (const replay of [false, true]) {
      for (const schedule of schedules(chunks.length, durable.length)) {
        let state = createEmptyDesktopV3CacheState()
        const coordinator = new DesktopV3LivePatchCoordinator({
          getSnapshot: () => state,
          commitSnapshot: (_previous, next) => { state = next },
          requestFrame: () => 1, cancelFrame: () => {},
          setTimer: () => 1, clearTimer: () => {}, isDocumentHidden: () => false,
        })
        let liveIndex = 0
        let durableIndex = 0
        let offset = 0
        const trace = `${chunks.length} chunks; replay=${replay}; ${schedule.join('')}`
        let previousContent = ''
        const visible = () => buildDesktopV3ConversationRenderItems(selectRenderedSessionMessages(state, 'session-1'))
          .filter((item) => item.type !== 'live-working')
        for (const lane of schedule) {
          if (lane === 'L') {
            const text = chunks[liveIndex++]
            const patch = {
              session_id: 'session-1', run_id: 'run-1', stream_id: stream,
              stream_kind: 'assistant_text' as const, operation: 'append' as const, step: 1, step_id: 'step-1',
              live_seq_start: liveIndex, live_seq_end: liveIndex,
              offset_start: offset, offset_end: offset + text.length, text, recorded_at: liveIndex,
            }
            offset += text.length
            coordinator.accept(patch, 1)
            coordinator.flushNow()
            if (replay) {
              coordinator.accept(patch, 1)
              coordinator.flushNow()
            }
          } else {
            const next = durable[durableIndex++]
            const frame: RealtimeMessage = {
              protocol: 'v3.realtime', protocol_version: 1, kind: 'event',
              session_id: 'session-1', event_type: next.eventType,
              endpoint_cursor: `cursor-${durableIndex}`,
              event: { ...next.sessionEvent!, payload: next.payload },
            }
            coordinator.beforeDurableFrame(frame)
            applyRealtimeFrame(state, { frame })
            coordinator.afterDurableFrame(frame)
            if (replay) {
              coordinator.beforeDurableFrame(frame)
              applyRealtimeFrame(state, { frame })
              coordinator.afterDurableFrame(frame)
            }
          }
          const rows = visible()
          const expectedKeys = [`live-assistant:run-1:${stream}`]
          if (durableIndex >= 2) expectedKeys.push('live-tool:call-edit')
          if (durableIndex >= 4) expectedKeys.push('live-tool:call-read')
          assert.deepEqual(rows.map(desktopV3RenderItemKey), expectedKeys, trace)
          if (durableIndex > 0) assert.equal(rows[0].timelineSeq, 10, trace)
          const first = rows[0]
          const content = first.type === 'message' ? first.message.content : first.type === 'live-assistant' ? first.content : ''
          assert.ok(content.length > 0 && fullText.startsWith(content), `corrupted content: ${trace}`)
          assert.ok(content.startsWith(previousContent), `text disappeared: ${trace}`)
          previousContent = content
        }
        assert.equal(selectRenderedSessionMessages(state, 'session-1').committed.find((entry) => entry.id === 'message-1')?.content, fullText, trace)
        const hydrated = buildDesktopV3ConversationRenderItems({
          committed: [
            { id: 'saved-edit', session_id: 'session-1', role: 'tool', content: 'done', global_seq: 25, created_at: 25, metadata: { call_id: 'call-edit' } },
            { id: 'saved-read', session_id: 'session-1', role: 'tool', content: 'done', global_seq: 36, created_at: 36, metadata: { call_id: 'call-read' } },
            message,
          ], liveRuns: [], pendingUser: [], runIntents: [],
        })
        assert.deepEqual(hydrated.map(desktopV3RenderItemKey), visible().map(desktopV3RenderItemKey), trace)
        assert.equal(hydrated[0].timelineSeq, 10, trace)
        coordinator.dispose()
        checked++
      }
    }
  }
  assert.equal(checked, 124)
})
