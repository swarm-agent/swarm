import assert from 'node:assert/strict'
import { afterEach, test } from 'node:test'

import { requestJson } from './api'

const originalFetch = globalThis.fetch

afterEach(() => {
  globalThis.fetch = originalFetch
})

test('requestJson disables browser cache by default', async () => {
  let captured: RequestInit | undefined
  globalThis.fetch = (async (_input: RequestInfo | URL, init?: RequestInit) => {
    captured = init
    return new Response(JSON.stringify({ ok: true }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch

  await requestJson<{ ok: boolean }>('/v1/ui/settings', undefined, false)

  assert.equal(captured?.cache, 'no-store')
})

test('requestJson preserves explicit cache option', async () => {
  let captured: RequestInit | undefined
  globalThis.fetch = (async (_input: RequestInfo | URL, init?: RequestInit) => {
    captured = init
    return new Response(JSON.stringify({ ok: true }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch

  await requestJson<{ ok: boolean }>('/v1/ui/settings', { cache: 'reload' }, false)

  assert.equal(captured?.cache, 'reload')
})
