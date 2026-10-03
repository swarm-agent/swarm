import test from 'node:test'
import assert from 'node:assert/strict'
import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from './desktop-v3-cache-reducer'
import { hydrateResponseToAction } from './desktop-v3-cache-wire'
import { hydrateSnapshotFixture, projectionA, sessionA } from './desktop-v3-cache.backend-fixtures'

// Purpose: archive receipts are authoritative before realtime arrives. The cache
// reducer must reject stale hydration/reactivation rather than resurrecting an
// archived row, while allowing a genuinely newer restore. Reducer-level events
// are the narrowest deterministic proof of this ordering (not live transport).
test('versioned archive receipt survives stale hydrate and reactivation; newer restore wins', () => {
  const state = createEmptyDesktopV3CacheState()
  const projection = (seq: number) => ({ ...projectionA, last_event_seq: seq, projection_high_watermark_seq: seq })
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.projectionsBySession[sessionA.id] = projection(10)
  const receipt = { ok: true, results: [{ session_id: sessionA.id, archived: true, tombstone: { session_id: sessionA.id, archived: true, kind: 'archived', updated_at: 20 }, projection: projection(20) }] }
  desktopV3CacheReducer(state, { type: 'mutation.sessionArchiveResult', raw: receipt })
  assert.equal(state.tombstonesBySession[sessionA.id].updated_at, 20)
  assert.equal(state.tombstonesBySession[sessionA.id].session?.title, sessionA.title)
  desktopV3CacheReducer(state, hydrateResponseToAction(hydrateSnapshotFixture({
    sessions_by_id: { [sessionA.id]: sessionA }, projections_by_session: { [sessionA.id]: projection(10) }, messages_by_session: {},
    session_order: [sessionA.id], selector: { kind: 'session_ids', session_ids: [sessionA.id] },
  }), [sessionA.id]))
  assert.equal(state.tombstonesBySession[sessionA.id].archived, true)
  const reactivate = (seq: number) => desktopV3CacheReducer(state, { type: 'realtime.applyEvent', event: {
    sessionId: sessionA.id, eventType: 'session.reactivated', projection: projection(seq), payload: { session: sessionA },
  } })
  reactivate(15)
  assert.equal(state.tombstonesBySession[sessionA.id].archived, true)
  reactivate(21)
  assert.equal(state.tombstonesBySession[sessionA.id], undefined)
  desktopV3CacheReducer(state, { type: 'mutation.sessionArchiveResult', raw: receipt })
  assert.equal(state.tombstonesBySession[sessionA.id], undefined, 'late archive response must not overwrite newer restore')
})
