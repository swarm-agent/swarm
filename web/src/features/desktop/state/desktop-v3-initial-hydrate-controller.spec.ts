import test from 'node:test'
import assert from 'node:assert/strict'

import {
  hydrateDesktopV3InitialSessions,
  planDesktopV3SelectiveHydration,
  resetDesktopV3InitialHydrateControllerForTests,
} from './desktop-v3-initial-hydrate-controller'
import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from './desktop-v3-cache-reducer'
import type { DesktopV3CacheAction } from './desktop-v3-cache-types'
import { hydrateSnapshotFixture, messageA1, messageB1, projectionA, projectionB, sessionA, sessionB, snapshotFixture } from './desktop-v3-cache.backend-fixtures'

test('selective hydration plans backend hydrate from memory projections only', () => {
  const bootstrap = snapshotFixture({
    sessions_by_id: { [sessionA.id]: sessionA, [sessionB.id]: sessionB },
    projections_by_session: { [sessionA.id]: projectionA, [sessionB.id]: projectionB },
    session_order: [sessionA.id, sessionB.id],
    messages_by_session: {},
  })

  assert.deepEqual(planDesktopV3SelectiveHydration({
    bootstrapResponse: bootstrap,
    preBootstrapCachedProjections: {},
    preferredSessionId: sessionA.id,
    sessionIds: [sessionA.id, sessionB.id],
  }), {
    reusedSessionIds: [],
    hydrateSessionIds: [sessionA.id, sessionB.id],
  })

  assert.deepEqual(planDesktopV3SelectiveHydration({
    bootstrapResponse: bootstrap,
    preBootstrapCachedProjections: { [sessionA.id]: projectionA, [sessionB.id]: projectionB },
    preferredSessionId: sessionA.id,
    sessionIds: [sessionA.id, sessionB.id],
  }), {
    reusedSessionIds: [],
    hydrateSessionIds: [],
  })
})

test('initial hydrate requests selected session from backend and stores returned tail', async () => {
  resetDesktopV3InitialHydrateControllerForTests()
  let state = createEmptyDesktopV3CacheState()
  const requests: string[][] = []

  await hydrateDesktopV3InitialSessions({
    sessionIds: [sessionA.id],
    preferredSessionId: sessionA.id,
    bootstrapResponse: snapshotFixture({
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      session_order: [sessionA.id],
      messages_by_session: {},
    }),
    preBootstrapCachedProjections: {},
    postHydrate: async (input) => {
      requests.push(input.selector.session_ids ?? [])
      return hydrateSnapshotFixture({
        selector: { kind: 'session_ids', session_ids: [sessionA.id] },
        sessions_by_id: { [sessionA.id]: sessionA },
        projections_by_session: { [sessionA.id]: projectionA },
        session_order: [sessionA.id],
        messages_by_session: { [sessionA.id]: [messageA1] },
      })
    },
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
  })

  assert.deepEqual(requests, [[sessionA.id]])
  assert.equal(state.messagesBySession[sessionA.id].items[0].id, messageA1.id)
  assert.equal(state.desktopInitialHydrate.status, 'ready')
})

test('initial hydrate can hydrate all requested sidebar sessions from backend bounded tails', async () => {
  resetDesktopV3InitialHydrateControllerForTests()
  let state = createEmptyDesktopV3CacheState()
  const requests: string[][] = []

  await hydrateDesktopV3InitialSessions({
    sessionIds: [sessionA.id, sessionB.id],
    bootstrapResponse: snapshotFixture({
      sessions_by_id: { [sessionA.id]: sessionA, [sessionB.id]: sessionB },
      projections_by_session: { [sessionA.id]: projectionA, [sessionB.id]: projectionB },
      session_order: [sessionA.id, sessionB.id],
      messages_by_session: {},
    }),
    preBootstrapCachedProjections: {},
    postHydrate: async (input) => {
      const sessionIds = input.selector.session_ids ?? []
      requests.push(sessionIds)
      return hydrateSnapshotFixture({
        selector: { kind: 'session_ids', session_ids: sessionIds },
        sessions_by_id: Object.fromEntries(sessionIds.map((sessionId) => [sessionId, sessionId === sessionA.id ? sessionA : sessionB])),
        projections_by_session: Object.fromEntries(sessionIds.map((sessionId) => [sessionId, sessionId === sessionA.id ? projectionA : projectionB])),
        session_order: sessionIds,
        messages_by_session: Object.fromEntries(sessionIds.map((sessionId) => [sessionId, sessionId === sessionA.id ? [messageA1] : [messageB1]])),
      })
    },
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
  })

  assert.deepEqual(requests, [[sessionA.id, sessionB.id]])
  assert.equal(state.messagesBySession[sessionA.id].items[0].id, messageA1.id)
  assert.equal(state.messagesBySession[sessionB.id].items[0].id, messageB1.id)
  assert.equal(state.desktopInitialHydrate.status, 'ready')
})
