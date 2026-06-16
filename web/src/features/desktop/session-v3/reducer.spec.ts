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
