import assert from 'node:assert/strict'
import test from 'node:test'

import { createEmptyDesktopV3CacheState } from '../state/desktop-v3-cache-reducer'
import { getDesktopV3CacheSnapshot, resetDesktopV3CacheForTests } from '../state/desktop-v3-cache-store'
import { reviewDesktopV3Worktrees, unarchiveDesktopV3ReviewSessions } from './review-worktrees-api'

const originalFetch = globalThis.fetch

function sessionSnapshot(id: string) {
  return {
    id,
    workspace_path: '/workspace',
    workspace_name: 'workspace',
    title: 'Restored session',
    mode: 'auto',
    created_at: 1,
    updated_at: 3,
    message_count: 0,
    last_message_at: 0,
  }
}

test('promotion sends explicit exact source and target lineage', async () => {
  const calls: Array<{ url: string; body: Record<string, unknown> }> = []
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), body: init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : {} })
    return new Response(JSON.stringify({
      ok: true, target_detection: '', current_target_branch: 'dev', current_target_head: 'target-head', comparison: '',
      retained: [], done: [], archived_session_ids: [], recently_archived: [], grace_period_ms: 1,
      checkout_dirty: false, checkout_dirty_count: 0, blocked_by_checkout_count: 0, complete: true,
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch
  try {
    await reviewDesktopV3Worktrees({
      workspacePath: '/workspace', promoteSessionIds: ['session-lane'],
      sourceHeadBySessionId: { 'session-lane': 'source-head' }, targetBranch: 'dev', targetHead: 'target-head',
    })
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.equal(calls[0]?.url, '/v3/sessions:review-worktrees')
  assert.equal(calls[0]?.body.workspace_path, '/workspace')
  assert.deepEqual(calls[0]?.body.promote_session_ids, ['session-lane'])
  assert.deepEqual(calls[0]?.body.source_head_by_session_id, { 'session-lane': 'source-head' })
  assert.equal(calls[0]?.body.target_branch, 'dev')
  assert.equal(calls[0]?.body.target_head, 'target-head')
  assert.equal('integrate_session_ids' in (calls[0]?.body || {}), false)
})

test('unarchive applies committed reactivation and authoritative hydrate to the Desktop cache', async () => {
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap.scopeId = 'sidebar'
  state.tombstonesBySession.restored = {
    session_id: 'restored',
    archived: true,
    kind: 'archived',
    updated_at: 2,
    session: sessionSnapshot('restored'),
  }
  state.worksetsById.sidebar = { workset_id: 'sidebar', sessionIds: [], inactiveSessionIds: ['restored'] }
  state.sessionOrderByScope.sidebar = []
  resetDesktopV3CacheForTests(state)

  const calls: Array<{ url: string; body: Record<string, unknown> }> = []
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    const body = init?.body ? JSON.parse(String(init.body)) as Record<string, unknown> : {}
    calls.push({ url, body })
    if (url === '/v3/sessions:unarchive') {
      return new Response(JSON.stringify({
        ok: true,
        unarchived_session_ids: ['restored'],
        reactivated: {
          restored: {
            endpoint_seq: 7,
            endpoint_cursor: 'cursor-7',
            session_id: 'restored',
            event: {
              id: 'event-3',
              session_id: 'restored',
              seq: 3,
              event_type: 'session.reactivated',
              payload: { session_id: 'restored', seq: 3, kind: 'session.reactivate', session: sessionSnapshot('restored') },
              ts_unix_ms: 3,
            },
            projection: { session_id: 'restored', last_event_seq: 3, projection_high_watermark_seq: 3, updated_at: 3 },
          },
        },
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (url === '/v3/sync/hydrate') {
      return new Response(JSON.stringify({
        ok: true,
        rev: 7,
        snapshot_endpoint_cursor: 'cursor-7',
        sessions_by_id: { restored: sessionSnapshot('restored') },
        projections_by_session: { restored: { session_id: 'restored', last_event_seq: 3, projection_high_watermark_seq: 3, updated_at: 3 } },
        run_intents_by_session: { restored: [] },
        current_run_state_by_session: {},
        permission_summaries_by_session: {},
        session_views_by_id: { restored: { has_active_plan: false, active_plan: null } },
        session_order: ['restored'],
        sync_scope: { surface: 'desktop', stream_kind: 'v3.sync.snapshot', selector_filter_hash: 'restored', resource_set: 'current_run_state,session_view,active_plan,permission_summaries' },
        scope_id: 'hydrate-restored',
        selector: { kind: 'session_ids', session_ids: ['restored'] },
        known_sessions: {},
        tombstones_by_session: {},
        replay_instructions: { stream_path: '/v3/sync/stream', transport: 'http_post', after_endpoint_cursor: 'cursor-7', bootstrap_required_on_cursor_error: true },
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    throw new Error(`unexpected request ${url}`)
  }) as typeof fetch

  try {
    await unarchiveDesktopV3ReviewSessions({ restored: 2 })
  } finally {
    globalThis.fetch = originalFetch
  }

  const restored = getDesktopV3CacheSnapshot()
  assert.equal(restored.tombstonesBySession.restored, undefined)
  assert.equal(restored.sessionsById.restored?.kind, 'full')
  assert.deepEqual(restored.sessionOrderByScope.sidebar, ['restored'])
  assert.deepEqual(restored.worksetsById.sidebar?.sessionIds, ['restored'])
  assert.deepEqual(restored.worksetsById.sidebar?.inactiveSessionIds, [])
  assert.equal(restored.realtime.endpointCursor, undefined)
  assert.deepEqual(calls.map((call) => call.url), ['/v3/sessions:unarchive', '/v3/sync/hydrate'])
  assert.deepEqual(calls[1]?.body.session_ids, ['restored'])

  resetDesktopV3CacheForTests()
})
