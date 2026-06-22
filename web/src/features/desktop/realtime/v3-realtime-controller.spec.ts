import 'fake-indexeddb/auto'

import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

import { DesktopV3RealtimeTransport } from '../session-v3/transport'
import { createDesktopV3CacheOwner } from '../state/desktop-v3-cache-owner'
import { selectRenderedSessionMessages } from '../state/desktop-v3-cache-selectors'
import { readDesktopV3Owner, resetDesktopV3CacheDBForTests } from '../state/desktop-v3-cache-db'
import {
  DesktopV3RealtimeControllerRuntime,
  DesktopV3StreamCommitController,
  buildDesktopV3ReconnectInput,
  mapTransportStatus,
  requireDesktopV3RealtimeControllerReady,
  resetDesktopV3RealtimeControllerForTests,
  retainDesktopV3RealtimeController,
  setDesktopV3RealtimeControllerFactoryForTests,
} from './v3-realtime-controller'
import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from '../state/desktop-v3-cache-reducer'
import { hydrateSnapshotFixture, messageA1, messageA2, projectionA, projectionB, reconnectFixture, runIntentA, sessionA, sessionB } from '../state/desktop-v3-cache.backend-fixtures'
import type { SessionV3RealtimeResumeWire } from '../session-v3/types'
import type { DesktopV3CacheAction, DesktopV3CacheState, RealtimeMessage, V3SessionEvent } from '../state/desktop-v3-cache-types'

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

test('Desktop V3 realtime transport sends subscribe.session and waits for replay.complete', async () => {
  const socket = new FakeWebSocket()
  const transport = new DesktopV3RealtimeTransport({
    getEndpointCursor: () => 'C0',
    openSocket: () => socket as unknown as WebSocket,
    livenessTimeoutMs: 60_000,
  })

  await transport.start()
  socket.open()
  const ready = transport.registerSessionAndWait({
    session_id: 'session-a',
    subscription_id: 'sub-a',
    endpoint_cursor: 'stale-session-cursor',
  })
  await Promise.resolve()

  assert.deepEqual(socket.sent.at(-1), {
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'subscribe.session',
    session_id: 'session-a',
    subscription_id: 'sub-a',
    endpoint_cursor: 'C0',
  })

  socket.emit({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'replay.complete',
    session_id: 'session-a',
    subscription_id: 'sub-a',
    endpoint_cursor: 'C1',
  })
  await ready
  transport.stop()
})

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

  const latestSubscribe = socket.sent.at(-1) as Record<string, unknown>
  assert.equal(latestSubscribe.kind, 'subscribe.session')
  assert.equal(latestSubscribe.endpoint_cursor, 'v3c1.test_payload_2.test_signature_2')
  assert.equal(latestSubscribe.session_id, 'session-a')
  assert.equal('after_seq' in latestSubscribe, false)
  assert.equal('after_rev' in latestSubscribe, false)
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

test('Desktop V3 realtime controller waits for delayed sidebar bootstrap before reconnecting', async () => {
  let state = createEmptyDesktopV3CacheState()
  const sockets: FakeWebSocket[] = []
  const hydrateRequests: unknown[] = []
  let reconnectCount = 0
  let releaseBootstrap!: () => void
  const bootstrapReady = new Promise<void>((resolve) => {
    releaseBootstrap = resolve
  })
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => true,
    reconnect: async () => {
      reconnectCount += 1
      return reconnectFixture({
        snapshot_endpoint_cursor: 'cursor-reconnect',
        sessions_by_id: { [sessionA.id]: sessionA },
        projections_by_session: { [sessionA.id]: projectionA },
        run_intents_by_session: {},
        current_run_intent_by_session: {},
        session_order: [sessionA.id],
        workset_id: 'global-scope',
        realtime: {
          stream_path: '/v3/realtime/stream',
          resume: {
            protocol: 'v3.realtime',
            protocol_version: 1,
            kind: 'resume',
            endpoint_cursor: 'cursor-reconnect',
            subscriptions: [{ subscription_id: 'sub-a', session_id: sessionA.id }],
            worksets: [{
              workset_id: 'global-scope',
              subscription_id: 'workset-sub',
              selector: { kind: 'global', global: true },
              resources: ['run_intents'],
              auto_subscribe_sessions: true,
            }],
          },
        },
      })
    },
    hydrate: async (input) => {
      hydrateRequests.push(input)
      return hydrateSnapshotFixture({
        sessions_by_id: { [sessionA.id]: sessionA },
        projections_by_session: { [sessionA.id]: projectionA },
        session_order: [sessionA.id],
        messages_by_session: { [sessionA.id]: [messageA1, messageA2] },
        run_intents_by_session: {},
      })
    },
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  let ready = false
  const readyPromise = controller.start(sessionA.id, bootstrapReady).then(() => {
    ready = true
  })
  await flushAsyncWork()

  assert.equal(sockets.length, 0)
  assert.equal(reconnectCount, 0)
  assert.equal(ready, false)
  assert.equal(hydrateRequests.length, 0)

  state = readyControllerState()
  releaseBootstrap()
  await waitFor(() => sockets.length === 1)
  assert.equal(reconnectCount, 1)
  assert.equal(sockets[0].readyState, FakeWebSocket.CONNECTING)

  sockets[0].open()
  await readyPromise
  assert.equal(ready, true)
  await waitFor(() => hydrateRequests.length === 1)
  controller.stop()
})

test('Desktop V3 immediate send waits for retained realtime bootstrap and first resume', async () => {
  resetDesktopV3RealtimeControllerForTests()
  let state = createEmptyDesktopV3CacheState()
  const sockets: FakeWebSocket[] = []
  let reconnectCount = 0
  let releaseBootstrap!: () => void
  const bootstrapReady = new Promise<void>((resolve) => {
    releaseBootstrap = resolve
  })
  setDesktopV3RealtimeControllerFactoryForTests(() => new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => true,
    reconnect: async () => {
      reconnectCount += 1
      return reconnectFixture({
        snapshot_endpoint_cursor: 'cursor-reconnect',
        sessions_by_id: { [sessionA.id]: sessionA },
        projections_by_session: { [sessionA.id]: projectionA },
        run_intents_by_session: {},
        current_run_intent_by_session: {},
        session_order: [sessionA.id],
        workset_id: 'global-scope',
        realtime: {
          stream_path: '/v3/realtime/stream',
          resume: {
            protocol: 'v3.realtime',
            protocol_version: 1,
            kind: 'resume',
            endpoint_cursor: 'cursor-reconnect',
            subscriptions: [{ subscription_id: 'sub-a', session_id: sessionA.id }],
            worksets: [{
              workset_id: 'global-scope',
              subscription_id: 'workset-sub',
              selector: { kind: 'global', global: true },
              resources: ['run_intents'],
              auto_subscribe_sessions: true,
            }],
          },
        },
      })
    },
    hydrate: async (input) => hydrateSnapshotFixture({
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      session_order: (input.session_ids ?? []).filter((sessionId) => sessionId === sessionA.id),
      messages_by_session: { [sessionA.id]: [messageA1] },
      run_intents_by_session: {},
    }),
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  }))

  const lease = retainDesktopV3RealtimeController({
    ownerKey: 'desktop-shell',
    preferredSessionId: sessionA.id,
    bootstrap: bootstrapReady,
  })
  let immediateReady = false
  const immediateSendReady = requireDesktopV3RealtimeControllerReady().then((controller) => {
    immediateReady = true
    return controller
  })

  await flushAsyncWork()
  assert.equal(sockets.length, 0)
  assert.equal(reconnectCount, 0)
  assert.equal(immediateReady, false)

  state = readyControllerState()
  releaseBootstrap()
  await waitFor(() => sockets.length === 1)
  assert.equal(reconnectCount, 1)
  await flushAsyncWork()
  assert.equal(immediateReady, false)

  sockets[0].open()
  const controller = await immediateSendReady
  assert.equal(immediateReady, true)
  assert.equal(controller, await requireDesktopV3RealtimeControllerReady())
  assert.equal(sockets.length, 1)
  await lease.ready
  lease.release()
  resetDesktopV3RealtimeControllerForTests()
})

test('Desktop V3 workspace and session route switching keeps exactly one retained socket', async () => {
  resetDesktopV3RealtimeControllerForTests()
  let state = readyControllerState()
  const sockets: FakeWebSocket[] = []
  const hydrateRequests: unknown[] = []
  setDesktopV3RealtimeControllerFactoryForTests(() => new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => true,
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      run_intents_by_session: {},
      current_run_intent_by_session: {},
      session_order: [sessionA.id],
      workset_id: 'global-scope',
      realtime: {
        stream_path: '/v3/realtime/stream',
        resume: {
          protocol: 'v3.realtime',
          protocol_version: 1,
          kind: 'resume',
          endpoint_cursor: 'cursor-reconnect',
          subscriptions: [{ subscription_id: 'sub-a', session_id: sessionA.id }],
          worksets: [{
            workset_id: 'global-scope',
            subscription_id: 'workset-sub',
            selector: { kind: 'global', global: true },
            resources: ['run_intents'],
            auto_subscribe_sessions: true,
          }],
        },
      },
    }),
    hydrate: async (input) => {
      hydrateRequests.push(input)
      const requested = new Set(input.session_ids ?? [])
      return hydrateSnapshotFixture({
        sessions_by_id: Object.fromEntries(Object.entries({ [sessionA.id]: sessionA, [sessionB.id]: sessionB }).filter(([sessionId]) => requested.has(sessionId))),
        projections_by_session: Object.fromEntries(Object.entries({ [sessionA.id]: projectionA, [sessionB.id]: projectionB }).filter(([sessionId]) => requested.has(sessionId))),
        session_order: (input.session_ids ?? []).filter((sessionId) => requested.has(sessionId)),
        messages_by_session: Object.fromEntries(Object.entries({ [sessionA.id]: [messageA1], [sessionB.id]: [messageA2] }).filter(([sessionId]) => requested.has(sessionId))),
        run_intents_by_session: {},
      })
    },
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  }))

  const lease = retainDesktopV3RealtimeController({ ownerKey: 'desktop-shell', preferredSessionId: null })
  await waitFor(() => sockets.length === 1)
  sockets[0].open()
  await lease.ready
  assert.equal(sockets.length, 1)

  const controller = await requireDesktopV3RealtimeControllerReady()
  await controller.ensureSessionHistory(sessionA.id)
  assert.equal(sockets.length, 1)

  await requireDesktopV3RealtimeControllerReady()
  await controller.ensureSessionHistory(sessionB.id)
  assert.equal(sockets.length, 1)
  assert.deepEqual(
    hydrateRequests.map((request) => (request as { session_ids: string[] }).session_ids),
    [[sessionA.id], [sessionB.id]],
  )

  lease.release()
  await flushAsyncWork()
  assert.equal(sockets[0].closed, true)
  resetDesktopV3RealtimeControllerForTests()
})

test('Desktop V3 direct route session missing from reconnect is explicitly subscribed and hydrated after resume', async () => {
  let state = readyControllerState()
  const sockets: FakeWebSocket[] = []
  const hydrateRequests: unknown[] = []
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => true,
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      run_intents_by_session: {},
      current_run_intent_by_session: {},
      session_order: [sessionA.id],
      workset_id: 'global-scope',
      realtime: {
        stream_path: '/v3/realtime/stream',
        resume: {
          protocol: 'v3.realtime',
          protocol_version: 1,
          kind: 'resume',
          endpoint_cursor: 'cursor-reconnect',
          subscriptions: [{ subscription_id: 'sub-a', session_id: sessionA.id }],
          worksets: [{
            workset_id: 'global-scope',
            subscription_id: 'workset-sub',
            selector: { kind: 'global', global: true },
            resources: ['run_intents'],
            auto_subscribe_sessions: true,
          }],
        },
      },
    }),
    hydrate: async (input) => {
      hydrateRequests.push(input)
      return hydrateSnapshotFixture({
        sessions_by_id: { [sessionB.id]: sessionB },
        projections_by_session: { [sessionB.id]: projectionB },
        session_order: [sessionB.id],
        messages_by_session: { [sessionB.id]: [messageA1] },
        run_intents_by_session: {},
        selector: { kind: 'session_ids', session_ids: [sessionB.id] },
      })
    },
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  const ready = controller.start(sessionB.id)
  await waitFor(() => sockets.length === 1)
  sockets[0].open()
  await ready

  const resume = sockets[0].sent[0] as SessionV3RealtimeResumeWire
  assert.deepEqual(
    resume.subscriptions?.map((subscription) => subscription.session_id),
    [sessionA.id, sessionB.id],
  )
  assert.equal(
    resume.subscriptions?.find((subscription) => subscription.session_id === sessionB.id)?.endpoint_cursor,
    'cursor-reconnect',
  )
  await waitFor(() => hydrateRequests.length === 1)
  assert.deepEqual(
    (hydrateRequests[0] as { session_ids: string[] }).session_ids,
    [sessionB.id],
  )
  controller.stop()
})

test('Desktop V3 auto-discovered session hydrates once across duplicate discovery frames', async () => {
  let state = readyControllerState()
  const sockets: FakeWebSocket[] = []
  const hydrateRequests: unknown[] = []
  let releaseFirstHydrate!: () => void
  const firstHydrate = new Promise<void>((resolve) => {
    releaseFirstHydrate = resolve
  })
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => true,
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: {},
      projections_by_session: {},
      run_intents_by_session: {},
      current_run_intent_by_session: {},
      session_order: [],
      workset_id: 'global-scope',
      realtime: {
        stream_path: '/v3/realtime/stream',
        resume: {
          protocol: 'v3.realtime',
          protocol_version: 1,
          kind: 'resume',
          endpoint_cursor: 'cursor-reconnect',
          subscriptions: [],
          worksets: [{
            workset_id: 'global-scope',
            subscription_id: 'workset-sub',
            selector: { kind: 'global', global: true },
            resources: ['run_intents'],
            auto_subscribe_sessions: true,
          }],
        },
      },
    }),
    hydrate: async (input) => {
      hydrateRequests.push(input)
      await firstHydrate
      return hydrateSnapshotFixture({
        sessions_by_id: { [sessionB.id]: sessionB },
        projections_by_session: { [sessionB.id]: projectionB },
        messages_by_session: {},
        run_intents_by_session: {},
        session_order: [sessionB.id],
        selector: { kind: 'session_ids', session_ids: [sessionB.id] },
      })
    },
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  try {
    const ready = controller.start()
    await waitFor(() => sockets.length === 1)
    sockets[0].open()
    await ready

    const discoveryFrame = {
      protocol: 'v3.realtime',
      protocol_version: 1,
      kind: 'workset.session.discovered',
      workset_id: 'global-scope',
      session_id: sessionB.id,
      subscription_id: 'sub-session-b',
      auto_subscribed: true,
      endpoint_cursor: 'cursor-discovered',
    }

    sockets[0].emit(discoveryFrame)
    sockets[0].emit(discoveryFrame)
    await waitFor(() => hydrateRequests.length === 1)
    assert.deepEqual((hydrateRequests[0] as { session_ids: string[] }).session_ids, [sessionB.id])

    releaseFirstHydrate()
    await waitFor(() => state.projectionsBySession[sessionB.id] !== undefined)
    sockets[0].emit(discoveryFrame)
    await flushAsyncWork()
    assert.equal(hydrateRequests.length, 1)
  } finally {
    controller.stop()
    releaseFirstHydrate()
  }
})

test('Desktop V3 retained realtime controller releases connecting startup without opening later', async () => {
  resetDesktopV3RealtimeControllerForTests()
  let state = readyControllerState()
  const sockets: FakeWebSocket[] = []
  setDesktopV3RealtimeControllerFactoryForTests(() => new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => true,
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: {},
      projections_by_session: {},
      run_intents_by_session: {},
      current_run_intent_by_session: {},
      session_order: [],
      workset_id: 'global-scope',
      realtime: {
        stream_path: '/v3/realtime/stream',
        resume: {
          protocol: 'v3.realtime',
          protocol_version: 1,
          kind: 'resume',
          endpoint_cursor: 'cursor-reconnect',
          subscriptions: [],
          worksets: [],
        },
      },
    }),
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  }))

  const first = retainDesktopV3RealtimeController()
  const second = retainDesktopV3RealtimeController()
  await flushAsyncWork()
  assert.equal(sockets.length, 1)
  assert.equal(sockets[0].readyState, FakeWebSocket.CONNECTING)

  second.release()
  first.release()
  assert.equal(sockets[0].closed, true)
  sockets[0].open()
  await assert.rejects(first.ready, /released/)
  await assert.rejects(requireDesktopV3RealtimeControllerReady(), /not retained/)
  assert.equal(sockets.length, 1)
  resetDesktopV3RealtimeControllerForTests()
})

test('Desktop V3 retained realtime controller handles unmount before delayed bootstrap opens a socket', async () => {
  resetDesktopV3RealtimeControllerForTests()
  let state = createEmptyDesktopV3CacheState()
  const sockets: FakeWebSocket[] = []
  let reconnectCount = 0
  let releaseBootstrap!: () => void
  const bootstrapReady = new Promise<void>((resolve) => {
    releaseBootstrap = resolve
  })

  setDesktopV3RealtimeControllerFactoryForTests(() => new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => true,
    reconnect: async () => {
      reconnectCount += 1
      return reconnectFixture({
        snapshot_endpoint_cursor: 'cursor-reconnect',
        sessions_by_id: {},
        projections_by_session: {},
        run_intents_by_session: {},
        current_run_intent_by_session: {},
        session_order: [],
        workset_id: 'global-scope',
        realtime: {
          stream_path: '/v3/realtime/stream',
          resume: {
            protocol: 'v3.realtime',
            protocol_version: 1,
            kind: 'resume',
            endpoint_cursor: 'cursor-reconnect',
            subscriptions: [],
            worksets: [],
          },
        },
      })
    },
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  }))

  const lease = retainDesktopV3RealtimeController({
    ownerKey: 'desktop-shell',
    bootstrap: bootstrapReady,
  })
  const readyRejects = assert.rejects(lease.ready, /released/)
  await flushAsyncWork()
  lease.release()
  await flushAsyncWork()
  releaseBootstrap()
  await readyRejects
  await assert.rejects(requireDesktopV3RealtimeControllerReady(), /not retained/)
  assert.equal(reconnectCount, 0)
  assert.equal(sockets.length, 0)
  resetDesktopV3RealtimeControllerForTests()
})

test('Desktop V3 retained realtime controller keeps one owner lease through Strict Mode remount', async () => {
  resetDesktopV3RealtimeControllerForTests()
  let state = readyControllerState()
  const sockets: FakeWebSocket[] = []
  setDesktopV3RealtimeControllerFactoryForTests(() => new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => true,
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: {},
      projections_by_session: {},
      run_intents_by_session: {},
      current_run_intent_by_session: {},
      session_order: [],
      workset_id: 'global-scope',
      realtime: {
        stream_path: '/v3/realtime/stream',
        resume: {
          protocol: 'v3.realtime',
          protocol_version: 1,
          kind: 'resume',
          endpoint_cursor: 'cursor-reconnect',
          subscriptions: [],
          worksets: [],
        },
      },
    }),
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  }))

  const first = retainDesktopV3RealtimeController({ ownerKey: 'desktop-shell' })
  await waitFor(() => sockets.length === 1)
  const second = retainDesktopV3RealtimeController({ ownerKey: 'desktop-shell' })
  await flushAsyncWork()

  assert.equal(sockets.length, 1)
  assert.equal(sockets[0].closed, false)
  first.release()
  await flushAsyncWork()
  assert.equal(sockets[0].closed, false)

  sockets[0].open()
  await second.ready
  await requireDesktopV3RealtimeControllerReady()

  second.release()
  await flushAsyncWork()
  assert.equal(sockets[0].closed, true)
  await assert.rejects(requireDesktopV3RealtimeControllerReady(), /not retained/)
  resetDesktopV3RealtimeControllerForTests()
})

test('Desktop V3 retained realtime controller reports bootstrap failure without creating a controller on ready require', async () => {
  resetDesktopV3RealtimeControllerForTests()
  const bootstrapFailure = Promise.reject(new Error('bootstrap failed'))
  bootstrapFailure.catch(() => undefined)
  setDesktopV3RealtimeControllerFactoryForTests(() => new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => createEmptyDesktopV3CacheState(),
    dispatch: () => {},
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => true,
    reconnect: async () => {
      throw new Error('reconnect should wait for bootstrap')
    },
  }))

  const lease = retainDesktopV3RealtimeController({
    ownerKey: 'desktop-shell',
    bootstrap: bootstrapFailure,
  })

  await assert.rejects(lease.ready, /bootstrap failed/)
  await assert.rejects(requireDesktopV3RealtimeControllerReady(), /bootstrap failed/)
  lease.release()
  await assert.rejects(requireDesktopV3RealtimeControllerReady(), /not retained/)
  resetDesktopV3RealtimeControllerForTests()
})

test('Desktop V3 reconnect input requires exact principal-global sidebar scope', () => {
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'global-scope' }
  state.syncScopesById['global-scope'] = {
    scopeId: 'global-scope',
    surface: 'desktop',
    streamKind: 'v3.sync.snapshot',
    selectorFilterHash: 'global-hash',
    resourceSet: 'run_intents',
    selector: { kind: 'global', global: true },
    endpointCursor: 'cursor-bootstrap',
    replayPath: '/v3/sync/stream',
    replayTransport: 'http_post',
    needsBootstrap: false,
  }

  const input = buildDesktopV3ReconnectInput(state, 'client-a')
  assert.equal(input.workset.workset_id, 'global-scope')
  assert.deepEqual(input.workset.selector, { kind: 'global', global: true })
  assert.deepEqual(input.workset.history, { mode: 'none' })
  assert.equal(input.workset.resources.events, false)
  assert.equal(input.workset.resources.run_intents, true)
  assert.equal(input.workset.auto_subscribe_sessions, true)

  state.syncScopesById['global-scope'].selector = { kind: 'recent', global: true }
  assert.throws(
    () => buildDesktopV3ReconnectInput(state, 'client-a'),
    /principal-wide global selector/,
  )
})

test('Desktop V3 active-run repair merges websocket and HTTP events through durable sequence', async () => {
  assert.equal(await resetDesktopV3CacheDBForTests(), true)
  const owner = testDesktopV3CacheOwner()
  let state: DesktopV3CacheState = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'global-scope' }
  state.syncScopesById['global-scope'] = {
    scopeId: 'global-scope',
    surface: 'desktop',
    streamKind: 'v3.sync.snapshot',
    selectorFilterHash: 'global-hash',
    resourceSet: 'run_intents',
    selector: { kind: 'global', global: true },
    endpointCursor: 'cursor-bootstrap',
    replayPath: '/v3/sync/stream',
    replayTransport: 'http_post',
    needsBootstrap: false,
  }

  const sockets: FakeWebSocket[] = []
  const repairEvent: V3SessionEvent = {
    id: 'evt-repair-3',
    session_id: sessionA.id,
    seq: 3,
    event_type: 'session.assistant.delta',
    payload: { run_id: runIntentA.run_id, delta: 'repair-' },
    ts_unix_ms: 3,
  }
  const liveEvent: V3SessionEvent = {
    id: 'evt-live-4',
    session_id: sessionA.id,
    seq: 4,
    event_type: 'session.assistant.delta',
    payload: { run_id: runIntentA.run_id, delta: 'live' },
    ts_unix_ms: 4,
  }
  const readRequests: Array<{ sessionId: string; afterSeq: number; limit?: number }> = []
  let releaseFirstRepairPage!: () => void
  const firstRepairPage = new Promise<void>((resolve) => {
    releaseFirstRepairPage = resolve
  })

  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => owner,
    saveActiveOwnerKey: () => true,
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      run_intents_by_session: { [sessionA.id]: [runIntentA] },
      current_run_intent_by_session: { [sessionA.id]: runIntentA },
      session_order: [sessionA.id],
      workset_id: 'global-scope',
      realtime: {
        stream_path: '/v3/realtime/stream',
        resume: {
          protocol: 'v3.realtime',
          protocol_version: 1,
          kind: 'resume',
          endpoint_cursor: 'cursor-reconnect',
          subscriptions: [{ subscription_id: 'sub-a', session_id: sessionA.id }],
          worksets: [{
            workset_id: 'global-scope',
            subscription_id: 'workset-sub',
            selector: { kind: 'global', global: true },
            resources: ['run_intents'],
            auto_subscribe_sessions: true,
          }],
        },
      },
    }),
    readEventsPage: async (input) => {
      readRequests.push(input)
      if (readRequests.length === 1) {
        await firstRepairPage
        return {
          ok: true,
          session_id: input.sessionId,
          events: [repairEvent],
          projection: { ...projectionA, last_event_seq: 3, projection_high_watermark_seq: 3 },
          high_watermark_seq: 3,
          next_seq: 4,
          applied_seq: 3,
        }
      }
      return {
        ok: true,
        session_id: input.sessionId,
        events: [],
        projection: { ...projectionA, last_event_seq: 3, projection_high_watermark_seq: 3 },
        high_watermark_seq: 3,
        next_seq: 4,
        applied_seq: 3,
      }
    },
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  const ready = controller.start()
  await waitFor(() => readRequests.length === 1)
  await flushAsyncWork()
  assert.equal(sockets.length, 0)

  releaseFirstRepairPage()
  await waitFor(() => sockets.length === 1)
  sockets[0].open()
  await ready

  sockets[0].emit({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    session_id: sessionA.id,
    event_type: liveEvent.event_type,
    event: liveEvent,
    projection: { ...projectionA, last_event_seq: 4, projection_high_watermark_seq: 4 },
    endpoint_cursor: 'cursor-live-4',
  })

  await waitFor(() => state.realtime.endpointCursor === 'cursor-live-4')
  assert.equal(
    state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id]?.assistantDraft?.content,
    'repair-live',
  )

  assert.deepEqual(
    state.eventsBySession[sessionA.id].map((event) => `${event.seq}:${event.id}`),
    ['3:evt-repair-3', '4:evt-live-4'],
  )
  assert.equal(state.realtime.endpointCursor, 'cursor-live-4')

  const durableOwner = await readDesktopV3Owner(owner.key)
  assert.equal(
    durableOwner?.liveRunsBySession?.[sessionA.id]?.[runIntentA.run_id]?.assistantDraft?.content,
    'repair-live',
  )
  controller.stop()
  assert.equal(await resetDesktopV3CacheDBForTests(), true)
})

test('Desktop V3 active-run repair completes before terminal websocket event', async () => {
  let state: DesktopV3CacheState = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'global-scope' }
  state.syncScopesById['global-scope'] = {
    scopeId: 'global-scope',
    surface: 'desktop',
    streamKind: 'v3.sync.snapshot',
    selectorFilterHash: 'global-hash',
    resourceSet: 'run_intents',
    selector: { kind: 'global', global: true },
    endpointCursor: 'cursor-bootstrap',
    replayPath: '/v3/sync/stream',
    replayTransport: 'http_post',
    needsBootstrap: false,
  }

  const sockets: FakeWebSocket[] = []
  const activeRepairIntent = {
    ...runIntentA,
    status: 'running',
    updated_at: 30,
    event_seq: 3,
  }
  const terminalRunIntent = {
    ...runIntentA,
    status: 'completed',
    updated_at: 50,
    event_seq: 5,
  }
  const olderActiveLifecycleEvent: V3SessionEvent = {
    id: 'evt-repair-running-3',
    session_id: sessionA.id,
    seq: 3,
    event_type: 'session.run.running',
    payload: {
      run_id: runIntentA.run_id,
      status: 'running',
      lifecycle: { status: 'running' },
      run_intent: activeRepairIntent,
    },
    ts_unix_ms: 3,
  }
  const olderActiveDeltaEvent: V3SessionEvent = {
    id: 'evt-repair-delta-4',
    session_id: sessionA.id,
    seq: 4,
    event_type: 'session.assistant.delta',
    payload: { run_id: runIntentA.run_id, delta: 'stale-repair' },
    ts_unix_ms: 4,
  }
  const terminalEvent: V3SessionEvent = {
    id: 'evt-terminal-5',
    session_id: sessionA.id,
    seq: 5,
    event_type: 'session.run.completed',
    payload: {
      run_id: runIntentA.run_id,
      status: 'completed',
      lifecycle: { status: 'completed' },
      run_intent: terminalRunIntent,
    },
    ts_unix_ms: 5,
  }
  const readRequests: Array<{ sessionId: string; afterSeq: number; limit?: number }> = []
  let releaseRepairPage!: () => void
  let repairPageReturned = false
  const repairPage = new Promise<void>((resolve) => {
    releaseRepairPage = resolve
  })

  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => true,
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      run_intents_by_session: { [sessionA.id]: [runIntentA] },
      current_run_intent_by_session: { [sessionA.id]: runIntentA },
      session_order: [sessionA.id],
      workset_id: 'global-scope',
      realtime: {
        stream_path: '/v3/realtime/stream',
        resume: {
          protocol: 'v3.realtime',
          protocol_version: 1,
          kind: 'resume',
          endpoint_cursor: 'cursor-reconnect',
          subscriptions: [{ subscription_id: 'sub-a', session_id: sessionA.id }],
          worksets: [{
            workset_id: 'global-scope',
            subscription_id: 'workset-sub',
            selector: { kind: 'global', global: true },
            resources: ['run_intents'],
            auto_subscribe_sessions: true,
          }],
        },
      },
    }),
    readEventsPage: async (input) => {
      readRequests.push(input)
      await repairPage
      repairPageReturned = true
      return {
        ok: true,
        session_id: input.sessionId,
        events: [olderActiveLifecycleEvent, olderActiveDeltaEvent],
        projection: { ...projectionA, last_event_seq: 4, projection_high_watermark_seq: 4 },
        high_watermark_seq: 4,
        next_seq: 4,
        applied_seq: 4,
      }
    },
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  const ready = controller.start()
  await waitFor(() => readRequests.length === 1)
  await flushAsyncWork()
  assert.equal(sockets.length, 0)

  releaseRepairPage()
  await waitFor(() => repairPageReturned)
  await waitFor(() => sockets.length > 0)
  sockets[0].open()
  await ready

  sockets[0].emit({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    session_id: sessionA.id,
    event_type: terminalEvent.event_type,
    event: terminalEvent,
    projection: { ...projectionA, last_event_seq: 5, projection_high_watermark_seq: 5 },
    endpoint_cursor: 'cursor-terminal-5',
  })

  await waitFor(() => state.realtime.endpointCursor === 'cursor-terminal-5')
  assert.equal(state.currentRunIntentBySession[sessionA.id], undefined)
  assert.equal(state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id]?.status, 'completed')

  await flushAsyncWork()

  assert.equal(state.currentRunIntentBySession[sessionA.id], undefined)
  assert.equal(state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id]?.status, 'completed')
  assert.equal(state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id]?.assistantDraft?.content, 'stale-repair')
  assert.equal(state.runIntentsBySession[sessionA.id]?.[runIntentA.run_id]?.status, 'completed')
  assert.equal(state.runIntentsBySession[sessionA.id]?.[runIntentA.run_id]?.event_seq, 5)
  assert.deepEqual(state.sessionsById[sessionA.id]?.kind === 'full' ? state.sessionsById[sessionA.id]?.session.lifecycle : undefined, { status: 'completed' })
  assert.deepEqual(
    state.eventsBySession[sessionA.id].map((event) => `${event.seq}:${event.id}`),
    ['3:evt-repair-running-3', '4:evt-repair-delta-4', '5:evt-terminal-5'],
  )
  assert.equal(state.realtime.endpointCursor, 'cursor-terminal-5')
  controller.stop()
})


test('Desktop V3 active-run repair defers only events for the repaired run', async () => {
  let state: DesktopV3CacheState = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'global-scope' }
  state.syncScopesById['global-scope'] = {
    scopeId: 'global-scope',
    surface: 'desktop',
    streamKind: 'v3.sync.snapshot',
    selectorFilterHash: 'global-hash',
    resourceSet: 'run_intents',
    selector: { kind: 'global', global: true },
    endpointCursor: 'cursor-bootstrap',
    replayPath: '/v3/sync/stream',
    replayTransport: 'http_post',
    needsBootstrap: false,
  }

  const sockets: FakeWebSocket[] = []
  const activeRepairIntent = {
    ...runIntentA,
    status: 'running',
    updated_at: 30,
    event_seq: 3,
  }
  const terminalRunA = {
    ...runIntentA,
    status: 'completed',
    updated_at: 50,
    event_seq: 5,
  }
  const runB = {
    ...runIntentA,
    run_id: 'run-b',
    status: 'running',
    created_at: 60,
    updated_at: 60,
    event_seq: 6,
  }
  const staleRunALifecycleEvent: V3SessionEvent = {
    id: 'evt-repair-running-3',
    session_id: sessionA.id,
    seq: 3,
    event_type: 'session.run.running',
    payload: {
      run_id: runIntentA.run_id,
      status: 'running',
      lifecycle: { status: 'running' },
      run_intent: activeRepairIntent,
    },
    ts_unix_ms: 3,
  }
  const staleRunADeltaEvent: V3SessionEvent = {
    id: 'evt-repair-delta-4',
    session_id: sessionA.id,
    seq: 4,
    event_type: 'session.assistant.delta',
    payload: { run_id: runIntentA.run_id, delta: 'stale-run-a' },
    ts_unix_ms: 4,
  }
  const terminalRunAEvent: V3SessionEvent = {
    id: 'evt-terminal-5',
    session_id: sessionA.id,
    seq: 5,
    event_type: 'session.run.completed',
    payload: {
      run_id: runIntentA.run_id,
      status: 'completed',
      lifecycle: { status: 'completed' },
      run_intent: terminalRunA,
    },
    ts_unix_ms: 5,
  }
  const runningRunBEvent: V3SessionEvent = {
    id: 'evt-run-b-running-6',
    session_id: sessionA.id,
    seq: 6,
    event_type: 'session.run.running',
    payload: {
      run_id: runB.run_id,
      status: 'running',
      lifecycle: { status: 'running' },
      run_intent: runB,
    },
    ts_unix_ms: 6,
  }
  const runBDeltaEvent: V3SessionEvent = {
    id: 'evt-run-b-delta-7',
    session_id: sessionA.id,
    seq: 7,
    event_type: 'session.assistant.delta',
    payload: { run_id: runB.run_id, delta: 'run-b-delta' },
    ts_unix_ms: 7,
  }
  const readRequests: Array<{ sessionId: string; afterSeq: number; limit?: number }> = []
  let releaseRepairPage!: () => void
  let repairPageReturned = false
  const repairPage = new Promise<void>((resolve) => {
    releaseRepairPage = resolve
  })

  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => true,
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      run_intents_by_session: { [sessionA.id]: [runIntentA] },
      current_run_intent_by_session: { [sessionA.id]: runIntentA },
      session_order: [sessionA.id],
      workset_id: 'global-scope',
      realtime: {
        stream_path: '/v3/realtime/stream',
        resume: {
          protocol: 'v3.realtime',
          protocol_version: 1,
          kind: 'resume',
          endpoint_cursor: 'cursor-reconnect',
          subscriptions: [{ subscription_id: 'sub-a', session_id: sessionA.id }],
          worksets: [{
            workset_id: 'global-scope',
            subscription_id: 'workset-sub',
            selector: { kind: 'global', global: true },
            resources: ['run_intents'],
            auto_subscribe_sessions: true,
          }],
        },
      },
    }),
    readEventsPage: async (input) => {
      readRequests.push(input)
      await repairPage
      repairPageReturned = true
      return {
        ok: true,
        session_id: input.sessionId,
        events: [staleRunALifecycleEvent, staleRunADeltaEvent],
        projection: { ...projectionA, last_event_seq: 4, projection_high_watermark_seq: 4 },
        high_watermark_seq: 4,
        next_seq: 4,
        applied_seq: 4,
      }
    },
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  const ready = controller.start()
  await waitFor(() => readRequests.length === 1)
  await flushAsyncWork()
  assert.equal(sockets.length, 0)

  releaseRepairPage()
  await waitFor(() => repairPageReturned)
  await waitFor(() => sockets.length > 0)
  sockets[0].open()
  await ready

  sockets[0].emit({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    session_id: sessionA.id,
    event_type: terminalRunAEvent.event_type,
    event: terminalRunAEvent,
    projection: { ...projectionA, last_event_seq: 5, projection_high_watermark_seq: 5 },
    endpoint_cursor: 'cursor-terminal-5',
  })
  sockets[0].emit({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    session_id: sessionA.id,
    event_type: runningRunBEvent.event_type,
    event: runningRunBEvent,
    projection: { ...projectionA, last_event_seq: 6, projection_high_watermark_seq: 6 },
    endpoint_cursor: 'cursor-run-b-6',
  })
  sockets[0].emit({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    session_id: sessionA.id,
    event_type: runBDeltaEvent.event_type,
    event: runBDeltaEvent,
    projection: { ...projectionA, last_event_seq: 7, projection_high_watermark_seq: 7 },
    endpoint_cursor: 'cursor-run-b-7',
  })

  await waitFor(() => state.liveRunsBySession[sessionA.id]?.[runB.run_id]?.assistantDraft?.content === 'run-b-delta')
  assert.equal(
    state.liveRunsBySession[sessionA.id]?.[runB.run_id]?.assistantDraft?.content,
    'run-b-delta',
  )

  await flushAsyncWork()

  assert.equal(
    state.currentRunIntentBySession[sessionA.id]?.run_id,
    runB.run_id,
  )
  assert.equal(
    state.liveRunsBySession[sessionA.id]?.[runB.run_id]?.assistantDraft?.content,
    'run-b-delta',
  )
  assert.equal(
    state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id]?.status,
    'completed',
  )
  await waitFor(() => state.realtime.endpointCursor === 'cursor-run-b-7')
  assert.deepEqual(
    state.eventsBySession[sessionA.id].map((event) => `${event.seq}:${event.id}`),
    ['3:evt-repair-running-3', '4:evt-repair-delta-4', '5:evt-terminal-5', '6:evt-run-b-running-6', '7:evt-run-b-delta-7'],
  )
  controller.stop()
})

test('Desktop V3 active-run repair ignores replacement-run overlays from HTTP page', async () => {
  let state: DesktopV3CacheState = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'global-scope' }
  state.syncScopesById['global-scope'] = {
    scopeId: 'global-scope',
    surface: 'desktop',
    streamKind: 'v3.sync.snapshot',
    selectorFilterHash: 'global-hash',
    resourceSet: 'run_intents',
    selector: { kind: 'global', global: true },
    endpointCursor: 'cursor-bootstrap',
    replayPath: '/v3/sync/stream',
    replayTransport: 'http_post',
    needsBootstrap: false,
  }

  const sockets: FakeWebSocket[] = []
  const runA = {
    ...runIntentA,
    status: 'running',
    updated_at: 30,
    event_seq: 3,
  }
  const terminalRunA = {
    ...runA,
    status: 'completed',
    updated_at: 50,
    event_seq: 5,
  }
  const runB = {
    ...runIntentA,
    run_id: 'run-b',
    status: 'running',
    created_at: 60,
    updated_at: 60,
    event_seq: 6,
  }
  const terminalRunAEvent: V3SessionEvent = {
    id: 'evt-terminal-5',
    session_id: sessionA.id,
    seq: 5,
    event_type: 'session.run.completed',
    payload: {
      run_id: runA.run_id,
      status: 'completed',
      lifecycle: { status: 'completed' },
      run_intent: terminalRunA,
    },
    ts_unix_ms: 5,
  }
  const runningRunBEvent: V3SessionEvent = {
    id: 'evt-run-b-running-6',
    session_id: sessionA.id,
    seq: 6,
    event_type: 'session.run.running',
    payload: {
      run_id: runB.run_id,
      status: 'running',
      lifecycle: { status: 'running' },
      run_intent: runB,
    },
    ts_unix_ms: 6,
  }
  const runBDeltaEvent: V3SessionEvent = {
    id: 'evt-run-b-delta-7',
    session_id: sessionA.id,
    seq: 7,
    event_type: 'session.assistant.delta',
    payload: { run_id: runB.run_id, delta: 'run-b-delta' },
    ts_unix_ms: 7,
  }
  const readRequests: Array<{ sessionId: string; afterSeq: number; limit?: number }> = []

  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => true,
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      run_intents_by_session: { [sessionA.id]: [runA] },
      current_run_intent_by_session: { [sessionA.id]: runA },
      session_order: [sessionA.id],
      workset_id: 'global-scope',
      realtime: {
        stream_path: '/v3/realtime/stream',
        resume: {
          protocol: 'v3.realtime',
          protocol_version: 1,
          kind: 'resume',
          endpoint_cursor: 'cursor-reconnect',
          subscriptions: [{ subscription_id: 'sub-a', session_id: sessionA.id }],
          worksets: [{
            workset_id: 'global-scope',
            subscription_id: 'workset-sub',
            selector: { kind: 'global', global: true },
            resources: ['run_intents'],
            auto_subscribe_sessions: true,
          }],
        },
      },
    }),
    readEventsPage: async (input) => {
      readRequests.push(input)
      return {
        ok: true,
        session_id: input.sessionId,
        events: [terminalRunAEvent, runningRunBEvent, runBDeltaEvent],
        projection: { ...projectionA, last_event_seq: 7, projection_high_watermark_seq: 7 },
        high_watermark_seq: 7,
        next_seq: 7,
        applied_seq: 7,
      }
    },
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  const ready = controller.start()
  await waitFor(() => sockets.length > 0)
  sockets[0].open()
  await ready
  await waitFor(() => readRequests.length === 1)
  await waitFor(() => state.currentRunIntentBySession[sessionA.id] === undefined)

  assert.equal(state.currentRunIntentBySession[sessionA.id], undefined)
  assert.equal(state.liveRunsBySession[sessionA.id]?.[runB.run_id], undefined)
  assert.equal(
    state.liveRunsBySession[sessionA.id]?.[runA.run_id]?.status,
    'completed',
  )

  sockets[0].emit({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    session_id: sessionA.id,
    event_type: runningRunBEvent.event_type,
    event: runningRunBEvent,
    projection: { ...projectionA, last_event_seq: 6, projection_high_watermark_seq: 6 },
    endpoint_cursor: 'cursor-run-b-6',
  })
  sockets[0].emit({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    session_id: sessionA.id,
    event_type: runBDeltaEvent.event_type,
    event: runBDeltaEvent,
    projection: { ...projectionA, last_event_seq: 7, projection_high_watermark_seq: 7 },
    endpoint_cursor: 'cursor-run-b-7',
  })

  await waitFor(() => state.liveRunsBySession[sessionA.id]?.[runB.run_id]?.assistantDraft?.content === 'run-b-delta')
  assert.equal(
    state.currentRunIntentBySession[sessionA.id]?.run_id,
    runB.run_id,
  )
  assert.equal(
    state.liveRunsBySession[sessionA.id]?.[runB.run_id]?.assistantDraft?.content,
    'run-b-delta',
  )
  assert.equal(
    state.liveRunsBySession[sessionA.id]?.[runA.run_id]?.status,
    'completed',
  )
  await waitFor(() => state.realtime.endpointCursor === 'cursor-run-b-7')
  controller.stop()
})

test('warm restore resumes from persisted realtime cursor', async () => {
  let state = readyControllerState()
  state.realtime.endpointCursor = 'cursor-persisted-warm'
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.projectionsBySession[sessionA.id] = projectionA
  state.sessionOrderByScope['global-scope'] = [sessionA.id]

  const sockets: FakeWebSocket[] = []
  const openCursors: string[] = []
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => true,
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect-newer',
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      run_intents_by_session: {},
      current_run_intent_by_session: {},
      session_order: [sessionA.id],
      workset_id: 'global-scope',
      realtime: {
        stream_path: '/v3/realtime/stream',
        resume: {
          protocol: 'v3.realtime',
          protocol_version: 1,
          kind: 'resume',
          endpoint_cursor: 'cursor-reconnect-newer',
          subscriptions: [{ subscription_id: 'sub-a', session_id: sessionA.id, endpoint_cursor: 'cursor-reconnect-newer' }],
          worksets: [{
            workset_id: 'global-scope',
            subscription_id: 'workset-sub',
            selector: { kind: 'global', global: true },
            resources: ['run_intents'],
            auto_subscribe_sessions: true,
          }],
        },
      },
    }),
    openSocket: ({ endpointCursor }) => {
      openCursors.push(endpointCursor)
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  const ready = controller.start(null)
  await waitFor(() => sockets.length === 1)
  sockets[0].open()
  await ready

  const resume = sockets[0].sent[0] as SessionV3RealtimeResumeWire
  assert.deepEqual(openCursors, ['cursor-persisted-warm'])
  assert.equal(resume.endpoint_cursor, 'cursor-persisted-warm')
  assert.equal(resume.subscriptions?.[0]?.endpoint_cursor, 'cursor-persisted-warm')
  assert.equal(state.realtime.endpointCursor, 'cursor-persisted-warm')
  controller.stop()
})

test('expired persisted cursor takes the explicit cursor-error recovery path', async () => {
  let state = readyControllerState()
  state.realtime.endpointCursor = 'cursor-persisted-expired'
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.projectionsBySession[sessionA.id] = projectionA
  state.sessionOrderByScope['global-scope'] = [sessionA.id]

  const sockets: FakeWebSocket[] = []
  let reconnectCount = 0
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => true,
    reconnect: async () => {
      reconnectCount += 1
      return reconnectFixture({
        snapshot_endpoint_cursor: `cursor-reconnect-${reconnectCount}`,
        sessions_by_id: { [sessionA.id]: sessionA },
        projections_by_session: { [sessionA.id]: projectionA },
        run_intents_by_session: {},
        current_run_intent_by_session: {},
        session_order: [sessionA.id],
        workset_id: 'global-scope',
        realtime: {
          stream_path: '/v3/realtime/stream',
          resume: {
            protocol: 'v3.realtime',
            protocol_version: 1,
            kind: 'resume',
            endpoint_cursor: `cursor-reconnect-${reconnectCount}`,
            subscriptions: [{ subscription_id: 'sub-a', session_id: sessionA.id }],
            worksets: [],
          },
        },
      })
    },
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  const ready = controller.start(null)
  await waitFor(() => sockets.length === 1)
  sockets[0].open()
  await ready
  assert.equal((sockets[0].sent[0] as SessionV3RealtimeResumeWire).endpoint_cursor, 'cursor-persisted-expired')

  sockets[0].emit({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'cursor.error',
    reason: 'expired cursor',
  })
  await waitFor(() => sockets.length === 2)
  sockets[1].open()
  await waitFor(() => sockets[1].sent.length > 0)

  assert.equal((sockets[1].sent[0] as SessionV3RealtimeResumeWire).endpoint_cursor, 'cursor-reconnect-2')
  assert.equal(state.realtime.endpointCursor, 'cursor-reconnect-2')
  controller.stop()
})

test('active repair considers run-intent keys outside session_order', async () => {
  let state: DesktopV3CacheState = readyControllerState()
  const repairEvent: V3SessionEvent = {
    id: 'evt-outside-order-6',
    session_id: sessionA.id,
    seq: 6,
    event_type: 'session.assistant.delta',
    payload: { run_id: runIntentA.run_id, delta: 'outside-order' },
    ts_unix_ms: 6,
  }
  const activeIntent = { ...runIntentA, status: 'running', event_seq: 5, updated_at: 5 }
  const sockets: FakeWebSocket[] = []
  const readRequests: Array<{ sessionId: string; afterSeq: number; limit?: number }> = []
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => true,
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      run_intents_by_session: { [sessionA.id]: [activeIntent] },
      current_run_intent_by_session: { [sessionA.id]: activeIntent },
      session_order: [],
      workset_id: 'global-scope',
    }),
    readEventsPage: async (input) => {
      readRequests.push(input)
      return {
        ok: true,
        session_id: input.sessionId,
        events: [repairEvent],
        projection: { ...projectionA, last_event_seq: 6, projection_high_watermark_seq: 6 },
        high_watermark_seq: 6,
        next_seq: 7,
        applied_seq: 6,
      }
    },
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  const ready = controller.start(null)
  await waitFor(() => readRequests.length === 1)
  await waitFor(() => sockets.length === 1)
  sockets[0].open()
  await ready

  assert.deepEqual(readRequests.map((request) => [request.sessionId, request.afterSeq]), [[sessionA.id, 0]])
  assert.equal(state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id]?.assistantDraft?.content, 'outside-order')
  controller.stop()
})

test('Desktop V3 cursor-error rehydrate repairs selected transcript after replacement resume', async () => {
  let state = readyControllerState()
  state.selectedSessionId = sessionA.id
  state.sessionsById[sessionA.id] = { kind: 'full', session: { ...sessionA, message_count: 2 }, needsHydrate: false }
  state.projectionsBySession[sessionA.id] = { ...projectionA, last_event_seq: 2, projection_high_watermark_seq: 2 }
  state.messagesBySession[sessionA.id] = {
    items: [messageA1],
    byMessageId: { [messageA1.id]: 0 },
    byGlobalSeq: { [String(messageA1.global_seq)]: 0 },
    sourceMessageCount: 1,
    sourceLastMessageAt: messageA1.created_at,
    sourceProjectionHighWatermarkSeq: 1,
    source: 'network',
  }

  const sockets: FakeWebSocket[] = []
  const hydrateRequests: unknown[] = []
  let reconnectCount = 0
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => true,
    reconnect: async () => {
      reconnectCount += 1
      return reconnectFixture({
        snapshot_endpoint_cursor: `cursor-reconnect-${reconnectCount}`,
        sessions_by_id: { [sessionA.id]: { ...sessionA, message_count: 2 } },
        projections_by_session: { [sessionA.id]: { ...projectionA, last_event_seq: 2, projection_high_watermark_seq: 2 } },
        run_intents_by_session: {},
        current_run_intent_by_session: {},
        messages_by_session: {},
        events_by_session: {},
        session_order: [sessionA.id],
        workset_id: 'global-scope',
        realtime: {
          stream_path: '/v3/realtime/stream',
          resume: {
            protocol: 'v3.realtime',
            protocol_version: 1,
            kind: 'resume',
            endpoint_cursor: `cursor-reconnect-${reconnectCount}`,
            subscriptions: [{ subscription_id: 'sub-a', session_id: sessionA.id }],
            worksets: [{
              workset_id: 'global-scope',
              subscription_id: 'workset-sub',
              selector: { kind: 'global', global: true },
              resources: ['run_intents'],
              auto_subscribe_sessions: true,
            }],
          },
        },
      })
    },
    hydrate: async (input) => {
      hydrateRequests.push(input)
      return hydrateSnapshotFixture({
        sessions_by_id: { [sessionA.id]: { ...sessionA, message_count: 2 } },
        projections_by_session: { [sessionA.id]: { ...projectionA, last_event_seq: 2, projection_high_watermark_seq: 2 } },
        session_order: [sessionA.id],
        messages_by_session: { [sessionA.id]: [messageA1, messageA2] },
      })
    },
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  let ready: Promise<void> | undefined
  try {
    ready = controller.start(sessionA.id)
    await waitFor(() => sockets.length === 1)
    sockets[0].open()
    await ready
    await waitFor(() => hydrateRequests.length === 1)
    await waitFor(() => state.messagesBySession[sessionA.id]?.items.length === 2)
    await flushAsyncWork()

    state.messagesBySession[sessionA.id] = {
      items: [messageA1],
      byMessageId: { [messageA1.id]: 0 },
      byGlobalSeq: { [String(messageA1.global_seq)]: 0 },
      sourceMessageCount: 1,
      sourceLastMessageAt: messageA1.created_at,
      sourceProjectionHighWatermarkSeq: 1,
      source: 'network',
    }

    sockets[0].emit({
      protocol: 'v3.realtime',
      protocol_version: 1,
      kind: 'cursor.error',
      reason: 'stale cursor',
    })
    await waitFor(() => sockets.length === 2)
    assert.equal(hydrateRequests.length, 1)

    sockets[1].open()
    await waitFor(() => hydrateRequests.length === 2)
    assert.deepEqual(state.messagesBySession[sessionA.id].items.map((message) => message.id), [messageA1.id, messageA2.id])
    assert.deepEqual(new Set(state.messagesBySession[sessionA.id].items.map((message) => message.id)).size, 2)
  } finally {
    controller.stop()
    await ready?.catch(() => {})
  }
})

test('Desktop V3 cursor-error repair queues behind overlapping hydrate before refetching transcript', async () => {
  let state = readyControllerState()
  state.selectedSessionId = sessionA.id
  state.sessionsById[sessionA.id] = { kind: 'full', session: { ...sessionA, message_count: 2 }, needsHydrate: false }
  state.projectionsBySession[sessionA.id] = { ...projectionA, last_event_seq: 1, projection_high_watermark_seq: 1 }
  state.messagesBySession[sessionA.id] = {
    items: [messageA1],
    byMessageId: { [messageA1.id]: 0 },
    byGlobalSeq: { [String(messageA1.global_seq)]: 0 },
    sourceMessageCount: 1,
    sourceLastMessageAt: messageA1.created_at,
    sourceProjectionHighWatermarkSeq: 1,
    source: 'network',
  }

  const sockets: FakeWebSocket[] = []
  const hydrateRequests: unknown[] = []
  let reconnectCount = 0
  let releaseFirstHydrate!: () => void
  const firstHydrate = new Promise<void>((resolve) => {
    releaseFirstHydrate = resolve
  })
  const reconnectProjection = { ...projectionA, last_event_seq: 2, projection_high_watermark_seq: 2 }

  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => true,
    reconnect: async () => {
      reconnectCount += 1
      return reconnectFixture({
        snapshot_endpoint_cursor: `cursor-reconnect-${reconnectCount}`,
        sessions_by_id: { [sessionA.id]: { ...sessionA, message_count: 2 } },
        projections_by_session: { [sessionA.id]: reconnectProjection },
        run_intents_by_session: {},
        current_run_intent_by_session: {},
        messages_by_session: {},
        events_by_session: {},
        session_order: [sessionA.id],
        workset_id: 'global-scope',
        realtime: {
          stream_path: '/v3/realtime/stream',
          resume: {
            protocol: 'v3.realtime',
            protocol_version: 1,
            kind: 'resume',
            endpoint_cursor: `cursor-reconnect-${reconnectCount}`,
            subscriptions: [{ subscription_id: 'sub-a', session_id: sessionA.id }],
            worksets: [{
              workset_id: 'global-scope',
              subscription_id: 'workset-sub',
              selector: { kind: 'global', global: true },
              resources: ['run_intents'],
              auto_subscribe_sessions: true,
            }],
          },
        },
      })
    },
    hydrate: async (input) => {
      hydrateRequests.push(input)
      if (hydrateRequests.length === 1) {
        await firstHydrate
        return hydrateSnapshotFixture({
          sessions_by_id: { [sessionA.id]: { ...sessionA, message_count: 1, last_message_at: messageA1.created_at } },
          projections_by_session: { [sessionA.id]: { ...projectionA, last_event_seq: 1, projection_high_watermark_seq: 1 } },
          session_order: [sessionA.id],
          messages_by_session: { [sessionA.id]: [messageA1] },
          selector: { kind: 'session_ids', session_ids: [sessionA.id] },
        })
      }
      return hydrateSnapshotFixture({
        sessions_by_id: { [sessionA.id]: { ...sessionA, message_count: 2 } },
        projections_by_session: { [sessionA.id]: reconnectProjection },
        session_order: [sessionA.id],
        messages_by_session: { [sessionA.id]: [messageA1, messageA2] },
        selector: { kind: 'session_ids', session_ids: [sessionA.id] },
      })
    },
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  let ready: Promise<void> | undefined
  try {
    ready = controller.start(sessionA.id)
    await waitFor(() => sockets.length === 1)
    sockets[0].open()
    await ready
    await waitFor(() => hydrateRequests.length === 1)

    sockets[0].emit({
      protocol: 'v3.realtime',
      protocol_version: 1,
      kind: 'cursor.error',
      reason: 'stale cursor',
    })
    await waitFor(() => sockets.length === 2)
    sockets[1].open()
    await flushAsyncWork()
    assert.equal(hydrateRequests.length, 1)

    releaseFirstHydrate()
    await waitFor(() => hydrateRequests.length === 2)
    await waitFor(() => state.messagesBySession[sessionA.id]?.items.length === 2)

    assert.deepEqual(state.messagesBySession[sessionA.id].items.map((message) => message.id), [messageA1.id, messageA2.id])
    assert.deepEqual(new Set(state.messagesBySession[sessionA.id].items.map((message) => message.id)).size, 2)
    assert.equal(state.realtime.endpointCursor, 'cursor-reconnect-2')
  } finally {
    controller.stop()
    releaseFirstHydrate()
    await ready?.catch(() => {})
  }
})

test('Desktop V3 realtime event persists before publication and cursor advance', async () => {
  assert.equal(await resetDesktopV3CacheDBForTests(), true)
  const owner = createDesktopV3CacheOwner({
    origin: 'https://desktop.example.test',
    accountScopeId: 'acct-a',
    userId: 'user-a',
    surface: 'desktop',
  })
  let state = readyControllerState()
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.projectionsBySession[sessionA.id] = projectionA
  state.sessionOrderByScope['global-scope'] = [sessionA.id]
  state.realtime.endpointCursor = 'cursor-reconnect'

  const sockets: FakeWebSocket[] = []
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => owner,
    saveActiveOwnerKey: () => true,
    now: () => 5_000,
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      run_intents_by_session: {},
      current_run_intent_by_session: {},
      session_order: [sessionA.id],
      workset_id: 'global-scope',
    }),
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  const ready = controller.start()
  await waitFor(() => sockets.length === 1)
  sockets[0].open()
  await ready

  sockets[0].emit({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    session_id: sessionA.id,
    event_type: 'session.assistant.delta',
    event: {
      id: 'evt-durable-before-publication',
      session_id: sessionA.id,
      event_type: 'session.assistant.delta',
      seq: 3,
      payload: {
        run_id: runIntentA.run_id,
        run_intent: runIntentA,
        delta: 'durable-first',
      },
      ts_unix_ms: 5_000,
    },
    projection: { ...projectionA, last_event_seq: 3, projection_high_watermark_seq: 3 },
    endpoint_cursor: 'cursor-live-3',
  })

  assert.equal(state.realtime.endpointCursor, 'cursor-reconnect')
  await waitFor(() => state.realtime.endpointCursor === 'cursor-live-3')
  const durableOwner = await readDesktopV3Owner(owner.key)
  assert.equal(
    durableOwner?.liveRunsBySession?.[sessionA.id]?.[runIntentA.run_id]?.assistantDraft?.content,
    'durable-first',
  )
  assert.equal(durableOwner?.realtimeEndpointCursor, 'cursor-live-3')
  assert.equal(state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id]?.assistantDraft?.content, 'durable-first')

  controller.stop()
  assert.equal(await resetDesktopV3CacheDBForTests(), true)
})

test('failed write leaves state and endpoint cursor unchanged', async () => {
  let state = readyControllerState()
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.projectionsBySession[sessionA.id] = projectionA
  state.sessionOrderByScope['global-scope'] = [sessionA.id]
  state.realtime.endpointCursor = 'cursor-reconnect'

  const sockets: FakeWebSocket[] = []
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => false,
    saveActiveOwnerKey: () => true,
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      run_intents_by_session: {},
      current_run_intent_by_session: {},
      session_order: [sessionA.id],
      workset_id: 'global-scope',
    }),
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  const ready = controller.start()
  await waitFor(() => sockets.length === 1)
  sockets[0].open()
  await ready

  sockets[0].emit(realtimeDeltaFrame('evt-failed-1', 1, 'cursor-failed-1', 'lost'))
  await flushAsyncWork()
  await waitFor(() => sockets[0].closed)

  assert.equal(state.realtime.endpointCursor, 'cursor-reconnect')
  assert.equal(state.eventsBySession[sessionA.id], undefined)
  assert.equal(state.liveRunsBySession[sessionA.id], undefined)
  controller.stop()
})

test('active-owner storage failure leaves state and cursor unchanged', async () => {
  let state = readyControllerState()
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.projectionsBySession[sessionA.id] = projectionA
  state.sessionOrderByScope['global-scope'] = [sessionA.id]
  state.realtime.endpointCursor = 'cursor-reconnect'

  const sockets: FakeWebSocket[] = []
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => false,
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      run_intents_by_session: {},
      current_run_intent_by_session: {},
      session_order: [sessionA.id],
      workset_id: 'global-scope',
    }),
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  const ready = controller.start()
  await waitFor(() => sockets.length === 1)
  sockets[0].open()
  await ready

  sockets[0].emit(realtimeDeltaFrame('evt-active-owner-failed', 1, 'cursor-failed-active-owner', 'lost'))
  await waitFor(() => sockets.length === 2)
  sockets[1].open()
  await waitFor(() => sockets[1].sent.length > 0)

  assert.equal(state.realtime.endpointCursor, 'cursor-reconnect')
  assert.equal(state.eventsBySession[sessionA.id], undefined)
  assert.equal(state.liveRunsBySession[sessionA.id], undefined)
  assert.equal((sockets[1].sent[0] as SessionV3RealtimeResumeWire).endpoint_cursor, 'cursor-reconnect')
  controller.stop()
})

test('A-E interleaved frames publish in endpoint order', async () => {
  let state = readyControllerState()
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.projectionsBySession[sessionA.id] = projectionA
  state.sessionOrderByScope['global-scope'] = [sessionA.id]
  state.realtime.endpointCursor = 'cursor-reconnect'

  const sockets: FakeWebSocket[] = []
  const writes: string[] = []
  const releases: Array<() => void> = []
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async (owner) => {
      writes.push(owner.realtimeEndpointCursor ?? '')
      await new Promise<void>((resolve) => releases.push(resolve))
      return true
    },
    saveActiveOwnerKey: () => true,
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      run_intents_by_session: {},
      current_run_intent_by_session: {},
      session_order: [sessionA.id],
      workset_id: 'global-scope',
    }),
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  const ready = controller.start()
  await waitFor(() => sockets.length === 1)
  sockets[0].open()
  await ready

  for (const label of ['A', 'B', 'C', 'D', 'E']) {
    sockets[0].emit(realtimeDeltaFrame(`evt-${label}`, label.charCodeAt(0), `cursor-${label}`, label))
  }

  await waitFor(() => writes.length === 1)
  assert.equal(state.realtime.endpointCursor, 'cursor-reconnect')
  for (let index = 0; index < releases.length; index += 1) {
    releases[index]()
    await waitFor(() => writes.length >= Math.min(index + 2, 5))
  }

  await waitFor(() => state.realtime.endpointCursor === 'cursor-E')
  assert.deepEqual(writes, ['cursor-A', 'cursor-B', 'cursor-C', 'cursor-D', 'cursor-E'])
  assert.deepEqual(state.eventsBySession[sessionA.id].map((event) => event.id), ['evt-A', 'evt-B', 'evt-C', 'evt-D', 'evt-E'])
  assert.equal(state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id]?.assistantDraft?.content, 'ABCDE')
  controller.stop()
})

test('five simultaneous streams persist, restore, and finalize deterministically', async () => {
  assert.equal(await resetDesktopV3CacheDBForTests(), true)
  const owner = testDesktopV3CacheOwner()
  let state = readyControllerState()
  const sessionIds = ['session-a', 'session-b', 'session-c', 'session-d', 'session-e']
  const sessions = Object.fromEntries(sessionIds.map((sessionId, index) => [sessionId, {
    ...sessionA,
    id: sessionId,
    title: `Session ${sessionId.toUpperCase()}`,
    created_at: 100 + index,
    updated_at: 200 + index,
    message_count: 0,
    last_message_at: 0,
  }]))
  const projections = Object.fromEntries(sessionIds.map((sessionId, index) => [sessionId, {
    ...projectionA,
    session_id: sessionId,
    last_event_seq: 0,
    projection_high_watermark_seq: 0,
    updated_at: 200 + index,
  }]))
  const runIntents = Object.fromEntries(sessionIds.map((sessionId, index) => [sessionId, [{
    ...runIntentA,
    session_id: sessionId,
    run_id: `run-${sessionId}`,
    status: 'running',
    event_seq: 0,
    created_at: 300 + index,
    updated_at: 300 + index,
  }]]))

  for (const sessionId of sessionIds) {
    state.sessionsById[sessionId] = { kind: 'full', session: sessions[sessionId], needsHydrate: false }
    state.projectionsBySession[sessionId] = projections[sessionId]
    state.runIntentsBySession[sessionId] = { [`run-${sessionId}`]: runIntents[sessionId][0] }
    state.currentRunIntentBySession[sessionId] = runIntents[sessionId][0]
  }
  state.sessionOrderByScope['global-scope'] = sessionIds
  state.selectedSessionId = 'session-a'
  state.realtime.endpointCursor = 'cursor-reconnect'

  const sockets: FakeWebSocket[] = []
  const hydrateRequests: string[][] = []
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => owner,
    saveActiveOwnerKey: () => true,
    now: () => 9_000,
    hydrate: async (input) => {
      hydrateRequests.push(input.selector.session_ids ?? [])
      return hydrateSnapshotFixture({ session_order: [], sessions_by_id: {}, projections_by_session: {}, messages_by_session: {} })
    },
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: sessions,
      projections_by_session: projections,
      run_intents_by_session: runIntents,
      current_run_intent_by_session: Object.fromEntries(sessionIds.map((sessionId) => [sessionId, runIntents[sessionId][0]])),
      session_order: sessionIds,
      workset_id: 'global-scope',
      realtime: {
        stream_path: '/v3/realtime/stream',
        resume: {
          protocol: 'v3.realtime',
          protocol_version: 1,
          kind: 'resume',
          endpoint_cursor: 'cursor-reconnect',
          subscriptions: [],
          worksets: [{
            workset_id: 'global-scope',
            subscription_id: 'workset-sub',
            selector: { kind: 'global', global: true },
            auto_subscribe_sessions: true,
          }],
        },
      },
    }),
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  const ready = controller.start(null)
  await waitFor(() => sockets.length === 1)
  sockets[0].open()
  await ready
  assert.equal(sockets.length, 1)

  const interleaved: Array<{ sessionId: string; seq: number; eventType: string; payload: Record<string, unknown> }> = [
    { sessionId: 'session-a', seq: 1, eventType: 'session.assistant.delta', payload: { delta: 'A-draft' } },
    { sessionId: 'session-b', seq: 1, eventType: 'session.reasoning.delta', payload: { reasoning_key: 'reasoning-b', text_delta: 'B-thinks' } },
    { sessionId: 'session-c', seq: 1, eventType: 'session.tool.started', payload: { call_id: 'call-c', tool_name: 'read', arguments: '{"file":"c"}' } },
    { sessionId: 'session-d', seq: 1, eventType: 'session.assistant.delta', payload: { delta: 'D-draft' } },
    { sessionId: 'session-e', seq: 1, eventType: 'session.tool.started', payload: { call_id: 'call-e', tool_name: 'grep', arguments: '{"query":"e"}' } },
    { sessionId: 'session-a', seq: 2, eventType: 'session.tool.started', payload: { call_id: 'call-a', tool_name: 'read', arguments: '{"file":"a"}' } },
    { sessionId: 'session-b', seq: 2, eventType: 'session.assistant.delta', payload: { delta: 'B-answer' } },
    { sessionId: 'session-c', seq: 2, eventType: 'session.tool.delta', payload: { call_id: 'call-c', output_delta: 'C-output' } },
    { sessionId: 'session-d', seq: 2, eventType: 'session.reasoning.delta', payload: { reasoning_key: 'reasoning-d', text_delta: 'D-thinks' } },
    { sessionId: 'session-e', seq: 2, eventType: 'session.tool.delta', payload: { call_id: 'call-e', output_delta: 'E-output' } },
  ]

  interleaved.forEach((frame, index) => {
    sockets[0].emit(cp9RealtimeEventFrame({
      ...frame,
      endpointCursor: `cursor-cp9-${index + 1}`,
    }))
  })

  await waitFor(() => state.realtime.endpointCursor === 'cursor-cp9-10')
  const persisted = await readDesktopV3Owner(owner.key)
  assert.equal(persisted?.realtimeEndpointCursor, 'cursor-cp9-10')
  assert.deepEqual(Object.keys(persisted?.liveRunsBySession ?? {}).sort(), sessionIds)
  assert.equal(persisted?.liveRunsBySession?.['session-a']?.['run-session-a']?.assistantSegments?.[0]?.content, 'A-draft')
  assert.equal(persisted?.liveRunsBySession?.['session-b']?.['run-session-b']?.reasoning?.text, 'B-thinks')
  assert.equal(persisted?.liveRunsBySession?.['session-c']?.['run-session-c']?.toolCallsByCallId['call-c']?.outputText, 'C-output')
  assert.equal(persisted?.liveRunsBySession?.['session-e']?.['run-session-e']?.toolCallsByCallId['call-e']?.outputText, 'E-output')

  assert.ok(persisted)
  const restored = desktopV3CacheReducer(createEmptyDesktopV3CacheState(), {
    type: 'desktopV3Cache.restore',
    owner: persisted,
  })
  assert.deepEqual(restored.liveRunsBySession, persisted.liveRunsBySession)
  assert.equal(selectRenderedSessionMessages(restored, 'session-e').liveRuns[0]?.toolCallsByCallId['call-e']?.outputText, 'E-output')
  assert.deepEqual(hydrateRequests, [])

  for (const sessionId of sessionIds) {
    const seq = 3
    sockets[0].emit(cp9RealtimeEventFrame({
      sessionId,
      seq,
      eventType: 'session.assistant.completed',
      endpointCursor: `cursor-cp9-final-${sessionId}`,
      payload: {
        status: 'completed',
        message: {
          id: `msg-final-${sessionId}`,
          session_id: sessionId,
          global_seq: seq,
          role: 'assistant',
          content: `final ${sessionId}`,
          metadata: {},
          created_at: 10_000 + seq,
        },
        run_intent: { ...runIntents[sessionId][0], status: 'completed', event_seq: seq },
      },
    }))
  }
  await waitFor(() => state.realtime.endpointCursor === 'cursor-cp9-final-session-e')

  for (const sessionId of sessionIds) {
    const rendered = selectRenderedSessionMessages(state, sessionId)
    assert.equal(rendered.committed.filter((message) => message.role === 'assistant').length, 1)
    assert.equal(rendered.liveRuns.length, 1)
    assert.equal(rendered.liveRuns[0].assistantDraft, undefined)
    assert.equal(rendered.liveRuns[0].assistantSegments, undefined)
  }
  assert.equal(state.liveRunsBySession['session-a']?.['run-session-a']?.toolCallsByCallId['call-a']?.toolName, 'read')
  assert.equal(state.liveRunsBySession['session-b']?.['run-session-b']?.reasoning?.text, 'B-thinks')
  assert.equal(state.liveRunsBySession['session-c']?.['run-session-c']?.toolCallsByCallId['call-c']?.outputText, 'C-output')
  assert.equal(state.liveRunsBySession['session-d']?.['run-session-d']?.reasoning?.text, 'D-thinks')
  assert.equal(state.liveRunsBySession['session-e']?.['run-session-e']?.toolCallsByCallId['call-e']?.outputText, 'E-output')

  controller.stop()
  assert.equal(await resetDesktopV3CacheDBForTests(), true)
})

test('final message and overlay cleanup are one IndexedDB transaction', async () => {
  assert.equal(await resetDesktopV3CacheDBForTests(), true)
  const owner = testDesktopV3CacheOwner()
  let state = readyControllerState()
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.projectionsBySession[sessionA.id] = projectionA
  state.sessionOrderByScope['global-scope'] = [sessionA.id]
  state.realtime.endpointCursor = 'cursor-reconnect'

  const sockets: FakeWebSocket[] = []
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => owner,
    saveActiveOwnerKey: () => true,
    now: () => 7_000,
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      run_intents_by_session: {},
      current_run_intent_by_session: {},
      session_order: [sessionA.id],
      workset_id: 'global-scope',
    }),
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  const ready = controller.start()
  await waitFor(() => sockets.length === 1)
  sockets[0].open()
  await ready

  sockets[0].emit(realtimeDeltaFrame('evt-final-delta', 1, 'cursor-final-delta', 'done'))
  await waitFor(() => state.realtime.endpointCursor === 'cursor-final-delta')
  sockets[0].emit(realtimeFinalMessageFrame('evt-final-message', 2, 'cursor-final-message'))
  await waitFor(() => state.realtime.endpointCursor === 'cursor-final-message')

  const durableOwner = await readDesktopV3Owner(owner.key)
  assert.equal(durableOwner?.realtimeEndpointCursor, 'cursor-final-message')
  assert.equal(durableOwner?.liveRunsBySession?.[sessionA.id]?.[runIntentA.run_id], undefined)
  assert.equal(state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id], undefined)
  assert.equal(state.messagesBySession[sessionA.id]?.items[0]?.id, 'message-final')
  controller.stop()
  assert.equal(await resetDesktopV3CacheDBForTests(), true)
})

test('repair events use the durable stream committer', async () => {
  let state = readyControllerState()
  const owner = testDesktopV3CacheOwner()
  const sockets: FakeWebSocket[] = []
  const persistedDrafts: string[] = []
  const repairEvent: V3SessionEvent = {
    id: 'evt-repair-delta',
    session_id: sessionA.id,
    event_type: 'session.assistant.delta',
    seq: 3,
    payload: {
      run_id: runIntentA.run_id,
      run_intent: { ...runIntentA, event_seq: 3 },
      delta: 'repair-durable',
    },
    ts_unix_ms: 3,
  }

  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => owner,
    writeOwnerAndTails: async (persistedOwner) => {
      persistedDrafts.push(
        persistedOwner.liveRunsBySession?.[sessionA.id]?.[runIntentA.run_id]?.assistantDraft?.content ?? '',
      )
      return true
    },
    saveActiveOwnerKey: () => true,
    readEventsPage: async () => ({
      ok: true,
      session_id: sessionA.id,
      events: [repairEvent],
      projection: { ...projectionA, last_event_seq: 3, projection_high_watermark_seq: 3 },
      high_watermark_seq: 3,
      next_seq: 4,
      applied_seq: 3,
    }),
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      run_intents_by_session: { [sessionA.id]: [runIntentA] },
      current_run_intent_by_session: { [sessionA.id]: runIntentA },
      session_order: [sessionA.id],
      workset_id: 'global-scope',
    }),
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  const ready = controller.start()
  await waitFor(() => sockets.length === 1)
  sockets[0].open()
  await ready

  await waitFor(() => state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id]?.assistantDraft?.content === 'repair-durable')
  assert.ok(persistedDrafts.includes('repair-durable'))
  controller.stop()
})

test('commit failure reopens from previous durable cursor', async () => {
  let state = readyControllerState()
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.projectionsBySession[sessionA.id] = projectionA
  state.sessionOrderByScope['global-scope'] = [sessionA.id]
  state.realtime.endpointCursor = 'cursor-reconnect'

  const sockets: FakeWebSocket[] = []
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => false,
    saveActiveOwnerKey: () => true,
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      run_intents_by_session: {},
      current_run_intent_by_session: {},
      session_order: [sessionA.id],
      workset_id: 'global-scope',
    }),
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  const ready = controller.start()
  await waitFor(() => sockets.length === 1)
  sockets[0].open()
  await ready

  sockets[0].emit(realtimeDeltaFrame('evt-reopen-failed', 1, 'cursor-failed', 'lost'))
  await waitFor(() => sockets.length === 2)
  sockets[1].open()
  await waitFor(() => sockets[1].sent.length > 0)

  assert.equal((sockets[1].sent[0] as SessionV3RealtimeResumeWire).endpoint_cursor, 'cursor-reconnect')
  assert.equal(state.realtime.endpointCursor, 'cursor-reconnect')
  controller.stop()
})

test('workset discovery commits before background hydrate starts', async () => {
  let state = readyControllerState()
  state.realtime.endpointCursor = 'cursor-reconnect'

  const sockets: FakeWebSocket[] = []
  const releases: Array<() => void> = []
  const hydrateRequests: string[][] = []
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => {
      await new Promise<void>((resolve) => releases.push(resolve))
      return true
    },
    saveActiveOwnerKey: () => true,
    hydrate: async (input) => {
      hydrateRequests.push(input.session_ids)
      return hydrateSnapshotFixture({
        sessions_by_id: { 'session-discovered': { ...sessionA, id: 'session-discovered' } },
        projections_by_session: { 'session-discovered': { ...projectionA, session_id: 'session-discovered' } },
        session_order: ['session-discovered'],
        messages_by_session: { 'session-discovered': [] },
        selector: { kind: 'session_ids', session_ids: ['session-discovered'] },
      })
    },
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: {},
      projections_by_session: {},
      run_intents_by_session: {},
      current_run_intent_by_session: {},
      session_order: [],
      workset_id: 'global-scope',
    }),
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  const ready = controller.start()
  await waitFor(() => sockets.length === 1)
  sockets[0].open()
  await ready

  sockets[0].emit({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'workset.session.discovered',
    workset_id: 'global-scope',
    session_id: 'session-discovered',
    subscription_id: 'sub-discovered',
    auto_subscribed: true,
    endpoint_cursor: 'cursor-discovered',
  })

  await waitFor(() => releases.length === 1)
  assert.deepEqual(hydrateRequests, [])
  releases[0]()
  await waitFor(() => hydrateRequests.length === 1)
  assert.equal(state.sessionsById['session-discovered']?.kind, 'full')
  controller.stop()
})

test('repair and live events racing through one queue remain ordered', async () => {
  let state = readyControllerState()
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.projectionsBySession[sessionA.id] = projectionA
  state.sessionOrderByScope['global-scope'] = [sessionA.id]
  state.liveRunsBySession[sessionA.id] = {
    [runIntentA.run_id]: {
      sessionId: sessionA.id,
      runId: runIntentA.run_id,
      status: 'running',
      toolCallsByCallId: {},
      lastEventSeqSeen: 3,
      assistantDraft: { content: 'abc', updatedAt: 3, timelineSeq: 1 },
    },
  }

  const persistedDrafts: string[] = []
  const commit = new DesktopV3StreamCommitController({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async (owner) => {
      persistedDrafts.push(
        owner.liveRunsBySession?.[sessionA.id]?.[runIntentA.run_id]?.assistantDraft?.content ?? '',
      )
      return true
    },
    saveActiveOwnerKey: () => true,
    now: () => 9_000,
  })

  const repair = commit.commitActions([{
    type: 'liveRun.mergeRepairEvents',
    sessionId: sessionA.id,
    runId: runIntentA.run_id,
    events: [{
      source: 'sync-stream',
      sessionId: sessionA.id,
      eventType: 'session.assistant.delta',
      sessionEvent: {
        id: 'evt-repair-4',
        session_id: sessionA.id,
        seq: 4,
        event_type: 'session.assistant.delta',
        payload: { run_id: runIntentA.run_id, delta: 'd' },
        ts_unix_ms: 4,
      },
      projection: { ...projectionA, last_event_seq: 4, projection_high_watermark_seq: 4 },
      payload: { run_id: runIntentA.run_id, delta: 'd' },
    }],
  }])
  const live = commit.commitActions([{
    type: 'realtime.applyEvent',
    endpointCursor: 'cursor-live-5',
    event: {
      source: 'realtime',
      sessionId: sessionA.id,
      eventType: 'session.assistant.delta',
      sessionEvent: {
        id: 'evt-live-5',
        session_id: sessionA.id,
        seq: 5,
        event_type: 'session.assistant.delta',
        payload: { run_id: runIntentA.run_id, delta: 'e' },
        ts_unix_ms: 5,
      },
      projection: { ...projectionA, last_event_seq: 5, projection_high_watermark_seq: 5 },
      payload: { run_id: runIntentA.run_id, delta: 'e' },
    },
  }])

  await Promise.all([repair, live])

  const liveRun = state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id]
  assert.equal(liveRun?.assistantDraft?.content, 'abcde')
  assert.equal(liveRun?.lastEventSeqSeen, 5)
  assert.equal(state.realtime.endpointCursor, 'cursor-live-5')
  assert.deepEqual(persistedDrafts, ['abcd', 'abcde'])
  assert.deepEqual(
    state.eventsBySession[sessionA.id].map((event) => `${event.seq}:${event.id}`),
    ['4:evt-repair-4', '5:evt-live-5'],
  )
})

test('Desktop V3 has exactly one production live-event ingress', async () => {
  const realtimeController = await readSource(
    'web/src/features/desktop/realtime/v3-realtime-controller.ts',
  )
  const cacheReducer = await readSource(
    'web/src/features/desktop/state/desktop-v3-cache-reducer.ts',
  )

  assert.match(realtimeController, /commitDesktopV3StreamFrame/)
  assert.doesNotMatch(
    realtimeController,
    /this\.dispatch\(\{\s*type:\s*['"]realtime\.applyEvent['"]/,
  )
  assert.doesNotMatch(realtimeController, /\/v3\/sessions\/.*\/stream/)
  assert.doesNotMatch(cacheReducer, /outboxRecordToCacheEvent\(outbox\)/)
})

test('Desktop V3 transport status maps to cache status', () => {
  assert.equal(mapTransportStatus('stopped'), 'closed')
  assert.equal(mapTransportStatus('closed'), 'closed')
  assert.equal(mapTransportStatus('connecting'), 'connecting')
  assert.equal(mapTransportStatus('open'), 'open')
  assert.equal(mapTransportStatus('reopening'), 'reconnecting')
  assert.equal(mapTransportStatus('rehydrating'), 'reconnecting')
  assert.equal(mapTransportStatus('stale'), 'stale')
  assert.equal(mapTransportStatus('error'), 'error')
})

function cp9RealtimeEventFrame(input: {
  sessionId: string
  seq: number
  eventType: string
  endpointCursor: string
  payload: Record<string, unknown>
}): RealtimeMessage {
  const runId = `run-${input.sessionId}`
  return {
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    session_id: input.sessionId,
    event_type: input.eventType,
    event: {
      id: `evt-cp9-${input.sessionId}-${input.seq}-${input.eventType}`,
      session_id: input.sessionId,
      event_type: input.eventType,
      seq: input.seq,
      payload: {
        run_id: runId,
        ...input.payload,
      },
      ts_unix_ms: input.seq,
    },
    projection: {
      ...projectionA,
      session_id: input.sessionId,
      last_event_seq: input.seq,
      projection_high_watermark_seq: input.seq,
    },
    endpoint_cursor: input.endpointCursor,
  }
}

function realtimeDeltaFrame(id: string, seq: number, endpointCursor: string, delta: string): RealtimeMessage {
  return {
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    session_id: sessionA.id,
    event_type: 'session.assistant.delta',
    event: {
      id,
      session_id: sessionA.id,
      event_type: 'session.assistant.delta',
      seq,
      payload: {
        run_id: runIntentA.run_id,
        run_intent: runIntentA,
        delta,
      },
      ts_unix_ms: seq,
    },
    projection: { ...projectionA, last_event_seq: seq, projection_high_watermark_seq: seq },
    endpoint_cursor: endpointCursor,
  }
}

function realtimeFinalMessageFrame(id: string, seq: number, endpointCursor: string): RealtimeMessage {
  return {
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    session_id: sessionA.id,
    event_type: 'session.assistant.completed',
    event: {
      id,
      session_id: sessionA.id,
      event_type: 'session.assistant.completed',
      seq,
      payload: {
        run_id: runIntentA.run_id,
        run_intent: { ...runIntentA, status: 'completed', event_seq: seq },
        message: {
          id: 'message-final',
          session_id: sessionA.id,
          global_seq: seq,
          role: 'assistant',
          content: 'done',
          metadata: {},
          created_at: seq,
        },
      },
      ts_unix_ms: seq,
    },
    projection: { ...projectionA, last_event_seq: seq, projection_high_watermark_seq: seq },
    endpoint_cursor: endpointCursor,
  }
}

test('active repair starts after restored overlay sequence', async () => {
  let state: DesktopV3CacheState = readyControllerState()
  const activeIntent = { ...runIntentA, status: 'running', event_seq: 5, updated_at: 5 }
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.projectionsBySession[sessionA.id] = { ...projectionA, last_event_seq: 3, projection_high_watermark_seq: 3 }
  state.currentRunIntentBySession[sessionA.id] = activeIntent
  state.liveRunsBySession[sessionA.id] = {
    [runIntentA.run_id]: {
      sessionId: sessionA.id,
      runId: runIntentA.run_id,
      status: 'running',
      toolCallsByCallId: {},
      lastEventSeqSeen: 3,
      assistantDraft: { content: 'abc', updatedAt: 3, timelineSeq: 1 },
    },
  }

  const event4: V3SessionEvent = {
    id: 'evt-repair-4',
    session_id: sessionA.id,
    seq: 4,
    event_type: 'session.assistant.delta',
    payload: { run_id: runIntentA.run_id, delta: 'd' },
    ts_unix_ms: 4,
  }
  const event5: V3SessionEvent = {
    id: 'evt-repair-5',
    session_id: sessionA.id,
    seq: 5,
    event_type: 'session.assistant.delta',
    payload: { run_id: runIntentA.run_id, delta: 'e' },
    ts_unix_ms: 5,
  }
  const sockets: FakeWebSocket[] = []
  const readRequests: Array<{ sessionId: string; afterSeq: number; limit?: number }> = []

  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => true,
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: { ...projectionA, last_event_seq: 5, projection_high_watermark_seq: 5 } },
      run_intents_by_session: { [sessionA.id]: [activeIntent] },
      current_run_intent_by_session: { [sessionA.id]: activeIntent },
      session_order: [sessionA.id],
      workset_id: 'global-scope',
    }),
    readEventsPage: async (input) => {
      readRequests.push(input)
      return {
        ok: true,
        session_id: input.sessionId,
        events: [event4, event5],
        projection: { ...projectionA, last_event_seq: 5, projection_high_watermark_seq: 5 },
        high_watermark_seq: 5,
        next_seq: 6,
        applied_seq: 5,
      }
    },
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  const ready = controller.start(null)
  await waitFor(() => readRequests.length === 1)
  assert.deepEqual(readRequests.map((request) => [request.sessionId, request.afterSeq]), [[sessionA.id, 3]])
  await waitFor(() => state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id]?.assistantDraft?.content === 'abcde')
  assert.equal(state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id]?.lastEventSeqSeen, 5)
  assert.equal(sockets.length, 1)
  assert.equal(sockets[0].sent.length, 0)
  sockets[0].open()
  await ready
  controller.stop()
})

test('slow-consumer recovery resumes from last durable cursor', async () => {
  let state = readyControllerState()
  state.realtime.endpointCursor = 'cursor-5'
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.projectionsBySession[sessionA.id] = projectionA
  state.sessionOrderByScope['global-scope'] = [sessionA.id]

  const sockets: FakeWebSocket[] = []
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    resolveOwner: () => testDesktopV3CacheOwner(),
    writeOwnerAndTails: async () => true,
    saveActiveOwnerKey: () => true,
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-12',
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      run_intents_by_session: {},
      current_run_intent_by_session: {},
      session_order: [sessionA.id],
      workset_id: 'global-scope',
      realtime: {
        stream_path: '/v3/realtime/stream',
        resume: {
          protocol: 'v3.realtime',
          protocol_version: 1,
          kind: 'resume',
          endpoint_cursor: 'cursor-12',
          subscriptions: [{ subscription_id: 'sub-a', session_id: sessionA.id }],
          worksets: [],
        },
      },
    }),
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  const ready = controller.start(null)
  await waitFor(() => sockets.length === 1)
  sockets[0].open()
  await ready
  assert.equal((sockets[0].sent[0] as SessionV3RealtimeResumeWire).endpoint_cursor, 'cursor-5')

  sockets[0].emit({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'slow_consumer.reconnect_required',
    reason: 'client fell behind',
  })
  await waitFor(() => sockets.length === 2)
  sockets[1].open()
  await waitFor(() => sockets[1].sent.length > 0)

  assert.equal((sockets[1].sent[0] as SessionV3RealtimeResumeWire).endpoint_cursor, 'cursor-5')
  assert.equal(state.realtime.endpointCursor, 'cursor-5')
  controller.stop()
})

function testDesktopV3CacheOwner() {
  return createDesktopV3CacheOwner({
    origin: 'https://desktop.example.test',
    accountScopeId: 'acct-test',
    userId: 'user-test',
    surface: 'desktop',
  })
}

function readyControllerState(): DesktopV3CacheState {
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'global-scope' }
  state.syncScopesById['global-scope'] = {
    scopeId: 'global-scope',
    surface: 'desktop',
    streamKind: 'v3.sync.snapshot',
    selectorFilterHash: 'global-hash',
    resourceSet: 'run_intents',
    selector: { kind: 'global', global: true },
    endpointCursor: 'cursor-bootstrap',
    replayPath: '/v3/sync/stream',
    replayTransport: 'http_post',
    needsBootstrap: false,
  }
  return state
}

async function readSource(path: string): Promise<string> {
  const localPath = path.startsWith('web/') ? path.slice('web/'.length) : path
  return readFile(localPath, 'utf8')
}

async function waitFor(predicate: () => boolean, timeoutMs = 1_000): Promise<void> {
  const started = Date.now()
  while (!predicate()) {
    if (Date.now() - started > timeoutMs) {
      throw new Error('timed out waiting for condition')
    }
    await new Promise((resolve) => setTimeout(resolve, 5))
  }
}

async function flushAsyncWork(): Promise<void> {
  await new Promise((resolve) => setTimeout(resolve, 0))
}
