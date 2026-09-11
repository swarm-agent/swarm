import test from 'node:test'
import assert from 'node:assert/strict'
import { isAutomationManagementSession } from './desktop-automation-purpose'
import { isDesktopV3NavigationHiddenSession } from './desktop-v3-session-visibility'
import { createEmptyDesktopV3CacheState, applySnapshot } from './desktop-v3-cache-reducer'
import { selectDesktopSidebarRows } from './desktop-v3-cache-selectors'
import { sessionA, sessionB, snapshotFixture } from './desktop-v3-cache.backend-fixtures'

// Purpose: canonical snapshot hydration must retain automation chats for the
// embedded renderer while ordinary navigation excludes only the complete purpose
// contract. Titles and ordinary automation bindings must not classify a session.
test('automation management survives hydration without entering ordinary navigation', () => {
  const management = { ...sessionB, metadata: { swarm_v3_session_purpose: 'automation_management', swarm_v3_purpose_workspace_id: 'workspace' } }
  assert.equal(isAutomationManagementSession(management, 'workspace'), true)
  assert.equal(isAutomationManagementSession(management, 'foreign'), false)
  assert.equal(isDesktopV3NavigationHiddenSession(management), true)
  assert.equal(isDesktopV3NavigationHiddenSession({ ...sessionA, title: 'Automation planning', metadata: { automation_id: 'saved' } }), false)
  assert.equal(isAutomationManagementSession({ ...management, metadata: { swarm_v3_session_purpose: 'automation_management' } }), false)
  const state = createEmptyDesktopV3CacheState()
  const snapshot = snapshotFixture({ sessions_by_id: { [sessionA.id]: sessionA, [management.id]: management }, projections_by_session: {}, messages_by_session: {}, run_intents_by_session: {}, session_order: [management.id, sessionA.id] })
  applySnapshot(state, { source: 'bootstrap', scopeId: snapshot.scope_id, snapshot })
  assert.deepEqual(selectDesktopSidebarRows(state).map(row => row.sessionId), [sessionA.id])
  assert.ok(state.sessionsById[management.id])
})
