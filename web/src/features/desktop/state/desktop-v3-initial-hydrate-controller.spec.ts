import test from 'node:test'
import assert from 'node:assert/strict'

import { bootstrapDesktopV3Sidebar, resetDesktopV3BootstrapControllerForTests } from './desktop-v3-bootstrap-controller'
import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from './desktop-v3-cache-reducer'
import { hydrateDesktopV3InitialSessions, resetDesktopV3InitialHydrateControllerForTests } from './desktop-v3-initial-hydrate-controller'
import type { DesktopV3CacheAction } from './desktop-v3-cache-types'
import type { DesktopV3HydrateInput } from './desktop-v3-sync-api'
import { hydrateResponseToAction } from './desktop-v3-cache-wire'
import {
  hydrateSnapshotFixture,
  messageA1,
  messageB1,
  projectionA,
  projectionB,
  sessionA,
  sessionB,
  snapshotFixture,
} from './desktop-v3-cache.backend-fixtures'

const expectedInitialHydrateBody = {
  surface: 'desktop',
  session_ids: [sessionB.id, sessionA.id],
  history: {
    mode: 'tail',
    max_messages_per_session: 200,
  },
  resources: {
    messages: true,
    events: false,
    run_intents: true,
    active_plan: true,
    plan_revisions: false,
  },
  include_active: true,
}

test('initial bulk hydrate posts all bootstrap session_order ids', async () => {
  resetDesktopV3BootstrapControllerForTests()
  let bootstrapCalls = 0
  const hydrateBodies: DesktopV3HydrateInput[] = []

  await bootstrapDesktopV3Sidebar({
    postBootstrap: async () => {
      bootstrapCalls += 1
      return snapshotFixture({
        scope_id: 'bootstrap-scope',
        snapshot_endpoint_cursor: 'v3c1.bootstrap',
        session_order: [sessionB.id, sessionA.id],
        messages_by_session: {},
      })
    },
    postHydrate: async (body) => {
      hydrateBodies.push(body)
      return hydrateSnapshotFixture({
        scope_id: 'hydrate-scope',
        snapshot_endpoint_cursor: 'v3c1.hydrate',
        selector: { kind: 'session_ids', session_ids: [sessionB.id, sessionA.id] },
        sessions_by_id: { [sessionA.id]: sessionA, [sessionB.id]: sessionB },
        projections_by_session: { [sessionA.id]: projectionA, [sessionB.id]: projectionB },
        session_order: [sessionB.id, sessionA.id],
        messages_by_session: { [sessionA.id]: [messageA1], [sessionB.id]: [messageB1] },
      })
    },
    dispatch: () => {},
  })

  assert.equal(bootstrapCalls, 1)
  assert.equal(hydrateBodies.length, 1)
  assert.deepEqual(hydrateBodies[0], expectedInitialHydrateBody)
})

test('hydrate response populates message cache for all initial sessions', async () => {
  resetDesktopV3InitialHydrateControllerForTests()
  let state = createEmptyDesktopV3CacheState()

  await hydrateDesktopV3InitialSessions({
    sessionIds: [sessionB.id, sessionA.id],
    postHydrate: async () => hydrateSnapshotFixture({
      scope_id: 'hydrate-scope',
      snapshot_endpoint_cursor: 'v3c1.hydrate',
      selector: { kind: 'session_ids', session_ids: [sessionB.id, sessionA.id] },
      sessions_by_id: { [sessionA.id]: sessionA, [sessionB.id]: sessionB },
      projections_by_session: { [sessionA.id]: projectionA, [sessionB.id]: projectionB },
      session_order: [sessionB.id, sessionA.id],
      messages_by_session: { [sessionA.id]: [messageA1], [sessionB.id]: [messageB1] },
    }),
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
  })

  assert.equal(state.messagesBySession[sessionA.id].items[0].id, messageA1.id)
  assert.equal(state.messagesBySession[sessionB.id].items[0].id, messageB1.id)
  assert.equal(state.desktopInitialHydrate.status, 'ready')
  assert.deepEqual(new Set(state.desktopInitialHydrate.hydratedSessionIds), new Set([sessionA.id, sessionB.id]))
  assert.equal(state.syncScopesById['hydrate-scope'].endpointCursor, 'v3c1.hydrate')
})

test('hydrate failure preserves existing cache', async () => {
  resetDesktopV3InitialHydrateControllerForTests()
  let state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'scope-a' }
  state.sessionOrderByScope['scope-a'] = [sessionA.id]
  state.messagesBySession[sessionA.id] = {
    items: [messageA1],
    byMessageId: { [messageA1.id]: 0 },
    byGlobalSeq: { [`${sessionA.id}:${messageA1.global_seq}`]: 0 },
  }
  state.selectedSessionId = sessionA.id

  await hydrateDesktopV3InitialSessions({
    sessionIds: [sessionA.id],
    postHydrate: async () => {
      throw new Error('hydrate failed')
    },
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
  })

  assert.equal(state.desktopInitialHydrate.status, 'error')
  assert.deepEqual(state.sessionOrderByScope['scope-a'], [sessionA.id])
  assert.equal(state.messagesBySession[sessionA.id].items[0].id, messageA1.id)
  assert.equal(state.selectedSessionId, sessionA.id)
})

test('empty bootstrap session_order skips hydrate', async () => {
  resetDesktopV3BootstrapControllerForTests()
  let state = createEmptyDesktopV3CacheState()
  let hydrateCalls = 0

  await bootstrapDesktopV3Sidebar({
    postBootstrap: async () => snapshotFixture({
      scope_id: 'bootstrap-empty',
      session_order: [],
      sessions_by_id: {},
      projections_by_session: {},
      messages_by_session: {},
    }),
    postHydrate: async () => {
      hydrateCalls += 1
      return hydrateSnapshotFixture()
    },
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
  })

  assert.equal(hydrateCalls, 0)
  assert.equal(state.desktopInitialHydrate.status, 'ready')
  assert.deepEqual(state.desktopInitialHydrate.hydratedSessionIds, [])
})

test('hydrate only mutates requested sessions', () => {
  let state = createEmptyDesktopV3CacheState()
  state.messagesBySession['session-c'] = {
    items: [{ id: 'msg-c', session_id: 'session-c', global_seq: 1, role: 'user', content: 'keep', created_at: 1 }],
    byMessageId: { 'msg-c': 0 },
    byGlobalSeq: { 'session-c:1': 0 },
  }

  state = desktopV3CacheReducer(state, hydrateResponseToAction(hydrateSnapshotFixture({
    selector: { kind: 'session_ids', session_ids: [sessionA.id, sessionB.id] },
    sessions_by_id: { [sessionA.id]: sessionA, [sessionB.id]: sessionB },
    projections_by_session: { [sessionA.id]: projectionA, [sessionB.id]: projectionB },
    session_order: [sessionA.id, sessionB.id],
    messages_by_session: { [sessionA.id]: [messageA1], [sessionB.id]: [messageB1] },
  }), [sessionA.id, sessionB.id]))

  assert.equal(state.messagesBySession['session-c'].items[0].content, 'keep')
})

test('hydrate response leak is rejected by fail-closed reducer', () => {
  const state = createEmptyDesktopV3CacheState()

  assert.throws(
    () => desktopV3CacheReducer(state, hydrateResponseToAction(hydrateSnapshotFixture({
      selector: { kind: 'session_ids', session_ids: [sessionA.id] },
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      session_order: [sessionA.id],
      messages_by_session: { [sessionB.id]: [messageB1] },
    }), [sessionA.id])),
    /hydrate messages_by_session includes non-requested session session-b/,
  )
  assert.equal(state.messagesBySession[sessionB.id], undefined)
})
