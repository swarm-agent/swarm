import test from 'node:test'
import assert from 'node:assert/strict'

import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from './desktop-v3-cache-reducer'
import { buildSidebarSessionTree, desktopSessionRecordFromV3SidebarRow, desktopSidebarWorkspacePathForSession } from '../layout/desktop-app-page'
import { selectDesktopSidebarRows } from './desktop-v3-cache-selectors'
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
  const record = desktopSessionRecordFromV3SidebarRow({
    sessionId: 'worktree-session',
    record: {
      kind: 'full',
      session: {
        ...sessionA,
        id: 'worktree-session',
        workspace_path: '/worktrees/swarm-go/agent/feature',
        workspace_name: 'swarm-go',
        worktree_enabled: true,
        worktree_root_path: '/worktrees/swarm-go/agent/feature',
        metadata: {
          swarm_v3_source_workspace_path: '/workspaces/swarm-go',
          local_workspace_binding_id: 'binding-source',
        },
      },
      needsHydrate: false,
    },
    projection: undefined,
    runIntents: {},
    currentRunIntent: undefined,
    pendingPermissions: [],
    pendingPermissionCount: 0,
  })
  const workspacePathByBinding = new Map([['binding-source', '/workspaces/swarm-go']])

  assert.equal(desktopSidebarWorkspacePathForSession(record, workspacePathByBinding), '/workspaces/swarm-go')
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
