import test from 'node:test'
import assert from 'node:assert/strict'

import { DesktopV3RealtimeController } from './v3-realtime-controller'
import { normalizeDesktopStateSnapshot } from '../state/desktop-state-snapshot'

const OPAQUE_CURSOR_1 = 'v3c1.test_payload_1.test_signature_1'
const OPAQUE_CURSOR_2 = 'v3c1.test_payload_2.test_signature_2'
const OPAQUE_CURSOR_3 = 'v3c1.test_payload_3.test_signature_3'
const OPAQUE_SYNC_SNAPSHOT_CURSOR = 'v3c1.sync_snapshot_payload.sync_snapshot_signature'

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


test('diagnostic: Desktop snapshot cursor dispatch must not seed /v3/realtime/stream endpoint cursor', async () => {
  await withMockRealtime(async ({ sockets }) => {
    const dispatched: Array<{ type: string; endpointCursor: string }> = []
    const originalCustomEvent = globalThis.CustomEvent
    globalThis.CustomEvent = class TestCustomEvent<T = unknown> extends Event {
      readonly detail: T
      constructor(type: string, init?: CustomEventInit<T>) {
        super(type)
        this.detail = init?.detail as T
      }
    } as typeof CustomEvent
    globalThis.window = {
      ...(globalThis.window ?? {}),
      location: { protocol: 'http:', host: '127.0.0.1:5555' },
      setTimeout: globalThis.setTimeout.bind(globalThis),
      clearTimeout: globalThis.clearTimeout.bind(globalThis),
      dispatchEvent: (event: Event) => {
        const cursor = String((event as CustomEvent<{ endpointCursor?: string }>).detail?.endpointCursor ?? '').trim()
        if (event.type === 'desktop:v3-realtime-snapshot-cursor' && cursor) {
          dispatched.push({ type: event.type, endpointCursor: cursor })
        }
        return true
      },
    } as unknown as Window & typeof globalThis

    try {
      const controller = new DesktopV3RealtimeController({
        getEndpointCursor: () => '',
        onFrame: () => true,
      })

      const snapshot = normalizeDesktopStateSnapshot({
        rev: 91,
        snapshot_endpoint_cursor: OPAQUE_SYNC_SNAPSHOT_CURSOR,
        sessions_by_id: {},
        session_order: [],
      })
      assert.equal(snapshot.snapshotEndpointCursor, OPAQUE_SYNC_SNAPSHOT_CURSOR)
      assert.deepEqual(dispatched, [{ type: 'desktop:v3-realtime-snapshot-cursor', endpointCursor: OPAQUE_SYNC_SNAPSHOT_CURSOR }])

      controller.setEndpointCursor(OPAQUE_SYNC_SNAPSHOT_CURSOR)
      await controller.subscribeSession('session-scope-mismatch', null, 'sub-scope-mismatch')
      await new Promise((resolve) => queueMicrotask(resolve))

      assert.equal(sockets.length, 1)
      const openedURL = new URL(String((sockets[0] as unknown as { url: string | URL }).url))
      assert.notEqual(
        openedURL.searchParams.get('endpoint_cursor'),
        OPAQUE_SYNC_SNAPSHOT_CURSOR,
        'diagnostic failure: Desktop seeded /v3/realtime/stream from snapshot_endpoint_cursor; live server rejects this as endpoint_cursor_scope_mismatch / sync cursor scope mismatch for stream_kind',
      )
      assert.notDeepEqual(
        sockets[0].sent.at(-1),
        {
          protocol: 'v3.realtime',
          protocol_version: 1,
          kind: 'subscribe.session',
          session_id: 'session-scope-mismatch',
          subscription_id: 'sub-scope-mismatch',
          endpoint_cursor: OPAQUE_SYNC_SNAPSHOT_CURSOR,
        },
        'diagnostic failure: Desktop sent subscribe.session with snapshot_endpoint_cursor; wrong cursor source is normalizeDesktopStateSnapshot snapshot_endpoint_cursor dispatch',
      )
    } finally {
      globalThis.CustomEvent = originalCustomEvent
    }
  })
})
