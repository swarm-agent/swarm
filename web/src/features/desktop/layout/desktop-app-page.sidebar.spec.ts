import assert from 'node:assert/strict'
import test from 'node:test'

import type { DesktopSessionRecord } from '../types/realtime'
import {
  compareSidebarSessions,
  sessionActivityLabel,
  sessionStatusDetail,
  sessionStatusTone,
  sessionTimerLabel,
} from './desktop-app-page'

function makeSession(id: string, overrides: Partial<DesktopSessionRecord> = {}): DesktopSessionRecord {
  return {
    id,
    title: id,
    workspacePath: '/repo',
    workspaceName: 'repo',
    mode: 'auto',
    metadata: undefined,
    messageCount: 0,
    updatedAt: 1_000,
    createdAt: 500,
    permissionsHydrated: false,
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
      toolHistory: [],
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
    ...overrides,
  }
}

test('sidebar labels render the direct stream tool label', () => {
  const session = makeSession('live-tool', {
    updatedAt: 10_000,
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-live-tool',
      startedAt: 10_000,
      lastEventAt: 12_500,
      lastEventType: 'session.tool.delta',
      toolName: 'manage-image',
      sidebarToolName: 'read',
      toolCallId: 'call-image',
      summary: 'manage-image',
    },
  })

  assert.equal(sessionStatusTone(session), 'running')
  assert.equal(sessionActivityLabel(session), 'read')
  assert.equal(sessionTimerLabel(session, 15_250), '5s')
  assert.equal(sessionStatusDetail(session, 15_250), 'just now')
})

test('sidebar labels do not fall back to retained tool state', () => {
  const session = makeSession('retained-tool', {
    updatedAt: 10_000,
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-retained-tool',
      startedAt: 10_000,
      lastEventAt: 12_500,
      lastEventType: 'session.tool.completed',
      toolName: null,
    sidebarToolName: null,
      retainedToolName: 'bash',
      retainedToolState: 'done',
      summary: 'Assistant responding…',
    },
  })

  assert.equal(sessionActivityLabel(session), '')
})

test('sidebar labels do not fall back to live tool history', () => {
  const session = makeSession('history-tool', {
    updatedAt: 10_000,
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-history-tool',
      startedAt: 10_000,
      lastEventAt: 12_500,
      lastEventType: 'session.tool.delta',
      toolName: null,
    sidebarToolName: null,
      summary: 'Streaming response…',
      toolHistory: [
        {
          key: 'history-tool',
          sessionId: 'history-tool',
          runId: 'run-history-tool',
          stepId: 'step-1',
          callId: 'call-read',
          toolInstanceId: 'step-1:call-read',
          toolName: 'read',
          toolArguments: null,
          toolOutput: '',
          state: 'running',
          step: 1,
          seq: 22,
          startedAt: 11_000,
          updatedAt: 12_500,
          completedAt: null,
        },
      ],
    },
  })

  assert.equal(sessionActivityLabel(session), '')
})

test('sidebar active sort anchors to run start time, not latest DB live event', () => {
  const olderRunWithFreshToolEvent = makeSession('older-run-fresh-tool', {
    updatedAt: 20_000,
    createdAt: 1_000,
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-older',
      startedAt: 1_000,
      lastEventAt: 20_000,
      lastEventType: 'session.tool.delta',
      toolName: 'bash',
    },
  })
  const newerRunWithoutFreshEvent = makeSession('newer-run-stale', {
    updatedAt: 10_500,
    createdAt: 10_000,
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-newer',
      startedAt: 10_000,
      lastEventAt: 10_500,
      lastEventType: 'session.assistant.delta',
    },
  })

  assert.equal(compareSidebarSessions(newerRunWithoutFreshEvent, olderRunWithFreshToolEvent, 20_500) < 0, true)
})

test('sidebar active sort keeps initial start-time order when an older row streams', () => {
  const sessions = [
    makeSession('0', { updatedAt: 50_000, createdAt: 50_000, live: { ...makeSession('base').live, status: 'running', startedAt: 50_000, lastEventAt: 50_000 } }),
    makeSession('1', { updatedAt: 40_000, createdAt: 40_000, live: { ...makeSession('base').live, status: 'running', startedAt: 40_000, lastEventAt: 40_000 } }),
    makeSession('2', { updatedAt: 30_000, createdAt: 30_000, live: { ...makeSession('base').live, status: 'running', startedAt: 30_000, lastEventAt: 30_000 } }),
    makeSession('3', { updatedAt: 100_000, createdAt: 20_000, live: { ...makeSession('base').live, status: 'running', startedAt: 20_000, lastEventAt: 100_000 } }),
    makeSession('4', { updatedAt: 10_000, createdAt: 10_000, live: { ...makeSession('base').live, status: 'running', startedAt: 10_000, lastEventAt: 10_000 } }),
  ]

  assert.deepEqual([...sessions].sort((left, right) => compareSidebarSessions(left, right, 100_500)).map((session) => session.id), ['0', '1', '2', '3', '4'])
})

test('sidebar active sort keeps active DB sessions pinned above recent idle rows', () => {
  const active = makeSession('active', {
    updatedAt: 1_000,
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-active',
      startedAt: 1_000,
      lastEventAt: 1_500,
    },
  })
  const recentIdle = makeSession('recent-idle', {
    updatedAt: 20_000,
    live: makeSession('base').live,
  })

  assert.equal(compareSidebarSessions(active, recentIdle, 20_500) < 0, true)
})
