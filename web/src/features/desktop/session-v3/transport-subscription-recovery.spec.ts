import assert from 'node:assert/strict'
import test from 'node:test'
import { setImmediate } from 'node:timers/promises'

import { DesktopV3RealtimeTransport, SESSION_CONNECT_ACK_TIMEOUT_MS } from './transport'

// Requirement: a session demand must receive replay.complete before it is
// considered live. Threat: a silently unacknowledged subscribe remains in the
// transport registry, so controller reconciliation thinks it is subscribed and
// Orchestrator output stops until refresh. DesktopV3RealtimeTransport's
// pending subscription, replay acknowledgement and rehydrate boundary are the
// narrowest layer that can reproduce a missing acknowledgement without a daemon.
class Socket extends EventTarget {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3
  readyState = Socket.CONNECTING
  sent: Array<Record<string, unknown>> = []
  send(raw: string) { this.sent.push(JSON.parse(raw)) }
  close() { this.readyState = Socket.CLOSED; this.dispatchEvent(new Event('close')) }
  open() { this.readyState = Socket.OPEN; this.dispatchEvent(new Event('open')) }
  frame(frame: Record<string, unknown>) {
    const event = new Event('message')
    Object.defineProperty(event, 'data', { value: JSON.stringify({ protocol: 'v3.realtime', protocol_version: 1, ...frame }) })
    this.dispatchEvent(event)
  }
}

function setup(t: import('node:test').TestContext) {
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalWebSocket = Object.getOwnPropertyDescriptor(globalThis, 'WebSocket')
  let now = 0
  let timerId = 0
  const timers = new Map<number, { due: number; callback: () => void }>()
  Object.defineProperty(globalThis, 'window', { configurable: true, value: {
    setTimeout(callback: () => void, delay: number) {
      const id = ++timerId
      timers.set(id, { due: now + delay, callback })
      return id
    },
    clearTimeout(id: number) { timers.delete(id) },
  } })
  Object.defineProperty(globalThis, 'WebSocket', { configurable: true, value: Socket })
  const sockets: Socket[] = []
  const recoveries: string[] = []
  const frames: string[] = []
  let failRecovery = false
  const transport = new DesktopV3RealtimeTransport({
    getEndpointCursor: () => 'committed-cursor',
    now: () => now,
    openSocket: () => {
      const socket = new Socket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
    onFrame: ({ frame }) => { if (frame.kind === 'event') frames.push(String(frame.event?.event_seq)) },
    onRehydrateRequested: (reason) => {
      recoveries.push(reason)
      if (failRecovery) throw new Error('durable snapshot unavailable')
      return {
        endpointCursor: 'recovered-cursor',
        subscriptions: [{ session_id: 'orchestrator', subscription_id: 'view:orchestrator', endpoint_cursor: 'committed-cursor' }],
      }
    },
  })
  t.after(() => {
    transport.stop()
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
    if (originalWebSocket) Object.defineProperty(globalThis, 'WebSocket', originalWebSocket)
    else Reflect.deleteProperty(globalThis, 'WebSocket')
  })
  return {
    transport, sockets, recoveries, frames, timers,
    fail() { failRecovery = true },
    async advance(ms: number) {
      now += ms
      for (const [id, timer] of [...timers]) {
        if (timer.due <= now && timers.delete(id)) timer.callback()
      }
      await setImmediate()
    },
  }
}

test('missing session replay acknowledgement recovers from durable cursor without refresh or duplicate subscription', { timeout: 2_000 }, async (t) => {
  const h = setup(t)
  await h.transport.start()
  h.sockets[0].open()
  const pending = h.transport.subscribeSession({ session_id: 'orchestrator', subscription_id: 'view:orchestrator', endpoint_cursor: 'committed-cursor' })
  assert.equal(h.sockets[0].sent.filter((frame) => frame.kind === 'subscribe.session').length, 1)
  const rejected = assert.rejects(pending, /acknowledgement timed out/)
  await h.advance(SESSION_CONNECT_ACK_TIMEOUT_MS)
  await rejected
  assert.equal(h.recoveries.length, 1)
  assert.equal(h.sockets.length, 2)
  h.sockets[1].open()
  const resumes = h.sockets[1].sent.filter((frame) => frame.kind === 'resume')
  assert.equal(resumes.length, 1)
  assert.equal((resumes[0].subscriptions as Array<Record<string, unknown>>)[0].endpoint_cursor, 'committed-cursor')
  h.sockets[1].frame({ kind: 'event', session_id: 'orchestrator', endpoint_cursor: 'event-cursor', event: { session_id: 'orchestrator', event_seq: 1 } })
  h.sockets[1].frame({ kind: 'replay.complete', session_id: 'orchestrator', subscription_id: 'view:orchestrator' })
  await setImmediate()
  assert.deepEqual(h.frames, ['1'])
  assert.equal(h.transport.diagnostics().sessionSubscriptionCount, 1)
  assert.equal(h.recoveries.length, 1)
  h.transport.stop()
  assert.equal(h.timers.size, 0)
})

test('acknowledged session never starts recovery; failed durable recovery stays stale, without retries', { timeout: 2_000 }, async (t) => {
  const h = setup(t)
  await h.transport.start()
  h.sockets[0].open()
  const acknowledged = h.transport.subscribeSession({ session_id: 'orchestrator', subscription_id: 'view:orchestrator', endpoint_cursor: 'committed-cursor' })
  h.sockets[0].frame({ kind: 'replay.complete', session_id: 'orchestrator', subscription_id: 'view:orchestrator' })
  await acknowledged
  await h.advance(SESSION_CONNECT_ACK_TIMEOUT_MS)
  assert.deepEqual(h.recoveries, [])
  h.sockets[0].frame({ kind: 'keepalive', endpoint_cursor: 'committed-cursor' })
  h.transport.unsubscribeSession('orchestrator')
  const missing = h.transport.subscribeSession({ session_id: 'orchestrator', subscription_id: 'view:orchestrator', endpoint_cursor: 'committed-cursor' })
  h.fail()
  const rejected = assert.rejects(missing, /acknowledgement timed out/)
  await h.advance(SESSION_CONNECT_ACK_TIMEOUT_MS)
  await rejected
  assert.equal(h.transport.diagnostics().status, 'stale')
  assert.equal(h.transport.diagnostics().desired, false)
  assert.equal(h.recoveries.length, 1)
  assert.equal(h.timers.size, 0)
  await h.advance(SESSION_CONNECT_ACK_TIMEOUT_MS * 2)
  assert.equal(h.sockets.length, 1)
  assert.equal(h.recoveries.length, 1)
})
