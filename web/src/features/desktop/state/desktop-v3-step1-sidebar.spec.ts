import test from 'node:test'
import assert from 'node:assert/strict'

import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from './desktop-v3-cache-reducer'
import { buildSidebarSessionTree, desktopSessionRecordFromV3SidebarRow } from '../layout/desktop-app-page'
import { selectDesktopSidebarRows } from './desktop-v3-cache-selectors'
import { selectSession } from './desktop-v3-cache-wire'
import { sessionA, sessionB } from './desktop-v3-cache.backend-fixtures'

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
