import test from 'node:test'
import assert from 'node:assert/strict'

import { DESKTOP_V3_BOOTSTRAP_DEFAULT_INPUT, postDesktopV3SyncBootstrap } from './desktop-v3-sync-api'
import { snapshotFixture } from './desktop-v3-cache.backend-fixtures'

test('postDesktopV3SyncBootstrap posts exact Step 1 payload to bootstrap endpoint', async () => {
  const originalFetch = globalThis.fetch
  const calls: Array<{ url: string; init: RequestInit }> = []
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init: init ?? {} })
    return new Response(JSON.stringify(snapshotFixture({ messages_by_session: {}, events_by_session: {} })), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch

  try {
    await postDesktopV3SyncBootstrap()
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.equal(calls.length, 1)
  assert.equal(calls[0].url, '/v3/sync/bootstrap')
  assert.equal(calls[0].init.method, 'POST')
  assert.deepEqual(JSON.parse(String(calls[0].init.body)), DESKTOP_V3_BOOTSTRAP_DEFAULT_INPUT)
})
