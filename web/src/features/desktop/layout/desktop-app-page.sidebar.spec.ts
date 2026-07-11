import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import test from 'node:test'

import type { DesktopSessionRecord } from '../types/realtime'
import { DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY } from '../session-v3/api'
import {
  SIDEBAR_SESSION_GROUPS,
  compareSidebarSessions,
  desktopRouteWorkspacePathForSession,
  buildSidebarSessionTree,
  filterInactiveSidebarSessionTrees,
  sessionActivityLabel,
  sessionStatusDetail,
  sessionStatusTone,
  sessionTimerLabel,
  sessionActiveRunIntent,
  sessionSidebarDisplayGroup,
  sidebarCheckboxVisibilityClass,
  sidebarRootIDsForSelectionGroup,
  sidebarShouldClearSelectionForSessionChange,
  sidebarShouldReleaseCheckboxRevealSuppression,
  sidebarShouldRenderSelectionToolbar,
} from './desktop-app-page'

test('sidebar session focus clears selection immediately, including same-route clicks', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const handlerStart = source.indexOf('const handleSelectSession = useCallback')
  const handlerEnd = source.indexOf('const chatWorkspacePath', handlerStart)
  const handlerSource = source.slice(handlerStart, handlerEnd)

  assert.ok(handlerStart >= 0 && handlerEnd > handlerStart)
  assert.match(handlerSource, /const normalizedSessionId = sessionId\.trim\(\)\s*handleClearSidebarSelection\(\)\s*void selectAndHydrateDesktopV3Session/)
  assert.doesNotMatch(handlerSource, /routeSessionId\s*[!=]==?\s*normalizedSessionId/)
})

test('external sidebar session changes also clear selection mode so checkboxes hide', () => {
  assert.equal(sidebarShouldClearSelectionForSessionChange('session-a', 'session-b'), true)
  assert.equal(sidebarShouldClearSelectionForSessionChange('session-a', 'session-a'), false)
  assert.equal(sidebarShouldClearSelectionForSessionChange('session-a', '  '), false)
  assert.equal(sidebarShouldClearSelectionForSessionChange('', 'session-a'), true)
})

test('activated sidebar rows suppress checkbox reveal until hover and focus both leave', () => {
  assert.equal(sidebarCheckboxVisibilityClass(true, true), 'w-4 opacity-100')
  assert.equal(sidebarCheckboxVisibilityClass(false, true), 'w-0 opacity-0')
  assert.match(sidebarCheckboxVisibilityClass(false, false), /group-hover:w-4/)
  assert.match(sidebarCheckboxVisibilityClass(false, false), /group-focus-within:w-4/)

  assert.equal(sidebarShouldReleaseCheckboxRevealSuppression(true, true), false)
  assert.equal(sidebarShouldReleaseCheckboxRevealSuppression(false, true), false)
  assert.equal(sidebarShouldReleaseCheckboxRevealSuppression(true, false), false)
  assert.equal(sidebarShouldReleaseCheckboxRevealSuppression(false, false), true)
})

test('search result activation clears selection before hydration, including same-session results', async () => {
  const source = await readFile(new URL('./desktop-app-page.tsx', import.meta.url), 'utf8')
  const handlerStart = source.indexOf('const handleOpenSearchResult = useCallback')
  const handlerEnd = source.indexOf('useEffect(() => {', handlerStart)
  const handlerSource = source.slice(handlerStart, handlerEnd)

  assert.ok(handlerStart >= 0 && handlerEnd > handlerStart)
  assert.match(handlerSource, /handleClearSidebarSelection\(\)\s*void selectAndHydrateDesktopV3Session\(sessionId\)/)
})

test('session route workspace ignores unbound remote paths from hydrated sessions', () => {
  const session = makeSession('remote-session', {
    workspacePath: '/remote/workspaces/swarm-go',
    metadata: {
      swarm_v3_source_workspace_path: '/remote/workspaces/swarm-go',
      local_workspace_binding_id: 'remote-binding',
    },
  })
  const localWorkspacePaths = new Set(['/local/swarm-go'])

  assert.equal(desktopRouteWorkspacePathForSession(session, new Map(), localWorkspacePaths), '')
  assert.equal(
    desktopRouteWorkspacePathForSession(
      session,
      new Map([['remote-binding', '/local/swarm-go']]),
      localWorkspacePaths,
    ),
    '/local/swarm-go',
  )
})

test('session route workspace accepts authoritative local metadata paths', () => {
  const session = makeSession('local-session', {
    workspacePath: '/runtime/swarm-go',
    metadata: { swarm_v3_source_workspace_path: '/local/swarm-go' },
  })

  assert.equal(
    desktopRouteWorkspacePathForSession(session, new Map(), new Set(['/local/swarm-go'])),
    '/local/swarm-go',
  )
})

function activeRunIntent(
  sessionId: string,
  runId: string,
  createdAt: number,
  updatedAt = createdAt,
  timing: { startedAt?: number; cumulativeDurationMs?: number } = {},
) {
  return {
    sessionId,
    runId,
    status: 'running',
    blockedReason: '',
    createdAt,
    startedAt: timing.startedAt ?? createdAt,
    cumulativeDurationMs: timing.cumulativeDurationMs,
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

test('sidebar active sort keeps earlier active positions above newer live activity', () => {
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

  assert.equal(compareSidebarSessions(olderRunWithFreshToolEvent, newerRunWithoutFreshEvent, 20_500) < 0, true)
})

test('sidebar active sort keeps first-started position order when an older row streams', () => {
  const sessions = [
    makeSession('0', { updatedAt: 50_000, createdAt: 50_000, runIntent: activeRunIntent('0', 'run-0', 50_000), live: { ...makeSession('base').live, status: 'running', startedAt: 50_000, lastEventAt: 50_000 } }),
    makeSession('1', { updatedAt: 40_000, createdAt: 40_000, runIntent: activeRunIntent('1', 'run-1', 40_000), live: { ...makeSession('base').live, status: 'running', startedAt: 40_000, lastEventAt: 40_000 } }),
    makeSession('2', { updatedAt: 30_000, createdAt: 30_000, runIntent: activeRunIntent('2', 'run-2', 30_000), live: { ...makeSession('base').live, status: 'running', startedAt: 30_000, lastEventAt: 30_000 } }),
    makeSession('3', { updatedAt: 100_000, createdAt: 20_000, runIntent: activeRunIntent('3', 'run-3', 20_000, 100_000), live: { ...makeSession('base').live, status: 'running', startedAt: 20_000, lastEventAt: 100_000 } }),
    makeSession('4', { updatedAt: 10_000, createdAt: 10_000, runIntent: activeRunIntent('4', 'run-4', 10_000), live: { ...makeSession('base').live, status: 'running', startedAt: 10_000, lastEventAt: 10_000 } }),
  ]

  assert.deepEqual([...sessions].sort((left, right) => compareSidebarSessions(left, right, 100_500)).map((session) => session.id), ['4', '3', '2', '1', '0'])
})

test('sidebar active sort positions a restarted old conversation by its new run start', () => {
  const existingActive = makeSession('existing-active', {
    updatedAt: 60_000,
    createdAt: 60_000,
    runIntent: activeRunIntent('existing-active', 'run-existing', 60_000),
    live: { ...makeSession('base').live, status: 'running', startedAt: 60_000, lastEventAt: 60_000 },
  })
  const restartedOldConversation = makeSession('restarted-old', {
    updatedAt: 100_000,
    createdAt: 1_000,
    runIntent: activeRunIntent('restarted-old', 'run-restarted', 100_000),
    live: { ...makeSession('base').live, status: 'running', startedAt: 100_000, lastEventAt: 100_000 },
  })

  assert.equal(compareSidebarSessions(existingActive, restartedOldConversation, 100_500) < 0, true)
})

test('sidebar inactivity filter hides stale ordinary trees but protects selected, pinned, running, and active descendants', () => {
  const now = 100 * 60 * 60 * 1000
  const staleAt = now - 13 * 60 * 60 * 1000
  const recentAt = now - 2 * 60 * 60 * 1000
  const sessions = [
    makeSession('stale', { updatedAt: staleAt }),
    makeSession('selected', { updatedAt: staleAt }),
    makeSession('pinned', { updatedAt: staleAt, metadata: { [DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY]: true } }),
    makeSession('recent', { updatedAt: recentAt }),
    makeSession('parent', { updatedAt: staleAt }),
    makeSession('child', { updatedAt: recentAt, metadata: { parent_session_id: 'parent', requested_subagent: 'explorer' } }),
  ]
  const result = filterInactiveSidebarSessionTrees(buildSidebarSessionTree(sessions, now), now, 12, 'selected')
  assert.deepEqual(result.nodes.map((node) => node.session.id).sort(), ['parent', 'pinned', 'recent', 'selected'])
  assert.equal(result.hiddenCount, 1)
  assert.equal(filterInactiveSidebarSessionTrees(buildSidebarSessionTree(sessions, now), now, null).hiddenCount, 0)
})

test('sidebar manual pin moves an active chat into the Pinned display group', () => {
  const pinnedIdle = makeSession('pinned-idle', {
    updatedAt: 10_000,
    createdAt: 1_000,
    metadata: { [DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY]: true },
  })
  const staleIdle = makeSession('stale-idle', {
    updatedAt: 80_000,
    createdAt: 70_000,
  })

  assert.equal(sessionSidebarDisplayGroup(pinnedIdle), 'pinned')
  assert.equal(sessionSidebarDisplayGroup(staleIdle), 'active_chats')
  assert.equal(compareSidebarSessions(pinnedIdle, staleIdle, 100_500) < 0, true)
})

test('sidebar global selection includes roots across every visible display group', () => {
  const now = 100_000
  const nodes = buildSidebarSessionTree([
    makeSession('review', { metadata: { swarm_v3_sidebar_group: 'needs_review' } }),
    makeSession('progress', { metadata: { swarm_v3_sidebar_group: 'in_progress' } }),
    makeSession('pinned', { metadata: { [DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY]: true } }),
    makeSession('chat'),
  ], now)

  assert.deepEqual(sidebarRootIDsForSelectionGroup(nodes, null), ['review', 'progress', 'pinned', 'chat'])
  assert.deepEqual(sidebarRootIDsForSelectionGroup(nodes, 'needs_review'), ['review'])
  assert.deepEqual(sidebarRootIDsForSelectionGroup(nodes, 'active_chats'), ['chat'])
})

test('sidebar selection toolbar renders only in the master group', () => {
  assert.equal(sidebarShouldRenderSelectionToolbar(true, 'needs_review', 'needs_review'), true)
  assert.equal(sidebarShouldRenderSelectionToolbar(true, 'needs_review', 'active_chats'), false)
  assert.equal(sidebarShouldRenderSelectionToolbar(false, 'needs_review', 'needs_review'), false)
  assert.equal(sidebarShouldRenderSelectionToolbar(true, null, 'needs_review'), false)
})

test('sidebar renders contextual controls for active groups without an Archived section', () => {
  assert.deepEqual(SIDEBAR_SESSION_GROUPS.map((group) => group.id), [
    'needs_review',
    'in_progress',
    'pinned',
    'active_chats',
  ])
  assert.deepEqual(
    SIDEBAR_SESSION_GROUPS.filter((group) => group.showInactiveThreshold).map((group) => group.id),
    ['active_chats'],
  )
})

test('sidebar manual pin sort ignores pins for in-progress plan sessions', () => {
  const pinnedInProgressPlan = makeSession('pinned-in-progress-plan', {
    updatedAt: 10_000,
    createdAt: 1_000,
    metadata: {
      [DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY]: true,
      swarm_v3_sidebar_group: 'in_progress',
    },
  })
  const staleIdle = makeSession('stale-idle', {
    updatedAt: 80_000,
    createdAt: 70_000,
  })

  assert.equal(sessionSidebarDisplayGroup(pinnedInProgressPlan), 'in_progress')
  assert.equal(compareSidebarSessions(staleIdle, pinnedInProgressPlan, 100_500) < 0, true)
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


test('sidebar needs review sessions without active runs sort by durable last activity', () => {
  const staleNeedsReview = makeSession('stale-needs-review', {
    updatedAt: 10_000,
    createdAt: 1_000,
    metadata: { swarm_v3_sidebar_group: 'needs_review' },
  })
  const recentPaused = makeSession('recent-paused', {
    updatedAt: 90_000,
    createdAt: 80_000,
  })

  assert.equal(compareSidebarSessions(recentPaused, staleNeedsReview, 120_000) < 0, true)
  assert.equal(sessionStatusDetail(staleNeedsReview, 120_000), '1 min ago')
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


test('sidebar timer uses backend run timing and cumulative duration instead of created_at', () => {
  const session = makeSession('backend-timed-run', {
    runIntent: activeRunIntent('backend-timed-run', 'run-backend', 1_000, 120_000, {
      startedAt: 120_000,
      cumulativeDurationMs: 90_000,
    }),
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-backend',
      startedAt: 1_000,
      lastEventAt: 125_000,
    },
  })

  assert.equal(sessionTimerLabel(session, 125_000), '5s (1m35s)')
})

test('sidebar timer uses compact loop and overall duration pattern', () => {
  const session = makeSession('second-run', {
    runIntent: activeRunIntent('second-run', 'run-second', 1_000, 120_000, {
      startedAt: 120_000,
      cumulativeDurationMs: 90_000,
    }),
  })

  assert.equal(sessionTimerLabel(session, 125_000), '5s (1m35s)')
})

test('sidebar active timer falls back to canonical run created_at when started_at is absent', () => {
  const session = makeSession('created-at-run', {
    runIntent: {
      ...activeRunIntent('created-at-run', 'run-created-at', 10_000, 15_000),
      startedAt: undefined,
    },
  })

  assert.equal(sessionTimerLabel(session, 15_500), '5s')
  assert.equal(sessionActivityLabel(session), '')
})

test('sidebar pending executor status uses canonical run state and usable timing', () => {
  const session = makeSession('pending-run', {
    runIntent: {
      ...activeRunIntent('pending-run', 'run-pending', 10_000, 10_500),
      status: 'pending_executor',
      startedAt: undefined,
    },
  })

  assert.equal(sessionTimerLabel(session, 12_500), '2s')
  assert.equal(sessionActivityLabel(session), '')
  assert.notEqual(sessionActivityLabel(session), 'Starting')
  assert.notEqual(sessionActivityLabel(session), 'Pending executor')
  assert.notEqual(sessionActivityLabel(session), 'Pending execution')
})

test('sidebar active timer falls back to canonical run updated_at when start and create times are absent', () => {
  const session = makeSession('updated-at-run', {
    runIntent: {
      ...activeRunIntent('updated-at-run', 'run-updated-at', 0, 10_500),
      startedAt: undefined,
    },
  })

  assert.equal(sessionTimerLabel(session, 12_500), '2s')
  assert.equal(sessionActivityLabel(session), '')
})

test('sidebar timer does not fall back to live without a canonical active run intent', () => {
  const session = makeSession('no-run-intent', {
    live: {
      ...makeSession('base').live,
      status: 'running',
      runId: 'run-live-only',
      startedAt: 1_000,
      lastEventAt: 125_000,
    },
  })

  assert.equal(sessionTimerLabel(session, 125_000), '')
})
