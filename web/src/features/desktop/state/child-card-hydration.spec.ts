import assert from 'node:assert/strict'
import test from 'node:test'
import { retainCurrentChildHydration } from './desktop-v3-session-hydrator'
import { isDesktopV3SessionViewReady } from './desktop-v3-cache-selectors'
import { desktopV3CacheReducer, createEmptyDesktopV3CacheState } from './desktop-v3-cache-reducer'
import { hydrateResponseToAction } from './desktop-v3-cache-wire'

// Requirement: batched card replies must not leak canceled route identities or
// overwrite composer settings. Canonical reducer/selector is the narrowest layer
// proving real question records are usable without claiming full session readiness.
test('permission-only hydration preserves question identity without faking composer readiness', () => {
  const response: any = { ok: true, rev: 1, scope_id: 'cards', snapshot_endpoint_cursor: 'opaque',
    sync_scope: { surface: 'desktop', stream_kind: 'v3.sync.snapshot', selector_filter_hash: 'cards', resource_set: 'permission_details,permission_summaries,active_plan' },
    selector: { kind: 'session_ids', session_ids: ['old', 'current'] }, session_order: ['old', 'current'],
    sessions_by_id: Object.fromEntries(['old', 'current'].map(id => [id, { id, account_scope_id: 'account', user_id: 'user', workspace_path: '.', created_at: 1 }])), projections_by_session: {},
    session_views_by_id: { old: { pending_permissions: [] }, current: { has_active_plan: false, pending_permissions: [{ id: 'permission', session_id: 'current', run_id: 'run', call_id: 'call', tool_name: 'ask_user', status: 'pending', tool_arguments: '{"question":"Choose"}', created_at: 1, updated_at: 2 }] } },
    permission_summaries_by_session: { current: { pending_approval_count: 1 } }, known_sessions: {}, tombstones_by_session: {},
  }
  const filtered = retainCurrentChildHydration(response, ['old', 'current'], ['current'])
  assert.deepEqual(filtered.session_order, ['current'])
  assert.equal(filtered.snapshot_endpoint_cursor, 'opaque')
  const state = desktopV3CacheReducer(createEmptyDesktopV3CacheState(), hydrateResponseToAction(filtered, ['current']))
  assert.equal(state.sessionsById.old, undefined)
  assert.equal(state.permissionsBySession.current[0].id, 'permission')
  assert.equal(state.permissionsBySession.current[0].runId, 'run')
  assert.equal(state.permissionsBySession.current[0].callId, 'call')
  assert.equal(isDesktopV3SessionViewReady(state, 'current'), false)
  assert.throws(() => retainCurrentChildHydration({ ...response, sessions_by_id: { foreign: { id: 'foreign' } } }, ['old', 'current'], ['current']), /Unexpected session/)
})
