import test from 'node:test'
import assert from 'node:assert/strict'

import { openDesktopV3RealtimeTransportSocket } from '../session-v3/transport'

test('Desktop V3 realtime transport bootstraps the local session cookie and uses only endpoint_cursor query params', async () => {
  const fetchCalls: string[] = []
  const websocketURLs: string[] = []
  const originalFetch = globalThis.fetch
  const originalWindow = globalThis.window
  const originalWebSocket = globalThis.WebSocket

  class FakeWebSocket {
    url: string

    constructor(input: string | URL) {
      this.url = String(input)
      websocketURLs.push(this.url)
    }

    addEventListener() {}
    removeEventListener() {}
    send() {}
    close() {}
  }

  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const url = String(input)
    fetchCalls.push(url)
    if (url === '/v1/auth/desktop/session') {
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch
  globalThis.window = {
    location: {
      protocol: 'http:',
      host: '127.0.0.1:5555',
    },
  } as unknown as Window & typeof globalThis
  globalThis.WebSocket = FakeWebSocket as unknown as typeof WebSocket

  try {
    const { ensureDesktopSession } = await import('../../../app/api')

    await ensureDesktopSession(true)
    openDesktopV3RealtimeTransportSocket({ endpointCursor: 'cursor-local-auth' })

    assert.deepEqual(fetchCalls, ['/v1/auth/desktop/session'])
    assert.deepEqual(websocketURLs, [
      'ws://127.0.0.1:5555/v3/realtime/stream?endpoint_cursor=cursor-local-auth',
    ])
    const url = new URL(websocketURLs[0])
    assert.equal(url.pathname, '/v3/realtime/stream')
    assert.deepEqual(Array.from(url.searchParams.keys()), ['endpoint_cursor'])
    assert.equal(url.searchParams.get('token'), null)
    assert.equal(url.searchParams.get('after_seq'), null)
  } finally {
    globalThis.fetch = originalFetch
    globalThis.window = originalWindow
    globalThis.WebSocket = originalWebSocket
  }
})
