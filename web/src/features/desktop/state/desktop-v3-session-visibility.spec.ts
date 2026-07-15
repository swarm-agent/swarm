import test from 'node:test'
import assert from 'node:assert/strict'

import { createEmptyDesktopV3CacheState, applyHydrateSnapshot, applyReconnectSnapshot, applySnapshot, applyWorksetSessionDiscovered, applyWorksetSessionUpdated } from './desktop-v3-cache-reducer'
import { selectDesktopSidebarRows } from './desktop-v3-cache-selectors'
import { hydrateSnapshotFixture, reconnectFixture, sessionA, sessionB, snapshotFixture } from './desktop-v3-cache.backend-fixtures'
import { isDesktopV3NavigationHiddenSession } from './desktop-v3-session-visibility'

const hiddenSession = {
  ...sessionA,
  id: 'system-sidechat',
  metadata: { lineage_kind: 'system_sidechat' },
}

test('navigation-hidden predicate recognizes every supported backend marker', () => {
  assert.equal(isDesktopV3NavigationHiddenSession({ ...sessionA, navigation_hidden: true }), true)
  assert.equal(isDesktopV3NavigationHiddenSession({ ...sessionA, system_session: true }), true)
  assert.equal(isDesktopV3NavigationHiddenSession({ ...sessionA, system_sidechat: true }), true)
  assert.equal(isDesktopV3NavigationHiddenSession({ ...sessionA, lineage_kind: 'system_sidechat' }), true)
  assert.equal(isDesktopV3NavigationHiddenSession(hiddenSession), true)
  assert.equal(isDesktopV3NavigationHiddenSession(sessionA), false)
})

test('bootstrap excludes hidden sidechats while ordinary and non-system child sessions remain visible', () => {
  const state = createEmptyDesktopV3CacheState()
  const childSession = { ...sessionB, id: 'ordinary-child', metadata: { lineage_kind: 'subagent' } }
  applySnapshot(state, snapshotFixture({
    sessions_by_id: { [sessionA.id]: sessionA, [childSession.id]: childSession, [hiddenSession.id]: hiddenSession },
    session_order: [hiddenSession.id, childSession.id, sessionA.id],
  }))

  assert.deepEqual(state.sessionOrderByScope['selector-hash:messages,run_intents'], [childSession.id, sessionA.id])
  assert.deepEqual(selectDesktopSidebarRows(state).map((row) => row.sessionId), [childSession.id, sessionA.id])
})

test('explicit hydrate retains hidden sidechat cache data without sidebar membership', () => {
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'sidebar' }
  state.sessionOrderByScope.sidebar = []
  applyHydrateSnapshot(state, hydrateSnapshotFixture({
    session_order: [hiddenSession.id],
    sessions_by_id: { [hiddenSession.id]: hiddenSession },
  }), [hiddenSession.id])

  assert.equal(state.sessionsById[hiddenSession.id]?.kind, 'full')
  assert.equal(state.sessionOrderByScope.sidebar.includes(hiddenSession.id), false)
})

test('reconnect and realtime discovered/updated remove hidden sessions from navigation membership', () => {
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'sidebar' }
  state.sessionOrderByScope.sidebar = [hiddenSession.id]
  applyReconnectSnapshot(state, reconnectFixture({
    workset_id: 'sidebar',
    session_order: [hiddenSession.id],
    sessions_by_id: { [hiddenSession.id]: hiddenSession },
  }))
  assert.equal(state.sessionOrderByScope.sidebar.includes(hiddenSession.id), false)

  state.sessionOrderByScope.sidebar = [hiddenSession.id]
  applyWorksetSessionDiscovered(state, {
    kind: 'workset.session.discovered',
    workset_id: 'sidebar',
    session_id: hiddenSession.id,
    session: hiddenSession,
    endpoint_cursor: 'cursor-discovered',
  })
  assert.equal(state.sessionOrderByScope.sidebar.includes(hiddenSession.id), false)

  state.sessionOrderByScope.sidebar = [hiddenSession.id]
  applyWorksetSessionUpdated(state, {
    kind: 'workset.session.updated',
    workset_id: 'sidebar',
    session_id: hiddenSession.id,
    session: hiddenSession,
    endpoint_cursor: 'cursor-2',
  })
  assert.equal(state.sessionOrderByScope.sidebar.includes(hiddenSession.id), false)
  assert.deepEqual(selectDesktopSidebarRows(state), [])
})

test('archived hidden sessions are excluded by the final sidebar selector', () => {
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'sidebar' }
  state.sessionOrderByScope.sidebar = []
  state.tombstonesBySession = {
    hidden: { session_id: hiddenSession.id, archived: true, session: hiddenSession },
    visible: { session_id: sessionA.id, archived: true, session: sessionA },
  }

  assert.deepEqual(selectDesktopSidebarRows(state).map((row) => row.sessionId), [sessionA.id])
})
