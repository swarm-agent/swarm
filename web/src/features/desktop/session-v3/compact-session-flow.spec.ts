import test from 'node:test'
import assert from 'node:assert/strict'

import { compactDesktopV3Session, setDesktopV3CompactSessionFlowDepsForTests } from './compact-session-flow'
import type { DesktopV3CacheAction, SyncSnapshotResponse } from '../state/desktop-v3-cache-types'

test('compactDesktopV3Session hydrates through V3 sync using terminal compact cursor', async () => {
  const actions: DesktopV3CacheAction[] = []
  const hydrateInputs: unknown[] = []
  const restore = setDesktopV3CompactSessionFlowDepsForTests({
    compact: async (sessionId) => ({
      ok: true,
      sessionId,
      runId: 'run-compact',
      status: 'completed',
      realtimeOutbox: {
        endpointSeq: 456,
        endpointCursor: 'v3c1.terminal-compact-cursor',
        sessionId,
      },
    }),
    hydrate: async (input) => {
      hydrateInputs.push(input)
      return {
        ok: true,
        rev: 456,
        snapshot_endpoint_cursor: 'v3c1.snapshot-after-compact',
        sessions_by_id: {},
        projections_by_session: {},
        messages_by_session: { 'session-v3': [] },
        run_intents_by_session: { 'session-v3': [] },
        session_order: ['session-v3'],
        sync_scope: {
          surface: 'desktop',
          stream_kind: 'v3.sync.snapshot',
          selector_filter_hash: 'selector-hash',
          resource_set: 'current_run_state,membership,messages,projections,run_intents,session_view,sessions,tombstones',
        },
        scope_id: 'selector-hash:current_run_state,membership,messages,projections,run_intents,session_view,sessions,tombstones',
        selector: { kind: 'session_ids', session_ids: ['session-v3'] },
        known_sessions: {},
        tombstones_by_session: {},
        omissions: [],
        pagination: {},
        watermarks: {},
        replay_instructions: {
          stream_path: '/v3/sync/stream',
          transport: 'http_post',
          after_endpoint_cursor: 'v3c1.snapshot-after-compact',
          bootstrap_required_on_cursor_error: true,
        },
      } as SyncSnapshotResponse
    },
    dispatch: (action) => {
      actions.push(action)
    },
  })

  try {
    const response = await compactDesktopV3Session({ sessionId: 'session-v3', note: 'keep constraints' })
    assert.equal(response.status, 'completed')
    assert.equal(hydrateInputs.length, 1)
    assert.deepEqual(hydrateInputs[0], {
      surface: 'desktop',
      session_ids: ['session-v3'],
      history: {
        mode: 'tail',
        max_messages_per_session: 200,
        manifest_policy: 'manifest',
      },
      resources: {
        messages: true,
        events: false,
        run_intents: true,
        current_run_state: true,
        session_view: true,
        active_plan: true,
        plan_revisions: false,
        permission_summaries: false,
      },
      include_active: true,
      known_sessions: {
        'session-v3': { endpoint_cursor: 'v3c1.terminal-compact-cursor' },
      },
    })
    assert.equal(actions.length, 1)
    assert.equal(actions[0].type, 'hydrate.apply')
  } finally {
    restore()
  }
})
