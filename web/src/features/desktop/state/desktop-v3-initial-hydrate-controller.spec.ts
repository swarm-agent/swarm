import test from 'node:test'
import assert from 'node:assert/strict'

import { bootstrapDesktopV3Sidebar, resetDesktopV3BootstrapControllerForTests } from './desktop-v3-bootstrap-controller'
import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from './desktop-v3-cache-reducer'
import { hydrateDesktopV3InitialSessions, planDesktopV3SelectiveHydration, resetDesktopV3InitialHydrateControllerForTests } from './desktop-v3-initial-hydrate-controller'
import type { DesktopV3CacheAction, SessionSnapshot, V3SessionProjection } from './desktop-v3-cache-types'
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
import { createDesktopV3CacheOwner } from './desktop-v3-cache-owner'
import { buildPersistedDesktopV3MessageTailV1, DESKTOP_V3_CACHE_SCHEMA_VERSION, type PersistedDesktopV3MessageTailV1, type PersistedDesktopV3OwnerV1 } from './desktop-v3-cache-persisted-types'

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

const persistedOwner = createDesktopV3CacheOwner({
  origin: 'https://desktop.example.test',
  accountScopeId: 'acct-a',
  userId: 'user-a',
  surface: 'desktop',
})

function generatedSession(index: number, overrides: Partial<SessionSnapshot> = {}): SessionSnapshot {
  return {
    id: `session-${index}`,
    workspace_path: '/repo',
    workspace_name: 'repo',
    title: `Session ${index}`,
    mode: 'auto',
    created_at: index,
    updated_at: 1_000 + index,
    message_count: 1,
    last_message_at: 2_000 + index,
    ...overrides,
  }
}

function generatedProjection(sessionId: string, seq: number): V3SessionProjection {
  return {
    session_id: sessionId,
    last_event_seq: seq,
    projection_high_watermark_seq: seq,
    updated_at: 3_000 + seq,
  }
}

function generatedTail(
  session: SessionSnapshot,
  projection: V3SessionProjection,
  overrides: Partial<PersistedDesktopV3MessageTailV1> = {},
): PersistedDesktopV3MessageTailV1 {
  return buildPersistedDesktopV3MessageTailV1({
    ownerKey: persistedOwner.key,
    sessionId: session.id,
    persistedAt: 4_000,
    messages: session.message_count === 0
      ? []
      : [{ id: `msg-${session.id}`, session_id: session.id, global_seq: 1, role: 'user', content: `hello ${session.id}`, created_at: session.last_message_at }],
    sourceMessageCount: session.message_count,
    sourceLastMessageAt: session.last_message_at,
    sourceProjectionHighWatermarkSeq: projection.projection_high_watermark_seq,
    hydratedAt: 4_001,
    ...overrides,
  })
}

function ownerRecordForSessions(
  sessions: SessionSnapshot[],
  projections: Record<string, V3SessionProjection>,
  selectedSessionId = sessions[0]?.id,
): PersistedDesktopV3OwnerV1 {
  const scopeId = 'persisted-scope'
  return {
    schemaVersion: DESKTOP_V3_CACHE_SCHEMA_VERSION,
    ownerKey: persistedOwner.key,
    owner: persistedOwner,
    persistedAt: 3_000,
    selectedSessionId,
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
    sessionOrderByScope: { [scopeId]: sessions.map((session) => session.id) },
    sidebarSessionsById: Object.fromEntries(sessions.map((session) => [session.id, { session, projection: projections[session.id] }])),
  }
}

test('cold bootstrap selectively hydrates bootstrap session_order ids in bounded cold path', async () => {
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
        selector: { kind: 'session_ids', session_ids: body.session_ids },
        sessions_by_id: Object.fromEntries(body.session_ids.map((id) => [id, id === sessionA.id ? sessionA : sessionB])),
        projections_by_session: Object.fromEntries(body.session_ids.map((id) => [id, id === sessionA.id ? projectionA : projectionB])),
        session_order: body.session_ids,
        messages_by_session: Object.fromEntries(body.session_ids.map((id) => [id, id === sessionA.id ? [messageA1] : [messageB1]])),
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


test('selective planner reuses unchanged warm cache and hydrates fail-closed stale cases', () => {
  const sessions = Array.from({ length: 50 }, (_value, index) => generatedSession(index + 1))
  const projections = Object.fromEntries(sessions.map((session, index) => [session.id, generatedProjection(session.id, index + 1)]))
  const tails = Object.fromEntries(sessions.map((session) => [session.id, generatedTail(session, projections[session.id])]))
  const warm = planDesktopV3SelectiveHydration({
    bootstrapResponse: snapshotFixture({
      sessions_by_id: Object.fromEntries(sessions.map((session) => [session.id, session])),
      projections_by_session: projections,
      session_order: sessions.map((session) => session.id),
      messages_by_session: {},
    }),
    preBootstrapCachedProjections: projections,
    persistedTailsBySession: tails,
  })
  assert.equal(warm.hydrateSessionIds.length, 0)
  assert.deepEqual(warm.reusedSessionIds, sessions.map((session) => session.id))

  const changedProjection = { ...projections[sessions[12].id], projection_high_watermark_seq: 999 }
  assert.deepEqual(planDesktopV3SelectiveHydration({
    bootstrapResponse: snapshotFixture({
      sessions_by_id: Object.fromEntries(sessions.map((session) => [session.id, session])),
      projections_by_session: { ...projections, [sessions[12].id]: changedProjection },
      session_order: sessions.map((session) => session.id),
      messages_by_session: {},
    }),
    preBootstrapCachedProjections: projections,
    persistedTailsBySession: tails,
  }).hydrateSessionIds, [sessions[12].id])

  assert.deepEqual(planDesktopV3SelectiveHydration({
    bootstrapResponse: snapshotFixture({
      sessions_by_id: Object.fromEntries(sessions.map((session) => [session.id, session])),
      projections_by_session: projections,
      session_order: sessions.map((session) => session.id),
      messages_by_session: {},
    }),
    preBootstrapCachedProjections: projections,
    persistedTailsBySession: { ...tails, [sessions[2].id]: undefined },
  }).hydrateSessionIds, [sessions[2].id])

  const changedCount = { ...sessions[3], message_count: sessions[3].message_count + 1 }
  assert.deepEqual(planDesktopV3SelectiveHydration({
    bootstrapResponse: snapshotFixture({
      sessions_by_id: { [changedCount.id]: changedCount },
      projections_by_session: { [changedCount.id]: projections[changedCount.id] },
      session_order: [changedCount.id],
      messages_by_session: {},
    }),
    preBootstrapCachedProjections: projections,
    persistedTailsBySession: tails,
  }).hydrateSessionIds, [changedCount.id])

  const changedLastMessage = { ...sessions[4], last_message_at: sessions[4].last_message_at + 1 }
  assert.deepEqual(planDesktopV3SelectiveHydration({
    bootstrapResponse: snapshotFixture({
      sessions_by_id: { [changedLastMessage.id]: changedLastMessage },
      projections_by_session: { [changedLastMessage.id]: projections[changedLastMessage.id] },
      session_order: [changedLastMessage.id],
      messages_by_session: {},
    }),
    preBootstrapCachedProjections: projections,
    persistedTailsBySession: tails,
  }).hydrateSessionIds, [changedLastMessage.id])

  const staleTail = generatedTail(sessions[5], projections[sessions[5].id], {
    sourceProjectionHighWatermarkSeq: projections[sessions[5].id].projection_high_watermark_seq - 1,
  })
  assert.deepEqual(planDesktopV3SelectiveHydration({
    bootstrapResponse: snapshotFixture({
      sessions_by_id: { [sessions[5].id]: sessions[5] },
      projections_by_session: { [sessions[5].id]: projections[sessions[5].id] },
      session_order: [sessions[5].id],
      messages_by_session: {},
    }),
    preBootstrapCachedProjections: projections,
    persistedTailsBySession: { [sessions[5].id]: staleTail },
  }).hydrateSessionIds, [sessions[5].id])

  const missingMetadataTail = generatedTail(sessions[6], projections[sessions[6].id], { sourceMessageCount: undefined })
  assert.deepEqual(planDesktopV3SelectiveHydration({
    bootstrapResponse: snapshotFixture({
      sessions_by_id: { [sessions[6].id]: sessions[6] },
      projections_by_session: { [sessions[6].id]: projections[sessions[6].id] },
      session_order: [sessions[6].id],
      messages_by_session: {},
    }),
    preBootstrapCachedProjections: projections,
    persistedTailsBySession: { [sessions[6].id]: missingMetadataTail },
  }).hydrateSessionIds, [sessions[6].id])

  const emptySession = generatedSession(99, { id: 'session-empty', message_count: 0, last_message_at: 0 })
  const emptyProjection = generatedProjection(emptySession.id, 0)
  const emptyTail = generatedTail(emptySession, emptyProjection)
  assert.deepEqual(planDesktopV3SelectiveHydration({
    bootstrapResponse: snapshotFixture({
      sessions_by_id: { [emptySession.id]: emptySession },
      projections_by_session: { [emptySession.id]: emptyProjection },
      session_order: [emptySession.id],
      messages_by_session: {},
    }),
    preBootstrapCachedProjections: { [emptySession.id]: emptyProjection },
    persistedTailsBySession: { [emptySession.id]: emptyTail },
  }).hydrateSessionIds, [])
})

test('selective planner hydrates new/routed sessions and skips bootstrap tombstones', () => {
  const cachedSession = generatedSession(1)
  const cachedProjection = generatedProjection(cachedSession.id, 1)
  const newSession = generatedSession(2)
  const newProjection = generatedProjection(newSession.id, 2)
  const routedSession = generatedSession(100, { id: 'session-routed' })
  const routedProjection = generatedProjection(routedSession.id, 100)

  assert.deepEqual(planDesktopV3SelectiveHydration({
    bootstrapResponse: snapshotFixture({
      sessions_by_id: { [cachedSession.id]: cachedSession, [newSession.id]: newSession },
      projections_by_session: { [cachedSession.id]: cachedProjection, [newSession.id]: newProjection },
      session_order: [cachedSession.id, newSession.id],
      messages_by_session: {},
    }),
    preBootstrapCachedProjections: { [cachedSession.id]: cachedProjection },
    persistedTailsBySession: { [cachedSession.id]: generatedTail(cachedSession, cachedProjection) },
  }).hydrateSessionIds, [newSession.id])

  assert.deepEqual(planDesktopV3SelectiveHydration({
    bootstrapResponse: snapshotFixture({
      sessions_by_id: {},
      projections_by_session: {},
      tombstones_by_session: { [cachedSession.id]: { session_id: cachedSession.id, deleted: true } },
      session_order: [cachedSession.id],
      messages_by_session: {},
    }),
    preBootstrapCachedProjections: { [cachedSession.id]: cachedProjection },
    persistedTailsBySession: { [cachedSession.id]: generatedTail(cachedSession, cachedProjection) },
  }).hydrateSessionIds, [])

  assert.deepEqual(planDesktopV3SelectiveHydration({
    bootstrapResponse: snapshotFixture({
      sessions_by_id: { [cachedSession.id]: cachedSession },
      projections_by_session: { [cachedSession.id]: cachedProjection },
      session_order: [cachedSession.id],
      messages_by_session: {},
    }),
    preBootstrapCachedProjections: { [cachedSession.id]: cachedProjection, [routedSession.id]: routedProjection },
    persistedTailsBySession: {
      [cachedSession.id]: generatedTail(cachedSession, cachedProjection),
      [routedSession.id]: generatedTail(routedSession, routedProjection),
    },
    preferredSessionId: routedSession.id,
  }).hydrateSessionIds, [routedSession.id])
})

test('warm cache restores 50 persisted tails and sends zero hydrate calls', async () => {
  resetDesktopV3BootstrapControllerForTests()
  let state = createEmptyDesktopV3CacheState()
  const sessions = Array.from({ length: 50 }, (_value, index) => generatedSession(index + 1))
  const projections = Object.fromEntries(sessions.map((session, index) => [session.id, generatedProjection(session.id, index + 1)]))
  const tails = sessions.map((session) => generatedTail(session, projections[session.id]))
  const selectedTail = tails[0]
  let hydrateCalls = 0

  await bootstrapDesktopV3Sidebar({
    loadActiveOwnerKey: () => persistedOwner.key,
    readOwner: async () => ownerRecordForSessions(sessions, projections),
    readMessageTail: async () => selectedTail,
    readMessageTails: async (_ownerKey, sessionIds) => tails.filter((tail) => sessionIds.includes(tail.sessionId)),
    postBootstrap: async () => snapshotFixture({
      sessions_by_id: Object.fromEntries(sessions.map((session) => [session.id, session])),
      projections_by_session: projections,
      session_order: sessions.map((session) => session.id),
      messages_by_session: {},
    }),
    postHydrate: async () => {
      hydrateCalls += 1
      throw new Error('warm cache must not hydrate')
    },
    dispatch: (action) => {
      state = desktopV3CacheReducer(state, action)
    },
    getSnapshot: () => state,
  })

  assert.equal(hydrateCalls, 0)
  assert.equal(state.messagesBySession[sessions[0].id].source, 'persisted')
  assert.equal(state.messagesBySession[sessions[49].id].source, 'persisted')
  assert.equal(Object.keys(state.messagesBySession).length, 50)
  assert.equal(state.sessionsById[sessions[25].id]?.needsHydrate, false)
})

test('selected stale session is hydrated before bounded concurrent background batches', async () => {
  resetDesktopV3InitialHydrateControllerForTests()
  const sessions = Array.from({ length: 23 }, (_value, index) => generatedSession(index + 1))
  const projections = Object.fromEntries(sessions.map((session, index) => [session.id, generatedProjection(session.id, index + 1)]))
  const staleSelectedTail = generatedTail(sessions[0], projections[sessions[0].id], { sourceLastMessageAt: 1 })
  const staleBackgroundTails = sessions.slice(1).map((session) => generatedTail(session, projections[session.id], { sourceMessageCount: 0 }))
  const hydrateBodies: DesktopV3HydrateInput[] = []
  let activeHydrates = 0
  let maxActiveHydrates = 0
  let state = createEmptyDesktopV3CacheState()
  state.sessionOrderByScope['scope'] = sessions.map((session) => session.id)
  for (const session of sessions) {
    state.sessionsById[session.id] = { kind: 'full', session, needsHydrate: true }
  }

  await hydrateDesktopV3InitialSessions({
    sessionIds: sessions.map((session) => session.id),
    bootstrapResponse: snapshotFixture({
      sessions_by_id: Object.fromEntries(sessions.map((session) => [session.id, session])),
      projections_by_session: projections,
      session_order: sessions.map((session) => session.id),
      messages_by_session: {},
    }),
    preBootstrapCachedProjections: projections,
    selectedMessageTail: staleSelectedTail,
    preferredSessionId: sessions[0].id,
    ownerKey: persistedOwner.key,
    readMessageTails: async () => staleBackgroundTails,
    postHydrate: async (body) => {
      hydrateBodies.push(body)
      activeHydrates += 1
      maxActiveHydrates = Math.max(maxActiveHydrates, activeHydrates)
      await new Promise((resolve) => setTimeout(resolve, 0))
      activeHydrates -= 1
      return hydrateSnapshotFixture({
        selector: { kind: 'session_ids', session_ids: body.session_ids },
        sessions_by_id: Object.fromEntries(body.session_ids.map((id) => [id, sessions.find((session) => session.id === id)!])),
        projections_by_session: Object.fromEntries(body.session_ids.map((id) => [id, projections[id]])),
        session_order: body.session_ids,
        messages_by_session: Object.fromEntries(body.session_ids.map((id) => [id, [{ id: `net-${id}`, session_id: id, global_seq: 1, role: 'user', content: 'network', created_at: 1 }]])),
      })
    },
    dispatch: (action) => {
      state = desktopV3CacheReducer(state, action)
    },
  })

  assert.deepEqual(hydrateBodies[0].session_ids, [sessions[0].id])
  assert.equal(hydrateBodies.slice(1).every((body) => body.session_ids.length <= 10), true)
  assert.equal(maxActiveHydrates <= 2, true)
})

test('hydrate failure keeps cached transcript rendered and needsHydrate true while sibling batch applies', async () => {
  resetDesktopV3InitialHydrateControllerForTests()
  let state = createEmptyDesktopV3CacheState()
  const sessions = Array.from({ length: 12 }, (_value, index) => generatedSession(index + 1))
  const projections = Object.fromEntries(sessions.map((session, index) => [session.id, generatedProjection(session.id, index + 1)]))
  state.sessionOrderByScope['scope'] = sessions.map((session) => session.id)
  for (const session of sessions) state.sessionsById[session.id] = { kind: 'full', session, needsHydrate: true }
  state.messagesBySession[sessions[0].id] = {
    items: [{ id: 'cached', session_id: sessions[0].id, global_seq: 1, role: 'user', content: 'cached', created_at: 1 }],
    byMessageId: { cached: 0 },
    byGlobalSeq: { [`${sessions[0].id}:1`]: 0 },
    source: 'persisted',
  }

  await hydrateDesktopV3InitialSessions({
    sessionIds: sessions.map((session) => session.id),
    bootstrapResponse: snapshotFixture({
      sessions_by_id: Object.fromEntries(sessions.map((session) => [session.id, session])),
      projections_by_session: projections,
      session_order: sessions.map((session) => session.id),
      messages_by_session: {},
    }),
    preBootstrapCachedProjections: projections,
    postHydrate: async (body) => {
      if (body.session_ids.includes(sessions[0].id)) throw new Error('hydrate failed')
      return hydrateSnapshotFixture({
        selector: { kind: 'session_ids', session_ids: body.session_ids },
        sessions_by_id: Object.fromEntries(body.session_ids.map((id) => [id, sessions.find((session) => session.id === id)!])),
        projections_by_session: Object.fromEntries(body.session_ids.map((id) => [id, projections[id]])),
        session_order: body.session_ids,
        messages_by_session: Object.fromEntries(body.session_ids.map((id) => [id, [{ id: `net-${id}`, session_id: id, global_seq: 1, role: 'user', content: 'network', created_at: 1 }]])),
      })
    },
    dispatch: (action) => {
      state = desktopV3CacheReducer(state, action)
    },
  })

  assert.equal(state.desktopInitialHydrate.status, 'error')
  assert.equal(state.messagesBySession[sessions[0].id].items[0].content, 'cached')
  assert.equal(state.sessionsById[sessions[0].id]?.needsHydrate, true)
  assert.equal(state.messagesBySession[sessions[11].id].items[0].content, 'network')
  assert.equal(state.sessionsById[sessions[11].id]?.needsHydrate, false)
})
