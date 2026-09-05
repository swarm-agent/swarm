import test from 'node:test'
import assert from 'node:assert/strict'

import { buildDesktopV3ConversationRenderItems, desktopV3RenderItemKey } from './desktop-v3-existing-conversation-pane'
import type { LiveRunOverlay, MessageSnapshot } from '../../state/desktop-v3-cache-types'

// Requirement: assistant text is one ordered activity through draft, retention,
// commit and hydration. Prevent re-keying, late-commit reordering and content-only
// suppression of distinct streams. These pure renderer tests exercise the owning
// buildDesktopV3ConversationRenderItems boundary without transport/browser timing.
const streamId = 'assistant:run-1:step:1'
const key = `live-assistant:run-1:${streamId}`
const draft = { content: 'Before the tools.', streamId, timelineSeq: 10, updatedAt: 10 }
const tool = { callId: 'call-edit', toolName: 'edit', timelineSeq: 20, updatedAt: 20 }
const committed: MessageSnapshot = {
  id: 'message-1', session_id: 'session-1', role: 'assistant', content: draft.content,
  global_seq: 30, created_at: 30,
  metadata: { run_id: 'run-1', stream_id: streamId, stream_start_seq: 10 },
}
function run(extra: Partial<LiveRunOverlay> = {}): LiveRunOverlay {
  return { sessionId: 'session-1', runId: 'run-1', status: 'running', toolCallsByCallId: { 'call-edit': tool }, ...extra }
}
function items(messages: MessageSnapshot[], runs: LiveRunOverlay[]) {
  return buildDesktopV3ConversationRenderItems({ committed: messages, liveRuns: runs, pendingUser: [], runIntents: [] })
    .filter((item) => item.type !== 'live-working')
}

test('assistant activity retains its key and position across draft, tools, commit overlap and hydration', () => {
  const segment = { ...draft, id: key, createdAt: 10 }
  const hydratedTool: MessageSnapshot = {
    id: 'tool-message', session_id: 'session-1', role: 'tool', content: 'done',
    global_seq: 25, created_at: 25, metadata: { call_id: tool.callId },
  }
  const phases = [
    items([], [run({ assistantDraft: draft })]),
    items([], [run({ assistantSegments: [segment] })]),
    items([committed], [run({ assistantDraft: draft, assistantSegments: [segment] })]),
    items([hydratedTool, committed], []),
  ]
  for (const phase of phases) {
    assert.deepEqual(phase.map(desktopV3RenderItemKey), [key, 'live-tool:call-edit'])
    assert.equal(phase[0].timelineSeq, 10)
  }
})

test('identical assistant content in another stream or run is not a replay', () => {
  for (const other of [
    run({ assistantDraft: { ...draft, streamId: 'assistant:run-1:step:2', timelineSeq: 40 } }),
    run({ runId: 'run-2', assistantDraft: { ...draft, timelineSeq: 40 } }),
  ]) {
    const phase = items([committed], [other])
    assert.equal(phase.filter((item) => item.type === 'live-assistant').length, 1)
    assert.equal(phase.length, 3)
  }
})

test('absent or invalid stream-start anchors preserve the canonical message sequence', () => {
  for (const start of [undefined, 0, -1, 31, '10', Number.NaN]) {
    const message = { ...committed, metadata: { ...committed.metadata, stream_start_seq: start } }
    assert.equal(items([message], [run()])[1].timelineSeq, 30)
  }
  const user = { ...committed, role: 'user' as const }
  assert.equal(items([user], [run()])[1].timelineSeq, 30)
})
