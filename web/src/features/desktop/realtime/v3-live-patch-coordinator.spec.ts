import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

import { DesktopV3LivePatchCoordinator } from './v3-live-patch-coordinator'
import { createEmptyDesktopV3CacheState } from '../state/desktop-v3-cache-reducer'
import type { DesktopV3CacheAction, DesktopV3CacheState } from '../state/desktop-v3-cache-types'
import type { SessionV3RealtimeLivePatchWire } from '../session-v3/types'

const encoder = new TextEncoder()

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
