import assert from 'node:assert/strict'
import test from 'node:test'

import {
  applyDesktopDaemonEvent,
  applyDesktopDurableEventEnvelope,
  getDesktopSnapshot,
  markDesktopStale,
  mergeDesktopSnapshot,
  replaceDesktopFromSnapshot,
  subscribeDesktop,
} from './desktop-state-store'
import type { DesktopSessionRecord } from '../types/realtime'

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

test('replaceDesktopFromSnapshot replaces the external store snapshot and notifies subscribers', () => {
  replaceDesktopFromSnapshot({ rev: 0 })

  const notifications: number[] = []
  const unsubscribe = subscribeDesktop(() => notifications.push(getDesktopSnapshot().rev))

  const next = replaceDesktopFromSnapshot({ rev: 12, sessionsById: { 'session-1': session('session-1', 20) } })

  assert.equal(next, getDesktopSnapshot())
  assert.equal(getDesktopSnapshot().rev, 12)
  assert.equal(getDesktopSnapshot().sessionsById['session-1']?.id, 'session-1')
  assert.deepEqual(notifications, [12])

  unsubscribe()
  replaceDesktopFromSnapshot({ rev: 13 })
  assert.deepEqual(notifications, [12])
})

test('mergeDesktopSnapshot upserts scoped hydration without dropping other cached sessions', () => {
  replaceDesktopFromSnapshot({
    rev: 40,
    sessionsById: {
      active: session('active', 40),
      other: session('other', 20),
    },
    sessionOrder: ['active', 'other'],
    messagesBySessionId: {
      active: [{ id: 'active-message', sessionId: 'active', role: 'assistant', content: 'streaming', createdAt: 40, globalSeq: 40 }],
      other: [{ id: 'other-message', sessionId: 'other', role: 'assistant', content: 'cached', createdAt: 20, globalSeq: 20 }],
    },
  })

  mergeDesktopSnapshot({
    rev: 42,
    sessionsById: {
      active: { ...session('active', 42), title: 'Updated active' },
    },
    sessionOrder: ['active'],
    messagesBySessionId: {
      active: [{ id: 'active-message-2', sessionId: 'active', role: 'assistant', content: 'updated', createdAt: 42, globalSeq: 42 }],
    },
  })

  const snapshot = getDesktopSnapshot()
  assert.equal(snapshot.rev, 42)
  assert.equal(snapshot.sessionsById.active?.title, 'Updated active')
  assert.equal(snapshot.sessionsById.other?.title, 'Session other')
  assert.deepEqual(snapshot.sessionOrder, ['active', 'other'])
  assert.deepEqual(snapshot.messagesBySessionId.active?.map((message) => message.id), ['active-message-2'])
  assert.deepEqual(snapshot.messagesBySessionId.other?.map((message) => message.id), ['other-message'])
})

test('mergeDesktopSnapshot does not roll back newer live stream state from a delayed scoped snapshot', () => {
  const streaming = session('streaming', 50)
  streaming.lastEventSeq = 50
  streaming.projectionHighWatermarkSeq = 50
  streaming.live.status = 'running'
  streaming.live.runId = 'run-live'
  streaming.live.seq = 50
  streaming.live.summary = 'Streaming response...'

  replaceDesktopFromSnapshot({
    rev: 50,
    sessionsById: { streaming },
    sessionOrder: ['streaming'],
    runIntentsBySessionId: {
      streaming: { sessionId: 'streaming', runId: 'run-live', status: 'running', blockedReason: '', createdAt: 50, updatedAt: 50, eventSeq: 50 },
    },
  })

  mergeDesktopSnapshot({
    rev: 51,
    sessionsById: {
      streaming: { ...session('streaming', 45), lastEventSeq: 45, projectionHighWatermarkSeq: 45 },
    },
    sessionOrder: ['streaming'],
  })

  const merged = getDesktopSnapshot().sessionsById.streaming
  assert.equal(merged?.updatedAt, 50)
  assert.equal(merged?.live.status, 'running')
  assert.equal(merged?.live.runId, 'run-live')
  assert.equal(merged?.live.seq, 50)
  assert.equal(getDesktopSnapshot().runIntentsBySessionId.streaming?.runId, 'run-live')
})

test('applyDesktopDaemonEvent patches through the reducer and emits only on state changes', () => {
  replaceDesktopFromSnapshot({ rev: 20 })

  let notificationCount = 0
  const unsubscribe = subscribeDesktop(() => {
    notificationCount += 1
  })

  applyDesktopDaemonEvent({
    rev: 21,
    prevRev: 20,
    type: 'desktop/session/upsert',
    payload: { session: session('session-2', 30) },
  })

  assert.equal(getDesktopSnapshot().rev, 21)
  assert.equal(getDesktopSnapshot().sessionsById['session-2']?.id, 'session-2')
  assert.equal(notificationCount, 1)

  applyDesktopDaemonEvent({
    rev: 21,
    prevRev: 20,
    type: 'desktop/session/upsert',
    payload: { session: session('duplicate', 40) },
  })

  assert.equal(getDesktopSnapshot().sessionsById.duplicate, undefined)
  assert.equal(notificationCount, 1)
  unsubscribe()
})

test('applyDesktopDurableEventEnvelope routes session tool events by global stream envelope identity without sidebar log spam', () => {
  replaceDesktopFromSnapshot({ rev: 0 })
  const originalConsoleLog = console.log
  const logs: unknown[][] = []
  console.log = (...args: unknown[]) => {
    logs.push(args)
  }
  try {
    applyDesktopDurableEventEnvelope({
      global_seq: 100,
      source_seq: 7,
      stream: 'session:session-from-envelope',
      entity_id: 'session-from-envelope',
      event_type: 'session.tool.started',
      ts_unix_ms: 1234,
      payload: {
        run_id: 'run-1',
        tool_name: 'read',
        call_id: 'call-1',
        step_id: 'step-1',
        tool_instance_id: 'step-1:call-1',
        arguments: '{"path":"file.ts"}',
        step: 1,
      },
    })
  } finally {
    console.log = originalConsoleLog
  }

  const session = getDesktopSnapshot().sessionsById['session-from-envelope']
  assert.ok(session)
  assert.equal(session.live.status, 'running')
  assert.equal(session.live.sidebarToolName, 'read')
  assert.equal(session.live.toolName, 'read')
  assert.equal(session.live.toolCallId, 'call-1')
  assert.equal(session.live.lastEventType, 'session.tool.started')
  assert.equal(session.live.seq, 7)
  assert.equal(getDesktopSnapshot().sessionsById['']?.live.sidebarToolName, undefined)
  assert.deepEqual(logs, [])
})

test('markDesktopStale updates canonical stale state and requests resync', () => {
  replaceDesktopFromSnapshot({ rev: 30 })

  const stale = markDesktopStale('stream cursor mismatch')

  assert.equal(stale, getDesktopSnapshot())
  assert.equal(stale.rev, 30)
  assert.equal(stale.status, 'stale')
  assert.equal(stale.staleReason, 'stream cursor mismatch')
  assert.equal(stale.resyncRequested, true)
})
