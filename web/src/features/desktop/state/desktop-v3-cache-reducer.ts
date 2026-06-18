import type {
  CacheEvent,
  DesktopV3CacheAction,
  DesktopV3CacheState,
  LiveRunOverlay,
  MessageListCache,
  MessageSnapshot,
  PendingUserMessage,
  RealtimeMessage,
  SessionEventPayload,
  SessionMessageMutationResponse,
  SessionsReconnectResponse,
  SubscriptionCache,
  SyncSnapshotResponse,
  V3RealtimeOutboxRecord,
  V3SessionEvent,
  V3SessionRunIntent,
  V3SessionTombstone,
  WorksetCache,
  SessionSnapshot,
  MessageMutationConflictResponse,
} from './desktop-v3-cache-types'
import { decodeSessionEventPayload, normalizeRealtimeEventFrame, outboxRecordToCacheEvent } from './desktop-v3-cache-wire'

const ACTIVE_RUN_INTENT_STATUSES = new Set(['pending_executor', 'running', 'dispatch_blocked'])
const TERMINAL_RUN_INTENT_STATUSES = new Set(['completed', 'failed', 'cancelled', 'interrupted', 'expired'])

export function createEmptyDesktopV3CacheState(surface = 'desktop'): DesktopV3CacheState {
  return {
    version: 1,
    syncScopesById: {},
    realtime: {
      status: 'closed',
      surface,
      needsReconnect: false,
      needsBootstrap: false,
    },
    desktopSidebarBootstrap: {
      status: 'idle',
    },
    desktopInitialHydrate: {
      status: 'idle',
      requestedSessionIds: [],
      hydratedSessionIds: [],
    },
    sessionsById: {},
    projectionsBySession: {},
    sessionOrderByScope: {},
    tombstonesBySession: {},
    messagesBySession: {},
    eventsBySession: {},
    runIntentsBySession: {},
    currentRunIntentBySession: {},
    pendingUserByClientRequestId: {},
    liveRunsBySession: {},
    subscriptionsById: {},
    worksetsById: {},
    plansBySession: {},
    planRevisionsBySession: {},
    permissionsBySession: {},
    usageBySession: {},
    preferencesBySession: {},
    agentModelPolicyBySession: {},
    historyManifestsBySession: {},
    historyChunksById: {},
    omissionsByScope: {},
    paginationByScope: {},
    watermarksByScope: {},
  }
}

export function desktopV3CacheReducer(state: DesktopV3CacheState, action: DesktopV3CacheAction): DesktopV3CacheState {
  switch (action.type) {
    case 'desktopSidebarBootstrap.update':
      state.desktopSidebarBootstrap = {
        ...state.desktopSidebarBootstrap,
        ...action.patch,
      }
      return state
    case 'desktopInitialHydrate.update':
      state.desktopInitialHydrate = {
        ...state.desktopInitialHydrate,
        ...action.patch,
      }
      return state
    case 'session.select':
      state.selectedSessionId = action.sessionId?.trim() || undefined
      return state
    case 'snapshot.apply':
      return applyBootstrapSnapshot(state, action.snapshot)
    case 'hydrate.apply':
      return applyHydrateSnapshot(state, action.snapshot, action.requestedSessionIds)
    case 'syncStream.applyBatch':
      return applySyncStreamBatch(state, action)
    case 'reconnect.applySnapshot':
      return applyReconnectSnapshot(state, action.snapshot)
    case 'realtime.storeResume':
      state.realtime.streamPath = action.streamPath
      state.realtime.resumeFrame = action.resume
      state.realtime.endpointCursor = action.resume.endpoint_cursor
      return state
    case 'realtime.applyEvent':
      applyCacheEvent(state, action.event)
      if (action.endpointCursor) {
        state.realtime.endpointCursor = action.endpointCursor
      }
      return state
    case 'realtime.worksetSessionDiscovered':
      applyWorksetSessionDiscovered(state, action.frame)
      return state
    case 'realtime.worksetSessionRemoved':
      applyWorksetSessionRemoved(state, action.frame)
      return state
    case 'realtime.cursorError':
      markCursorError(state, action.frame)
      return state
    case 'realtime.control':
    case 'realtime.unknownFrame':
      return applyRealtimeFrame(state, { frame: action.frame })
    case 'mutation.messageResult':
      return applyMessageMutationResult(state, action.raw, action.clientRequestId, action.messageId)
    case 'pendingUser.upsert':
      return upsertPendingUserMessage(state, action.input)
    default: {
      const _exhaustive: never = action
      return _exhaustive
    }
  }
}

export function applyBootstrapSnapshot(
  state: DesktopV3CacheState,
  raw: SyncSnapshotResponse,
): DesktopV3CacheState {
  return applySnapshot(state, { source: 'bootstrap', scopeId: raw.scope_id, snapshot: raw })
}

export function applySnapshot(
  state: DesktopV3CacheState,
  action: { source: 'bootstrap'; scopeId: string; snapshot: SyncSnapshotResponse },
): DesktopV3CacheState {
  const snapshot = action.snapshot
  requireProtocol(snapshot.scope_id, 'snapshot.scope_id')
  requireProtocol(snapshot.sync_scope, 'snapshot.sync_scope')
  requireProtocol(snapshot.snapshot_endpoint_cursor, 'snapshot.snapshot_endpoint_cursor')

  writeSyncScope(state, snapshot)
  upsertSessions(state, snapshot.sessions_by_id)
  mergeRecord(state.projectionsBySession, snapshot.projections_by_session)
  state.sessionOrderByScope[snapshot.scope_id] = removeTombstonedIds(state, snapshot.session_order ?? [])
  applyTombstonesBySession(state, snapshot.tombstones_by_session)
  mergeRunIntentsBySession(state, snapshot.run_intents_by_session)
  mergeSnapshotResources(state, snapshot, snapshot.scope_id)
  applyMessagesBySessionFromSnapshot(state, snapshot.messages_by_session)
  applyEventsBySessionFromSnapshot(state, snapshot.events_by_session)

  return state
}

export function applyHydrateSnapshot(
  state: DesktopV3CacheState,
  raw: SyncSnapshotResponse,
  requestedSessionIds: string[],
): DesktopV3CacheState {
  return applyHydrate(state, { source: 'hydrate', scopeId: raw.scope_id, requestedSessionIds, snapshot: raw })
}

export function applyHydrate(
  state: DesktopV3CacheState,
  action: {
    source: 'hydrate'
    scopeId: string
    requestedSessionIds: string[]
    snapshot: SyncSnapshotResponse
  },
): DesktopV3CacheState {
  const snapshot = action.snapshot
  if (snapshot.selector?.kind !== 'session_ids') {
    throw new Error(`protocol invalid: hydrate selector kind must be session_ids, got ${String(snapshot.selector?.kind ?? '')}`)
  }

  const requested = new Set(action.requestedSessionIds)
  for (const sessionId of snapshot.session_order ?? []) {
    if (!requested.has(sessionId)) {
      throw new Error(`protocol invalid: hydrate session_order includes non-requested session ${sessionId}`)
    }
  }
  assertMapSubset(snapshot.sessions_by_id, requested, 'sessions_by_id')
  assertMapSubset(snapshot.projections_by_session, requested, 'projections_by_session')
  assertMapSubset(snapshot.messages_by_session, requested, 'messages_by_session')
  assertMapSubset(snapshot.events_by_session, requested, 'events_by_session')
  assertMapSubset(snapshot.run_intents_by_session, requested, 'run_intents_by_session')
  assertObjectKeysSubset('tombstones_by_session', snapshot.tombstones_by_session, requested)
  assertObjectKeysSubset('plans_by_session', snapshot.plans_by_session, requested)
  assertObjectKeysSubset('plan_revisions_by_session', snapshot.plan_revisions_by_session, requested)
  assertObjectKeysSubset('permissions_by_session', snapshot.permissions_by_session, requested)
  assertObjectKeysSubset('usage_by_session', snapshot.usage_by_session, requested)
  assertObjectKeysSubset('preferences_by_session', snapshot.preferences_by_session, requested)
  assertObjectKeysSubset('agent_model_policy_by_session', snapshot.agent_model_policy_by_session, requested)
  assertObjectKeysSubset('history_manifests_by_session', snapshot.history_manifests_by_session, requested)

  requireProtocol(snapshot.scope_id, 'snapshot.scope_id')
  requireProtocol(snapshot.sync_scope, 'snapshot.sync_scope')
  requireProtocol(snapshot.snapshot_endpoint_cursor, 'snapshot.snapshot_endpoint_cursor')
  writeSyncScope(state, snapshot)
  upsertSessions(state, snapshot.sessions_by_id)
  mergeRecord(state.projectionsBySession, onlyRequested(snapshot.projections_by_session, requested))
  applyTombstonesBySession(state, snapshot.tombstones_by_session)
  mergeRunIntentsBySession(state, snapshot.run_intents_by_session)
  mergeSnapshotResources(state, snapshot, snapshot.scope_id, requested)
  applyMessagesBySessionFromSnapshot(state, snapshot.messages_by_session)
  applyEventsBySessionFromSnapshot(state, snapshot.events_by_session)

  for (const sessionId of requested) {
    const record = state.sessionsById[sessionId]
    if (record?.kind === 'full') {
      record.needsHydrate = false
    }
  }

  return state
}

export function applySyncStreamBatch(
  state: DesktopV3CacheState,
  action: {
    scopeId: string
    endpointCursor: string
    events: CacheEvent[]
    hasMore: boolean
    replayInstructions: { stream_path: string; transport: string; after_endpoint_cursor?: string }
  },
): DesktopV3CacheState {
  const scope = state.syncScopesById[action.scopeId]

  requireProtocol(
    scope,
    `sync stream scope ${action.scopeId} is not bootstrapped`,
  )

  for (const event of action.events) {
    applyCacheEvent(state, event)
  }

  scope.endpointCursor = action.endpointCursor
  scope.replayPath = action.replayInstructions?.stream_path ?? '/v3/sync/stream'
  scope.replayTransport = action.replayInstructions?.transport ?? 'http_post'
  scope.needsBootstrap = false
  scope.lastError = undefined
  scope.lastErrorCode = undefined

  return state
}

export function applyReconnectSnapshot(
  state: DesktopV3CacheState,
  raw: SessionsReconnectResponse,
): DesktopV3CacheState {
  state.realtime.endpointCursor = raw.snapshot_endpoint_cursor
  state.realtime.surface = raw.surface ?? state.realtime.surface
  upsertSessions(state, raw.sessions_by_id)
  mergeRecord(state.projectionsBySession, raw.projections_by_session)
  mergeRunIntentsBySession(state, raw.run_intents_by_session)
  mergeRecord(state.currentRunIntentBySession, raw.current_run_intent_by_session)
  mergeOptionalResources(state, raw)
  applyMessagesBySessionFromSnapshot(state, raw.messages_by_session)
  applyEventsBySessionFromSnapshot(state, raw.events_by_session)

  for (const subscription of raw.subscriptions ?? []) {
    const id = subscriptionId(subscription)
    if (id) state.subscriptionsById[id] = { ...state.subscriptionsById[id], ...subscription }
  }

  for (const workset of raw.worksets ?? []) {
    const id = worksetId(workset)
    if (id) state.worksetsById[id] = { ...state.worksetsById[id], ...workset }
  }

  if (raw.workset_id) {
    state.sessionOrderByScope[raw.workset_id] = [...(raw.session_order ?? [])]
    state.worksetsById[raw.workset_id] = {
      ...state.worksetsById[raw.workset_id],
      workset_id: raw.workset_id,
      sessionIds: [...(raw.session_order ?? [])],
    }
  }

  if (raw.realtime?.resume) {
    state.realtime.resumeFrame = raw.realtime.resume
    state.realtime.streamPath = raw.realtime.stream_path
  }

  return state
}

export function applyRealtimeFrame(
  state: DesktopV3CacheState,
  action: { frame: RealtimeMessage },
): DesktopV3CacheState {
  const frame = action.frame
  switch (frame.kind) {
    case 'hello':
      state.realtime.status = 'open'
      state.realtime.endpointCursor = frame.endpoint_cursor
      state.realtime.lastHelloCursor = frame.endpoint_cursor
      return state

    case 'event':
      applyCacheEvent(state, normalizeRealtimeEventFrame(frame))
      state.realtime.endpointCursor = frame.endpoint_cursor
      return state

    case 'replay.started':
      markSubscriptionReplaying(state, frame)
      return state

    case 'replay.complete':
      markSubscriptionReplayComplete(state, frame)
      state.realtime.endpointCursor = frame.endpoint_cursor
      return state

    case 'endpoint.watermark':
      state.realtime.endpointCursor = frame.endpoint_cursor
      return state

    case 'keepalive':
      state.realtime.endpointCursor = frame.endpoint_cursor
      state.realtime.lastKeepaliveCursor = frame.endpoint_cursor
      return state

    case 'projection.high_watermark':
      return applyProjectionWatermark(state, frame)

    case 'workset.session.discovered':
      applyWorksetSessionDiscovered(state, frame)
      return state

    case 'workset.session.removed':
      applyWorksetSessionRemoved(state, frame)
      return state

    case 'cursor.error':
      markCursorError(state, frame)
      return state

    case 'auth.denied':
      state.realtime.status = 'auth_denied'
      state.realtime.errorCode = stringField(frame.error_code)
      state.realtime.error = stringField(frame.error)
      return state

    case 'slow_consumer.reconnect_required':
      state.realtime.needsReconnect = true
      return state

    default:
      return state
  }
}

export function applyMessageMutationResult(
  state: DesktopV3CacheState,
  raw: SessionMessageMutationResponse | MessageMutationConflictResponse,
  clientRequestId: string,
  messageId: string,
): DesktopV3CacheState {
  if (raw.ok === false) {
    const pending = state.pendingUserByClientRequestId[clientRequestId]
    if (pending) {
      pending.status = 'failed'
      pending.error = stringField(raw.error) || stringField(raw.error_code) || 'message mutation failed'
    }
    return state
  }

  if (raw.message) {
    upsertCommittedMessage(state, raw.session_id || raw.message.session_id, raw.message)
  }
  delete state.pendingUserByClientRequestId[clientRequestId]
  for (const [pendingClientRequestId, pending] of Object.entries(state.pendingUserByClientRequestId)) {
    if (pending.messageId === messageId) {
      delete state.pendingUserByClientRequestId[pendingClientRequestId]
    }
  }
  if (raw.run_intent) {
    upsertRunIntent(state, raw.run_intent.session_id || raw.session_id, raw.run_intent)
  }
  const outbox = raw.realtime_outbox ?? raw.mutation?.realtime_outbox
  if (outbox) {
    applyCacheEvent(state, outboxRecordToCacheEvent(outbox))
  }
  return state
}

export function upsertPendingUserMessage(
  state: DesktopV3CacheState,
  input: {
    sessionId: string
    clientRequestId: string
    messageId: string
    content: string
    metadata?: Record<string, unknown>
    createdAt: number
  },
): DesktopV3CacheState {
  const pending: PendingUserMessage = {
    clientRequestId: input.clientRequestId,
    messageId: input.messageId,
    sessionId: input.sessionId,
    role: 'user',
    content: input.content,
    metadata: input.metadata,
    createdAt: input.createdAt,
    status: 'pending',
  }
  state.pendingUserByClientRequestId[input.clientRequestId] = pending
  return state
}

export function applyCacheEvent(state: DesktopV3CacheState, event: CacheEvent): DesktopV3CacheState {
  const { sessionId, projection, payload, eventType } = event

  if (projection) {
    state.projectionsBySession[sessionId] = projection
  }

  if (event.sessionEvent) {
    const existingEvents = state.eventsBySession[sessionId] ?? []
    const index = existingEvents.findIndex((entry) => entry.id === event.sessionEvent?.id)
    if (index >= 0) {
      existingEvents[index] = event.sessionEvent
      state.eventsBySession[sessionId] = existingEvents
    } else {
      state.eventsBySession[sessionId] = [...existingEvents, event.sessionEvent].sort((left, right) => left.seq - right.seq)
    }
  }

  if (payload.session) {
    state.sessionsById[sessionId] = {
      kind: 'full',
      session: payload.session,
      needsHydrate: false,
    }
  }

  if (payload.message) {
    upsertCommittedMessage(state, sessionId, payload.message)
  }

  if (payload.run_intent) {
    upsertRunIntent(state, sessionId, payload.run_intent)
  }

  const record = state.sessionsById[sessionId]
  if (payload.lifecycle && record?.kind === 'full') {
    record.session.lifecycle = payload.lifecycle
  }

  if (payload.usage_summary) {
    state.usageBySession[sessionId] = payload.usage_summary
  }

  if (payload.tombstone || eventType === 'session.deleted') {
    applyTombstone(state, sessionId, payload.tombstone)
  }

  applyScalarSessionPatchIfPresent(state, sessionId, payload, eventType)
  applyLiveRunOverlayFromEvent(state, event)

  return state
}

export function upsertCommittedMessage(
  state: DesktopV3CacheState,
  sessionId: string,
  message: MessageSnapshot,
): void {
  const list = state.messagesBySession[sessionId] ?? buildMessageListCache([])
  const idIndex = list.byMessageId[message.id]
  const seqKey = messageGlobalSeqKey(message)
  const seqIndex = list.byGlobalSeq[seqKey]
  const nextItems = [...list.items]

  if (idIndex !== undefined) {
    nextItems[idIndex] = message
  } else if (seqIndex !== undefined) {
    nextItems[seqIndex] = message
  } else {
    nextItems.push(message)
  }

  state.messagesBySession[sessionId] = buildMessageListCache(nextItems)
  removeCommittedPendingForSession(state, sessionId, [message])
  maybeClearLiveAssistantOverlay(state, sessionId, message)
}

export function upsertRunIntent(
  state: DesktopV3CacheState,
  sessionId: string,
  runIntent: V3SessionRunIntent,
): void {
  const byRunId = state.runIntentsBySession[sessionId] ?? {}
  const existing = byRunId[runIntent.run_id]
  if (!existing || runIntent.event_seq >= existing.event_seq) {
    byRunId[runIntent.run_id] = runIntent
    state.runIntentsBySession[sessionId] = byRunId
  }

  if (ACTIVE_RUN_INTENT_STATUSES.has(runIntent.status)) {
    state.currentRunIntentBySession[sessionId] = runIntent
  } else if (TERMINAL_RUN_INTENT_STATUSES.has(runIntent.status)
    && state.currentRunIntentBySession[sessionId]?.run_id === runIntent.run_id) {
    delete state.currentRunIntentBySession[sessionId]
  }

  const liveRuns = state.liveRunsBySession[sessionId] ?? {}
  const liveRun = liveRuns[runIntent.run_id] ?? createLiveRunOverlay(sessionId, runIntent.run_id)
  liveRun.status = normalizeLiveRunStatus(runIntent.status)
  liveRun.lastEventSeqSeen = Math.max(liveRun.lastEventSeqSeen ?? 0, runIntent.event_seq)
  liveRuns[runIntent.run_id] = liveRun
  state.liveRunsBySession[sessionId] = liveRuns
}

export function applyTombstone(
  state: DesktopV3CacheState,
  sessionId: string,
  tombstone?: V3SessionTombstone,
): void {
  state.tombstonesBySession[sessionId] = tombstone ?? { session_id: sessionId, deleted: true }
  for (const [scopeId, order] of Object.entries(state.sessionOrderByScope)) {
    state.sessionOrderByScope[scopeId] = order.filter((id) => id !== sessionId)
  }
  for (const [subscriptionId, subscription] of Object.entries(state.subscriptionsById)) {
    if (subscription.session_id === sessionId || subscription.sessionId === sessionId) {
      delete state.subscriptionsById[subscriptionId]
    }
  }
  for (const workset of Object.values(state.worksetsById)) {
    workset.sessionIds = (workset.sessionIds ?? []).filter((id) => id !== sessionId)
    if (!workset.inactiveSessionIds) workset.inactiveSessionIds = []
    if (!workset.inactiveSessionIds.includes(sessionId)) workset.inactiveSessionIds.push(sessionId)
  }
}

export function applyWorksetSessionDiscovered(state: DesktopV3CacheState, frame: RealtimeMessage): void {
  const worksetIdValue = stringField(frame.workset_id)
  const sessionId = stringField(frame.session_id)
  if (!worksetIdValue || !sessionId) return

  if (!state.sessionsById[sessionId]) {
    state.sessionsById[sessionId] = {
      kind: 'stub',
      id: sessionId,
      needsHydrate: true,
      discoveredByWorksetId: worksetIdValue,
      discoveredAt: Date.now(),
    }
  }

  state.sessionOrderByScope[worksetIdValue] = appendUnique(state.sessionOrderByScope[worksetIdValue] ?? [], sessionId)
  const workset = state.worksetsById[worksetIdValue] ?? { workset_id: worksetIdValue, worksetId: worksetIdValue }
  workset.sessionIds = appendUnique(workset.sessionIds ?? [], sessionId)
  workset.inactiveSessionIds = (workset.inactiveSessionIds ?? []).filter((id) => id !== sessionId)
  state.worksetsById[worksetIdValue] = workset
  state.realtime.endpointCursor = frame.endpoint_cursor
}

export function applyWorksetSessionRemoved(state: DesktopV3CacheState, frame: RealtimeMessage): void {
  const worksetIdValue = stringField(frame.workset_id)
  const sessionId = stringField(frame.session_id)
  if (!worksetIdValue || !sessionId) return

  state.sessionOrderByScope[worksetIdValue] = (state.sessionOrderByScope[worksetIdValue] ?? []).filter((id) => id !== sessionId)
  const workset = state.worksetsById[worksetIdValue] ?? { workset_id: worksetIdValue, worksetId: worksetIdValue }
  workset.sessionIds = (workset.sessionIds ?? []).filter((id) => id !== sessionId)
  workset.inactiveSessionIds = appendUnique(workset.inactiveSessionIds ?? [], sessionId)
  state.worksetsById[worksetIdValue] = workset
  state.realtime.endpointCursor = frame.endpoint_cursor
}

export function markCursorError(
  state: DesktopV3CacheState,
  frameOrError: {
    error_code?: string
    error?: string
    bootstrap_required?: boolean
    oldest_available_endpoint_seq?: number
    latest_endpoint_seq?: number
    missing_endpoint_seq?: number
    scope_id?: string
  },
): void {
  const errorCode = stringField(frameOrError.error_code)
  const error = stringField(frameOrError.error)
  state.realtime.errorCode = errorCode
  state.realtime.error = error
  if (frameOrError.bootstrap_required) {
    state.realtime.needsBootstrap = true
    const scopeId = stringField(frameOrError.scope_id)
    if (scopeId && state.syncScopesById[scopeId]) {
      state.syncScopesById[scopeId].needsBootstrap = true
      state.syncScopesById[scopeId].lastErrorCode = errorCode
      state.syncScopesById[scopeId].lastError = error
    } else {
      for (const scope of Object.values(state.syncScopesById)) {
        scope.needsBootstrap = true
        scope.lastErrorCode = errorCode
        scope.lastError = error
      }
    }
  }
}

export function applyMessagesBySessionFromSnapshot(
  state: DesktopV3CacheState,
  messagesBySession: Record<string, MessageSnapshot[]> | undefined,
): DesktopV3CacheState {
  if (!messagesBySession) return state

  for (const [sessionId, messages] of Object.entries(messagesBySession)) {
    state.messagesBySession[sessionId] = buildMessageListCache(messages)
    removeCommittedPendingForSession(state, sessionId, messages)
  }

  return state
}

export function buildMessageListCache(messages: MessageSnapshot[]): MessageListCache {
  const deduped: MessageSnapshot[] = []
  const indexById: Record<string, number> = {}
  const indexBySeq: Record<string, number> = {}

  for (const message of messages) {
    const existingById = indexById[message.id]
    const seqKey = messageGlobalSeqKey(message)
    const existingBySeq = indexBySeq[seqKey]
    if (existingById !== undefined) {
      deduped[existingById] = message
    } else if (existingBySeq !== undefined) {
      deduped[existingBySeq] = message
    } else {
      const index = deduped.push(message) - 1
      indexById[message.id] = index
      indexBySeq[seqKey] = index
    }
  }

  const items = [...deduped].sort((left, right) => {
    if (left.global_seq !== right.global_seq) return left.global_seq - right.global_seq
    return (left.created_at - right.created_at) || left.id.localeCompare(right.id)
  })
  const byMessageId: Record<string, number> = {}
  const byGlobalSeq: Record<string, number> = {}
  items.forEach((message, index) => {
    byMessageId[message.id] = index
    byGlobalSeq[messageGlobalSeqKey(message)] = index
  })
  return { items, byMessageId, byGlobalSeq }
}

function writeSyncScope(state: DesktopV3CacheState, snapshot: SyncSnapshotResponse): void {
  const scopeId = snapshot.scope_id
  const syncScope = snapshot.sync_scope
  state.syncScopesById[scopeId] = {
    scopeId,
    surface: syncScope.surface,
    streamKind: 'v3.sync.snapshot',
    selectorFilterHash: syncScope.selector_filter_hash,
    resourceSet: syncScope.resource_set,
    selector: snapshot.selector,
    endpointCursor: snapshot.snapshot_endpoint_cursor,
    replayPath: '/v3/sync/stream',
    replayTransport: 'http_post',
    needsBootstrap: false,
  }
}

function mergeSnapshotResources(
  state: DesktopV3CacheState,
  snapshot: SyncSnapshotResponse,
  scopeId: string,
  requested?: Set<string>,
): void {
  mergeOptionalResources(state, snapshot, requested)
  if (snapshot.omissions !== undefined) state.omissionsByScope[scopeId] = snapshot.omissions
  if (snapshot.pagination !== undefined) state.paginationByScope[scopeId] = snapshot.pagination
  if (snapshot.watermarks !== undefined) state.watermarksByScope[scopeId] = snapshot.watermarks
}

function mergeOptionalResources(
  state: DesktopV3CacheState,
  raw: Partial<SyncSnapshotResponse> | SessionsReconnectResponse,
  requested?: Set<string>,
): void {
  mergeRecord(state.plansBySession, maybeOnlyRequested(raw.plans_by_session, requested))
  mergeRecord(state.planRevisionsBySession, maybeOnlyRequested(raw.plan_revisions_by_session, requested))
  mergeRecord(state.permissionsBySession, maybeOnlyRequested(raw.permissions_by_session, requested))
  mergeRecord(state.usageBySession, maybeOnlyRequested(raw.usage_by_session, requested))
  mergeRecord(state.preferencesBySession, maybeOnlyRequested(raw.preferences_by_session, requested))
  mergeRecord(state.agentModelPolicyBySession, maybeOnlyRequested(raw.agent_model_policy_by_session, requested))
  mergeRecord(state.historyManifestsBySession, maybeOnlyRequested(raw.history_manifests_by_session, requested))
  mergeRecord(state.historyChunksById, raw.history_chunks_by_id)
}

function upsertSessions(state: DesktopV3CacheState, sessionsById: Record<string, SessionSnapshot> | undefined): void {
  if (!sessionsById) return
  for (const [sessionId, session] of Object.entries(sessionsById)) {
    state.sessionsById[sessionId] = { kind: 'full', session, needsHydrate: false }
  }
}

function mergeRunIntentsBySession(state: DesktopV3CacheState, runIntentsBySession: Record<string, V3SessionRunIntent[]> | undefined): void {
  if (!runIntentsBySession) return
  for (const [sessionId, runIntents] of Object.entries(runIntentsBySession)) {
    for (const runIntent of runIntents) {
      upsertRunIntent(state, sessionId, runIntent)
    }
  }
}

function applyTombstonesBySession(state: DesktopV3CacheState, tombstonesBySession: Record<string, V3SessionTombstone> | undefined): void {
  if (!tombstonesBySession) return
  for (const [sessionId, tombstone] of Object.entries(tombstonesBySession)) {
    applyTombstone(state, sessionId, tombstone)
  }
}

function applyEventsBySessionFromSnapshot(state: DesktopV3CacheState, eventsBySession: Record<string, V3SessionEvent[]> | undefined): void {
  if (!eventsBySession) return
  for (const [sessionId, events] of Object.entries(eventsBySession)) {
    state.eventsBySession[sessionId] = [...events].sort((left, right) => left.seq - right.seq)
  }
}

function applyScalarSessionPatchIfPresent(
  state: DesktopV3CacheState,
  sessionId: string,
  payload: SessionEventPayload,
  eventType: string,
): void {
  const record = state.sessionsById[sessionId]
  if (record?.kind !== 'full') return

  if (typeof payload.title === 'string') record.session.title = payload.title
  if (typeof payload.mode === 'string') record.session.mode = payload.mode
  if (typeof payload.status === 'string' && eventType.startsWith('run.')) {
    const runId = typeof payload.run_id === 'string' ? payload.run_id : undefined
    if (runId) {
      upsertRunIntent(state, sessionId, {
        session_id: sessionId,
        run_id: runId,
        status: payload.status,
        blocked_reason: typeof payload.blocked_reason === 'string' ? payload.blocked_reason : undefined,
        created_at: Number(payload.created_at ?? 0),
        updated_at: Number(payload.updated_at ?? Date.now()),
        event_seq: Number(payload.event_seq ?? state.projectionsBySession[sessionId]?.last_event_seq ?? 0),
      })
    }
  }
}

function removeCommittedPendingForSession(state: DesktopV3CacheState, sessionId: string, messages: MessageSnapshot[]): void {
  for (const [clientRequestId, pending] of Object.entries(state.pendingUserByClientRequestId)) {
    if (pending.sessionId !== sessionId) continue
    if (messages.some((message) => pendingMatchesCommitted(pending, message))) {
      delete state.pendingUserByClientRequestId[clientRequestId]
    }
  }
}

function pendingMatchesCommitted(pending: PendingUserMessage, message: MessageSnapshot): boolean {
  if (pending.messageId === message.id) return true
  return pending.sessionId === message.session_id
    && pending.role === message.role
    && pending.content.trim() === message.content.trim()
    && pending.createdAt <= message.created_at
}

function maybeClearLiveAssistantOverlay(state: DesktopV3CacheState, sessionId: string, message: MessageSnapshot): void {
  if (message.role !== 'assistant') return
  const runId = stringFromMetadata(message.metadata, 'run_id') || stringFromMetadata(message.metadata, 'runId')
  if (!runId) return
  const liveRun = state.liveRunsBySession[sessionId]?.[runId]
  if (liveRun) {
    delete liveRun.assistantDraft
  }
}

function createLiveRunOverlay(sessionId: string, runId: string): LiveRunOverlay {
  return {
    sessionId,
    runId,
    status: 'pending_executor',
    toolCallsByCallId: {},
  }
}

function applyLiveRunOverlayFromEvent(
  state: DesktopV3CacheState,
  event: CacheEvent,
): void {
  const payload = event.payload ?? {}
  const sessionId = event.sessionId
  const eventSeq = event.sessionEvent?.seq ?? event.projection?.last_event_seq ?? 0
  const updatedAt = Number(
    payload.recorded_at ??
      payload.updated_at ??
      event.sessionEvent?.ts_unix_ms ??
      Date.now(),
  )

  const runIntent = recordValue(payload.runIntent) ?? recordValue(payload.run_intent)
  const runId = stringValue(payload.run_id) || stringValue(runIntent?.run_id)
  if (!runId) {
    return
  }

  const liveRun = ensureLiveRunOverlay(state, sessionId, runId)
  liveRun.lastEventSeqSeen = Math.max(liveRun.lastEventSeqSeen ?? 0, eventSeq)

  switch (event.eventType) {
    case 'session.assistant.delta':
    case 'session.message.delta': {
      const delta =
        stringValue(payload.delta) ||
        stringValue(payload.text_delta) ||
        stringValue(payload.content_delta) ||
        ''

      if (!delta) {
        return
      }

      liveRun.status = liveRun.status === 'pending_executor' ? 'running' : liveRun.status
      liveRun.assistantDraft = {
        content: `${liveRun.assistantDraft?.content ?? ''}${delta}`,
        updatedAt,
      }
      return
    }

    case 'session.tool.delta': {
      const callId = stringValue(payload.call_id)
      if (!callId) {
        return
      }

      const tool = liveRun.toolCallsByCallId[callId] ?? {
        callId,
        updatedAt,
      }

      tool.stepId = stringValue(payload.step_id) || tool.stepId
      tool.toolInstanceId =
        stringValue(payload.tool_instance_id) || tool.toolInstanceId
      tool.toolName = stringValue(payload.tool_name) || tool.toolName

      const argsDelta =
        stringValue(payload.arguments_delta) ||
        stringValue(payload.arguments) ||
        ''
      const outputDelta =
        stringValue(payload.output_delta) ||
        stringValue(payload.delta) ||
        stringValue(payload.output) ||
        ''

      if (argsDelta) {
        tool.argumentsText = `${tool.argumentsText ?? ''}${argsDelta}`
      }

      if (outputDelta) {
        tool.outputText = `${tool.outputText ?? ''}${outputDelta}`
      }

      tool.status = stringValue(payload.status) || tool.status || 'running'
      tool.updatedAt = updatedAt

      liveRun.toolCallsByCallId[callId] = tool
      return
    }

    default:
      return
  }
}

function ensureLiveRunOverlay(
  state: DesktopV3CacheState,
  sessionId: string,
  runId: string,
): LiveRunOverlay {
  state.liveRunsBySession[sessionId] ??= {}
  state.liveRunsBySession[sessionId][runId] ??= {
    sessionId,
    runId,
    status: 'running',
    toolCallsByCallId: {},
  }
  return state.liveRunsBySession[sessionId][runId]
}

function normalizeLiveRunStatus(status: string): LiveRunOverlay['status'] {
  switch (status) {
    case 'pending_executor':
    case 'running':
    case 'dispatch_blocked':
    case 'completed':
    case 'failed':
    case 'cancelled':
    case 'interrupted':
    case 'expired':
      return status
    default:
      return 'running'
  }
}

function markSubscriptionReplaying(state: DesktopV3CacheState, frame: RealtimeMessage): void {
  const id = stringField(frame.subscription_id)
  if (!id) return
  state.subscriptionsById[id] = { ...state.subscriptionsById[id], subscription_id: id, replaying: true, caughtUp: false }
}

function markSubscriptionReplayComplete(state: DesktopV3CacheState, frame: RealtimeMessage): void {
  const id = stringField(frame.subscription_id)
  if (!id) return
  state.subscriptionsById[id] = { ...state.subscriptionsById[id], subscription_id: id, replaying: false, caughtUp: true }
}

function applyProjectionWatermark(state: DesktopV3CacheState, frame: RealtimeMessage): DesktopV3CacheState {
  if (frame.projection?.session_id) {
    state.projectionsBySession[frame.projection.session_id] = frame.projection
  }
  state.realtime.endpointCursor = frame.endpoint_cursor
  return state
}

function removeTombstonedIds(state: DesktopV3CacheState, ids: string[]): string[] {
  return ids.filter((id) => !state.tombstonesBySession[id])
}

function requireProtocol<T>(value: T, name: string): asserts value is NonNullable<T> {
  if (!value) throw new Error(`protocol invalid: missing ${name}`)
}

function assertMapSubset<T>(map: Record<string, T> | undefined, allowed: Set<string>, label: string): void {
  if (!map) return
  for (const sessionId of Object.keys(map)) {
    if (!allowed.has(sessionId)) {
      throw new Error(`protocol invalid: hydrate ${label} includes non-requested session ${sessionId}`)
    }
  }
}

function assertObjectKeysSubset(
  name: string,
  value: Record<string, unknown> | undefined,
  allowed: Set<string>,
): void {
  if (!value) {
    return
  }

  for (const key of Object.keys(value)) {
    requireProtocol(
      allowed.has(key),
      `hydrate ${name} included non-requested session ${key}`,
    )
  }
}

function onlyRequested<T>(map: Record<string, T> | undefined, requested: Set<string>): Record<string, T> | undefined {
  if (!map) return undefined
  const result: Record<string, T> = {}
  for (const [key, value] of Object.entries(map)) {
    if (requested.has(key)) result[key] = value
  }
  return result
}

function maybeOnlyRequested<T>(map: Record<string, T> | undefined, requested?: Set<string>): Record<string, T> | undefined {
  return requested ? onlyRequested(map, requested) : map
}

function mergeRecord<T>(target: Record<string, T>, source: Record<string, T | undefined> | undefined): void {
  if (!source) return
  for (const [key, value] of Object.entries(source)) {
    target[key] = value as T
  }
}

function messageGlobalSeqKey(message: MessageSnapshot): string {
  return `${message.session_id}:${message.global_seq}`
}

function appendUnique(values: string[], value: string): string[] {
  return values.includes(value) ? values : [...values, value]
}

function stringField(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value : undefined
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined
}

function stringFromMetadata(metadata: Record<string, unknown> | undefined, key: string): string | undefined {
  const value = metadata?.[key]
  return typeof value === 'string' && value.trim() ? value : undefined
}

function subscriptionId(subscription: SubscriptionCache | Record<string, unknown>): string | undefined {
  return stringField(subscription.subscription_id) || stringField(subscription.subscriptionId)
}

function worksetId(workset: WorksetCache | Record<string, unknown>): string | undefined {
  return stringField(workset.workset_id) || stringField(workset.worksetId)
}

export function cacheEventFromOutbox(raw: V3RealtimeOutboxRecord): CacheEvent {
  return outboxRecordToCacheEvent(raw)
}

export function payloadFromEventPayload(payload: unknown): SessionEventPayload {
  return decodeSessionEventPayload({ id: '', session_id: '', seq: 0, event_type: '', payload, ts_unix_ms: 0 })
}
