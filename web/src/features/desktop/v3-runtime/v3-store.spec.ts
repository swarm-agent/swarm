import assert from 'node:assert/strict'
import test from 'node:test'

import type { ChatMessageRecord } from '../chat/types/chat'
import type { DesktopSessionRecord } from '../types/realtime'
import {
  createV3RuntimeController,
  createV3EventEnvelope,
  createV3SnapshotEnvelope,
  normalizeV3RealtimeFrame,
  selectV3ActiveRun,
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

function userMessage(sessionId: string, id: string, globalSeq: number): ChatMessageRecord {
  return {
    ...message(sessionId, id, globalSeq),
    role: 'user',
    content: `user ${id}`,
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

test('V3 runtime store does not advance realtime cursors for duplicate websocket/replay overlap', () => {
  const runtime = createV3RuntimeController()
  runtime.applyEnvelope(createV3SnapshotEnvelope({
    rev: 10,
    sessionsById: { s1: session('s1', 10) },
    sessionOrder: ['s1'],
  }, { receivedAt: 10 }))

  const websocketFrame = {
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    endpoint_cursor: 'cursor-11',
    rev: 11,
    prevRev: 10,
    event_type: 'desktop/message/upsert',
    event: {
      id: 'event-s1-m1',
      session_id: 's1',
      event_type: 'desktop/message/upsert',
      payload: { message: message('s1', 'm1', 11) },
      seq: 11,
    },
  }
  const applied = runtime.applyEnvelope(normalizeV3RealtimeFrame(websocketFrame, { receivedAt: 11 }))
  const duplicateReplay = runtime.applyEnvelope(normalizeV3RealtimeFrame({
    ...websocketFrame,
    endpoint_cursor: 'cursor-12',
  }, { receivedAt: 12 }))

  assert.equal(applied.applied, true)
  assert.equal(duplicateReplay.duplicate, true)
  assert.equal(duplicateReplay.shouldAdvanceCursor, false)
  assert.equal(runtime.getSnapshot().cursorsByScope['session:s1']?.endpointCursor, 'cursor-11')
  assert.deepEqual(runtime.getDesktopSnapshot().messagesBySessionId.s1?.map((item) => item.id), ['m1'])
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

test('V3 runtime assistant deltas update canonical live draft without mutating message cache', () => {
  const runtime = createV3RuntimeController()
  runtime.applyEnvelope(createV3SnapshotEnvelope({
    rev: 1,
    sessionsById: { s1: session('s1', 1) },
    sessionOrder: ['s1'],
    messagesBySessionId: { s1: [userMessage('s1', 'm-user-1', 1)] },
  }, { receivedAt: 1 }))

  runtime.applyEnvelope(normalizeV3RealtimeFrame({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    session_id: 's1',
    endpoint_cursor: 'cursor-2',
    event_type: 'session.assistant.delta',
    event: {
      id: 'evt-s1-2',
      session_id: 's1',
      event_type: 'session.assistant.delta',
      seq: 2,
      ts_unix_ms: 2,
      payload: { run_id: 'run-1', delta: 'hello ' },
    },
  }, { receivedAt: 2 }))
  runtime.applyEnvelope(normalizeV3RealtimeFrame({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    session_id: 's1',
    endpoint_cursor: 'cursor-3',
    event_type: 'session.assistant.delta',
    event: {
      id: 'evt-s1-3',
      session_id: 's1',
      event_type: 'session.assistant.delta',
      seq: 3,
      ts_unix_ms: 3,
      payload: { run_id: 'run-1', delta: 'world' },
    },
  }, { receivedAt: 3 }))

  const snapshot = runtime.getSnapshot()
  assert.equal(selectV3Session(snapshot, 's1')?.live.assistantDraft, 'hello world')
  assert.equal(selectV3Session(snapshot, 's1')?.live.status, 'running')
  assert.equal(selectV3Session(snapshot, 's1')?.live.awaitingAck, false)
  assert.equal(selectV3Session(snapshot, 's1')?.live.summary, 'Streaming response…')
  assert.equal(selectV3Session(snapshot, 's1')?.live.error, null)
  assert.deepEqual(selectV3Messages(snapshot, 's1').map((item) => item.id), ['m-user-1'])
  assert.deepEqual([
    ...selectV3Messages(snapshot, 's1').map((item) => item.content),
    selectV3Session(snapshot, 's1')?.live.assistantDraft,
  ], ['user m-user-1', 'hello world'])
})

test('V3 runtime completion merges final assistant message and clears live draft without duplicates', () => {
  const runtime = createV3RuntimeController()
  runtime.applyEnvelope(createV3SnapshotEnvelope({
    rev: 1,
    sessionsById: { s1: session('s1', 1) },
    sessionOrder: ['s1'],
    messagesBySessionId: { s1: [userMessage('s1', 'm-user-1', 1)] },
  }, { receivedAt: 1 }))
  runtime.applyEnvelope(createV3EventEnvelope({
    type: 'session.assistant.delta',
    payload: { session_id: 's1', run_id: 'run-1', delta: 'hello world' },
  }, { receivedAt: 2 }))

  runtime.applyEnvelope(createV3EventEnvelope({
    type: 'session.assistant.completed',
    payload: {
      session_id: 's1',
      run_id: 'run-1',
      status: 'completed',
      message: {
        id: 'm-assistant-2',
        session_id: 's1',
        global_seq: 2,
        role: 'assistant',
        content: 'hello world',
        created_at: 2,
      },
      run_intent: { session_id: 's1', run_id: 'run-1', status: 'completed', updated_at: 2, event_seq: 2 },
    },
  }, { receivedAt: 3 }))

  const snapshot = runtime.getSnapshot()
  assert.equal(selectV3Session(snapshot, 's1')?.live.assistantDraft, '')
  assert.deepEqual(selectV3Messages(snapshot, 's1').map((item) => item.id), ['m-user-1', 'm-assistant-2'])
  assert.equal(selectV3Messages(snapshot, 's1').filter((item) => item.role === 'assistant' && item.content === 'hello world').length, 1)
})

test('V3 metadata-only snapshot merge preserves live draft and cached history', () => {
  const runtime = createV3RuntimeController()
  const base = session('s1', 1)
  runtime.applyEnvelope(createV3SnapshotEnvelope({
    rev: 1,
    sessionsById: { s1: base },
    sessionOrder: ['s1'],
    messagesBySessionId: { s1: [userMessage('s1', 'm-user-1', 1)] },
    runIntentsBySessionId: {
      s1: { sessionId: 's1', runId: 'run-1', status: 'running', blockedReason: '', createdAt: 1, updatedAt: 1, eventSeq: 1 },
    },
  }, { receivedAt: 1 }))
  runtime.applyEnvelope(createV3EventEnvelope({
    type: 'session.assistant.delta',
    payload: { session_id: 's1', run_id: 'run-1', delta: 'streaming' },
  }, { receivedAt: 2 }))

  runtime.applyEnvelope(createV3SnapshotEnvelope({
    rev: runtime.getDesktopSnapshot().rev + 1,
    sessionsById: { s1: { ...base, updatedAt: 3, lastEventSeq: 99, projectionHighWatermarkSeq: 99 } },
    sessionOrder: ['s1'],
    messagesBySessionId: { s1: [] },
  }, {
    mode: 'merge',
    receivedAt: 3,
    source: { kind: 'http', transport: 'http', name: 'metadata-only' },
  }))

  const snapshot = runtime.getSnapshot()
  assert.equal(selectV3Session(snapshot, 's1')?.live.assistantDraft, 'streaming')
  assert.equal(selectV3Session(snapshot, 's1')?.live.status, 'running')
  assert.equal(selectV3Session(snapshot, 's1')?.live.runId, 'run-1')
  assert.equal(selectV3Session(snapshot, 's1')?.runIntent?.runId, 'run-1')
  assert.deepEqual(selectV3Messages(snapshot, 's1').map((item) => item.id), ['m-user-1'])
})

test('V3 message hot cache keeps newest 200 final messages while live draft stays transient', () => {
  const runtime = createV3RuntimeController()
  runtime.applyEnvelope(createV3SnapshotEnvelope({
    rev: 1,
    sessionsById: { s1: session('s1', 1) },
    sessionOrder: ['s1'],
    messagesBySessionId: {
      s1: Array.from({ length: 201 }, (_, index) => message('s1', `m-${index + 1}`, index + 1)),
    },
  }, { receivedAt: 1 }))
  runtime.applyEnvelope(createV3EventEnvelope({
    type: 'session.assistant.delta',
    payload: { session_id: 's1', run_id: 'run-1', delta: 'still live' },
  }, { receivedAt: 2 }))

  const snapshot = runtime.getSnapshot()
  const messages = selectV3Messages(snapshot, 's1')
  assert.equal(messages.length, 200)
  assert.equal(messages[0]?.id, 'm-2')
  assert.equal(messages[messages.length - 1]?.id, 'm-201')
  assert.equal(selectV3Session(snapshot, 's1')?.live.assistantDraft, 'still live')
})


test('V3 active-run selector ignores stale inactive lifecycle when canonical run intent is active', () => {
  const runtime = createV3RuntimeController()
  runtime.applyEnvelope(createV3SnapshotEnvelope({
    rev: 30,
    sessionsById: {
      s1: {
        ...session('s1', 30),
        lifecycle: {
          sessionId: 's1',
          runId: 'run-stale',
          active: false,
          phase: 'completed',
          startedAt: 10,
          endedAt: 20,
          updatedAt: 20,
          generation: 1,
          stopReason: null,
          error: null,
          ownerTransport: null,
        },
      },
    },
    sessionOrder: ['s1'],
    runIntentsBySessionId: {
      s1: { sessionId: 's1', runId: 'run-canonical', status: 'running', blockedReason: '', createdAt: 30, updatedAt: 31, eventSeq: 31 },
    },
  }, { receivedAt: 30 }))

  const snapshot = runtime.getSnapshot()
  assert.equal(selectV3ActiveRun(snapshot, 's1')?.runId, 'run-canonical')
  assert.equal(selectV3Session(snapshot, 's1')?.runIntent?.runId, 'run-canonical')
})

test('V3 active-run selector ignores lifecycle/live-only active state without canonical run intent', () => {
  const runtime = createV3RuntimeController()
  const liveOnly = session('s1', 40)
  liveOnly.lifecycle = {
    sessionId: 's1',
    runId: 'run-lifecycle-only',
    active: true,
    phase: 'running',
    startedAt: 40,
    endedAt: 0,
    updatedAt: 41,
    generation: 1,
    stopReason: null,
    error: null,
    ownerTransport: null,
  }
  liveOnly.live.status = 'running'
  liveOnly.live.runId = 'run-live-only'
  liveOnly.live.startedAt = 40

  runtime.applyEnvelope(createV3SnapshotEnvelope({
    rev: 40,
    sessionsById: { s1: liveOnly },
    sessionOrder: ['s1'],
  }, { receivedAt: 40 }))

  const snapshot = runtime.getSnapshot()
  assert.equal(selectV3ActiveRun(snapshot, 's1'), null)
  assert.equal(selectV3Session(snapshot, 's1')?.runIntent, null)
})

test('V3 terminal canonical run intent clears active selector despite stale live state', () => {
  const runtime = createV3RuntimeController()
  const active = session('s1', 50)
  active.live.status = 'running'
  active.live.runId = 'run-1'
  active.live.startedAt = 50
  runtime.applyEnvelope(createV3SnapshotEnvelope({
    rev: 50,
    sessionsById: { s1: active },
    sessionOrder: ['s1'],
    runIntentsBySessionId: {
      s1: { sessionId: 's1', runId: 'run-1', status: 'running', blockedReason: '', createdAt: 50, updatedAt: 50, eventSeq: 50 },
    },
  }, { receivedAt: 50 }))

  runtime.applyEnvelope(createV3EventEnvelope({
    type: 'session.run_intent.recorded',
    payload: {
      session_id: 's1',
      status: 'idle',
      run_intent: { session_id: 's1', run_id: 'run-1', status: 'completed', updated_at: 60, event_seq: 60 },
    },
  }, { receivedAt: 60 }))

  const snapshot = runtime.getSnapshot()
  assert.equal(selectV3ActiveRun(snapshot, 's1'), null)
  assert.equal(selectV3Session(snapshot, 's1')?.runIntent, null)
  assert.equal(selectV3Session(snapshot, 's1')?.live.status, 'idle')
})
