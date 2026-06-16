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

test('session.assistant.completed appends assistant message, clears draft, idles live state, and clears run intent', () => {
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
  state = applyFrame(state, v3EventFrame({ rev: 2, prevRev: 1, seq: 106, eventType: 'session.assistant.delta', payload: { run_id: 'run-v3', delta: 'Final text' } }))
  state = applyFrame(state, v3EventFrame({
    rev: 3,
    prevRev: 2,
    seq: 163,
    eventType: 'session.assistant.completed',
    payload: {
      run_id: 'run-v3',
      status: 'completed',
      message: {
        id: 'msg-assistant-final',
        session_id: 'session-v3',
        global_seq: 163,
        role: 'assistant',
        content: 'Final text',
        created_at: 1163,
      },
      run_intent: {
        session_id: 'session-v3',
        run_id: 'run-v3',
        status: 'completed',
        updated_at: 1163,
        event_seq: 163,
      },
    },
  }))

  const session = state.desktop.sessionsById['session-v3']
  assert.ok(session)
  assert.equal(state.desktop.rev, 3)
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
