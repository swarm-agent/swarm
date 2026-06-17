import assert from 'node:assert/strict'
import test from 'node:test'

import { createSessionV3ReducerInitialState, sessionV3Reducer } from './reducer'
import type { SessionV3RealtimeFrameWire } from './types'

function applyFrame(state: ReturnType<typeof createSessionV3ReducerInitialState>, frame: SessionV3RealtimeFrameWire) {
  return sessionV3Reducer(state, { type: 'frame', frame }).state
}

test('workset.session.removed clears auto-discovered reducer subscriptions and membership', () => {
  let state = createSessionV3ReducerInitialState()

  state = applyFrame(state, {
    kind: 'workset.session.discovered',
    session_id: 'session-auto',
    subscription_id: 'subscription-auto',
    workset_id: 'workset-1',
    workset_subscription_id: 'workset-subscription-1',
    auto_subscribed: true,
    endpoint_cursor: 'cursor-1',
  })

  assert.equal(state.subscriptionsBySessionId['session-auto']?.autoSubscribed, true)
  assert.deepEqual(state.subscriptionsBySessionId['session-auto']?.worksetIds, ['workset-1'])
  assert.deepEqual(state.worksetsById['workset-1']?.sessionIds, ['session-auto'])

  state = applyFrame(state, {
    kind: 'workset.session.removed',
    session_id: 'session-auto',
    subscription_id: 'subscription-auto',
    workset_id: 'workset-1',
    workset_subscription_id: 'workset-subscription-1',
    auto_subscribed: true,
    endpoint_cursor: 'cursor-2',
  })

  assert.equal(state.subscriptionsBySessionId['session-auto'], undefined)
  assert.deepEqual(state.discoveredSessionIds, [])
  assert.deepEqual(state.removedSessionIds, ['session-auto'])
  assert.deepEqual(state.worksetsById['workset-1']?.sessionIds, [])
  assert.deepEqual(state.worksetsById['workset-1']?.removedSessionIds, ['session-auto'])
  assert.equal(state.endpointCursor, 'cursor-2')
})

test('explicit subscribe frame makes a session manual so workset removal preserves it', () => {
  let state = createSessionV3ReducerInitialState()

  state = applyFrame(state, {
    kind: 'workset.session.discovered',
    session_id: 'session-manual',
    subscription_id: 'subscription-auto',
    workset_id: 'workset-1',
    workset_subscription_id: 'workset-subscription-1',
    auto_subscribed: true,
    endpoint_cursor: 'cursor-1',
  })
  state = applyFrame(state, {
    kind: 'subscribe.session',
    session_id: 'session-manual',
    subscription_id: 'subscription-manual',
    endpoint_cursor: 'cursor-2',
  })

  assert.equal(state.subscriptionsBySessionId['session-manual']?.autoSubscribed, false)
  assert.equal(state.subscriptionsBySessionId['session-manual']?.subscriptionId, 'subscription-manual')

  state = applyFrame(state, {
    kind: 'workset.session.removed',
    session_id: 'session-manual',
    subscription_id: 'subscription-auto',
    workset_id: 'workset-1',
    workset_subscription_id: 'workset-subscription-1',
    auto_subscribed: true,
    endpoint_cursor: 'cursor-3',
  })

  assert.equal(state.subscriptionsBySessionId['session-manual']?.autoSubscribed, false)
  assert.equal(state.subscriptionsBySessionId['session-manual']?.subscriptionId, 'subscription-manual')
  assert.deepEqual(state.worksetsById['workset-1']?.sessionIds, [])
  assert.deepEqual(state.worksetsById['workset-1']?.removedSessionIds, ['session-manual'])
})

test('reconnect snapshot seeds authoritative workset membership from canonical session order', () => {
  const initial = createSessionV3ReducerInitialState()
  const result = sessionV3Reducer(initial, {
    type: 'reconnect',
    result: {
      snapshot: {
        rev: 10,
        snapshotEndpointCursor: 'cursor-10',
        sessionsById: {
          'session-b': {
            id: 'session-b',
            title: 'B',
            workspacePath: '/repo',
            workspaceName: 'repo',
            mode: 'auto',
            sessionApi: 'v3',
            messageCount: 0,
            updatedAt: 20,
            createdAt: 10,
            permissionsHydrated: false,
            lifecycle: null,
            live: emptyLiveState(),
            pendingPermissions: [],
            pendingPermissionCount: 0,
            usage: null,
          },
          'session-a': {
            id: 'session-a',
            title: 'A',
            workspacePath: '/repo',
            workspaceName: 'repo',
            mode: 'auto',
            sessionApi: 'v3',
            messageCount: 0,
            updatedAt: 30,
            createdAt: 10,
            permissionsHydrated: false,
            lifecycle: null,
            live: emptyLiveState(),
            pendingPermissions: [],
            pendingPermissionCount: 0,
            usage: null,
          },
        },
        sessionOrder: ['session-a', 'session-b'],
      },
      endpointCursor: 'cursor-10',
      clientId: 'client-1',
      surface: 'desktop',
      worksetId: 'workset-1',
      subscriptions: [],
      worksets: [{
        workset_id: 'workset-1',
        subscription_id: 'workset-subscription-1',
        selector: { kind: 'global', global: true },
        auto_subscribe_sessions: true,
      }],
      realtimeResume: null,
      diagnosticsBySession: {},
      wire: { ok: true, rev: 10 },
    },
  })

  assert.deepEqual(result.state.worksetsById['workset-1']?.sessionIds, ['session-a', 'session-b'])
  assert.deepEqual(result.state.worksetsById['workset-1']?.removedSessionIds, [])
})

function emptyLiveState() {
  return {
    runId: null,
    agentName: null,
    startedAt: null,
    status: 'idle' as const,
    step: 0,
    toolName: null,
    sidebarToolName: null,
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
    reasoningState: 'idle' as const,
    reasoningSegment: 0,
    reasoningStartedAt: null,
    reasoningCompletedAt: null,
    reasoningTimelineSeq: 0,
    reasoningHistory: [],
    awaitingAck: false,
  }
}

function v3EventFrame(input: {
  sessionId?: string
  endpointCursor?: string
  rev: number
  prevRev: number
  seq?: number
  eventType: string
  payload?: Record<string, unknown>
}): SessionV3RealtimeFrameWire {
  const sessionId = input.sessionId ?? 'session-v3'
  const seq = input.seq ?? input.rev
  return {
    protocol: 'v3.realtime',
    protocol_version: 1,
    kind: 'event',
    session_id: sessionId,
    endpoint_cursor: input.endpointCursor ?? `cursor-${input.rev}`,
    last_seq: seq,
    high_watermark_seq: seq,
    rev: input.rev,
    prevRev: input.prevRev,
    event_type: input.eventType,
    event: {
      id: `v3evt_${sessionId}_${String(seq).padStart(20, '0')}`,
      session_id: sessionId,
      seq,
      event_type: input.eventType,
      ts_unix_ms: 1000 + seq,
      payload: { session_id: sessionId, ...input.payload },
    },
  }
}

test('session.assistant.started applies running runIntent from V3 realtime frame', () => {
  let state = createSessionV3ReducerInitialState()

  state = applyFrame(state, v3EventFrame({
    rev: 1,
    prevRev: 0,
    seq: 7,
    eventType: 'session.assistant.started',
    payload: {
      run_id: 'run-v3',
      run_intent: {
        session_id: 'session-v3',
        run_id: 'run-v3',
        status: 'running',
        created_at: 1007,
        updated_at: 1007,
        event_seq: 7,
      },
    },
  }))

  const session = state.desktop.sessionsById['session-v3']
  assert.ok(session)
  assert.equal(state.desktop.rev, 1)
  assert.equal(session.runIntent?.runId, 'run-v3')
  assert.equal(session.runIntent?.status, 'running')
  assert.equal(state.desktop.runIntentsBySessionId['session-v3']?.runId, 'run-v3')
  assert.equal(session.live.status, 'running')
  assert.equal(session.live.runId, 'run-v3')
  assert.equal(session.live.lastEventType, 'session.assistant.started')
})

test('session.assistant.delta appends assistantDraft from V3 realtime frame', () => {
  let state = createSessionV3ReducerInitialState()

  state = applyFrame(state, v3EventFrame({
    rev: 1,
    prevRev: 0,
    seq: 7,
    eventType: 'session.assistant.started',
    payload: {
      run_id: 'run-v3',
      run_intent: { session_id: 'session-v3', run_id: 'run-v3', status: 'running', event_seq: 7 },
    },
  }))
  state = applyFrame(state, v3EventFrame({ rev: 2, prevRev: 1, seq: 106, eventType: 'session.assistant.delta', payload: { run_id: 'run-v3', delta: 'Hey! 👋\n\n' } }))
  state = applyFrame(state, v3EventFrame({ rev: 3, prevRev: 2, seq: 120, eventType: 'session.assistant.delta', payload: { run_id: 'run-v3', delta: 'Streaming works.' } }))

  const live = state.desktop.sessionsById['session-v3']?.live
  assert.equal(state.desktop.rev, 3)
  assert.equal(live?.assistantDraft, 'Hey! 👋\n\nStreaming works.')
  assert.equal(live?.status, 'running')
  assert.equal(live?.lastEventType, 'session.assistant.delta')
})

test('session.assistant.completed appends assistant message, clears draft, updates projection fields, idles live state, and clears run intent', () => {
  let state = createSessionV3ReducerInitialState()

  state = applyFrame(state, v3EventFrame({
    rev: 51921,
    prevRev: 0,
    seq: 40,
    eventType: 'session.assistant.started',
    payload: {
      run_id: 'run-v3',
      run_intent: { session_id: 'session-v3', run_id: 'run-v3', status: 'running', event_seq: 40 },
    },
  }))
  state = applyFrame(state, v3EventFrame({ rev: 51922, prevRev: 51921, seq: 41, eventType: 'session.assistant.delta', payload: { run_id: 'run-v3', delta: 'Final text' } }))
  state = applyFrame(state, {
    ...v3EventFrame({
      rev: 51923,
      prevRev: 51922,
      seq: 42,
      eventType: 'session.assistant.completed',
      payload: {
        status: 'completed',
        session: {
          id: 'session-v3',
          title: 'User initiated conversation with hey',
          workspace_path: '/repo',
          workspace_name: 'repo',
          session_api: 'v3',
          message_count: 12,
          updated_at: 519230,
          last_message_at: 519230,
          last_event_seq: 42,
          projection_high_watermark_seq: 42,
        },
        message: {
          id: 'msg-assistant-final',
          session_id: 'session-v3',
          global_seq: 42,
          role: 'assistant',
          content: 'Final text',
          created_at: 519230,
        },
        run_intent: {
          session_id: 'session-v3',
          run_id: 'run-v3',
          status: 'completed',
          updated_at: 519230,
          event_seq: 42,
        },
      },
    }),
    endpoint_cursor: 'v3c1-completed',
    last_seq: 42,
    high_watermark_seq: 42,
    projection: {
      session_id: 'session-v3',
      last_event_seq: 42,
      projection_high_watermark_seq: 42,
      updated_at: 519230,
    },
  })

  const session = state.desktop.sessionsById['session-v3']
  assert.ok(session)
  assert.equal(state.desktop.rev, 51923)
  assert.equal(state.endpointCursor, 'v3c1-completed')
  assert.equal(session.title, 'User initiated conversation with hey')
  assert.equal(session.messageCount, 12)
  assert.equal(session.updatedAt, 519230)
  assert.equal(session.lastEventSeq, 42)
  assert.equal(session.projectionHighWatermarkSeq, 42)
  assert.equal(session.runIntent, null)
  assert.equal(state.desktop.runIntentsBySessionId['session-v3'], undefined)
  assert.equal(session.live.assistantDraft, '')
  assert.equal(session.live.status, 'idle')
  assert.equal(session.live.runId, null)
  assert.equal(session.live.lastEventType, 'session.assistant.completed')
  assert.deepEqual(state.desktop.messagesBySessionId['session-v3']?.map((message) => message.id), ['msg-assistant-final'])
  assert.equal(state.desktop.messagesBySessionId['session-v3']?.[0]?.content, 'Final text')
})

test('sendMessage mutation advances desktop rev before next realtime prevRev', () => {
  let state = createSessionV3ReducerInitialState()

  const mutation = sessionV3Reducer(state, {
    type: 'mutation',
    sessionId: 'session-v3',
    response: {
      ok: true,
      message: {
        id: 'msg-user-1',
        session_id: 'session-v3',
        global_seq: 5,
        role: 'user',
        content: 'hello',
        created_at: 1000,
      },
      run_intent: {
        session_id: 'session-v3',
        run_id: 'run-v3',
        status: 'pending_executor',
        created_at: 1001,
        updated_at: 1001,
        event_seq: 5,
      },
      realtime_outbox: {
        endpoint_seq: 1,
        endpoint_cursor: 'cursor-1',
        session_id: 'session-v3',
      },
    },
  })
  state = mutation.state

  assert.equal(state.desktop.rev, 1)
  assert.equal(state.endpointCursor, 'cursor-1')
  assert.equal(state.desktop.status, 'ready')

  const next = sessionV3Reducer(state, {
    type: 'frame',
    frame: v3EventFrame({
      endpointCursor: 'cursor-2',
      rev: 2,
      prevRev: 1,
      seq: 7,
      eventType: 'session.assistant.started',
      payload: {
        run_id: 'run-v3',
        run_intent: { session_id: 'session-v3', run_id: 'run-v3', status: 'running', event_seq: 7 },
      },
    }),
  })

  assert.equal(next.stale, false)
  assert.equal(next.state.desktop.status, 'ready')
  assert.equal(next.state.desktop.staleReason, null)
  assert.equal(next.state.desktop.rev, 2)
  assert.equal(next.state.endpointCursor, 'cursor-2')
})
