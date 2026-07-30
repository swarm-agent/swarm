import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

import { DesktopV3LivePatchCoordinator } from './v3-live-patch-coordinator'
import { applyCacheEvent, createEmptyDesktopV3CacheState } from '../state/desktop-v3-cache-reducer'
import { selectDesktopToolActivities } from '../state/desktop-v3-cache-selectors'
import type { DesktopV3CacheAction, DesktopV3CacheState, MessageSnapshot, RealtimeMessage } from '../state/desktop-v3-cache-types'
import type { SessionV3RealtimeLivePatchWire } from '../session-v3/types'
import { normalizeRealtimeEventFrame } from '../state/desktop-v3-cache-wire'

const encoder = new TextEncoder()

test('Desktop V3 rejects provider construction live patches that bypass durable ordering', () => {
  let state = createEmptyDesktopV3CacheState()
  let commits = 0
  const coordinator = new DesktopV3LivePatchCoordinator({
    getSnapshot: () => state,
    commitSnapshot: (_previous, next) => {
      commits += 1
      state = next
    },
    requestFrame: () => 0,
    cancelFrame: () => undefined,
    setTimer: () => 0,
    clearTimer: () => undefined,
    isDocumentHidden: () => false,
  })
  const text = JSON.stringify({
    path_id: 'run.v3.provider-tool-construction.v1',
    type: 'session.provider_tool_call.started',
    run_id: 'run-a',
    step: 1,
    event_index: 1,
    call_id: 'call-edit',
    tool_name: 'edit',
    status: 'building',
    recorded_at: 1,
  })
  coordinator.accept(livePatch({
    stream_id: 'provider-tool:run-a:step:1:event:1',
    stream_kind: 'provider_tool_call' as never,
    text,
    offset_end: encoder.encode(text).byteLength,
  }), 1)
  assert.equal(commits, 0)
  assert.equal(coordinator.debugSnapshotForTests().scheduled, false)
  assert.equal(selectDesktopToolActivities(state, 'session-a', 'run-a').length, 0)
})

test('Desktop V3 ten thousand patches commit once per animation frame', () => {
  let state = createEmptyDesktopV3CacheState()
  const frameCallbacks: FrameRequestCallback[] = []
  let commits = 0
  const coordinator = new DesktopV3LivePatchCoordinator({
    getSnapshot: () => state,
    commitSnapshot: (_previous, next) => {
      commits += 1
      state = next
    },
    requestFrame: (callback) => {
      frameCallbacks.push(callback)
      return frameCallbacks.length
    },
    cancelFrame: () => undefined,
    setTimer: () => 0,
    clearTimer: () => undefined,
    isDocumentHidden: () => false,
  })

  for (let i = 0; i < 10_000; i += 1) {
    coordinator.accept(livePatch({ live_seq_start: i + 1, live_seq_end: i + 1, offset_start: i, offset_end: i + 1, text: 'x' }), 1)
  }

  const beforeFrame = coordinator.debugSnapshotForTests()
  assert.equal(commits, 0)
  assert.equal(beforeFrame.pendingKeys, 1)
  assert.equal(beforeFrame.pendingBytes, 10_000)
  assert.ok(beforeFrame.pendingAllocatedBytes <= (2 * 10_000) + 256)
  assert.equal(frameCallbacks.length, 1)

  frameCallbacks[0](1)

  assert.equal(commits, 1)
  const draft = state.liveRunsBySession['session-a']['run-a'].assistantDraft
  assert.equal(draft?.content, 'x'.repeat(10_000))
  assert.equal(draft?.liveSeqEnd, 10_000)
  assert.equal(draft?.offsetEnd, 10_000)
})

test('Desktop V3 one frame batches one hundred synthetic streams', () => {
  let state = createEmptyDesktopV3CacheState()
  const frameCallbacks: FrameRequestCallback[] = []
  let commits = 0
  const coordinator = new DesktopV3LivePatchCoordinator({
    getSnapshot: () => state,
    commitSnapshot: (_previous, next) => {
      commits += 1
      state = next
    },
    requestFrame: (callback) => {
      frameCallbacks.push(callback)
      return frameCallbacks.length
    },
    cancelFrame: () => undefined,
    setTimer: () => 0,
    clearTimer: () => undefined,
    isDocumentHidden: () => false,
  })

  for (let delta = 0; delta < 100; delta += 1) {
    for (let stream = 0; stream < 100; stream += 1) {
      coordinator.accept(livePatch({
        run_id: `run-${stream}`,
        stream_id: `assistant:run-${stream}:step:1`,
        live_seq_start: delta + 1,
        live_seq_end: delta + 1,
        offset_start: delta,
        offset_end: delta + 1,
        text: String(stream % 10),
      }), 1)
    }
  }

  frameCallbacks[0](1)

  assert.equal(commits, 1)
  assert.equal(Object.keys(state.liveRunsBySession['session-a']).length, 100)
  for (let stream = 0; stream < 100; stream += 1) {
    const run = state.liveRunsBySession['session-a'][`run-${stream}`]
    assert.equal(run.assistantDraft?.content, String(stream % 10).repeat(100))
    assert.equal((run.assistantSegments ?? []).filter((segment) => segment.streamId === `assistant:run-${stream}:step:1`).length, 0)
  }
})

test('Desktop V3 live gap pauses only one stream', () => {
  let state = createEmptyDesktopV3CacheState()
  const frameCallbacks: FrameRequestCallback[] = []
  const coordinator = new DesktopV3LivePatchCoordinator({
    getSnapshot: () => state,
    commitSnapshot: (_previous, next) => {
      state = next
    },
    requestFrame: (callback) => {
      frameCallbacks.push(callback)
      return frameCallbacks.length
    },
    cancelFrame: () => undefined,
    setTimer: () => 0,
    clearTimer: () => undefined,
    isDocumentHidden: () => false,
  })

  coordinator.accept(livePatch({ run_id: 'run-a', stream_id: 'stream-a', live_seq_start: 1, live_seq_end: 1, offset_start: 0, offset_end: 2, text: 'aa' }), 1)
  coordinator.accept(livePatch({ run_id: 'run-b', stream_id: 'stream-b', live_seq_start: 1, live_seq_end: 1, offset_start: 0, offset_end: 2, text: 'bb' }), 1)
  frameCallbacks[0](1)

  coordinator.accept(livePatch({ run_id: 'run-a', stream_id: 'stream-a', live_seq_start: 2, live_seq_end: 2, offset_start: 7, offset_end: 9, text: 'xx' }), 1)
  coordinator.accept(livePatch({ run_id: 'run-b', stream_id: 'stream-b', live_seq_start: 2, live_seq_end: 2, offset_start: 2, offset_end: 4, text: 'cc' }), 1)
  frameCallbacks[1](2)

  assert.equal(state.liveRunsBySession['session-a']['run-a'].assistantDraft?.content, 'aa')
  assert.equal(state.liveRunsBySession['session-a']['run-b'].assistantDraft?.content, 'bbcc')
  assert.equal(coordinator.debugSnapshotForTests().pausedKeys, 1)
})


test('Desktop V3 terminal commit prevents scheduled live resurrection', () => {
  let state = createEmptyDesktopV3CacheState()
  const frameCallbacks: FrameRequestCallback[] = []
  let cancelCalls = 0
  const coordinator = new DesktopV3LivePatchCoordinator({
    getSnapshot: () => state,
    commitSnapshot: (_previous, next) => {
      state = next
    },
    requestFrame: (callback) => {
      frameCallbacks.push(callback)
      return frameCallbacks.length
    },
    cancelFrame: () => { cancelCalls += 1 },
    setTimer: () => 0,
    clearTimer: () => undefined,
    isDocumentHidden: () => false,
  })

  coordinator.accept(livePatch({ text: 'pending', offset_start: 0, offset_end: 7 }), 1)
  assert.equal(frameCallbacks.length, 1)
  const committed = committedAssistantFrame({ content: 'pending', streamId: 'assistant:run-a:step:1' })
  coordinator.beforeDurableFrame(committed)
  applyCacheEvent(state, normalizeRealtimeEventFrame(committed))
  frameCallbacks[0](1)

  assert.equal(state.liveRunsBySession['session-a']?.['run-a']?.assistantDraft, undefined)
  assert.equal(coordinator.debugSnapshotForTests().pendingKeys, 0)
  assert.equal(cancelCalls, 1)
})

test('Desktop V3 reconnect gap falls back to durable progress', () => {
  let state = createEmptyDesktopV3CacheState()
  state.liveRunsBySession['session-a'] = {
    'run-a': {
      sessionId: 'session-a',
      runId: 'run-a',
      status: 'running',
      toolCallsByCallId: {},
      assistantDraft: {
        content: 'x'.repeat(60),
        updatedAt: 1,
        streamId: 'assistant:run-a:step:1',
        liveSeqEnd: 0,
        offsetEnd: 60,
        durableOffsetEnd: 60,
      },
    },
  }
  const frameCallbacks: FrameRequestCallback[] = []
  const coordinator = new DesktopV3LivePatchCoordinator({
    getSnapshot: () => state,
    commitSnapshot: (_previous, next) => {
      state = next
    },
    requestFrame: (callback) => {
      frameCallbacks.push(callback)
      return frameCallbacks.length
    },
    cancelFrame: () => undefined,
    setTimer: () => 0,
    clearTimer: () => undefined,
    isDocumentHidden: () => false,
  })

  coordinator.resetGeneration(2)
  coordinator.accept(livePatch({ live_seq_start: 1, live_seq_end: 1, offset_start: 100, offset_end: 101, text: 'y' }), 2)
  assert.equal(frameCallbacks.length, 0)
  assert.equal(coordinator.debugSnapshotForTests().pausedKeys, 1)
  assert.equal(state.liveRunsBySession['session-a']['run-a'].assistantDraft?.content, 'x'.repeat(60))

  const durable = durableDeltaFrame({
    delta: 'z'.repeat(40),
    offsetStart: 60,
    offsetEnd: 100,
    streamId: 'assistant:run-a:step:1',
  })
  coordinator.beforeDurableFrame(durable)
  applyCacheEvent(state, normalizeRealtimeEventFrame(durable))
  assert.equal(state.liveRunsBySession['session-a']['run-a'].assistantDraft?.content, `${'x'.repeat(60)}${'z'.repeat(40)}`)

  coordinator.accept(livePatch({ live_seq_start: 2, live_seq_end: 2, offset_start: 100, offset_end: 101, text: 'y' }), 2)
  assert.equal(frameCallbacks.length, 0)

  coordinator.accept(livePatch({
    run_id: 'run-a',
    stream_id: 'assistant:run-a:step:2',
    step: 2,
    step_id: 'step-2',
    live_seq_start: 1,
    live_seq_end: 1,
    offset_start: 0,
    offset_end: 3,
    text: 'new',
  }), 2)
  frameCallbacks[0](1)
  assert.equal(state.liveRunsBySession['session-a']['run-a'].assistantDraft?.streamId, 'assistant:run-a:step:2')
  assert.equal(state.liveRunsBySession['session-a']['run-a'].assistantDraft?.content, 'new')
})

test('Desktop V3 sustained stream commits once per paint window', () => {
  let state = createEmptyDesktopV3CacheState()
  const frameCallbacks: FrameRequestCallback[] = []
  let commits = 0
  const coordinator = new DesktopV3LivePatchCoordinator({
    getSnapshot: () => state,
    commitSnapshot: (_previous, next) => {
      commits += 1
      state = next
    },
    requestFrame: (callback) => {
      frameCallbacks.push(callback)
      return frameCallbacks.length
    },
    cancelFrame: () => undefined,
    setTimer: () => 0,
    clearTimer: () => undefined,
    isDocumentHidden: () => false,
  })

  for (let windowIndex = 0; windowIndex < 120; windowIndex += 1) {
    const base = windowIndex * 100
    for (let delta = 0; delta < 100; delta += 1) {
      coordinator.accept(livePatch({
        live_seq_start: base + delta + 1,
        live_seq_end: base + delta + 1,
        offset_start: base + delta,
        offset_end: base + delta + 1,
        text: 'x',
      }), 1)
    }
    assert.equal(commits, windowIndex)
    frameCallbacks[windowIndex](windowIndex)
    assert.equal(commits, windowIndex + 1)
    assert.equal(state.liveRunsBySession['session-a']['run-a'].assistantDraft?.content, 'x'.repeat(base + 100))
  }
})

test('Desktop V3 live accumulators do not rebuild text per patch', async () => {
  const coordinator = await readFile(new URL('./v3-live-patch-coordinator.ts', import.meta.url), 'utf8')
  assert.doesNotMatch(coordinator, /pending\.text\s*\+=/)
  assert.doesNotMatch(coordinator, /pending\.text\s*=\s*pending\.text\s*\+/)
  assert.doesNotMatch(coordinator, /chunks\.push/)
  assert.match(coordinator, /Utf8AppendBuffer/)
  assert.match(coordinator, /\.append\(patch\.text\)/)

  const backend = await readFile(new URL('../../../../../swarmd/internal/api/sessions_v3_live_hub.go', import.meta.url), 'utf8')
  assert.match(backend, /bytes\.Buffer/)
  assert.match(backend, /WriteString/)
  assert.doesNotMatch(backend, /pending\.Text\s*=\s*pending\.Text\s*\+/)
  assert.doesNotMatch(backend, /patch\.Text\[[^\]]+\]/)
})


function durableDeltaFrame(input: { delta: string; offsetStart: number; offsetEnd: number; streamId: string }): RealtimeMessage {
  return {
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    session_id: 'session-a',
    event_type: 'session.assistant.delta',
    endpoint_cursor: 'cursor-durable',
    event: {
      id: 'evt-durable-delta',
      session_id: 'session-a',
      seq: 10,
      event_type: 'session.assistant.delta',
      payload: { run_id: 'run-a', stream_id: input.streamId, delta: input.delta, offset_start: input.offsetStart, offset_end: input.offsetEnd },
      ts_unix_ms: 10,
    },
  }
}

function committedAssistantFrame(input: { content: string; streamId: string }): RealtimeMessage {
  const message: MessageSnapshot = {
    id: 'msg-committed',
    session_id: 'session-a',
    global_seq: 10,
    role: 'assistant',
    content: input.content,
    metadata: { run_id: 'run-a', stream_id: input.streamId },
    created_at: 10,
  }
  return {
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    session_id: 'session-a',
    event_type: 'session.assistant.completed',
    endpoint_cursor: 'cursor-committed',
    event: {
      id: 'evt-committed',
      session_id: 'session-a',
      seq: 11,
      event_type: 'session.assistant.completed',
      payload: { run_id: 'run-a', message },
      ts_unix_ms: 11,
    },
  }
}

function livePatch(overrides: Partial<SessionV3RealtimeLivePatchWire> = {}): SessionV3RealtimeLivePatchWire {
  const text = overrides.text ?? 'x'
  const offsetStart = overrides.offset_start ?? 0
  const offsetEnd = overrides.offset_end ?? offsetStart + encoder.encode(text).byteLength
  return {
    session_id: 'session-a',
    run_id: 'run-a',
    stream_id: 'assistant:run-a:step:1',
    stream_kind: 'assistant_text',
    operation: 'append',
    step: 1,
    step_id: 'step-1',
    live_seq_start: 1,
    live_seq_end: 1,
    offset_start: offsetStart,
    offset_end: offsetEnd,
    text,
    recorded_at: 1,
    ...overrides,
  }
}
