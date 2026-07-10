import assert from 'node:assert/strict'
import test from 'node:test'

import { deleteDesktopSessions, searchDesktopSessions } from './session-search-api'

const originalFetch = globalThis.fetch

function jsonFetch(calls: Array<{ url: string; body: unknown }>, payload: Record<string, unknown>): typeof fetch {
  return (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), body: init?.body ? JSON.parse(String(init.body)) : null })
    return new Response(JSON.stringify(payload), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }) as typeof fetch
}

test('searchDesktopSessions exposes summary counters and lineage metrics', async () => {
  const calls: Array<{ url: string; body: unknown }> = []
  globalThis.fetch = jsonFetch(calls, {
    items: [{ id: 'child', library_metric: { session_id: 'child', parent_session_id: 'root', root_session_id: 'root', lineage_kind: 'delegated_subagent' } }],
    pagination: { has_more: false },
    summary: { active_conversation_count: 2, archived_conversation_count: 1, raw_session_count: 4, agent_child_count: 1, logical_content_bytes: 2048 },
  })
  try {
    const result = await searchDesktopSessions({ global: true, limit: 50 })
    assert.equal(result.summary.active_conversation_count, 2)
    assert.equal(result.summary.raw_session_count, 4)
    assert.equal(result.items[0]?.library_metric?.root_session_id, 'root')
  } finally { globalThis.fetch = originalFetch }
  assert.equal(calls[0]?.url, '/v3/sessions:search')
})

test('deleteDesktopSessions preserves preview token for confirmed execution', async () => {
  const calls: Array<{ url: string; body: unknown }> = []
  const preview = { conversation_count: 1, session_count: 2, child_count: 1, logical_bytes: 100, active_run_count: 0, pending_approval_count: 0, recent_75_overlap_count: 1, session_ids: ['child', 'root'], confirmation_token: 'candidate-token' }
  globalThis.fetch = jsonFetch(calls, { ok: true, preview })
  try {
    const result = await deleteDesktopSessions({ session_ids: ['root'], global: true, dry_run: true })
    await deleteDesktopSessions({ session_ids: ['root'], global: true, confirmation_token: result.confirmation_token, confirm_recent: true })
  } finally { globalThis.fetch = originalFetch }
  assert.deepEqual(calls.map((call) => call.body), [
    { session_ids: ['root'], global: true, dry_run: true },
    { session_ids: ['root'], global: true, confirmation_token: 'candidate-token', confirm_recent: true },
  ])
})
