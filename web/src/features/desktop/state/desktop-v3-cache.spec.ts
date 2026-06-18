import test from 'node:test'
import assert from 'node:assert/strict'
import {
  hydrateResponseToAction,
  reconnectResponseToActions,
  realtimeFrameToActions,
  syncStreamResponseToAction,
} from './desktop-v3-cache-wire'
import {
  applyBootstrapSnapshot,
  applyHydrateSnapshot,
  applyMessageMutationResult,
  applyRealtimeFrame,
  applySyncStreamBatch,
  createEmptyDesktopV3CacheState,
  desktopV3CacheReducer,
  upsertPendingUserMessage,
} from './desktop-v3-cache-reducer'
import {
  hydrateSnapshotFixture,
  messageA1,
  messageB1,
  messageMutationFixture,
  reconnectFixture,
  realtimeFrameFixture,
  sessionA,
  sessionB,
  snapshotFixture,
  syncStreamFixture,
  tombstoneB,
} from './desktop-v3-cache.backend-fixtures'
import type { DesktopV3CacheState } from './desktop-v3-cache-types'

function bootstrappedState(): DesktopV3CacheState {
  return applyBootstrapSnapshot(createEmptyDesktopV3CacheState(), snapshotFixture())
}

test('bootstrap stores scoped cursor, scope metadata, orders, run intents, and only present message keys', () => {
  const state = bootstrappedState()
  const scopeId = 'selector-hash:messages,run_intents'

  assert.equal(state.syncScopesById[scopeId].endpointCursor, 'cursor-bootstrap-1')
  assert.equal(state.syncScopesById[scopeId].selector.workspace_path, '/repo')
  assert.deepEqual(state.sessionOrderByScope[scopeId], [sessionA.id, sessionB.id])
  assert.equal(state.messagesBySession[sessionA.id].items.length, 2)
  assert.equal(state.messagesBySession[sessionB.id], undefined)
  assert.equal(state.runIntentsBySession[sessionA.id]['run-a'].status, 'running')
})

test('metadata-only bootstrap preserves existing transcripts when messages_by_session is empty', () => {
  const state = bootstrappedState()
  applyBootstrapSnapshot(state, snapshotFixture({
    snapshot_endpoint_cursor: 'cursor-bootstrap-2',
    messages_by_session: {},
    session_order: [sessionA.id],
  }))

  assert.equal(state.syncScopesById['selector-hash:messages,run_intents'].endpointCursor, 'cursor-bootstrap-2')
  assert.deepEqual(state.messagesBySession[sessionA.id].items.map((message) => message.id), ['msg-a-1', 'msg-a-2'])
})

test('bootstrap explicit empty message list is authoritative for that session only', () => {
  const state = bootstrappedState()
  applyBootstrapSnapshot(state, snapshotFixture({ messages_by_session: { [sessionA.id]: [] } }))

  assert.deepEqual(state.messagesBySession[sessionA.id].items, [])
})

test('bootstrap tombstones remove ids from every sidebar order without deleting transcript', () => {
  const state = bootstrappedState()
  state.messagesBySession[sessionB.id] = {
    items: [messageB1],
    byMessageId: { [messageB1.id]: 0 },
    byGlobalSeq: { [`${sessionB.id}:1`]: 0 },
  }

  applyBootstrapSnapshot(state, snapshotFixture({
    tombstones_by_session: { [sessionB.id]: tombstoneB },
    session_order: [sessionA.id, sessionB.id],
  }))

  assert.deepEqual(state.sessionOrderByScope['selector-hash:messages,run_intents'], [sessionA.id])
  assert.equal(state.tombstonesBySession[sessionB.id].deleted, true)
  assert.equal(state.messagesBySession[sessionB.id].items[0].id, messageB1.id)
})

test('hydrate is scoped to requested sessions and preserves unrelated messages and order', () => {
  const state = bootstrappedState()
  applyHydrateSnapshot(state, hydrateSnapshotFixture(), [sessionB.id])

  assert.equal(state.syncScopesById['session-b-hash:messages'].endpointCursor, 'cursor-hydrate-b')
  assert.deepEqual(state.messagesBySession[sessionA.id].items.map((message) => message.id), ['msg-a-1', 'msg-a-2'])
  assert.deepEqual(state.messagesBySession[sessionB.id].items.map((message) => message.id), ['msg-b-1'])
  assert.deepEqual(state.sessionOrderByScope['selector-hash:messages,run_intents'], [sessionA.id, sessionB.id])
})

test('hydrate rejects non-requested payload membership', () => {
  const state = bootstrappedState()
  assert.throws(
    () => applyHydrateSnapshot(state, hydrateSnapshotFixture({ sessions_by_id: { [sessionA.id]: sessionA } }), [sessionB.id]),
    /non-requested session session-a/,
  )
})

test('sync stream applies events in order and updates cursor even for empty event batches', () => {
  const state = bootstrappedState()
  const stream = syncStreamFixture()
  const action = syncStreamResponseToAction(stream, 'selector-hash:messages,run_intents')
  applySyncStreamBatch(state, action)

  assert.equal(state.messagesBySession[sessionB.id].items[0].id, messageB1.id)
  assert.equal(state.syncScopesById['selector-hash:messages,run_intents'].endpointCursor, 'cursor-stream-2')

  applySyncStreamBatch(state, {
    ...action,
    endpointCursor: 'cursor-stream-3',
    events: [],
  })
  assert.equal(state.messagesBySession[sessionB.id].items[0].id, messageB1.id)
  assert.equal(state.syncScopesById['selector-hash:messages,run_intents'].endpointCursor, 'cursor-stream-3')
})

test('reconnect feeds same cache and stores resume frame exactly', () => {
  const state = createEmptyDesktopV3CacheState()
  const raw = reconnectFixture()
  const actions = reconnectResponseToActions(raw)
  for (const action of actions) desktopV3CacheReducer(state, action)

  assert.equal(state.realtime.endpointCursor, 'cursor-reconnect')
  assert.equal(state.realtime.streamPath, '/v3/realtime/stream')
  assert.equal(state.realtime.resumeFrame, raw.realtime?.resume)
  assert.deepEqual(state.sessionOrderByScope['workset-1'], [sessionA.id])
  assert.equal(state.subscriptionsById['sub-a'].status, 'active')
})

test('realtime control frames update cursors/status without mutating transcripts', () => {
  const state = bootstrappedState()
  applyRealtimeFrame(state, { frame: realtimeFrameFixture({ kind: 'hello', endpoint_cursor: 'cursor-hello', event: undefined }) })

  assert.equal(state.realtime.status, 'open')
  assert.equal(state.realtime.lastHelloCursor, 'cursor-hello')
  assert.equal(state.messagesBySession[sessionA.id].items.length, 2)

  applyRealtimeFrame(state, { frame: realtimeFrameFixture({ kind: 'cursor.error', bootstrap_required: true, error_code: 'too_old', event: undefined }) })
  assert.equal(state.realtime.needsBootstrap, true)
  assert.equal(state.messagesBySession[sessionA.id].items.length, 2)
})

test('realtime workset discovered creates sidebar stub before event arrives and removed preserves transcript', () => {
  const state = createEmptyDesktopV3CacheState()
  applyRealtimeFrame(state, {
    frame: realtimeFrameFixture({
      kind: 'workset.session.discovered',
      workset_id: 'workset-1',
      session_id: 'session-new',
      endpoint_cursor: 'cursor-discovered',
      event: undefined,
    }),
  })

  assert.deepEqual(state.sessionOrderByScope['workset-1'], ['session-new'])
  assert.equal(state.sessionsById['session-new'].kind, 'stub')

  state.messagesBySession['session-new'] = {
    items: [{ ...messageB1, session_id: 'session-new' }],
    byMessageId: { [messageB1.id]: 0 },
    byGlobalSeq: { 'session-new:1': 0 },
  }
  applyRealtimeFrame(state, {
    frame: realtimeFrameFixture({
      kind: 'workset.session.removed',
      workset_id: 'workset-1',
      session_id: 'session-new',
      endpoint_cursor: 'cursor-removed',
      event: undefined,
    }),
  })
  assert.deepEqual(state.sessionOrderByScope['workset-1'], [])
  assert.equal(state.messagesBySession['session-new'].items.length, 1)
})

test('message mutation reconciles pending by client request and message id', () => {
  const state = createEmptyDesktopV3CacheState()
  upsertPendingUserMessage(state, {
    sessionId: sessionA.id,
    clientRequestId: 'client-1',
    messageId: messageA1.id,
    content: messageA1.content,
    createdAt: 1,
  })

  applyMessageMutationResult(state, messageMutationFixture(), 'client-1', messageA1.id)

  assert.equal(state.pendingUserByClientRequestId['client-1'], undefined)
  assert.equal(state.messagesBySession[sessionA.id].items[0].id, messageA1.id)
  assert.equal(state.runIntentsBySession[sessionA.id]['run-a'].status, 'running')
})

test('wire adapters normalize frame/event boundaries', () => {
  const hydrateAction = hydrateResponseToAction(hydrateSnapshotFixture(), [sessionB.id])
  assert.equal(hydrateAction.type, 'hydrate.apply')

  const actions = realtimeFrameToActions(realtimeFrameFixture())
  assert.equal(actions[0].type, 'realtime.applyEvent')
  if (actions[0].type === 'realtime.applyEvent') {
    assert.equal(actions[0].event.payload.message?.id, messageB1.id)
  }
})
