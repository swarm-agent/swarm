import test from 'node:test'
import assert from 'node:assert/strict'

import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from './desktop-v3-cache-reducer'
import { buildSidebarSessionTree, desktopSessionRecordFromV3SidebarRow, desktopSidebarWorkspacePathForSession } from '../layout/desktop-app-page'
import { selectDesktopSidebarGroupedRows, selectDesktopSidebarRows } from './desktop-v3-cache-selectors'
import { hydrateResponseToAction, realtimeFrameToActions, selectSession } from './desktop-v3-cache-wire'
import { hydrateSnapshotFixture, messageA1, messageB1, projectionA, projectionB, runIntentA, sessionA, sessionB, snapshotFixture } from './desktop-v3-cache.backend-fixtures'

test('sidebar selector uses scope order', () => {
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'scope-a' }
  state.sessionOrderByScope['scope-a'] = [sessionB.id, sessionA.id]
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.sessionsById[sessionB.id] = { kind: 'full', session: sessionB, needsHydrate: false }

  assert.deepEqual(selectDesktopSidebarRows(state).map((row) => row.sessionId), [sessionB.id, sessionA.id])
})

test('Desktop V3 sidebar render uses bootstrap session_order across workspaces', () => {
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'scope-a' }
  state.sessionOrderByScope['scope-a'] = [sessionB.id, sessionA.id]
  state.sessionsById[sessionA.id] = {
    kind: 'full',
    session: { ...sessionA, workspace_path: '/workspace/a', workspace_name: 'workspace-a' },
    needsHydrate: false,
  }
  state.sessionsById[sessionB.id] = {
    kind: 'full',
    session: { ...sessionB, workspace_path: '/workspace/b', workspace_name: 'workspace-b' },
    needsHydrate: false,
  }

  const rows = selectDesktopSidebarRows(state)
  const records = rows.map(desktopSessionRecordFromV3SidebarRow)
  const nodes = buildSidebarSessionTree(records, 25, true)

  assert.deepEqual(records.map((record) => record.id), [sessionB.id, sessionA.id])
  assert.deepEqual(nodes.map((node) => node.session.id), [sessionB.id, sessionA.id])
})

test('Desktop V3 sidebar record reflects server permission summary as blocked', () => {
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'scope-a' }
  state.sessionOrderByScope['scope-a'] = [sessionA.id]
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.permissionSummaryBySessionId[sessionA.id] = {
    pendingApprovalCount: 1,
    oldestPendingAt: 1,
    newestPendingAt: 1,
    updatedAt: 1,
  }

  const record = selectDesktopSidebarRows(state).map(desktopSessionRecordFromV3SidebarRow)[0]

  assert.equal(record.permissionsHydrated, true)
  assert.equal(record.pendingPermissionCount, 1)
  assert.equal(record.pendingPermissions.length, 0)
  assert.equal(record.live.status, 'blocked')
})

test('Desktop V3 sidebar replaces a realtime-discovered ID stub with the canonical hydrated title', () => {
  const state = createEmptyDesktopV3CacheState()
  const sessionId = 'session-client-b'
  const canonicalTitle = 'Canonical generated title'
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'scope-a' }
  state.sessionOrderByScope['scope-a'] = [sessionId]
  state.sessionsById[sessionId] = { kind: 'stub', id: sessionId, needsHydrate: true }
  state.projectionsBySession[sessionId] = {
    ...projectionB,
    session_id: sessionId,
    last_event_seq: 9,
    projection_high_watermark_seq: 9,
    updated_at: 90,
  }

  assert.equal(desktopSessionRecordFromV3SidebarRow(selectDesktopSidebarRows(state)[0]).title, sessionId)

  desktopV3CacheReducer(state, hydrateResponseToAction({
    ...hydrateSnapshotFixture({
      sessions_by_id: { [sessionId]: { ...sessionB, id: sessionId, title: canonicalTitle } },
      projections_by_session: { [sessionId]: { ...projectionB, session_id: sessionId } },
      messages_by_session: { [sessionId]: [] },
      session_order: [sessionId],
      selector: { kind: 'session_ids', session_ids: [sessionId] },
    }),
  }, [sessionId]))

  const record = desktopSessionRecordFromV3SidebarRow(selectDesktopSidebarRows(state)[0])
  assert.equal(state.sessionsById[sessionId]?.kind, 'full')
  assert.equal(record.title, canonicalTitle)
})

test('Desktop V3 sidebar render includes bootstrapped session outside launcher workspaces', () => {
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'scope-a' }
  state.sessionOrderByScope['scope-a'] = [sessionA.id]
  state.sessionsById[sessionA.id] = {
    kind: 'full',
    session: { ...sessionA, workspace_path: '/workspace/from-session-only', workspace_name: 'session-only' },
    needsHydrate: false,
  }

  const launcherWorkspacePaths: string[] = []
  const records = selectDesktopSidebarRows(state).map(desktopSessionRecordFromV3SidebarRow)
  const renderedNodes = buildSidebarSessionTree(records, 25, true)

  assert.deepEqual(renderedNodes.map((node) => node.session.id), [sessionA.id])
  assert.equal(launcherWorkspacePaths.includes(renderedNodes[0].session.workspacePath), false)
})

test('Desktop V3 worktree sidebar uses source workspace metadata instead of runtime path', () => {
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'scope-a' }
  state.sessionOrderByScope['scope-a'] = ['worktree-session']
  state.sessionsById['worktree-session'] = {
    kind: 'full',
    session: {
      ...sessionA,
      id: 'worktree-session',
      workspace_path: '/worktrees/swarm-go/agent/feature',
      workspace_name: 'swarm-go',
      worktree_enabled: true,
      worktree_root_path: '/worktrees/swarm-go/agent/feature',
      worktree_branch: 'agent/feature',
      metadata: {
        swarm_v3_source_workspace_path: '/workspaces/swarm-go',
        local_workspace_binding_id: 'binding-source',
      },
    },
    needsHydrate: false,
  }
  const row = selectDesktopSidebarRows(state)[0]
  const record = desktopSessionRecordFromV3SidebarRow(row)
  const workspacePathByBinding = new Map([['binding-source', '/workspaces/swarm-go']])

  assert.equal(desktopSidebarWorkspacePathForSession(record, workspacePathByBinding), '/workspaces/swarm-go')
  assert.equal(row.branchLabel, 'agent/feature')
  assert.equal(record.metadata?.swarm_v3_branch_label, 'agent/feature')
})

test('Desktop V3 sidebar derives plan row state, checkpoint progress, and explicit groups from active plan payload', () => {
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'scope-a' }
  state.sessionOrderByScope['scope-a'] = [sessionA.id, sessionB.id, 'chat-session']
  state.sessionsById[sessionA.id] = { kind: 'full', session: { ...sessionA, worktree_branch: '' }, needsHydrate: false }
  state.sessionsById[sessionB.id] = { kind: 'full', session: { ...sessionB, worktree_branch: 'agent/blocked' }, needsHydrate: false }
  state.sessionsById['chat-session'] = { kind: 'full', session: { ...sessionA, id: 'chat-session', title: 'Chat', worktree_branch: '' }, needsHydrate: false }
  state.hasActivePlanBySession[sessionA.id] = true
  state.plansBySession[sessionA.id] = {
    id: 'plan-review',
    title: 'Review plan',
    plan: '# Review',
    status: 'approved',
    approvalState: 'approved',
    updatedAt: 3,
    document: {
      id: 'plan-review',
      title: 'Review plan',
      status: 'approved',
      schemaVersion: '',
      revisionId: '',
      info: { goal: '', scope: '', context: '', decisions: [], constraints: [], assumptions: [], openQuestions: [], relevantFiles: [], successCriteria: [], validationStrategy: '' },
      executionPolicy: { mode: 'automatic', shape: 'checkpointed', followupCheckpointPolicy: '' },
      executionState: { status: 'waiting_review', activeAttemptId: 'cp-2:attempt-1', parentSessionId: sessionA.id, currentSessionId: sessionA.id, currentRunId: 'run-review', lastCheckpointId: 'cp-2', lastAttemptId: 'cp-2:attempt-1', lastOutcome: 'needs_review', startedAt: 1, updatedAt: 3, completedAt: 0 },
      checkpoints: [
        { id: 'cp-1', title: 'Done', status: 'completed', objective: '', tasks: [], acceptanceCriteria: [], notes: '', report: '', result: '', changedFiles: [], validation: [], attemptId: '', runId: '', sessionId: '', startedAt: 0, completedAt: 0, review: null, attempts: [], order: 1 },
        { id: 'cp-2', title: 'Review me', status: 'needs_review', objective: '', tasks: [], acceptanceCriteria: [], notes: '', report: '', result: '', changedFiles: [], validation: [], attemptId: 'cp-2:attempt-1', runId: 'run-review', sessionId: sessionA.id, startedAt: 2, completedAt: 3, review: { status: 'pending', reviewerId: '', reviewerType: '', result: '', notes: '', reviewedAt: 0 }, attempts: [], order: 2 },
      ],
      activeCheckpointId: 'cp-2',
      renderedText: '',
      displayText: '',
    },
  }
  state.hasActivePlanBySession[sessionB.id] = true
  state.plansBySession[sessionB.id] = {
    id: 'plan-running',
    title: 'Running plan',
    plan: '# Running',
    status: 'approved',
    approvalState: 'approved',
    updatedAt: 4,
    document: {
      id: 'plan-running',
      title: 'Running plan',
      status: 'approved',
      schemaVersion: '',
      revisionId: '',
      info: { goal: '', scope: '', context: '', decisions: [], constraints: [], assumptions: [], openQuestions: [], relevantFiles: [], successCriteria: [], validationStrategy: '' },
      executionPolicy: { mode: 'automatic', shape: 'checkpointed', followupCheckpointPolicy: '' },
      executionState: { status: 'in_progress', activeAttemptId: 'cp-1:attempt-1', parentSessionId: sessionB.id, currentSessionId: sessionB.id, currentRunId: 'run-running', lastCheckpointId: 'cp-1', lastAttemptId: 'cp-1:attempt-1', lastOutcome: '', startedAt: 1, updatedAt: 4, completedAt: 0 },
      checkpoints: [
        { id: 'cp-1', title: 'Running', status: 'in_progress', objective: '', tasks: [], acceptanceCriteria: [], notes: '', report: '', result: '', changedFiles: [], validation: [], attemptId: 'cp-1:attempt-1', runId: 'run-running', sessionId: sessionB.id, startedAt: 1, completedAt: 0, review: null, attempts: [], order: 1 },
      ],
      activeCheckpointId: 'cp-1',
      renderedText: '',
      displayText: '',
    },
  }

  const rows = selectDesktopSidebarRows(state)
  const reviewRow = rows.find((row) => row.sessionId === sessionA.id)
  const runningRow = rows.find((row) => row.sessionId === sessionB.id)
  const chatRow = rows.find((row) => row.sessionId === 'chat-session')
  assert.equal(reviewRow?.rowType, 'plan_session')
  assert.equal(reviewRow?.sidebarGroup, 'needs_review')
  assert.equal(reviewRow?.planExecution?.statusLabel, 'REVIEW')
  assert.equal(reviewRow?.planExecution?.checkpointProgress.label, '2/2')
  assert.equal(reviewRow?.branchLabel, '')
  assert.equal(runningRow?.sidebarGroup, 'in_progress')
  assert.equal(runningRow?.planExecution?.statusLabel, 'RUNNING')
  assert.equal(runningRow?.branchLabel, 'agent/blocked')
  assert.equal(chatRow?.rowType, 'single_chat')
  assert.equal(chatRow?.sidebarGroup, 'active_chats')

  const grouped = selectDesktopSidebarGroupedRows(state)
  assert.deepEqual(grouped.needs_review.map((row) => row.sessionId), [sessionA.id])
  assert.deepEqual(grouped.in_progress.map((row) => row.sessionId), [sessionB.id])
  assert.deepEqual(grouped.active_chats.map((row) => row.sessionId), ['chat-session'])
  assert.deepEqual(grouped.archived, [])
})

test('Desktop V3 sidebar derives archived rows from archived tombstones only', () => {
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'scope-a' }
  state.sessionOrderByScope['scope-a'] = [sessionA.id, sessionB.id]
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.sessionsById[sessionB.id] = { kind: 'full', session: sessionB, needsHydrate: false }
  state.tombstonesBySession[sessionA.id] = {
    session_id: sessionA.id,
    kind: 'archived',
    archived: true,
    updated_at: 50,
    session: { ...sessionA, title: 'Archived A' },
  }
  state.tombstonesBySession[sessionB.id] = {
    session_id: sessionB.id,
    kind: 'deleted',
    deleted: true,
    updated_at: 60,
    session: sessionB,
  }

  const rows = selectDesktopSidebarRows(state)
  assert.deepEqual(rows.map((row) => row.sessionId), [sessionA.id])
  assert.equal(rows[0].sidebarGroup, 'archived')

  const grouped = selectDesktopSidebarGroupedRows(state)
  assert.deepEqual(grouped.needs_review, [])
  assert.deepEqual(grouped.in_progress, [])
  assert.deepEqual(grouped.active_chats, [])
  assert.deepEqual(grouped.archived.map((row) => row.sessionId), [sessionA.id])

  const archivedRecord = desktopSessionRecordFromV3SidebarRow(grouped.archived[0])
  assert.equal(archivedRecord.title, 'Archived A')
  assert.equal(archivedRecord.metadata?.swarm_v3_sidebar_group, 'archived')
})

test('unsubscribed V3 workset archive frame removes the live sidebar row and retains an archived tombstone', () => {
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'scope-a' }
  state.sessionOrderByScope['scope-a'] = [sessionA.id, sessionB.id]
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.sessionsById[sessionB.id] = { kind: 'full', session: sessionB, needsHydrate: false }
  state.worksetsById['scope-a'] = { sessionIds: [sessionA.id, sessionB.id], inactiveSessionIds: [] }

  const tombstone = {
    session_id: sessionA.id,
    kind: 'archived',
    archived: true,
    deleted: false,
    updated_at: 200,
    session: sessionA,
  }
  const archiveCursor = 'archive-cursor'
  const actions = realtimeFrameToActions({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'workset.session.removed',
    workset_id: 'scope-a',
    session_id: sessionA.id,
    event_type: 'session.archived',
    endpoint_cursor: archiveCursor,
    event: {
      id: 'archive-a',
      session_id: sessionA.id,
      seq: 5,
      event_type: 'session.archived',
      payload: { session: sessionA, tombstone },
      ts_unix_ms: 200,
    },
  })
  for (const action of actions) desktopV3CacheReducer(state, action)

  assert.equal(actions[0].type, 'realtime.applyEvent')
  assert.equal(state.tombstonesBySession[sessionA.id]?.archived, true)
  assert.equal(state.realtime.endpointCursor, archiveCursor)
  assert.deepEqual(state.sessionOrderByScope['scope-a'], [sessionB.id])
  assert.deepEqual(state.worksetsById['scope-a'].sessionIds, [sessionB.id])
  assert.deepEqual(selectDesktopSidebarGroupedRows(state).archived.map((row) => row.sessionId), [sessionA.id])
})

test('archived V3 hydrate response cannot reintroduce a session into the active sidebar', () => {
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'scope-a' }
  state.sessionOrderByScope['scope-a'] = []
  state.worksetsById['scope-a'] = { sessionIds: [], inactiveSessionIds: [sessionA.id] }

  desktopV3CacheReducer(state, hydrateResponseToAction({
    ...hydrateSnapshotFixture({
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      messages_by_session: { [sessionA.id]: [] },
      session_order: [sessionA.id],
      selector: { kind: 'session_ids', session_ids: [sessionA.id] },
      tombstones_by_session: {
        [sessionA.id]: {
          session_id: sessionA.id,
          kind: 'archived',
          archived: true,
          deleted: false,
          updated_at: 200,
          session: sessionA,
        },
      },
    }),
  }, [sessionA.id]))

  assert.equal(state.tombstonesBySession[sessionA.id]?.archived, true)
  assert.deepEqual(state.sessionOrderByScope['scope-a'], [])
  assert.deepEqual(state.worksetsById['scope-a'].sessionIds, [])
  assert.deepEqual(selectDesktopSidebarGroupedRows(state).active_chats, [])
  assert.deepEqual(selectDesktopSidebarGroupedRows(state).archived.map((row) => row.sessionId), [sessionA.id])
})

test('archive mutation result moves sidebar row into Archived with cached session metadata', () => {
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'scope-a' }
  state.sessionOrderByScope['scope-a'] = [sessionA.id, sessionB.id]
  state.sessionOrderByScope['scope-pinned'] = [sessionA.id]
  state.sessionsById[sessionA.id] = {
    kind: 'full',
    session: {
      ...sessionA,
      title: 'Cached Session A',
      workspace_path: '/workspace/cached',
      workspace_name: 'cached-workspace',
      metadata: { swarm_v3_desktop_sidebar_pinned: true },
    },
    needsHydrate: false,
  }
  state.sessionsById[sessionB.id] = { kind: 'full', session: sessionB, needsHydrate: false }
  state.currentRunIntentBySession[sessionA.id] = { ...runIntentA, session_id: sessionA.id, status: 'running' }

  desktopV3CacheReducer(state, {
    type: 'mutation.sessionArchiveResult',
    raw: {
      ok: true,
      archived: true,
      results: [{
        session_id: sessionA.id,
        archived: true,
        tombstone: {
          session_id: sessionA.id,
          kind: 'archived',
          archived: true,
          updated_at: 75,
        },
      }],
    },
  })

  assert.deepEqual(state.sessionOrderByScope['scope-a'], [sessionB.id])
  assert.deepEqual(state.sessionOrderByScope['scope-pinned'], [])
  assert.equal(state.currentRunIntentBySession[sessionA.id], undefined)

  const grouped = selectDesktopSidebarGroupedRows(state)
  assert.deepEqual(grouped.active_chats.map((row) => row.sessionId), [sessionB.id])
  assert.deepEqual(grouped.in_progress, [])
  assert.deepEqual(grouped.archived.map((row) => row.sessionId), [sessionA.id])

  const archivedRecord = desktopSessionRecordFromV3SidebarRow(grouped.archived[0])
  assert.equal(archivedRecord.title, 'Cached Session A')
  assert.equal(archivedRecord.workspacePath, '/workspace/cached')
  assert.equal(archivedRecord.workspaceName, 'cached-workspace')
  assert.equal(archivedRecord.metadata?.swarm_v3_sidebar_group, 'archived')
})

test('bulk archive mutation cleans several sessions from collections in one reducer pass', () => {
  const state = createEmptyDesktopV3CacheState()
  const sessionC = { ...sessionA, id: 'session-c', title: 'Session C' }
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'scope-a' }
  state.sessionOrderByScope['scope-a'] = [sessionA.id, sessionB.id, sessionC.id]
  state.sessionOrderByScope['scope-pinned'] = [sessionB.id, sessionC.id]
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.sessionsById[sessionB.id] = { kind: 'full', session: sessionB, needsHydrate: false }
  state.sessionsById[sessionC.id] = { kind: 'full', session: sessionC, needsHydrate: false }
  state.subscriptionsById = {
    'sub-a': { session_id: sessionA.id },
    'sub-b': { session_id: sessionB.id },
    'sub-c': { session_id: sessionC.id },
  }
  state.worksetsById['scope-a'] = {
    workset_id: 'scope-a',
    sessionIds: [sessionA.id, sessionB.id, sessionC.id],
    inactiveSessionIds: [],
  }
  state.liveRunsBySession[sessionA.id] = {}
  state.liveRunsBySession[sessionB.id] = {}

  desktopV3CacheReducer(state, {
    type: 'mutation.sessionArchiveResult',
    raw: {
      ok: true,
      archived: true,
      results: [sessionA, sessionB].map((session, index) => ({
        session_id: session.id,
        archived: true,
        tombstone: { session_id: session.id, kind: 'archived', archived: true, updated_at: 100 + index },
      })),
    },
  })

  assert.deepEqual(state.sessionOrderByScope['scope-a'], [sessionC.id])
  assert.deepEqual(state.sessionOrderByScope['scope-pinned'], [sessionC.id])
  assert.deepEqual(Object.keys(state.subscriptionsById), ['sub-c'])
  assert.deepEqual(state.worksetsById['scope-a'].sessionIds, [sessionC.id])
  assert.deepEqual(state.worksetsById['scope-a'].inactiveSessionIds, [sessionA.id, sessionB.id])
  assert.equal(state.liveRunsBySession[sessionA.id], undefined)
  assert.equal(state.liveRunsBySession[sessionB.id], undefined)
  assert.deepEqual(selectDesktopSidebarGroupedRows(state).archived.map((row) => row.sessionId).sort(), [sessionA.id, sessionB.id].sort())
})

test('archive mutation and realtime tombstone ordering is idempotent without repeated collection cleanup', () => {
  for (const realtimeFirst of [false, true]) {
    const state = createEmptyDesktopV3CacheState()
    state.sessionOrderByScope['scope-a'] = [sessionA.id, sessionB.id]
    state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
    state.sessionsById[sessionB.id] = { kind: 'full', session: sessionB, needsHydrate: false }
    state.subscriptionsById['sub-a'] = { session_id: sessionA.id }
    state.worksetsById['scope-a'] = { sessionIds: [sessionA.id, sessionB.id], inactiveSessionIds: [] }

    const mutation = () => desktopV3CacheReducer(state, {
      type: 'mutation.sessionArchiveResult',
      raw: {
        ok: true,
        archived: true,
        results: [{
          session_id: sessionA.id,
          archived: true,
          tombstone: { session_id: sessionA.id, kind: 'archived', archived: true, updated_at: 100 },
        }],
      },
    })
    const realtime = () => desktopV3CacheReducer(state, {
      type: 'realtime.applyEvent',
      event: {
        source: 'realtime',
        sessionId: sessionA.id,
        eventType: 'session.archived',
        payload: {
          tombstone: { session_id: sessionA.id, kind: 'archived', archived: true, updated_at: 200 },
        },
      },
    })

    if (realtimeFirst) realtime()
    else mutation()
    state.sessionOrderByScope['post-cleanup-marker'] = [sessionA.id]
    state.subscriptionsById['post-cleanup-marker'] = { session_id: sessionA.id }
    if (realtimeFirst) mutation()
    else realtime()

    assert.equal(state.tombstonesBySession[sessionA.id].updated_at, realtimeFirst ? 100 : 200)
    assert.deepEqual(state.sessionOrderByScope['post-cleanup-marker'], [sessionA.id])
    assert.equal(state.subscriptionsById['post-cleanup-marker']?.session_id, sessionA.id)
  }
})

test('unsubscribed V3 workset reactivation frame restores a missing sidebar session without local tombstone state', () => {
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'scope-a' }
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.sessionOrderByScope['scope-a'] = []
  state.worksetsById['scope-a'] = { sessionIds: [], inactiveSessionIds: [sessionA.id] }
  const reactivationCursor = 'reactivation-cursor'

  const actions = realtimeFrameToActions({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'workset.session.updated',
    workset_id: 'scope-a',
    session_id: sessionA.id,
    event_type: 'session.reactivated',
    endpoint_cursor: reactivationCursor,
    event: {
      id: 'reactivate-a',
      session_id: sessionA.id,
      seq: 6,
      event_type: 'session.reactivated',
      payload: { session: sessionA },
      ts_unix_ms: 300,
    },
  })
  for (const action of actions) desktopV3CacheReducer(state, action)

  assert.equal(actions[0].type, 'realtime.applyEvent')
  assert.equal(state.tombstonesBySession[sessionA.id], undefined)
  assert.equal(state.realtime.endpointCursor, reactivationCursor)
  assert.deepEqual(state.sessionOrderByScope['scope-a'], [sessionA.id])
  assert.deepEqual(state.worksetsById['scope-a'].sessionIds, [sessionA.id])
  assert.deepEqual(state.worksetsById['scope-a'].inactiveSessionIds, [])
})

test('metadata-only bootstrap after hydrate preserves messages', () => {
  const state = createEmptyDesktopV3CacheState()
  state.messagesBySession[sessionA.id] = {
    items: [messageA1],
    byMessageId: { [messageA1.id]: 0 },
    byGlobalSeq: { [`${sessionA.id}:${messageA1.global_seq}`]: 0 },
  }

  desktopV3CacheReducer(state, {
    type: 'snapshot.apply',
    source: 'bootstrap',
    scopeId: 'bootstrap-scope',
    snapshot: snapshotFixture({
      scope_id: 'bootstrap-scope',
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      session_order: [sessionA.id],
      messages_by_session: {},
    }),
  })

  assert.equal(state.messagesBySession[sessionA.id].items[0].id, messageA1.id)
})

test('clicking sidebar session only selects and leaves messages unchanged', () => {
  const state = createEmptyDesktopV3CacheState()
  state.messagesBySession[sessionA.id] = {
    items: [{ id: 'msg-a', session_id: sessionA.id, global_seq: 1, role: 'user', content: 'hello', created_at: 1 }],
    byMessageId: { 'msg-a': 0 },
    byGlobalSeq: { [`${sessionA.id}:1`]: 0 },
  }

  desktopV3CacheReducer(state, selectSession(sessionA.id))

  assert.equal(state.selectedSessionId, sessionA.id)
  assert.equal(state.messagesBySession[sessionA.id].items[0].content, 'hello')
})

test('click does not hydrate after initial load', () => {
  let state = createEmptyDesktopV3CacheState()
  state = desktopV3CacheReducer(state, hydrateResponseToAction(snapshotFixture({
    selector: { kind: 'session_ids', session_ids: [sessionA.id, sessionB.id] },
    sessions_by_id: { [sessionA.id]: sessionA, [sessionB.id]: sessionB },
    projections_by_session: { [sessionA.id]: projectionA, [sessionB.id]: projectionB },
    session_order: [sessionA.id, sessionB.id],
    messages_by_session: { [sessionA.id]: [messageA1], [sessionB.id]: [messageB1] },
  }), [sessionA.id, sessionB.id]))
  const beforeMessages = structuredClone(state.messagesBySession)

  state = desktopV3CacheReducer(state, selectSession(sessionA.id))

  assert.equal(state.selectedSessionId, sessionA.id)
  assert.deepEqual(state.messagesBySession, beforeMessages)
})
