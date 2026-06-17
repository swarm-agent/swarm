import assert from 'node:assert/strict'
import test from 'node:test'

import { DesktopSessionV3Runtime, runtimeClientId } from './runtime'
import {
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

function makeSyncSnapshot(endpointCursor: string): SessionV3SyncSnapshot {
  return {
    snapshot: {
      rev: Number(endpointCursor.replace(/\D/g, '')) || 1,
      snapshotEndpointCursor: endpointCursor,
    },
    endpointCursor,
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
          return makeSyncSnapshot(`cursor-${capturedRequests.length}`)
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
        return makeSyncSnapshot('cursor-boot')
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
    assert.equal(syncRequests[0]?.recent?.limit, 50)
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
    assert.deepEqual(resume.worksets?.map((workset) => workset.workset_id), ['desktop-v3-runtime:global'])
    assert.equal(resume.worksets?.[0]?.auto_subscribe_sessions, true)
    assert.equal(resume.worksets?.[0]?.selector.kind, 'global')
    assert.equal(resume.worksets?.[0]?.selector.global, true)
    assert.equal(resume.worksets?.[0]?.selector.recent?.limit, 50)
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
  let syncSnapshotCalls = 0
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
        syncSnapshotCalls += 1
        return makeSyncSnapshot('cursor-1')
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
  assert.equal(syncSnapshotCalls, 1)
  assert.deepEqual(openEndpointCursors, ['cursor-1'])
  assert.equal(sockets.length, 1)
  sockets[0].open()
  assert.equal(parseResume(sockets[0]).endpoint_cursor, 'cursor-1')

  await runtime.stopRun({ sessionId: 'session-1', runId: 'run-1', targetSwarmId: 'host-swarm-id' })

  assert.equal(stopCalls, 1)
  assert.equal(syncSnapshotCalls, 1)
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

test('refresh resets the V3 socket and reopens once from the latest snapshot endpoint cursor', async () => {
  installTransportGlobals()
  const sockets: MockRealtimeSocket[] = []
  const openEndpointCursors: string[] = []
  const snapshots = [makeSyncSnapshot('cursor-1'), makeSyncSnapshot('cursor-2')]

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
  assert.deepEqual(resume.worksets?.map((workset) => workset.workset_id), ['desktop-v3-runtime:global'])
  assert.equal(runtime.diagnostics().socketState, 'open')
  runtime.shutdown()
})

test('runtime constructs resume workset from runtime config while adding known session subscriptions', async () => {
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
    assert.deepEqual(resume.worksets?.map((workset) => workset.workset_id), ['desktop-v3-runtime:global'])
    assert.equal(resume.worksets?.[0]?.auto_subscribe_sessions, true)
    assert.deepEqual(resume.subscriptions?.map((subscription) => subscription.session_id), ['session-1'])
  } finally {
    runtime.shutdown()
    MockRealtimeSocket.collect = null
    restoreWebSocket()
  }
})

test('runtime uses one canonical workspace recent scope for bootstrap and realtime resume', async () => {
  installTransportGlobals()
  const sockets: MockRealtimeSocket[] = []
  const bootstrapRequests: SessionV3StateSnapshotRequest[] = []
  const runtime = new DesktopSessionV3Runtime({
    workset: {
      workset_id: 'w1',
      selector: { kind: 'workspace' },
      workspace: { workspace_path: '/repo/a' },
      recent: { limit: 50 },
      auto_subscribe_sessions: true,
    },
    api: {
      bootstrapSessionV3Sync: async (request = {}) => {
        bootstrapRequests.push(request)
        return makeSyncSnapshot('cursor-workspace')
      },
    },
    transportOptions: {
      livenessTimeoutMs: 60_000,
      openSocket: ({ endpointCursor }) => {
        const socket = new MockRealtimeSocket(`ws://localhost/v3/realtime/stream?endpoint_cursor=${endpointCursor}`)
        sockets.push(socket)
        return socket as unknown as WebSocket
      },
    },
  })

  try {
    await runtime.boot()

    assert.equal(bootstrapRequests.length, 1)
    assert.equal(bootstrapRequests[0]?.global, false)
    assert.equal(bootstrapRequests[0]?.workspacePath, '/repo/a')
    assert.equal(bootstrapRequests[0]?.recent?.limit, 50)
    assert.equal(sockets.length, 1)
    sockets[0].open()

    const resume = parseResume(sockets[0])
    assert.equal(resume.endpoint_cursor, 'cursor-workspace')
    assert.equal(resume.worksets?.[0]?.workset_id, 'w1')
    assert.equal(resume.worksets?.[0]?.auto_subscribe_sessions, true)
    assert.equal(resume.worksets?.[0]?.selector.kind, 'workspace')
    assert.equal(resume.worksets?.[0]?.selector.workspace_path, '/repo/a')
    assert.equal(resume.worksets?.[0]?.selector.recent?.limit, 50)
  } finally {
    runtime.shutdown()
  }
})

test('runtime targeted session boot does not pair session cursor with global workset resume', async () => {
  installTransportGlobals()
  const sockets: MockRealtimeSocket[] = []
  const bootstrapRequests: SessionV3StateSnapshotRequest[] = []
  const hydrateRequests: SessionV3StateSnapshotRequest[] = []
  const runtime = new DesktopSessionV3Runtime({
    api: {
      bootstrapSessionV3Sync: async (request = {}) => {
        bootstrapRequests.push(request)
        return makeSyncSnapshot('cursor-bootstrap')
      },
      hydrateSessionV3Sync: async (request) => {
        hydrateRequests.push(request)
        return makeSyncSnapshot('cursor-session')
      },
    },
    transportOptions: {
      livenessTimeoutMs: 60_000,
      openSocket: ({ endpointCursor }) => {
        const socket = new MockRealtimeSocket(`ws://localhost/v3/realtime/stream?endpoint_cursor=${endpointCursor}`)
        sockets.push(socket)
        return socket as unknown as WebSocket
      },
    },
  })

  try {
    await runtime.boot({ sessionIds: [' session-2 ', 'session-2'] })

    assert.equal(bootstrapRequests.length, 0)
    assert.equal(hydrateRequests.length, 1)
    assert.deepEqual(hydrateRequests[0].sessionIds, ['session-2'])
    assert.equal(hydrateRequests[0].global, false)
    assert.equal(sockets.length, 1)
    sockets[0].open()

    const resume = parseResume(sockets[0])
    assert.equal(resume.endpoint_cursor, 'cursor-session')
    assert.deepEqual(resume.subscriptions?.map((subscription) => subscription.session_id), ['session-2'])
    assert.equal(resume.subscriptions?.[0]?.endpoint_cursor, 'cursor-session')
    assert.deepEqual(resume.worksets ?? [], [])
  } finally {
    runtime.shutdown()
  }
})

test('runtime targeted hydrate after session-only boot does not attach a global workset to session cursor', async () => {
  installTransportGlobals()
  const sockets: MockRealtimeSocket[] = []
  const bootstrapRequests: SessionV3StateSnapshotRequest[] = []
  const hydrateRequests: SessionV3StateSnapshotRequest[] = []
  const hydrateCursors = ['cursor-session-2', 'cursor-session-3']
  const runtime = new DesktopSessionV3Runtime({
    api: {
      bootstrapSessionV3Sync: async (request = {}) => {
        bootstrapRequests.push(request)
        return makeSyncSnapshot('cursor-bootstrap')
      },
      hydrateSessionV3Sync: async (request) => {
        hydrateRequests.push(request)
        const cursor = hydrateCursors.shift()
        assert.ok(cursor)
        return makeSyncSnapshot(cursor)
      },
    },
    transportOptions: {
      livenessTimeoutMs: 60_000,
      openSocket: ({ endpointCursor }) => {
        const socket = new MockRealtimeSocket(`ws://localhost/v3/realtime/stream?endpoint_cursor=${endpointCursor}`)
        sockets.push(socket)
        return socket as unknown as WebSocket
      },
    },
  })

  try {
    await runtime.boot({ sessionIds: ['session-2'] })

    assert.equal(bootstrapRequests.length, 0)
    assert.equal(hydrateRequests.length, 1)
    assert.equal(sockets.length, 1)
    sockets[0].open()

    const firstResume = parseResume(sockets[0])
    assert.equal(firstResume.endpoint_cursor, 'cursor-session-2')
    assert.deepEqual(firstResume.subscriptions?.map((subscription) => subscription.session_id), ['session-2'])
    assert.equal(firstResume.subscriptions?.[0]?.endpoint_cursor, 'cursor-session-2')
    assert.deepEqual(firstResume.worksets ?? [], [])

    await runtime.hydrateSessions(['session-3'])

    assert.equal(bootstrapRequests.length, 0)
    assert.equal(hydrateRequests.length, 2)
    assert.deepEqual(hydrateRequests[1].sessionIds, ['session-3'])
    assert.equal(sockets.length, 1)
    const latestResume = JSON.parse(sockets[0].sent[sockets[0].sent.length - 1] ?? '') as SessionV3RealtimeResumeWire
    assert.equal(latestResume.endpoint_cursor, 'cursor-session-2')
    assert.deepEqual(latestResume.subscriptions?.map((subscription) => subscription.session_id).sort(), ['session-2', 'session-3'])
    assert.equal(latestResume.subscriptions?.find((subscription) => subscription.session_id === 'session-3')?.endpoint_cursor, 'cursor-session-3')
    assert.deepEqual(latestResume.worksets ?? [], [])
    assert.equal(runtime.diagnostics().worksetSubscriptionCount, 0)
  } finally {
    runtime.shutdown()
  }
})

test('runtime targeted hydrate preserves workspace recent workset scope while adding session subscription', async () => {
  installTransportGlobals()
  const sockets: MockRealtimeSocket[] = []
  const hydrateRequests: SessionV3StateSnapshotRequest[] = []
  const runtime = new DesktopSessionV3Runtime({
    workset: {
      workset_id: 'w1',
      selector: { kind: 'workspace' },
      workspace: { workspace_path: '/repo/a' },
      recent: { limit: 50 },
      auto_subscribe_sessions: true,
    },
    api: {
      bootstrapSessionV3Sync: async () => makeSyncSnapshot('cursor-1'),
      hydrateSessionV3Sync: async (request) => {
        hydrateRequests.push(request)
        return makeSyncSnapshot('cursor-2')
      },
    },
    transportOptions: {
      livenessTimeoutMs: 60_000,
      openSocket: ({ endpointCursor }) => {
        const socket = new MockRealtimeSocket(`ws://localhost/v3/realtime/stream?endpoint_cursor=${endpointCursor}`)
        sockets.push(socket)
        return socket as unknown as WebSocket
      },
    },
  })

  try {
    await runtime.boot()
    assert.equal(sockets.length, 1)
    sockets[0].open()
    assert.equal(parseResume(sockets[0]).worksets?.[0]?.selector.kind, 'workspace')

    await runtime.hydrateSessions(['session-2'])

    assert.equal(hydrateRequests.length, 1)
    assert.deepEqual(hydrateRequests[0].sessionIds, ['session-2'])
    assert.equal(sockets.length, 1)
    const latestResume = JSON.parse(sockets[0].sent[sockets[0].sent.length - 1] ?? '') as SessionV3RealtimeResumeWire
    assert.equal(latestResume.endpoint_cursor, 'cursor-1')
    assert.deepEqual(latestResume.subscriptions?.map((subscription) => subscription.session_id), ['session-2'])
    assert.equal(latestResume.subscriptions?.[0]?.endpoint_cursor, 'cursor-2')
    assert.equal(latestResume.worksets?.[0]?.workset_id, 'w1')
    assert.equal(latestResume.worksets?.[0]?.selector.kind, 'workspace')
    assert.equal(latestResume.worksets?.[0]?.selector.workspace_path, '/repo/a')
    assert.equal(latestResume.worksets?.[0]?.selector.recent?.limit, 50)
  } finally {
    runtime.shutdown()
  }
})

test('runtime targeted hydrate uses sync hydrate and updates existing realtime resume without opening a second socket', async () => {
  installTransportGlobals()
  const sockets: MockRealtimeSocket[] = []
  const bootstrapRequests: SessionV3StateSnapshotRequest[] = []
  const hydrateRequests: SessionV3StateSnapshotRequest[] = []
  const runtime = new DesktopSessionV3Runtime({
    api: {
      bootstrapSessionV3Sync: async (request = {}) => {
        bootstrapRequests.push(request)
        return makeSyncSnapshot('cursor-1')
      },
      hydrateSessionV3Sync: async (request) => {
        hydrateRequests.push(request)
        return makeSyncSnapshot('cursor-2')
      },
    },
    transportOptions: {
      livenessTimeoutMs: 60_000,
      openSocket: ({ endpointCursor }) => {
        const socket = new MockRealtimeSocket(`ws://localhost/v3/realtime/stream?endpoint_cursor=${endpointCursor}`)
        sockets.push(socket)
        return socket as unknown as WebSocket
      },
    },
  })

  try {
    await runtime.boot()
    assert.deepEqual(bootstrapRequests.map((request) => request.global), [true])
    assert.equal(hydrateRequests.length, 0)
    assert.equal(sockets.length, 1)
    sockets[0].open()
    assert.equal(parseResume(sockets[0]).endpoint_cursor, 'cursor-1')

    await runtime.hydrateSessions([' session-2 ', 'session-2'])

    assert.equal(bootstrapRequests.length, 1)
    assert.equal(hydrateRequests.length, 1)
    assert.deepEqual(hydrateRequests[0].sessionIds, ['session-2'])
    assert.equal(hydrateRequests[0].global, false)
    assert.equal(sockets.length, 1)
    assert.equal(sockets[0].closed, false)
    const latestResume = JSON.parse(sockets[0].sent[sockets[0].sent.length - 1] ?? '') as SessionV3RealtimeResumeWire
    assert.equal(latestResume.endpoint_cursor, 'cursor-1')
    assert.deepEqual(latestResume.subscriptions?.map((subscription) => subscription.session_id), ['session-2'])
    assert.equal(latestResume.subscriptions?.[0]?.endpoint_cursor, 'cursor-2')
    assert.deepEqual(latestResume.worksets?.map((workset) => workset.workset_id), ['desktop-v3-runtime:global'])
    assert.equal(latestResume.worksets?.[0]?.selector.kind, 'global')
    assert.equal(latestResume.worksets?.[0]?.selector.global, true)
    assert.equal(latestResume.worksets?.[0]?.selector.recent?.limit, 50)
  } finally {
    runtime.shutdown()
  }
})
