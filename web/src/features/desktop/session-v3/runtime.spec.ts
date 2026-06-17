import assert from 'node:assert/strict'
import test from 'node:test'

import { DesktopSessionV3Runtime, runtimeClientId } from './runtime'
import {
  SESSION_V3_REALTIME_PROTOCOL,
  SESSION_V3_REALTIME_PROTOCOL_VERSION,
  SESSION_V3_REALTIME_RESUME_KIND,
  type SessionV3RealtimeResumeWire,
  type SessionV3StateSnapshotRequest,
  type SessionV3SyncSnapshot,
  type SessionV3RunStopResponseWire,
} from './types'

class MockRealtimeSocket {
  static collect: MockRealtimeSocket[] | null = null

  readyState = MockRealtimeSocket.CONNECTING
  sent: string[] = []
  closed = false
  readonly url: string
  private readonly listeners = new Map<string, Array<() => void>>()

  constructor(url?: string | URL) {
    this.url = url instanceof URL ? url.toString() : String(url ?? '')
    MockRealtimeSocket.collect?.push(this)
  }

  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3

  addEventListener(type: string, listener: () => void): void {
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

  private emit(type: string): void {
    for (const listener of this.listeners.get(type) ?? []) {
      listener()
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

function installMockRealtimeSocketConstructor(): () => void {
  const target = globalThis as typeof globalThis & { WebSocket?: unknown }
  const previous = Object.getOwnPropertyDescriptor(target, 'WebSocket')
  Object.defineProperty(target, 'WebSocket', { value: MockRealtimeSocket, configurable: true })
  return () => {
    if (previous) {
      Object.defineProperty(target, 'WebSocket', previous)
    } else {
      delete target.WebSocket
    }
  }
}

function installMemoryLocalStorage(initial: Record<string, string> = {}): () => void {
  const target = globalThis as typeof globalThis & { localStorage?: Storage }
  const previous = Object.getOwnPropertyDescriptor(target, 'localStorage')
  const data = new Map<string, string>(Object.entries(initial))
  const storage = {
    get length() { return data.size },
    clear: () => { data.clear() },
    getItem: (key: string) => data.get(key) ?? null,
    key: (index: number) => Array.from(data.keys())[index] ?? null,
    removeItem: (key: string) => { data.delete(key) },
    setItem: (key: string, value: string) => { data.set(key, String(value)) },
  } satisfies Storage
  Object.defineProperty(target, 'localStorage', { value: storage, configurable: true })
  return () => {
    if (previous) {
      Object.defineProperty(target, 'localStorage', previous)
    } else {
      delete target.localStorage
    }
  }
}

function makeResume(endpointCursor: string): SessionV3RealtimeResumeWire {
  return {
    protocol: SESSION_V3_REALTIME_PROTOCOL,
    protocol_version: SESSION_V3_REALTIME_PROTOCOL_VERSION,
    kind: SESSION_V3_REALTIME_RESUME_KIND,
    endpoint_cursor: endpointCursor,
    subscriptions: [],
    worksets: [
      {
        workset_id: 'workset-1',
        subscription_id: 'workset-sub-1',
        selector: { kind: 'global', global: true },
        auto_subscribe_sessions: true,
      },
    ],
  }
}

function makeReconnectSnapshot(endpointCursor: string): SessionV3SyncSnapshot {
  return {
    snapshot: {
      rev: Number(endpointCursor.replace(/\D/g, '')) || 1,
      snapshotEndpointCursor: endpointCursor,
    },
    endpointCursor,
    subscriptions: [],
    worksets: [
      {
        workset_id: 'workset-1',
        subscription_id: 'workset-sub-1',
        selector: { kind: 'global', global: true },
        auto_subscribe_sessions: true,
      },
    ],
    realtimeResume: makeResume(endpointCursor),
    wire: { ok: true, rev: 1, snapshot_endpoint_cursor: endpointCursor },
  }
}

function parseResume(socket: MockRealtimeSocket): SessionV3RealtimeResumeWire {
  assert.equal(socket.sent.length, 1)
  return JSON.parse(socket.sent[0]) as SessionV3RealtimeResumeWire
}

test('runtimeClientId persists a stable desktop V3 client id', () => {
  const restore = installMemoryLocalStorage()
  try {
    const first = runtimeClientId()
    const second = runtimeClientId()
    assert.equal(second, first)
    assert.match(first, /^desktop-v3-runtime:/)
  } finally {
    restore()
  }
})

test('runtime boot uses bootstrap sync API and preserves explicit client id locally', async () => {
  installTransportGlobals()
  const restore = installMemoryLocalStorage()
  try {
    const capturedRequests: SessionV3StateSnapshotRequest[] = []
    const makeRuntime = (explicitClientId?: string) => new DesktopSessionV3Runtime({
      clientId: explicitClientId,
      api: {
        bootstrapSessionV3Sync: async (input = {}) => {
          capturedRequests.push(input)
          return makeReconnectSnapshot(`cursor-${capturedRequests.length}`)
        },
      },
      transportOptions: {
        livenessTimeoutMs: 60_000,
        openSocket: () => new MockRealtimeSocket() as unknown as WebSocket,
      },
    })

    const first = makeRuntime()
    await first.boot()
    first.shutdown()

    const second = makeRuntime()
    await second.boot()
    second.shutdown()

    const explicit = makeRuntime(' desktop-v3-runtime:explicit-client ')
    await explicit.boot()
    explicit.shutdown()

    assert.equal(capturedRequests.length, 3)
    assert.equal(capturedRequests[0]?.global, true)
    assert.equal(capturedRequests[1]?.global, true)
    assert.equal(capturedRequests[2]?.global, true)
    assert.equal(runtimeClientId(), 'desktop-v3-runtime:explicit-client')
  } finally {
    restore()
  }
})

test('runtime boot opens one V3 realtime stream and sends one resume for workset plus known sessions', async () => {
  installTransportGlobals()
  const sockets: MockRealtimeSocket[] = []
  MockRealtimeSocket.collect = sockets
  const restoreWebSocket = installMockRealtimeSocketConstructor()
  const syncRequests: SessionV3StateSnapshotRequest[] = []
  const runtime = new DesktopSessionV3Runtime({
    clientId: 'client-boot',
    wantedSessionIds: ['session-1'],
    api: {
      bootstrapSessionV3Sync: async (options = {}) => {
        syncRequests.push(options)
        return makeReconnectSnapshot('cursor-boot')
      },
    },
    transportOptions: {
      livenessTimeoutMs: 60_000,
    },
  })

  try {
    await runtime.boot()

    assert.equal(syncRequests.length, 1)
    assert.equal(syncRequests[0]?.global, true)
    assert.deepEqual(syncRequests[0]?.resources, {
      messages: true,
      events: true,
      runIntents: true,
      activePlan: true,
      planRevisions: true,
    })
    assert.equal(Object.prototype.hasOwnProperty.call(syncRequests[0]?.resources ?? {}, 'plans'), false)
    assert.equal(Object.prototype.hasOwnProperty.call(syncRequests[0]?.resources ?? {}, 'permissions'), false)
    assert.equal(Object.prototype.hasOwnProperty.call(syncRequests[0]?.resources ?? {}, 'usage'), false)
    assert.equal(Object.prototype.hasOwnProperty.call(syncRequests[0]?.resources ?? {}, 'preferences'), false)
    assert.equal(Object.prototype.hasOwnProperty.call(syncRequests[0]?.resources ?? {}, 'agentModelPolicy'), false)
    assert.equal(sockets.length, 1)
    const socketUrl = new URL(sockets[0].url)
    assert.equal(socketUrl.pathname, '/v3/realtime/stream')
    assert.equal(socketUrl.searchParams.get('endpoint_cursor'), 'cursor-boot')
    assert.deepEqual(Array.from(socketUrl.searchParams.keys()), ['endpoint_cursor'])
    assert.equal(sockets[0].sent.length, 0)

    sockets[0].open()

    assert.equal(sockets.length, 1)
    const resume = parseResume(sockets[0])
    assert.equal(resume.endpoint_cursor, 'cursor-boot')
    assert.deepEqual(resume.subscriptions?.map((subscription) => subscription.session_id), ['session-1'])
    assert.deepEqual(resume.worksets?.map((workset) => workset.workset_id), ['workset-1'])
    assert.equal(resume.worksets?.[0]?.auto_subscribe_sessions, true)
    assert.equal(runtime.diagnostics().socketState, 'open')
  } finally {
    runtime.shutdown()
    MockRealtimeSocket.collect = null
    restoreWebSocket()
  }
})

test('stopRun applies mutation outbox without reconnecting or opening another V3 socket', async () => {
  installTransportGlobals()
  const sockets: MockRealtimeSocket[] = []
  const openEndpointCursors: string[] = []
  let reconnectCalls = 0
  let stopCalls = 0
  const stopResponse: SessionV3RunStopResponseWire = {
    ok: true,
    session_id: 'session-1',
    run_id: 'run-1',
    status: 'cancelled',
    mutation: {
      realtime_outbox: {
        endpoint_seq: 2,
        endpoint_cursor: 'cursor-2',
        session_id: 'session-1',
      },
    },
  }

  const runtime = new DesktopSessionV3Runtime({
    wantedSessionIds: ['session-1'],
    api: {
      bootstrapSessionV3Sync: async () => {
        reconnectCalls += 1
        return makeReconnectSnapshot('cursor-1')
      },
      stopSessionV3Run: async (sessionId, input) => {
        stopCalls += 1
        assert.equal(sessionId, 'session-1')
        assert.equal(input.runId, 'run-1')
        return stopResponse
      },
    },
    transportOptions: {
      livenessTimeoutMs: 60_000,
      openSocket: ({ endpointCursor }) => {
        openEndpointCursors.push(endpointCursor)
        const socket = new MockRealtimeSocket()
        sockets.push(socket)
        return socket as unknown as WebSocket
      },
    },
  })

  await runtime.boot()
  assert.equal(reconnectCalls, 1)
  assert.deepEqual(openEndpointCursors, ['cursor-1'])
  assert.equal(sockets.length, 1)
  sockets[0].open()
  assert.equal(parseResume(sockets[0]).endpoint_cursor, 'cursor-1')

  await runtime.stopRun({ sessionId: 'session-1', runId: 'run-1', targetSwarmId: 'host-swarm-id' })

  assert.equal(stopCalls, 1)
  assert.equal(reconnectCalls, 1)
  assert.deepEqual(openEndpointCursors, ['cursor-1'])
  assert.equal(sockets.length, 1)
  assert.equal(sockets[0].closed, false)
  assert.equal(runtime.getState().endpointCursor, 'cursor-2')
  const diagnostics = runtime.diagnostics()
  assert.equal(diagnostics.socketState, 'open')
  assert.equal(diagnostics.sessionSubscriptionCount, 1)
  const latestResume = JSON.parse(sockets[0].sent[sockets[0].sent.length - 1] ?? '') as SessionV3RealtimeResumeWire
  assert.equal(latestResume.endpoint_cursor, 'cursor-2')
  assert.deepEqual(latestResume.subscriptions?.map((subscription) => subscription.session_id), ['session-1'])
  runtime.shutdown()
})

test('refresh resets the V3 socket and reconnects once from the latest snapshot endpoint cursor', async () => {
  installTransportGlobals()
  const sockets: MockRealtimeSocket[] = []
  const openEndpointCursors: string[] = []
  const snapshots = [makeReconnectSnapshot('cursor-1'), makeReconnectSnapshot('cursor-2')]

  const runtime = new DesktopSessionV3Runtime({
    wantedSessionIds: ['session-1'],
    api: {
      bootstrapSessionV3Sync: async () => {
        const snapshot = snapshots.shift()
        assert.ok(snapshot)
        return snapshot
      },
    },
    transportOptions: {
      livenessTimeoutMs: 60_000,
      openSocket: ({ endpointCursor }) => {
        openEndpointCursors.push(endpointCursor)
        const socket = new MockRealtimeSocket()
        sockets.push(socket)
        return socket as unknown as WebSocket
      },
    },
  })

  await runtime.boot()
  assert.deepEqual(openEndpointCursors, ['cursor-1'])
  assert.equal(sockets.length, 1)
  sockets[0].open()
  assert.equal(parseResume(sockets[0]).endpoint_cursor, 'cursor-1')

  await runtime.refresh({ reason: 'forced refresh' })

  assert.equal(sockets[0].closed, true)
  assert.equal(sockets[0].sent.length, 1)
  assert.deepEqual(openEndpointCursors, ['cursor-1', 'cursor-2'])
  assert.equal(sockets.length, 2)
  assert.equal(runtime.diagnostics().socketState, 'connecting')

  sockets[1].open()
  assert.equal(sockets[1].sent.length, 1)
  const resume = parseResume(sockets[1])
  assert.equal(resume.endpoint_cursor, 'cursor-2')
  assert.deepEqual(resume.subscriptions?.map((subscription) => subscription.session_id), ['session-1'])
  assert.deepEqual(resume.worksets?.map((workset) => workset.workset_id), ['workset-1'])
  assert.equal(runtime.diagnostics().socketState, 'open')
  runtime.shutdown()
})

test('runtime resume preserves backend worksets while adding known session subscriptions', async () => {
  installTransportGlobals()
  const sockets: MockRealtimeSocket[] = []
  MockRealtimeSocket.collect = sockets
  const restoreWebSocket = installMockRealtimeSocketConstructor()
  const runtime = new DesktopSessionV3Runtime({
    wantedSessionIds: ['session-1'],
    api: {
      bootstrapSessionV3Sync: async () => ({
        snapshot: {
          rev: 10,
          snapshotEndpointCursor: 'cursor-10',
        },
        endpointCursor: 'cursor-10',
        subscriptions: [],
        worksets: [],
        realtimeResume: {
          protocol: SESSION_V3_REALTIME_PROTOCOL,
          protocol_version: SESSION_V3_REALTIME_PROTOCOL_VERSION,
          kind: SESSION_V3_REALTIME_RESUME_KIND,
          endpoint_cursor: 'cursor-10',
          subscriptions: [],
          worksets: [
            {
              workset_id: 'workset-1',
              subscription_id: 'workset-sub-1',
              selector: { kind: 'global', global: true },
              auto_subscribe_sessions: true,
            },
          ],
        },
            wire: { ok: true, rev: 10, snapshot_endpoint_cursor: 'cursor-10' },
      }),
    },
    transportOptions: {
      livenessTimeoutMs: 60_000,
    },
  })

  try {
    await runtime.boot()
    assert.equal(sockets.length, 1)
    sockets[0].open()

    const resume = parseResume(sockets[0])
    assert.equal(resume.endpoint_cursor, 'cursor-10')
    assert.deepEqual(resume.worksets?.map((workset) => workset.workset_id), ['workset-1'])
    assert.equal(resume.worksets?.[0]?.auto_subscribe_sessions, true)
    assert.deepEqual(resume.subscriptions?.map((subscription) => subscription.session_id), ['session-1'])
  } finally {
    runtime.shutdown()
    MockRealtimeSocket.collect = null
    restoreWebSocket()
  }
})
