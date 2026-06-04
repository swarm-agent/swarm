import assert from 'node:assert/strict'
import test from 'node:test'

import type { DesktopSessionRecord, DesktopStoreState } from '../types/realtime'
import { applyEnvelope, useDesktopStore } from './use-desktop-store'

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

test('desktop panel and controller route V3 streams to Sessions API v3 only', async () => {
  const { readFile } = await import('node:fs/promises')
  const controllerSource = await readFile(new URL('./run-stream-controller.ts', import.meta.url), 'utf8')
  const querySource = await readFile(new URL('../chat/queries/chat-queries.ts', import.meta.url), 'utf8')
  const panelSource = await readFile(new URL('../chat/components/desktop-chat-panel.tsx', import.meta.url), 'utf8')

  assert.match(querySource, /`\/v3\/sessions\/\$\{encodeURIComponent\(normalizedSessionId\)\}\/stream`/)
  assert.match(querySource, /url\.searchParams\.set\("after_seq"/)
  assert.match(controllerSource, /sessionApi: resumeRequest\.sessionApi/)
  assert.match(controllerSource, /type === 'cursor\.error'/)
  assert.match(controllerSource, /if \(isV3ResumeRequest\(request\)\) \{\n\s+return\n\s+\}/)
  assert.match(panelSource, /liveSession\?\.sessionApi\?\.trim\(\)\.toLowerCase\(\) === 'v3'/)
  assert.doesNotMatch(querySource, /\/v3\/sessions\/[^`]+\/run\/stream/)
})

test('desktop store submitPrompt for V3 primary sessions commits through Sessions API v3 without V2 run dispatch', async () => {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)
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

    const urls = calls.map((entry) => String(entry.input))
    assert.deepEqual(urls, ['/v3/sessions/session-v3/messages'])
    assert.equal(urls.some((url) => url.startsWith('/v1/swarm/managed-hosts/sessions')), false)
    assert.equal(urls.some((url) => url.startsWith('/v2/sessions')), false)
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
    assert.equal(updated.live.lastEventType, 'run.pending_executor')
  } finally {
    globalThis.fetch = originalFetch
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
