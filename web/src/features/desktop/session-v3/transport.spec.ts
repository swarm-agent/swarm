import assert from 'node:assert/strict'
import test from 'node:test'

import { DesktopV3RealtimeTransport, openDesktopV3RealtimeTransportSocket } from './transport'
import type { SessionV3RealtimeFrameWire, SessionV3RealtimeResumeWire } from './types'

class MockRealtimeSocket {
  readyState = MockRealtimeSocket.CONNECTING
  sent: string[] = []
  closed = false
  private readonly listeners = new Map<string, Array<(event?: { data?: unknown }) => void>>()

  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3

  addEventListener(type: string, listener: (event?: { data?: unknown }) => void): void {
    const listeners = this.listeners.get(type) ?? []
    listeners.push(listener)
    this.listeners.set(type, listeners)
  }

  send(raw: string): void {
    this.sent.push(raw)
  }

  close(): void {
    if (this.readyState === MockRealtimeSocket.CLOSED) return
    this.closed = true
    this.readyState = MockRealtimeSocket.CLOSED
    this.emit('close')
  }

  open(): void {
    this.readyState = MockRealtimeSocket.OPEN
    this.emit('open')
  }

  message(frame: SessionV3RealtimeFrameWire): void {
    this.emit('message', { data: JSON.stringify(frame) })
  }

  private emit(type: string, event?: { data?: unknown }): void {
    for (const listener of this.listeners.get(type) ?? []) {
      listener(event)
    }
  }
}

function installTransportGlobals(): void {
  const target = globalThis as typeof globalThis & { window?: unknown; WebSocket?: unknown }
  target.window ??= {
    setTimeout: globalThis.setTimeout.bind(globalThis),
    clearTimeout: globalThis.clearTimeout.bind(globalThis),
    location: { protocol: 'http:', host: 'localhost' },
  }
  target.WebSocket ??= MockRealtimeSocket
}

function latestResume(resumes: SessionV3RealtimeResumeWire[]): SessionV3RealtimeResumeWire {
  const resume = resumes[resumes.length - 1]
  assert.ok(resume)
  return resume
}

test('transport socket URL uses only endpoint_cursor', () => {
  installTransportGlobals()
  const target = globalThis as typeof globalThis & { WebSocket?: unknown }
  const previous = Object.getOwnPropertyDescriptor(target, 'WebSocket')
  let capturedUrl = ''
  class CapturingSocket {
    constructor(url?: string | URL) {
      capturedUrl = url instanceof URL ? url.toString() : String(url ?? '')
    }
  }
  Object.defineProperty(target, 'WebSocket', { value: CapturingSocket, configurable: true })
  try {
    openDesktopV3RealtimeTransportSocket({ endpointCursor: 'cursor-1' })
    const url = new URL(capturedUrl)
    assert.equal(url.pathname, '/v3/realtime/stream')
    assert.equal(url.searchParams.get('endpoint_cursor'), 'cursor-1')
    assert.deepEqual(Array.from(url.searchParams.keys()), ['endpoint_cursor'])
    assert.throws(() => openDesktopV3RealtimeTransportSocket({ endpointCursor: ' ' }), /requires endpoint_cursor/)
  } finally {
    if (previous) {
      Object.defineProperty(target, 'WebSocket', previous)
    } else {
      delete target.WebSocket
    }
  }
})

test('transport reopens normal closes from last endpoint cursor without rehydrate', async () => {
  installTransportGlobals()
  const sockets: MockRealtimeSocket[] = []
  const openEndpointCursors: string[] = []
  const resumes: SessionV3RealtimeResumeWire[] = []
  let rehydrateCalls = 0
  const transport = new DesktopV3RealtimeTransport({
    getEndpointCursor: () => 'cursor-0',
    openSocket: ({ endpointCursor }) => {
      openEndpointCursors.push(endpointCursor)
      const socket = new MockRealtimeSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
    onResumeSent: (resume) => resumes.push(resume),
    onRehydrateRequested: () => {
      rehydrateCalls += 1
    },
    reopenBaseDelayMs: 1,
    reopenMaxDelayMs: 1,
    livenessTimeoutMs: 60_000,
  })
  transport.registerSession({
    session_id: 'session-1',
    subscription_id: 'subscription-1',
    endpoint_cursor: 'cursor-0',
  })

  await transport.start()
  assert.deepEqual(openEndpointCursors, ['cursor-0'])
  assert.equal(sockets.length, 1)
  sockets[0].open()
  assert.equal(resumes.length, 1)

  sockets[0].message({ kind: 'event', session_id: 'session-1', endpoint_cursor: 'cursor-1' })
  sockets[0].close()
  await new Promise((resolve) => setTimeout(resolve, 5))

  assert.equal(rehydrateCalls, 0)
  assert.deepEqual(openEndpointCursors, ['cursor-0', 'cursor-1'])
  assert.equal(sockets.length, 2)
  sockets[1].open()
  assert.equal(latestResume(resumes).endpoint_cursor, 'cursor-1')
  assert.deepEqual(latestResume(resumes).subscriptions?.map((subscription) => subscription.session_id), ['session-1'])
  transport.stop()
})

test('transport cursor errors recover only through rehydrate result', async () => {
  installTransportGlobals()
  const sockets: MockRealtimeSocket[] = []
  const resumes: SessionV3RealtimeResumeWire[] = []
  let rehydrateCalls = 0
  const transport = new DesktopV3RealtimeTransport({
    getEndpointCursor: () => 'cursor-0',
    openSocket: () => {
      const socket = new MockRealtimeSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
    onResumeSent: (resume) => resumes.push(resume),
    onRehydrateRequested: async () => {
      rehydrateCalls += 1
      return {
        endpointCursor: 'cursor-rehydrated',
        subscriptions: [{ session_id: 'session-1', subscription_id: 'subscription-1', endpoint_cursor: 'cursor-rehydrated' }],
      }
    },
    reopenBaseDelayMs: 1,
    reopenMaxDelayMs: 1,
    livenessTimeoutMs: 60_000,
  })

  await transport.start()
  sockets[0].open()
  assert.equal(resumes.length, 1)

  sockets[0].message({ kind: 'cursor.error', error: 'gap', endpoint_cursor: 'cursor-gap' })
  await new Promise((resolve) => setTimeout(resolve, 0))

  assert.equal(rehydrateCalls, 1)
  assert.equal(sockets.length, 2)
  assert.equal(transport.diagnostics().reopenTimerActive, false)
  sockets[1].open()
  assert.equal(latestResume(resumes).endpoint_cursor, 'cursor-rehydrated')
  assert.deepEqual(latestResume(resumes).subscriptions?.map((subscription) => subscription.session_id), ['session-1'])
  transport.stop()
})

test('transport removal drops auto-discovered subscriptions from future resumes', async () => {
  installTransportGlobals()
  const resumes: SessionV3RealtimeResumeWire[] = []
  const socket = new MockRealtimeSocket()
  const transport = new DesktopV3RealtimeTransport({
    getEndpointCursor: () => 'cursor-0',
    openSocket: () => socket as unknown as WebSocket,
    onResumeSent: (resume) => resumes.push(resume),
    livenessTimeoutMs: 60_000,
  })
  transport.registerWorkset({
    workset_id: 'workset-1',
    subscription_id: 'workset-subscription-1',
    selector: { kind: 'global', global: true },
    auto_subscribe_sessions: true,
  })

  await transport.start()
  socket.open()
  assert.deepEqual(latestResume(resumes).subscriptions, [])

  socket.message({
    kind: 'workset.session.discovered',
    session_id: 'session-auto',
    subscription_id: 'subscription-auto',
    workset_id: 'workset-1',
    workset_subscription_id: 'workset-subscription-1',
    auto_subscribed: true,
    endpoint_cursor: 'cursor-1',
  })
  assert.equal(transport.diagnostics().sessionSubscriptionCount, 1)
  assert.equal(transport.diagnostics().sessions[0]?.autoDiscovered, true)

  socket.message({
    kind: 'workset.session.removed',
    session_id: 'session-auto',
    subscription_id: 'subscription-auto',
    workset_id: 'workset-1',
    workset_subscription_id: 'workset-subscription-1',
    auto_subscribed: true,
    endpoint_cursor: 'cursor-2',
  })

  assert.equal(transport.diagnostics().sessionSubscriptionCount, 0)
  assert.equal(latestResume(resumes).endpoint_cursor, 'cursor-2')
  assert.deepEqual(latestResume(resumes).subscriptions, [])
  assert.deepEqual(latestResume(resumes).worksets?.map((workset) => workset.workset_id), ['workset-1'])
  transport.stop()
})

test('transport removal preserves explicit manual subscriptions', async () => {
  installTransportGlobals()
  const resumes: SessionV3RealtimeResumeWire[] = []
  const socket = new MockRealtimeSocket()
  const transport = new DesktopV3RealtimeTransport({
    getEndpointCursor: () => 'cursor-0',
    openSocket: () => socket as unknown as WebSocket,
    onResumeSent: (resume) => resumes.push(resume),
    livenessTimeoutMs: 60_000,
  })
  transport.registerSession({
    session_id: 'session-manual',
    subscription_id: 'subscription-manual',
    endpoint_cursor: 'cursor-0',
  })

  await transport.start()
  socket.open()
  assert.deepEqual(latestResume(resumes).subscriptions?.map((subscription) => subscription.session_id), ['session-manual'])

  socket.message({
    kind: 'workset.session.removed',
    session_id: 'session-manual',
    subscription_id: 'subscription-auto',
    workset_id: 'workset-1',
    workset_subscription_id: 'workset-subscription-1',
    auto_subscribed: true,
    endpoint_cursor: 'cursor-1',
  })

  assert.equal(transport.diagnostics().sessionSubscriptionCount, 1)
  assert.equal(transport.diagnostics().sessions[0]?.autoDiscovered, false)
  assert.deepEqual(latestResume(resumes).subscriptions?.map((subscription) => subscription.session_id), ['session-manual'])
  transport.stop()
})
