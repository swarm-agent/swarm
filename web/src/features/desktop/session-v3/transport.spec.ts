import assert from 'node:assert/strict'
import test from 'node:test'

import { DesktopV3RealtimeTransport } from './transport'
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
