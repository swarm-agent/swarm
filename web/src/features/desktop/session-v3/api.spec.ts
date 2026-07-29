import test from 'node:test'
import assert from 'node:assert/strict'

import {
  DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY,
  sessionV3MetadataSettingsMutationResponse,
  updateSessionV3DesktopSidebarPinned,
} from './api'

const originalFetch = globalThis.fetch

test('updateSessionV3DesktopSidebarPinned posts the UI pin metadata key', async () => {
  const calls: Array<{ url: string; body: unknown }> = []
  globalThis.fetch = jsonFetch(calls, {
    ok: true,
    session_id: 'session-1',
    metadata: { [DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY]: true },
    mutation: { realtime_outbox: { session_id: 'session-1' } },
  })

  try {
    const response = await updateSessionV3DesktopSidebarPinned(' session-1 ', true, { custom: 'kept' })
    assert.equal(response.metadata?.[DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY], true)
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.equal(calls.length, 1)
  assert.equal(calls[0]?.url, '/v3/sessions/session-1/metadata')
  assert.match(String((calls[0]?.body as Record<string, unknown>)?.client_request_id), /^desktop-sidebar-pin:[0-9a-f-]{36}$/)
  assert.deepEqual((calls[0]?.body as Record<string, unknown>)?.metadata, {
    custom: 'kept',
    [DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY]: true,
  })
})

test('sessionV3MetadataSettingsMutationResponse maps metadata mutation onto settings result shape', () => {
  const mapped = sessionV3MetadataSettingsMutationResponse({
    ok: true,
    session_id: 'session-1',
    metadata: { [DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY]: false },
    mutation: { realtime_outbox: { session_id: 'session-1' } },
    realtime_outbox: { session_id: 'session-1' },
  }, 'fallback-session')

  assert.deepEqual(mapped, {
    ok: true,
    session_id: 'session-1',
    metadata: { [DESKTOP_V3_SIDEBAR_PINNED_METADATA_KEY]: false },
    turn_usage: undefined,
    usage_summary: undefined,
    mutation: { realtime_outbox: { session_id: 'session-1' } },
    realtime_outbox: { session_id: 'session-1' },
  })
})

function jsonFetch(calls: Array<{ url: string; body: unknown }>, payload: Record<string, unknown>): typeof fetch {
  return (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), body: init?.body ? JSON.parse(String(init.body)) : null })
    return new Response(JSON.stringify(payload), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch
}
