import assert from 'node:assert/strict'
import test from 'node:test'

import { getDesktopSnapshot, replaceDesktopFromSnapshot } from './desktop-state-store'
import { startDesktopStateStream } from './desktop-state-stream'
import type { DesktopDaemonSnapshot } from './desktop-state'

interface V3RealtimeSocketOptions {
  afterRev?: number
}

class MockDesktopSocket {
  closed = false
  private readonly messageListeners: Array<(event: MessageEvent) => void> = []
  private readonly eventListeners: Array<(event: Event) => void> = []

  addEventListener(type: 'message', listener: (event: MessageEvent) => void): void
  addEventListener(type: 'error' | 'close', listener: (event: Event) => void): void
  addEventListener(type: 'message' | 'error' | 'close', listener: ((event: MessageEvent) => void) | ((event: Event) => void)): void {
    if (type === 'message') {
      this.messageListeners.push(listener as (event: MessageEvent) => void)
      return
    }
    this.eventListeners.push(listener as (event: Event) => void)
  }

  send(raw: unknown): void {
    for (const listener of this.messageListeners) {
      listener({ data: raw } as MessageEvent)
    }
  }

  emitEvent(): void {
    for (const listener of this.eventListeners) {
      listener({} as Event)
    }
  }

  close(): void {
    this.closed = true
  }
}

interface StreamHarness {
  sockets: MockDesktopSocket[]
  openCalls: V3RealtimeSocketOptions[]
  start(snapshots: DesktopDaemonSnapshot[], queueLimit?: number): ReturnType<typeof startDesktopStateStream>
}

function createHarness(): StreamHarness {
  const sockets: MockDesktopSocket[] = []
  const openCalls: V3RealtimeSocketOptions[] = []

  return {
    sockets,
    openCalls,
    start(snapshots: DesktopDaemonSnapshot[], queueLimit?: number) {
      let snapshotIndex = 0
      return startDesktopStateStream({
        queueLimit,
        fetchSnapshot: async () => snapshots[Math.min(snapshotIndex++, snapshots.length - 1)],
        openSocket: async (options) => {
          openCalls.push(options)
          const socket = new MockDesktopSocket()
          sockets.push(socket)
          return socket
        },
      })
    },
  }
}

async function flushStreamLifecycle(): Promise<void> {
  await Promise.resolve()
  await Promise.resolve()
  await new Promise<void>((resolve) => setTimeout(resolve, 0))
}

function realtimeEvent(rev: number, prevRev: number, payload: Record<string, unknown>): string {
  return JSON.stringify({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    rev,
    prevRev,
    event_type: 'desktop/session/upsert',
    event: { payload },
  })
}

test('stream boots from snapshot first and applies events only through reducer revisions', async () => {
  replaceDesktopFromSnapshot({ rev: 0 })
  const harness = createHarness()
  const handle = await harness.start([{ rev: 4 }])

  assert.equal(getDesktopSnapshot().rev, 4)
  assert.deepEqual(harness.openCalls, [{ afterRev: 4 }])

  harness.sockets[0].send(realtimeEvent(5, 4, { session: { id: 'session-1', updatedAt: 10, createdAt: 10 } }))
  await flushStreamLifecycle()

  assert.equal(getDesktopSnapshot().rev, 5)
  assert.equal(getDesktopSnapshot().sessionsById['session-1']?.id, 'session-1')
  handle.close()
})

test('bounded stream queue overflow closes stale socket and resyncs from a replacement snapshot', async () => {
  replaceDesktopFromSnapshot({ rev: 0 })
  const harness = createHarness()
  const handle = await harness.start([{ rev: 1 }, { rev: 10 }], 1)
  const staleSocket = harness.sockets[0]

  staleSocket.send(realtimeEvent(2, 1, { session: { id: 'queued', updatedAt: 10, createdAt: 10 } }))
  staleSocket.send(realtimeEvent(3, 2, { session: { id: 'overflow', updatedAt: 11, createdAt: 11 } }))
  await flushStreamLifecycle()

  assert.equal(staleSocket.closed, true)
  assert.equal(getDesktopSnapshot().rev, 10)
  assert.equal(getDesktopSnapshot().status, 'ready')
  assert.equal(getDesktopSnapshot().sessionsById.queued, undefined)
  assert.equal(getDesktopSnapshot().sessionsById.overflow, undefined)
  assert.deepEqual(harness.openCalls, [{ afterRev: 1 }, { afterRev: 10 }])
  handle.close()
})

test('resync control frames close the current socket and reload the snapshot', async () => {
  replaceDesktopFromSnapshot({ rev: 0 })
  const harness = createHarness()
  const handle = await harness.start([{ rev: 2 }, { rev: 8 }])
  const staleSocket = harness.sockets[0]

  staleSocket.send(JSON.stringify({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'slow_consumer.reconnect_required',
    error_code: 'slow_consumer',
    reason: 'client fell behind',
  }))
  await flushStreamLifecycle()

  assert.equal(staleSocket.closed, true)
  assert.equal(getDesktopSnapshot().rev, 8)
  assert.equal(getDesktopSnapshot().status, 'ready')
  assert.equal(harness.sockets.length, 2)
  assert.deepEqual(harness.openCalls, [{ afterRev: 2 }, { afterRev: 8 }])
  handle.close()
})

test('prevRev mismatch marks stale, closes the socket, and ignores further messages until snapshot reload', async () => {
  replaceDesktopFromSnapshot({ rev: 0 })
  const harness = createHarness()
  const handle = await harness.start([{ rev: 5 }, { rev: 20 }])
  const staleSocket = harness.sockets[0]

  staleSocket.send(realtimeEvent(7, 1, { session: { id: 'bad', updatedAt: 10, createdAt: 10 } }))
  await flushStreamLifecycle()
  staleSocket.send(realtimeEvent(21, 20, { session: { id: 'ignored', updatedAt: 12, createdAt: 12 } }))
  await flushStreamLifecycle()

  assert.equal(staleSocket.closed, true)
  assert.equal(getDesktopSnapshot().rev, 20)
  assert.equal(getDesktopSnapshot().sessionsById.bad, undefined)
  assert.equal(getDesktopSnapshot().sessionsById.ignored, undefined)
  assert.deepEqual(harness.openCalls, [{ afterRev: 5 }, { afterRev: 20 }])
  handle.close()
})
