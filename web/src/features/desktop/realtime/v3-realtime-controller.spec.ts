import test from 'node:test'
import assert from 'node:assert/strict'

import { DesktopV3RealtimeTransport } from '../session-v3/transport'
import type { SessionV3RealtimeResumeWire } from '../session-v3/types'

if (typeof window === 'undefined') {
  Object.defineProperty(globalThis, 'window', {
    value: {
      setTimeout: globalThis.setTimeout.bind(globalThis),
      clearTimeout: globalThis.clearTimeout.bind(globalThis),
    },
    configurable: true,
  })
}

class FakeWebSocket extends EventTarget {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3

  readyState = FakeWebSocket.CONNECTING
  sent: unknown[] = []
  closed = false

  send(payload: string): void {
    this.sent.push(JSON.parse(payload))
  }

  close(): void {
    if (this.readyState === FakeWebSocket.CLOSED) return
    this.closed = true
    this.readyState = FakeWebSocket.CLOSED
    this.dispatchEvent(new Event('close'))
  }

  open(): void {
    this.readyState = FakeWebSocket.OPEN
    this.dispatchEvent(new Event('open'))
  }

  emit(payload: unknown): void {
    const event = new Event('message') as MessageEvent
    Object.defineProperty(event, 'data', { value: JSON.stringify(payload) })
    this.dispatchEvent(event)
  }
}

test('Desktop V3 realtime transport persists endpoint.watermark cursor without application mutation', async () => {
  const delivered: string[] = []
  const resumes: SessionV3RealtimeResumeWire[] = []
  const socket = new FakeWebSocket()
  const transport = new DesktopV3RealtimeTransport({
    getEndpointCursor: () => 'v3c1.test_payload_1.test_signature_1',
    openSocket: () => socket as unknown as WebSocket,
    onFrame: ({ frame }) => delivered.push(String(frame.kind ?? frame.type ?? '')),
    onResumeSent: (resume) => resumes.push(resume),
    livenessTimeoutMs: 60_000,
  })
  transport.registerWorkset({
    workset_id: 'desktop-workset',
    subscription_id: 'desktop-workset-subscription',
    selector: { kind: 'global', global: true },
    auto_subscribe_sessions: true,
  })

  await transport.start()
  socket.open()
  assert.equal(resumes.length, 1)
  assert.equal(resumes[0].endpoint_cursor, 'v3c1.test_payload_1.test_signature_1')
  assert.equal(resumes[0].worksets?.[0]?.workset_id, 'desktop-workset')
  assert.equal(resumes[0].worksets?.[0]?.auto_subscribe_sessions, true)

  socket.emit({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'endpoint.watermark',
    endpoint_cursor: 'v3c1.test_payload_2.test_signature_2',
    high_watermark_seq: 2,
    rev: 2,
    prevRev: 1,
  })
  assert.deepEqual(delivered, ['endpoint.watermark'])

  transport.registerSession({
    session_id: 'session-a',
    subscription_id: 'sub-a',
    endpoint_cursor: 'stale-session-cursor',
  })

  const latestResume = resumes[resumes.length - 1]
  assert.equal(latestResume.endpoint_cursor, 'v3c1.test_payload_2.test_signature_2')
  assert.equal(latestResume.subscriptions?.[0]?.endpoint_cursor, 'v3c1.test_payload_2.test_signature_2')
  assert.equal('after_seq' in (latestResume.subscriptions?.[0] ?? {}), false)
  assert.equal('after_rev' in (latestResume.subscriptions?.[0] ?? {}), false)
  transport.stop()
})

test('Desktop V3 realtime transport does not advance cursor for auto-discovery before durable event', async () => {
  const resumes: SessionV3RealtimeResumeWire[] = []
  const sockets: FakeWebSocket[] = []
  const transport = new DesktopV3RealtimeTransport({
    getEndpointCursor: () => 'C0',
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
    onResumeSent: (resume) => resumes.push(resume),
    livenessTimeoutMs: 60_000,
  })

  transport.registerWorkset({
    workset_id: 'desktop-workset',
    subscription_id: 'desktop-workset-subscription',
    selector: { kind: 'global', global: true },
    auto_subscribe_sessions: true,
  })

  await transport.start()
  sockets[0].open()
  assert.equal(resumes[0].endpoint_cursor, 'C0')

  sockets[0].emit({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'workset.session.discovered',
    workset_id: 'desktop-workset',
    session_id: 'session-new',
    subscription_id: 'sub-session-new',
    auto_subscribed: true,
    endpoint_cursor: 'C1',
  })

  sockets[0].close()
  transport.stop('simulate disconnect before durable event')
  await transport.start()
  sockets[1].open()

  assert.equal(resumes[1].endpoint_cursor, 'C0')
  assert.equal(resumes[1].subscriptions?.[0]?.session_id, 'session-new')
  assert.equal(resumes[1].subscriptions?.[0]?.endpoint_cursor, 'C0')

  sockets[1].emit({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    session_id: 'session-new',
    endpoint_cursor: 'C1',
    event: {
      id: 'event-session-created',
      session_id: 'session-new',
      event_type: 'session.created',
      seq: 1,
      payload: {},
    },
  })

  transport.stop('simulate reconnect after durable event')
  await transport.start()
  sockets[2].open()

  assert.equal(resumes[2].endpoint_cursor, 'C1')
  assert.equal(resumes[2].subscriptions?.[0]?.endpoint_cursor, 'C1')
  transport.stop()
})

test('Desktop V3 realtime transport sends one resume containing workset and known sessions', async () => {
  const resumes: SessionV3RealtimeResumeWire[] = []
  const socket = new FakeWebSocket()
  const transport = new DesktopV3RealtimeTransport({
    getEndpointCursor: () => 'v3c1.bootstrap_payload.bootstrap_signature',
    openSocket: () => socket as unknown as WebSocket,
    onResumeSent: (resume) => resumes.push(resume),
    livenessTimeoutMs: 60_000,
  })
  transport.registerWorkset({
    workset_id: 'desktop-workset',
    subscription_id: 'desktop-workset-subscription',
    selector: { kind: 'global', global: true },
    auto_subscribe_sessions: true,
  })
  transport.registerSession({
    session_id: 'session-a',
    subscription_id: 'sub-a',
    endpoint_cursor: 'session-cursor-a',
  })

  await transport.start()
  socket.open()

  assert.equal(resumes.length, 1)
  assert.equal(resumes[0].kind, 'resume')
  assert.equal(resumes[0].endpoint_cursor, 'v3c1.bootstrap_payload.bootstrap_signature')
  assert.deepEqual(resumes[0].subscriptions?.map((subscription) => subscription.session_id), ['session-a'])
  assert.deepEqual(resumes[0].worksets?.map((workset) => workset.workset_id), ['desktop-workset'])
  assert.equal('after_seq' in resumes[0], false)
  transport.stop()
})
