import test from 'node:test'
import assert from 'node:assert/strict'
import {
  bootstrapResponseToAction,
  hydrateResponseToAction,
  reconnectResponseToActions,
  realtimeFrameToActions,
  selectSession,
  syncStreamResponseToAction,
} from './desktop-v3-cache-wire'
import {
  applyBootstrapSnapshot,
  applyHydrateSnapshot,
  applyMessageMutationResult,
  applyRealtimeFrame,
  applySyncStreamBatch,
  buildMessageListCache,
  createEmptyDesktopV3CacheState,
  desktopV3CacheReducer,
  upsertCommittedMessage,
  upsertPendingUserMessage,
} from './desktop-v3-cache-reducer'
import {
  hydrateSnapshotFixture,
  messageA1,
  messageA2,
  messageB1,
  messageMutationFixture,
  projectionA,
  projectionB,
  reconnectFixture,
  realtimeFrameFixture,
  runIntentA,
  sessionA,
  sessionB,
  snapshotFixture,
  syncStreamFixture,
  tombstoneB,
} from './desktop-v3-cache.backend-fixtures'
import { selectDesktopSidebarRows, selectLiveRuns, selectRenderedSessionMessages } from './desktop-v3-cache-selectors'
import { createDesktopV3CacheOwner } from './desktop-v3-cache-owner'
import { buildPersistedDesktopV3MessageTailV1, DESKTOP_V3_CACHE_SCHEMA_VERSION } from './desktop-v3-cache-persisted-types'
import type { DesktopV3CacheState, MessageSnapshot, V3SessionEvent } from './desktop-v3-cache-types'

function bootstrappedState(): DesktopV3CacheState {
  return applyBootstrapSnapshot(createEmptyDesktopV3CacheState(), snapshotFixture())
}

const persistedOwner = createDesktopV3CacheOwner({
  origin: 'https://desktop.example.test',
  accountScopeId: 'acct-a',
  userId: 'user-a',
  surface: 'desktop',
})

function deltaFrame(
  eventType: string,
  payload: Record<string, unknown>,
  seq: number,
  endpointCursor: string,
) {
  return eventFrame(eventType, {
    id: `evt-${eventType}-${seq}`,
    session_id: sessionA.id,
    seq,
    event_type: eventType,
    payload: {
      run_id: 'run-live',
      recorded_at: 1_000 + seq,
      ...payload,
    },
    ts_unix_ms: 2_000 + seq,
  }, endpointCursor)
}

function eventFrame(eventType: string, event: V3SessionEvent, endpointCursor: string) {
  return realtimeFrameFixture({
    kind: 'event',
    session_id: event.session_id,
    event_type: eventType,
    event,
    projection: {
      session_id: event.session_id,
      last_event_seq: event.seq,
      projection_high_watermark_seq: event.seq,
      updated_at: event.ts_unix_ms,
    },
    endpoint_cursor: endpointCursor,
  })
}

test('desktopV3Cache.restore rebuilds cached sidebar, selected transcript indexes, cursors, and stale status', () => {
  let state = bootstrappedState()
  state.messagesBySession[sessionB.id] = buildMessageListCache([messageB1], { source: 'network' })
  state.pendingUserByClientRequestId['pending-1'] = {
    clientRequestId: 'pending-1',
    messageId: 'pending-msg',
    sessionId: sessionB.id,
    role: 'user',
    content: 'pending',
    createdAt: 1,
    status: 'pending',
  }

  const scopeId = 'persisted-scope'
  const selectedMessageTail = buildPersistedDesktopV3MessageTailV1({
    ownerKey: persistedOwner.key,
    sessionId: sessionA.id,
    persistedAt: 2_000,
    messages: [messageA2, messageA1],
    sourceMessageCount: sessionA.message_count,
    sourceLastMessageAt: sessionA.last_message_at,
    sourceProjectionHighWatermarkSeq: projectionA.projection_high_watermark_seq,
    hydratedAt: 2_001,
  })

  state = desktopV3CacheReducer(state, {
    type: 'desktopV3Cache.restore',
    owner: {
      schemaVersion: DESKTOP_V3_CACHE_SCHEMA_VERSION,
      ownerKey: persistedOwner.key,
      owner: persistedOwner,
      persistedAt: 1_000,
      selectedSessionId: sessionA.id,
      sidebarScopeId: scopeId,
      syncScopesById: {
        [scopeId]: {
          scopeId,
          surface: 'desktop',
          streamKind: 'v3.sync.snapshot',
          selectorFilterHash: 'persisted-selector',
          resourceSet: 'messages,run_intents',
          selector: { kind: 'workspace', workspace_path: '/repo' },
          endpointCursor: 'cursor-persisted',
          replayPath: '/v3/sync/stream',
          replayTransport: 'http_post',
          needsBootstrap: false,
        },
      },
      sessionOrderByScope: { [scopeId]: [sessionA.id, sessionB.id] },
      sidebarSessionsById: {
        [sessionA.id]: { session: sessionA, projection: projectionA, runIntents: [runIntentA] },
        [sessionB.id]: { session: sessionB, projection: projectionB },
      },
    },
    selectedMessageTail,
  })

  assert.equal(state.desktopSidebarBootstrap.status, 'cached')
  assert.equal(state.desktopSidebarBootstrap.scopeId, scopeId)
  assert.equal(state.desktopSidebarBootstrap.stale, true)
  assert.equal(state.desktopInitialHydrate.status, 'cached')
  assert.equal(state.desktopInitialHydrate.scopeId, scopeId)
  assert.equal(state.realtime.needsBootstrap, true)
  assert.equal(state.syncScopesById[scopeId].endpointCursor, 'cursor-persisted')
  assert.equal(state.syncScopesById[scopeId].needsBootstrap, true)
  assert.deepEqual(state.sessionOrderByScope[scopeId], [sessionA.id, sessionB.id])
  assert.equal(state.selectedSessionId, sessionA.id)
  assert.equal(state.sessionsById[sessionA.id]?.kind, 'full')
  assert.equal(state.sessionsById[sessionA.id]?.needsHydrate, true)
  assert.equal(state.projectionsBySession[sessionA.id].projection_high_watermark_seq, projectionA.projection_high_watermark_seq)
  assert.equal(state.currentRunIntentBySession[sessionA.id]?.run_id, runIntentA.run_id)
  assert.deepEqual(state.messagesBySession[sessionA.id].items.map((message) => message.id), [messageA1.id, messageA2.id])
  assert.deepEqual(state.messagesBySession[sessionA.id].byMessageId, { [messageA1.id]: 0, [messageA2.id]: 1 })
  assert.deepEqual(state.messagesBySession[sessionA.id].byGlobalSeq, { [`${sessionA.id}:1`]: 0, [`${sessionA.id}:2`]: 1 })
  assert.equal(state.messagesBySession[sessionA.id].source, 'persisted')
  assert.equal(state.messagesBySession[sessionB.id], undefined)
  assert.equal(state.pendingUserByClientRequestId['pending-1']?.content, 'pending')
})

test('bootstrap stores scoped cursor, scope metadata, orders, message source metadata, run intents, and only present message keys', () => {
  const state = bootstrappedState()
  const scopeId = 'selector-hash:messages,run_intents'

  assert.equal(state.syncScopesById[scopeId].endpointCursor, 'cursor-bootstrap-1')
  assert.equal(state.syncScopesById[scopeId].selector.workspace_path, '/repo')
  assert.deepEqual(state.sessionOrderByScope[scopeId], [sessionA.id, sessionB.id])
  assert.equal(state.messagesBySession[sessionA.id].items.length, 2)
  assert.equal(state.messagesBySession[sessionA.id].sourceMessageCount, sessionA.message_count)
  assert.equal(state.messagesBySession[sessionA.id].sourceLastMessageAt, sessionA.last_message_at)
  assert.equal(state.messagesBySession[sessionA.id].sourceProjectionHighWatermarkSeq, projectionA.projection_high_watermark_seq)
  assert.equal(state.messagesBySession[sessionA.id].source, 'network')
  assert.equal(typeof state.messagesBySession[sessionA.id].hydratedAt, 'number')
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

test('message event upsert increments stale source count once and preserves hydration timestamp', () => {
  const state = createEmptyDesktopV3CacheState()
  state.sessionsById[sessionA.id] = {
    kind: 'full',
    session: { ...sessionA, message_count: 1_000, last_message_at: 1_000 },
    needsHydrate: false,
  }
  state.messagesBySession[sessionA.id] = buildMessageListCache([messageA1, messageA2], {
    sourceMessageCount: 1_000,
    sourceLastMessageAt: 1_000,
    sourceProjectionHighWatermarkSeq: 2,
    hydratedAt: 777,
    source: 'network',
  })

  const newMessage: MessageSnapshot = {
    id: 'msg-a-3',
    session_id: sessionA.id,
    global_seq: 3,
    role: 'assistant',
    content: 'new message',
    created_at: 1_001,
  }
  upsertCommittedMessage(state, sessionA.id, newMessage)

  assert.equal(state.messagesBySession[sessionA.id].sourceMessageCount, 1_001)
  assert.equal(state.messagesBySession[sessionA.id].sourceLastMessageAt, 1_001)
  assert.equal(state.messagesBySession[sessionA.id].hydratedAt, 777)

  upsertCommittedMessage(state, sessionA.id, { ...newMessage, content: 'edited' })
  assert.equal(state.messagesBySession[sessionA.id].sourceMessageCount, 1_001)
  assert.equal(state.messagesBySession[sessionA.id].hydratedAt, 777)
})

test('metadata-only bootstrap preserves existing transcripts when messages_by_session is omitted', () => {
  const state = bootstrappedState()
  const response = snapshotFixture({
    snapshot_endpoint_cursor: 'cursor-bootstrap-2',
    session_order: [sessionA.id],
  })
  delete response.messages_by_session

  desktopV3CacheReducer(state, bootstrapResponseToAction(response))

  assert.equal(state.syncScopesById['selector-hash:messages,run_intents'].endpointCursor, 'cursor-bootstrap-2')
  assert.deepEqual(state.messagesBySession[sessionA.id].items.map((message) => message.id), ['msg-a-1', 'msg-a-2'])
})

test('bootstrap explicit empty message list is authoritative for that session only when messages are in scope', () => {
  const state = bootstrappedState()
  applyBootstrapSnapshot(state, snapshotFixture({ messages_by_session: { [sessionA.id]: [] } }))

  assert.deepEqual(state.messagesBySession[sessionA.id].items, [])
})

test('metadata-only bootstrap ignores message and event payloads when resources are out of scope', () => {
  const state = bootstrappedState()
  state.eventsBySession[sessionA.id] = [{
    id: 'evt-a-existing',
    session_id: sessionA.id,
    seq: 1,
    event_type: 'message.stored',
    payload: { message: messageA1 },
    ts_unix_ms: 3,
  }]

  applyBootstrapSnapshot(state, snapshotFixture({
    snapshot_endpoint_cursor: 'cursor-bootstrap-metadata-only',
    messages_by_session: { [sessionA.id]: [] },
    events_by_session: { [sessionA.id]: [] },
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'selector-hash',
      resource_set: 'run_intents',
    },
    scope_id: 'selector-hash:run_intents',
  }))

  assert.deepEqual(state.messagesBySession[sessionA.id].items.map((message) => message.id), ['msg-a-1', 'msg-a-2'])
  assert.deepEqual(state.eventsBySession[sessionA.id].map((event) => event.id), ['evt-a-existing'])
})

test('metadata-only bootstrap ignores run intent payloads when run intents are out of scope', () => {
  const state = bootstrappedState()
  applyBootstrapSnapshot(state, snapshotFixture({
    run_intents_by_session: { [sessionA.id]: [] },
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'selector-hash',
      resource_set: 'messages',
    },
    scope_id: 'selector-hash:messages',
  }))

  assert.equal(state.runIntentsBySession[sessionA.id]['run-a'].status, 'running')
  assert.equal(state.currentRunIntentBySession[sessionA.id]?.run_id, 'run-a')
})

test('in-scope empty bootstrap run intents clear stale run and current intent', () => {
  const state = bootstrappedState()
  applyBootstrapSnapshot(state, snapshotFixture({
    run_intents_by_session: { [sessionA.id]: [] },
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'selector-hash',
      resource_set: 'run_intents',
    },
    scope_id: 'selector-hash:run_intents',
  }))

  assert.equal(state.runIntentsBySession[sessionA.id], undefined)
  assert.equal(state.currentRunIntentBySession[sessionA.id], undefined)
})

test('authoritative empty run intents clear rendered live overlay', () => {
  const state = bootstrappedState()
  assert.deepEqual(selectRenderedSessionMessages(state, sessionA.id).liveRuns.map((run) => run.runId), ['run-a'])

  applyBootstrapSnapshot(state, snapshotFixture({
    run_intents_by_session: { [sessionA.id]: [] },
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'selector-hash',
      resource_set: 'run_intents',
    },
    scope_id: 'selector-hash:run_intents',
  }))

  assert.deepEqual(selectRenderedSessionMessages(state, sessionA.id).liveRuns, [])
})

test('bootstrap tombstone clears stale run state without run-intent key', () => {
  const state = bootstrappedState()
  assert.equal(state.currentRunIntentBySession[sessionA.id]?.run_id, 'run-a')
  assert.deepEqual(Object.keys(state.liveRunsBySession[sessionA.id] ?? {}), ['run-a'])

  applyBootstrapSnapshot(state, snapshotFixture({
    sessions_by_id: {},
    projections_by_session: {},
    messages_by_session: {},
    run_intents_by_session: {},
    tombstones_by_session: {
      [sessionA.id]: { ...tombstoneB, session_id: sessionA.id },
    },
    session_order: [],
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'selector-hash',
      resource_set: 'run_intents',
    },
    scope_id: 'selector-hash:run_intents',
  }))

  assert.equal(state.runIntentsBySession[sessionA.id], undefined)
  assert.equal(state.currentRunIntentBySession[sessionA.id], undefined)
  assert.equal(state.liveRunsBySession[sessionA.id], undefined)
})

test('snapshot optional resources are governed by resource set authority', () => {
  const state = bootstrappedState()
  state.plansBySession[sessionA.id] = { id: 'stale-plan' }
  state.planRevisionsBySession[sessionA.id] = [{ id: 'stale-revision' }]
  applyBootstrapSnapshot(state, snapshotFixture({
    plans_by_session: {},
    plan_revisions_by_session: { [sessionA.id]: [] },
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'selector-hash',
      resource_set: 'active_plan,plan_revisions',
    },
    scope_id: 'selector-hash:active_plan,plan_revisions',
  }))

  assert.equal(state.plansBySession[sessionA.id], undefined)
  assert.deepEqual(state.planRevisionsBySession[sessionA.id], [])

  state.plansBySession[sessionA.id] = { id: 'restored-stale-plan' }
  applyBootstrapSnapshot(state, snapshotFixture({
    plans_by_session: {},
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'selector-hash',
      resource_set: 'messages',
    },
    scope_id: 'selector-hash:messages',
  }))

  assert.deepEqual(state.plansBySession[sessionA.id], { id: 'restored-stale-plan' })
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

test('hydrate explicit empty message list is authoritative when messages are in scope', () => {
  const state = bootstrappedState()
  applyHydrateSnapshot(state, hydrateSnapshotFixture(), [sessionB.id])
  applyHydrateSnapshot(state, hydrateSnapshotFixture({ messages_by_session: { [sessionB.id]: [] } }), [sessionB.id])

  assert.deepEqual(state.messagesBySession[sessionB.id].items, [])
})

test('metadata-only hydrate validates subset but ignores empty message payload when messages are out of scope', () => {
  const state = bootstrappedState()
  applyHydrateSnapshot(state, hydrateSnapshotFixture(), [sessionB.id])
  applyHydrateSnapshot(state, hydrateSnapshotFixture({
    messages_by_session: { [sessionB.id]: [] },
    events_by_session: { [sessionB.id]: [] },
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'session-b-hash',
      resource_set: 'projections',
    },
    scope_id: 'session-b-hash:projections',
  }), [sessionB.id])

  assert.deepEqual(state.messagesBySession[sessionB.id].items.map((message) => message.id), ['msg-b-1'])
})

test('hydrate run intents obey resource set authority', () => {
  const state = bootstrappedState()
  applyHydrateSnapshot(state, hydrateSnapshotFixture({
    sessions_by_id: { [sessionA.id]: sessionA },
    projections_by_session: { [sessionA.id]: projectionA },
    messages_by_session: {},
    run_intents_by_session: { [sessionA.id]: [] },
    session_order: [sessionA.id],
    selector: { kind: 'session_ids', session_ids: [sessionA.id] },
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'session-a-hash',
      resource_set: 'messages',
    },
    scope_id: 'session-a-hash:messages',
  }), [sessionA.id])
  assert.equal(state.runIntentsBySession[sessionA.id]['run-a'].status, 'running')
  assert.equal(state.currentRunIntentBySession[sessionA.id]?.run_id, 'run-a')

  applyHydrateSnapshot(state, hydrateSnapshotFixture({
    sessions_by_id: { [sessionA.id]: sessionA },
    projections_by_session: { [sessionA.id]: projectionA },
    messages_by_session: {},
    run_intents_by_session: { [sessionA.id]: [] },
    session_order: [sessionA.id],
    selector: { kind: 'session_ids', session_ids: [sessionA.id] },
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'session-a-hash',
      resource_set: 'run_intents',
    },
    scope_id: 'session-a-hash:run_intents',
  }), [sessionA.id])
  assert.equal(state.runIntentsBySession[sessionA.id], undefined)
  assert.equal(state.currentRunIntentBySession[sessionA.id], undefined)
})

test('hydrate requested tombstone without run-intent key clears stale run state', () => {
  const state = bootstrappedState()
  assert.equal(state.currentRunIntentBySession[sessionA.id]?.run_id, 'run-a')
  assert.deepEqual(Object.keys(state.liveRunsBySession[sessionA.id] ?? {}), ['run-a'])

  applyHydrateSnapshot(state, hydrateSnapshotFixture({
    sessions_by_id: {},
    projections_by_session: {},
    messages_by_session: {},
    run_intents_by_session: {},
    tombstones_by_session: {
      [sessionA.id]: { ...tombstoneB, session_id: sessionA.id },
    },
    session_order: [],
    selector: { kind: 'session_ids', session_ids: [sessionA.id] },
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'session-a-hash',
      resource_set: 'run_intents',
    },
    scope_id: 'session-a-hash:run_intents',
  }), [sessionA.id])

  assert.equal(state.runIntentsBySession[sessionA.id], undefined)
  assert.equal(state.currentRunIntentBySession[sessionA.id], undefined)
  assert.equal(state.liveRunsBySession[sessionA.id], undefined)
})

test('hydrate optional plan resources clear stale restored plans only when in scope', () => {
  const state = bootstrappedState()
  state.plansBySession[sessionB.id] = { id: 'stale-plan-b' }
  state.planRevisionsBySession[sessionB.id] = [{ id: 'stale-revision-b' }]

  applyHydrateSnapshot(state, hydrateSnapshotFixture({
    plans_by_session: {},
    plan_revisions_by_session: { [sessionB.id]: [] },
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'session-b-hash',
      resource_set: 'active_plan,plan_revisions',
    },
    scope_id: 'session-b-hash:active_plan,plan_revisions',
  }), [sessionB.id])

  assert.equal(state.plansBySession[sessionB.id], undefined)
  assert.deepEqual(state.planRevisionsBySession[sessionB.id], [])
})

test('hydrate rejects non-requested payload membership', () => {
  const state = bootstrappedState()
  assert.throws(
    () => applyHydrateSnapshot(state, hydrateSnapshotFixture({ sessions_by_id: { [sessionA.id]: sessionA } }), [sessionB.id]),
    /non-requested session session-a/,
  )
})

test('hydrate rejects optional resource maps for non-requested sessions', () => {
  const state = bootstrappedState()
  const raw = hydrateSnapshotFixture({
    plans_by_session: {
      [sessionB.id]: { id: 'plan-b' },
      [sessionA.id]: { id: 'plan-a' },
    },
  })

  assert.throws(
    () => desktopV3CacheReducer(state, hydrateResponseToAction(raw, [sessionB.id])),
    /plans_by_session included non-requested session session-a/,
  )
})

test('hydrate rejects projections for non-requested sessions', () => {
  const state = bootstrappedState()
  const raw = hydrateSnapshotFixture({
    sessions_by_id: { [sessionA.id]: sessionA },
    projections_by_session: { [sessionB.id]: projectionB },
    messages_by_session: { [sessionA.id]: [messageA1] },
    events_by_session: {},
    run_intents_by_session: {},
    session_order: [sessionA.id],
    selector: { kind: 'session_ids', session_ids: [sessionA.id] },
  })

  assert.throws(
    () => desktopV3CacheReducer(state, hydrateResponseToAction(raw, [sessionA.id])),
    /hydrate projections_by_session includes non-requested session session-b/,
  )
})

test('hydrate rejects tombstones for non-requested sessions before mutating sidebar', () => {
  const state = bootstrappedState()
  const originalOrder = [...state.sessionOrderByScope['selector-hash:messages,run_intents']]
  const raw = hydrateSnapshotFixture({
    sessions_by_id: { [sessionA.id]: sessionA },
    projections_by_session: { [sessionA.id]: projectionA },
    messages_by_session: { [sessionA.id]: [messageA1] },
    events_by_session: {},
    run_intents_by_session: {},
    session_order: [sessionA.id],
    selector: { kind: 'session_ids', session_ids: [sessionA.id] },
    tombstones_by_session: { [sessionB.id]: tombstoneB },
  })

  assert.throws(
    () => desktopV3CacheReducer(state, hydrateResponseToAction(raw, [sessionA.id])),
    /hydrate tombstones_by_session included non-requested session session-b/,
  )

  assert.deepEqual(state.sessionOrderByScope['selector-hash:messages,run_intents'], originalOrder)
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

test('sync stream rejects unknown scope before mutating cache', () => {
  const state = bootstrappedState()
  const originalMessages = structuredClone(state.messagesBySession)
  const action = syncStreamResponseToAction(syncStreamFixture(), 'missing-scope')

  assert.throws(
    () => desktopV3CacheReducer(state, action),
    /not bootstrapped/,
  )

  assert.deepEqual(state.messagesBySession, originalMessages)
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

test('realtimeFrameToActions event advances the realtime endpoint cursor', () => {
  const state = createEmptyDesktopV3CacheState()
  const frame = deltaFrame('session.assistant.delta', { delta: 'hello' }, 2, 'v3c1.after-event')

  for (const action of realtimeFrameToActions(frame)) {
    desktopV3CacheReducer(state, action)
  }

  assert.equal(state.realtime.endpointCursor, 'v3c1.after-event')
})

test('realtime assistant and message deltas append assistant draft without committing messages', () => {
  const state = bootstrappedState()
  const beforeCommittedCount = state.messagesBySession[sessionA.id].items.length

  applyRealtimeFrame(state, { frame: deltaFrame('session.assistant.delta', { delta: 'hel' }, 3, 'cursor-delta-1') })
  applyRealtimeFrame(state, { frame: deltaFrame('session.message.delta', { text_delta: 'lo' }, 4, 'cursor-delta-2') })

  const liveRun = state.liveRunsBySession[sessionA.id]['run-live']
  assert.equal(liveRun.assistantDraft?.content, 'hello')
  assert.equal(liveRun.lastEventSeqSeen, 4)
  assert.equal(state.messagesBySession[sessionA.id].items.length, beforeCommittedCount)
})

test('realtime session.tool.started creates live tool overlay before output deltas arrive', () => {
  const state = bootstrappedState()

  applyRealtimeFrame(state, {
    frame: deltaFrame('session.tool.started', {
      call_id: 'call-1',
      step_id: 'step-1',
      tool_instance_id: 'tool-instance-1',
      tool_name: 'search',
      arguments: '{"query":"a"}',
      status: 'started',
    }, 5, 'cursor-tool-start'),
  })

  const tool = state.liveRunsBySession[sessionA.id]['run-live'].toolCallsByCallId['call-1']
  assert.equal(tool.stepId, 'step-1')
  assert.equal(tool.toolInstanceId, 'tool-instance-1')
  assert.equal(tool.toolName, 'search')
  assert.equal(tool.argumentsText, '{"query":"a"}')
  assert.equal(tool.outputText, undefined)
  assert.equal(tool.status, 'started')
})

test('realtime session.tool.delta appends output and terminal event replaces final output', () => {
  const state = bootstrappedState()

  applyRealtimeFrame(state, {
    frame: deltaFrame('session.tool.started', {
      call_id: 'call-1',
      step_id: 'step-1',
      tool_instance_id: 'tool-instance-1',
      tool_name: 'search',
      arguments: '{"query":"a"}',
    }, 5, 'cursor-tool-start'),
  })
  applyRealtimeFrame(state, {
    frame: deltaFrame('session.tool.delta', {
      call_id: 'call-1',
      output_delta: 'result-',
    }, 6, 'cursor-tool-delta'),
  })
  applyRealtimeFrame(state, {
    frame: deltaFrame('session.tool.completed', {
      call_id: 'call-1',
      tool_name: 'search',
      output: '{"summary":"done"}',
      duration_ms: 42,
      status: 'completed',
    }, 7, 'cursor-tool-complete'),
  })

  const tool = state.liveRunsBySession[sessionA.id]['run-live'].toolCallsByCallId['call-1']
  assert.equal(tool.argumentsText, '{"query":"a"}')
  assert.equal(tool.outputText, '{"summary":"done"}')
  assert.equal(tool.status, 'completed')
  assert.equal(tool.durationMs, 42)
})

test('committed assistant final message clears matching live assistant draft', () => {
  const state = bootstrappedState()
  applyRealtimeFrame(state, { frame: deltaFrame('session.assistant.delta', { delta: 'draft' }, 3, 'cursor-draft') })

  const finalMessage: MessageSnapshot = {
    ...messageA2,
    id: 'msg-final-live',
    global_seq: 3,
    content: 'draft',
    metadata: { run_id: 'run-live' },
    created_at: 30,
  }
  applyRealtimeFrame(state, {
    frame: eventFrame('message.stored', {
      id: 'evt-final-live',
      session_id: sessionA.id,
      seq: 4,
      event_type: 'message.stored',
      payload: { message: finalMessage },
      ts_unix_ms: 31,
    }, 'cursor-final'),
  })

  assert.equal(state.messagesBySession[sessionA.id].items.some((message) => message.id === 'msg-final-live'), true)
  assert.equal(state.liveRunsBySession[sessionA.id]['run-live'].assistantDraft, undefined)
})

test('live run selector returns stable sequence/update/run id ordering', () => {
  const state = createEmptyDesktopV3CacheState()
  state.liveRunsBySession[sessionA.id] = {
    'run-c': { sessionId: sessionA.id, runId: 'run-c', status: 'running', toolCallsByCallId: {}, lastEventSeqSeen: 2 },
    'run-b': { sessionId: sessionA.id, runId: 'run-b', status: 'running', toolCallsByCallId: {}, lastEventSeqSeen: 1, assistantDraft: { content: 'b', updatedAt: 20 } },
    'run-a': { sessionId: sessionA.id, runId: 'run-a', status: 'running', toolCallsByCallId: {}, lastEventSeqSeen: 1, assistantDraft: { content: 'a', updatedAt: 10 } },
  }

  assert.deepEqual(selectLiveRuns(state, sessionA.id).map((run) => run.runId), ['run-a', 'run-b', 'run-c'])
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
