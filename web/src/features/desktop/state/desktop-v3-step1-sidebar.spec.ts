import test from 'node:test'
import assert from 'node:assert/strict'

import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from './desktop-v3-cache-reducer'
import { buildSidebarSessionTree, desktopSessionRecordFromV3SidebarRow, desktopSidebarWorkspacePathForSession } from '../layout/desktop-app-page'
import { selectDesktopSidebarGroupedRows, selectDesktopSidebarRows } from './desktop-v3-cache-selectors'
import { selectSession } from './desktop-v3-cache-wire'
import { hydrateResponseToAction } from './desktop-v3-cache-wire'
import { messageA1, messageB1, projectionA, projectionB, sessionA, sessionB, snapshotFixture } from './desktop-v3-cache.backend-fixtures'

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
      executionPolicy: { mode: 'automatic', shape: 'checkpointed' },
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
      executionPolicy: { mode: 'automatic', shape: 'checkpointed' },
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
