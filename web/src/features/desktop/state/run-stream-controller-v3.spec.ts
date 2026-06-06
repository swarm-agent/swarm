import assert from 'node:assert/strict'
import { afterEach, test } from 'node:test'

import { queryClient } from '../../../app/query-client'
import { sessionMessagesQueryKey } from '../../queries/query-options'
import type { DesktopSessionRecord, DesktopStoreState } from '../types/realtime'
import { applyEnvelope, useDesktopStore } from './use-desktop-store'
import type { RunStreamEventMessage } from './run-stream-controller'

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

test('V3 stream keeps reused provider call IDs as separate live tool history records', () => {
  const session = makeSession({ id: 'session-v3', sessionApi: 'v3', lastEventSeq: 1, projectionHighWatermarkSeq: 1 })
  useDesktopStore.setState(makeState(session), true)

  const emitToolEvent = (seq: number, eventType: 'session.tool.started' | 'session.tool.completed', step: number, rawOutput: string) => {
    useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
      type: 'event',
      ok: true,
      session_id: 'session-v3',
      last_seq: seq,
      event: {
        id: `v3evt_session-v3_${String(seq).padStart(20, '0')}`,
        session_id: 'session-v3',
        seq,
        event_type: eventType,
        ts_unix_ms: 20 + seq,
        payload: {
          session_id: 'session-v3',
          run_id: 'run-v3',
          step_id: `step-${step}`,
          tool_instance_id: `step-${step}:call-reused`,
          tool_name: 'read',
          call_id: 'call-reused',
          arguments: JSON.stringify({ path: `${step}.txt` }),
          raw_output: rawOutput,
          step,
        },
      },
    }, 20 + seq)
  }

  emitToolEvent(2, 'session.tool.started', 1, '')
  emitToolEvent(3, 'session.tool.completed', 1, 'first')
  emitToolEvent(4, 'session.tool.started', 2, '')
  emitToolEvent(5, 'session.tool.completed', 2, 'second')

  const updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.live.toolHistory?.length, 2)
  assert.deepEqual(updated.live.toolHistory?.map((item) => item.callId), ['call-reused', 'call-reused'])
  assert.deepEqual(updated.live.toolHistory?.map((item) => item.toolInstanceId), ['step-2:call-reused', 'step-1:call-reused'])
  assert.deepEqual(updated.live.toolHistory?.map((item) => item.toolOutput), ['second', 'first'])
  assert.deepEqual(updated.live.toolHistory?.map((item) => item.seq), [4, 2])
})

test('V3 stream retains sequence on interleaved assistant segments and live tools', () => {
  const session = makeSession({ id: 'session-v3', sessionApi: 'v3', lastEventSeq: 1, projectionHighWatermarkSeq: 1 })
  useDesktopStore.setState(makeState(session), true)

  const emit = (seq: number, eventType: string, payload: Record<string, unknown>) => {
    useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
      type: 'event',
      ok: true,
      session_id: 'session-v3',
      last_seq: seq,
      event: {
        id: `v3evt_session-v3_${String(seq).padStart(20, '0')}`,
        session_id: 'session-v3',
        seq,
        event_type: eventType,
        ts_unix_ms: 20 + seq,
        payload: { session_id: 'session-v3', run_id: 'run-v3', ...payload },
      },
    }, 20 + seq)
  }

  emit(2, 'session.assistant.delta', { delta: 'SEGMENT A' })
  emit(3, 'session.tool.started', { step_id: 'step-1', tool_instance_id: 'step-1:call-1', tool_name: 'list', call_id: 'call-1', arguments: '{}', step: 1 })
  emit(4, 'session.tool.completed', { step_id: 'step-1', tool_instance_id: 'step-1:call-1', tool_name: 'list', call_id: 'call-1', raw_output: 'first', step: 1 })
  emit(5, 'session.assistant.delta', { delta: 'SEGMENT B' })
  emit(6, 'session.tool.started', { step_id: 'step-2', tool_instance_id: 'step-2:call-2', tool_name: 'list', call_id: 'call-2', arguments: '{}', step: 2 })

  const updated = useDesktopStore.getState().sessions['session-v3']
  assert.deepEqual(updated.live.retainedAssistantSegments.map((segment) => [segment.content, segment.seq]), [['SEGMENT A', 2], ['SEGMENT B', 5]])
  assert.deepEqual(updated.live.toolHistory?.map((item) => [item.callId, item.seq]), [['call-2', 6], ['call-1', 3]])
})

test('V3 assistant draft promotion ignores stale scheduled flushes after tool start', () => {
  const originalWindow = globalThis.window
  const scheduled: Array<() => void> = []
  const canceled = new Set<number>()
  const testWindow = (originalWindow ?? {}) as typeof window
  testWindow.requestAnimationFrame = ((callback: FrameRequestCallback) => {
    const id = scheduled.length + 1
    scheduled.push(() => {
      if (!canceled.has(id)) callback(0)
    })
    return id
  }) as typeof window.requestAnimationFrame
  testWindow.cancelAnimationFrame = ((id: number) => {
    canceled.add(id)
  }) as typeof window.cancelAnimationFrame
  testWindow.setTimeout = ((callback: TimerHandler) => {
    if (typeof callback === 'function') callback()
    return 0
  }) as typeof window.setTimeout
  globalThis.window = testWindow

  try {
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
        event_type: 'session.assistant.delta',
        ts_unix_ms: 20,
        payload: { session_id: 'session-v3', run_id: 'run-v3', delta: 'First message' },
      },
    }, 20)

    useDesktopStore.getState().__testApplyRunStreamFrame?.('session-v3', {
      type: 'event',
      ok: true,
      session_id: 'session-v3',
      last_seq: 3,
      event: {
        id: 'v3evt_session-v3_00000000000000000003',
        session_id: 'session-v3',
        seq: 3,
        event_type: 'session.tool.started',
        ts_unix_ms: 21,
        payload: { session_id: 'session-v3', run_id: 'run-v3', step_id: 'step-1', tool_instance_id: 'step-1:call-1', tool_name: 'list', call_id: 'call-1', arguments: '{}', step: 1 },
      },
    }, 21)

    scheduled.forEach((callback) => callback())

    const updated = useDesktopStore.getState().sessions['session-v3']
    assert.equal(updated.live.assistantDraft, '')
    assert.equal(updated.live.retainedAssistantSegments.length, 1)
    assert.equal(updated.live.retainedAssistantSegments[0]?.content, 'First message')
  } finally {
    if (originalWindow) {
      globalThis.window = originalWindow
    } else {
      Reflect.deleteProperty(globalThis, 'window')
    }
  }
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

test('desktop V3 canonical session updates do not depend on V3-only realtime sockets', async () => {
  const { readFile } = await import('node:fs/promises')
  const controllerSource = await readFile(new URL('./run-stream-controller.ts', import.meta.url), 'utf8')
  const querySource = await readFile(new URL('../chat/queries/chat-queries.ts', import.meta.url), 'utf8')
  const panelSource = await readFile(new URL('../chat/components/desktop-chat-panel.tsx', import.meta.url), 'utf8')
  const storeSource = await readFile(new URL('./use-desktop-store.ts', import.meta.url), 'utf8')

  assert.match(querySource, /V3 sessions use the global \/ws session:\* connection/)
  assert.doesNotMatch(storeSource, /DesktopV3RealtimeController/)
  assert.doesNotMatch(storeSource, /requireV3RealtimeController/)
  assert.doesNotMatch(storeSource, /resolveV3RealtimeSubscriptions/)
  assert.doesNotMatch(storeSource, /applyV3RealtimeFrame/)
  assert.doesNotMatch(storeSource, /subscribe\.session/)
  assert.match(storeSource, /if \(sessionApi === 'v3'\) \{\n\s+set\(\{ realtimeDesired: true \}\)\n\s+await get\(\)\.connect\(\)\n\s+return\n\s+\}/)
  assert.match(panelSource, /liveSession\?\.sessionApi\?\.trim\(\)\.toLowerCase\(\) === 'v3'/)
  assert.match(panelSource, /session\.tool\.started/)
  assert.match(panelSource, /session\.tool\.delta/)
  assert.match(panelSource, /session\.tool\.completed/)
  assert.doesNotMatch(controllerSource, /\/v3\/sessions\/[^`]+\/stream/)
  assert.doesNotMatch(querySource, /\/v3\/sessions\/[^`]+\/stream/)
  assert.doesNotMatch(querySource, /\/v3\/sessions\/[^`]+\/run\/stream/)
})

test('desktop store submitPrompt for V3 primary sessions commits through Sessions API v3 and uses global websocket only', async () => {
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
    setInterval: ((callback: TimerHandler, timeout?: number) => setInterval(callback, timeout)) as typeof window.setInterval,
    clearInterval: ((timer?: number) => clearInterval(timer)) as typeof window.clearInterval,
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
    assert.deepEqual(websocketURLs, ['ws://127.0.0.1:7777/ws'])
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
test('global /ws V3 session title envelope updates Desktop title from canonical payload', () => {
  const session = makeSession({ id: 'session-v3', title: 'New chat', sessionApi: 'v3' })
  const patch = applyEnvelope(makeState(session), {
    global_seq: 10,
    stream: 'session:session-v3',
    event_type: 'session.title.updated',
    entity_id: 'session-v3',
    ts_unix_ms: 100,
    payload: {
      session_id: 'session-v3',
      title: 'Generated title',
      updated_at: 100,
      session: {
        id: 'session-v3',
        title: 'Generated title',
        workspace_path: '/repo',
        workspace_name: 'repo',
        mode: 'auto',
        session_api: 'v3',
        message_count: 1,
        created_at: 1,
        updated_at: 100,
      },
    },
  })

  const updated = patch.sessions?.['session-v3']
  assert.equal(updated?.title, 'Generated title')
  assert.equal(updated?.sessionApi, 'v3')
  assert.equal(updated?.live.lastEventType, null)
  assert.equal(patch.lastGlobalSeq, 10)
})

test('global /ws V3 message and run-intent envelopes update Desktop canonical state', async () => {
  const session = makeSession({ id: 'session-v3', sessionApi: 'v3' })
  useDesktopStore.setState(makeState(session), true)

  useDesktopStore.setState((state) => applyEnvelope(state, {
    global_seq: 11,
    stream: 'session:session-v3',
    event_type: 'session.message.appended',
    entity_id: 'session-v3',
    ts_unix_ms: 101,
    payload: {
      session_id: 'session-v3',
      message: {
        id: 'msg-user-v3',
        session_id: 'session-v3',
        global_seq: 11,
        role: 'user',
        content: 'hello global',
        created_at: 101,
      },
    },
  }))
  useDesktopStore.setState((state) => applyEnvelope(state, {
    global_seq: 12,
    stream: 'session:session-v3',
    event_type: 'session.run_intent.recorded',
    entity_id: 'session-v3',
    ts_unix_ms: 102,
    payload: {
      session_id: 'session-v3',
      run_intent: {
        session_id: 'session-v3',
        run_id: 'run-v3',
        status: 'pending_executor',
        event_seq: 12,
        created_at: 102,
        updated_at: 102,
      },
    },
  }))

  const updated = useDesktopStore.getState().sessions['session-v3']
  assert.equal(updated.messageCount, 1)
  assert.equal(updated.live.runId, 'run-v3')
  assert.equal(updated.live.status, 'starting')
  assert.equal(updated.live.summary, 'Pending executor…')
  assert.equal(updated.live.lastEventType, 'session.run_intent.recorded')
  assert.equal(useDesktopStore.getState().lastGlobalSeq, 12)
  await new Promise((resolve) => setTimeout(resolve, 0))
  assert.equal(queryClient.getQueryData<unknown[]>(sessionMessagesQueryKey('session-v3'))?.length, 1)
})

test('global /ws V3 assistant lifecycle envelopes update Desktop live and message state', () => {
  const originalWindow = globalThis.window
  const testWindow = originalWindow ?? {} as typeof window
  testWindow.setTimeout = ((callback: TimerHandler) => {
    if (typeof callback === 'function') callback()
    return 0
  }) as typeof window.setTimeout
  globalThis.window = testWindow
  const session = makeSession({ id: 'session-v3', sessionApi: 'v3' })
  useDesktopStore.setState(makeState(session), true)

  try {
    useDesktopStore.setState((state) => applyEnvelope(state, {
      global_seq: 13,
      stream: 'session:session-v3',
      event_type: 'session.assistant.delta',
      entity_id: 'session-v3',
      ts_unix_ms: 103,
      payload: { session_id: 'session-v3', run_id: 'run-v3', delta: 'hi' },
    }))
    let updated = useDesktopStore.getState().sessions['session-v3']
    assert.equal(updated.live.assistantDraft, 'hi')
    assert.equal(updated.live.status, 'running')
    assert.equal(updated.live.lastEventType, 'session.assistant.delta')

    useDesktopStore.setState((state) => applyEnvelope(state, {
      global_seq: 14,
      stream: 'session:session-v3',
      event_type: 'session.assistant.completed',
      entity_id: 'session-v3',
      ts_unix_ms: 104,
      payload: {
        session_id: 'session-v3',
        run_id: 'run-v3',
        message: {
          id: 'msg-assistant-v3',
          session_id: 'session-v3',
          global_seq: 14,
          role: 'assistant',
          content: 'hi',
          created_at: 104,
        },
        run_intent: { session_id: 'session-v3', run_id: 'run-v3', status: 'completed' },
      },
    }))

    updated = useDesktopStore.getState().sessions['session-v3']
    assert.equal(updated.live.status, 'idle')
    assert.equal(updated.live.runId, null)
    assert.equal(updated.live.assistantDraft, '')
    assert.equal(updated.live.lastEventType, 'session.assistant.completed')
    assert.equal(updated.messageCount, 1)
    assert.equal(useDesktopStore.getState().lastGlobalSeq, 14)
  } finally {
    if (originalWindow) {
      globalThis.window = originalWindow
    } else {
      Reflect.deleteProperty(globalThis, 'window')
    }
  }
})

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

test('V3 ensureRunStream uses only the global /ws socket and subscribes session wildcard', async () => {
  const websocketURLs: string[] = []
  const sent: Array<Record<string, unknown>> = []
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
    setInterval: ((callback: TimerHandler, timeout?: number) => setInterval(callback, timeout)) as typeof window.setInterval,
    clearInterval: ((timer?: number) => clearInterval(timer)) as typeof window.clearInterval,
  } as unknown as Window & typeof globalThis
  globalThis.WebSocket = FakeWebSocket as unknown as typeof WebSocket
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const url = String(input)
    if (url === '/v1/auth/desktop/session') {
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    useDesktopStore.setState(makeState(makeSession({ id: 'session-v3-a', sessionApi: 'v3' })), true)

    await useDesktopStore.getState().ensureRunStream('session-v3-a')
    await new Promise((resolve) => setImmediate(resolve))

    assert.deepEqual(websocketURLs, ['ws://127.0.0.1:7777/ws'])
    assert.equal(sent.some((message) => message.type === 'subscribe' && message.channel === 'session:*'), true)
    assert.equal(sent.some((message) => message.kind === 'subscribe.session'), false)
    assert.equal(websocketURLs.some((url) => url.includes('/v3/realtime/stream')), false)
  } finally {
    useDesktopStore.getState().disconnect()
    globalThis.fetch = originalFetch
    globalThis.window = originalWindow
    globalThis.WebSocket = originalWebSocket
  }
})
