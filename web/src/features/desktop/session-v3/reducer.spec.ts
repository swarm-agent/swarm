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

test('sync snapshot seeds authoritative workset membership from canonical session order', () => {
  const initial = createSessionV3ReducerInitialState()
  const result = sessionV3Reducer(initial, {
    type: 'sync-snapshot',
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
      wire: { ok: true, rev: 10 },
    },
    subscriptions: [],
    worksets: [{
        workset_id: 'workset-1',
        subscription_id: 'workset-subscription-1',
        selector: { kind: 'global', global: true },
        auto_subscribe_sessions: true,
    }],
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

test('minimal mutation responses advance only from realtime outbox cursor and apply preferences', () => {
  let state = createSessionV3ReducerInitialState()

  const modeOnly = sessionV3Reducer(state, {
    type: 'mutation',
    sessionId: 'session-v3',
    response: {
      ok: true,
      session_id: 'session-v3',
      realtime_outbox: {
        endpoint_seq: 3,
        endpoint_cursor: 'cursor-3',
        session_id: 'session-v3',
      },
    },
  })
  state = modeOnly.state

  assert.equal(state.desktop.rev, 3)
  assert.equal(state.endpointCursor, 'cursor-3')
  assert.equal(state.desktop.sessionsById['session-v3'], undefined)

  const preference = sessionV3Reducer(state, {
    type: 'mutation',
    sessionId: 'session-v3',
    response: {
      ok: true,
      session_id: 'session-v3',
      preference: { provider: 'openai', model: 'gpt-4.1', thinking: 'medium', service_tier: 'auto', context_mode: 'full', updated_at: 42 },
      mutation: { realtime_outbox: { endpoint_seq: 4, endpoint_cursor: 'cursor-4', session_id: 'session-v3' } },
    },
  })
  state = preference.state

  assert.equal(state.desktop.rev, 4)
  assert.equal(state.endpointCursor, 'cursor-4')
  assert.equal(state.desktop.preferencesBySessionId['session-v3']?.preference.model, 'gpt-4.1')
})

test('minimal mode metadata and agent mutation responses update local session state', () => {
  let state = createSessionV3ReducerInitialState()
  state = sessionV3Reducer(state, {
    type: 'snapshot',
    snapshot: {
      rev: 1,
      sessionsById: {
        'session-v3': { id: 'session-v3', title: 'Existing', workspacePath: '/repo', workspaceName: 'repo', mode: 'auto', metadata: { old: true }, sessionApi: 'v3', messageCount: 0, updatedAt: 1, createdAt: 1, permissionsHydrated: false, lifecycle: null, live: emptyLiveState(), pendingPermissions: [], pendingPermissionCount: 0, usage: null },
      },
      sessionOrder: ['session-v3'],
      snapshotEndpointCursor: 'cursor-1',
    },
  }).state

  state = sessionV3Reducer(state, {
    type: 'mutation',
    sessionId: 'session-v3',
    response: {
      ok: true,
      session_id: 'session-v3',
      mode: 'manual',
      metadata: { next: true },
      realtime_outbox: { endpoint_seq: 2, endpoint_cursor: 'cursor-2', session_id: 'session-v3' },
    },
  }).state

  assert.equal(state.desktop.sessionsById['session-v3']?.mode, 'manual')
  assert.deepEqual(state.desktop.sessionsById['session-v3']?.metadata, { next: true })
  assert.equal(state.desktop.sessionsById['session-v3']?.title, 'Existing')

  state = sessionV3Reducer(state, {
    type: 'mutation',
    sessionId: 'session-v3',
    response: {
      ok: true,
      session_id: 'session-v3',
      agent_model_policy: {
        agent_name: 'explorer',
        resolved_agent_name: 'explorer',
        source: 'agent',
        locked: true,
        reason: 'agent selected',
        preference: { provider: 'codex', model: 'gpt-5.4', thinking: 'medium', service_tier: 'flex', context_mode: 'large', updated_at: 3 },
        context_window: 1000,
        max_output_tokens: 8192,
      },
      realtime_outbox: { endpoint_seq: 3, endpoint_cursor: 'cursor-3', session_id: 'session-v3' },
    },
  }).state

  assert.equal(state.endpointCursor, 'cursor-3')
  assert.equal(state.desktop.agentModelPolicyBySessionId['session-v3']?.resolvedAgentName, 'explorer')
  assert.equal(state.desktop.agentModelPolicyBySessionId['session-v3']?.preference.model, 'gpt-5.4')
})

test('minimal compact and stop mutation responses reconcile run intent state', () => {
  let state = createSessionV3ReducerInitialState()

  state = sessionV3Reducer(state, {
    type: 'mutation',
    sessionId: 'session-v3',
    response: {
      ok: true,
      session_id: 'session-v3',
      compaction: { run_id: 'run-compact', status: 'running', owner_transport: 'desktop' },
      realtime_outbox: { endpoint_seq: 1, endpoint_cursor: 'cursor-1', session_id: 'session-v3' },
    },
  }).state

  assert.equal(state.desktop.runIntentsBySessionId['session-v3']?.runId, 'run-compact')
  assert.equal(state.desktop.runIntentsBySessionId['session-v3']?.status, 'running')

  state = sessionV3Reducer(state, {
    type: 'snapshot',
    snapshot: {
      rev: 2,
      sessionsById: {
        'session-v3': { id: 'session-v3', title: 'Existing', workspacePath: '/repo', workspaceName: 'repo', mode: 'auto', sessionApi: 'v3', messageCount: 0, updatedAt: 2, createdAt: 1, permissionsHydrated: false, lifecycle: null, runIntent: state.desktop.runIntentsBySessionId['session-v3'], live: { ...emptyLiveState(), runId: 'run-compact', status: 'running' }, pendingPermissions: [], pendingPermissionCount: 0, usage: null },
      },
      sessionOrder: ['session-v3'],
      runIntentsBySessionId: state.desktop.runIntentsBySessionId,
    },
    mode: 'merge',
  }).state

  state = sessionV3Reducer(state, {
    type: 'mutation',
    sessionId: 'session-v3',
    response: {
      ok: true,
      session_id: 'session-v3',
      run_id: 'run-compact',
      status: 'cancelled',
      realtime_outbox: { endpoint_seq: 3, endpoint_cursor: 'cursor-3', session_id: 'session-v3' },
    },
  }).state

  assert.equal(state.desktop.runIntentsBySessionId['session-v3'], undefined)
  assert.equal(state.desktop.sessionsById['session-v3']?.runIntent, null)
  assert.equal(state.desktop.sessionsById['session-v3']?.live.status, 'idle')
})

test('minimal plan and permission mutation responses update local state without hydrate fields', () => {
  let state = createSessionV3ReducerInitialState()
  state = sessionV3Reducer(state, {
    type: 'snapshot',
    snapshot: {
      rev: 1,
      sessionsById: {
        'session-v3': { id: 'session-v3', title: 'Existing', workspacePath: '/repo', workspaceName: 'repo', mode: 'auto', sessionApi: 'v3', messageCount: 0, updatedAt: 1, createdAt: 1, permissionsHydrated: false, lifecycle: null, live: emptyLiveState(), pendingPermissions: [], pendingPermissionCount: 0, usage: null },
      },
      sessionOrder: ['session-v3'],
    },
  }).state

  state = sessionV3Reducer(state, {
    type: 'mutation',
    sessionId: 'session-v3',
    response: {
      ok: true,
      session_id: 'session-v3',
      plan: { id: 'plan-1', title: 'Plan', plan: 'Do it', status: 'active', approval_state: 'approved', updated_at: 2 },
      plan_revisions: [{ id: 'plan-1', title: 'Plan', plan: 'Do it', status: 'active', version: 1, created_at: 2 }],
      realtime_outbox: { endpoint_seq: 2, endpoint_cursor: 'cursor-2', session_id: 'session-v3' },
    },
  }).state

  assert.equal(state.desktop.plansBySessionId['session-v3']?.id, 'plan-1')
  assert.equal(state.desktop.planRevisionsBySessionId['session-v3']?.length, 1)

  state = sessionV3Reducer(state, {
    type: 'mutation',
    sessionId: 'session-v3',
    response: {
      ok: true,
      session_id: 'session-v3',
      permission: { id: 'perm-1', session_id: 'session-v3', run_id: 'run-1', call_id: 'call-1', tool_name: 'bash', tool_arguments: '{}', status: 'pending', requirement: 'approval', mode: 'auto', created_at: 3, updated_at: 3 },
      realtime_outbox: { endpoint_seq: 3, endpoint_cursor: 'cursor-3', session_id: 'session-v3' },
    },
  }).state

  assert.equal(state.desktop.permissionsById['perm-1']?.status, 'pending')
  assert.equal(state.desktop.sessionsById['session-v3']?.pendingPermissionCount, 1)

  state = sessionV3Reducer(state, {
    type: 'mutation',
    sessionId: 'session-v3',
    response: {
      ok: true,
      session_id: 'session-v3',
      permission: { id: 'perm-1', session_id: 'session-v3', run_id: 'run-1', call_id: 'call-1', tool_name: 'bash', tool_arguments: '{}', status: 'approved', decision: 'approve', requirement: 'approval', mode: 'auto', created_at: 3, updated_at: 4, resolved_at: 4 },
      realtime_outbox: { endpoint_seq: 4, endpoint_cursor: 'cursor-4', session_id: 'session-v3' },
    },
  }).state

  assert.equal(state.desktop.permissionsById['perm-1'], undefined)
  assert.equal(state.desktop.sessionsById['session-v3']?.pendingPermissionCount, 0)
})

test('sync stream replay is idempotent and advances cursor only from stream cursor', () => {
  let state = createSessionV3ReducerInitialState()
  state = sessionV3Reducer(state, {
    type: 'snapshot',
    snapshot: { rev: 1, snapshotEndpointCursor: 'cursor-1' },
    endpointCursor: 'cursor-1',
  }).state

  const response = {
    ok: true,
    endpoint_cursor: 'cursor-stream-3',
    after_endpoint_seq: 1,
    high_watermark_seq: 3,
    has_more: false,
    replay_instructions: { after_endpoint_cursor: 'cursor-stream-3' },
    events: [
      {
        endpoint_seq: 3,
        endpoint_cursor: 'cursor-event-ignored',
        session_id: 'session-v3',
        event: {
          id: 'event-3',
          session_id: 'session-v3',
          seq: 9,
          event_type: 'session.assistant.started',
          ts_unix_ms: 1009,
          payload: {
            run_id: 'run-v3',
            run_intent: { session_id: 'session-v3', run_id: 'run-v3', status: 'running', event_seq: 9 },
          },
        },
        projection: { session_id: 'session-v3', last_event_seq: 9, projection_high_watermark_seq: 9, updated_at: 1009 },
        created_at: 1009,
      },
    ],
  }

  const first = sessionV3Reducer(state, { type: 'sync-stream-result', response })
  assert.equal(first.state.endpointCursor, 'cursor-stream-3')
  assert.equal(first.state.desktop.rev, 3)
  assert.equal(first.state.desktop.sessionsById['session-v3']?.runIntent?.status, 'running')

  const duplicate = sessionV3Reducer(first.state, { type: 'sync-stream-result', response })
  assert.equal(duplicate.state.endpointCursor, 'cursor-stream-3')
  assert.equal(duplicate.state.desktop.rev, 3)
  assert.equal(duplicate.duplicate, true)
})

test('sync snapshot tombstones remove sessions and subscriptions', () => {
  let state = createSessionV3ReducerInitialState()
  state = sessionV3Reducer(state, {
    type: 'sync-snapshot',
    result: {
      snapshot: {
        rev: 1,
        snapshotEndpointCursor: 'cursor-1',
        sessionsById: {
          'session-remove': { id: 'session-remove', title: 'Remove me', workspacePath: '/repo', workspaceName: 'repo', mode: 'auto', sessionApi: 'v3', messageCount: 0, updatedAt: 1, createdAt: 1, permissionsHydrated: false, lifecycle: null, live: emptyLiveState(), pendingPermissions: [], pendingPermissionCount: 0, usage: null },
        },
        sessionOrder: ['session-remove'],
      },
      endpointCursor: 'cursor-1',
      tombstonesBySession: {},
      replayInstructions: null,
      syncScope: null,
      scopeId: '',
      selector: null,
      wire: { ok: true, rev: 1, snapshot_endpoint_cursor: 'cursor-1' },
    },
    subscriptions: [{ session_id: 'session-remove', subscription_id: 'sub-remove', endpoint_cursor: 'cursor-1' }],
  }).state

  state = sessionV3Reducer(state, {
    type: 'sync-snapshot',
    result: {
      snapshot: { rev: 2, snapshotEndpointCursor: 'cursor-2', sessionsById: {}, sessionOrder: [] },
      endpointCursor: 'cursor-2',
      tombstonesBySession: { 'session-remove': { session_id: 'session-remove', deleted: true } },
      replayInstructions: null,
      syncScope: null,
      scopeId: '',
      selector: null,
      wire: { ok: true, rev: 2, snapshot_endpoint_cursor: 'cursor-2', tombstones_by_session: { 'session-remove': { session_id: 'session-remove', deleted: true } } },
    },
    subscriptions: [{ session_id: 'session-remove', subscription_id: 'sub-remove', endpoint_cursor: 'cursor-2' }],
  }).state

  assert.equal(state.desktop.sessionsById['session-remove'], undefined)
  assert.equal(state.subscriptionsBySessionId['session-remove'], undefined)
  assert.deepEqual(state.removedSessionIds, ['session-remove'])
})
