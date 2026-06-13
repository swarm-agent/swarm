import assert from 'node:assert/strict'
import test from 'node:test'

import type { ChatMessageRecord } from '../chat/types/chat'
import type { DesktopSessionRecord } from '../types/realtime'
import {
  createV3RuntimeController,
  createV3SnapshotEnvelope,
  normalizeV3RealtimeFrame,
  selectV3Messages,
  selectV3Session,
  selectV3WorkspaceSessions,
} from './index'

function session(id: string, updatedAt: number, workspacePath = '/workspace'): DesktopSessionRecord {
  return {
    id,
    title: `Session ${id}`,
    workspacePath,
    workspaceName: workspacePath.split('/').filter(Boolean).pop() || 'workspace',
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
    role: 'assistant',
    content: `message ${id}`,
    createdAt: globalSeq,
  }
}

test('V3 runtime store applies envelopes through the canonical reducer and notifies subscribers', () => {
  const runtime = createV3RuntimeController()
  const notifications: number[] = []
  const unsubscribe = runtime.subscribe(() => {
    notifications.push(runtime.getSnapshot().mutationSeq)
  })

  const result = runtime.applyEnvelope(createV3SnapshotEnvelope({
    rev: 10,
    sessionsById: { s1: session('s1', 10) },
    sessionOrder: ['s1'],
  }, { receivedAt: 100 }))

  assert.equal(result.applied, true)
  assert.equal(result.snapshot.desktop.rev, 10)
  assert.equal(result.snapshot.desktop.sessionsById.s1?.id, 's1')
  assert.equal(result.snapshot.mutationSeq, 1)
  assert.equal(result.snapshot.lastApply?.envelopeKind, 'snapshot')
  assert.equal(result.snapshot.lastApply?.applied, true)
  assert.deepEqual(notifications, [1])

  unsubscribe()
  runtime.applyEnvelope(createV3SnapshotEnvelope({ rev: 11 }, { receivedAt: 101 }))
  assert.deepEqual(notifications, [1])
})

test('V3 runtime store deduplicates already-applied envelope ids before reducer mutation', () => {
  const runtime = createV3RuntimeController()
  runtime.applyEnvelope(createV3SnapshotEnvelope({
    rev: 1,
    sessionsById: { s1: session('s1', 1) },
    sessionOrder: ['s1'],
  }, { id: 'snapshot-1', receivedAt: 1 }))
  const before = runtime.getSnapshot()

  const duplicate = runtime.applyEnvelope(createV3SnapshotEnvelope({
    rev: 2,
    sessionsById: { s2: session('s2', 2) },
    sessionOrder: ['s2'],
  }, { id: 'snapshot-1', receivedAt: 2 }))

  assert.equal(duplicate.duplicate, true)
  assert.equal(duplicate.applied, false)
  assert.equal(duplicate.snapshot, before)
  assert.equal(runtime.getDesktopSnapshot().rev, 1)
  assert.equal(runtime.getDesktopSnapshot().sessionsById.s2, undefined)
})

test('V3 runtime store advances cursors only after successful reducer application', () => {
  const runtime = createV3RuntimeController()
  runtime.applyEnvelope(createV3SnapshotEnvelope({
    rev: 10,
    sessionsById: { s1: session('s1', 10) },
    sessionOrder: ['s1'],
  }, { receivedAt: 10 }))

  const applied = runtime.applyEnvelope(normalizeV3RealtimeFrame({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    endpoint_cursor: 'cursor-11',
    rev: 11,
    prevRev: 10,
    event_type: 'desktop/message/upsert',
    event: {
      session_id: 's1',
      event_type: 'desktop/message/upsert',
      payload: { message: message('s1', 'm1', 11) },
      seq: 11,
    },
  }, { receivedAt: 11 }))

  assert.equal(applied.applied, true)
  assert.equal(applied.cursorScope, 'session:s1')
  assert.equal(applied.snapshot.cursorsByScope['session:s1']?.endpointCursor, 'cursor-11')
  assert.equal(applied.snapshot.cursorsByScope['session:s1']?.globalSeq, 11)
  assert.equal(applied.snapshot.lastApply?.shouldAdvanceCursor, true)

  const rejected = runtime.applyEnvelope(normalizeV3RealtimeFrame({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    endpoint_cursor: 'cursor-13',
    rev: 13,
    prevRev: 99,
    event_type: 'desktop/message/upsert',
    event: {
      session_id: 's1',
      event_type: 'desktop/message/upsert',
      payload: { message: message('s1', 'm2', 13) },
      seq: 13,
    },
  }, { receivedAt: 13 }))

  assert.equal(rejected.rejected, true)
  assert.equal(rejected.cursorScope, null)
  assert.equal(rejected.snapshot.cursorsByScope['session:s1']?.endpointCursor, 'cursor-11')
  assert.equal(rejected.snapshot.desktop.messagesBySessionId.s1?.some((item) => item.id === 'm2'), false)
})

test('V3 selectors expose UI-safe projections from the runtime snapshot', () => {
  const runtime = createV3RuntimeController()
  runtime.applyEnvelope(createV3SnapshotEnvelope({
    rev: 20,
    sessionsById: {
      s1: { ...session('s1', 20, '/workspace/a'), usage: null },
      s2: session('s2', 10, '/workspace/b'),
    },
    sessionOrder: ['s1', 's2'],
    messagesBySessionId: {
      s1: [message('s1', 'm1', 20)],
    },
    runIntentsBySessionId: {
      s1: { sessionId: 's1', runId: 'run-1', status: 'running', blockedReason: '', createdAt: 20, updatedAt: 20, eventSeq: 20 },
    },
  }, { receivedAt: 20 }))
  const snapshot = runtime.getSnapshot()

  assert.equal(selectV3Session(snapshot, 's1')?.runIntent?.runId, 'run-1')
  assert.deepEqual(selectV3Messages(snapshot, 's1').map((item) => item.id), ['m1'])
  assert.deepEqual(selectV3WorkspaceSessions(snapshot, '/workspace/a').map((item) => item.id), ['s1'])
  assert.deepEqual(selectV3WorkspaceSessions(snapshot, { workspacePaths: ['/workspace/b', '/workspace/a'] }).map((item) => item.id), ['s1', 's2'])
})
