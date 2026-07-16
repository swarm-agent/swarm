import test from 'node:test'
import assert from 'node:assert/strict'

import { buildDesktopV3ConversationRenderItems } from '../chat/components/desktop-v3-existing-conversation-pane'
import type { SessionV3RealtimeLivePatchWire } from '../session-v3/types'
import {
  applyCacheEvent,
  applyDesktopV3LivePatchBatch,
  applyMessageMutationResult,
  buildMessageListCache,
  createEmptyDesktopV3CacheState,
  desktopV3CacheReducer,
  upsertCommittedMessage,
  upsertPendingUserMessage,
  upsertRunIntent,
} from './desktop-v3-cache-reducer'
import { selectRenderedSessionMessages } from './desktop-v3-cache-selectors'
import type {
  CacheEvent,
  DesktopV3CacheState,
  MessageSnapshot,
  V3SessionProjection,
  V3SessionRunIntent,
} from './desktop-v3-cache-types'

const sessionId = 'session-ordering-incident'
const oldRunId = 'run-paused-before-resend'
const newRunId = 'run-created-by-resend'
const clientRequestId = 'client-resend'
const pendingMessageId = 'message-resend'

const committedHistory: MessageSnapshot[] = [
  {
    id: 'message-initial-user',
    session_id: sessionId,
    global_seq: 1,
    role: 'user',
    content: 'initial question',
    created_at: 1,
  },
  {
    id: 'message-old-assistant',
    session_id: sessionId,
    global_seq: 2,
    role: 'assistant',
    content: 'answer before pause',
    metadata: { run_id: oldRunId },
    created_at: 2,
  },
]

function projection(seq: number): V3SessionProjection {
  return {
    session_id: sessionId,
    last_event_seq: seq,
    projection_high_watermark_seq: seq,
    updated_at: seq,
  }
}

function runIntent(runId: string, status: V3SessionRunIntent['status'], eventSeq: number): V3SessionRunIntent {
  return {
    session_id: sessionId,
    run_id: runId,
    status,
    created_at: eventSeq,
    updated_at: eventSeq,
    event_seq: eventSeq,
  }
}

function initialState(): DesktopV3CacheState {
  const state = createEmptyDesktopV3CacheState()
  state.messagesBySession[sessionId] = buildMessageListCache(committedHistory, {
    knownFull: true,
    sourceMessageCount: committedHistory.length,
    sourceLastMessageAt: 2,
    sourceProjectionHighWatermarkSeq: 2,
    source: 'network',
  })
  state.projectionsBySession[sessionId] = projection(2)
  upsertRunIntent(state, sessionId, runIntent(oldRunId, 'running', 2))
  return state
}

function livePatch(runId: string, text: string, recordedAt: number): SessionV3RealtimeLivePatchWire {
  return {
    session_id: sessionId,
    run_id: runId,
    stream_id: `assistant:${runId}:step:1`,
    stream_kind: 'assistant_text',
    operation: 'append',
    step: 1,
    step_id: 'step-1',
    live_seq_start: 1,
    live_seq_end: 1,
    offset_start: 0,
    offset_end: new TextEncoder().encode(text).byteLength,
    text,
    recorded_at: recordedAt,
  }
}

function signature(state: DesktopV3CacheState): string[] {
  return buildDesktopV3ConversationRenderItems(selectRenderedSessionMessages(state, sessionId)).map((item) => {
    if (item.type === 'message') {
      return `message:${item.message.role}:${item.message.id}:global=${item.message.global_seq}`
    }
    if (item.type === 'pending-user') {
      return `pending-user:${String(item.message.runId)}:${item.message.messageId}:timeline=${item.timelineSeq}`
    }
    if (item.type === 'live-assistant') {
      const runId = item.id.slice('live-assistant:'.length, item.id.lastIndexOf(':draft'))
      return `live-assistant:${runId}:timeline=${item.timelineSeq}`
    }
    return `${item.type}:timeline=${item.timelineSeq ?? 0}`
  })
}

function indexOfRun(state: DesktopV3CacheState, runId: string): number {
  return signature(state).findIndex((entry) => entry.startsWith(`live-assistant:${runId}:`))
}

function indexOfPending(state: DesktopV3CacheState): number {
  return signature(state).findIndex((entry) => entry.startsWith(`pending-user:${newRunId}:`))
}

test('Desktop V3 causally orders a paused-stream resend after its pending user and contrasts durable controls', () => {
  let pausedState = initialState()
  pausedState = applyDesktopV3LivePatchBatch(pausedState, [
    livePatch(oldRunId, 'old stream retained while reconnecting', 10),
  ])
  desktopV3CacheReducer(pausedState, {
    type: 'realtime.statusChanged',
    status: 'reconnecting',
    errorCode: 'stream_paused',
    error: 'diagnostic reconnect pause',
  })

  assert.equal(pausedState.realtime.status, 'reconnecting')
  assert.equal(pausedState.projectionsBySession[sessionId]?.last_event_seq, 2)

  upsertPendingUserMessage(pausedState, {
    sessionId,
    clientRequestId,
    messageId: pendingMessageId,
    content: 'continue after pause',
    metadata: { source: 'ordering-regression' },
    runId: newRunId,
    createdAt: 20,
  })
  pausedState = applyDesktopV3LivePatchBatch(pausedState, [
    livePatch(newRunId, 'new stream arrived before HTTP response', 30),
  ])

  const pausedTrace = {
    oldRunId,
    newRunId,
    projectionLastEventSeq: pausedState.projectionsBySession[sessionId]?.last_event_seq,
    oldRunTimelineSeq: pausedState.liveRunsBySession[sessionId]?.[oldRunId]?.assistantDraft?.timelineSeq,
    pendingUserTimelineSeq: pausedState.pendingUserByClientRequestId[clientRequestId]?.timelineSeq,
    newRunTimelineSeq: pausedState.liveRunsBySession[sessionId]?.[newRunId]?.assistantDraft?.timelineSeq,
    signature: signature(pausedState),
    newLiveIndex: indexOfRun(pausedState, newRunId),
    pendingUserIndex: indexOfPending(pausedState),
  }
  assert.deepEqual(pausedTrace, {
    oldRunId: 'run-paused-before-resend',
    newRunId: 'run-created-by-resend',
    projectionLastEventSeq: 2,
    oldRunTimelineSeq: 3,
    pendingUserTimelineSeq: 4,
    newRunTimelineSeq: 5,
    signature: [
      'message:user:message-initial-user:global=1',
      'message:assistant:message-old-assistant:global=2',
      'live-assistant:run-paused-before-resend:timeline=3',
      'pending-user:run-created-by-resend:message-resend:timeline=4',
      'live-assistant:run-created-by-resend:timeline=5',
    ],
    newLiveIndex: 4,
    pendingUserIndex: 3,
  })
  assert.ok(pausedTrace.pendingUserIndex < pausedTrace.newLiveIndex)

  const realtimeCommittedMessage: MessageSnapshot = {
    id: pendingMessageId,
    session_id: sessionId,
    global_seq: 3,
    role: 'user',
    content: 'continue after pause',
    metadata: { run_id: newRunId },
    created_at: 20,
  }
  applyCacheEvent(pausedState, {
    source: 'realtime',
    sessionId,
    eventType: 'session.message.appended',
    sessionEvent: {
      id: 'event-restart-user',
      session_id: sessionId,
      seq: 3,
      event_type: 'session.message.appended',
      payload: { message: realtimeCommittedMessage },
      ts_unix_ms: 20,
    },
    projection: projection(3),
    payload: { message: realtimeCommittedMessage },
  })

  applyMessageMutationResult(pausedState, {
    ok: true,
    session_id: sessionId,
    projection: projection(4),
    message: {
      id: pendingMessageId,
      session_id: sessionId,
      global_seq: 0,
      role: 'user',
      content: 'malformed response must not replace realtime snapshot',
      metadata: { run_id: newRunId },
      created_at: 40,
    },
    run_intent: runIntent(newRunId, 'pending_executor', 4),
    mutation: { realtime_outbox: null },
    realtime_outbox: null,
  }, clientRequestId, pendingMessageId)
  upsertCommittedMessage(pausedState, sessionId, {
    ...realtimeCommittedMessage,
    global_seq: 2,
    content: 'older response must not replace realtime snapshot',
    created_at: 50,
  })

  const committedTrace = {
    projectionLastEventSeq: pausedState.projectionsBySession[sessionId]?.last_event_seq,
    signature: signature(pausedState),
    committedUserIndex: signature(pausedState).findIndex((entry) => entry.startsWith('message:user:message-resend:')),
    newLiveIndex: indexOfRun(pausedState, newRunId),
  }
  assert.deepEqual(committedTrace, {
    projectionLastEventSeq: 4,
    signature: [
      'message:user:message-initial-user:global=1',
      'message:assistant:message-old-assistant:global=2',
      'message:user:message-resend:global=3',
      'live-assistant:run-paused-before-resend:timeline=3',
      'live-assistant:run-created-by-resend:timeline=5',
    ],
    committedUserIndex: 2,
    newLiveIndex: 4,
  })
  assert.ok(committedTrace.committedUserIndex < committedTrace.newLiveIndex)
  assert.deepEqual(pausedState.messagesBySession[sessionId]?.items.find((message) => message.id === pendingMessageId), realtimeCommittedMessage)

  let durableCancellationState = initialState()
  durableCancellationState = applyDesktopV3LivePatchBatch(durableCancellationState, [
    livePatch(oldRunId, 'old stream before durable cancellation', 10),
  ])
  const cancellationIntent = runIntent(oldRunId, 'cancelled', 3)
  const cancellationEvent: CacheEvent = {
    source: 'realtime',
    sessionId,
    eventType: 'session.run.cancelled',
    sessionEvent: {
      id: 'event-old-run-cancelled',
      session_id: sessionId,
      seq: 3,
      event_type: 'session.run.cancelled',
      payload: { run_id: oldRunId, run_intent: cancellationIntent },
      ts_unix_ms: 15,
    },
    projection: projection(3),
    payload: { run_id: oldRunId, run_intent: cancellationIntent },
  }
  applyCacheEvent(durableCancellationState, cancellationEvent)
  upsertPendingUserMessage(durableCancellationState, {
    sessionId,
    clientRequestId,
    messageId: pendingMessageId,
    content: 'continue after durable stop',
    metadata: { source: 'ordering-regression' },
    runId: newRunId,
    createdAt: 20,
  })
  durableCancellationState = applyDesktopV3LivePatchBatch(durableCancellationState, [
    livePatch(newRunId, 'new stream after durable stop', 30),
  ])

  const cancellationTrace = {
    projectionLastEventSeq: durableCancellationState.projectionsBySession[sessionId]?.last_event_seq,
    oldRunStatus: durableCancellationState.runIntentsBySession[sessionId]?.[oldRunId]?.status,
    pendingUserTimelineSeq: durableCancellationState.pendingUserByClientRequestId[clientRequestId]?.timelineSeq,
    newRunTimelineSeq: durableCancellationState.liveRunsBySession[sessionId]?.[newRunId]?.assistantDraft?.timelineSeq,
    signature: signature(durableCancellationState),
    pendingUserIndex: indexOfPending(durableCancellationState),
    newLiveIndex: indexOfRun(durableCancellationState, newRunId),
  }
  assert.deepEqual(cancellationTrace, {
    projectionLastEventSeq: 3,
    oldRunStatus: 'cancelled',
    pendingUserTimelineSeq: 4,
    newRunTimelineSeq: 5,
    signature: [
      'message:user:message-initial-user:global=1',
      'message:assistant:message-old-assistant:global=2',
      'live-assistant:run-paused-before-resend:timeline=3',
      'pending-user:run-created-by-resend:message-resend:timeline=4',
      'live-assistant:run-created-by-resend:timeline=5',
    ],
    pendingUserIndex: 3,
    newLiveIndex: 4,
  })
  assert.ok(cancellationTrace.pendingUserIndex < cancellationTrace.newLiveIndex)
})
