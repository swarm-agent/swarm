import test from 'node:test'
import assert from 'node:assert/strict'

import { DesktopV3RealtimeController } from './v3-realtime-controller'

test('V3 realtime controller persists endpoint.watermark cursor without application mutation', async () => {
  const originalFetch = globalThis.fetch
  const originalWindow = globalThis.window
  const originalWebSocket = globalThis.WebSocket
  const sockets: FakeWebSocket[] = []

  class FakeWebSocket extends EventTarget {
    static CONNECTING = 0
    static OPEN = 1
    static CLOSING = 2
    static CLOSED = 3

    readyState = FakeWebSocket.OPEN
    sent: unknown[] = []

    constructor(readonly url: string | URL) {
      super()
      sockets.push(this)
    }

    send(payload: string): void {
      this.sent.push(JSON.parse(payload))
    }

    close(): void {
      this.readyState = FakeWebSocket.CLOSED
      this.dispatchEvent(new Event('close'))
    }

    emit(payload: unknown): void {
      const event = new Event('message') as MessageEvent
      Object.defineProperty(event, 'data', { value: JSON.stringify(payload) })
      this.dispatchEvent(event)
    }
  }

  globalThis.fetch = (async (input: RequestInfo | URL) => {
    if (String(input) === '/v1/auth/desktop/session') {
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    throw new Error(`unexpected fetch: ${String(input)}`)
  }) as typeof fetch
  globalThis.window = {
    location: { protocol: 'http:', host: '127.0.0.1:5555' },
    setTimeout: globalThis.setTimeout.bind(globalThis),
    clearTimeout: globalThis.clearTimeout.bind(globalThis),
  } as unknown as Window & typeof globalThis
  globalThis.WebSocket = FakeWebSocket as unknown as typeof WebSocket

  try {
    const delivered: string[] = []
    const controller = new DesktopV3RealtimeController({
      getEndpointCursor: () => 'cursor-1',
      onFrame: (_sessionId, frame) => {
        delivered.push(String(frame.kind ?? frame.type ?? ''))
        return false
      },
    })

    await controller.subscribeSession('session-a', 'cursor-1', 'sub-a')
    await new Promise((resolve) => queueMicrotask(resolve))
    assert.equal(sockets.length, 1)
    assert.deepEqual(sockets[0].sent.at(-1), {
      protocol: 'v3.realtime',
      protocol_version: 1,
      kind: 'subscribe.session',
      session_id: 'session-a',
      subscription_id: 'sub-a',
      endpoint_cursor: 'cursor-1',
    })

    sockets[0].emit({
      protocol: 'v3.realtime',
      protocol_version: 1,
      kind: 'endpoint.watermark',
      endpoint_cursor: 'cursor-2',
      high_watermark_seq: 2,
      rev: 2,
      prevRev: 1,
    })
    assert.deepEqual(delivered, ['endpoint.watermark'])

    controller.syncSessions([{ sessionId: 'session-a', endpointCursor: 'cursor-1', subscriptionId: 'sub-a' }], { resubscribe: true })
    assert.deepEqual(sockets[0].sent.at(-1), {
      protocol: 'v3.realtime',
      protocol_version: 1,
      kind: 'subscribe.session',
      session_id: 'session-a',
      subscription_id: 'sub-a',
      endpoint_cursor: 'cursor-2',
    })
  } finally {
    globalThis.fetch = originalFetch
    globalThis.window = originalWindow
    globalThis.WebSocket = originalWebSocket
  }
})
