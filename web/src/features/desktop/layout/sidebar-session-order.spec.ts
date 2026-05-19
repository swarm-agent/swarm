import assert from 'node:assert/strict'
import test from 'node:test'

import type { DesktopSessionRecord } from '../types/realtime'
import { orderSidebarSessions, reconcileSidebarSessionOrder } from './sidebar-session-order'

function makeSession(id: string): DesktopSessionRecord {
  return {
    id,
    title: id,
    workspacePath: '/repo',
    workspaceName: 'repo',
    mode: 'auto',
    metadata: undefined,
    messageCount: 0,
    updatedAt: 0,
    createdAt: 1,
    permissionsHydrated: false,
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

test('keeps an existing sidebar session in place when live activity moves it to incoming first position', () => {
  const previousOrder = ['0', '1', '2', '3', '4']
  const incoming = ['3', '0', '1', '2', '4'].map(makeSession)

  assert.deepEqual(reconcileSidebarSessionOrder(previousOrder, incoming), ['0', '1', '2', '3', '4'])
})

test('compacts sidebar order when a previous session disappears', () => {
  const previousOrder = ['0', '1', '2', '3', '4']
  const incoming = ['0', '1', '2', '4'].map(makeSession)

  assert.deepEqual(reconcileSidebarSessionOrder(previousOrder, incoming), ['0', '1', '2', '4'])
})

test('places new sessions at their incoming position without reordering existing sessions', () => {
  const previousOrder = ['0', '1', '2', '3', '4']
  const incoming = ['0', '1', 'new', '2', '3', '4'].map(makeSession)

  assert.deepEqual(reconcileSidebarSessionOrder(previousOrder, incoming), ['0', '1', 'new', '2', '3', '4'])
})

test('ordered sidebar sessions use latest records while preserving stable id order', () => {
  const { order, sessions } = orderSidebarSessions(
    [makeSession('3'), makeSession('0'), makeSession('1'), makeSession('2'), makeSession('4')],
    ['0', '1', '2', '3', '4'],
  )

  assert.deepEqual(order, ['0', '1', '2', '3', '4'])
  assert.deepEqual(sessions.map((session) => session.id), ['0', '1', '2', '3', '4'])
})
