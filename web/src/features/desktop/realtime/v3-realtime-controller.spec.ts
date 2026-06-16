import test from 'node:test'
import assert from 'node:assert/strict'

import { DesktopV3RealtimeController } from './v3-realtime-controller'

const OPAQUE_CURSOR_1 = 'v3c1.test_payload_1.test_signature_1'
const OPAQUE_CURSOR_2 = 'v3c1.test_payload_2.test_signature_2'
const OPAQUE_CURSOR_3 = 'v3c1.test_payload_3.test_signature_3'

async function withMockRealtime(run: (ctx: { sockets: Array<{ sent: unknown[]; emit: (payload: unknown) => void }> }) => Promise<void>): Promise<void> {
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
      if (this.readyState === FakeWebSocket.CLOSED) return
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
    await run({ sockets })
  } finally {
    globalThis.fetch = originalFetch
    globalThis.window = originalWindow
    globalThis.WebSocket = originalWebSocket
  }
}

test('V3 realtime controller persists endpoint.watermark cursor without application mutation', async () => {
  await withMockRealtime(async ({ sockets }) => {
    const delivered: string[] = []
    const controller = new DesktopV3RealtimeController({
      getEndpointCursor: () => OPAQUE_CURSOR_1,
      onFrame: (_sessionId, frame) => {
        delivered.push(String(frame.kind ?? frame.type ?? ''))
        return false
      },
    })

    await controller.subscribeSession('session-a', OPAQUE_CURSOR_1, 'sub-a')
    await new Promise((resolve) => queueMicrotask(resolve))
    assert.equal(sockets.length, 1)
    assert.deepEqual(sockets[0].sent.at(-1), {
      protocol: 'v3.realtime',
      protocol_version: 1,
      kind: 'subscribe.session',
      session_id: 'session-a',
      subscription_id: 'sub-a',
      endpoint_cursor: OPAQUE_CURSOR_1,
    })

    sockets[0].emit({
      protocol: 'v3.realtime',
      protocol_version: 1,
      kind: 'endpoint.watermark',
      endpoint_cursor: OPAQUE_CURSOR_2,
      high_watermark_seq: 2,
      rev: 2,
      prevRev: 1,
    })
    assert.deepEqual(delivered, ['endpoint.watermark'])

    controller.syncSessions([{ sessionId: 'session-a', endpointCursor: OPAQUE_CURSOR_1, subscriptionId: 'sub-a' }], { resubscribe: true })
    assert.deepEqual(sockets[0].sent.at(-1), {
      protocol: 'v3.realtime',
      protocol_version: 1,
      kind: 'subscribe.session',
      session_id: 'session-a',
      subscription_id: 'sub-a',
      endpoint_cursor: OPAQUE_CURSOR_2,
    })
  })
})

test('V3 realtime controller keeps bootstrap cursor ahead of stale subscription cursor for durable reconnect repair', async () => {
  await withMockRealtime(async ({ sockets }) => {
    const controller = new DesktopV3RealtimeController({
      getEndpointCursor: () => OPAQUE_CURSOR_1,
      onFrame: () => true,
    })

    await controller.subscribeSession('session-a', OPAQUE_CURSOR_1, 'sub-a')
    await new Promise((resolve) => queueMicrotask(resolve))

    controller.setEndpointCursor(OPAQUE_CURSOR_3)
    controller.syncSessions([{ sessionId: 'session-a', endpointCursor: OPAQUE_CURSOR_1, subscriptionId: 'sub-a' }], { resubscribe: true })

    assert.deepEqual(sockets[0].sent.at(-1), {
      protocol: 'v3.realtime',
      protocol_version: 1,
      kind: 'subscribe.session',
      session_id: 'session-a',
      subscription_id: 'sub-a',
      endpoint_cursor: OPAQUE_CURSOR_3,
    })
  })
})
