import assert from 'node:assert/strict'
import test from 'node:test'

import type { ChatMessageRecord } from '../chat/types/chat'
import { createEmptyDesktopState, type DesktopDaemonSnapshot } from '../state/desktop-state'
import type { DesktopSessionRecord } from '../types/realtime'
import {
  applyV3Envelope,
  createV3ConnectionStaleEnvelope,
  createV3ControlEnvelope,
  createV3EventEnvelope,
  createV3OptimisticSendEnvelope,
  createV3PersistedRestoreEnvelope,
  createV3SnapshotEnvelope,
  normalizeV3RealtimeFrame,
  validateV3Envelope,
} from './index'

function session(id: string, updatedAt: number): DesktopSessionRecord {
  return {
    id,
    title: `Session ${id}`,
    workspacePath: '/workspace',
    workspaceName: 'workspace',
    mode: 'auto',
    messageCount: 0,
    updatedAt,
    createdAt: updatedAt,
    permissionsHydrated: true,
    lifecycle: null,
    live: {
      runId: null,
      agentName: null,
      startedAt: null,
      status: 'idle',
      step: 0,
      toolName: null,
      sidebarToolName: null,
      toolCallId: null,
      toolArguments: null,
      toolOutput: '',
      retainedToolName: null,
      retainedToolCallId: null,
      retainedToolArguments: null,
      retainedToolOutput: '',
      retainedToolState: null,
      summary: null,
      lastEventType: null,
      lastEventAt: null,
      error: null,
      seq: 0,
      assistantDraft: '',
      retainedAssistantSegments: [],
      reasoningSummary: '',
      reasoningText: '',
      reasoningState: 'idle',
      reasoningSegment: 0,
      reasoningStartedAt: null,
      awaitingAck: false,
    },
    pendingPermissions: [],
    pendingPermissionCount: 0,
    usage: null,
  }
}

function message(sessionId: string, id: string, globalSeq: number): ChatMessageRecord {
  return {
    id,
    sessionId,
    globalSeq,
    role: 'user',
    content: 'hello',
    createdAt: globalSeq,
  }
}

test('snapshot and persisted restore envelopes carry source and revision cursor metadata', () => {
  const snapshot: DesktopDaemonSnapshot = {
    rev: 5,
    sessionsById: { s1: session('s1', 10) },
    sessionOrder: ['s1'],
  }

  const httpEnvelope = createV3SnapshotEnvelope(snapshot, { receivedAt: 100 })
  const persistedEnvelope = createV3PersistedRestoreEnvelope(snapshot, { mode: 'merge', receivedAt: 101 })

  assert.equal(httpEnvelope.kind, 'snapshot')
  assert.equal(httpEnvelope.meta.source.kind, 'snapshot')
  assert.equal(httpEnvelope.meta.source.transport, 'http')
  assert.equal(httpEnvelope.meta.cursor.rev, 5)
  assert.deepEqual(validateV3Envelope(httpEnvelope), { ok: true })
  assert.equal(persistedEnvelope.kind, 'persisted.restore')
  assert.equal(persistedEnvelope.meta.source.kind, 'persisted')
  assert.equal(persistedEnvelope.meta.source.transport, 'indexeddb')
  assert.equal(persistedEnvelope.mode, 'merge')
})

test('websocket event normalizer defines identity, domain, cursor, source, and session metadata', () => {
  const envelope = normalizeV3RealtimeFrame({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    endpoint_cursor: 'cursor-11',
    subscription_id: 'desktop:s1',
    rev: 11,
    prevRev: 10,
    event_type: 'session.title.updated',
    event: {
      id: 'event-11',
      session_id: 's1',
      event_type: 'session.title.updated',
      payload: { session_id: 's1', title: 'Renamed' },
      seq: 7,
      ts_unix_ms: 1234,
    },
  }, { receivedAt: 200 })

  assert.equal(envelope.kind, 'event')
  assert.equal(envelope.meta.id, 'event-11')
  assert.equal(envelope.meta.source.kind, 'websocket')
  assert.equal(envelope.meta.source.transport, 'browser-websocket')
  assert.equal(envelope.meta.source.subscriptionId, 'desktop:s1')
  assert.equal(envelope.meta.sessionId, 's1')
  assert.equal(envelope.meta.eventType, 'session.title.updated')
  assert.equal(envelope.meta.domain, 'session')
  assert.equal(envelope.meta.cursor.endpointCursor, 'cursor-11')
  assert.equal(envelope.meta.cursor.rev, 11)
  assert.equal(envelope.meta.cursor.prevRev, 10)
  assert.equal(envelope.meta.cursor.globalSeq, 7)
  assert.equal(envelope.meta.cursor.sourceSeq, 7)
  assert.equal(envelope.meta.cursor.tsUnixMs, 1234)
})

test('applyV3Envelope applies websocket event once and advances cursor only after reducer success', () => {
  const base = applyV3Envelope(createEmptyDesktopState(), createV3SnapshotEnvelope({
    rev: 10,
    sessionsById: { s1: session('s1', 10) },
    sessionOrder: ['s1'],
  })).state
  const envelope = normalizeV3RealtimeFrame({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    endpoint_cursor: 'cursor-11',
    rev: 11,
    prevRev: 10,
    event_type: 'session.title.updated',
    event: {
      session_id: 's1',
      event_type: 'session.title.updated',
      payload: { session_id: 's1', title: 'Renamed' },
      seq: 11,
    },
  })

  const first = applyV3Envelope(base, envelope)
  assert.equal(first.applied, true)
  assert.equal(first.rejected, false)
  assert.equal(first.shouldAdvanceCursor, true)
  assert.equal(first.state.sessionsById.s1?.title, 'Renamed')
  assert.equal(first.state.rev, 11)

  const duplicate = applyV3Envelope(first.state, envelope)
  assert.equal(duplicate.applied, false)
  assert.equal(duplicate.rejected, false)
  assert.equal(duplicate.shouldAdvanceCursor, false)
  assert.equal(duplicate.state, first.state)
})

test('applyV3Envelope rejects cursor advancement when a realtime gap marks state stale', () => {
  const base = applyV3Envelope(createEmptyDesktopState(), createV3SnapshotEnvelope({ rev: 10 })).state
  const envelope = normalizeV3RealtimeFrame({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    endpoint_cursor: 'cursor-12',
    rev: 12,
    prevRev: 8,
    event_type: 'session.title.updated',
    event: {
      session_id: 's1',
      event_type: 'session.title.updated',
      payload: { session_id: 's1', title: 'Bad gap' },
      seq: 12,
    },
  })

  const result = applyV3Envelope(base, envelope)
  assert.equal(result.applied, false)
  assert.equal(result.rejected, true)
  assert.equal(result.stale, true)
  assert.equal(result.shouldAdvanceCursor, false)
  assert.match(result.reason ?? '', /rev mismatch/)
  assert.equal(result.state.sessionsById.s1, undefined)
})

test('invalid event envelope hard-fails through the reducer stale path', () => {
  const envelope = createV3EventEnvelope({ type: '', payload: { session_id: 's1' } }, { id: 'bad-event', receivedAt: 1 })
  const result = applyV3Envelope(createEmptyDesktopState(), envelope)

  assert.equal(validateV3Envelope(envelope).ok, false)
  assert.equal(result.applied, false)
  assert.equal(result.rejected, true)
  assert.equal(result.stale, true)
  assert.match(result.reason ?? '', /missing event type/)
})

test('optimistic send envelope updates messages without claiming durable cursor advancement', () => {
  const base = applyV3Envelope(createEmptyDesktopState(), createV3SnapshotEnvelope({
    rev: 2,
    sessionsById: { s1: session('s1', 2) },
    sessionOrder: ['s1'],
  })).state
  const envelope = createV3OptimisticSendEnvelope({
    message: message('s1', 'client-msg-1', 0),
    receivedAt: 3,
  })

  const result = applyV3Envelope(base, envelope)
  assert.equal(result.applied, true)
  assert.equal(result.shouldAdvanceCursor, false)
  assert.equal(result.state.messagesBySessionId.s1?.[0]?.id, 'client-msg-1')
})

test('connection control envelopes route resync-required frames into stale state', () => {
  const base = applyV3Envelope(createEmptyDesktopState(), createV3SnapshotEnvelope({ rev: 1 })).state
  const control = normalizeV3RealtimeFrame({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'cursor.error',
    endpoint_cursor: 'cursor-99',
    error_code: 'cursor_gone',
  })

  const result = applyV3Envelope(base, control)
  assert.equal(result.applied, false)
  assert.equal(result.rejected, true)
  assert.equal(result.stale, true)
  assert.equal(result.shouldAdvanceCursor, false)
  assert.match(result.state.staleReason ?? '', /cursor_gone/)
})

test('replay control envelopes advance cursors without mutating desktop state', () => {
  const base = applyV3Envelope(createEmptyDesktopState(), createV3SnapshotEnvelope({ rev: 1 })).state
  const replayComplete = createV3ControlEnvelope('replay.complete', {
    receivedAt: 20,
    sessionId: 'session-1',
    endpointCursor: 'cursor-20',
    highWatermarkSeq: 20,
  })

  const result = applyV3Envelope(base, replayComplete)

  assert.equal(result.applied, false)
  assert.equal(result.rejected, false)
  assert.equal(result.shouldAdvanceCursor, true)
  assert.equal(result.state, base)
})

test('connection stale envelope validates explicit reason', () => {
  const envelope = createV3ConnectionStaleEnvelope('manual resync required', { receivedAt: 1 })
  const result = applyV3Envelope(createEmptyDesktopState(), envelope)

  assert.deepEqual(validateV3Envelope(envelope), { ok: true })
  assert.equal(result.state.status, 'stale')
  assert.equal(result.state.resyncRequested, true)
  assert.equal(result.shouldAdvanceCursor, false)
})
