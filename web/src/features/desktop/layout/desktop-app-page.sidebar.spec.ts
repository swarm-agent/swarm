import assert from 'node:assert/strict'
import test from 'node:test'

import type { DesktopSessionRecord } from '../types/realtime'
import {
  compareSidebarSessions,
  sessionActivityLabel,
  sessionStatusDetail,
  sessionStatusTone,
  sessionTimerLabel,
  sessionActiveRunIntent,
} from './desktop-app-page'

function activeRunIntent(sessionId: string, runId: string, createdAt: number, updatedAt = createdAt) {
  return {
    sessionId,
    runId,
    status: 'running',
    blockedReason: '',
    createdAt,
    updatedAt,
    eventSeq: 1,
  }
}

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
    runIntent: null,
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
    runIntent: activeRunIntent('live-tool', 'run-live-tool', 10_000, 12_500),
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
    runIntent: activeRunIntent('retained-tool', 'run-retained-tool', 10_000, 12_500),
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
    runIntent: activeRunIntent('history-tool', 'run-history-tool', 10_000, 12_500),
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
    runIntent: activeRunIntent('older-run-fresh-tool', 'run-older', 1_000, 20_000),
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
    runIntent: activeRunIntent('newer-run-stale', 'run-newer', 10_000, 10_500),
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
    makeSession('0', { updatedAt: 50_000, createdAt: 50_000, runIntent: activeRunIntent('0', 'run-0', 50_000), live: { ...makeSession('base').live, status: 'running', startedAt: 50_000, lastEventAt: 50_000 } }),
    makeSession('1', { updatedAt: 40_000, createdAt: 40_000, runIntent: activeRunIntent('1', 'run-1', 40_000), live: { ...makeSession('base').live, status: 'running', startedAt: 40_000, lastEventAt: 40_000 } }),
    makeSession('2', { updatedAt: 30_000, createdAt: 30_000, runIntent: activeRunIntent('2', 'run-2', 30_000), live: { ...makeSession('base').live, status: 'running', startedAt: 30_000, lastEventAt: 30_000 } }),
    makeSession('3', { updatedAt: 100_000, createdAt: 20_000, runIntent: activeRunIntent('3', 'run-3', 20_000, 100_000), live: { ...makeSession('base').live, status: 'running', startedAt: 20_000, lastEventAt: 100_000 } }),
    makeSession('4', { updatedAt: 10_000, createdAt: 10_000, runIntent: activeRunIntent('4', 'run-4', 10_000), live: { ...makeSession('base').live, status: 'running', startedAt: 10_000, lastEventAt: 10_000 } }),
  ]

  assert.deepEqual([...sessions].sort((left, right) => compareSidebarSessions(left, right, 100_500)).map((session) => session.id), ['0', '1', '2', '3', '4'])
})

test('sidebar active sort keeps active DB sessions pinned above recent idle rows', () => {
  const active = makeSession('active', {
    updatedAt: 1_000,
    runIntent: activeRunIntent('active', 'run-active', 1_000, 1_500),
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

test('sidebar stopped sort keeps active rows above a newly stopped long session', () => {
  const active = makeSession('active', {
    updatedAt: 40_000,
    createdAt: 30_000,
    runIntent: activeRunIntent('active', 'run-active', 30_000, 40_000),
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-active',
      startedAt: 30_000,
      lastEventAt: 40_000,
    },
  })
  const stoppedLongSession = makeSession('stopped-long', {
    updatedAt: 100_000,
    createdAt: 1_000,
    lifecycle: {
      sessionId: 'stopped-long',
      runId: 'run-stopped-long',
      active: false,
      phase: 'stopped',
      startedAt: 1_000,
      endedAt: 100_000,
      updatedAt: 100_000,
      generation: 1,
      stopReason: 'completed',
      error: null,
      ownerTransport: null,
    },
    live: makeSession('base').live,
  })

  assert.equal(compareSidebarSessions(active, stoppedLongSession, 100_500) < 0, true)
})

test('sidebar stopped sort resolves long sessions by durable activity instead of start time', () => {
  const stoppedLongSession = makeSession('stopped-long', {
    updatedAt: 100_000,
    createdAt: 1_000,
    lifecycle: {
      sessionId: 'stopped-long',
      runId: 'run-stopped-long',
      active: false,
      phase: 'stopped',
      startedAt: 1_000,
      endedAt: 100_000,
      updatedAt: 100_000,
      generation: 1,
      stopReason: 'completed',
      error: null,
      ownerTransport: null,
    },
    live: makeSession('base').live,
  })
  const previouslyNewerIdle = makeSession('previously-newer-idle', {
    updatedAt: 90_000,
    createdAt: 80_000,
    lifecycle: {
      sessionId: 'previously-newer-idle',
      runId: 'run-previously-newer-idle',
      active: false,
      phase: 'stopped',
      startedAt: 80_000,
      endedAt: 90_000,
      updatedAt: 90_000,
      generation: 1,
      stopReason: 'completed',
      error: null,
      ownerTransport: null,
    },
    live: makeSession('base').live,
  })

  assert.equal(compareSidebarSessions(stoppedLongSession, previouslyNewerIdle, 130_000) < 0, true)
})


test('sidebar active status and timer ignore lifecycle/live-only liveness without canonical active run', () => {
  const liveOnly = makeSession('live-only', {
    updatedAt: 20_000,
    lifecycle: {
      sessionId: 'live-only',
      runId: 'run-lifecycle-only',
      active: true,
      phase: 'running',
      startedAt: 1_000,
      endedAt: 0,
      updatedAt: 20_000,
      generation: 1,
      stopReason: null,
      error: null,
      ownerTransport: null,
    },
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-live-only',
      startedAt: 1_000,
      lastEventAt: 20_000,
    },
  })

  assert.equal(sessionActiveRunIntent(liveOnly), null)
  assert.equal(sessionStatusTone(liveOnly), 'idle')
  assert.equal(sessionActivityLabel(liveOnly), '')
})

test('sidebar stale inactive lifecycle cannot suppress canonical active timer/status', () => {
  const canonical = makeSession('canonical-active', {
    runIntent: activeRunIntent('canonical-active', 'run-canonical', 1_000, 2_000),
    lifecycle: {
      sessionId: 'canonical-active',
      runId: 'run-canonical',
      active: false,
      phase: 'completed',
      startedAt: 1_000,
      endedAt: 1_500,
      updatedAt: 1_500,
      generation: 1,
      stopReason: null,
      error: null,
      ownerTransport: null,
    },
    live: {
      ...makeSession('base').live,
      status: 'idle',
      runId: null,
      startedAt: null,
    },
  })

  assert.equal(sessionActiveRunIntent(canonical)?.runId, 'run-canonical')
  assert.equal(sessionStatusTone(canonical), 'running')
  assert.equal(sessionTimerLabel(canonical, 2_500), '1s')
})

test('sidebar terminal canonical run intent clears active status even if live remains running', () => {
  const terminal = makeSession('terminal-run', {
    runIntent: {
      ...activeRunIntent('terminal-run', 'run-terminal', 1_000, 2_000),
      status: 'cancelled',
    },
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-terminal',
      startedAt: 1_000,
    },
  })

  assert.equal(sessionActiveRunIntent(terminal), null)
  assert.equal(sessionStatusTone(terminal), 'idle')
  assert.equal(sessionActivityLabel(terminal), '')
})
