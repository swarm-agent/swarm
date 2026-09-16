import test from 'node:test'
import assert from 'node:assert/strict'

import { createEmptyDesktopV3CacheState, applyHydrateSnapshot, applyReconnectSnapshot, applySnapshot, applyWorksetSessionDiscovered, applyWorksetSessionUpdated } from './desktop-v3-cache-reducer'
import { selectDesktopSidebarRows, selectDesktopVideoStudioRows } from './desktop-v3-cache-selectors'
import { hydrateSnapshotFixture, reconnectFixture, sessionA, sessionB, snapshotFixture } from './desktop-v3-cache.backend-fixtures'
import { isDesktopV3NavigationHiddenSession, isDesktopV3VideoStudioMetadata, isDesktopV3VideoStudioSession } from './desktop-v3-session-visibility'

const hiddenSession = {
  ...sessionA,
  id: 'system-sidechat',
  metadata: { lineage_kind: 'system_sidechat' },
}

const automationExecutionSession = {
  ...sessionB,
  id: 'automation-occurrence-run',
  metadata: {
    automation_v2_occurrence_id: 'occ-12345678',
    swarm_v3_session_purpose: 'automation_execution',
    swarm_v3_purpose_workspace_id: 'workspace-a',
    navigation_hidden: true,
  },
}

test('navigation-hidden predicate recognizes every supported backend marker', () => {
  assert.equal(isDesktopV3NavigationHiddenSession({ ...sessionA, navigation_hidden: true }), true)
  assert.equal(isDesktopV3NavigationHiddenSession({ ...sessionA, system_session: true }), true)
  assert.equal(isDesktopV3NavigationHiddenSession({ ...sessionA, system_sidechat: true }), true)
  assert.equal(isDesktopV3NavigationHiddenSession({ ...sessionA, lineage_kind: 'system_sidechat' }), true)
  assert.equal(isDesktopV3NavigationHiddenSession(hiddenSession), true)
  assert.equal(isDesktopV3NavigationHiddenSession(automationExecutionSession), true)
  assert.equal(isDesktopV3NavigationHiddenSession({ ...sessionA, metadata: { automation_v2_occurrence_id: 'occ-123' } }), true)
  assert.equal(isDesktopV3NavigationHiddenSession({ ...sessionA, metadata: { swarm_v3_session_purpose: 'automation_execution', swarm_v3_purpose_workspace_id: 'ws' } }), true)
  assert.equal(isDesktopV3NavigationHiddenSession({ ...sessionA, metadata: { swarm_v3_session_purpose: 'automation_management', swarm_v3_purpose_workspace_id: 'ws' } }), true)
  assert.equal(isDesktopV3NavigationHiddenSession({ ...sessionA, metadata: { automation_v2_parent_id: 'parent-123' } }), true)
  assert.equal(isDesktopV3NavigationHiddenSession({ ...sessionA, metadata: { automation_v2_optimization: true } }), true)
  assert.equal(isDesktopV3NavigationHiddenSession({ ...sessionA, metadata: { automation_review_id: 'review-123' } }), true)
  assert.equal(isDesktopV3NavigationHiddenSession({ ...sessionA, metadata: { automation_v2_occurrence_id: '' } }), false)
  assert.equal(isDesktopV3NavigationHiddenSession(sessionA), false)
})

test('Video Studio classification recognizes dedicated-tool and AI-created project contracts', () => {
  const videoSession = { ...sessionA, metadata: { experience: ' VIDEO_STUDIO ', launch_source: 'VIDEO_TOOL' } }
  assert.equal(isDesktopV3VideoStudioSession(videoSession), true)
  assert.equal(isDesktopV3VideoStudioSession({ ...videoSession, metadata: { experience: 'video_studio' } }), false)
  assert.equal(isDesktopV3VideoStudioSession({ ...videoSession, metadata: { launch_source: 'video_tool' } }), false)
  assert.equal(isDesktopV3VideoStudioSession({ ...videoSession, metadata: { lineage_kind: ' VIDEO_PROJECT ' } }), true)
  assert.equal(isDesktopV3VideoStudioMetadata({ lineage_kind: 'video_child' }), false)
})

test('ordinary and Video selectors partition canonical V3 sessions without dropping hydration authority', () => {
  const state = createEmptyDesktopV3CacheState()
  const videoSession = {
    ...sessionB,
    id: 'video-studio-session',
    metadata: { experience: 'video_studio', launch_source: 'video_tool' },
  }
  const snapshot = snapshotFixture({
    sessions_by_id: { [sessionA.id]: sessionA, [videoSession.id]: videoSession },
    projections_by_session: {},
    messages_by_session: {},
    run_intents_by_session: {},
    session_order: [videoSession.id, sessionA.id],
  })
  applySnapshot(state, { source: 'bootstrap', scopeId: snapshot.scope_id, snapshot })

  assert.deepEqual(selectDesktopSidebarRows(state).map((row) => row.sessionId), [sessionA.id])
  assert.deepEqual(selectDesktopVideoStudioRows(state).map((row) => row.sessionId), [videoSession.id])
  assert.equal(state.sessionsById[videoSession.id]?.kind, 'full')
})

test('bootstrap excludes hidden sidechats while ordinary and non-system child sessions remain visible', () => {
  const state = createEmptyDesktopV3CacheState()
  const childSession = { ...sessionB, id: 'ordinary-child', metadata: { lineage_kind: 'subagent' } }
  const snapshot = snapshotFixture({
    sessions_by_id: { [sessionA.id]: sessionA, [childSession.id]: childSession, [hiddenSession.id]: hiddenSession },
    projections_by_session: {},
    messages_by_session: {},
    run_intents_by_session: {},
    session_order: [hiddenSession.id, childSession.id, sessionA.id],
  })
  applySnapshot(state, { source: 'bootstrap', scopeId: snapshot.scope_id, snapshot })

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
    projections_by_session: {},
    messages_by_session: {},
    selector: { kind: 'session_ids', session_ids: [hiddenSession.id] },
  }), [hiddenSession.id])

  assert.equal(state.sessionsById[hiddenSession.id]?.kind, 'full')
  assert.deepEqual(selectDesktopSidebarRows(state, 'sidebar'), [])
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
    hidden: { kind: 'archived', session_id: hiddenSession.id, archived: true, session: hiddenSession },
    visible: { kind: 'archived', session_id: sessionA.id, archived: true, session: sessionA },
  }

  assert.deepEqual(selectDesktopSidebarRows(state, 'sidebar').map((row) => row.sessionId), [sessionA.id])
})

test('bootstrap, hydrate, and realtime exclude automation occurrence execution sessions from sidebar while keeping them hydrated by ID', () => {
  const state = createEmptyDesktopV3CacheState()
  const snapshot = snapshotFixture({
    sessions_by_id: {
      [sessionA.id]: sessionA,
      [automationExecutionSession.id]: automationExecutionSession,
    },
    projections_by_session: {},
    messages_by_session: {},
    run_intents_by_session: {},
    session_order: [automationExecutionSession.id, sessionA.id],
  })
  applySnapshot(state, { source: 'bootstrap', scopeId: snapshot.scope_id, snapshot })

  // Sidebar must only include sessionA, never the automation execution session
  assert.deepEqual(selectDesktopSidebarRows(state).map((row) => row.sessionId), [sessionA.id])
  // Execution session must remain fully durable and hydrated in cache, addressable by ID
  assert.equal(state.sessionsById[automationExecutionSession.id]?.kind, 'full')
  assert.equal(state.sessionsById[automationExecutionSession.id]?.session.id, automationExecutionSession.id)

  // Explicit hydrate retains execution session cache data without sidebar membership
  const hydratedState = createEmptyDesktopV3CacheState()
  hydratedState.desktopSidebarBootstrap = { status: 'ready', scopeId: 'sidebar' }
  hydratedState.sessionOrderByScope.sidebar = []
  applyHydrateSnapshot(hydratedState, hydrateSnapshotFixture({
    session_order: [automationExecutionSession.id],
    sessions_by_id: { [automationExecutionSession.id]: automationExecutionSession },
    projections_by_session: {},
    messages_by_session: {},
    selector: { kind: 'session_ids', session_ids: [automationExecutionSession.id] },
  }), [automationExecutionSession.id])
  assert.equal(hydratedState.sessionsById[automationExecutionSession.id]?.kind, 'full')
  assert.deepEqual(selectDesktopSidebarRows(hydratedState, 'sidebar'), [])

  // Reconnect removes execution session from sidebar navigation
  state.sessionOrderByScope.sidebar = [automationExecutionSession.id]
  applyReconnectSnapshot(state, reconnectFixture({
    workset_id: 'sidebar',
    session_order: [automationExecutionSession.id],
    sessions_by_id: { [automationExecutionSession.id]: automationExecutionSession },
  }))
  assert.equal(state.sessionOrderByScope.sidebar.includes(automationExecutionSession.id), false)

  // Realtime discovered removes execution session from sidebar navigation
  state.sessionOrderByScope.sidebar = [automationExecutionSession.id]
  applyWorksetSessionDiscovered(state, {
    kind: 'workset.session.discovered',
    workset_id: 'sidebar',
    session_id: automationExecutionSession.id,
    session: automationExecutionSession,
    endpoint_cursor: 'cursor-discovered',
  })
  assert.equal(state.sessionOrderByScope.sidebar.includes(automationExecutionSession.id), false)

  // Realtime updated removes execution session from sidebar navigation
  state.sessionOrderByScope.sidebar = [automationExecutionSession.id]
  applyWorksetSessionUpdated(state, {
    kind: 'workset.session.updated',
    workset_id: 'sidebar',
    session_id: automationExecutionSession.id,
    session: automationExecutionSession,
    endpoint_cursor: 'cursor-updated',
  })
  assert.equal(state.sessionOrderByScope.sidebar.includes(automationExecutionSession.id), false)
  assert.deepEqual(selectDesktopSidebarRows(state).map((row) => row.sessionId), [sessionA.id])
})
