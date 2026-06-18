import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from './desktop-v3-cache-reducer'
import { buildSidebarSessionTree, desktopSessionRecordFromV3SidebarRow } from '../layout/desktop-app-page'
import { selectDesktopSidebarRows } from './desktop-v3-cache-selectors'
import { selectSession } from './desktop-v3-cache-wire'
import { hydrateResponseToAction } from './desktop-v3-cache-wire'
import { messageA1, messageB1, projectionA, projectionB, runIntentA, sessionA, sessionB, snapshotFixture } from './desktop-v3-cache.backend-fixtures'

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


test('Desktop V3 selected chat keeps a composer and uses canonical cache mutation path', async () => {
  const source = await readFile(new URL('../layout/desktop-app-page.tsx', import.meta.url), 'utf8')
  const paneSource = source.slice(source.indexOf('function DesktopV3ChatPane({'), source.indexOf('function DesktopV3CommittedMessage'))

  assert.match(paneSource, /data-testid="desktop-v3-chat-pane"/)
  assert.match(paneSource, /data-testid="desktop-v3-chat-composer"/)
  assert.match(paneSource, /data-testid="desktop-v3-chat-input"/)
  assert.match(paneSource, /postDesktopV3SessionMessage\(sessionId/)
  assert.match(paneSource, /type: 'pendingUser\.upsert'/)
  assert.match(paneSource, /messageMutationResponseToAction\(raw, clientRequestId, messageId\)/)
  assert.doesNotMatch(source, /DesktopChatPanel/)
  assert.doesNotMatch(source, /useDesktopUiStore|useDesktopStore|desktop-ui-store|desktop-state-store/)
})

test('Desktop V3 message API helper posts only to canonical Sessions V3 endpoint', async () => {
  const source = await readFile(new URL('./desktop-v3-sync-api.ts', import.meta.url), 'utf8')
  const helperSource = source.slice(source.indexOf('export async function postDesktopV3SessionMessage'))

  assert.match(helperSource, /`\/v3\/sessions\/\$\{encodeURIComponent\(normalizedSessionId\)\}\/messages`/)
  assert.match(helperSource, /client_request_id: input\.clientRequestId/)
  assert.match(helperSource, /role: input\.role/)
  assert.match(helperSource, /content: input\.content/)
  assert.doesNotMatch(helperSource, /\/v1\/sessions|\/v2\/sessions|target_swarm_id|session_id:/)
})

test('Desktop V3 mutation action resolves pending user through reducer cache path', () => {
  const state = createEmptyDesktopV3CacheState()
  const clientRequestId = `desktop-v3-message:${sessionA.id}:test`
  const messageId = `pending:${clientRequestId}`

  let next = desktopV3CacheReducer(state, {
    type: 'pendingUser.upsert',
    input: {
      sessionId: sessionA.id,
      clientRequestId,
      messageId,
      content: 'hello from restored composer',
      createdAt: 10,
    },
  })

  assert.equal(next.pendingUserByClientRequestId[clientRequestId]?.content, 'hello from restored composer')

  next = desktopV3CacheReducer(next, {
    type: 'mutation.messageResult',
    clientRequestId,
    messageId,
    raw: {
      ok: true,
      session_id: sessionA.id,
      message: { ...messageA1, id: 'committed-from-send', content: 'hello from restored composer', global_seq: 5 },
      run_intent: runIntentA,
      mutation: {},
      realtime_outbox: null,
    },
  })

  assert.equal(next.pendingUserByClientRequestId[clientRequestId], undefined)
  assert.equal(next.messagesBySession[sessionA.id].items.at(-1)?.id, 'committed-from-send')
  assert.equal(next.runIntentsBySession[sessionA.id]?.[runIntentA.run_id]?.status, runIntentA.status)
})
