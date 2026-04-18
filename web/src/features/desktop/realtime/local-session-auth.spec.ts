import test from 'node:test'
import assert from 'node:assert/strict'

test('desktop websocket clients bootstrap the local session cookie and do not send token query params', async () => {
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
    const { openDesktopWebSocket } = await import('./client')
    const { openRunStream } = await import('../chat/queries/chat-queries')

    await openDesktopWebSocket()
    await openRunStream('session-local-auth')

    assert.deepEqual(fetchCalls, ['/v1/auth/desktop/session', '/v1/auth/desktop/session'])
    assert.deepEqual(websocketURLs, [
      'ws://127.0.0.1:5555/ws',
      'ws://127.0.0.1:5555/v1/sessions/session-local-auth/run/stream',
    ])
    for (const url of websocketURLs) {
      assert.equal(new URL(url).searchParams.get('token'), null)
    }
  } finally {
    globalThis.fetch = originalFetch
    globalThis.window = originalWindow
    globalThis.WebSocket = originalWebSocket
  }
})
