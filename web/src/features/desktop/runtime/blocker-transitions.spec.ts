import test from 'node:test'
import assert from 'node:assert/strict'
import { createBlockerTransitionTracker } from './blocker-transitions'
import { createEmptyDesktopV3CacheState } from '../state/desktop-v3-cache-reducer'
import { selectDesktopSidebarRows, selectDesktopSidebarGroupedRows } from '../state/desktop-v3-cache-selectors'
import { normalizeDesktopSessionPlan } from '../chat/services/session-plan-record'
import { sessionA, projectionA } from '../state/desktop-v3-cache.backend-fixtures'

// Requirement: only canonical blocked checkpoints require external recovery.
// Threat: permissions, failed attempts, duplicate snapshots and reconnects produce
// false blockers or repeated alerts. Exercise the actual cache selectors and tracker.
function fixture() {
  const state = createEmptyDesktopV3CacheState()
  state.hasActivePlanBySession[sessionA.id] = true
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'scope-a' }
  state.sessionOrderByScope['scope-a'] = [sessionA.id]
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.projectionsBySession[sessionA.id] = { ...projectionA, session_id: sessionA.id, active_plan_id: 'plan-a' }
  const update = (status: string, attempt = 'attempt-a') => {
    state.plansBySession[sessionA.id] = normalizeDesktopSessionPlan({ id: 'plan-a', title: 'Build', document: {
      active_checkpoint_id: 'cp-a', execution_state: { status }, checkpoints: [{ id: 'cp-a', title: 'Build', status, attempt_id: attempt,
        handoff: { title: 'Missing access', overview: 'The service account lacks access.' },
        recommendation: { decision: 'defer', action: 'Grant access, then tell Swarm.', reason: 'Missing access', action_state: 'needs_approval' },
      }],
    } })
    return selectDesktopSidebarRows(state)
  }
  return { state, update }
}

test('blocked grouping is distinct from failed, review and permission waits', () => {
  const { state, update } = fixture()
  assert.equal(update('blocked')[0].sidebarGroup, 'blocked')
  assert.equal(selectDesktopSidebarGroupedRows(state).blocked.length, 1)
  assert.equal(update('failed')[0].planExecution?.statusLabel, 'FAILED')
  assert.notEqual(update('failed')[0].sidebarGroup, 'blocked')
  assert.equal(update('needs_review')[0].sidebarGroup, 'needs_review')
  state.permissionSummaryBySessionId[sessionA.id] = { pendingApprovalCount: 1, oldestPendingAt: 1, newestPendingAt: 1, updatedAt: 1 }
  assert.notEqual(update('in_progress')[0].sidebarGroup, 'blocked')
})

test('blocker transitions notify once with reason/action, remain silent on replay and reconnect', () => {
  const { update } = fixture()
  const track = createBlockerTransitionTracker()
  assert.deepEqual(track(update('in_progress')), [])
  const blocked = update('blocked')
  assert.match(track(blocked)[0], /lacks access.*Next action: Grant access/)
  assert.deepEqual(track(blocked), [])
  assert.deepEqual(track([]), [])
  assert.deepEqual(track(blocked), [])
  assert.deepEqual(track(update('failed')), [])
  assert.deepEqual(track(update('in_progress')), [])
  assert.equal(track(update('blocked', 'attempt-b')).length, 1)
  assert.deepEqual(createBlockerTransitionTracker()(update('blocked')), [])
})
