import assert from 'node:assert/strict'
import test from 'node:test'

import {
  applyDesktopDaemonEvent,
  getDesktopSnapshot,
  markDesktopStale,
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

test('markDesktopStale updates canonical stale state and requests resync', () => {
  replaceDesktopFromSnapshot({ rev: 30 })

  const stale = markDesktopStale('stream cursor mismatch')

  assert.equal(stale, getDesktopSnapshot())
  assert.equal(stale.rev, 30)
  assert.equal(stale.status, 'stale')
  assert.equal(stale.staleReason, 'stream cursor mismatch')
  assert.equal(stale.resyncRequested, true)
})
