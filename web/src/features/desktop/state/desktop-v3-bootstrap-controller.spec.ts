import test from 'node:test'
import assert from 'node:assert/strict'

import { bootstrapDesktopV3Sidebar, resetDesktopV3BootstrapControllerForTests } from './desktop-v3-bootstrap-controller'
import { createEmptyDesktopV3CacheState } from './desktop-v3-cache-reducer'
import type { DesktopV3CacheAction } from './desktop-v3-cache-types'
import { messageA1, messageA2, messageB1, projectionA, projectionB, snapshotFixture, sessionA, sessionB } from './desktop-v3-cache.backend-fixtures'
import { createDesktopV3CacheOwner } from './desktop-v3-cache-owner'
import { buildPersistedDesktopV3MessageTailV1, DESKTOP_V3_CACHE_SCHEMA_VERSION, type PersistedDesktopV3OwnerV1 } from './desktop-v3-cache-persisted-types'
import { desktopV3CacheReducer } from './desktop-v3-cache-reducer'

const persistedOwner = createDesktopV3CacheOwner({
  origin: 'https://desktop.example.test',
  accountScopeId: 'acct-a',
  userId: 'user-a',
  surface: 'desktop',
})

function persistedOwnerRecordFixture(): PersistedDesktopV3OwnerV1 {
  const scopeId = 'persisted-scope'
  return {
    schemaVersion: DESKTOP_V3_CACHE_SCHEMA_VERSION,
    ownerKey: persistedOwner.key,
    owner: persistedOwner,
    persistedAt: 1_000,
    selectedSessionId: sessionA.id,
    sidebarScopeId: scopeId,
    syncScopesById: {
      [scopeId]: {
        scopeId,
        surface: 'desktop',
        streamKind: 'v3.sync.snapshot',
        selectorFilterHash: 'persisted-selector',
        resourceSet: 'messages,run_intents',
        selector: { kind: 'workspace', workspace_path: '/repo' },
        endpointCursor: 'cursor-persisted',
        replayPath: '/v3/sync/stream',
        replayTransport: 'http_post',
        needsBootstrap: false,
      },
    },
    sessionOrderByScope: { [scopeId]: [sessionA.id, sessionB.id] },
    sidebarSessionsById: {
      [sessionA.id]: { session: sessionA, projection: projectionA },
      [sessionB.id]: { session: sessionB, projection: projectionB },
    },
  }
}

test('bootstrap controller restores selected persisted owner and tail before network bootstrap', async () => {
  resetDesktopV3BootstrapControllerForTests()
  let state = createEmptyDesktopV3CacheState()
  const order: string[] = []
  const readTailSessionIds: string[] = []
  const persistedTail = buildPersistedDesktopV3MessageTailV1({
    ownerKey: persistedOwner.key,
    sessionId: sessionA.id,
    persistedAt: 2_000,
    messages: [messageA2, messageA1],
    sourceMessageCount: sessionA.message_count,
    sourceLastMessageAt: sessionA.last_message_at,
    sourceProjectionHighWatermarkSeq: projectionA.projection_high_watermark_seq,
    hydratedAt: 2_001,
  })

  await bootstrapDesktopV3Sidebar({
    loadActiveOwnerKey: () => {
      order.push('load-active-owner')
      return persistedOwner.key
    },
    readOwner: async () => {
      order.push('read-owner')
      return persistedOwnerRecordFixture()
    },
    readMessageTail: async (_ownerKey, sessionId) => {
      order.push('read-message-tail')
      readTailSessionIds.push(sessionId)
      return persistedTail
    },
    postBootstrap: async () => {
      order.push('post-bootstrap')
      assert.equal(state.desktopSidebarBootstrap.status, 'cached')
      assert.equal(state.desktopInitialHydrate.status, 'cached')
      assert.equal(state.messagesBySession[sessionA.id].source, 'persisted')
      assert.deepEqual(state.messagesBySession[sessionA.id].items.map((message) => message.id), [messageA1.id, messageA2.id])
      return snapshotFixture({
        scope_id: 'scope-network',
        sessions_by_id: {},
        projections_by_session: {},
        session_order: [],
        messages_by_session: {},
        run_intents_by_session: {},
      })
    },
    postHydrate: async () => {
      throw new Error('hydrate should not run for empty network session order')
    },
    dispatch: (action: DesktopV3CacheAction) => {
      order.push(`dispatch:${action.type}`)
      state = desktopV3CacheReducer(state, action)
    },
  })

  assert.deepEqual(readTailSessionIds, [sessionA.id])
  assert.equal(order.indexOf('dispatch:desktopV3Cache.restore') < order.indexOf('post-bootstrap'), true)
  assert.equal(state.desktopSidebarBootstrap.status, 'ready')
  assert.equal(state.desktopSidebarBootstrap.stale, false)
})

test('bootstrap controller restores route-preferred session tail before network bootstrap', async () => {
  resetDesktopV3BootstrapControllerForTests()
  let state = createEmptyDesktopV3CacheState()
  const readTailSessionIds: string[] = []
  const tailA = buildPersistedDesktopV3MessageTailV1({
    ownerKey: persistedOwner.key,
    sessionId: sessionA.id,
    persistedAt: 2_000,
    messages: [messageA1],
  })
  const tailB = buildPersistedDesktopV3MessageTailV1({
    ownerKey: persistedOwner.key,
    sessionId: sessionB.id,
    persistedAt: 2_001,
    messages: [messageB1],
  })

  await bootstrapDesktopV3Sidebar({
    preferredSessionId: sessionB.id,
    loadActiveOwnerKey: () => persistedOwner.key,
    readOwner: async () => persistedOwnerRecordFixture(),
    readMessageTail: async (_ownerKey, sessionId) => {
      readTailSessionIds.push(sessionId)
      return sessionId === sessionB.id ? tailB : tailA
    },
    postBootstrap: async () => {
      assert.equal(state.selectedSessionId, sessionB.id)
      assert.equal(state.messagesBySession[sessionA.id], undefined)
      assert.deepEqual(state.messagesBySession[sessionB.id].items.map((message) => message.id), [messageB1.id])
      return snapshotFixture({
        scope_id: 'scope-network',
        sessions_by_id: {},
        projections_by_session: {},
        session_order: [],
        messages_by_session: {},
        run_intents_by_session: {},
      })
    },
    postHydrate: async () => {
      throw new Error('hydrate should not run for empty network session order')
    },
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
  })

  assert.deepEqual(readTailSessionIds, [sessionB.id])
})

test('bootstrap controller restores route-preferred selection without falling back to persisted selected tail on route miss', async () => {
  resetDesktopV3BootstrapControllerForTests()
  let state = createEmptyDesktopV3CacheState()
  const readTailSessionIds: string[] = []

  await bootstrapDesktopV3Sidebar({
    preferredSessionId: sessionB.id,
    loadActiveOwnerKey: () => persistedOwner.key,
    readOwner: async () => persistedOwnerRecordFixture(),
    readMessageTail: async (_ownerKey, sessionId) => {
      readTailSessionIds.push(sessionId)
      return undefined
    },
    postBootstrap: async () => {
      assert.equal(state.selectedSessionId, sessionB.id)
      assert.equal(state.messagesBySession[sessionA.id], undefined)
      assert.equal(state.messagesBySession[sessionB.id], undefined)
      assert.equal(state.desktopInitialHydrate.status, 'idle')
      return snapshotFixture({
        scope_id: 'scope-network',
        sessions_by_id: {},
        projections_by_session: {},
        session_order: [],
        messages_by_session: {},
        run_intents_by_session: {},
      })
    },
    postHydrate: async () => {
      throw new Error('hydrate should not run for empty network session order')
    },
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
  })

  assert.deepEqual(readTailSessionIds, [sessionB.id])
})

test('bootstrap controller preserves restored cached state when network bootstrap fails', async () => {
  resetDesktopV3BootstrapControllerForTests()
  let state = createEmptyDesktopV3CacheState()
  const persistedTail = buildPersistedDesktopV3MessageTailV1({
    ownerKey: persistedOwner.key,
    sessionId: sessionA.id,
    persistedAt: 2_000,
    messages: [messageA1],
  })

  await bootstrapDesktopV3Sidebar({
    loadActiveOwnerKey: () => persistedOwner.key,
    readOwner: async () => persistedOwnerRecordFixture(),
    readMessageTail: async () => persistedTail,
    postBootstrap: async () => {
      throw new Error('offline')
    },
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
  })

  assert.equal(state.desktopSidebarBootstrap.status, 'error')
  assert.equal(state.desktopSidebarBootstrap.error, 'offline')
  assert.equal(state.desktopSidebarBootstrap.source, 'persisted')
  assert.equal(state.desktopSidebarBootstrap.stale, true)
  assert.equal(state.desktopInitialHydrate.status, 'cached')
  assert.equal(state.desktopInitialHydrate.source, 'persisted')
  assert.equal(state.desktopInitialHydrate.stale, true)
  assert.deepEqual(state.sessionOrderByScope['persisted-scope'], [sessionA.id, sessionB.id])
  assert.equal(state.selectedSessionId, sessionA.id)
  assert.deepEqual(state.messagesBySession[sessionA.id].items.map((message) => message.id), [messageA1.id])
})

test('bootstrap controller cold-misses to normal network path when persisted owner is unavailable', async () => {
  resetDesktopV3BootstrapControllerForTests()
  let state = createEmptyDesktopV3CacheState()
  const actions: DesktopV3CacheAction[] = []
  let postBootstrapCalled = false

  await bootstrapDesktopV3Sidebar({
    loadActiveOwnerKey: () => persistedOwner.key,
    readOwner: async () => undefined,
    readMessageTail: async () => {
      throw new Error('message tail should not be read without owner')
    },
    postBootstrap: async () => {
      postBootstrapCalled = true
      return snapshotFixture({
        scope_id: 'cold-scope',
        sessions_by_id: {},
        projections_by_session: {},
        session_order: [],
        messages_by_session: {},
        run_intents_by_session: {},
      })
    },
    postHydrate: async () => {
      throw new Error('hydrate should not run for empty network session order')
    },
    dispatch: (action: DesktopV3CacheAction) => {
      actions.push(action)
      state = desktopV3CacheReducer(state, action)
    },
  })

  assert.equal(postBootstrapCalled, true)
  assert.equal(actions.some((action) => action.type === 'desktopV3Cache.restore'), false)
  assert.equal(actions[0].type, 'desktopSidebarBootstrap.update')
  assert.equal(actions[0].type === 'desktopSidebarBootstrap.update' ? actions[0].patch.status : '', 'loading')
  assert.equal(state.desktopSidebarBootstrap.status, 'ready')
  assert.equal(state.desktopSidebarBootstrap.scopeId, 'cold-scope')
})

test('bootstrap controller dispatches bootstrapResponseToAction and stores scope id', async () => {
  resetDesktopV3BootstrapControllerForTests()
  let state = createEmptyDesktopV3CacheState()
  const response = snapshotFixture({
    scope_id: 'scope-a',
    snapshot_endpoint_cursor: 'v3c1.cursor-a',
    sessions_by_id: { [sessionA.id]: sessionA },
    projections_by_session: {
      [sessionA.id]: {
        session_id: sessionA.id,
        last_event_seq: 7,
        projection_high_watermark_seq: 7,
        updated_at: 12,
      },
    },
    session_order: [sessionA.id],
    messages_by_session: {},
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'scope-a',
      resource_set: 'run_intents',
    },
  })

  await bootstrapDesktopV3Sidebar({
    postBootstrap: async () => response,
    postHydrate: async () => snapshotFixture({
      scope_id: 'hydrate-scope-a',
      snapshot_endpoint_cursor: 'v3c1.hydrate-a',
      selector: { kind: 'session_ids', session_ids: [sessionA.id] },
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      session_order: [sessionA.id],
      messages_by_session: { [sessionA.id]: [messageA1] },
      run_intents_by_session: {},
      tombstones_by_session: {},
      plans_by_session: {},
      plan_revisions_by_session: {},
      permissions_by_session: {},
      usage_by_session: {},
      preferences_by_session: {},
      agent_model_policy_by_session: {},
      history_manifests_by_session: {},
    }),
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
  })

  assert.equal(state.desktopSidebarBootstrap.status, 'ready')
  assert.equal(state.desktopSidebarBootstrap.scopeId, 'scope-a')
  assert.equal(state.syncScopesById['scope-a'].endpointCursor, 'v3c1.cursor-a')
  assert.deepEqual(state.sessionOrderByScope['scope-a'], [sessionA.id])
  assert.equal(state.sessionsById[sessionA.id]?.kind, 'full')
  assert.equal(state.desktopInitialHydrate.status, 'ready')
  assert.equal(state.messagesBySession[sessionA.id].items[0].id, messageA1.id)
})

test('bootstrap controller coalesces concurrent in-flight bootstrap calls only', async () => {
  resetDesktopV3BootstrapControllerForTests()

  let calls = 0
  let resolveBootstrap!: (response: ReturnType<typeof snapshotFixture>) => void

  const pending = new Promise<ReturnType<typeof snapshotFixture>>((resolve) => {
    resolveBootstrap = resolve
  })

  const first = bootstrapDesktopV3Sidebar({
    postBootstrap: async () => {
      calls += 1
      return pending
    },
    postHydrate: async () => snapshotFixture({
      scope_id: 'hydrate-empty',
      selector: { kind: 'session_ids', session_ids: [] },
      sessions_by_id: {},
      projections_by_session: {},
      session_order: [],
      messages_by_session: {},
      run_intents_by_session: {},
    }),
    dispatch: () => {},
  })

  const second = bootstrapDesktopV3Sidebar({
    postBootstrap: async () => {
      calls += 1
      return snapshotFixture({ scope_id: 'scope-b', messages_by_session: {} })
    },
    postHydrate: async () => snapshotFixture({
      scope_id: 'hydrate-empty',
      selector: { kind: 'session_ids', session_ids: [] },
      sessions_by_id: {},
      projections_by_session: {},
      session_order: [],
      messages_by_session: {},
      run_intents_by_session: {},
    }),
    dispatch: () => {},
  })

  await Promise.resolve()
  assert.equal(calls, 1)

  resolveBootstrap(snapshotFixture({ scope_id: 'scope-a', messages_by_session: {} }))

  await Promise.all([first, second])

  await bootstrapDesktopV3Sidebar({
    postBootstrap: async () => {
      calls += 1
      return snapshotFixture({ scope_id: 'scope-c', messages_by_session: {} })
    },
    postHydrate: async () => snapshotFixture({
      scope_id: 'hydrate-empty',
      selector: { kind: 'session_ids', session_ids: [] },
      sessions_by_id: {},
      projections_by_session: {},
      session_order: [],
      messages_by_session: {},
      run_intents_by_session: {},
    }),
    dispatch: () => {},
  })

  assert.equal(calls, 2)
})

test('bootstrap error preserves existing cache', async () => {
  resetDesktopV3BootstrapControllerForTests()
  let state = createEmptyDesktopV3CacheState()
  state.sessionOrderByScope['scope-a'] = [sessionA.id]
  state.messagesBySession[sessionA.id] = {
    items: [{ id: 'msg-a', session_id: sessionA.id, global_seq: 1, role: 'user', content: 'hello', created_at: 1 }],
    byMessageId: { 'msg-a': 0 },
    byGlobalSeq: { [`${sessionA.id}:1`]: 0 },
  }

  await bootstrapDesktopV3Sidebar({
    postBootstrap: async () => {
      throw new Error('bootstrap failed')
    },
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
  })

  assert.equal(state.desktopSidebarBootstrap.status, 'error')
  assert.equal(state.desktopSidebarBootstrap.error, 'bootstrap failed')
  assert.deepEqual(state.sessionOrderByScope['scope-a'], [sessionA.id])
  assert.equal(state.messagesBySession[sessionA.id].items[0].content, 'hello')
})
