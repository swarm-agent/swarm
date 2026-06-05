import assert from 'node:assert/strict'
import { afterEach, test } from 'node:test'

import { queryClient } from '../../../app/query-client'
import type { DesktopSessionRecord, DesktopStoreState } from '../types/realtime'
import { applyEnvelope, useDesktopStore } from './use-desktop-store'
import type { RunStreamEventMessage } from './run-stream-controller'
import { DesktopV3RealtimeController } from './v3-realtime-controller'

function emptyLiveState(): DesktopSessionRecord['live'] {
  return {
    runId: null,
    agentName: null,
    startedAt: null,
    status: 'idle',
    step: 0,
    toolName: null,
    toolCallId: null,
    toolArguments: null,
    toolOutput: '',
    retainedToolName: null,
    retainedToolCallId: null,
    retainedToolArguments: null,
    retainedToolOutput: '',
    retainedToolState: null,
    toolHistory: [],
    summary: null,
    lastEventType: null,
    lastEventAt: null,
    error: null,
    seq: 0,
    assistantDraft: '',
    retainedAssistantSegments: [],
    reasoningSummary: '',
    reasoningText: '',
    reasoningState: 'idle',
    reasoningSegment: 0,
    reasoningStartedAt: null,
    awaitingAck: false,
  }
}

function makeSession(input: Partial<DesktopSessionRecord> & Pick<DesktopSessionRecord, 'id'>): DesktopSessionRecord {
  return {
    id: input.id,
    title: input.title ?? 'V3 session',
    workspacePath: input.workspacePath ?? '/repo',
    workspaceName: input.workspaceName ?? 'repo',
    mode: input.mode ?? 'auto',
    metadata: input.metadata,
    sessionApi: input.sessionApi ?? 'v3',
    lastEventSeq: input.lastEventSeq ?? 0,
    projectionHighWatermarkSeq: input.projectionHighWatermarkSeq ?? 0,
    messageCount: input.messageCount ?? 0,
    updatedAt: input.updatedAt ?? 1,
    createdAt: input.createdAt ?? 1,
    permissionsHydrated: input.permissionsHydrated ?? false,
    lifecycle: input.lifecycle ?? null,
    live: input.live ?? emptyLiveState(),
    pendingPermissions: input.pendingPermissions ?? [],
    pendingPermissionCount: input.pendingPermissionCount ?? 0,
    usage: input.usage ?? null,
  }
}

function makeState(session: DesktopSessionRecord): DesktopStoreState {
  return {
    ...useDesktopStore.getState(),
    sessions: { [session.id]: session },
    lastGlobalSeq: 0,
  }
}

afterEach(async () => {
  useDesktopStore.getState().disconnect()
  useDesktopStore.setState({
    sessions: {},
    notifications: [],
    lastGlobalSeq: 0,
    reconnectTimer: null,
    heartbeatTimer: null,
    livenessTimer: null,
    reconnectAttempt: 0,
    realtimeDesired: false,
    connectionState: 'idle',
  })
  queryClient.clear()
  await new Promise((resolve) => setImmediate(resolve))
})

test('V3 stream frame application commits ordered durable message events and cursor state', () => {
  const session = makeSession({ id: 'session-v3', lastEventSeq: 1, projectionHighWatermarkSeq: 1 })
  useDesktopStore.setState(makeState(session), true)

  useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
    type: 'event',
    ok: true,
    session_id: 'session-v3',
    last_seq: 2,
    event: {
      id: 'v3evt_session-v3_00000000000000000002',
      session_id: 'session-v3',
      seq: 2,
      event_type: 'session.message.appended',
      ts_unix_ms: 10,
      payload: {
        session_id: 'session-v3',
        message: {
          id: 'msg-v3-2',
          session_id: 'session-v3',
          global_seq: 2,
          role: 'user',
          content: 'hello from v3 stream',
          created_at: 10,
        },
      },
    },
  }, 11)

  const updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.sessionApi, 'v3')
  assert.equal(updated.lastEventSeq, 2)
  assert.equal(updated.projectionHighWatermarkSeq, 2)
  assert.equal(updated.messageCount, 1)
  assert.equal(updated.live.lastEventType, 'session.message.appended')
  assert.equal(updated.live.lastEventAt, 11)
})

test('V3 stream maps committed assistant lifecycle events into live draft and final message state', () => {
  const originalWindow = globalThis.window
  const testWindow = originalWindow ?? {} as typeof window
  testWindow.setTimeout = ((callback: TimerHandler) => {
    if (typeof callback === 'function') callback()
    return 0
  }) as typeof window.setTimeout
  globalThis.window = testWindow
  const session = makeSession({ id: 'session-v3', sessionApi: 'v3', lastEventSeq: 1, projectionHighWatermarkSeq: 1 })
  useDesktopStore.setState(makeState(session), true)

  try {

    useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
      type: 'event',
      ok: true,
      session_id: 'session-v3',
      last_seq: 2,
      event: {
        id: 'v3evt_session-v3_00000000000000000002',
        session_id: 'session-v3',
        seq: 2,
        event_type: 'session.assistant.started',
        ts_unix_ms: 20,
        payload: { session_id: 'session-v3', run_id: 'run-v3', status: 'running' },
      },
    }, 20)

    let updated = useDesktopStore.getState().sessions['session-v3']
    assert.equal(updated.live.runId, 'run-v3')
    assert.equal(updated.live.status, 'running')
    assert.equal(updated.live.summary, 'Assistant responding…')

    useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
      type: 'event',
      ok: true,
      session_id: 'session-v3',
      last_seq: 3,
      event: {
        id: 'v3evt_session-v3_00000000000000000003',
        session_id: 'session-v3',
        seq: 3,
        event_type: 'session.assistant.delta',
        ts_unix_ms: 21,
        payload: { session_id: 'session-v3', run_id: 'run-v3', delta: 'hel' },
      },
    }, 21)
    useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
      type: 'event',
      ok: true,
      session_id: 'session-v3',
      last_seq: 4,
      event: {
        id: 'v3evt_session-v3_00000000000000000004',
        session_id: 'session-v3',
        seq: 4,
        event_type: 'session.assistant.delta',
        ts_unix_ms: 22,
        payload: { session_id: 'session-v3', run_id: 'run-v3', delta: 'lo' },
      },
    }, 22)

    updated = useDesktopStore.getState().sessions['session-v3']
    assert.equal(updated.live.assistantDraft, 'hello')
    assert.equal(updated.live.lastEventType, 'session.assistant.delta')

    useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
      type: 'event',
      ok: true,
      session_id: 'session-v3',
      last_seq: 5,
      event: {
        id: 'v3evt_session-v3_00000000000000000005',
        session_id: 'session-v3',
        seq: 5,
        event_type: 'session.assistant.completed',
        ts_unix_ms: 23,
        payload: {
          session_id: 'session-v3',
          run_id: 'run-v3',
          status: 'completed',
          message: {
            id: 'msg-assistant-v3',
            session_id: 'session-v3',
            global_seq: 5,
            role: 'assistant',
            content: 'hello',
            created_at: 23,
          },
          run_intent: { run_id: 'run-v3', status: 'completed' },
        },
      },
    }, 23)

    updated = useDesktopStore.getState().sessions['session-v3']
    assert.equal(updated.lastEventSeq, 5)
    assert.equal(updated.projectionHighWatermarkSeq, 5)
    assert.equal(updated.live.status, 'idle')
    assert.equal(updated.live.runId, null)
    assert.equal(updated.live.assistantDraft, '')
    assert.equal(updated.live.lastEventType, 'session.assistant.completed')
    assert.equal(updated.messageCount, 1)
  } finally {
    if (originalWindow) {
      globalThis.window = originalWindow
    } else {
      Reflect.deleteProperty(globalThis, 'window')
    }
  }
})

test('V3 stream maps committed run failures into replayable error state', () => {
  const session = makeSession({ id: 'session-v3', sessionApi: 'v3', live: { ...emptyLiveState(), status: 'running', runId: 'run-v3', assistantDraft: 'partial' } })
  useDesktopStore.setState(makeState(session), true)

  useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
    type: 'event',
    ok: true,
    session_id: 'session-v3',
    last_seq: 2,
    event: {
      id: 'v3evt_session-v3_00000000000000000002',
      session_id: 'session-v3',
      seq: 2,
      event_type: 'session.run.failed',
      ts_unix_ms: 30,
      payload: { session_id: 'session-v3', run_id: 'run-v3', status: 'failed', error: 'provider unavailable' },
    },
  }, 30)

  const updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.live.status, 'error')
  assert.equal(updated.live.runId, null)
  assert.equal(updated.live.error, 'provider unavailable')
  assert.equal(updated.live.summary, 'provider unavailable')
  assert.equal(updated.live.lastEventType, 'session.run.failed')
})


test('V3 stream maps session.tool events into live tool state', () => {
  const session = makeSession({ id: 'session-v3', sessionApi: 'v3', lastEventSeq: 1, projectionHighWatermarkSeq: 1 })
  useDesktopStore.setState(makeState(session), true)

  useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
    type: 'event',
    ok: true,
    session_id: 'session-v3',
    last_seq: 2,
    event: {
      id: 'v3evt_session-v3_00000000000000000002',
      session_id: 'session-v3',
      seq: 2,
      event_type: 'session.tool.started',
      ts_unix_ms: 20,
      payload: { session_id: 'session-v3', run_id: 'run-v3', step_id: 'step-1', tool_instance_id: 'tool-1', tool_name: 'bash', call_id: 'call-1', arguments: '{"command":"echo hi"}', step: 1 },
    },
  }, 20)

  let updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.live.toolName, 'bash')
  assert.equal(updated.live.toolCallId, 'call-1')
  assert.equal(updated.live.toolArguments, '{"command":"echo hi"}')
  assert.equal(updated.live.summary, 'bash')
  assert.equal(updated.live.lastEventType, 'session.tool.started')

  useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
    type: 'event',
    ok: true,
    session_id: 'session-v3',
    last_seq: 3,
    event: {
      id: 'v3evt_session-v3_00000000000000000003',
      session_id: 'session-v3',
      seq: 3,
      event_type: 'session.tool.delta',
      ts_unix_ms: 21,
      payload: { session_id: 'session-v3', run_id: 'run-v3', step_id: 'step-1', tool_instance_id: 'tool-1', tool_name: 'bash', call_id: 'call-1', output: 'chunk' },
    },
  }, 21)

  updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.live.toolOutput, 'chunk')
  assert.equal(updated.live.lastEventType, 'session.tool.delta')

  useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
    type: 'event',
    ok: true,
    session_id: 'session-v3',
    last_seq: 4,
    event: {
      id: 'v3evt_session-v3_00000000000000000004',
      session_id: 'session-v3',
      seq: 4,
      event_type: 'session.tool.completed',
      ts_unix_ms: 22,
      payload: { session_id: 'session-v3', run_id: 'run-v3', step_id: 'step-1', tool_instance_id: 'tool-1', tool_name: 'bash', call_id: 'call-1', output: 'done', raw_output: 'raw done' },
    },
  }, 22)

  updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.live.toolName, null)
  assert.equal(updated.live.retainedToolName, 'bash')
  assert.equal(updated.live.retainedToolOutput, 'raw done')
  assert.equal(updated.live.retainedToolState, 'done')
  assert.equal(updated.live.toolHistory?.length, 1)
  assert.equal(updated.live.toolHistory?.[0]?.callId, 'call-1')
  assert.equal(updated.live.toolHistory?.[0]?.toolInstanceId, 'tool-1')
  assert.equal(updated.live.toolHistory?.[0]?.toolOutput, 'raw done')
  assert.equal(updated.live.lastEventType, 'session.tool.completed')
})

test('V3 replay control frames update cursor state without V2 resume semantics', () => {
  const session = makeSession({ id: 'session-v3', lastEventSeq: 2, projectionHighWatermarkSeq: 2 })
  useDesktopStore.setState(makeState(session), true)

  useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
    type: 'replay.complete',
    ok: true,
    session_id: 'session-v3',
    last_seq: 4,
    high_watermark_seq: 4,
    next_seq: 4,
  }, 22)

  const updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.lastEventSeq, 4)
  assert.equal(updated.projectionHighWatermarkSeq, 4)
  assert.equal(updated.live.lastEventType, 'replay.complete')
  assert.equal(updated.live.awaitingAck, false)
})

test('desktop panel and controller route V3 streams to native realtime only', async () => {
  const { readFile } = await import('node:fs/promises')
  const controllerSource = await readFile(new URL('./run-stream-controller.ts', import.meta.url), 'utf8')
  const v3RealtimeControllerSource = await readFile(new URL('./v3-realtime-controller.ts', import.meta.url), 'utf8')
  const querySource = await readFile(new URL('../chat/queries/chat-queries.ts', import.meta.url), 'utf8')
  const panelSource = await readFile(new URL('../chat/components/desktop-chat-panel.tsx', import.meta.url), 'utf8')

  assert.match(querySource, /V3_REALTIME_STREAM_PATH/)
  assert.match(querySource, /V3 sessions use the shared \/v3\/realtime\/stream connection/)
  assert.match(v3RealtimeControllerSource, /kind: 'subscribe\.session'/)
  assert.match(v3RealtimeControllerSource, /kind: 'unsubscribe\.session'/)
  assert.match(v3RealtimeControllerSource, /validateV3RealtimeMessage/)
  const storeSource = await readFile(new URL('./use-desktop-store.ts', import.meta.url), 'utf8')
  assert.match(storeSource, /if \(sessionApi === 'v3'\) \{\n\s+await requireV3RealtimeController\(\)\.ensure\(\)\n\s+return\n\s+\}/)
  assert.match(storeSource, /resolveV3RealtimeSubscriptions/)
  assert.match(panelSource, /liveSession\?\.sessionApi\?\.trim\(\)\.toLowerCase\(\) === 'v3'/)
  assert.doesNotMatch(querySource, /\/v3\/sessions\/[^`]+\/stream/)
  assert.doesNotMatch(querySource, /\/v3\/sessions\/[^`]+\/run\/stream/)
})

test('desktop store submitPrompt for V3 primary sessions commits through Sessions API v3 and starts the V3 stream', async () => {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const websocketURLs: string[] = []
  let websocketCloseCount = 0
  const originalFetch = globalThis.fetch
  const originalWindow = globalThis.window
  const originalWebSocket = globalThis.WebSocket

  class FakeWebSocket {
    static CONNECTING = 0
    static OPEN = 1
    static CLOSING = 2
    static CLOSED = 3
    readyState = FakeWebSocket.OPEN
    url: string

    constructor(input: string | URL) {
      this.url = String(input)
      websocketURLs.push(this.url)
    }

    addEventListener(type: string, callback: EventListenerOrEventListenerObject) {
      if (type === 'open') {
        queueMicrotask(() => {
          if (typeof callback === 'function') {
            callback(new Event('open'))
          } else {
            callback.handleEvent(new Event('open'))
          }
        })
      }
    }
    close() {
      websocketCloseCount += 1
      this.readyState = FakeWebSocket.CLOSED
    }
    send() {}
  }

  globalThis.window = {
    location: { protocol: 'http:', host: '127.0.0.1:7777' },
    setTimeout: ((callback: TimerHandler, timeout?: number) => setTimeout(callback, timeout)) as typeof window.setTimeout,
    clearTimeout: ((timer?: number) => clearTimeout(timer)) as typeof window.clearTimeout,
  } as unknown as Window & typeof globalThis
  globalThis.WebSocket = FakeWebSocket as unknown as typeof WebSocket
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)
    if (url === '/v1/auth/desktop/session') {
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url === '/v3/sessions/session-v3/run/stop') {
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url !== '/v3/sessions/session-v3/messages') {
      throw new Error(`unexpected fetch: ${url}`)
    }
    return new Response(JSON.stringify({
      ok: true,
      session: {
        id: 'session-v3',
        title: 'V3 session',
        workspace_path: '/repo',
        workspace_name: 'repo',
        mode: 'auto',
        session_api: 'v3',
        message_count: 1,
        updated_at: 20,
        created_at: 1,
      },
      projection: {
        session_id: 'session-v3',
        last_event_seq: 3,
        projection_high_watermark_seq: 3,
        updated_at: 20,
      },
      message: {
        id: 'msg-v3-submit',
        session_id: 'session-v3',
        global_seq: 2,
        role: 'user',
        content: 'hello primary',
        created_at: 19,
      },
      run_intent: {
        session_id: 'session-v3',
        run_id: 'v3run-session-v3-2',
        status: 'pending_executor',
        event_seq: 3,
        created_at: 20,
        updated_at: 20,
      },
      messages: [],
      events: [],
    }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch

  try {
    const session = makeSession({ id: 'session-v3', sessionApi: 'v3' })
    useDesktopStore.setState(makeState(session), true)

    await useDesktopStore.getState().submitPrompt({
      sessionId: 'session-v3',
      sessionApi: 'v3',
      clientRequestId: 'desktop-v3-message:test-submit',
      workspacePath: '/repo',
      workspaceName: 'repo',
      prompt: 'hello primary',
      agentName: 'swarm',
    })

    const urls = calls.map((entry) => String(entry.input)).sort()
    assert.deepEqual(urls, ['/v3/sessions/session-v3/messages', '/v1/auth/desktop/session'].sort())
    assert.equal(urls.some((url) => url.startsWith('/v1/swarm/managed-hosts/sessions')), false)
    assert.equal(urls.some((url) => url.startsWith('/v2/sessions')), false)
    assert.deepEqual(websocketURLs, ['ws://127.0.0.1:7777/v3/realtime/stream'])
    const body = JSON.parse(String(calls[0]?.init?.body ?? '{}')) as Record<string, unknown>
    assert.deepEqual(body, {
      client_request_id: 'desktop-v3-message:test-submit',
      role: 'user',
      content: 'hello primary',
    })

    const updated = useDesktopStore.getState().sessions['session-v3']
    assert.equal(updated.sessionApi, 'v3')
    assert.equal(updated.lastEventSeq, 3)
    assert.equal(updated.projectionHighWatermarkSeq, 3)
    assert.equal(updated.live.runId, 'v3run-session-v3-2')
    assert.equal(updated.live.status, 'starting')
    assert.equal(updated.live.startedAt, 20)
    assert.equal(updated.live.lastEventType, 'run.pending_executor')

    await useDesktopStore.getState().stopRun('session-v3')
    const stopCall = calls.find((entry) => String(entry.input) === '/v3/sessions/session-v3/run/stop')
    assert.ok(stopCall)
    assert.deepEqual(JSON.parse(String(stopCall.init?.body ?? '{}')), { type: 'run.stop', run_id: 'v3run-session-v3-2' })
    assert.equal(websocketCloseCount, 0)
  } finally {
    useDesktopStore.getState().disconnect()
    globalThis.fetch = originalFetch
    globalThis.window = originalWindow
    globalThis.WebSocket = originalWebSocket
  }
})

// Keep the V3 stream path compatible with existing session envelope handling.
test('V3 session.created payload nesting maps through applyEnvelope', () => {
  const patch = applyEnvelope({ ...useDesktopStore.getState(), sessions: {}, lastGlobalSeq: 0 }, {
    event_type: 'session.created',
    entity_id: 'session-created',
    ts_unix_ms: 1,
    payload: {
      id: 'session-created',
      session_id: 'session-created',
      title: 'created',
      workspace_path: '/repo',
      workspace_name: 'repo',
      mode: 'auto',
      session_api: 'v3',
      created_at: 1,
      updated_at: 1,
    },
  })
  assert.equal(patch.sessions?.['session-created']?.workspacePath, '/repo')
})

test('V3 realtime controller multiplexes multiple sessions over one native socket and isolates cursor errors', async () => {
  const websocketURLs: string[] = []
  const sent: Array<Record<string, unknown>> = []
  const fetchCalls: string[] = []
  const originalFetch = globalThis.fetch
  const originalWindow = globalThis.window
  const originalWebSocket = globalThis.WebSocket
  const listeners = new Map<string, Array<(event: Event | MessageEvent) => void>>()

  class FakeWebSocket {
    static CONNECTING = 0
    static OPEN = 1
    static CLOSING = 2
    static CLOSED = 3
    readyState = FakeWebSocket.OPEN
    url: string

    constructor(input: string | URL) {
      this.url = String(input)
      websocketURLs.push(this.url)
    }

    addEventListener(type: string, callback: EventListenerOrEventListenerObject) {
      const wrapped = (event: Event | MessageEvent) => {
        if (typeof callback === 'function') {
          callback(event)
        } else {
          callback.handleEvent(event)
        }
      }
      listeners.set(type, [...(listeners.get(type) ?? []), wrapped])
      if (type === 'open') {
        queueMicrotask(() => wrapped(new Event('open')))
      }
    }

    close() {
      this.readyState = FakeWebSocket.CLOSED
    }

    send(payload: string) {
      sent.push(JSON.parse(payload) as Record<string, unknown>)
    }
  }

  globalThis.window = {
    location: { protocol: 'http:', host: '127.0.0.1:7777' },
    addEventListener() {},
    setTimeout: ((callback: TimerHandler, timeout?: number) => setTimeout(callback, timeout)) as typeof window.setTimeout,
    clearTimeout: ((timer?: number) => clearTimeout(timer)) as typeof window.clearTimeout,
  } as unknown as Window & typeof globalThis
  globalThis.WebSocket = FakeWebSocket as unknown as typeof WebSocket
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const url = String(input)
    fetchCalls.push(url)
    if (url === '/v1/auth/desktop/session') {
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url === '/v3/sessions/session-v3-b') {
      return new Response(JSON.stringify({
        ok: true,
        session: {
          id: 'session-v3-b',
          title: 'B',
          workspace_path: '/repo',
          workspace_name: 'repo',
          mode: 'auto',
          session_api: 'v3',
          message_count: 0,
          updated_at: 5,
          created_at: 1,
        },
        projection: {
          session_id: 'session-v3-b',
          last_event_seq: 5,
          projection_high_watermark_seq: 5,
          updated_at: 5,
        },
      }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  const originalSetTimeout = globalThis.window.setTimeout
  globalThis.window.setTimeout = ((callback: TimerHandler, timeout?: number) => {
    if (timeout === 0 && typeof callback === 'function') {
      queueMicrotask(() => callback())
    }
    return 0
  }) as typeof window.setTimeout

  useDesktopStore.setState({
    ...makeState(makeSession({ id: 'session-v3-a', sessionApi: 'v3', lastEventSeq: 2, projectionHighWatermarkSeq: 2 })),
    sessions: {
      'session-v3-a': makeSession({ id: 'session-v3-a', sessionApi: 'v3', lastEventSeq: 2, projectionHighWatermarkSeq: 2 }),
      'session-v3-b': makeSession({ id: 'session-v3-b', sessionApi: 'v3', lastEventSeq: 4, projectionHighWatermarkSeq: 4 }),
    },
  }, true)

  const reconnects: Array<{ sessionId: string; reason: string }> = []
  const frames: Array<{ sessionId: string; seq: number }> = []
  const controller = new DesktopV3RealtimeController({
    getSubscriptions: () => [
      { sessionId: 'session-v3-a', afterSeq: 2 },
      { sessionId: 'session-v3-b', afterSeq: 4 },
    ],
    onFrame: (payload) => {
      if (payload.session_id) {
        useDesktopStore.getState().__testApplyRunStreamFrame?.(payload.session_id, {
          type: payload.kind,
          ok: true,
          session_id: payload.session_id,
          last_seq: payload.last_seq,
          high_watermark_seq: payload.high_watermark_seq,
          next_seq: payload.next_seq,
          error: payload.error,
          event: payload.event as RunStreamEventMessage['event'],
        }, Date.now())
      }
      if (payload.kind === 'event' && payload.session_id && payload.event?.seq) {
        frames.push({ sessionId: payload.session_id, seq: payload.event.seq })
      }
    },
    onReconnectPending: (sessionId, reason) => {
      reconnects.push({ sessionId, reason })
    },
    onResumeFailure: () => undefined,
  })

  try {
    await controller.ensure()
    await new Promise((resolve) => setImmediate(resolve))

    assert.deepEqual(websocketURLs, ['ws://127.0.0.1:7777/v3/realtime/stream'])
    assert.deepEqual(sent.map((message) => ({ kind: message.kind, session_id: message.session_id, after_seq: message.after_seq })), [
      { kind: 'subscribe.session', session_id: 'session-v3-a', after_seq: 2 },
      { kind: 'subscribe.session', session_id: 'session-v3-b', after_seq: 4 },
    ])

    for (const callback of listeners.get('message') ?? []) {
      callback(new MessageEvent('message', {
        data: JSON.stringify({
          protocol: 'v3.realtime',
          protocol_version: 1,
          kind: 'event',
          session_id: 'session-v3-b',
          last_seq: 5,
          high_watermark_seq: 5,
          endpoint_cursor: 'cursor-9',
          event_type: 'session.assistant.delta',
          event: {
            session_id: 'session-v3-b',
            seq: 5,
            event_type: 'session.assistant.delta',
            payload: { session_id: 'session-v3-b', delta: 'b' },
          },
        }),
      }))
      callback(new MessageEvent('message', {
        data: JSON.stringify({
          protocol: 'v3.realtime',
          protocol_version: 1,
          kind: 'cursor.error',
          session_id: 'session-v3-b',
          error_code: 'session_cursor_gap',
          error: 'refetch only b',
          last_seq: 4,
          next_seq: 6,
        }),
      }))
    }

    await new Promise((resolve) => setImmediate(resolve))
    await new Promise((resolve) => setImmediate(resolve))

    assert.deepEqual(frames, [{ sessionId: 'session-v3-b', seq: 5 }])
    assert.deepEqual(reconnects, [{ sessionId: 'session-v3-b', reason: 'refetch only b' }])
    assert.equal(websocketURLs.length, 1)
    assert.deepEqual(fetchCalls, ['/v1/auth/desktop/session', '/v3/sessions/session-v3-b'])
  } finally {
    controller.close()
    globalThis.fetch = originalFetch
    globalThis.window.setTimeout = originalSetTimeout
    globalThis.window = originalWindow
    globalThis.WebSocket = originalWebSocket
  }
})
