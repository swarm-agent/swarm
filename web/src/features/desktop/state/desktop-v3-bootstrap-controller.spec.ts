import test from 'node:test'
import assert from 'node:assert/strict'

import { bootstrapDesktopV3Sidebar, resetDesktopV3BootstrapControllerForTests } from './desktop-v3-bootstrap-controller'
import { createEmptyDesktopV3CacheState } from './desktop-v3-cache-reducer'
import type { DesktopV3CacheAction } from './desktop-v3-cache-types'
import { messageA1, projectionA, snapshotFixture, sessionA } from './desktop-v3-cache.backend-fixtures'
import { desktopV3CacheReducer } from './desktop-v3-cache-reducer'

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
