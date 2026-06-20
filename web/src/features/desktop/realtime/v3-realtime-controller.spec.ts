import test from 'node:test'
import assert from 'node:assert/strict'

import { DesktopV3RealtimeTransport } from '../session-v3/transport'
import {
  DesktopV3RealtimeControllerRuntime,
  buildDesktopV3ReconnectInput,
  mapTransportStatus,
} from './v3-realtime-controller'
import { createEmptyDesktopV3CacheState, desktopV3CacheReducer } from '../state/desktop-v3-cache-reducer'
import { projectionA, reconnectFixture, runIntentA, sessionA } from '../state/desktop-v3-cache.backend-fixtures'
import type { SessionV3RealtimeResumeWire } from '../session-v3/types'
import type { DesktopV3CacheAction, DesktopV3CacheState, V3SessionEvent } from '../state/desktop-v3-cache-types'

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

test('Desktop V3 active-run repair defers websocket overlay and rebuilds once from stored event sequence', async () => {
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
  const duplicateLiveFromRepair: V3SessionEvent = {
    ...liveEvent,
    id: 'evt-live-4-duplicate-http',
    payload: { run_id: runIntentA.run_id, delta: 'SHOULD_NOT_APPLY' },
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
          events: [repairEvent, duplicateLiveFromRepair],
          projection: { ...projectionA, last_event_seq: 4, projection_high_watermark_seq: 4 },
          high_watermark_seq: 4,
          next_seq: 4,
          applied_seq: 4,
        }
      }
      return {
        ok: true,
        session_id: input.sessionId,
        events: [],
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

  await controller.start()
  sockets[0].open()
  await waitFor(() => readRequests.length === 1)

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

  assert.equal(state.realtime.endpointCursor, 'cursor-live-4')
  assert.equal(state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id]?.assistantDraft, undefined)

  releaseFirstRepairPage()
  await waitFor(() => state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id]?.assistantDraft?.content === 'repair-live')

  assert.deepEqual(
    state.eventsBySession[sessionA.id].map((event) => `${event.seq}:${event.id}`),
    ['3:evt-repair-3', '4:evt-live-4'],
  )
  assert.equal(state.realtime.endpointCursor, 'cursor-live-4')
  controller.stop()
})

test('Desktop V3 active-run repair abandons an in-flight page after terminal websocket event', async () => {
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

  await controller.start()
  sockets[0].open()
  await waitFor(() => readRequests.length === 1)

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

  assert.equal(state.currentRunIntentBySession[sessionA.id], undefined)
  assert.equal(state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id]?.status, 'completed')
  assert.equal(state.realtime.endpointCursor, 'cursor-terminal-5')

  releaseRepairPage()
  await waitFor(() => repairPageReturned)
  await flushAsyncWork()

  assert.equal(state.currentRunIntentBySession[sessionA.id], undefined)
  assert.equal(state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id]?.status, 'completed')
  assert.equal(state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id]?.assistantDraft, undefined)
  assert.equal(state.runIntentsBySession[sessionA.id]?.[runIntentA.run_id]?.status, 'completed')
  assert.equal(state.runIntentsBySession[sessionA.id]?.[runIntentA.run_id]?.event_seq, 5)
  assert.deepEqual(state.sessionsById[sessionA.id]?.kind === 'full' ? state.sessionsById[sessionA.id]?.session.lifecycle : undefined, { status: 'completed' })
  assert.deepEqual(
    state.eventsBySession[sessionA.id].map((event) => `${event.seq}:${event.id}`),
    ['5:evt-terminal-5'],
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

  await controller.start()
  sockets[0].open()
  await waitFor(() => readRequests.length === 1)

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

  assert.equal(
    state.liveRunsBySession[sessionA.id]?.[runB.run_id]?.assistantDraft?.content,
    'run-b-delta',
  )

  releaseRepairPage()
  await waitFor(() => repairPageReturned)
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
  assert.equal(state.realtime.endpointCursor, 'cursor-run-b-7')
  assert.deepEqual(
    state.eventsBySession[sessionA.id].map((event) => `${event.seq}:${event.id}`),
    ['5:evt-terminal-5', '6:evt-run-b-running-6', '7:evt-run-b-delta-7'],
  )
  controller.stop()
})

test('Desktop V3 active-run repair applies replacement-run overlays from HTTP page', async () => {
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

  await controller.start()
  sockets[0].open()
  await waitFor(() => readRequests.length === 1)
  await waitFor(() => state.currentRunIntentBySession[sessionA.id]?.run_id === runB.run_id)

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
  assert.equal(
    state.realtime.endpointCursor,
    'cursor-run-b-7',
  )
  controller.stop()
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
