import assert from 'node:assert/strict'
import test from 'node:test'
import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from './desktop-v3-cache-reducer'
import { selectDesktopPendingPermissions } from './desktop-v3-cache-selectors'
import { taskAttentionPermissions } from './task-attention'
import { permissionRequiresApproval } from '../permissions/services/permission-payload'
import type { DesktopPermissionRecord } from '../types/realtime'
import type { DesktopV3CacheState, SessionSnapshot } from './desktop-v3-cache-types'

function permission(id: string, sessionId = 'session'): DesktopPermissionRecord {
  return { id, sessionId, runId: 'run', callId: id, toolName: 'bash', toolArguments: '{}',
    status: 'pending', decision: '', reason: '', requirement: 'approval', mode: 'auto',
    createdAt: 1, updatedAt: 1, resolvedAt: 0, permissionRequestedAt: 1 }
}

function fixture(): DesktopV3CacheState {
  const state = createEmptyDesktopV3CacheState()
  for (const id of ['session', 'other']) {
    state.sessionsById[id] = { kind: 'full', needsHydrate: false, session: {
      id, account_scope_id: 'account', user_id: 'user',
    } as SessionSnapshot }
  }
  state.permissionsBySession.session = [permission('first'), permission('second')]
  state.permissionsBySession.other = [permission('first', 'other')]
  return state
}

function visible(state: DesktopV3CacheState): void {
  assert.deepEqual(selectDesktopPendingPermissions(state, 'session')
    .filter(p => permissionRequiresApproval(p, 'auto')).map(p => p.id), ['second'], 'chat only shows the remaining request')
  assert.deepEqual(taskAttentionPermissions(state, ['session']).map(p => p.id), ['second'], 'Swarm only shows the remaining request')
  assert.deepEqual(selectDesktopPendingPermissions(state, 'other').map(p => p.id), ['first'], 'another session is untouched')
}

// Purpose: the chat and Swarm selectors must hide terminal records retained by
// permission.resolveResult/applyPermissionEvent. Exercise both delivery orders,
// realtime-only resolution, delayed hydration and replay at the reducer boundary:
// no remount/refresh may be needed, and unrelated requests must remain actionable.
test('permission decisions stay cleared in chat and Swarm across HTTP, realtime and stale hydration', () => {
  for (const status of ['approved', 'denied', 'cancelled', 'expired']) {
    for (const sources of [['http'], ['realtime'], ['http', 'realtime'], ['realtime', 'http']]) {
      let state = fixture()
      const resolved = { ...permission('first'), status, updatedAt: 5, resolvedAt: 5 }
      for (const source of sources) {
        state = desktopV3CacheReducer(state, source === 'http'
          ? { type: 'permission.resolveResult', sessionId: 'session', permissionId: 'first', permission: resolved }
          : { type: 'realtime.applyEvent', event: { source: 'realtime', sessionId: 'session', eventType: 'permission.updated', payload: { permission: resolved } } })
        visible(state)
        assert.equal(state.permissionsBySession.session.find(p => p.id === 'first')?.status, status)
      }
      state = desktopV3CacheReducer(state, {
        type: 'hydrate.apply', source: 'hydrate', scopeId: 'review', requestedSessionIds: ['session'],
        snapshot: {
          sessions_by_id: { session: (state.sessionsById.session as { session: SessionSnapshot }).session },
          session_views_by_id: { session: { pending_permissions: [permission('first'), permission('second')] } },
          selector: { kind: 'session_ids', session_ids: ['session'] },
          scope_id: 'review', snapshot_endpoint_cursor: 'opaque-review-cursor',
          sync_scope: { surface: 'desktop', stream_kind: 'v3.sync.snapshot', selector_filter_hash: 'review', resource_set: 'session_view' },
        } as never,
      })
      visible(state)
      state = desktopV3CacheReducer(state, {
        type: 'realtime.applyEvent', event: { source: 'realtime', sessionId: 'session', eventType: 'permission.requested', payload: { permission: permission('first') } },
      })
      visible(state)
      assert.equal(state.permissionsBySession.session.find(p => p.id === 'first')?.status, status, 'resolution evidence survives stale replay')
    }
  }
})

// Purpose: selectDesktopPendingPermissions is the shared actionable-state boundary.
// Inconsistent pending records with resolution evidence must not re-enable review;
// genuine requests still obey permissionRequiresApproval's existing mode policy.
test('pending selectors exclude decision evidence without suppressing genuinely pending requests', () => {
  const state = fixture()
  state.permissionsBySession.session = [
    { ...permission('resolved'), resolvedAt: 2 },
    { ...permission('decided'), decision: 'allow_once' },
    permission('second'),
  ]
  visible(state)
  assert.equal(permissionRequiresApproval(permission('second'), 'auto'), true)
  assert.equal(permissionRequiresApproval({ ...permission('second'), toolName: 'read' }, 'auto'), false)
})

// Purpose: applyPermissionEvent must reject a mismatched session payload without
// clearing any pending permission. This negative reducer test proves the scoped
// normalizer still owns identity validation when terminal events are retained.
test('foreign-session terminal events cannot clear either session permission', () => {
  const state = desktopV3CacheReducer(fixture(), {
    type: 'realtime.applyEvent', event: {
      source: 'realtime', sessionId: 'session', eventType: 'permission.updated',
      payload: { permission: { ...permission('first', 'other'), status: 'approved', updatedAt: 5, resolvedAt: 5 } },
    },
  })
  assert.deepEqual(selectDesktopPendingPermissions(state, 'session').map(p => p.id), ['first', 'second'])
  assert.deepEqual(selectDesktopPendingPermissions(state, 'other').map(p => p.id), ['first'])
})
