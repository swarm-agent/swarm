import test from 'node:test'
import assert from 'node:assert/strict'

import {
  hydrateDesktopV3InitialSessions,
  planDesktopV3Hydration,
  resetDesktopV3InitialHydrateControllerForTests,
} from './desktop-v3-initial-hydrate-controller'
import { createEmptyDesktopV3CacheState, desktopV3CacheReducer, buildMessageListCache } from './desktop-v3-cache-reducer'
import type { DesktopV3CacheAction } from './desktop-v3-cache-types'
import { hydrateSnapshotFixture, messageA1, messageB1, projectionA, projectionB, sessionA, sessionB, snapshotFixture } from './desktop-v3-cache.backend-fixtures'

test('selective hydration reuses only sessions with a complete memory tail', () => {
  const state = createEmptyDesktopV3CacheState()
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.sessionsById[sessionB.id] = { kind: 'full', session: sessionB, needsHydrate: true }
  state.messagesBySession[sessionA.id] = buildMessageListCache([messageA1], {
    sourceMessageCount: sessionA.message_count,
    sourceLastMessageAt: sessionA.last_message_at,
  })
  state.messagesBySession[sessionB.id] = buildMessageListCache([messageB1], {
    sourceMessageCount: sessionB.message_count,
    sourceLastMessageAt: sessionB.last_message_at - 1,
  })

  assert.deepEqual(planDesktopV3Hydration(state, [sessionA.id, sessionB.id]), {
    reusedSessionIds: [sessionA.id],
    hydrateSessionIds: [sessionB.id],
  })

  state.tombstonesBySession[sessionA.id] = { session_id: sessionA.id, deleted: true }
  assert.deepEqual(planDesktopV3Hydration(state, [sessionA.id]), {
    reusedSessionIds: [],
    hydrateSessionIds: [sessionA.id],
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
      requests.push(input.session_ids ?? [])
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
    getSnapshot: () => state,
  })

  assert.deepEqual(requests, [[sessionA.id]])
  assert.equal(state.messagesBySession[sessionA.id].items[0].id, messageA1.id)
  assert.equal(state.desktopInitialHydrate.status, 'ready')
})

test('initial hydrate requests only the preferred session and does not batch hydrate sidebar sessions', async () => {
  resetDesktopV3InitialHydrateControllerForTests()
  let state = createEmptyDesktopV3CacheState()
  const requests: string[][] = []

  await hydrateDesktopV3InitialSessions({
    sessionIds: [sessionA.id, sessionB.id],
    preferredSessionId: sessionA.id,
    bootstrapResponse: snapshotFixture({
      sessions_by_id: { [sessionA.id]: sessionA, [sessionB.id]: sessionB },
      projections_by_session: { [sessionA.id]: projectionA, [sessionB.id]: projectionB },
      session_order: [sessionA.id, sessionB.id],
      messages_by_session: {},
    }),
    preBootstrapCachedProjections: {},
    postHydrate: async (input) => {
      assert.deepEqual(input.session_ids, [sessionA.id])
      assert.equal(input.history.mode, 'tail')
      assert.equal(input.resources.messages, true)
      const sessionIds = input.session_ids ?? []
      requests.push(sessionIds)
      return hydrateSnapshotFixture({
        selector: { kind: 'session_ids', session_ids: sessionIds },
        sessions_by_id: { [sessionA.id]: sessionA },
        projections_by_session: { [sessionA.id]: projectionA },
        session_order: sessionIds,
        messages_by_session: { [sessionA.id]: [messageA1] },
      })
    },
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    getSnapshot: () => state,
  })

  assert.deepEqual(requests, [[sessionA.id]])
  assert.equal(state.messagesBySession[sessionA.id].items[0].id, messageA1.id)
  assert.equal(state.messagesBySession[sessionB.id], undefined)
  assert.equal(state.desktopInitialHydrate.status, 'ready')
})
