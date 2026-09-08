import assert from 'node:assert/strict'
import test, { type TestContext } from 'node:test'
import { setImmediate } from 'node:timers/promises'

import { DesktopV3RealtimeTransport } from './transport'

// Requirement: only the owned socket generation may change transport liveness.
// Threat: asynchronous close completion from a replaced socket cancels the new
// socket's inactivity recovery and leaves a silent connection appearing open.
// Authority: DesktopV3RealtimeTransport.attachSocket, requestRehydrate,
// reopenFromDurableCursor, armLiveness and stop. This transport-level harness is
// the narrowest layer that exercises real recovery/frame handling without a
// browser, server, credentials or private timer-field inspection.
class DelayedCloseSocket extends EventTarget {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3
  readyState = DelayedCloseSocket.CONNECTING
  sent: Array<{ kind: string; endpoint_cursor: string }> = []

  send(payload: string): void {
    this.sent.push(JSON.parse(payload))
  }

  close(): void {
    if (this.readyState !== DelayedCloseSocket.CLOSED) {
      this.readyState = DelayedCloseSocket.CLOSING
    }
  }

  finishClose(): void {
    this.readyState = DelayedCloseSocket.CLOSED
    this.dispatchEvent(new Event('close'))
  }

  open(): void {
    this.readyState = DelayedCloseSocket.OPEN
    this.dispatchEvent(new Event('open'))
  }

  emit(kind: string): void {
    const event = new Event('message')
    Object.defineProperty(event, 'data', {
      value: JSON.stringify({ protocol: 'v3.realtime', protocol_version: 1, kind }),
    })
    this.dispatchEvent(event)
  }
}

function harness(t: TestContext) {
  let now = 0
  let nextId = 0
  const timers = new Map<number, { due: number; callback: () => void }>()
  const sockets: DelayedCloseSocket[] = []
  const recoveryReasons: string[] = []
  let failRecovery = false
  const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window')
  const originalWebSocket = Object.getOwnPropertyDescriptor(globalThis, 'WebSocket')
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      setTimeout(callback: () => void, delay: number) {
        const id = ++nextId
        timers.set(id, { due: now + delay, callback })
        return id
      },
      clearTimeout(id: number) { timers.delete(id) },
    },
  })
  Object.defineProperty(globalThis, 'WebSocket', { configurable: true, value: DelayedCloseSocket })
  const transport = new DesktopV3RealtimeTransport({
    getEndpointCursor: () => 'cursor-initial',
    now: () => now,
    openSocket: () => {
      const socket = new DelayedCloseSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
    onRehydrateRequested: (reason) => {
      recoveryReasons.push(reason)
      if (failRecovery) throw new Error('snapshot unavailable')
      return { endpointCursor: 'cursor-recovered' }
    },
    // Use production inactivity and reopen delays; time is virtual, not slept.
  })
  t.after(() => {
    transport.stop()
    if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow)
    else Reflect.deleteProperty(globalThis, 'window')
    if (originalWebSocket) Object.defineProperty(globalThis, 'WebSocket', originalWebSocket)
    else Reflect.deleteProperty(globalThis, 'WebSocket')
  })
  return {
    transport, sockets, timers, recoveryReasons,
    failRecovery() { failRecovery = true },
    async advance(ms: number) {
      now += ms
      // Snapshot due callbacks: newly armed timers run only on the next advance.
      for (const [id, timer] of [...timers]) {
        if (timer.due <= now && timers.delete(id)) timer.callback()
      }
      await setImmediate()
    },
  }
}

for (const recovery of ['cursor.error', 'slow_consumer.reconnect_required', 'durable-cursor reopen']) {
  test(`late close cannot disable replacement liveness after ${recovery}`, { timeout: 2_000 }, async (t) => {
    const h = harness(t)
    await h.transport.start()
    h.sockets[0].open()
    if (recovery === 'durable-cursor reopen') {
      h.transport.reopenFromDurableCursor('retry committed cursor')
    } else {
      h.sockets[0].emit(recovery)
    }
    await setImmediate()
    assert.equal(h.sockets.length, 2)
    assert.equal(h.sockets[0].readyState, DelayedCloseSocket.CLOSING)
    h.sockets[1].open()
    const beforeClose = h.transport.diagnostics()
    const timerBeforeClose = [...h.timers.keys()]
    assert.equal(timerBeforeClose.length, 1)
    const recoveriesBeforeTimeout = h.recoveryReasons.length
    assert.equal(recoveriesBeforeTimeout, recovery === 'durable-cursor reopen' ? 0 : 1)
    assert.equal(h.sockets[1].sent[0].kind, 'resume')
    assert.equal(h.sockets[1].sent[0].endpoint_cursor,
      recovery === 'durable-cursor reopen' ? 'cursor-initial' : 'cursor-recovered')

    // Browser close completion occurs AFTER replacement open; no new current
    // socket message is allowed to mask a canceled timer by rearming it.
    h.sockets[0].finishClose()
    assert.deepEqual(h.transport.diagnostics(), beforeClose)
    assert.deepEqual([...h.timers.keys()], timerBeforeClose)
    await h.advance(44_999)
    assert.equal(h.recoveryReasons.length, recoveriesBeforeTimeout)
    await h.advance(1)
    assert.equal(h.recoveryReasons.length, recoveriesBeforeTimeout + 1)
    assert.equal(h.recoveryReasons.at(-1), 'V3 realtime inactivity timeout')
    assert.equal(h.sockets[1].readyState, DelayedCloseSocket.CLOSING)
    assert.equal(h.sockets.length, 3)
    h.sockets[2].open()
    assert.equal(h.sockets[2].sent[0].endpoint_cursor, 'cursor-recovered')
    h.transport.stop()
    assert.equal(h.timers.size, 0)
    h.sockets[1].finishClose()
    h.sockets[2].finishClose()
    await h.advance(60_000)
    assert.equal(h.transport.diagnostics().status, 'stopped')
    assert.equal(h.sockets.length, 3)
    assert.equal(h.recoveryReasons.length, recoveriesBeforeTimeout + 1)
  })
}

// Control: an owned close must still cancel inactivity, reconnect once, and
// stop must cancel both the replacement's timer and any pending reconnect.
test('owned close reconnects and stop cancels liveness and scheduled reopen', { timeout: 2_000 }, async (t) => {
  const h = harness(t)
  await h.transport.start()
  h.sockets[0].open()
  h.sockets[0].finishClose()
  assert.equal(h.transport.diagnostics().reopenTimerActive, true)
  assert.equal(h.timers.size, 1)
  await h.advance(2_000) // Above production first-reopen jitter's upper bound.
  assert.equal(h.sockets.length, 2)
  h.sockets[1].open()
  assert.equal(h.sockets[1].sent[0].endpoint_cursor, 'cursor-initial')
  h.transport.stop()
  assert.equal(h.timers.size, 0)
  h.sockets[1].finishClose()
  await h.advance(60_000)
  assert.equal(h.sockets.length, 2)
  assert.deepEqual(h.recoveryReasons, [])

  await h.transport.start()
  h.sockets[2].open()
  h.sockets[2].finishClose()
  assert.equal(h.transport.diagnostics().reopenTimerActive, true)
  h.transport.stop()
  assert.equal(h.timers.size, 0)
  await h.advance(60_000)
  assert.equal(h.sockets.length, 3)
  assert.equal(h.transport.diagnostics().status, 'stopped')
  assert.deepEqual(h.recoveryReasons, [])
})

// Failure postcondition: recovery failure stays explicitly stale, with no
// residual timers or late-close-triggered reconnection posing as success.
test('inactivity recovery failure stays stale despite delayed close', { timeout: 2_000 }, async (t) => {
  const h = harness(t)
  await h.transport.start()
  h.sockets[0].open()
  h.failRecovery()
  await h.advance(45_000)
  assert.deepEqual(h.recoveryReasons, ['V3 realtime inactivity timeout'])
  assert.equal(h.transport.diagnostics().status, 'stale')
  assert.equal(h.transport.diagnostics().desired, false)
  assert.equal(h.timers.size, 0)
  h.sockets[0].finishClose()
  await h.advance(60_000)
  assert.equal(h.transport.diagnostics().status, 'stale')
  assert.equal(h.sockets.length, 1)
  assert.equal(h.timers.size, 0)
  assert.equal(h.recoveryReasons.length, 1)
})
