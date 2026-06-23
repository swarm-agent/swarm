import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

import { DesktopV3RealtimeTransport, SESSION_CONNECT_ACK_TIMEOUT_MS } from '../session-v3/transport'
import { selectRenderedSessionMessages } from '../state/desktop-v3-cache-selectors'
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

  socket.sent = []
  const connect = transport.subscribeSession({
    session_id: 'session-a',
    subscription_id: 'sub-a',
    endpoint_cursor: 'v3c1.test_payload_2.test_signature_2',
  })
  connect.catch(() => undefined)
  await flushAsyncWork()

  assert.equal(resumes.length, 1, 'open-socket subscription must not send another resume')
  assert.deepEqual(socket.sent.map((frame) => (frame as RealtimeMessage).kind), ['subscribe.session'])
  const subscription = socket.sent[0] as RealtimeMessage
  assert.equal(subscription.endpoint_cursor, 'v3c1.test_payload_2.test_signature_2')
  assert.equal('after_seq' in subscription, false)
  assert.equal('after_rev' in subscription, false)
  socket.emit({ protocol: 'v3.realtime', protocol_version: 1, kind: 'replay.complete', session_id: 'session-a', subscription_id: 'sub-a', endpoint_cursor: 'v3c1.test_payload_3.test_signature_3' })
  await connect
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
  const connect = transport.subscribeSession({
    session_id: 'session-a',
    subscription_id: 'sub-a',
    endpoint_cursor: 'session-cursor-a',
  })
  connect.catch(() => undefined)

  await transport.start()
  socket.open()

  assert.equal(resumes.length, 1)
  assert.equal(resumes[0].kind, 'resume')
  assert.equal(resumes[0].endpoint_cursor, 'v3c1.bootstrap_payload.bootstrap_signature')
  assert.deepEqual(resumes[0].subscriptions?.map((subscription) => subscription.session_id), ['session-a'])
  assert.deepEqual(resumes[0].worksets?.map((workset) => workset.workset_id), ['desktop-workset'])
  assert.equal('after_seq' in resumes[0], false)
  socket.emit({ protocol: 'v3.realtime', protocol_version: 1, kind: 'replay.complete', session_id: 'session-a', subscription_id: 'sub-a', endpoint_cursor: 'v3c1.after_replay.payload.signature' })
  await connect
  transport.stop()
})

test('Desktop V3 realtime controller waits for delayed sidebar bootstrap before opening realtime', async () => {
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
  assert.equal(reconnectCount, 0)
  assert.equal(sockets[0].readyState, FakeWebSocket.CONNECTING)

  sockets[0].open()
  await readyPromise
  assert.equal(ready, true)
  assert.equal(reconnectCount, 0)
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
  assert.equal(reconnectCount, 0)
  await flushAsyncWork()
  assert.equal(immediateReady, false)

  sockets[0].open()
  const controller = await immediateSendReady
  assert.equal(immediateReady, true)
  assert.equal(controller, await requireDesktopV3RealtimeControllerReady())
  assert.equal(sockets.length, 1)
  await lease.ready
  assert.equal(reconnectCount, 0)
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
    'startup resume must contain active sessionA plus selected sessionB only',
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

test('warm runtime resumes from in-memory realtime cursor', async () => {
  let state = readyControllerState()
  state.realtime.endpointCursor = 'cursor-memory-warm'
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
  assert.deepEqual(openCursors, ['cursor-memory-warm'])
  assert.equal(resume.endpoint_cursor, 'cursor-memory-warm')
  assert.equal(resume.subscriptions?.[0]?.endpoint_cursor, 'cursor-memory-warm')
  assert.equal(state.realtime.endpointCursor, 'cursor-memory-warm')
  controller.stop()
})

test('expired in-memory cursor takes the explicit cursor-error recovery path', async () => {
  let state = readyControllerState()
  state.realtime.endpointCursor = 'cursor-memory-expired'
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
  assert.equal((sockets[0].sent[0] as SessionV3RealtimeResumeWire).endpoint_cursor, 'cursor-bootstrap')

  sockets[0].emit({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'cursor.error',
    reason: 'expired cursor',
  })
  await waitFor(() => sockets.length === 2)
  sockets[1].open()
  await waitFor(() => sockets[1].sent.length > 0)

  assert.equal((sockets[1].sent[0] as SessionV3RealtimeResumeWire).endpoint_cursor, 'cursor-reconnect-1')
  assert.equal(state.realtime.endpointCursor, 'cursor-reconnect-1')
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
    assert.equal(state.realtime.endpointCursor, 'cursor-reconnect-1')
  } finally {
    controller.stop()
    releaseFirstHydrate()
    await ready?.catch(() => {})
  }
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

test('terminal reasoning completed flushes latest coalesced state immediately', async () => {
  let state = readyControllerState()
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.projectionsBySession[sessionA.id] = projectionA
  state.sessionOrderByScope['global-scope'] = [sessionA.id]
  state.runIntentsBySession[sessionA.id] = { [runIntentA.run_id]: runIntentA }
  state.currentRunIntentBySession[sessionA.id] = runIntentA

  const commit = new DesktopV3StreamCommitController({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
  })

  await commit.commitActions([realtimeReasoningAction({
    id: 'evt-reasoning-delta',
    seq: 1,
    endpointCursor: 'cursor-reasoning-delta',
    payload: { text_delta: 'latest thinking' },
  })])

  await commit.commitActions([realtimeReasoningAction({
    id: 'evt-reasoning-completed',
    seq: 2,
    endpointCursor: 'cursor-reasoning-completed',
    eventType: 'session.reasoning.completed',
  })])

  assert.equal(
    state.messagesBySession[sessionA.id]?.items.find((message) => message.role === 'reasoning')?.content,
    'latest thinking',
  )
  assert.equal(state.realtime.endpointCursor, 'cursor-reasoning-completed')

})

test('repair events use the durable stream committer', async () => {
  let state = readyControllerState()
  const sockets: FakeWebSocket[] = []
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
  const hydrateRequests: string[][] = []
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
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

  const commit = new DesktopV3StreamCommitController({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
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

function realtimeReasoningAction(input: {
  id: string
  seq: number
  endpointCursor: string
  eventType?: 'session.reasoning.delta' | 'session.reasoning.completed'
  payload?: Record<string, unknown>
}): DesktopV3CacheAction {
  const eventType = input.eventType ?? 'session.reasoning.delta'
  return {
    type: 'realtime.applyEvent',
    endpointCursor: input.endpointCursor,
    event: {
      source: 'realtime',
      sessionId: sessionA.id,
      eventType,
      sessionEvent: {
        id: input.id,
        session_id: sessionA.id,
        event_type: eventType,
        seq: input.seq,
        payload: {
          run_id: runIntentA.run_id,
          run_intent: { ...runIntentA, event_seq: input.seq },
          reasoning_key: 'summary-1',
          ...input.payload,
        },
        ts_unix_ms: input.seq,
      },
      projection: { ...projectionA, last_event_seq: input.seq, projection_high_watermark_seq: input.seq },
      payload: {
        run_id: runIntentA.run_id,
        run_intent: { ...runIntentA, event_seq: input.seq },
        reasoning_key: 'summary-1',
        ...(eventType === 'session.reasoning.completed' ? { text: 'latest thinking' } : undefined),
        ...input.payload,
      },
    },
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
  let reconnectCount = 0
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    reconnect: async () => {
      reconnectCount += 1
      return reconnectFixture({
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
  assert.equal((sockets[0].sent[0] as SessionV3RealtimeResumeWire).endpoint_cursor, 'cursor-bootstrap')
  state.realtime.endpointCursor = 'cursor-5'

  sockets[0].emit({
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'slow_consumer.reconnect_required',
    reason: 'client fell behind',
  })
  await waitFor(() => sockets.length === 2)
  sockets[1].open()
  await waitFor(() => sockets[1].sent.length > 0)

  assert.equal(reconnectCount, 1)
  assert.equal((sockets[1].sent[0] as SessionV3RealtimeResumeWire).endpoint_cursor, 'cursor-5')
  assert.equal(state.realtime.endpointCursor, 'cursor-5')
  controller.stop()
})


test('Desktop V3 cold startup opens realtime from bootstrap cursor without HTTP reconnect', async () => {
  let state = readyControllerState()
  state.realtime.endpointCursor = 'cursor-bootstrap'
  state.selectedSessionId = sessionA.id
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
    reconnect: async () => {
      reconnectCount += 1
      return reconnectFixture({
        snapshot_endpoint_cursor: 'cursor-reconnect',
        sessions_by_id: { [sessionA.id]: sessionA },
        projections_by_session: { [sessionA.id]: projectionA },
        run_intents_by_session: {},
        current_run_intent_by_session: {},
        session_order: [sessionA.id],
      })
    },
    hydrate: async () => hydrateSnapshotFixture({
      sessions_by_id: { [sessionA.id]: sessionA },
      projections_by_session: { [sessionA.id]: projectionA },
      session_order: [sessionA.id],
      messages_by_session: { [sessionA.id]: [messageA1, messageA2] },
      run_intents_by_session: {},
    }),
    openSocket: ({ endpointCursor }) => {
      assert.equal(endpointCursor, 'cursor-bootstrap')
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  try {
    const ready = controller.start(sessionA.id)
    ready.catch(() => undefined)
    await waitFor(() => sockets.length === 1)
    assert.equal(reconnectCount, 0)
    sockets[0].open()
    await ready
    assert.equal(reconnectCount, 0)
    assert.equal((sockets[0].sent[0] as RealtimeMessage).endpoint_cursor, 'cursor-bootstrap')
  } finally {
    controller.stop()
  }
})

test('Desktop V3 cold startup resume contains selected plus active sessions, not global membership', async () => {
  let state = readyControllerState()
  state.realtime.endpointCursor = 'cursor-bootstrap'
  state.selectedSessionId = 'selected-inactive'
  const inactiveIds = Array.from({ length: 300 }, (_, index) => `inactive-${index}`)
  const activeA = 'active-a'
  const activeB = 'active-b'
  state.sessionOrderByScope['global-scope'] = ['selected-inactive', activeA, ...inactiveIds, activeB]
  state.currentRunIntentBySession[activeA] = { session_id: activeA, run_id: 'run-a', status: 'running', created_at: 1, updated_at: 1, event_seq: 1 }
  state.currentRunIntentBySession[activeB] = { session_id: activeB, run_id: 'run-b', status: 'pending_executor', created_at: 1, updated_at: 2, event_seq: 1 }
  const reconnectSubscriptions = state.sessionOrderByScope['global-scope'].map((sessionId) => ({
    session_id: sessionId,
    subscription_id: `desktop:test:session:${sessionId}`,
    endpoint_cursor: 'cursor-reconnect',
  }))
  const sockets: FakeWebSocket[] = []
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    hydrate: async () => hydrateSnapshotFixture({
      sessions_by_id: {},
      projections_by_session: {},
      messages_by_session: {},
      session_order: [],
    }),
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: {},
      projections_by_session: {},
      run_intents_by_session: {},
      current_run_intent_by_session: {},
      session_order: state.sessionOrderByScope['global-scope'],
      realtime: {
        stream_path: '/v3/realtime/stream',
        resume: {
          protocol: 'v3.realtime',
          protocol_version: 1,
          kind: 'resume',
          endpoint_cursor: 'cursor-reconnect',
          subscriptions: reconnectSubscriptions,
          worksets: [{
            workset_id: 'global-scope',
            subscription_id: 'workset-sub',
            selector: { kind: 'global', global: true },
            resources: ['membership', 'projections', 'run_intents', 'sessions', 'tombstones'],
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

  try {
    const ready = controller.start('selected-inactive')
    ready.catch(() => undefined)
    await waitFor(() => sockets.length === 1)
    sockets[0].open()
    await ready
    const resume = sockets[0].sent[0] as RealtimeMessage
    assert.deepEqual(resume.subscriptions?.map((subscription) => subscription.session_id), ['selected-inactive', activeA, activeB])
    assert.deepEqual(resume.subscriptions?.map((subscription) => subscription.subscription_id?.endsWith(`:session:${subscription.session_id}`)), [
      true,
      true,
      true,
    ])
    assert.equal(resume.worksets?.length, 1)
    assert.deepEqual(resume.worksets?.[0]?.selector, { kind: 'global', global: true })
  } finally {
    controller.stop()
  }
})

test('Desktop V3 open transport adds one session with subscribe.session and no resume', async () => {
  const socket = new FakeWebSocket()
  const transport = new DesktopV3RealtimeTransport({
    getEndpointCursor: () => 'cursor-0',
    openSocket: () => socket as unknown as WebSocket,
    livenessTimeoutMs: 60_000,
  })
  await transport.start()
  socket.open()
  socket.sent = []

  const connect = transport.subscribeSession({
    session_id: 'session-new',
    subscription_id: 'sub-new',
    endpoint_cursor: 'cursor-0',
  })
  connect.catch(() => undefined)
  await flushAsyncWork()

  assert.deepEqual(socket.sent.map((frame) => (frame as RealtimeMessage).kind), ['subscribe.session'])
  assert.equal(socket.sent.filter((frame) => (frame as RealtimeMessage).kind === 'resume').length, 0)
  transport.unsubscribeSession('session-new')
  assert.deepEqual(socket.sent.map((frame) => (frame as RealtimeMessage).kind), ['subscribe.session', 'unsubscribe.session'])
  assert.equal(socket.sent.filter((frame) => (frame as RealtimeMessage).kind === 'resume').length, 0)
  const unsubscribe = socket.sent[1] as RealtimeMessage
  assert.equal(unsubscribe.session_id, 'session-new')
  assert.equal(unsubscribe.subscription_id, 'sub-new')
  await assert.rejects(connect, /unsubscribed/i)
  transport.stop()
})

test('Desktop V3 open transport resolves only on replay.complete and reuses ready subscription without another frame', async () => {
  const socket = new FakeWebSocket()
  const transport = new DesktopV3RealtimeTransport({
    getEndpointCursor: () => 'cursor-0',
    openSocket: () => socket as unknown as WebSocket,
    livenessTimeoutMs: 60_000,
  })
  await transport.start()
  socket.open()
  socket.sent = []

  const connect = transport.subscribeSession({
    session_id: 'session-ready',
    subscription_id: 'sub-ready',
    endpoint_cursor: 'cursor-0',
  })
  connect.catch(() => undefined)
  await flushAsyncWork()
  assert.deepEqual(socket.sent.map((frame) => (frame as RealtimeMessage).kind), ['subscribe.session'])

  socket.emit({ protocol: 'v3.realtime', protocol_version: 1, kind: 'replay.started', session_id: 'session-ready', subscription_id: 'sub-ready' })
  await flushAsyncWork()
  socket.sent = []
  const replayEvent = {
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    session_id: 'session-ready',
    subscription_id: 'sub-ready',
    endpoint_cursor: 'cursor-1',
    event: {
      id: 'event-session-ready',
      session_id: 'session-ready',
      event_type: 'session.created',
      seq: 1,
      payload: {},
    },
  }
  socket.emit(replayEvent)
  await flushAsyncWork()

  let resolved = false
  connect.then(() => {
    resolved = true
  }).catch(() => undefined)
  assert.equal(resolved, false, 'replayed event is not acknowledgement')

  socket.emit({ protocol: 'v3.realtime', protocol_version: 1, kind: 'replay.complete', session_id: 'session-ready', subscription_id: 'sub-ready', endpoint_cursor: 'cursor-2' })
  await connect

  await transport.subscribeSession({
    session_id: 'session-ready',
    subscription_id: 'sub-ready',
    endpoint_cursor: 'cursor-2',
  })
  assert.deepEqual(socket.sent, [], 'already-ready subscription must not send another frame')
  transport.stop()
})

test('Desktop V3 open transport rejects pending connect on acknowledgement timeout', async () => {
  const socket = new FakeWebSocket()
  const transport = new DesktopV3RealtimeTransport({
    getEndpointCursor: () => 'cursor-0',
    openSocket: () => socket as unknown as WebSocket,
    livenessTimeoutMs: 60_000,
  })
  await transport.start()
  socket.open()

  const originalSetTimeout = window.setTimeout
  const originalClearTimeout = window.clearTimeout
  let timeoutCallback: (() => void) | undefined
  let timeoutDelay = 0
  Object.defineProperty(window, 'setTimeout', {
    value: ((callback: () => void, delay?: number) => {
      timeoutCallback = callback
      timeoutDelay = delay ?? 0
      return 12345
    }) as typeof window.setTimeout,
    configurable: true,
  })
  Object.defineProperty(window, 'clearTimeout', {
    value: (() => undefined) as typeof window.clearTimeout,
    configurable: true,
  })

  try {
    const connect = transport.subscribeSession({
      session_id: 'session-timeout',
      subscription_id: 'sub-timeout',
      endpoint_cursor: 'cursor-0',
    })
    connect.catch(() => undefined)
    await flushAsyncWork()
    assert.equal(timeoutDelay, SESSION_CONNECT_ACK_TIMEOUT_MS)
    timeoutCallback?.()
    await assert.rejects(connect, /timed out/i)
  } finally {
    Object.defineProperty(window, 'setTimeout', { value: originalSetTimeout, configurable: true })
    Object.defineProperty(window, 'clearTimeout', { value: originalClearTimeout, configurable: true })
    transport.stop()
  }
})

test('Desktop V3 open transport keeps pending connect across reconnect resume', async () => {
  const sockets: FakeWebSocket[] = []
  const resumes: SessionV3RealtimeResumeWire[] = []
  const transport = new DesktopV3RealtimeTransport({
    getEndpointCursor: () => 'cursor-0',
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
    onResumeSent: (resume) => resumes.push(resume),
    reopenBaseDelayMs: 1,
    reopenMaxDelayMs: 1,
    livenessTimeoutMs: 60_000,
  })

  await transport.start()
  sockets[0].open()
  const connect = transport.subscribeSession({
    session_id: 'session-reconnect',
    subscription_id: 'sub-reconnect',
    endpoint_cursor: 'cursor-0',
  })
  connect.catch(() => undefined)
  await flushAsyncWork()
  assert.deepEqual(sockets[0].sent.map((frame) => (frame as RealtimeMessage).kind), ['resume', 'subscribe.session'])

  sockets[0].close()
  await waitFor(() => sockets.length === 2)
  sockets[1].open()
  await flushAsyncWork()
  assert.equal(resumes.length, 2)
  assert.deepEqual(resumes[1].subscriptions?.map((subscription) => subscription.session_id), ['session-reconnect'])

  let resolved = false
  connect.then(() => {
    resolved = true
  }).catch(() => undefined)
  assert.equal(resolved, false)
  sockets[1].emit({ protocol: 'v3.realtime', protocol_version: 1, kind: 'replay.complete', session_id: 'session-reconnect', subscription_id: 'sub-reconnect', endpoint_cursor: 'cursor-1' })
  await connect
  assert.equal(resolved, true)
  transport.stop()
})

test('Desktop V3 open transport rejects pending connect on auth denied and stop', async () => {
  const socket = new FakeWebSocket()
  const transport = new DesktopV3RealtimeTransport({
    getEndpointCursor: () => 'cursor-0',
    openSocket: () => socket as unknown as WebSocket,
    livenessTimeoutMs: 60_000,
  })
  await transport.start()
  socket.open()

  const denied = transport.subscribeSession({
    session_id: 'session-denied',
    subscription_id: 'sub-denied',
    endpoint_cursor: 'cursor-0',
  })
  denied.catch(() => undefined)
  await flushAsyncWork()
  socket.emit({ protocol: 'v3.realtime', protocol_version: 1, kind: 'auth.denied', session_id: 'session-denied', subscription_id: 'sub-denied', reason: 'subscription denied' })
  await assert.rejects(denied, /subscription denied/i)

  const socket2 = new FakeWebSocket()
  const transport2 = new DesktopV3RealtimeTransport({
    getEndpointCursor: () => 'cursor-0',
    openSocket: () => socket2 as unknown as WebSocket,
    livenessTimeoutMs: 60_000,
  })
  await transport2.start()
  socket2.open()
  const stopped = transport2.subscribeSession({
    session_id: 'session-stop',
    subscription_id: 'sub-stop',
    endpoint_cursor: 'cursor-0',
  })
  stopped.catch(() => undefined)
  await flushAsyncWork()
  transport2.stop('test stop rejection')
  await assert.rejects(stopped, /test stop rejection/i)
})

test('Desktop V3 real controller connectSession resolves only after matching replay.complete', async () => {
  let state = readyControllerState()
  state.realtime.endpointCursor = 'durable-cursor-after-create'
  const sockets: FakeWebSocket[] = []
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'durable-cursor-after-create',
      sessions_by_id: {},
      projections_by_session: {},
      run_intents_by_session: {},
      current_run_intent_by_session: {},
      session_order: [],
      realtime: {
        stream_path: '/v3/realtime/stream',
        resume: {
          protocol: 'v3.realtime',
          protocol_version: 1,
          kind: 'resume',
          endpoint_cursor: 'durable-cursor-after-create',
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
  })

  try {
    const ready = controller.start(null)
    ready.catch(() => undefined)
    await waitFor(() => sockets.length === 1)
    sockets[0].open()
    await ready
    sockets[0].sent = []

    let resolved = false
    const connect = controller.connectSession({
      sessionId: 'session-new',
      endpointCursor: 'cursor-before-create',
    }).then(() => {
      resolved = true
    })
    connect.catch(() => undefined)

    await flushAsyncWork()
    assert.deepEqual(sockets[0].sent.map((frame) => (frame as RealtimeMessage).kind), ['subscribe.session'])
    const subscription = sockets[0].sent[0] as RealtimeMessage
    assert.equal(subscription.session_id, 'session-new')
    assert.equal(subscription.endpoint_cursor, 'cursor-before-create')
    assert.equal(resolved, false, 'connectSession must remain pending after WebSocket.send')

    sockets[0].emit({ protocol: 'v3.realtime', protocol_version: 1, kind: 'replay.started', session_id: 'session-new', subscription_id: subscription.subscription_id })
    await flushAsyncWork()
    assert.equal(resolved, false, 'connectSession must remain pending after replay.started')

    sockets[0].emit({ protocol: 'v3.realtime', protocol_version: 1, kind: 'replay.complete', session_id: 'session-new', subscription_id: 'other-subscription', endpoint_cursor: 'cursor-1' })
    await flushAsyncWork()
    assert.equal(resolved, false, 'connectSession must ignore replay.complete for the wrong subscription_id')

    sockets[0].emit({ protocol: 'v3.realtime', protocol_version: 1, kind: 'replay.complete', session_id: 'other-session', subscription_id: subscription.subscription_id, endpoint_cursor: 'cursor-2' })
    await flushAsyncWork()
    assert.equal(resolved, false, 'connectSession must ignore replay.complete for the wrong session_id')

    sockets[0].emit({ protocol: 'v3.realtime', protocol_version: 1, kind: 'replay.complete', session_id: 'session-new', subscription_id: subscription.subscription_id, endpoint_cursor: 'cursor-3' })
    await connect
    assert.equal(resolved, true)
  } finally {
    controller.stop()
  }
})

test('Desktop V3 connectSession uses supplied endpoint cursor instead of durable snapshot cursor', async () => {
  let state = readyControllerState()
  state.realtime.endpointCursor = 'durable-cursor-after-create'
  const socket = new FakeWebSocket()
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'durable-cursor-after-create',
      sessions_by_id: {},
      projections_by_session: {},
      run_intents_by_session: {},
      current_run_intent_by_session: {},
      session_order: [],
      realtime: {
        stream_path: '/v3/realtime/stream',
        resume: {
          protocol: 'v3.realtime',
          protocol_version: 1,
          kind: 'resume',
          endpoint_cursor: 'durable-cursor-after-create',
          subscriptions: [],
          worksets: [],
        },
      },
    }),
    openSocket: () => socket as unknown as WebSocket,
  })

  try {
    const ready = controller.start(null)
    ready.catch(() => undefined)
    socket.open()
    await ready
    socket.sent = []

    const connect = controller.connectSession({
      sessionId: 'session-cursor',
      endpointCursor: 'cursor-before-create',
    })
    connect.catch(() => undefined)
    await flushAsyncWork()

    const subscription = socket.sent[0] as RealtimeMessage
    assert.equal(subscription.kind, 'subscribe.session')
    assert.equal(subscription.endpoint_cursor, 'cursor-before-create')
    assert.notEqual(subscription.endpoint_cursor, 'durable-cursor-after-create')

    socket.emit({ protocol: 'v3.realtime', protocol_version: 1, kind: 'replay.complete', session_id: 'session-cursor', subscription_id: subscription.subscription_id, endpoint_cursor: 'cursor-1' })
    await connect
  } finally {
    controller.stop()
  }
})

test('Desktop V3 connectSession rejects when no endpoint cursor is available', async () => {
  const state = readyControllerState()
  state.realtime.endpointCursor = undefined
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: () => undefined,
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    openSocket: () => {
      throw new Error('socket should not open without an endpoint cursor')
    },
  })

  try {
    assert.throws(() => controller.currentEndpointCursor(), /endpoint cursor/i)
    await assert.rejects(
      controller.connectSession({ sessionId: 'session-no-cursor' }),
      /endpoint cursor/i,
    )
  } finally {
    controller.stop()
  }
})

test('Desktop V3 recovery still calls HTTP reconnect after cursor.error', async () => {
  let state = readyControllerState()
  state.realtime.endpointCursor = 'cursor-bootstrap'
  const sockets: FakeWebSocket[] = []
  let reconnectCount = 0
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    reconnect: async () => {
      reconnectCount += 1
      return reconnectFixture({
        snapshot_endpoint_cursor: `cursor-reconnect-${reconnectCount}`,
        sessions_by_id: {},
        projections_by_session: {},
        run_intents_by_session: {},
        current_run_intent_by_session: {},
        session_order: [],
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
    ready.catch(() => undefined)
    await waitFor(() => sockets.length === 1)
    sockets[0].open()
    await ready
    const reconnectCountAfterStartup = reconnectCount
    sockets[0].emit({ protocol: 'v3.realtime', protocol_version: 1, kind: 'cursor.error', error: 'rejected', bootstrap_required: false })
    await waitFor(() => reconnectCount === reconnectCountAfterStartup + 1)
  } finally {
    controller.stop()
  }
})

test('Desktop V3 normal socket close reopens with resume and without HTTP reconnect', async () => {
  let state = readyControllerState()
  state.realtime.endpointCursor = 'cursor-bootstrap'
  const sockets: FakeWebSocket[] = []
  let reconnectCount = 0
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    reconnect: async () => {
      reconnectCount += 1
      return reconnectFixture()
    },
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  try {
    const ready = controller.start(null)
    ready.catch(() => undefined)
    await waitFor(() => sockets.length === 1)
    sockets[0].open()
    await ready
    assert.equal(reconnectCount, 0)
    assert.deepEqual(sockets[0].sent.map((frame) => (frame as RealtimeMessage).kind), ['resume'])

    sockets[0].close()
    await waitFor(() => sockets.length === 2, 3_000)
    sockets[1].open()
    await waitFor(() => sockets[1].sent.length > 0)

    assert.equal(reconnectCount, 0)
    assert.deepEqual(sockets[1].sent.map((frame) => (frame as RealtimeMessage).kind), ['resume'])
    assert.equal((sockets[1].sent[0] as RealtimeMessage).endpoint_cursor, 'cursor-bootstrap')
  } finally {
    controller.stop()
  }
})

test('Desktop V3 recovery resume contains active selected and pending sessions without global membership', async () => {
  let state = readyControllerState()
  state.realtime.endpointCursor = 'cursor-durable'
  state.selectedSessionId = 'selected-inactive'
  const inactiveIds = Array.from({ length: 300 }, (_, index) => `inactive-${index}`)
  state.sessionOrderByScope['global-scope'] = ['selected-inactive', ...inactiveIds, 'active-run', 'pending-send', 'pending-connect']
  state.pendingUserByClientRequestId['client-pending'] = {
    clientRequestId: 'client-pending',
    messageId: 'message-pending',
    sessionId: 'pending-send',
    role: 'user',
    content: 'pending',
    createdAt: 1,
    status: 'pending',
  }

  const sockets: FakeWebSocket[] = []
  let reconnectCount = 0
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    hydrate: async () => hydrateSnapshotFixture({
      sessions_by_id: {},
      projections_by_session: {},
      messages_by_session: {},
      session_order: [],
    }),
    reconnect: async () => {
      reconnectCount += 1
      return reconnectFixture({
        snapshot_endpoint_cursor: 'cursor-reconnect',
        sessions_by_id: {},
        projections_by_session: {},
        run_intents_by_session: {},
        current_run_intent_by_session: {},
        session_order: state.sessionOrderByScope['global-scope'],
        workset_id: 'global-scope',
        realtime: {
          stream_path: '/v3/realtime/stream',
          resume: {
            protocol: 'v3.realtime',
            protocol_version: 1,
            kind: 'resume',
            endpoint_cursor: 'cursor-reconnect',
            subscriptions: [{ subscription_id: 'backend-active', session_id: 'active-run' }],
            worksets: [{ workset_id: 'global-scope', subscription_id: 'workset-sub', selector: { kind: 'global', global: true }, auto_subscribe_sessions: true }],
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

  try {
    const ready = controller.start('selected-inactive')
    ready.catch(() => undefined)
    await waitFor(() => sockets.length === 1)
    sockets[0].open()
    await ready

    const pendingConnect = controller.connectSession({ sessionId: 'pending-connect', endpointCursor: 'cursor-before-connect' })
    pendingConnect.catch(() => undefined)
    await flushAsyncWork()

    sockets[0].emit({ protocol: 'v3.realtime', protocol_version: 1, kind: 'cursor.error', error: 'rejected', bootstrap_required: false })
    await waitFor(() => reconnectCount === 1)
    await waitFor(() => sockets.length === 2)
    sockets[1].open()
    await waitFor(() => sockets[1].sent.length > 0)

    const resume = sockets[1].sent[0] as RealtimeMessage
    assert.deepEqual(
      resume.subscriptions?.map((subscription) => subscription.session_id),
      ['active-run', 'selected-inactive', 'pending-send', 'pending-connect'],
    )
    assert.equal(resume.subscriptions?.some((subscription) => inactiveIds.includes(subscription.session_id)), false)
    assert.equal(resume.subscriptions?.length, 4)
  } finally {
    controller.stop()
  }
})

test('Desktop V3 desired-session reconciliation removes inactive sessions selected sequentially', async () => {
  let state = readyControllerState()
  state.realtime.endpointCursor = 'cursor-bootstrap'
  const sockets: FakeWebSocket[] = []
  const listeners = new Set<(mutation?: { action: DesktopV3CacheAction; previousState: DesktopV3CacheState; nextState: DesktopV3CacheState }) => void>()
  const dispatch = (action: DesktopV3CacheAction) => {
    const previousState = state
    state = desktopV3CacheReducer({ ...state }, action)
    const mutation = { action, previousState, nextState: state }
    for (const listener of listeners) listener(mutation)
  }
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch,
    subscribe: (listener) => {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    ensureSession: async () => ({}),
    hydrate: async () => hydrateSnapshotFixture({
      sessions_by_id: {},
      projections_by_session: {},
      messages_by_session: {},
      session_order: [],
    }),
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  try {
    const ready = controller.start(null)
    ready.catch(() => undefined)
    await waitFor(() => sockets.length === 1)
    sockets[0].open()
    await ready
    sockets[0].sent = []

    for (let index = 0; index < 100; index += 1) {
      const sessionId = `selected-${index}`
      dispatch({ type: 'session.select', sessionId })
      await waitFor(() => sockets[0].sent.some((frame) => (frame as RealtimeMessage).kind === 'subscribe.session' && (frame as RealtimeMessage).session_id === sessionId))
      const subscribe = [...sockets[0].sent].reverse().find((frame) => (frame as RealtimeMessage).kind === 'subscribe.session' && (frame as RealtimeMessage).session_id === sessionId) as RealtimeMessage
      sockets[0].emit({ protocol: 'v3.realtime', protocol_version: 1, kind: 'replay.complete', session_id: sessionId, subscription_id: subscribe.subscription_id, endpoint_cursor: `cursor-${index}` })
      await flushAsyncWork()
    }

    const frames = sockets[0].sent.map((frame) => frame as RealtimeMessage)
    assert.equal(frames.filter((frame) => frame.kind === 'resume').length, 0)
    assert.equal(frames.filter((frame) => frame.kind === 'subscribe.session').length, 100)
    assert.equal(frames.filter((frame) => frame.kind === 'unsubscribe.session').length, 99)
    assert.equal(frames.some((frame) => frame.kind === 'unsubscribe.session' && frame.session_id === 'selected-99'), false)
  } finally {
    controller.stop()
  }
})


test('Desktop V3 desired-session reconciliation keeps running background session when selection changes', async () => {
  let state = readyControllerState()
  state.realtime.endpointCursor = 'cursor-bootstrap'
  state.selectedSessionId = 'selected-a'
  state.sessionOrderByScope['global-scope'] = ['selected-a', 'background-running', 'selected-b']
  state.currentRunIntentBySession['background-running'] = {
    session_id: 'background-running',
    run_id: 'run-background',
    status: 'running',
    created_at: 1,
    updated_at: 1,
    event_seq: 1,
  }
  const sockets: FakeWebSocket[] = []
  const listeners = new Set<(mutation?: { action: DesktopV3CacheAction; previousState: DesktopV3CacheState; nextState: DesktopV3CacheState }) => void>()
  const dispatch = (action: DesktopV3CacheAction) => {
    const previousState = state
    state = desktopV3CacheReducer({ ...state }, action)
    const mutation = { action, previousState, nextState: state }
    for (const listener of listeners) listener(mutation)
  }
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch,
    subscribe: (listener) => {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    ensureSession: async () => ({}),
    hydrate: async () => hydrateSnapshotFixture({
      sessions_by_id: {},
      projections_by_session: {},
      messages_by_session: {},
      session_order: [],
    }),
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  try {
    const ready = controller.start('selected-a')
    ready.catch(() => undefined)
    await waitFor(() => sockets.length === 1)
    sockets[0].open()
    await ready
    const initialResume = sockets[0].sent[0] as RealtimeMessage
    assert.deepEqual(initialResume.subscriptions?.map((subscription) => subscription.session_id), ['selected-a', 'background-running'])
    sockets[0].sent = []

    dispatch({ type: 'session.select', sessionId: 'selected-b' })
    await waitFor(() => sockets[0].sent.some((frame) => (frame as RealtimeMessage).kind === 'subscribe.session' && (frame as RealtimeMessage).session_id === 'selected-b'))
    const subscribe = sockets[0].sent.find((frame) => (frame as RealtimeMessage).kind === 'subscribe.session' && (frame as RealtimeMessage).session_id === 'selected-b') as RealtimeMessage
    sockets[0].emit({ protocol: 'v3.realtime', protocol_version: 1, kind: 'replay.complete', session_id: 'selected-b', subscription_id: subscribe.subscription_id, endpoint_cursor: 'cursor-selected-b' })
    await flushAsyncWork()

    const frames = sockets[0].sent.map((frame) => frame as RealtimeMessage)
    assert.equal(frames.some((frame) => frame.kind === 'unsubscribe.session' && frame.session_id === 'background-running'), false)
    assert.equal(frames.some((frame) => frame.kind === 'unsubscribe.session' && frame.session_id === 'selected-a'), true)
    assert.equal(frames.filter((frame) => frame.kind === 'resume').length, 0)
  } finally {
    controller.stop()
  }
})


test('Desktop V3 desired-session reconciliation removes terminal unselected background session', async () => {
  let state = readyControllerState()
  state.realtime.endpointCursor = 'cursor-bootstrap'
  state.selectedSessionId = 'selected-a'
  state.sessionOrderByScope['global-scope'] = ['selected-a', 'background-running']
  state.currentRunIntentBySession['background-running'] = {
    session_id: 'background-running',
    run_id: 'run-background',
    status: 'running',
    created_at: 1,
    updated_at: 1,
    event_seq: 1,
  }
  const sockets: FakeWebSocket[] = []
  const listeners = new Set<(mutation?: { action: DesktopV3CacheAction; previousState: DesktopV3CacheState; nextState: DesktopV3CacheState }) => void>()
  const notify = (action: DesktopV3CacheAction, previousState: DesktopV3CacheState) => {
    const mutation = { action, previousState, nextState: state }
    for (const listener of listeners) listener(mutation)
  }
  const dispatch = (action: DesktopV3CacheAction) => {
    const previousState = state
    state = desktopV3CacheReducer({ ...state }, action)
    notify(action, previousState)
  }
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch,
    subscribe: (listener) => {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    ensureSession: async () => ({}),
    hydrate: async () => hydrateSnapshotFixture({
      sessions_by_id: {},
      projections_by_session: {},
      messages_by_session: {},
      session_order: [],
    }),
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  try {
    const ready = controller.start('selected-a')
    ready.catch(() => undefined)
    await waitFor(() => sockets.length === 1)
    sockets[0].open()
    await ready
    sockets[0].sent = []

    const previousState = state
    state = {
      ...state,
      currentRunIntentBySession: {
        ...state.currentRunIntentBySession,
        'background-running': undefined,
      },
    }
    notify({ type: 'realtime.control', frame: { protocol: 'v3.realtime', protocol_version: 1, kind: 'replay.complete' } }, previousState)

    await waitFor(() => sockets[0].sent.some((frame) => (frame as RealtimeMessage).kind === 'unsubscribe.session' && (frame as RealtimeMessage).session_id === 'background-running'))
    assert.equal(sockets[0].sent.filter((frame) => (frame as RealtimeMessage).kind === 'resume').length, 0)
  } finally {
    controller.stop()
  }
})


test('Desktop V3 auto-discovered inactive session remains during hydrate then unsubscribes', async () => {
  let state = readyControllerState()
  state.realtime.endpointCursor = 'cursor-bootstrap'
  const sockets: FakeWebSocket[] = []
  const listeners = new Set<(mutation?: { action: DesktopV3CacheAction; previousState: DesktopV3CacheState; nextState: DesktopV3CacheState }) => void>()
  const hydrateDeferred = createDeferredValue<ReturnType<typeof hydrateSnapshotFixture>>()
  let hydrateStarted = false
  const dispatch = (action: DesktopV3CacheAction) => {
    const previousState = state
    state = desktopV3CacheReducer({ ...state }, action)
    const mutation = { action, previousState, nextState: state }
    for (const listener of listeners) listener(mutation)
  }
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch,
    subscribe: (listener) => {
      listeners.add(listener)
      return () => listeners.delete(listener)
    },
    ensureSession: async () => ({}),
    hydrate: async () => {
      hydrateStarted = true
      return hydrateDeferred.promise
    },
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  try {
    const ready = controller.start(null)
    ready.catch(() => undefined)
    await waitFor(() => sockets.length === 1)
    sockets[0].open()
    await ready
    sockets[0].sent = []

    sockets[0].emit({
      protocol: 'v3.realtime',
      protocol_version: 1,
      kind: 'workset.session.discovered',
      workset_id: 'global-scope',
      session_id: 'auto-inactive',
      subscription_id: 'auto-sub-inactive',
      auto_subscribed: true,
      endpoint_cursor: 'cursor-auto',
    })
    await waitFor(() => hydrateStarted)
    await flushAsyncWork()
    assert.equal(sockets[0].sent.some((frame) => (frame as RealtimeMessage).kind === 'unsubscribe.session'), false)

    hydrateDeferred.resolve(hydrateSnapshotFixture({
      sessions_by_id: {},
      projections_by_session: {},
      messages_by_session: {},
      session_order: [],
    }))
    await waitFor(() => sockets[0].sent.some((frame) => (frame as RealtimeMessage).kind === 'unsubscribe.session' && (frame as RealtimeMessage).session_id === 'auto-inactive'))
    assert.equal(sockets[0].sent.filter((frame) => (frame as RealtimeMessage).kind === 'resume').length, 0)
  } finally {
    controller.stop()
  }
})


test('Desktop V3 300-member bootstrap does not create 300 subscriptions', async () => {
  let state = readyControllerState()
  state.realtime.endpointCursor = 'cursor-bootstrap'
  state.selectedSessionId = 'selected-inactive'
  const inactiveIds = Array.from({ length: 300 }, (_, index) => `inactive-${index}`)
  state.sessionOrderByScope['global-scope'] = ['selected-inactive', ...inactiveIds]
  const sockets: FakeWebSocket[] = []
  const reconnectSubscriptions = state.sessionOrderByScope['global-scope'].map((sessionId) => ({
    session_id: sessionId,
    subscription_id: `desktop:test:session:${sessionId}`,
    endpoint_cursor: 'cursor-reconnect',
  }))
  const controller = new DesktopV3RealtimeControllerRuntime({
    getSnapshot: () => state,
    dispatch: (action: DesktopV3CacheAction) => {
      state = desktopV3CacheReducer(state, action)
    },
    subscribe: () => () => {},
    ensureSession: async () => ({}),
    hydrate: async () => hydrateSnapshotFixture({
      sessions_by_id: {},
      projections_by_session: {},
      messages_by_session: {},
      session_order: [],
    }),
    reconnect: async () => reconnectFixture({
      snapshot_endpoint_cursor: 'cursor-reconnect',
      sessions_by_id: {},
      projections_by_session: {},
      run_intents_by_session: {},
      current_run_intent_by_session: {},
      session_order: state.sessionOrderByScope['global-scope'],
      realtime: {
        stream_path: '/v3/realtime/stream',
        resume: {
          protocol: 'v3.realtime',
          protocol_version: 1,
          kind: 'resume',
          endpoint_cursor: 'cursor-reconnect',
          subscriptions: reconnectSubscriptions,
          worksets: [{ workset_id: 'global-scope', subscription_id: 'workset-sub', selector: { kind: 'global', global: true }, auto_subscribe_sessions: true }],
        },
      },
    }),
    openSocket: () => {
      const socket = new FakeWebSocket()
      sockets.push(socket)
      return socket as unknown as WebSocket
    },
  })

  try {
    const ready = controller.start('selected-inactive')
    ready.catch(() => undefined)
    await waitFor(() => sockets.length === 1)
    sockets[0].open()
    await ready
    const resume = sockets[0].sent[0] as RealtimeMessage
    assert.equal(resume.subscriptions?.length, 1)
    assert.equal(resume.subscriptions?.[0]?.session_id, 'selected-inactive')
  } finally {
    controller.stop()
  }
})


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

function createDeferredValue<T>(): { promise: Promise<T>; resolve: (value: T) => void; reject: (reason?: unknown) => void } {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((promiseResolve, promiseReject) => {
    resolve = promiseResolve
    reject = promiseReject
  })
  return { promise, resolve, reject }
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
