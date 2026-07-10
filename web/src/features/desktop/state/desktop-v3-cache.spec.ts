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
  applyCacheEvent,
  applyHydrateSnapshot,
  applyMessageMutationResult,
  applyRealtimeFrame,
  applyReconnectSnapshot,
  applySessionCreateMutationResult,
  applySessionSettingsMutationResult,
  applySyncStreamBatch,
  buildMessageListCache,
  DESKTOP_V3_MAX_RETAINED_BACKGROUND_TRANSCRIPTS,
  applyDesktopV3LivePatchBatch,
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
  outboxFixture,
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
import { isDesktopV3SessionTailReady, selectDesktopPlanExecutionView, selectDesktopSidebarRows, selectDesktopV3HydratedTranscriptDiagnostics, selectLiveRuns, selectRenderedSessionMessages } from './desktop-v3-cache-selectors'
import { buildDesktopV3ConversationRenderItems, isDesktopV3ManualCompactionAckMessage } from '../chat/components/desktop-v3-existing-conversation-pane'
import type { CacheEvent, DesktopV3CacheState, MessageSnapshot, SessionCreateMutationResponse, SessionSnapshot, V3SessionEvent, V3SessionProjection } from './desktop-v3-cache-types'
import type { SessionV3RealtimeLivePatchWire } from '../session-v3/types'

const encoder = new TextEncoder()

function bootstrappedState(): DesktopV3CacheState {
  return applyBootstrapSnapshot(createEmptyDesktopV3CacheState(), snapshotFixture())
}

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

function cp8Session(index: number): SessionSnapshot {
  return {
    ...sessionA,
    id: `session-cp8-${index}`,
    title: `CP8 Session ${index}`,
    message_count: 1,
    last_message_at: 1_000 + index,
    updated_at: 1_000 + index,
  }
}

function cp8Message(sessionId: string, index: number): MessageSnapshot {
  return {
    id: `msg-cp8-${index}`,
    session_id: sessionId,
    global_seq: 1,
    role: 'assistant',
    content: `message ${index}`,
    created_at: 1_000 + index,
  }
}

function cp8Projection(sessionId: string, index: number): V3SessionProjection {
  return {
    session_id: sessionId,
    last_event_seq: 1,
    projection_high_watermark_seq: 1,
    updated_at: 1_000 + index,
  }
}

function hydrateCp8Session(state: DesktopV3CacheState, session: SessionSnapshot, index: number): void {
  applyHydrateSnapshot(state, hydrateSnapshotFixture({
    snapshot_endpoint_cursor: `cursor-cp8-${index}`,
    sessions_by_id: { [session.id]: session },
    projections_by_session: { [session.id]: cp8Projection(session.id, index) },
    messages_by_session: { [session.id]: [cp8Message(session.id, index)] },
    history_manifests_by_session: { [session.id]: [{ chunk_id: `chunk-cp8-${index}`, resource: 'messages' }] },
    history_chunks_by_id: { [`chunk-cp8-${index}`]: { chunk_id: `chunk-cp8-${index}`, resource: 'messages', messages: [cp8Message(session.id, index)] } },
    session_order: [session.id],
    selector: { kind: 'session_ids', session_ids: [session.id] },
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: `session-cp8-${index}-hash`,
      resource_set: 'messages,events',
    },
    scope_id: `session-cp8-${index}-hash:messages,events`,
  }), [session.id])
}


test('session_view active plan is normalized with execution data for Desktop selectors', () => {
  const state = createEmptyDesktopV3CacheState()
  applyBootstrapSnapshot(state, snapshotFixture({
    messages_by_session: {},
    run_intents_by_session: {},
    session_views_by_id: {
      [sessionA.id]: {
        agentic_settings: { mode: 'auto', agent_name: 'swarm', resolved_agent_name: 'swarm' },
        has_active_plan: true,
        active_plan: {
          id: 'plan-exec',
          title: 'Execution plan',
          plan: '# Plan',
          document: {
            id: 'plan-exec',
            title: 'Execution plan',
            status: 'approved',
            execution_policy: { mode: 'review_each_checkpoint', shape: 'checkpointed' },
            execution_state: {
              status: 'waiting_review',
              active_attempt_id: 'cp-1:attempt-1',
              current_session_id: 'session-fresh',
              current_run_id: 'run-fresh',
              last_checkpoint_id: 'cp-1',
              last_outcome: 'needs_review',
            },
            active_checkpoint_id: 'cp-1',
            checkpoints: [{
              id: 'cp-1',
              title: 'Expose plan execution data',
              status: 'needs_review',
              attempt_id: 'cp-1:attempt-1',
              run_id: 'run-fresh',
              session_id: 'session-fresh',
              review: { status: 'pending' },
              attempts: [{ id: 'cp-1:attempt-1', checkpoint_id: 'cp-1', status: 'needs_review', run_id: 'run-fresh' }],
            }],
          },
          status: 'approved',
          approval_state: 'approved',
          updated_at: 9,
        },
      },
    },
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'selector-hash',
      resource_set: 'session_view',
    },
    scope_id: 'selector-hash:session_view',
  }))

  const view = selectDesktopPlanExecutionView(state, sessionA.id)
  assert.equal(view?.plan.id, 'plan-exec')
  assert.equal(view?.policyMode, 'review_each_checkpoint')
  assert.equal(view?.policyShape, 'checkpointed')
  assert.equal(view?.status, 'waiting_review')
  assert.equal(view?.activeCheckpointId, 'cp-1')
  assert.equal(view?.activeCheckpoint?.attemptId, 'cp-1:attempt-1')
  assert.equal(view?.currentRunId, 'run-fresh')
  assert.equal(view?.currentSessionId, 'session-fresh')
  assert.equal(view?.freshContext, true)
  assert.equal(view?.reviewRequired, true)
  assert.equal(view?.attemptCount, 1)
})


test('Desktop plan execution view derives terminal status from durable plan state, not local todo inference', () => {
  const state = createEmptyDesktopV3CacheState()
  desktopV3CacheReducer(state, {
    type: 'planSnapshot.apply',
    sessionId: sessionA.id,
    hasActivePlan: true,
    activePlan: {
      id: 'durable-plan',
      title: 'Durable plan',
      plan: '# Durable',
      status: 'approved',
      approvalState: 'approved',
      updatedAt: 42,
      document: {
        id: 'durable-plan',
        title: 'Durable plan',
        status: 'approved',
        schemaVersion: '',
        revisionId: '',
        info: { goal: 'Durable state', scope: '', context: '', decisions: [], constraints: [], assumptions: [], openQuestions: [], relevantFiles: [], successCriteria: [], validationStrategy: '' },
        executionPolicy: { mode: 'review_each_checkpoint', shape: 'checkpointed', followupCheckpointPolicy: '' },
        executionState: {
          status: 'waiting_review',
          activeAttemptId: 'cp-1:attempt-1',
          parentSessionId: sessionA.id,
          currentSessionId: 'session-fresh',
          currentRunId: 'run-fresh',
          lastCheckpointId: 'cp-1',
          lastAttemptId: 'cp-1:attempt-1',
          lastOutcome: 'completed',
          startedAt: 1,
          updatedAt: 2,
          completedAt: 2,
        },
        checkpoints: [{
          id: 'cp-1',
          title: 'Needs user review',
          status: 'completed',
          objective: '',
          tasks: [],
          acceptanceCriteria: [],
          notes: '',
          report: 'done',
          result: '',
          changedFiles: [],
          validation: [],
          attemptId: 'cp-1:attempt-1',
          runId: 'run-fresh',
          sessionId: 'session-fresh',
          startedAt: 1,
          completedAt: 2,
          review: { status: 'pending', reviewerId: '', reviewerType: '', result: '', notes: '', reviewedAt: 0 },
          attempts: [{ id: 'cp-1:attempt-1', checkpointId: 'cp-1', status: 'completed', outcome: 'completed', runId: 'run-fresh', sessionId: 'session-fresh', parentSessionId: sessionA.id, startedAt: 1, completedAt: 2, report: 'done', result: '', changedFiles: [], validation: [] }],
          order: 1,
        }, {
          id: 'cp-2',
          title: 'Later checkpoint',
          status: 'pending',
          objective: '',
          tasks: [],
          acceptanceCriteria: [],
          notes: '',
          report: '',
          result: '',
          changedFiles: [],
          validation: [],
          attemptId: '',
          runId: '',
          sessionId: '',
          startedAt: 0,
          completedAt: 0,
          review: null,
          attempts: [],
          order: 2,
        }],
        activeCheckpointId: 'cp-1',
        renderedText: '',
        displayText: '',
      },
    },
    planRevisions: [],
  })

  const view = selectDesktopPlanExecutionView(state, sessionA.id)
  assert.equal(view?.status, 'waiting_review')
  assert.equal(view?.reviewRequired, true)
  assert.equal(view?.activeCheckpointId, 'cp-1')
  assert.equal(view?.activeCheckpoint?.status, 'completed')
  assert.equal(view?.completed, false)
})


test('plan document alone does not expose Desktop plan execution view without authoritative active-plan metadata', () => {
  const state = createEmptyDesktopV3CacheState()
  state.plansBySession[sessionA.id] = {
    id: 'stale-plan',
    title: 'Stale plan',
    plan: '# Stale',
    status: 'approved',
    approvalState: 'approved',
    updatedAt: 1,
    document: {
      id: 'stale-plan',
      title: 'Stale plan',
      status: 'approved',
      schemaVersion: '',
      revisionId: '',
      info: { goal: 'Stale inferred goal', scope: '', context: '', decisions: [], constraints: [], assumptions: [], openQuestions: [], relevantFiles: [], successCriteria: [], validationStrategy: '' },
      executionPolicy: null,
      executionState: null,
      checkpoints: [],
      activeCheckpointId: '',
      renderedText: '',
      displayText: '',
    },
  }

  assert.equal(selectDesktopPlanExecutionView(state, sessionA.id), null)
})


test('planSnapshot.apply true exposes Desktop plan execution view for live plan transitions', () => {
  const state = createEmptyDesktopV3CacheState()
  desktopV3CacheReducer(state, {
    type: 'planSnapshot.apply',
    sessionId: sessionA.id,
    hasActivePlan: true,
    activePlan: {
      id: 'live-plan',
      title: 'Live plan',
      plan: '# Live',
      status: 'approved',
      approvalState: 'approved',
      updatedAt: 1,
      document: {
        id: 'live-plan',
        title: 'Live plan',
        status: 'approved',
        schemaVersion: '',
        revisionId: '',
        info: { goal: 'Live transition', scope: '', context: '', decisions: [], constraints: [], assumptions: [], openQuestions: [], relevantFiles: [], successCriteria: [], validationStrategy: '' },
        executionPolicy: null,
        executionState: null,
        checkpoints: [{
          id: 'cp-1',
          title: 'Run it',
          status: 'pending',
          objective: '',
          tasks: [],
          acceptanceCriteria: [],
          notes: '',
          report: '',
          result: '',
          changedFiles: [],
          validation: [],
          attemptId: '',
          runId: '',
          sessionId: '',
          startedAt: 0,
          completedAt: 0,
          review: null,
          attempts: [],
          order: 1,
        }],
        activeCheckpointId: 'cp-1',
        renderedText: '',
        displayText: '',
      },
    },
    planRevisions: [],
  })

  const view = selectDesktopPlanExecutionView(state, sessionA.id)
  assert.equal(view?.plan.id, 'live-plan')
  assert.equal(view?.activeCheckpointId, 'cp-1')
})


test('session.plan.saved realtime event hydrates Desktop plan execution view from durable payload', () => {
  const state = createEmptyDesktopV3CacheState()
  applyRealtimeFrame(state, { frame: eventFrame('session.plan.saved', {
    id: 'evt-plan-saved',
    session_id: sessionA.id,
    seq: 10,
    event_type: 'session.plan.saved',
    payload: {
      session_id: sessionA.id,
      has_active_plan: true,
      active_plan: {
        id: 'event-plan',
        title: 'Event plan',
        plan: '# Event',
        status: 'approved',
        approval_state: 'approved',
        updated_at: 10,
        document: {
          id: 'event-plan',
          title: 'Event plan',
          status: 'approved',
          execution_policy: { mode: 'automatic', shape: 'checkpointed' },
          execution_state: { status: 'in_progress', active_attempt_id: 'attempt-1', current_run_id: 'run-1' },
          checkpoints: [{ id: 'cp-1', title: 'Start', status: 'in_progress', attempt_id: 'attempt-1', run_id: 'run-1' }],
          active_checkpoint_id: 'cp-1',
        },
      },
    },
    ts_unix_ms: 10,
  }, 'cursor-plan') })

  const view = selectDesktopPlanExecutionView(state, sessionA.id)
  assert.equal(view?.plan.id, 'event-plan')
  assert.equal(view?.policyMode, 'automatic')
  assert.equal(view?.activeCheckpointId, 'cp-1')
  assert.equal(view?.activeCheckpoint?.status, 'in_progress')
})


test('session_view has_active_plan false clears stale Desktop active plan state', () => {
  const state = createEmptyDesktopV3CacheState()
  state.plansBySession[sessionA.id] = {
    id: 'stale-plan',
    title: 'Stale plan',
    plan: '# Stale',
    status: 'approved',
    approvalState: 'approved',
    updatedAt: 1,
    document: null,
  }

  applyBootstrapSnapshot(state, snapshotFixture({
    messages_by_session: {},
    run_intents_by_session: {},
    session_views_by_id: {
      [sessionA.id]: {
        agentic_settings: { mode: 'auto', agent_name: 'swarm', resolved_agent_name: 'swarm' },
        has_active_plan: false,
      },
    },
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'selector-hash',
      resource_set: 'session_view,active_plan',
    },
    scope_id: 'selector-hash:session_view,active_plan',
  }))

  assert.equal(selectDesktopPlanExecutionView(state, sessionA.id), null)
  assert.equal(state.plansBySession[sessionA.id], null)
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

test('older history page prepends messages without losing tail provenance', () => {
  const state = createEmptyDesktopV3CacheState()
  state.messagesBySession[sessionA.id] = buildMessageListCache([messageA2], {
    knownTail: { newestSeq: 2 },
    sourceMessageCount: 2,
    tailHydratedAt: 777,
    source: 'network',
  })

  desktopV3CacheReducer(state, {
    type: 'messages.prependHistoryResult',
    sessionId: sessionA.id,
    messages: [messageA1],
    sourceMessageCount: sessionA.message_count,
    knownFull: true,
  })

  assert.deepEqual(state.messagesBySession[sessionA.id].items.map((message) => message.id), ['msg-a-1', 'msg-a-2'])
  assert.equal(state.messagesBySession[sessionA.id].oldestLoadedSeq, 1)
  assert.equal(state.messagesBySession[sessionA.id].knownFull, true)
  assert.equal(state.messagesBySession[sessionA.id].tailHydratedAt, 777)
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
  assert.equal(state.messagesBySession[sessionA.id].tailHydratedAt, undefined)
})

test('transcript tail readiness requires hydrate provenance, not only matching message watermarks', () => {
  const state = createEmptyDesktopV3CacheState()
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.messagesBySession[sessionA.id] = buildMessageListCache([messageA1, messageA2], {
    sourceMessageCount: sessionA.message_count,
    sourceLastMessageAt: sessionA.last_message_at,
    source: 'network',
  })

  assert.equal(isDesktopV3SessionTailReady(state, sessionA.id), false)

  state.messagesBySession[sessionA.id] = buildMessageListCache([messageA1, messageA2], {
    sourceMessageCount: sessionA.message_count,
    sourceLastMessageAt: sessionA.last_message_at,
    tailHydratedAt: 777,
    source: 'network',
  })

  assert.equal(isDesktopV3SessionTailReady(state, sessionA.id), true)
})

test('archived tombstones do not block hydrated transcript readiness', () => {
  const state = createEmptyDesktopV3CacheState()
  state.sessionsById[sessionA.id] = { kind: 'full', session: sessionA, needsHydrate: false }
  state.tombstonesBySession[sessionA.id] = {
    session_id: sessionA.id,
    kind: 'archived',
    archived: true,
    deleted: false,
    session: sessionA,
  }
  state.messagesBySession[sessionA.id] = buildMessageListCache([messageA1, messageA2], {
    sourceMessageCount: sessionA.message_count,
    sourceLastMessageAt: sessionA.last_message_at,
    tailHydratedAt: 777,
    source: 'network',
  })

  assert.equal(isDesktopV3SessionTailReady(state, sessionA.id), true)

  state.tombstonesBySession[sessionA.id] = { session_id: sessionA.id, deleted: true }
  assert.equal(isDesktopV3SessionTailReady(state, sessionA.id), false)
})


test('realtime message for unhydrated hidden session remains live-only for readiness', () => {
  const state = createEmptyDesktopV3CacheState()
  state.sessionsById[sessionB.id] = { kind: 'full', session: sessionB, needsHydrate: true }
  applyCacheEvent(state, {
    source: 'realtime',
    sessionId: sessionB.id,
    eventType: 'message.created',
    payload: { message: messageB1 },
    projection: { ...projectionB, projection_high_watermark_seq: messageB1.global_seq },
    sessionEvent: {
      id: 'evt-hidden-live-message',
      session_id: sessionB.id,
      seq: messageB1.global_seq,
      event_type: 'message.created',
      payload: { message: messageB1 },
      ts_unix_ms: messageB1.created_at,
    },
  })

  assert.equal(state.messagesBySession[sessionB.id].items[0].id, messageB1.id)
  assert.equal(state.messagesBySession[sessionB.id].tailHydratedAt, undefined)
  assert.equal(isDesktopV3SessionTailReady(state, sessionB.id), false)
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

test('snapshot ignores removed all-session resource maps even if stale server sends them', () => {
  const state = bootstrappedState()
  state.plansBySession[sessionA.id] = { id: 'scoped-plan' }
  state.planRevisionsBySession[sessionA.id] = [{ id: 'scoped-revision' }]
  state.permissionsBySession[sessionA.id] = [{
    id: 'live-permission',
    sessionId: sessionA.id,
    runId: 'run-a',
    callId: 'call-a',
    toolName: 'bash',
    toolArguments: '{}',
    status: 'pending',
    decision: '',
    reason: '',
    requirement: '',
    mode: 'auto',
    createdAt: 10,
    updatedAt: 10,
    resolvedAt: 0,
    permissionRequestedAt: 10,
  }]
  state.usageBySession[sessionA.id] = { tokens: 10 }
  state.preferencesBySession[sessionA.id] = { model: 'scoped-model' }
  state.agentModelPolicyBySession[sessionA.id] = { policy: 'scoped-policy' }

  applyBootstrapSnapshot(state, {
    ...snapshotFixture({
      sync_scope: {
        surface: 'desktop',
        stream_kind: 'v3.sync.snapshot',
        selector_filter_hash: 'selector-hash',
        resource_set: 'active_plan,plan_revisions,permissions,usage,preferences,agent_model_policy',
      },
      scope_id: 'selector-hash:removed-maps',
    }),
    plans_by_session: {},
    plan_revisions_by_session: { [sessionA.id]: [] },
    permissions_by_session: { [sessionA.id]: [{ id: 'stale-permission', session_id: sessionA.id, status: 'pending' }] },
    usage_by_session: { [sessionA.id]: { tokens: 1 } },
    preferences_by_session: { [sessionA.id]: { model: 'stale-model' } },
    agent_model_policy_by_session: { [sessionA.id]: { policy: 'stale-policy' } },
  } as Parameters<typeof applyBootstrapSnapshot>[1])

  assert.deepEqual(state.plansBySession[sessionA.id], { id: 'scoped-plan' })
  assert.deepEqual(state.planRevisionsBySession[sessionA.id], [{ id: 'scoped-revision' }])
  assert.deepEqual(state.permissionsBySession[sessionA.id]?.map((permission) => permission.id), ['live-permission'])
  assert.deepEqual(state.usageBySession[sessionA.id], { tokens: 10 })
  assert.deepEqual(state.preferencesBySession[sessionA.id], { model: 'scoped-model' })
  assert.deepEqual(state.agentModelPolicyBySession[sessionA.id], { policy: 'scoped-policy' })
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

test('CP8 LRU eviction retains at most bounded non-selected hydrated transcripts', () => {
  const state = createEmptyDesktopV3CacheState()
  const selected = cp8Session(0)
  desktopV3CacheReducer(state, selectSession(selected.id))
  hydrateCp8Session(state, selected, 0)

  const backgroundSessions = Array.from(
    { length: DESKTOP_V3_MAX_RETAINED_BACKGROUND_TRANSCRIPTS + 2 },
    (_, index) => cp8Session(index + 1),
  )
  for (const [index, session] of backgroundSessions.entries()) {
    hydrateCp8Session(state, session, index + 1)
  }

  const retainedBackgroundIds = backgroundSessions
    .map((session) => session.id)
    .filter((sessionId) => state.messagesBySession[sessionId])

  assert.equal(state.messagesBySession[selected.id]?.items[0].id, 'msg-cp8-0')
  assert.deepEqual(retainedBackgroundIds, backgroundSessions.slice(-DESKTOP_V3_MAX_RETAINED_BACKGROUND_TRANSCRIPTS).map((session) => session.id))
  assert.equal(state.messagesBySession[backgroundSessions[0].id], undefined)
  assert.equal(state.eventsBySession[backgroundSessions[0].id], undefined)
  assert.equal(state.historyManifestsBySession[backgroundSessions[0].id], undefined)
  assert.equal(state.historyChunksById['chunk-cp8-1'], undefined)
  assert.equal(state.sessionsById[backgroundSessions[0].id]?.needsHydrate, true)

  assert.deepEqual(selectDesktopV3HydratedTranscriptDiagnostics(state), {
    hydratedSessionCount: DESKTOP_V3_MAX_RETAINED_BACKGROUND_TRANSCRIPTS + 1,
    hydratedMessageCount: DESKTOP_V3_MAX_RETAINED_BACKGROUND_TRANSCRIPTS + 1,
    retainedBackgroundHydratedSessionCount: DESKTOP_V3_MAX_RETAINED_BACKGROUND_TRANSCRIPTS,
    inFlightHydrateSessionCount: 0,
    evictedTranscriptCount: 2,
  })
})

test('CP8 eviction protects selected and in-flight hydrated transcripts', () => {
  const state = createEmptyDesktopV3CacheState()
  const selected = cp8Session(0)
  const inFlight = cp8Session(1)
  desktopV3CacheReducer(state, selectSession(selected.id))
  hydrateCp8Session(state, selected, 0)
  hydrateCp8Session(state, inFlight, 1)
  desktopV3CacheReducer(state, {
    type: 'desktopV3Cache.markHydrateInFlight',
    sessionIds: [inFlight.id],
    inFlight: true,
  })

  const backgroundSessions = Array.from(
    { length: DESKTOP_V3_MAX_RETAINED_BACKGROUND_TRANSCRIPTS + 2 },
    (_, index) => cp8Session(index + 2),
  )
  for (const [index, session] of backgroundSessions.entries()) {
    hydrateCp8Session(state, session, index + 2)
  }

  assert.equal(state.messagesBySession[selected.id]?.items[0].id, 'msg-cp8-0')
  assert.equal(state.messagesBySession[inFlight.id]?.items[0].id, 'msg-cp8-1')
  assert.equal(state.messagesBySession[backgroundSessions[0].id], undefined)
  assert.equal(selectDesktopV3HydratedTranscriptDiagnostics(state).retainedBackgroundHydratedSessionCount, DESKTOP_V3_MAX_RETAINED_BACKGROUND_TRANSCRIPTS)
})

test('CP8 stale hydrate response after eviction does not resurrect hidden history', () => {
  const state = createEmptyDesktopV3CacheState()
  const selected = cp8Session(0)
  const stale = cp8Session(1)
  desktopV3CacheReducer(state, selectSession(selected.id))
  hydrateCp8Session(state, selected, 0)
  hydrateCp8Session(state, stale, 1)

  const backgroundSessions = Array.from(
    { length: DESKTOP_V3_MAX_RETAINED_BACKGROUND_TRANSCRIPTS + 1 },
    (_, index) => cp8Session(index + 2),
  )
  for (const [index, session] of backgroundSessions.entries()) {
    hydrateCp8Session(state, session, index + 2)
  }

  assert.equal(state.messagesBySession[stale.id], undefined)
  hydrateCp8Session(state, stale, 99)

  assert.equal(state.messagesBySession[stale.id], undefined)
  assert.equal(state.historyManifestsBySession[stale.id], undefined)
  assert.equal(state.historyChunksById['chunk-cp8-99'], undefined)
  assert.equal(state.sessionsById[stale.id]?.needsHydrate, true)
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
  state.sessionsById[sessionB.id].needsHydrate = true
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
  assert.equal(state.sessionsById[sessionB.id]?.needsHydrate, true)
})

test('hydrate completion accepts authoritative messages and tombstones only', () => {
  const state = bootstrappedState()
  state.sessionsById[sessionB.id].needsHydrate = true
  applyHydrateSnapshot(state, hydrateSnapshotFixture({ messages_by_session: { [sessionB.id]: [] } }), [sessionB.id])
  assert.deepEqual(state.messagesBySession[sessionB.id].items, [])
  assert.equal(state.sessionsById[sessionB.id]?.needsHydrate, false)

  state.sessionsById[sessionB.id].needsHydrate = true
  applyHydrateSnapshot(state, hydrateSnapshotFixture({
    messages_by_session: {},
    tombstones_by_session: { [sessionB.id]: tombstoneB },
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'session-b-hash',
      resource_set: 'run_intents',
    },
    scope_id: 'session-b-hash:run_intents',
  }), [sessionB.id])
  assert.equal(state.sessionsById[sessionB.id]?.needsHydrate, false)
  assert.deepEqual(state.sessionOrderByScope['selector-hash:messages,run_intents'], [sessionA.id])
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

test('older active run intent cannot revive current intent after terminal state', () => {
  const state = bootstrappedState()
  const terminalIntent = {
    ...runIntentA,
    status: 'completed',
    updated_at: 50,
    event_seq: 5,
  }
  const olderActiveIntent = {
    ...runIntentA,
    status: 'running',
    updated_at: 40,
    event_seq: 4,
  }
  const terminalEvent: V3SessionEvent = {
    id: 'evt-terminal-5',
    session_id: sessionA.id,
    seq: 5,
    event_type: 'session.run.completed',
    payload: { run_id: runIntentA.run_id, status: 'completed', run_intent: terminalIntent },
    ts_unix_ms: 5,
  }
  const olderActiveEvent: V3SessionEvent = {
    id: 'evt-older-active-4',
    session_id: sessionA.id,
    seq: 4,
    event_type: 'session.run.running',
    payload: { run_id: runIntentA.run_id, status: 'running', run_intent: olderActiveIntent },
    ts_unix_ms: 4,
  }

  applyCacheEvent(state, {
    source: 'realtime',
    sessionId: sessionA.id,
    eventType: terminalEvent.event_type,
    sessionEvent: terminalEvent,
    projection: { ...projectionA, last_event_seq: 5, projection_high_watermark_seq: 5 },
    payload: terminalEvent.payload as Record<string, unknown>,
  })
  applyCacheEvent(state, {
    source: 'sync-stream',
    sessionId: sessionA.id,
    eventType: olderActiveEvent.event_type,
    sessionEvent: olderActiveEvent,
    projection: { ...projectionA, last_event_seq: 4, projection_high_watermark_seq: 4 },
    payload: olderActiveEvent.payload as Record<string, unknown>,
  })

  assert.equal(state.currentRunIntentBySession[sessionA.id], undefined)
  assert.equal(state.runIntentsBySession[sessionA.id]?.[runIntentA.run_id]?.status, 'completed')
  assert.equal(state.runIntentsBySession[sessionA.id]?.[runIntentA.run_id]?.event_seq, 5)
  assert.equal(state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id]?.status, 'completed')
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

test('stale hydrate promotes a realtime-discovered stub without replacing its newer projection', () => {
  const state = createEmptyDesktopV3CacheState()
  const sessionId = 'session-client-b'
  const canonicalTitle = 'Canonical generated title'
  const newerProjection = {
    ...projectionB,
    session_id: sessionId,
    last_event_seq: 9,
    projection_high_watermark_seq: 9,
    updated_at: 90,
  }

  applyRealtimeFrame(state, {
    frame: realtimeFrameFixture({
      kind: 'workset.session.discovered',
      workset_id: 'workset-client-b',
      session_id: sessionId,
      endpoint_cursor: 'cursor-discovered-client-b',
      event: undefined,
      session: undefined,
      projection: newerProjection,
    }),
  })

  assert.equal(state.sessionsById[sessionId]?.kind, 'stub')
  applyHydrateSnapshot(state, hydrateSnapshotFixture({
    sessions_by_id: { [sessionId]: { ...sessionB, id: sessionId, title: canonicalTitle } },
    projections_by_session: { [sessionId]: { ...projectionB, session_id: sessionId } },
    messages_by_session: { [sessionId]: [] },
    session_order: [sessionId],
    selector: { kind: 'session_ids', session_ids: [sessionId] },
  }), [sessionId])

  assert.equal(state.sessionsById[sessionId]?.kind, 'full')
  assert.equal(state.sessionsById[sessionId]?.kind === 'full' ? state.sessionsById[sessionId].session.title : '', canonicalTitle)
  assert.equal(state.projectionsBySession[sessionId], newerProjection)
})

test('stale hydrate preserves a newer full cached session', () => {
  const state = createEmptyDesktopV3CacheState()
  const newerSession = { ...sessionB, title: 'Newer realtime title', updated_at: 90 }
  state.sessionsById[sessionB.id] = { kind: 'full', session: newerSession, needsHydrate: false }
  state.projectionsBySession[sessionB.id] = {
    ...projectionB,
    last_event_seq: 9,
    projection_high_watermark_seq: 9,
    updated_at: 90,
  }

  applyHydrateSnapshot(state, hydrateSnapshotFixture({
    sessions_by_id: { [sessionB.id]: { ...sessionB, title: 'Older hydrate title' } },
  }), [sessionB.id])

  assert.equal(state.sessionsById[sessionB.id]?.kind === 'full' ? state.sessionsById[sessionB.id].session.title : '', newerSession.title)
})

test('hydrate ignores removed plan maps and preserves scoped plan cache state', () => {
  const state = bootstrappedState()
  state.plansBySession[sessionB.id] = { id: 'scoped-plan-b' }
  state.planRevisionsBySession[sessionB.id] = [{ id: 'scoped-revision-b' }]

  applyHydrateSnapshot(state, {
    ...hydrateSnapshotFixture({
      sync_scope: {
        surface: 'desktop',
        stream_kind: 'v3.sync.snapshot',
        selector_filter_hash: 'session-b-hash',
        resource_set: 'active_plan,plan_revisions',
      },
      scope_id: 'session-b-hash:active_plan,plan_revisions',
    }),
    plans_by_session: {},
    plan_revisions_by_session: { [sessionB.id]: [] },
  } as Parameters<typeof applyHydrateSnapshot>[1], [sessionB.id])

  assert.deepEqual(state.plansBySession[sessionB.id], { id: 'scoped-plan-b' })
  assert.deepEqual(state.planRevisionsBySession[sessionB.id], [{ id: 'scoped-revision-b' }])
})

test('stale hydrate ignores removed resource maps while preserving scoped state and history', () => {
  const state = bootstrappedState()
  state.projectionsBySession[sessionB.id] = {
    ...projectionB,
    last_event_seq: 9,
    projection_high_watermark_seq: 9,
  }
  state.plansBySession[sessionB.id] = { id: 'new-live-plan' }
  state.planRevisionsBySession[sessionB.id] = [{ id: 'new-live-revision' }]
  state.permissionsBySession[sessionB.id] = [{
    id: 'new-permission',
    sessionId: sessionB.id,
    runId: 'run-b',
    callId: 'call-b',
    toolName: 'bash',
    toolArguments: '{}',
    status: 'pending',
    decision: '',
    reason: '',
    requirement: '',
    mode: 'auto',
    createdAt: 10,
    updatedAt: 10,
    resolvedAt: 0,
    permissionRequestedAt: 10,
  }]
  state.usageBySession[sessionB.id] = { tokens: 10 }
  state.preferencesBySession[sessionB.id] = { model: 'new' }
  state.agentModelPolicyBySession[sessionB.id] = { policy: 'new' }

  applyHydrateSnapshot(state, {
    ...hydrateSnapshotFixture({
      projections_by_session: { [sessionB.id]: projectionB },
      sync_scope: {
        surface: 'desktop',
        stream_kind: 'v3.sync.snapshot',
        selector_filter_hash: 'session-b-hash',
        resource_set: 'messages,active_plan,plan_revisions,permissions,usage,preferences,agent_model_policy',
      },
      scope_id: 'session-b-hash:messages,active_plan,plan_revisions,permissions,usage,preferences,agent_model_policy',
    }),
    plans_by_session: {},
    plan_revisions_by_session: { [sessionB.id]: [] },
    permissions_by_session: { [sessionB.id]: [{
      id: 'old-permission',
      session_id: sessionB.id,
      status: 'pending',
    }] },
    usage_by_session: { [sessionB.id]: { tokens: 1 } },
    preferences_by_session: { [sessionB.id]: { model: 'old' } },
    agent_model_policy_by_session: { [sessionB.id]: { policy: 'old' } },
  } as Parameters<typeof applyHydrateSnapshot>[1], [sessionB.id])

  assert.deepEqual(state.plansBySession[sessionB.id], { id: 'new-live-plan' })
  assert.deepEqual(state.planRevisionsBySession[sessionB.id], [{ id: 'new-live-revision' }])
  assert.deepEqual(state.permissionsBySession[sessionB.id]?.map((permission) => permission.id), ['new-permission'])
  assert.deepEqual(state.usageBySession[sessionB.id], { tokens: 10 })
  assert.deepEqual(state.preferencesBySession[sessionB.id], { model: 'new' })
  assert.deepEqual(state.agentModelPolicyBySession[sessionB.id], { policy: 'new' })
  assert.deepEqual(state.messagesBySession[sessionB.id].items.map((message) => message.id), ['msg-b-1'])
})

test('stale hydrate session view cannot revert a newer cached plan mode or auto model policy', () => {
  const state = bootstrappedState()
  const current = state.sessionsById[sessionB.id]
  assert.equal(current?.kind, 'full')
  if (current?.kind !== 'full') return
  state.sessionsById[sessionB.id] = {
    ...current,
    session: { ...current.session, mode: 'plan' },
  }
  state.projectionsBySession[sessionB.id] = {
    ...projectionB,
    last_event_seq: 9,
    projection_high_watermark_seq: 9,
  }
  state.preferencesBySession[sessionB.id] = { model: 'plan-model' }
  state.agentModelPolicyBySession[sessionB.id] = { model: 'plan-model' }

  applyHydrateSnapshot(state, hydrateSnapshotFixture({
    sessions_by_id: { [sessionB.id]: { ...sessionB, mode: 'auto' } },
    projections_by_session: { [sessionB.id]: projectionB },
    session_views_by_id: {
      [sessionB.id]: {
        agentic_settings: {
          mode: 'auto',
          agent_name: 'swarm',
          resolved_agent_name: 'swarm',
          effective_preference: { model: 'auto-model' },
          agent_model_policy: { model: 'auto-model' },
          projection_seq: projectionB.last_event_seq,
        },
      },
    },
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'session-b-hash',
      resource_set: 'session_view',
    },
    scope_id: 'session-b-hash:session_view',
    selector: { kind: 'session_ids', session_ids: [sessionB.id] },
    session_order: [sessionB.id],
  }), [sessionB.id])

  const retained = state.sessionsById[sessionB.id]
  assert.equal(retained?.kind === 'full' ? retained.session.mode : '', 'plan')
  assert.deepEqual(state.preferencesBySession[sessionB.id], { model: 'plan-model' })
  assert.deepEqual(state.agentModelPolicyBySession[sessionB.id], { model: 'plan-model' })
})

test('stale realtime scalar mode event cannot revert a newer cached plan projection', () => {
  const state = bootstrappedState()
  const current = state.sessionsById[sessionB.id]
  assert.equal(current?.kind, 'full')
  if (current?.kind !== 'full') return
  state.sessionsById[sessionB.id] = {
    ...current,
    session: { ...current.session, mode: 'plan' },
  }
  state.projectionsBySession[sessionB.id] = {
    ...projectionB,
    last_event_seq: 9,
    projection_high_watermark_seq: 9,
  }

  applyCacheEvent(state, {
    source: 'realtime',
    sessionId: sessionB.id,
    eventType: 'session.mode.updated',
    projection: projectionB,
    payload: { mode: 'auto' },
  })

  const retained = state.sessionsById[sessionB.id]
  assert.equal(retained?.kind === 'full' ? retained.session.mode : '', 'plan')
  assert.equal(state.projectionsBySession[sessionB.id].last_event_seq, 9)
})

test('stored events dedupe by immutable session sequence even with different ids', () => {
  const state = createEmptyDesktopV3CacheState()
  const first = {
    id: 'evt-first-id',
    session_id: sessionA.id,
    seq: 10,
    event_type: 'session.assistant.delta',
    payload: { run_id: 'run-live', delta: 'A' },
    ts_unix_ms: 10,
  }
  const duplicateSeq = {
    ...first,
    id: 'evt-second-id',
    payload: { run_id: 'run-live', delta: 'B' },
  }

  applyHydrateSnapshot(state, hydrateSnapshotFixture({
    sessions_by_id: { [sessionA.id]: sessionA },
    projections_by_session: { [sessionA.id]: { ...projectionA, last_event_seq: 10, projection_high_watermark_seq: 10 } },
    messages_by_session: {},
    events_by_session: { [sessionA.id]: [first, duplicateSeq] },
    session_order: [sessionA.id],
    selector: { kind: 'session_ids', session_ids: [sessionA.id] },
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'session-a-hash',
      resource_set: 'events',
    },
    scope_id: 'session-a-hash:events',
  }), [sessionA.id])

  assert.deepEqual(state.eventsBySession[sessionA.id].map((event) => event.id), ['evt-second-id'])

  applyCacheEvent(state, {
    source: 'realtime',
    sessionId: sessionA.id,
    eventType: first.event_type,
    sessionEvent: { ...first, id: 'evt-third-id' },
    payload: first.payload,
  })

  assert.deepEqual(state.eventsBySession[sessionA.id].map((event) => event.id), ['evt-second-id'])
})


test('permission realtime events update detail records while summary events own sidebar counts', () => {
  const state = bootstrappedState()
  applyCacheEvent(state, {
    source: 'realtime',
    sessionId: sessionA.id,
    eventType: 'permission.requested',
    sessionEvent: {
      id: 'evt-permission-requested',
      session_id: sessionA.id,
      seq: 30,
      event_type: 'permission.requested',
      ts_unix_ms: 30,
      payload: {
        permission: {
          id: 'perm-a',
          session_id: sessionA.id,
          run_id: 'run-live',
          call_id: 'call-a',
          tool_name: 'bash',
          tool_arguments: '{"cmd":"echo hi"}',
          status: 'pending',
          requirement: 'approval',
          mode: 'auto',
          created_at: 30,
          updated_at: 30,
          permission_requested_at: 30,
        },
      },
    },
    payload: {
      permission: {
        id: 'perm-a',
        session_id: sessionA.id,
        run_id: 'run-live',
        call_id: 'call-a',
        tool_name: 'bash',
        tool_arguments: '{"cmd":"echo hi"}',
        status: 'pending',
        requirement: 'approval',
        mode: 'auto',
        created_at: 30,
        updated_at: 30,
        permission_requested_at: 30,
      },
    },
  })

  let row = selectDesktopSidebarRows(state).find((entry) => entry.sessionId === sessionA.id)
  assert.equal(row?.pendingPermissionCount, 0)
  assert.equal(row?.pendingPermissions[0].id, 'perm-a')

  applyCacheEvent(state, {
    source: 'realtime',
    sessionId: sessionA.id,
    eventType: 'permission.summary.updated',
    payload: {
      session_id: sessionA.id,
      pending_approval_count: 1,
      oldest_pending_at: 30,
      newest_pending_at: 30,
      updated_at: 30,
    },
  })

  row = selectDesktopSidebarRows(state).find((entry) => entry.sessionId === sessionA.id)
  assert.equal(row?.pendingPermissionCount, 1)

  applyCacheEvent(state, {
    source: 'realtime',
    sessionId: sessionA.id,
    eventType: 'permission.updated',
    sessionEvent: {
      id: 'evt-permission-updated',
      session_id: sessionA.id,
      seq: 31,
      event_type: 'permission.updated',
      ts_unix_ms: 31,
      payload: {
        permission: {
          id: 'perm-a',
          session_id: sessionA.id,
          run_id: 'run-live',
          call_id: 'call-a',
          tool_name: 'bash',
          tool_arguments: '{"cmd":"echo hi"}',
          status: 'approved',
          decision: 'allow_once',
          requirement: 'approval',
          mode: 'auto',
          created_at: 30,
          updated_at: 31,
          resolved_at: 31,
          permission_requested_at: 30,
        },
      },
    },
    payload: {
      permission: {
        id: 'perm-a',
        session_id: sessionA.id,
        run_id: 'run-live',
        call_id: 'call-a',
        tool_name: 'bash',
        tool_arguments: '{"cmd":"echo hi"}',
        status: 'approved',
        decision: 'allow_once',
        requirement: 'approval',
        mode: 'auto',
        created_at: 30,
        updated_at: 31,
        resolved_at: 31,
        permission_requested_at: 30,
      },
    },
  })

  row = selectDesktopSidebarRows(state).find((entry) => entry.sessionId === sessionA.id)
  assert.equal(row?.pendingPermissionCount, 1)
  assert.equal(state.permissionsBySession[sessionA.id], undefined)

  applyCacheEvent(state, {
    source: 'realtime',
    sessionId: sessionA.id,
    eventType: 'permission.summary.updated',
    payload: {
      session_id: sessionA.id,
      pending_approval_count: 0,
      oldest_pending_at: 0,
      newest_pending_at: 0,
      updated_at: 31,
    },
  })

  row = selectDesktopSidebarRows(state).find((entry) => entry.sessionId === sessionA.id)
  assert.equal(row?.pendingPermissionCount, 0)
})

test('hydrate rejects non-requested payload membership', () => {
  const state = bootstrappedState()
  assert.throws(
    () => applyHydrateSnapshot(state, hydrateSnapshotFixture({ sessions_by_id: { [sessionA.id]: sessionA } }), [sessionB.id]),
    /non-requested session session-a/,
  )
})

test('bootstrap permission summaries are authoritative sidebar badges without full records', () => {
  const state = bootstrappedState()
  state.permissionSummaryBySessionId[sessionB.id] = {
    pendingApprovalCount: 2,
    oldestPendingAt: 20,
    newestPendingAt: 21,
    updatedAt: 21,
  }

  applyBootstrapSnapshot(state, snapshotFixture({
    sessions_by_id: { [sessionA.id]: sessionA },
    projections_by_session: { [sessionA.id]: projectionA },
    session_order: [sessionA.id],
    permission_summaries_by_session: {
      [sessionA.id]: {
        session_id: sessionA.id,
        pending_approval_count: 1,
        oldest_pending_at: 30,
        newest_pending_at: 30,
        updated_at: 30,
      },
    },
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'selector-hash',
      resource_set: 'current_run_state,permission_summaries',
    },
    scope_id: 'selector-hash:current_run_state,permission_summaries',
  }))

  const rowA = selectDesktopSidebarRows(state, 'selector-hash:current_run_state,permission_summaries')
    .find((entry) => entry.sessionId === sessionA.id)
  assert.equal(rowA?.pendingPermissionCount, 1)
  assert.equal(rowA?.pendingPermissions.length, 0)
  assert.equal(state.permissionSummaryBySessionId[sessionB.id], undefined)
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

test('reconnect applies active plan session views for all sidebar sessions', () => {
  const state = createEmptyDesktopV3CacheState()
  applyReconnectSnapshot(state, reconnectFixture({
    sessions_by_id: { [sessionA.id]: sessionA, [sessionB.id]: sessionB },
    projections_by_session: { [sessionA.id]: projectionA, [sessionB.id]: projectionB },
    session_order: [sessionA.id, sessionB.id],
    session_views_by_id: {
      [sessionA.id]: {
        has_active_plan: true,
        active_plan: {
          id: 'reconnect-plan-a',
          title: 'Reconnect plan A',
          plan: '# Reconnect plan A',
          status: 'approved',
          document: {
            id: 'reconnect-plan-a',
            title: 'Reconnect plan A',
            status: 'approved',
            execution_state: { status: 'waiting_review', last_checkpoint_id: 'cp-1', last_outcome: 'needs_review' },
            active_checkpoint_id: 'cp-1',
            checkpoints: [{ id: 'cp-1', title: 'Review A', status: 'needs_review', review: { status: 'pending' }, order: 1 }],
          },
        },
      },
      [sessionB.id]: {
        has_active_plan: true,
        active_plan: {
          id: 'reconnect-plan-b',
          title: 'Reconnect plan B',
          plan: '# Reconnect plan B',
          status: 'approved',
          document: {
            id: 'reconnect-plan-b',
            title: 'Reconnect plan B',
            status: 'approved',
            execution_state: { status: 'in_progress', current_run_id: 'run-b', last_checkpoint_id: 'cp-1' },
            active_checkpoint_id: 'cp-1',
            checkpoints: [{ id: 'cp-1', title: 'Run B', status: 'in_progress', run_id: 'run-b', session_id: sessionB.id, order: 1 }],
          },
        },
      },
    },
    realtime: {
      stream_path: '/v3/realtime/stream',
      resume: {
        protocol: 'v3.realtime',
        protocol_version: 1,
        kind: 'resume',
        endpoint_cursor: 'cursor-reconnect',
        subscriptions: [{ subscription_id: 'sub-a', session_id: sessionA.id }],
        worksets: [{
          workset_id: 'workset-1',
          subscription_id: 'workset-sub-1',
          selector: { kind: 'global', global: true },
          resources: ['current_run_state', 'active_plan'],
          auto_subscribe_sessions: false,
        }],
      },
    },
  }))

  const rows = selectDesktopSidebarRows(state, 'workset-1')
  assert.deepEqual(rows.map((row) => row.sessionId), [sessionA.id, sessionB.id])
  assert.equal(rows[0].planExecution?.statusLabel, 'REVIEW')
  assert.equal(rows[0].sidebarGroup, 'needs_review')
  assert.equal(rows[1].planExecution?.currentRunId, 'run-b')
  assert.equal(rows[1].sidebarGroup, 'in_progress')
})


test('reconnect repopulates Path A and Path B sessions, visible order, and principal subscriptions', () => {
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap = { status: 'ready', scopeId: 'global-scope' }
  const raw = reconnectFixture({
    workset_id: 'global-scope',
    sessions_by_id: {
      [sessionA.id]: sessionA,
      [sessionB.id]: sessionB,
    },
    projections_by_session: {
      [sessionA.id]: projectionA,
      [sessionB.id]: projectionB,
    },
    run_intents_by_session: { [sessionA.id]: [runIntentA] },
    current_run_intent_by_session: { [sessionA.id]: runIntentA },
    session_order: [sessionB.id, sessionA.id],
    subscriptions: [
      { subscription_id: 'sub-b', session_id: sessionB.id, status: 'active', endpoint_cursor: 'cursor-reconnect' },
      { subscription_id: 'sub-a', session_id: sessionA.id, status: 'active', endpoint_cursor: 'cursor-reconnect' },
    ],
    realtime: {
      stream_path: '/v3/realtime/stream',
      resume: {
        protocol: 'v3.realtime',
        protocol_version: 1,
        kind: 'resume',
        endpoint_cursor: 'cursor-reconnect',
        subscriptions: [
          { subscription_id: 'sub-b', session_id: sessionB.id, endpoint_cursor: 'cursor-reconnect' },
          { subscription_id: 'sub-a', session_id: sessionA.id, endpoint_cursor: 'cursor-reconnect' },
        ],
        worksets: [{
          workset_id: 'global-scope',
          subscription_id: 'workset-sub',
          selector: { kind: 'global', global: true },
          resources: ['run_intents'],
          auto_subscribe_sessions: true,
        }],
      },
    },
  })

  desktopV3CacheReducer(state, reconnectResponseToActions(raw)[0])

  assert.equal(state.sessionsById[sessionA.id]?.kind, 'full')
  assert.equal(state.sessionsById[sessionB.id]?.kind, 'full')
  assert.deepEqual(state.sessionOrderByScope['global-scope'], [sessionB.id, sessionA.id])
  assert.deepEqual(
    Object.values(state.subscriptionsById).map((subscription) => subscription.session_id).sort(),
    [sessionA.id, sessionB.id],
  )
  assert.equal(state.worksetsById['global-scope'].sessionIds?.length, 2)
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


test('Desktop V3 durable checkpoint overlapping pending live text renders once', () => {
  let state = bootstrappedState()
  state = applyDesktopV3LivePatchBatch(state, [livePatch({ text: 'hello world', offset_start: 0, offset_end: 11 })])

  applyCacheEvent(state, cacheEvent({
    id: 'evt-durable-overlap',
    session_id: sessionA.id,
    seq: 20,
    event_type: 'session.assistant.delta',
    payload: { run_id: 'run-live', stream_id: 'assistant:run-live:step:1', delta: 'hello world', offset_start: 0, offset_end: 11 },
    ts_unix_ms: 20,
  }))

  assert.equal(state.liveRunsBySession[sessionA.id]['run-live'].assistantDraft?.content, 'hello world')
  assert.equal(state.liveRunsBySession[sessionA.id]['run-live'].assistantDraft?.durableOffsetEnd, 11)
})

test('Desktop V3 durable checkpoint appends only unseen UTF-8 suffix', () => {
  let state = bootstrappedState()
  state = applyDesktopV3LivePatchBatch(state, [livePatch({ text: 'hé', offset_start: 0, offset_end: byteLength('hé') })])
  const durable = 'héllo 🌍'

  applyCacheEvent(state, cacheEvent({
    id: 'evt-durable-utf8-suffix',
    session_id: sessionA.id,
    seq: 21,
    event_type: 'session.assistant.delta',
    payload: { run_id: 'run-live', stream_id: 'assistant:run-live:step:1', delta: durable, offset_start: 0, offset_end: byteLength(durable) },
    ts_unix_ms: 21,
  }))

  const draft = state.liveRunsBySession[sessionA.id]['run-live'].assistantDraft
  assert.equal(draft?.content, durable)
  assert.equal(draft?.content.includes('�'), false)
  assert.equal(draft?.offsetEnd, byteLength(durable))
  assert.equal(draft?.durableOffsetEnd, byteLength(durable))
})

test('Desktop V3 invalid UTF-8 overlap pauses repair', () => {
  let state = bootstrappedState()
  state = applyDesktopV3LivePatchBatch(state, [livePatch({ text: 'hé', offset_start: 0, offset_end: byteLength('hé') })])

  applyCacheEvent(state, cacheEvent({
    id: 'evt-durable-split-codepoint',
    session_id: sessionA.id,
    seq: 22,
    event_type: 'session.assistant.delta',
    payload: { run_id: 'run-live', stream_id: 'assistant:run-live:step:1', delta: 'é!', offset_start: 2, offset_end: 2 + byteLength('é!') },
    ts_unix_ms: 22,
  }))

  const draft = state.liveRunsBySession[sessionA.id]['run-live'].assistantDraft
  assert.equal(draft?.content, 'hé')
  assert.equal(draft?.livePaused, true)
})

test('Desktop V3 same offsets with different bytes pauses repair', () => {
  let state = bootstrappedState()
  state = applyDesktopV3LivePatchBatch(state, [livePatch({ text: 'hello', offset_start: 0, offset_end: 5 })])

  applyCacheEvent(state, cacheEvent({
    id: 'evt-durable-mismatch',
    session_id: sessionA.id,
    seq: 23,
    event_type: 'session.assistant.delta',
    payload: { run_id: 'run-live', stream_id: 'assistant:run-live:step:1', delta: 'hullo', offset_start: 0, offset_end: 5 },
    ts_unix_ms: 23,
  }))

  const draft = state.liveRunsBySession[sessionA.id]['run-live'].assistantDraft
  assert.equal(draft?.content, 'hello')
  assert.equal(draft?.livePaused, true)
})

test('Desktop V3 pre-tool committed message clears only matching stream', () => {
  const state = bootstrappedState()
  state.liveRunsBySession[sessionA.id] = {
    'run-live': {
      sessionId: sessionA.id,
      runId: 'run-live',
      status: 'running',
      toolCallsByCallId: {},
      assistantSegments: [{
        id: 'live-assistant:run-live:assistant:run-live:step:1',
        content: 'step one exact',
        createdAt: 10,
        updatedAt: 11,
        timelineSeq: 1,
        streamId: 'assistant:run-live:step:1',
        streamStep: 1,
        stepId: 'step-1',
        liveSeqEnd: 1,
        offsetEnd: byteLength('step one exact'),
        durableOffsetEnd: byteLength('step one exact'),
      }],
      assistantDraft: {
        content: '  step two exact  ',
        updatedAt: 12,
        timelineSeq: 2,
        streamId: 'assistant:run-live:step:2',
        streamStep: 2,
        stepId: 'step-2',
        liveSeqEnd: 1,
        offsetEnd: byteLength('  step two exact  '),
        durableOffsetEnd: 0,
      },
    },
  }

  upsertCommittedMessage(state, sessionA.id, {
    id: 'msg-step-1',
    session_id: sessionA.id,
    global_seq: 30,
    role: 'assistant',
    content: 'step one exact',
    metadata: { run_id: 'run-live', stream_id: 'assistant:run-live:step:1' },
    created_at: 30,
  }, 'run-live', 'running')

  const run = state.liveRunsBySession[sessionA.id]['run-live']
  assert.equal(run.assistantSegments, undefined)
  assert.equal(run.assistantDraft?.streamId, 'assistant:run-live:step:2')
  assert.equal(run.assistantDraft?.content, '  step two exact  ')
})

test('Desktop V3 assistant segment preserves leading and trailing whitespace', () => {
  let state = bootstrappedState()
  const exact = '  héllo 🌍  '
  state = applyDesktopV3LivePatchBatch(state, [livePatch({ text: exact, offset_start: 0, offset_end: byteLength(exact) })])
  applyRealtimeFrame(state, { frame: deltaFrame('session.tool.started', { call_id: 'call-whitespace' }, 40, 'cursor-tool-whitespace') })

  const run = state.liveRunsBySession[sessionA.id]['run-live']
  assert.equal(run.assistantSegments?.[0]?.content, exact)
  const rendered = buildDesktopV3ConversationRenderItems(selectRenderedSessionMessages(state, sessionA.id))
  const liveAssistant = rendered.find((item) => item.type === 'live-assistant')
  assert.equal(liveAssistant?.type, 'live-assistant')
  if (liveAssistant?.type === 'live-assistant') assert.equal(liveAssistant.content, exact)
})

test('Desktop V3 live assistant does not sort by per-stream live sequence', () => {
  let state = bootstrappedState()
  state = applyDesktopV3LivePatchBatch(state, [livePatch({
    text: 'streaming reply',
    live_seq_start: 1,
    live_seq_end: 1,
    offset_start: 0,
    offset_end: byteLength('streaming reply'),
  })])

  const draft = state.liveRunsBySession[sessionA.id]['run-live'].assistantDraft
  assert.notEqual(draft?.timelineSeq, 1)
  const rendered = buildDesktopV3ConversationRenderItems(selectRenderedSessionMessages(state, sessionA.id))
  const userIndex = rendered.findIndex((item) => item.type === 'message' && item.message.id === messageA1.id)
  const committedAssistantIndex = rendered.findIndex((item) => item.type === 'message' && item.message.id === messageA2.id)
  const liveIndex = rendered.findIndex((item) => item.type === 'live-assistant')
  assert.ok(userIndex >= 0)
  assert.ok(committedAssistantIndex >= 0)
  assert.ok(liveIndex >= 0)
  assert.notEqual(liveIndex, 0)
  assert.ok(liveIndex > userIndex)
  assert.ok(liveIndex > committedAssistantIndex)
})

test('Desktop V3 high-frequency deltas are not retained', () => {
  const state = bootstrappedState()
  for (let i = 0; i < 10_000; i += 1) {
    applyCacheEvent(state, cacheEvent({
      id: `evt-assistant-delta-${i}`,
      session_id: sessionA.id,
      seq: 10_000 + i,
      event_type: 'session.assistant.delta',
      payload: { run_id: 'run-high-assistant', delta: 'a' },
      ts_unix_ms: 10_000 + i,
    }))
    applyCacheEvent(state, cacheEvent({
      id: `evt-reasoning-delta-${i}`,
      session_id: sessionA.id,
      seq: 20_000 + i,
      event_type: 'session.reasoning.delta',
      payload: { run_id: 'run-high-reasoning', reasoning_key: 'summary-1', text_delta: 'r' },
      ts_unix_ms: 20_000 + i,
    }))
    applyCacheEvent(state, cacheEvent({
      id: `evt-tool-delta-${i}`,
      session_id: sessionA.id,
      seq: 30_000 + i,
      event_type: 'session.tool.delta',
      payload: { run_id: 'run-high-tool', call_id: 'call-1', output_delta: 't' },
      ts_unix_ms: 30_000 + i,
    }))
  }
  applyCacheEvent(state, cacheEvent({
    id: 'evt-tool-start-retained',
    session_id: sessionA.id,
    seq: 40_001,
    event_type: 'session.tool.started',
    payload: { run_id: 'run-high-tool', call_id: 'call-1' },
    ts_unix_ms: 40_001,
  }))

  const retained = state.eventsBySession[sessionA.id] ?? []
  assert.equal(retained.filter((event) => ['session.assistant.delta', 'session.message.delta', 'session.reasoning.delta', 'session.tool.delta'].includes(event.event_type)).length, 0)
  assert.equal(retained.some((event) => event.id === 'evt-tool-start-retained'), true)
  const runs = state.liveRunsBySession[sessionA.id]
  assert.equal(runs['run-high-assistant'].assistantDraft?.content.length, 10_000)
  assert.equal(runs['run-high-reasoning'].reasoning?.text.length, 10_000)
  assert.equal(runs['run-high-tool'].toolCallsByCallId['call-1']?.outputText?.length, 10_000)
})

test('realtime reasoning deltas replace coalesced snapshots instead of appending each snapshot', () => {
  const state = bootstrappedState()

  applyRealtimeFrame(state, {
    frame: deltaFrame('session.reasoning.started', {
      reasoning_key: 'summary-1',
    }, 5, 'cursor-reasoning-start'),
  })
  applyRealtimeFrame(state, {
    frame: deltaFrame('session.reasoning.delta', {
      reasoning_key: 'summary-1',
      delta: 'inspect',
    }, 6, 'cursor-reasoning-delta-1'),
  })
  applyRealtimeFrame(state, {
    frame: deltaFrame('session.reasoning.delta', {
      reasoning_key: 'summary-1',
      delta: 'inspect files',
    }, 7, 'cursor-reasoning-delta-2'),
  })

  const reasoning = state.liveRunsBySession[sessionA.id]['run-live'].reasoning
  assert.equal(reasoning?.text, 'inspect files')
  assert.equal(reasoning?.summary, '')
  assert.equal(reasoning?.timelineSeq, 5)
  assert.equal(reasoning?.updatedSeq, 7)
})

test('realtime stream objects retain backend ordering sequence on live overlays', () => {
  const state = bootstrappedState()

  applyRealtimeFrame(state, { frame: deltaFrame('session.reasoning.delta', { reasoning_key: 'summary-1', delta: 'thinking' }, 3, 'cursor-order-reasoning') })
  applyRealtimeFrame(state, { frame: deltaFrame('session.tool.started', { call_id: 'call-1', tool_name: 'search' }, 4, 'cursor-order-tool') })
  applyRealtimeFrame(state, { frame: deltaFrame('session.assistant.delta', { delta: 'answer' }, 5, 'cursor-order-assistant') })

  const liveRun = state.liveRunsBySession[sessionA.id]['run-live']
  assert.equal(liveRun.reasoning?.timelineSeq, 3)
  assert.equal(liveRun.toolCallsByCallId['call-1']?.timelineSeq, 4)
  assert.equal(liveRun.assistantDraft?.timelineSeq, 5)
})

test('realtime provider tool construction events create live tool overlay while arguments stream', () => {
  const state = bootstrappedState()

  applyRealtimeFrame(state, {
    frame: deltaFrame('session.provider_tool_call.started', {
      tool_call_id: 'call-plan',
      step_id: 'step-1',
      tool_name: 'plan_manage',
    }, 5, 'cursor-provider-tool-start'),
  })
  applyRealtimeFrame(state, {
    frame: deltaFrame('session.provider_tool_call.arguments.delta', {
      tool_call_id: 'call-plan',
      arguments_delta: '{"action":"complete',
    }, 6, 'cursor-provider-tool-delta'),
  })
  applyRealtimeFrame(state, {
    frame: deltaFrame('session.provider_tool_call.arguments.snapshot', {
      tool_call_id: 'call-plan',
      tool_name: 'plan_manage',
      arguments_snapshot: '{"action":"complete_checkpoint","checkpoint_id":"cp-1"}',
    }, 7, 'cursor-provider-tool-snapshot'),
  })

  const tool = state.liveRunsBySession[sessionA.id]['run-live'].toolCallsByCallId['call-plan']
  assert.equal(tool.stepId, 'step-1')
  assert.equal(tool.toolInstanceId, 'provider-tool:call-plan')
  assert.equal(tool.toolName, 'plan_manage')
  assert.equal(tool.argumentsText, '{"action":"complete_checkpoint","checkpoint_id":"cp-1"}')
  assert.equal(tool.status, 'running')
  assert.equal(tool.timelineSeq, 5)
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
  assert.equal(tool.timelineSeq, 7)
})

test('realtime terminal tool event keeps completed live tool after later assistant text', () => {
  const state = bootstrappedState()

  applyRealtimeFrame(state, {
    frame: deltaFrame('session.tool.started', {
      call_id: 'call-write',
      step_id: 'step-write',
      tool_instance_id: 'tool-instance-write',
      tool_name: 'write',
      arguments: '{"path":"file.txt"}',
    }, 5, 'cursor-write-start'),
  })
  applyRealtimeFrame(state, {
    frame: deltaFrame('session.assistant.delta', { delta: 'after write start' }, 6, 'cursor-assistant-after-write'),
  })
  applyRealtimeFrame(state, {
    frame: deltaFrame('session.tool.completed', {
      call_id: 'call-write',
      tool_instance_id: 'tool-instance-write',
      tool_name: 'write',
      output: '{"bytes_written":4}',
      status: 'completed',
    }, 7, 'cursor-write-complete'),
  })

  const rendered = buildDesktopV3ConversationRenderItems(selectRenderedSessionMessages(state, sessionA.id))
    .filter((item) => item.type === 'live-assistant' || item.type === 'live-tool')
    .map((item) => item.type === 'live-assistant' ? `assistant:${item.content}` : `tool:${item.tool.callId}`)

  assert.deepEqual(rendered, [
    'assistant:after write start',
    'tool:call-write',
  ])
})

test('realtime task tool delta replaces full task stream snapshots instead of appending', () => {
  const state = bootstrappedState()
  const firstSnapshot = JSON.stringify({
    path_id: 'tool.task.stream.v1',
    tool: 'task',
    status: 'running',
    launches: [
      { child_session_id: 'child-1', status: 'running', subagent: 'explorer' },
    ],
  })
  const secondSnapshot = JSON.stringify({
    path_id: 'tool.task.stream.v1',
    tool: 'task',
    status: 'running',
    launches: [
      { child_session_id: 'child-1', status: 'running', subagent: 'explorer', current_tool: 'search' },
      { child_session_id: 'child-2', status: 'running', subagent: 'parallel' },
    ],
  })

  applyRealtimeFrame(state, {
    frame: deltaFrame('session.tool.started', {
      call_id: 'call-task',
      step_id: 'step-task',
      tool_instance_id: 'tool-instance-task',
      tool_name: 'task',
      arguments: '{"action":"spawn"}',
    }, 5, 'cursor-task-start'),
  })
  applyRealtimeFrame(state, {
    frame: deltaFrame('session.tool.delta', {
      call_id: 'call-task',
      tool_name: 'task',
      output: firstSnapshot,
    }, 6, 'cursor-task-delta-1'),
  })
  applyRealtimeFrame(state, {
    frame: deltaFrame('session.tool.delta', {
      call_id: 'call-task',
      tool_name: 'task',
      output: secondSnapshot,
    }, 7, 'cursor-task-delta-2'),
  })

  const tool = state.liveRunsBySession[sessionA.id]['run-live'].toolCallsByCallId['call-task']
  assert.equal(tool.outputText, secondSnapshot)
  assert.notEqual(tool.outputText, `${firstSnapshot}${secondSnapshot}`)
})

test('realtime task stream v2 deltas merge launch patches into keyed state without appending output JSON', () => {
  const state = bootstrappedState()
  const firstPatch = JSON.stringify({
    path_id: 'tool.task.stream.v2',
    stream_version: 2,
    tool: 'task',
    status: 'running',
    launch_count: 2,
    task_call_id: 'call-task',
    launch_key: 'child-1',
    launch_index: 1,
    launch: {
      launch_key: 'child-1',
      launch_index: 1,
      child_session_id: 'child-1',
      status: 'running',
      subagent: 'explorer',
    },
  })
  const secondPatch = JSON.stringify({
    path_id: 'tool.task.stream.v2',
    stream_version: 2,
    tool: 'task',
    status: 'running',
    launch_count: 2,
    task_call_id: 'call-task',
    launch_key: 'child-1',
    launch_index: 1,
    launch: {
      launch_key: 'child-1',
      launch_index: 1,
      child_session_id: 'child-1',
      status: 'running',
      subagent: 'explorer',
      current_tool: 'search',
    },
  })
  const thirdPatch = JSON.stringify({
    path_id: 'tool.task.stream.v2',
    stream_version: 2,
    tool: 'task',
    status: 'running',
    launch_count: 2,
    task_call_id: 'call-task',
    launch_key: 'child-2',
    launch_index: 2,
    launch: {
      launch_key: 'child-2',
      launch_index: 2,
      child_session_id: 'child-2',
      status: 'running',
      subagent: 'parallel',
    },
  })

  applyRealtimeFrame(state, {
    frame: deltaFrame('session.tool.started', {
      call_id: 'call-task',
      step_id: 'step-task',
      tool_instance_id: 'tool-instance-task',
      tool_name: 'task',
      arguments: '{"action":"spawn"}',
    }, 5, 'cursor-task-start'),
  })
  for (const [index, output] of [firstPatch, secondPatch, thirdPatch].entries()) {
    applyRealtimeFrame(state, {
      frame: deltaFrame('session.tool.delta', {
        call_id: 'call-task',
        tool_name: 'task',
        output,
      }, 6 + index, `cursor-task-v2-delta-${index}`),
    })
  }

  const tool = state.liveRunsBySession[sessionA.id]['run-live'].toolCallsByCallId['call-task']
  assert.equal(tool.outputText, undefined)
  assert.deepEqual(tool.taskStream?.launchOrder, ['child-1', 'child-2'])
  assert.equal(tool.taskStream?.launchesByKey['child-1']?.current_tool, 'search')
  assert.equal(tool.taskStream?.launchesByKey['child-2']?.subagent, 'parallel')
})

test('restored assistant delta with old seq is ignored', () => {
  const state = bootstrappedState()
  state.liveRunsBySession[sessionA.id] = {
    'run-live': {
      sessionId: sessionA.id,
      runId: 'run-live',
      status: 'running',
      toolCallsByCallId: {},
      lastEventSeqSeen: 7,
      assistantDraft: { content: 'persisted', updatedAt: 7, timelineSeq: 4 },
    },
  }

  applyCacheEvent(state, cacheEvent({
    id: 'evt-replayed-assistant-6',
    session_id: sessionA.id,
    seq: 6,
    event_type: 'session.assistant.delta',
    payload: { run_id: 'run-live', delta: '-duplicate' },
    ts_unix_ms: 6,
  }))

  const liveRun = state.liveRunsBySession[sessionA.id]['run-live']
  assert.equal(liveRun.assistantDraft?.content, 'persisted')
  assert.equal(liveRun.lastEventSeqSeen, 7)
})

test('restored tool delta with old seq is ignored', () => {
  const state = bootstrappedState()
  state.liveRunsBySession[sessionA.id] = {
    'run-live': {
      sessionId: sessionA.id,
      runId: 'run-live',
      status: 'running',
      toolCallsByCallId: {
        'call-1': {
          callId: 'call-1',
          toolName: 'read',
          outputText: 'persisted-output',
          status: 'running',
          updatedAt: 7,
          timelineSeq: 5,
        },
      },
      lastEventSeqSeen: 7,
    },
  }

  applyCacheEvent(state, cacheEvent({
    id: 'evt-replayed-tool-6',
    session_id: sessionA.id,
    seq: 6,
    event_type: 'session.tool.delta',
    payload: { run_id: 'run-live', call_id: 'call-1', output_delta: '-duplicate' },
    ts_unix_ms: 6,
  }))

  const tool = state.liveRunsBySession[sessionA.id]['run-live'].toolCallsByCallId['call-1']
  assert.equal(tool.outputText, 'persisted-output')
  assert.equal(state.liveRunsBySession[sessionA.id]['run-live'].lastEventSeqSeen, 7)
})

test('restored reasoning delta with old seq is ignored', () => {
  const state = bootstrappedState()
  state.liveRunsBySession[sessionA.id] = {
    'run-live': {
      sessionId: sessionA.id,
      runId: 'run-live',
      status: 'running',
      toolCallsByCallId: {},
      reasoning: {
        key: 'summary-1',
        reasoningKey: 'summary-1',
        state: 'running',
        summary: '',
        text: 'persisted-reasoning',
        startedAt: 5,
        completedAt: null,
        updatedAt: 7,
        timelineSeq: 5,
        updatedSeq: 7,
      },
      reasoningByKey: {
        'summary-1': {
          key: 'summary-1',
          reasoningKey: 'summary-1',
          state: 'running',
          summary: '',
          text: 'persisted-reasoning',
          startedAt: 5,
          completedAt: null,
          updatedAt: 7,
          timelineSeq: 5,
          updatedSeq: 7,
        },
      },
      lastEventSeqSeen: 7,
    },
  }

  applyCacheEvent(state, cacheEvent({
    id: 'evt-replayed-reasoning-6',
    session_id: sessionA.id,
    seq: 6,
    event_type: 'session.reasoning.delta',
    payload: { run_id: 'run-live', reasoning_key: 'summary-1', text_delta: '-duplicate' },
    ts_unix_ms: 6,
  }))

  const liveRun = state.liveRunsBySession[sessionA.id]['run-live']
  assert.equal(liveRun.reasoning?.text, 'persisted-reasoning')
  assert.equal(liveRun.reasoningByKey?.['summary-1']?.text, 'persisted-reasoning')
  assert.equal(liveRun.lastEventSeqSeen, 7)
})

test('new seq appends exactly once after restore', () => {
  const state = bootstrappedState()
  state.liveRunsBySession[sessionA.id] = {
    'run-live': {
      sessionId: sessionA.id,
      runId: 'run-live',
      status: 'running',
      toolCallsByCallId: {},
      lastEventSeqSeen: 7,
      assistantDraft: { content: 'persisted', updatedAt: 7, timelineSeq: 4 },
    },
  }

  applyCacheEvent(state, cacheEvent({
    id: 'evt-new-assistant-8',
    session_id: sessionA.id,
    seq: 8,
    event_type: 'session.assistant.delta',
    payload: { run_id: 'run-live', delta: '-new' },
    ts_unix_ms: 8,
  }))
  applyCacheEvent(state, cacheEvent({
    id: 'evt-replayed-new-assistant-8',
    session_id: sessionA.id,
    seq: 8,
    event_type: 'session.assistant.delta',
    payload: { run_id: 'run-live', delta: '-new' },
    ts_unix_ms: 8,
  }))

  const liveRun = state.liveRunsBySession[sessionA.id]['run-live']
  assert.equal(liveRun.assistantDraft?.content, 'persisted-new')
  assert.equal(liveRun.lastEventSeqSeen, 8)
})

test('repair merge keeps restored overlay and ignores replayed old output events', () => {
  const state = bootstrappedState()
  state.liveRunsBySession[sessionA.id] = {
    'run-live': {
      sessionId: sessionA.id,
      runId: 'run-live',
      status: 'running',
      toolCallsByCallId: {},
      lastEventSeqSeen: 7,
      assistantDraft: { content: 'persisted', updatedAt: 7, timelineSeq: 4 },
    },
  }

  desktopV3CacheReducer(state, {
    type: 'liveRun.mergeRepairEvents',
    sessionId: sessionA.id,
    runId: 'run-live',
    events: [
      cacheEvent({
        id: 'evt-repair-new-8',
        session_id: sessionA.id,
        seq: 8,
        event_type: 'session.assistant.delta',
        payload: { run_id: 'run-live', delta: '-repair' },
        ts_unix_ms: 8,
      }),
      cacheEvent({
        id: 'evt-repair-old-6',
        session_id: sessionA.id,
        seq: 6,
        event_type: 'session.assistant.delta',
        payload: { run_id: 'run-live', delta: '-old' },
        ts_unix_ms: 6,
      }),
    ],
  })

  const liveRun = state.liveRunsBySession[sessionA.id]['run-live']
  assert.equal(liveRun.assistantDraft?.content, 'persisted-repair')
  assert.equal(liveRun.lastEventSeqSeen, 8)
})


function byteLength(value: string): number {
  return encoder.encode(value).byteLength
}

function livePatch(overrides: Partial<SessionV3RealtimeLivePatchWire> = {}): SessionV3RealtimeLivePatchWire {
  const text = overrides.text ?? 'x'
  const offsetStart = overrides.offset_start ?? 0
  return {
    session_id: sessionA.id,
    run_id: 'run-live',
    stream_id: 'assistant:run-live:step:1',
    stream_kind: 'assistant_text',
    operation: 'append',
    step: 1,
    step_id: 'step-1',
    live_seq_start: 1,
    live_seq_end: 1,
    offset_start: offsetStart,
    offset_end: overrides.offset_end ?? offsetStart + byteLength(text),
    text,
    recorded_at: 1,
    ...overrides,
  }
}

function cacheEvent(event: V3SessionEvent): CacheEvent {
  return {
    source: 'realtime',
    sessionId: event.session_id,
    eventType: event.event_type,
    sessionEvent: event,
    projection: {
      session_id: event.session_id,
      last_event_seq: event.seq,
      projection_high_watermark_seq: event.seq,
      updated_at: event.ts_unix_ms,
    },
    payload: event.payload as CacheEvent['payload'],
  }
}

test('assistant completion clears overlay using payload run_id', () => {
  const state = bootstrappedState()
  applyRealtimeFrame(state, { frame: deltaFrame('session.assistant.delta', { delta: 'draft' }, 3, 'cursor-draft') })

  const finalMessage: MessageSnapshot = {
    ...messageA2,
    id: 'msg-final-live-payload-run',
    global_seq: 3,
    content: 'draft',
    metadata: {},
    created_at: 30,
  }
  applyRealtimeFrame(state, {
    frame: eventFrame('session.assistant.completed', {
      id: 'evt-final-live-payload-run',
      session_id: sessionA.id,
      seq: 4,
      event_type: 'session.assistant.completed',
      payload: {
        run_id: 'run-live',
        message: finalMessage,
        run_intent: { ...runIntentA, run_id: 'run-live', status: 'running', event_seq: 4 },
      },
      ts_unix_ms: 31,
    }, 'cursor-final'),
  })

  assert.equal(state.messagesBySession[sessionA.id].items.some((message) => message.id === 'msg-final-live-payload-run'), true)
  assert.equal(state.liveRunsBySession[sessionA.id]['run-live'].assistantDraft, undefined)
  assert.equal(state.liveRunsBySession[sessionA.id]['run-live'].assistantSegments, undefined)
})

test('assistant completion works when message metadata has no run_id', () => {
  const state = bootstrappedState()
  applyRealtimeFrame(state, { frame: deltaFrame('session.assistant.delta', { delta: 'draft' }, 3, 'cursor-draft') })

  const finalMessage: MessageSnapshot = {
    ...messageA2,
    id: 'msg-final-live-run-intent',
    global_seq: 3,
    content: 'draft',
    metadata: {},
    created_at: 30,
  }
  applyRealtimeFrame(state, {
    frame: eventFrame('session.assistant.completed', {
      id: 'evt-final-live-run-intent',
      session_id: sessionA.id,
      seq: 4,
      event_type: 'session.assistant.completed',
      payload: {
        message: finalMessage,
        run_intent: { ...runIntentA, run_id: 'run-live', status: 'running', event_seq: 4 },
      },
      ts_unix_ms: 31,
    }, 'cursor-final'),
  })

  assert.equal(state.liveRunsBySession[sessionA.id]['run-live'].assistantDraft, undefined)
})

test('terminal assistant completion preserves uncanonicalized reasoning', () => {
  const state = bootstrappedState()
  applyRealtimeFrame(state, { frame: deltaFrame('session.reasoning.delta', { reasoning_key: 'summary-1', delta: 'thinking' }, 2, 'cursor-reasoning') })
  applyRealtimeFrame(state, { frame: deltaFrame('session.assistant.delta', { delta: 'draft' }, 3, 'cursor-draft') })

  const finalMessage: MessageSnapshot = {
    ...messageA2,
    id: 'msg-final-live-terminal',
    global_seq: 3,
    content: 'draft',
    metadata: {},
    created_at: 30,
  }
  applyRealtimeFrame(state, {
    frame: eventFrame('session.assistant.completed', {
      id: 'evt-final-live-terminal',
      session_id: sessionA.id,
      seq: 4,
      event_type: 'session.assistant.completed',
      payload: {
        run_id: 'run-live',
        message: finalMessage,
        run_intent: { ...runIntentA, run_id: 'run-live', status: 'completed', event_seq: 4 },
      },
      ts_unix_ms: 31,
    }, 'cursor-final'),
  })

  const liveRun = state.liveRunsBySession[sessionA.id]?.['run-live']
  assert.ok(liveRun)
  assert.equal(liveRun.assistantDraft, undefined)
  assert.equal(liveRun.assistantSegments, undefined)
  assert.equal(liveRun.reasoning?.text, 'thinking')
  assert.equal(liveRun.reasoningByKey?.['step:summary-1']?.text, 'thinking')
})

function renderSignature(state: DesktopV3CacheState): string[] {
  return buildDesktopV3ConversationRenderItems(selectRenderedSessionMessages(state, sessionA.id))
    .map((item) => {
      if (item.type === 'message') return `message:${item.message.role}:${item.message.id}:${item.message.content}`
      if (item.type === 'live-tool') return `live-tool:${item.id}:${item.tool.callId}:${item.tool.outputText ?? ''}`
      if (item.type === 'live-reasoning') return `live-reasoning:${item.id}:${item.text}:${item.summary}:${item.state}`
      if (item.type === 'live-assistant') return `live-assistant:${item.id}:${item.content}`
      return item.type
    })
}



function applyCommittedToolEvent(state: DesktopV3CacheState, seq = 6): void {
  const toolMessage: MessageSnapshot = {
    id: `msg-tool-${seq}`,
    session_id: sessionA.id,
    global_seq: seq,
    role: 'tool',
    content: JSON.stringify({
      path_id: 'run.tool-history.v2',
      run_id: 'run-live',
      call_id: 'call-1',
      tool_instance_id: 'tool-instance-1',
      tool: 'search',
      output: '{"summary":"done"}',
    }),
    metadata: {
      run_id: 'run-live',
      call_id: 'call-1',
      tool_instance_id: 'tool-instance-1',
    },
    created_at: 30 + seq,
  }
  applyRealtimeFrame(state, {
    frame: eventFrame('session.tool.completed', {
      id: `evt-tool-commit-${seq}`,
      session_id: sessionA.id,
      seq,
      event_type: 'session.tool.completed',
      payload: {
        run_id: 'run-live',
        call_id: 'call-1',
        tool_instance_id: 'tool-instance-1',
        tool_name: 'search',
        output: '{"summary":"done"}',
        status: 'completed',
        message: toolMessage,
        run_intent: { ...runIntentA, run_id: 'run-live', status: 'completed', event_seq: seq },
      },
      ts_unix_ms: 40 + seq,
    }, `cursor-tool-commit-${seq}`),
  })
}

function applyCommittedReasoningEvent(state: DesktopV3CacheState, seq = 8): void {
  applyRealtimeFrame(state, {
    frame: eventFrame('session.reasoning.completed', {
      id: `evt-reasoning-complete-${seq}`,
      session_id: sessionA.id,
      seq,
      event_type: 'session.reasoning.completed',
      payload: {
        run_id: 'run-live',
        reasoning_key: 'summary-1',
        text: 'thinking done',
        run_intent: { ...runIntentA, run_id: 'run-live', status: 'completed', event_seq: seq },
      },
      ts_unix_ms: 50 + seq,
    }, `cursor-reasoning-complete-${seq}`),
  })
}

test('manual context compact renders checkpoint and suppresses duplicate assistant ack', () => {
  const state = bootstrappedState()
  const checkpoint: MessageSnapshot = {
    id: 'msg-compact-checkpoint',
    session_id: sessionA.id,
    global_seq: 3,
    role: 'system',
    content: '[context-compact] index=2 origin=manual\n\nThis checkpoint supersedes earlier transcript context for future model turns.\n\nCompacted recap:\nsummary',
    created_at: 30,
  }
  const ack: MessageSnapshot = {
    id: 'msg-compact-ack',
    session_id: sessionA.id,
    global_seq: 4,
    role: 'assistant',
    content: 'Manual context compact complete (Compact #2).\n\nCompacted recap:\nsummary',
    metadata: { source: 'manual_context_compaction_ack' },
    created_at: 31,
  }
  state.messagesBySession[sessionA.id].items.push(checkpoint, ack)

  assert.equal(isDesktopV3ManualCompactionAckMessage(ack), true)
  const rendered = buildDesktopV3ConversationRenderItems(selectRenderedSessionMessages(state, sessionA.id))
  assert.equal(rendered.some((item) => item.type === 'message' && item.message.id === checkpoint.id), true)
  assert.equal(rendered.some((item) => item.type === 'message' && item.message.id === ack.id), false)
})


test('committed tool message removes matching live tool and renders once', () => {
  const state = bootstrappedState()
  applyRealtimeFrame(state, { frame: deltaFrame('session.tool.started', { call_id: 'call-1', tool_instance_id: 'tool-instance-1', tool_name: 'search' }, 5, 'cursor-tool-start') })

  applyCommittedToolEvent(state, 6)

  assert.equal(state.liveRunsBySession[sessionA.id]?.['run-live']?.toolCallsByCallId['call-1'], undefined)
  assert.equal(state.messagesBySession[sessionA.id].items.filter((message) => message.role === 'tool').length, 1)
  const rendered = buildDesktopV3ConversationRenderItems(selectRenderedSessionMessages(state, sessionA.id))
  assert.equal(rendered.filter((item) => item.type === 'live-tool').length, 0)
  assert.equal(rendered.filter((item) => item.type === 'message' && item.message.role === 'tool').length, 1)
})

test('reasoning completion commits one reasoning message and renders once', () => {
  const state = bootstrappedState()
  applyRealtimeFrame(state, { frame: deltaFrame('session.reasoning.delta', { reasoning_key: 'summary-1', delta: 'thinking done' }, 7, 'cursor-reasoning-delta') })

  applyCommittedReasoningEvent(state, 8)
  applyCommittedReasoningEvent(state, 8)

  assert.equal(state.messagesBySession[sessionA.id].items.filter((message) => message.role === 'reasoning').length, 1)
  assert.equal(state.liveRunsBySession[sessionA.id]?.['run-live']?.reasoning, undefined)
  assert.equal(state.liveRunsBySession[sessionA.id]?.['run-live']?.reasoningByKey, undefined)
  const rendered = buildDesktopV3ConversationRenderItems(selectRenderedSessionMessages(state, sessionA.id))
  assert.equal(rendered.filter((item) => item.type === 'live-reasoning').length, 0)
  assert.equal(rendered.filter((item) => item.type === 'message' && item.message.role === 'reasoning').length, 1)
})

test('hydrate replays durable reasoning events and commits thinking message after refresh', () => {
  const state = bootstrappedState()
  const toolMessage: MessageSnapshot = {
    id: 'msg-refresh-tool',
    session_id: sessionA.id,
    global_seq: 6,
    role: 'tool',
    content: JSON.stringify({
      path_id: 'run.tool-history.v2',
      run_id: 'run-live',
      call_id: 'call-refresh',
      tool_instance_id: 'tool-refresh',
      tool: 'search',
      output: '{"summary":"done"}',
    }),
    metadata: {
      run_id: 'run-live',
      call_id: 'call-refresh',
      tool_instance_id: 'tool-refresh',
    },
    created_at: 65,
  }
  const assistantAfterReasoning: MessageSnapshot = {
    ...messageA2,
    id: 'msg-refresh-assistant-after-reasoning',
    global_seq: 8,
    content: 'answer after thinking',
    created_at: 80,
  }
  const reasoningEvents: V3SessionEvent[] = [
    {
      id: 'evt-refresh-reasoning-start',
      session_id: sessionA.id,
      seq: 5,
      event_type: 'session.reasoning.started',
      payload: {
        run_id: 'run-live',
        step: 1,
        step_id: 'step-1',
        reasoning_id: 'reasoning-1',
        reasoning_key: 'summary-1',
        recorded_at: 50,
      },
      ts_unix_ms: 50,
    },
    {
      id: 'evt-refresh-reasoning-delta',
      session_id: sessionA.id,
      seq: 6,
      event_type: 'session.reasoning.delta',
      payload: {
        run_id: 'run-live',
        step: 1,
        step_id: 'step-1',
        reasoning_id: 'reasoning-1',
        reasoning_key: 'summary-1',
        delta: 'thinking after refresh',
        recorded_at: 60,
      },
      ts_unix_ms: 60,
    },
    {
      id: 'evt-refresh-reasoning-complete',
      session_id: sessionA.id,
      seq: 7,
      event_type: 'session.reasoning.completed',
      payload: {
        run_id: 'run-live',
        step: 1,
        step_id: 'step-1',
        reasoning_id: 'reasoning-1',
        reasoning_key: 'summary-1',
        summary: 'thinking after refresh',
        recorded_at: 70,
      },
      ts_unix_ms: 70,
    },
  ]

  applyHydrateSnapshot(state, hydrateSnapshotFixture({
    snapshot_endpoint_cursor: 'cursor-refresh-reasoning',
    sessions_by_id: { [sessionA.id]: { ...sessionA, message_count: 4, last_message_at: 80 } },
    projections_by_session: { [sessionA.id]: { ...projectionA, last_event_seq: 7, projection_high_watermark_seq: 7, updated_at: 70 } },
    messages_by_session: { [sessionA.id]: [messageA1, messageA2, toolMessage, assistantAfterReasoning] },
    events_by_session: { [sessionA.id]: reasoningEvents },
    run_intents_by_session: { [sessionA.id]: [{ ...runIntentA, run_id: 'run-live', status: 'completed', event_seq: 7 }] },
    session_order: [sessionA.id],
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'session-a-reasoning-hash',
      resource_set: 'messages,events,run_intents',
    },
    scope_id: 'session-a-reasoning-hash:messages,events,run_intents',
    selector: { kind: 'session_ids', session_ids: [sessionA.id] },
  }), [sessionA.id])

  const reasoningMessages = state.messagesBySession[sessionA.id].items.filter((message) => message.role === 'reasoning')
  assert.equal(reasoningMessages.length, 1)
  assert.equal(reasoningMessages[0].content, 'thinking after refresh')
  assert.equal(reasoningMessages[0].global_seq, 7)
  assert.equal(state.liveRunsBySession[sessionA.id]?.['run-live'], undefined)
  const rendered = buildDesktopV3ConversationRenderItems(selectRenderedSessionMessages(state, sessionA.id))
  assert.deepEqual(
    rendered.filter((item) => item.type === 'message').map((item) => item.type === 'message' ? `${item.message.role}:${item.message.global_seq}` : ''),
    ['user:1', 'assistant:2', 'tool:6', 'reasoning:7', 'assistant:8'],
  )
  assert.equal(rendered.filter((item) => item.type === 'live-reasoning').length, 0)
})


test('tool and thinking committed messages render without live duplicates', () => {
  const state = bootstrappedState()
  applyRealtimeFrame(state, { frame: deltaFrame('session.tool.started', { call_id: 'call-1', tool_instance_id: 'tool-instance-1', tool_name: 'search' }, 5, 'cursor-tool-start') })
  applyCommittedToolEvent(state, 6)
  applyRealtimeFrame(state, { frame: deltaFrame('session.reasoning.delta', { reasoning_key: 'summary-1', delta: 'thinking done' }, 7, 'cursor-reasoning-delta') })
  applyCommittedReasoningEvent(state, 8)

  const rendered = renderSignature(state)
  assert.equal(rendered.filter((entry) => entry.startsWith('live-tool:')).length, 0)
  assert.equal(rendered.filter((entry) => entry.startsWith('live-reasoning:')).length, 0)
  assert.equal(rendered.some((entry) => entry.includes('msg-tool-6')), true)
})

test('tombstone removes all live and run state', () => {
  const state = bootstrappedState()
  applyRealtimeFrame(state, { frame: deltaFrame('session.assistant.delta', { delta: 'draft' }, 3, 'cursor-draft') })

  applyRealtimeFrame(state, {
    frame: eventFrame('session.deleted', {
      id: 'evt-session-a-deleted',
      session_id: sessionA.id,
      seq: 4,
      event_type: 'session.deleted',
      payload: { tombstone: { ...tombstoneB, session_id: sessionA.id } },
      ts_unix_ms: 31,
    }, 'cursor-deleted'),
  })

  assert.equal(state.liveRunsBySession[sessionA.id], undefined)
  assert.equal(state.currentRunIntentBySession[sessionA.id], undefined)
  assert.equal(state.runIntentsBySession[sessionA.id], undefined)
})

test('metadata-only bootstrap after finalization preserves committed final message and no draft', () => {
  const state = bootstrappedState()
  applyRealtimeFrame(state, { frame: deltaFrame('session.assistant.delta', { delta: 'draft' }, 3, 'cursor-draft') })

  const finalMessage: MessageSnapshot = {
    ...messageA2,
    id: 'msg-final-live-refresh',
    global_seq: 3,
    content: 'draft',
    metadata: {},
    created_at: 30,
  }
  applyRealtimeFrame(state, {
    frame: eventFrame('session.assistant.completed', {
      id: 'evt-final-live-refresh',
      session_id: sessionA.id,
      seq: 4,
      event_type: 'session.assistant.completed',
      payload: {
        run_id: 'run-live',
        message: finalMessage,
        run_intent: { ...runIntentA, run_id: 'run-live', status: 'completed', event_seq: 4 },
      },
      ts_unix_ms: 31,
    }, 'cursor-final'),
  })

  applyBootstrapSnapshot(state, snapshotFixture({
    snapshot_endpoint_cursor: 'cursor-after-final-refresh',
    messages_by_session: {},
    session_order: [sessionA.id],
  }))

  assert.equal(state.liveRunsBySession[sessionA.id]?.['run-live'], undefined)
  assert.equal(state.messagesBySession[sessionA.id].items.some((message) => message.id === finalMessage.id), true)
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

test('stale workset discovery cannot replace a newer canonical session mode', () => {
  const state = bootstrappedState()
  const current = state.sessionsById[sessionB.id]
  assert.equal(current?.kind, 'full')
  if (current?.kind !== 'full') return
  state.sessionsById[sessionB.id] = {
    ...current,
    session: { ...current.session, mode: 'plan' },
  }
  state.projectionsBySession[sessionB.id] = {
    ...projectionB,
    last_event_seq: 9,
    projection_high_watermark_seq: 9,
  }

  applyRealtimeFrame(state, {
    frame: realtimeFrameFixture({
      kind: 'workset.session.discovered',
      workset_id: 'workset-1',
      workset_subscription_id: 'workset-sub-1',
      session_id: sessionB.id,
      endpoint_cursor: 'cursor-stale-discovered',
      event: undefined,
      session: { ...sessionB, mode: 'auto' },
      projection: projectionB,
    }),
  })

  const retained = state.sessionsById[sessionB.id]
  assert.equal(retained?.kind === 'full' ? retained.session.mode : '', 'plan')
  assert.equal(state.projectionsBySession[sessionB.id].last_event_seq, 9)
})

test('realtime workset discovered applies child session shell and running state before event arrives', () => {
  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap.scopeId = 'global-scope'
  const childSession: SessionSnapshot = {
    ...sessionB,
    id: 'child-session',
    title: 'Map backend files',
    metadata: {
      parent_session_id: sessionA.id,
      lineage_kind: 'delegated_subagent',
      lineage_label: '@explorer',
      requested_subagent: 'purpose-review',
      subagent: 'explorer',
      assignment_label: 'Map backend files',
    },
  }

  applyRealtimeFrame(state, {
    frame: realtimeFrameFixture({
      kind: 'workset.session.discovered',
      workset_id: 'workset-1',
      workset_subscription_id: 'workset-sub-1',
      session_id: childSession.id,
      subscription_id: 'workset-sub-1:session:child-session',
      auto_subscribed: true,
      endpoint_cursor: 'cursor-discovered',
      event: undefined,
      session: childSession,
      projection: { ...projectionB, session_id: childSession.id, last_event_seq: 1, projection_high_watermark_seq: 1 },
      current_run_state: {
        session_id: childSession.id,
        run_id: 'child-run-active',
        active: true,
        status: 'running',
        created_at: 30,
        updated_at: 31,
        event_seq: 1,
      },
    }),
  })

  assert.deepEqual(state.sessionOrderByScope['workset-1'], [childSession.id])
  assert.deepEqual(state.sessionOrderByScope['global-scope'], [childSession.id])
  assert.equal(state.sessionsById[childSession.id]?.kind, 'full')
  assert.deepEqual(state.sessionsById[childSession.id]?.kind === 'full' ? state.sessionsById[childSession.id].session.metadata : undefined, childSession.metadata)
  assert.equal(state.currentRunIntentBySession[childSession.id]?.run_id, 'child-run-active')
  assert.equal(state.subscriptionsById['workset-sub-1:session:child-session']?.autoSubscribed, true)
  const sidebarRow = selectDesktopSidebarRows(state).find((row) => row.sessionId === childSession.id)
  assert.equal(sidebarRow?.currentRunIntent?.run_id, 'child-run-active')
  assert.equal(sidebarRow?.record.kind, 'full')
  assert.equal(state.realtime.endpointCursor, undefined)
})

test('current_run_state preserves backend timing fields in current and latest run models', () => {
  const state = createEmptyDesktopV3CacheState()
  applyBootstrapSnapshot(state, snapshotFixture({
    sessions_by_id: { [sessionA.id]: sessionA },
    projections_by_session: { [sessionA.id]: projectionA },
    run_intents_by_session: {},
    current_run_state_by_session: {
      [sessionA.id]: {
        session_id: sessionA.id,
        run_id: 'run-second',
        active: true,
        status: 'running',
        created_at: 1_000,
        started_at: 120_000,
        completed_at: 0,
        duration_ms: 0,
        cumulative_duration_ms: 90_000,
        updated_at: 120_000,
        event_seq: 12,
      },
    },
    session_order: [sessionA.id],
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'selector-hash',
      resource_set: 'current_run_state',
    },
    scope_id: 'selector-hash:current_run_state',
  }))

  const rendered = selectRenderedSessionMessages(state, sessionA.id)
  assert.equal(rendered.currentRunIntent?.started_at, 120_000)
  assert.equal(rendered.currentRunIntent?.cumulative_duration_ms, 90_000)
  assert.equal(rendered.latestRunIntent?.run_id, 'run-second')
  assert.equal(rendered.latestRunIntent?.started_at, 120_000)
  assert.equal(rendered.latestRunIntent?.cumulative_duration_ms, 90_000)
})

test('current_run_state preserves terminal backend cumulative duration after refresh', () => {
  const state = createEmptyDesktopV3CacheState()
  applyBootstrapSnapshot(state, snapshotFixture({
    sessions_by_id: { [sessionA.id]: sessionA },
    projections_by_session: { [sessionA.id]: projectionA },
    run_intents_by_session: {},
    current_run_state_by_session: {
      [sessionA.id]: {
        session_id: sessionA.id,
        run_id: 'run-second',
        active: false,
        status: 'completed',
        created_at: 1_000,
        started_at: 120_000,
        completed_at: 125_000,
        duration_ms: 5_000,
        cumulative_duration_ms: 95_000,
        updated_at: 125_000,
        event_seq: 13,
      },
    },
    session_order: [sessionA.id],
    sync_scope: {
      surface: 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: 'selector-hash',
      resource_set: 'current_run_state',
    },
    scope_id: 'selector-hash:current_run_state',
  }))

  const rendered = selectRenderedSessionMessages(state, sessionA.id)
  assert.equal(rendered.currentRunIntent, undefined)
  assert.equal(rendered.latestRunIntent?.duration_ms, 5_000)
  assert.equal(rendered.latestRunIntent?.cumulative_duration_ms, 95_000)
})

test('realtime workset removed preserves transcript after child discovery', () => {
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

test('message mutation applies current_run_state timing for existing chat resume timer', () => {
  const state = createEmptyDesktopV3CacheState()
  applyMessageMutationResult(state, messageMutationFixture({
    run_intent: { ...runIntentA, started_at: undefined, cumulative_duration_ms: undefined },
    current_run_state: {
      session_id: sessionA.id,
      run_id: runIntentA.run_id,
      active: true,
      status: 'running',
      created_at: 10,
      started_at: 10,
      cumulative_duration_ms: 90_000,
      updated_at: 10,
      event_seq: 2,
    },
  }), 'client-1', messageA1.id)

  assert.equal(state.currentRunIntentBySession[sessionA.id]?.started_at, 10)
  assert.equal(state.currentRunIntentBySession[sessionA.id]?.cumulative_duration_ms, 90_000)
  assert.equal(state.runIntentsBySession[sessionA.id]['run-a'].started_at, 10)
  assert.equal(state.runIntentsBySession[sessionA.id]['run-a'].cumulative_duration_ms, 90_000)
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

test('mutation response does not apply realtime outbox twice', () => {
  const state = bootstrappedState()
  const outbox = outboxFixture({
    event: {
      id: 'evt-mutation-outbox-delta',
      session_id: sessionA.id,
      seq: 9,
      event_type: 'session.assistant.delta',
      payload: {
        run_id: runIntentA.run_id,
        run_intent: runIntentA,
        delta: 'must-not-apply',
      },
      ts_unix_ms: 9,
    },
    projection: { ...projectionA, last_event_seq: 9, projection_high_watermark_seq: 9 },
  })

  applyMessageMutationResult(
    state,
    messageMutationFixture({
      realtime_outbox: outbox,
      mutation: { realtime_outbox: outbox },
    }),
    'client-1',
    messageA1.id,
  )

  assert.equal(state.liveRunsBySession[sessionA.id]?.[runIntentA.run_id]?.assistantDraft, undefined)
  assert.equal(Boolean(state.eventsBySession[sessionA.id]?.some((event) => event.id === 'evt-mutation-outbox-delta')), false)
})

test('session create replay does not regress fresher reconnect state or duplicate first message/run', () => {
  const sessionId = 'session-new'
  const firstMessage: MessageSnapshot = {
    id: 'desktop-v3-message:op-1',
    session_id: sessionId,
    global_seq: 2,
    role: 'user',
    content: 'start',
    created_at: 2,
  }
  const firstRun = {
    session_id: sessionId,
    run_id: 'desktop-v3-run:op-1',
    status: 'running',
    created_at: 3,
    updated_at: 7,
    event_seq: 7,
  }
  const freshSession = {
    id: sessionId,
    workspace_path: '/repo',
    workspace_name: 'repo',
    title: 'Fresh session',
    mode: 'auto',
    created_at: 1,
    updated_at: 7,
    message_count: 1,
    last_message_at: 2,
  }
  const staleSession = {
    ...freshSession,
    title: 'Stale create replay',
    updated_at: 1,
    message_count: 0,
    last_message_at: 0,
  }
  const freshProjection = {
    session_id: sessionId,
    last_event_seq: 7,
    projection_high_watermark_seq: 7,
    updated_at: 7,
  }
  const staleProjection = {
    session_id: sessionId,
    last_event_seq: 1,
    projection_high_watermark_seq: 1,
    updated_at: 1,
  }
  const createReplay: SessionCreateMutationResponse = {
    ok: true,
    session_id: sessionId,
    session: staleSession,
    projection: staleProjection,
    mutation: {},
    realtime_outbox: {
      endpoint_seq: 1,
      endpoint_cursor: 'cursor-create-1',
      session_id: sessionId,
      event: {
        id: 'evt-session-created',
        session_id: sessionId,
        seq: 1,
        event_type: 'session.created',
        payload: { session: staleSession },
        ts_unix_ms: 1,
      },
      projection: staleProjection,
    },
  }

  const state = createEmptyDesktopV3CacheState()
  state.desktopSidebarBootstrap.scopeId = 'scope-global'
  state.syncScopesById['scope-global'] = {
    scopeId: 'scope-global',
    surface: 'desktop',
    streamKind: 'v3.sync.snapshot',
    selectorFilterHash: 'selector-global',
    resourceSet: 'messages,run_intents',
    selector: { kind: 'global', global: true },
    endpointCursor: 'cursor-0',
    replayPath: '/v3/sync/stream',
    replayTransport: 'http_post',
    needsBootstrap: false,
  }
  state.tombstonesBySession[sessionId] = { session_id: sessionId, deleted: true }

  applyReconnectSnapshot(state, reconnectFixture({
    snapshot_endpoint_cursor: 'cursor-reconnect-7',
    sessions_by_id: { [sessionId]: freshSession },
    projections_by_session: { [sessionId]: freshProjection },
    messages_by_session: { [sessionId]: [firstMessage] },
    run_intents_by_session: { [sessionId]: [firstRun] },
    current_run_intent_by_session: { [sessionId]: firstRun },
    session_order: [sessionId],
    subscriptions: [{ subscription_id: 'sub-new', session_id: sessionId, status: 'active' }],
  }))

  applySessionCreateMutationResult(state, createReplay, 'scope-global')
  const messageReplay = messageMutationFixture({
    session_id: sessionId,
    message: firstMessage,
    run_intent: firstRun,
    realtime_outbox: {
      endpoint_seq: 2,
      endpoint_cursor: 'cursor-message-2',
      session_id: sessionId,
      event: {
        id: 'evt-first-message',
        session_id: sessionId,
        seq: 2,
        event_type: 'message.stored',
        payload: { message: firstMessage },
        ts_unix_ms: 2,
      },
      projection: { session_id: sessionId, last_event_seq: 2, projection_high_watermark_seq: 2, updated_at: 2 },
    },
  })
  applyMessageMutationResult(state, messageReplay, 'desktop-v3-first-message:op-1', firstMessage.id)

  assert.equal(state.projectionsBySession[sessionId].last_event_seq, 7)
  assert.equal(state.projectionsBySession[sessionId].projection_high_watermark_seq, 7)
  assert.equal(state.sessionsById[sessionId]?.kind, 'full')
  assert.equal(state.sessionsById[sessionId]?.kind === 'full' ? state.sessionsById[sessionId].session.title : '', 'Fresh session')
  assert.deepEqual(state.sessionOrderByScope['scope-global'], [sessionId])
  assert.equal(state.tombstonesBySession[sessionId], undefined)
  assert.deepEqual(state.messagesBySession[sessionId].items.map((message) => message.id), [firstMessage.id])
  assert.equal(Object.keys(state.runIntentsBySession[sessionId]).length, 1)
  assert.equal(state.currentRunIntentBySession[sessionId]?.run_id, firstRun.run_id)
  assert.equal(state.realtime.endpointCursor, 'cursor-reconnect-7')
  assert.equal(state.syncScopesById['scope-global'].endpointCursor, 'cursor-0')
})


test('session settings mutation updates mode metadata preference policy and projection cache immediately', () => {
  const state = bootstrappedState()
  state.preferencesBySession[sessionA.id] = {
    provider: 'codex',
    model: 'old-model',
    thinking: 'medium',
    serviceTier: '',
    contextMode: '',
    updatedAt: 10,
  }

  applySessionSettingsMutationResult(state, {
    ok: true,
    session_id: sessionA.id,
    mode: 'manual',
    metadata: {
      agent_name: 'explorer',
      resolved_agent_name: 'explorer',
      runtime_mode: 'read',
    },
    preference: {
      provider: 'fireworks',
      model: 'deepseek-v4-flash',
      thinking: '',
      serviceTier: 'fast',
      contextMode: '',
      updatedAt: 30,
    },
    agent_model_policy: { agent_name: 'explorer', locked: false },
    mutation: {
      event: {
        id: 'evt-settings-updated',
        session_id: sessionA.id,
        seq: 30,
        event_type: 'session.preference.updated',
        payload: {
          session_id: sessionA.id,
          preference: {
            provider: 'fireworks',
            model: 'deepseek-v4-flash',
            thinking: '',
            serviceTier: 'fast',
            contextMode: '',
            updatedAt: 30,
          },
          updated_at: 30,
        },
        ts_unix_ms: 30,
      },
      projection: {
        session_id: sessionA.id,
        last_event_seq: 30,
        projection_high_watermark_seq: 30,
        updated_at: 30,
      },
    },
  })

  const record = state.sessionsById[sessionA.id]
  assert.equal(record.kind, 'full')
  if (record.kind !== 'full') return
  assert.equal(record.session.mode, 'manual')
  assert.equal(record.session.updated_at, 30)
  assert.equal(record.session.metadata?.agent_name, 'explorer')
  assert.equal(record.session.metadata?.runtime_mode, 'read')
  assert.deepEqual(state.preferencesBySession[sessionA.id], {
    provider: 'fireworks',
    model: 'deepseek-v4-flash',
    thinking: '',
    serviceTier: 'fast',
    contextMode: '',
    updatedAt: 30,
  })
  assert.deepEqual(state.agentModelPolicyBySession[sessionA.id], { agent_name: 'explorer', locked: false })
  assert.equal(state.projectionsBySession[sessionA.id].last_event_seq, 30)
})

test('usage summary events update context cache turn by turn without timestamp regressions', () => {
  const state = bootstrappedState()

  applyCacheEvent(state, {
    source: 'realtime',
    sessionId: sessionA.id,
    eventType: 'run.usage.updated',
    sessionEvent: {
      id: 'evt-usage-1',
      session_id: sessionA.id,
      seq: 40,
      event_type: 'run.usage.updated',
      payload: {
        usage_summary: {
          session_id: sessionA.id,
          provider: 'codex',
          model: 'gpt-5.4',
          source: 'provider',
          context_window: 1000,
          total_tokens: 100,
          remaining_tokens: 900,
          updated_at: 100,
        },
      },
      ts_unix_ms: 100,
    },
    projection: { session_id: sessionA.id, last_event_seq: 40, projection_high_watermark_seq: 40, updated_at: 100 },
    payload: {
      usage_summary: {
        session_id: sessionA.id,
        provider: 'codex',
        model: 'gpt-5.4',
        source: 'provider',
        context_window: 1000,
        total_tokens: 100,
        remaining_tokens: 900,
        updated_at: 100,
      },
    },
  })

  assert.equal((state.usageBySession[sessionA.id] as Record<string, unknown>).remaining_tokens, 900)

  applyCacheEvent(state, {
    source: 'realtime',
    sessionId: sessionA.id,
    eventType: 'run.usage.updated',
    sessionEvent: {
      id: 'evt-usage-older',
      session_id: sessionA.id,
      seq: 41,
      event_type: 'run.usage.updated',
      payload: {
        usage_summary: {
          session_id: sessionA.id,
          context_window: 1000,
          total_tokens: 200,
          remaining_tokens: 800,
          updated_at: 90,
        },
      },
      ts_unix_ms: 90,
    },
    projection: { session_id: sessionA.id, last_event_seq: 41, projection_high_watermark_seq: 41, updated_at: 90 },
    payload: {
      usage_summary: {
        session_id: sessionA.id,
        context_window: 1000,
        total_tokens: 200,
        remaining_tokens: 800,
        updated_at: 90,
      },
    },
  })

  assert.equal((state.usageBySession[sessionA.id] as Record<string, unknown>).remaining_tokens, 900)
  assert.equal((state.usageBySession[sessionA.id] as Record<string, unknown>).updated_at, 100)

  applyCacheEvent(state, {
    source: 'realtime',
    sessionId: sessionA.id,
    eventType: 'run.usage.updated',
    sessionEvent: {
      id: 'evt-usage-newer',
      session_id: sessionA.id,
      seq: 42,
      event_type: 'run.usage.updated',
      payload: {
        usage_summary: {
          session_id: sessionA.id,
          provider: 'codex',
          model: 'gpt-5.4',
          source: 'provider',
          context_window: 1000,
          total_tokens: 150,
          remaining_tokens: 850,
          updated_at: 110,
        },
      },
      ts_unix_ms: 110,
    },
    projection: { session_id: sessionA.id, last_event_seq: 42, projection_high_watermark_seq: 42, updated_at: 110 },
    payload: {
      usage_summary: {
        session_id: sessionA.id,
        provider: 'codex',
        model: 'gpt-5.4',
        source: 'provider',
        context_window: 1000,
        total_tokens: 150,
        remaining_tokens: 850,
        updated_at: 110,
      },
    },
  })

  assert.equal((state.usageBySession[sessionA.id] as Record<string, unknown>).remaining_tokens, 850)
  assert.equal((state.usageBySession[sessionA.id] as Record<string, unknown>).total_tokens, 150)
})

test('session preference settings mutation repairs cached context window baseline immediately', () => {
  const state = bootstrappedState()
  state.usageBySession[sessionA.id] = {
    session_id: sessionA.id,
    provider: 'codex',
    model: 'old-model',
    source: 'provider',
    context_window: 1000,
    total_tokens: 100,
    remaining_tokens: 900,
    updated_at: 10,
  }

  applySessionSettingsMutationResult(state, {
    ok: true,
    session_id: sessionA.id,
    preference: {
      provider: 'codex',
      model: 'gpt-5.4',
      thinking: 'medium',
      serviceTier: '',
      contextMode: '1m',
      updatedAt: 50,
    },
    context_window: 2000,
    max_output_tokens: 4096,
  })

  const usage = state.usageBySession[sessionA.id] as Record<string, unknown>
  assert.equal(usage.provider, 'codex')
  assert.equal(usage.model, 'gpt-5.4')
  assert.equal(usage.context_window, 2000)
  assert.equal(usage.total_tokens, 100)
  assert.equal(usage.remaining_tokens, 1900)
  assert.equal(usage.updated_at, 50)
})

test('workset.session.updated preserves newer plan mode when compact sidebar shell omits mode', () => {
  const state = bootstrappedState()
  const current = state.sessionsById[sessionB.id]
  assert.equal(current?.kind, 'full')
  if (current?.kind !== 'full') return
  state.sessionsById[sessionB.id] = {
    ...current,
    session: { ...current.session, mode: 'plan' },
  }
  const updatedSession: SessionSnapshot = {
    ...sessionB,
    mode: '',
    title: 'Updated sidebar title',
    lifecycle: undefined,
    preference: {},
    metadata: {
      agent_name: 'explorer',
    },
  }

  const actions = realtimeFrameToActions(realtimeFrameFixture({
    kind: 'workset.session.updated',
    workset_id: 'scope-1',
    workset_subscription_id: 'workset-sub-1',
    session_id: sessionB.id,
    endpoint_cursor: 'cursor-workset-update-9',
    rev: 9,
    prevRev: 8,
    event_type: 'session.metadata.updated',
    session: updatedSession,
    projection: { ...projectionB, last_event_seq: 9, projection_high_watermark_seq: 9 },
    current_run_state: {
      session_id: sessionB.id,
      run_id: 'run-b-active',
      active: true,
      status: 'running',
      created_at: 10,
      updated_at: 12,
      event_seq: 9,
    },
    has_active_plan: true,
    active_plan: {
      id: 'plan-workset-active',
      title: 'Sidebar active plan',
      plan: '# Sidebar active plan',
      status: 'approved',
      approval_state: 'approved',
      updated_at: 12,
      document: {
        id: 'plan-workset-active',
        title: 'Sidebar active plan',
        status: 'approved',
        execution_policy: { mode: 'automatic', shape: 'checkpointed' },
        execution_state: { status: 'in_progress', active_attempt_id: 'cp-1:attempt-1', current_run_id: 'run-b-active', current_session_id: sessionB.id, last_checkpoint_id: 'cp-1' },
        active_checkpoint_id: 'cp-1',
        checkpoints: [{ id: 'cp-1', title: 'Keep sidebar fresh', status: 'in_progress', attempt_id: 'cp-1:attempt-1', run_id: 'run-b-active', session_id: sessionB.id, order: 1 }],
      },
    },
  }))

  for (const action of actions) desktopV3CacheReducer(state, action)

  assert.equal(state.sessionsById[sessionB.id]?.kind, 'full')
  assert.equal(state.sessionsById[sessionB.id]?.kind === 'full' ? state.sessionsById[sessionB.id].session.title : '', 'Updated sidebar title')
  assert.equal(state.sessionsById[sessionB.id]?.kind === 'full' ? state.sessionsById[sessionB.id].session.mode : '', 'plan')
  assert.equal(state.sessionsById[sessionB.id]?.kind === 'full' ? state.sessionsById[sessionB.id].session.lifecycle : undefined, undefined)
  assert.deepEqual(state.sessionsById[sessionB.id]?.kind === 'full' ? state.sessionsById[sessionB.id].session.preference : undefined, {})
  assert.deepEqual(state.sessionsById[sessionB.id]?.kind === 'full' ? state.sessionsById[sessionB.id].session.metadata : undefined, { agent_name: 'explorer' })
  assert.deepEqual(state.sessionOrderByScope['scope-1'], [sessionB.id])
  assert.equal(state.currentRunIntentBySession[sessionB.id]?.run_id, 'run-b-active')
  assert.equal(state.hasActivePlanBySession[sessionB.id], true)
  assert.equal(state.plansBySession[sessionB.id]?.id, 'plan-workset-active')
  assert.equal(selectDesktopSidebarRows(state, 'scope-1')[0]?.planExecution?.currentRunId, 'run-b-active')
  assert.equal(state.subscriptionsById['workset-sub-1'], undefined)
  assert.equal(state.realtime.endpointCursor, 'cursor-workset-update-9')
})

test('Desktop V3 live patch path copies only affected references', () => {
  const state = createEmptyDesktopV3CacheState()
  state.sessionsById = { [sessionA.id]: sessionA, [sessionB.id]: sessionB }
  state.messagesBySession = {
    [sessionA.id]: buildMessageListCache([messageA1]),
    [sessionB.id]: buildMessageListCache([messageB1]),
  }
  state.permissionsBySession = { [sessionA.id]: [], [sessionB.id]: [] }
  state.plansBySession = { [sessionA.id]: { id: 'plan-a' }, [sessionB.id]: { id: 'plan-b' } }
  state.liveRunsBySession = {
    [sessionA.id]: {
      [runIntentA.run_id]: { sessionId: sessionA.id, runId: runIntentA.run_id, status: 'running', toolCallsByCallId: {} },
      'run-untouched': { sessionId: sessionA.id, runId: 'run-untouched', status: 'running', toolCallsByCallId: {}, assistantDraft: { content: 'same', updatedAt: 1 } },
    },
    [sessionB.id]: {
      [runIntentA.run_id]: { sessionId: sessionB.id, runId: runIntentA.run_id, status: 'running', toolCallsByCallId: {} },
    },
  }

  const messagesBySession = state.messagesBySession
  const sessionsById = state.sessionsById
  const permissionsBySession = state.permissionsBySession
  const plansBySession = state.plansBySession
  const liveRunsBySession = state.liveRunsBySession
  const sessionARuns = state.liveRunsBySession[sessionA.id]
  const sessionBRuns = state.liveRunsBySession[sessionB.id]
  const untouchedRun = sessionARuns['run-untouched']
  const affectedRun = sessionARuns[runIntentA.run_id]

  const next = applyDesktopV3LivePatchBatch(state, [livePatchFixture({ text: 'x' })])

  assert.notEqual(next, state)
  assert.equal(next.messagesBySession, messagesBySession)
  assert.equal(next.sessionsById, sessionsById)
  assert.equal(next.permissionsBySession, permissionsBySession)
  assert.equal(next.plansBySession, plansBySession)
  assert.notEqual(next.liveRunsBySession, liveRunsBySession)
  assert.notEqual(next.liveRunsBySession[sessionA.id], sessionARuns)
  assert.equal(next.liveRunsBySession[sessionB.id], sessionBRuns)
  assert.equal(next.liveRunsBySession[sessionA.id]['run-untouched'], untouchedRun)
  assert.notEqual(next.liveRunsBySession[sessionA.id][runIntentA.run_id], affectedRun)
  assert.equal(next.liveRunsBySession[sessionA.id][runIntentA.run_id].assistantDraft?.content, 'x')
})

function livePatchFixture(overrides: Partial<SessionV3RealtimeLivePatchWire> = {}): SessionV3RealtimeLivePatchWire {
  const text = overrides.text ?? 'x'
  const offsetStart = overrides.offset_start ?? 0
  const offsetEnd = overrides.offset_end ?? offsetStart + new TextEncoder().encode(text).byteLength
  return {
    session_id: sessionA.id,
    run_id: runIntentA.run_id,
    stream_id: `assistant:${runIntentA.run_id}:step:1`,
    stream_kind: 'assistant_text',
    operation: 'append',
    step: 1,
    step_id: 'step-1',
    live_seq_start: 1,
    live_seq_end: 1,
    offset_start: offsetStart,
    offset_end: offsetEnd,
    text,
    recorded_at: 1,
    ...overrides,
  }
}
