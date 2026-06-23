import type {
  CacheEvent,
  DesktopV3CacheAction,
  DesktopV3CacheState,
  LiveRunOverlay,
  LiveRunReasoningOverlay,
  MessageListCache,
  MessageSnapshot,
  PendingUserMessage,
  RealtimeMessage,
  SessionEventPayload,
  SessionCreateMutationResponse,
  SessionMessageMutationResponse,
  SessionMutationErrorResponse,
  SessionSettingsMutationResponse,
  SessionsReconnectResponse,
  SubscriptionCache,
  SyncSnapshotResponse,
  V3SessionEvent,
  V3SessionProjection,
  V3SessionRunIntent,
  V3SessionTombstone,
  WorksetCache,
  SessionSnapshot,
  MessageMutationConflictResponse,
} from './desktop-v3-cache-types'
import type { DesktopPermissionRecord } from '../types/realtime'
import { decodeSessionEventPayload, normalizeRealtimeEventFrame } from './desktop-v3-cache-wire'

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
    case 'desktopV3Cache.applyHydrationPlan':
      return applyHydrationPlan(state, action.reusedSessionIds, action.hydrateSessionIds)
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
    case 'liveRun.mergeRepairEvents':
      mergeLiveRunRepairEvents(state, action.sessionId, action.runId, action.events)
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
    case 'realtime.statusChanged':
      state.realtime.status = action.status
      state.realtime.errorCode = action.errorCode
      state.realtime.error = action.error
      if (action.status === 'open') {
        state.realtime.needsReconnect = false
        state.realtime.errorCode = undefined
        state.realtime.error = undefined
      } else if (action.status === 'error' || action.status === 'stale') {
        state.realtime.needsReconnect = true
      }
      return state
    case 'mutation.sessionCreateResult':
      return applySessionCreateMutationResult(state, action.raw, action.sidebarScopeId)
    case 'mutation.messageResult':
      return applyMessageMutationResult(state, action.raw, action.clientRequestId, action.messageId)
    case 'mutation.sessionSettingsResult':
      return applySessionSettingsMutationResult(state, action.raw)
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

export function applyHydrationPlan(
  state: DesktopV3CacheState,
  reusedSessionIds: string[],
  hydrateSessionIds: string[],
): DesktopV3CacheState {
  for (const sessionId of reusedSessionIds) {
    const record = state.sessionsById[sessionId]
    if (record?.kind === 'full') record.needsHydrate = false
  }
  for (const sessionId of hydrateSessionIds) {
    const record = state.sessionsById[sessionId]
    if (record) record.needsHydrate = true
  }
  return state
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
  if (syncResourceSetContains(snapshot.sync_scope.resource_set, 'run_intents')) {
    const authoritativeRunIntentSessionIds = new Set([
      ...Object.keys(snapshot.sessions_by_id ?? {}),
      ...Object.keys(snapshot.tombstones_by_session ?? {}),
    ])
    replaceRunIntentsBySession(state, snapshot.run_intents_by_session, authoritativeRunIntentSessionIds)
  }
  mergeSnapshotResources(state, snapshot, snapshot.scope_id)
  if (syncResourceSetContains(snapshot.sync_scope.resource_set, 'messages')) {
    applyMessagesBySessionFromSnapshot(state, snapshot.messages_by_session)
  }
  if (syncResourceSetContains(snapshot.sync_scope.resource_set, 'events')) {
    applyEventsBySessionFromSnapshot(state, snapshot.events_by_session)
  }
  for (const sessionId of snapshot.session_order ?? []) {
    const record = state.sessionsById[sessionId]
    if (record?.kind === 'full' && hydrateResponseCompletesSession(snapshot, sessionId)) {
      record.needsHydrate = false
    }
  }

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
  const preHydrateProjections = { ...state.projectionsBySession }
  const preHydrateSessions = { ...state.sessionsById }
  writeSyncScope(state, snapshot)
  applyHydrateSessionsAndProjections(state, snapshot, requested, preHydrateProjections, preHydrateSessions)
  applyTombstonesBySession(state, snapshot.tombstones_by_session)
  applyHydrateAuthoritativeResources(state, snapshot, requested, preHydrateProjections)

  for (const sessionId of requested) {
    const record = state.sessionsById[sessionId]
    if (record?.kind === 'full' && hydrateResponseCompletesSession(snapshot, sessionId)) {
      record.needsHydrate = false
    }
  }

  return state
}

function applyHydrateSessionsAndProjections(
  state: DesktopV3CacheState,
  snapshot: SyncSnapshotResponse,
  requested: Set<string>,
  preHydrateProjections: Record<string, SyncSnapshotResponse['projections_by_session'][string]>,
  preHydrateSessions: DesktopV3CacheState['sessionsById'],
): void {
  for (const sessionId of requested) {
    const incomingProjection = snapshot.projections_by_session?.[sessionId]
    const existingProjection = preHydrateProjections[sessionId]
    const fresh = projectionSeq(incomingProjection) >= projectionSeq(existingProjection)
    const incomingSession = snapshot.sessions_by_id?.[sessionId]
    if (incomingSession && (fresh || !preHydrateSessions[sessionId])) {
      const existing = state.sessionsById[sessionId]
      state.sessionsById[sessionId] = {
        kind: 'full',
        session: incomingSession,
        needsHydrate: existing?.needsHydrate ?? true,
      }
    }
    if (incomingProjection && (fresh || !existingProjection)) {
      state.projectionsBySession[sessionId] = incomingProjection
    }
  }
}

function applyHydrateAuthoritativeResources(
  state: DesktopV3CacheState,
  snapshot: SyncSnapshotResponse,
  requested: Set<string>,
  preHydrateProjections: Record<string, V3SessionProjection | undefined>,
): void {
  const resourceSet = snapshot.sync_scope.resource_set
  const freshRequested = freshHydrateSessionIds(snapshot, requested, preHydrateProjections)
  mergeSnapshotResources(state, snapshot, snapshot.scope_id, freshRequested)

  if (syncResourceSetContains(resourceSet, 'messages')) {
    for (const sessionId of requested) {
      if (!hasOwn(snapshot.messages_by_session, sessionId)) continue
      const incoming = snapshot.messages_by_session?.[sessionId] ?? []
      if (hydrateProjectionIsFresh(snapshot, sessionId, preHydrateProjections)) {
        replaceMessagesForSession(state, sessionId, incoming)
      } else {
        mergeHistoricalMessagesForSession(state, sessionId, incoming)
      }
    }
  }

  if (syncResourceSetContains(resourceSet, 'events')) {
    for (const sessionId of requested) {
      if (!hasOwn(snapshot.events_by_session, sessionId)) continue
      const incoming = snapshot.events_by_session?.[sessionId] ?? []
      if (hydrateProjectionIsFresh(snapshot, sessionId, preHydrateProjections)) {
        state.eventsBySession[sessionId] = sortEvents(dedupeEventsByIdentity(incoming))
      } else {
        mergeEventsForSession(state, sessionId, incoming)
      }
    }
  }

  if (syncResourceSetContains(resourceSet, 'run_intents')) {
    for (const sessionId of requested) {
      if (hasOwn(snapshot.tombstones_by_session, sessionId)
        || hydrateProjectionIsFresh(snapshot, sessionId, preHydrateProjections)) {
        replaceRunIntentsForSession(state, sessionId, snapshot.run_intents_by_session?.[sessionId] ?? [])
      } else {
        for (const runIntent of snapshot.run_intents_by_session?.[sessionId] ?? []) {
          upsertRunIntent(state, sessionId, runIntent)
        }
      }
    }
  }
}

function hydrateProjectionIsFresh(
  snapshot: SyncSnapshotResponse,
  sessionId: string,
  preHydrateProjections: Record<string, V3SessionProjection | undefined>,
): boolean {
  return projectionSeq(snapshot.projections_by_session?.[sessionId]) >= projectionSeq(preHydrateProjections[sessionId])
}

function freshHydrateSessionIds(
  snapshot: SyncSnapshotResponse,
  requested: Set<string>,
  preHydrateProjections: Record<string, V3SessionProjection | undefined>,
): Set<string> {
  const fresh = new Set<string>()
  for (const sessionId of requested) {
    if (hasOwn(snapshot.tombstones_by_session, sessionId) || hydrateProjectionIsFresh(snapshot, sessionId, preHydrateProjections)) {
      fresh.add(sessionId)
    }
  }
  return fresh
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
  if (!state.realtime.endpointCursor) {
    state.realtime.endpointCursor = raw.snapshot_endpoint_cursor
  }
  state.realtime.surface = raw.surface ?? state.realtime.surface

  const resources = reconnectResourceSet(raw)
  const authoritativeSessionIds = new Set([
    ...Object.keys(raw.sessions_by_id ?? {}),
    ...(raw.session_order ?? []),
    ...Object.keys(raw.current_run_intent_by_session ?? {}),
    ...Object.keys(raw.run_intents_by_session ?? {}),
  ])

  upsertSessions(state, raw.sessions_by_id)
  mergeRecord(state.projectionsBySession, raw.projections_by_session)

  if (resources.has('run_intents')) {
    replaceRunIntentsBySession(state, raw.run_intents_by_session, authoritativeSessionIds)
    applyCurrentRunIntentsFromReconnect(state, raw, authoritativeSessionIds)
  } else {
    mergeRunIntentsBySession(state, raw.run_intents_by_session)
    mergeRecord(state.currentRunIntentBySession, raw.current_run_intent_by_session)
  }

  mergeReconnectOptionalResources(state, raw, resources, authoritativeSessionIds)
  if (resources.has('messages')) {
    applyMessagesBySessionFromSnapshot(state, raw.messages_by_session)
  } else {
    mergeMessagesBySessionFromSnapshot(state, raw.messages_by_session)
  }
  if (resources.has('events')) {
    applyEventsBySessionFromSnapshot(state, raw.events_by_session)
  } else {
    mergeEventsBySessionFromSnapshot(state, raw.events_by_session)
  }

  for (const subscription of raw.subscriptions ?? []) {
    const id = subscriptionId(subscription)
    if (id) state.subscriptionsById[id] = { ...state.subscriptionsById[id], ...subscription }
  }

  for (const workset of raw.worksets ?? []) {
    const id = worksetId(workset as unknown as Record<string, unknown>)
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

export function applySessionCreateMutationResult(
  state: DesktopV3CacheState,
  raw: SessionCreateMutationResponse | SessionMutationErrorResponse,
  sidebarScopeId: string,
): DesktopV3CacheState {
  if (raw.ok === false) return state

  const sessionId = raw.session_id.trim()
  if (!sessionId || raw.session.id !== sessionId) {
    throw new Error('Desktop V3 create response has inconsistent session identity')
  }

  const existingProjection = state.projectionsBySession[sessionId]
  if (!existingProjection || projectionSeq(raw.projection) >= projectionSeq(existingProjection)) {
    state.sessionsById[sessionId] = {
      kind: 'full',
      session: raw.session,
      needsHydrate: false,
    }
    state.projectionsBySession[sessionId] = raw.projection
  }
  delete state.tombstonesBySession[sessionId]

  state.sessionOrderByScope[sidebarScopeId] = prependUnique(
    state.sessionOrderByScope[sidebarScopeId] ?? [],
    sessionId,
  )

  const workset = state.worksetsById[sidebarScopeId]
  if (workset) {
    workset.sessionIds = prependUnique(workset.sessionIds ?? [], sessionId)
    workset.inactiveSessionIds = (workset.inactiveSessionIds ?? [])
      .filter((id) => id !== sessionId)
  }

  return state
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
  const sessionId = raw.session_id || raw.message?.session_id || raw.run_intent?.session_id || ''
  applyUsageSummaryFromUnknown(state, sessionId, raw.usage_summary)
  applyUsageSummaryFromUnknown(state, sessionId, recordValue(raw.mutation)?.usage_summary)
  delete state.pendingUserByClientRequestId[clientRequestId]
  for (const [pendingClientRequestId, pending] of Object.entries(state.pendingUserByClientRequestId)) {
    if (pending.messageId === messageId) {
      delete state.pendingUserByClientRequestId[pendingClientRequestId]
    }
  }
  if (raw.run_intent) {
    upsertRunIntent(state, raw.run_intent.session_id || raw.session_id, raw.run_intent)
  }
  return state
}

export function applySessionSettingsMutationResult(
  state: DesktopV3CacheState,
  raw: SessionSettingsMutationResponse,
): DesktopV3CacheState {
  if (raw.ok === false) return state
  applyMutationOutboxEvent(state, raw.mutation)

  const sessionId = stringField(raw.session_id) || stringField(raw.mutation?.session_id)
  if (!sessionId) return state

  const record = state.sessionsById[sessionId]
  if (record?.kind === 'full') {
    let nextSession = record.session
    if (typeof raw.mode === 'string') {
      nextSession = { ...nextSession, mode: raw.mode }
    }
    const metadata = recordValue(raw.metadata)
    if (metadata) {
      nextSession = { ...nextSession, metadata }
    }
    if (raw.preference !== undefined) {
      nextSession = { ...nextSession, preference: raw.preference }
    }
    if (nextSession !== record.session) {
      state.sessionsById[sessionId] = { ...record, session: nextSession }
    }
  }

  if (raw.preference !== undefined) {
    state.preferencesBySession[sessionId] = raw.preference
  }
  if (raw.agent_model_policy !== undefined) {
    state.agentModelPolicyBySession[sessionId] = raw.agent_model_policy
  }
  applyUsageSummaryFromUnknown(state, sessionId, raw.usage_summary)
  applyUsageSummaryFromUnknown(state, sessionId, recordValue(raw.mutation)?.usage_summary)
  applyUsageContextBaselineFromSettings(state, sessionId, raw)

  return state
}

function applyMutationOutboxEvent(
  state: DesktopV3CacheState,
  mutation: SessionSettingsMutationResponse['mutation'] | undefined | null,
): void {
  if (!mutation?.event) return
  applyCacheEvent(state, {
    source: 'outbox',
    sessionId: mutation.event.session_id,
    eventType: mutation.event.event_type,
    sessionEvent: mutation.event,
    projection: mutation.projection,
    payload: decodeSessionEventPayload(mutation.event),
  })
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

export function applyCacheEvent(
  state: DesktopV3CacheState,
  event: CacheEvent,
): DesktopV3CacheState {
  const { sessionId, projection, payload, eventType } = event
  const existingProjection = state.projectionsBySession[sessionId]
  const incomingProjectionIsFresh = projectionSeq(projection) >= projectionSeq(existingProjection)
  if (incomingProjectionIsFresh) {
    if (projection) state.projectionsBySession[sessionId] = projection
  }

  if (event.sessionEvent) {
    const existingEvents = state.eventsBySession[sessionId] ?? []
    const duplicateIndex = existingEvents.findIndex((entry) =>
      entry.id === event.sessionEvent?.id
      || entry.seq === event.sessionEvent?.seq,
    )
    if (duplicateIndex >= 0) {
      // Durable session events are immutable. Do not apply the same delta twice.
      return state
    }
    state.eventsBySession[sessionId] = [...existingEvents, event.sessionEvent]
      .sort((left, right) => left.seq - right.seq)
  }

  if (payload.session && incomingProjectionIsFresh) {
    state.sessionsById[sessionId] = {
      kind: 'full',
      session: payload.session,
      needsHydrate: false,
    }
  }

  if (payload.run_intent) {
    upsertRunIntent(state, sessionId, payload.run_intent)
  }

  if (payload.message) {
    upsertCommittedMessage(
      state,
      sessionId,
      payload.message,
      resolveDesktopV3CacheEventRunId(event),
      payload.run_intent?.status,
    )
  }

  const record = state.sessionsById[sessionId]
  if (payload.lifecycle && record?.kind === 'full') {
    state.sessionsById[sessionId] = {
      ...record,
      session: { ...record.session, lifecycle: payload.lifecycle },
    }
  }

  applyUsageSummaryFromEventPayload(state, sessionId, payload)

  applyPermissionEvent(state, event)

  if (payload.tombstone || eventType === 'session.deleted') {
    applyTombstone(state, sessionId, payload.tombstone)
  }

  applyScalarSessionPatchIfPresent(state, sessionId, payload, eventType)
  applyLiveRunOverlayFromEvent(state, event)
  if (payload.message) {
    reconcileLiveRunWithCommittedMessage(
      state,
      sessionId,
      payload.message,
      resolveDesktopV3CacheEventRunId(event),
      payload.run_intent?.status,
    )
  }
  commitCompletedReasoningEvent(state, event)

  return state
}

function applyPermissionEvent(state: DesktopV3CacheState, event: CacheEvent): void {
  if (event.eventType !== 'permission.requested' && event.eventType !== 'permission.updated' && !event.payload.permission) return
  const permission = normalizePermissionRecord(event.payload.permission, event.sessionId)
  if (!permission) return
  upsertPermissionRecord(state, permission)
}

function normalizePermissionRecord(value: unknown, fallbackSessionId: string): DesktopPermissionRecord | undefined {
  const source = recordValue(value)
  if (!source) return undefined
  const id = stringValue(source.id).trim()
  const sessionId = stringValue(source.session_id).trim() || stringValue(source.sessionId).trim() || fallbackSessionId
  if (!id || !sessionId) return undefined
  const savedRule = recordValue(source.saved_rule) ?? recordValue(source.savedRule)
  return {
    id,
    sessionId,
    runId: stringValue(source.run_id).trim() || stringValue(source.runId).trim(),
    callId: stringValue(source.call_id).trim() || stringValue(source.callId).trim(),
    toolName: stringValue(source.tool_name).trim() || stringValue(source.toolName).trim(),
    toolArguments: stringValue(source.tool_arguments) || stringValue(source.toolArguments),
    approvedArguments: stringValue(source.approved_arguments).trim() || stringValue(source.approvedArguments).trim() || undefined,
    savedRule: savedRule ? {
      id: stringValue(savedRule.id).trim(),
      kind: stringValue(savedRule.kind).trim(),
      decision: stringValue(savedRule.decision).trim(),
      tool: stringValue(savedRule.tool).trim() || undefined,
      pattern: stringValue(savedRule.pattern).trim() || undefined,
      createdAt: numberValue(savedRule.created_at) || numberValue(savedRule.createdAt) || undefined,
      updatedAt: numberValue(savedRule.updated_at) || numberValue(savedRule.updatedAt) || undefined,
    } : undefined,
    status: stringValue(source.status).trim(),
    decision: stringValue(source.decision).trim(),
    reason: stringValue(source.reason).trim(),
    requirement: stringValue(source.requirement).trim(),
    mode: stringValue(source.mode).trim(),
    createdAt: numberValue(source.created_at) || numberValue(source.createdAt),
    updatedAt: numberValue(source.updated_at) || numberValue(source.updatedAt),
    resolvedAt: numberValue(source.resolved_at) || numberValue(source.resolvedAt),
    permissionRequestedAt: numberValue(source.permission_requested_at) || numberValue(source.permissionRequestedAt),
  }
}

function normalizePermissionRecords(values: unknown[] | undefined, sessionId: string): DesktopPermissionRecord[] {
  return (values ?? [])
    .map((value) => normalizePermissionRecord(value, sessionId))
    .filter((permission): permission is DesktopPermissionRecord => Boolean(permission && permission.status.trim().toLowerCase() === 'pending'))
    .sort(comparePermissions)
}

function replacePermissionsBySession(
  target: DesktopV3CacheState['permissionsBySession'],
  source: Record<string, unknown[] | undefined> | undefined,
  sessionIds: Set<string>,
): void {
  for (const sessionId of sessionIds) {
    delete target[sessionId]
  }
  if (!source) return
  for (const sessionId of sessionIds) {
    const permissions = normalizePermissionRecords(source[sessionId], sessionId)
    if (permissions.length > 0) {
      target[sessionId] = permissions
    }
  }
}

function mergePermissionsBySession(
  target: DesktopV3CacheState['permissionsBySession'],
  source: Record<string, unknown[] | undefined> | undefined,
): void {
  if (!source) return
  for (const [sessionId, values] of Object.entries(source)) {
    const permissions = normalizePermissionRecords(values, sessionId)
    if (permissions.length > 0) {
      target[sessionId] = permissions
    }
  }
}

function upsertPermissionRecord(state: DesktopV3CacheState, permission: DesktopPermissionRecord): void {
  const sessionId = permission.sessionId || ''
  if (!sessionId) return
  const current = state.permissionsBySession[sessionId] ?? []
  const existing = current.find((entry) => entry.id === permission.id)
  if (existing && comparePermissionFreshness(permission, existing) < 0) {
    return
  }
  const next = [...current.filter((entry) => entry.id !== permission.id), permission].sort(comparePermissions)
  state.permissionsBySession[sessionId] = next
}

function permissionFreshness(permission: DesktopPermissionRecord): number {
  return Math.max(permission.resolvedAt || 0, permission.updatedAt || 0, permission.permissionRequestedAt || 0, permission.createdAt || 0)
}

function terminalPermissionRank(permission: DesktopPermissionRecord): number {
  return permission.status.trim().toLowerCase() === 'pending' ? 0 : 1
}

function comparePermissionFreshness(left: DesktopPermissionRecord, right: DesktopPermissionRecord): number {
  return permissionFreshness(left) - permissionFreshness(right)
    || terminalPermissionRank(left) - terminalPermissionRank(right)
}

function comparePermissions(left: DesktopPermissionRecord, right: DesktopPermissionRecord): number {
  return (left.permissionRequestedAt || left.createdAt || left.updatedAt || 0) - (right.permissionRequestedAt || right.createdAt || right.updatedAt || 0)
    || left.id.localeCompare(right.id)
}

function commitCompletedReasoningEvent(state: DesktopV3CacheState, event: CacheEvent): void {
  if (event.eventType !== 'session.reasoning.completed') return

  const payload = event.payload ?? {}
  const runId = resolveDesktopV3CacheEventRunId(event)
  if (!runId) return

  const sessionId = event.sessionId
  const key = reasoningOverlayKey(payload)
  const liveReasoning = state.liveRunsBySession[sessionId]?.[runId]?.reasoningByKey?.[key]
    ?? state.liveRunsBySession[sessionId]?.[runId]?.reasoning
  const text = stringValue(payload.text)
    || stringValue(payload.summary)
    || liveReasoning?.text
    || liveReasoning?.summary
    || ''
  const content = text.trim()
  if (!content) return

  const seq = event.sessionEvent?.seq ?? event.projection?.last_event_seq ?? 0
  const createdAt = Number(
    payload.recorded_at
      ?? payload.updated_at
      ?? event.sessionEvent?.ts_unix_ms
      ?? Date.now(),
  )
  const id = stringValue(payload.message_id)
    || `reasoning:${sessionId}:${runId}:${key}:${seq || createdAt}`

  const status = state.runIntentsBySession[sessionId]?.[runId]?.status
  const message: MessageSnapshot = {
    id,
    session_id: sessionId,
    global_seq: resolveCommittedReasoningMessageSeq(state, sessionId, id, seq),
    role: 'reasoning',
    content,
    created_at: createdAt,
    metadata: {
      run_id: runId,
      reasoning_overlay_key: key,
      reasoning_id: stringValue(payload.reasoning_id) || undefined,
      reasoning_key: stringValue(payload.reasoning_key) || undefined,
      step_id: stringValue(payload.step_id) || undefined,
      source_event_type: event.eventType,
    },
  }

  upsertCommittedMessage(state, sessionId, message, runId, status)
  reconcileLiveRunWithCommittedMessage(state, sessionId, message, runId, status)
}

export function upsertCommittedMessage(
  state: DesktopV3CacheState,
  sessionId: string,
  message: MessageSnapshot,
  sourceRunId?: string,
  sourceRunStatus?: string,
): void {
  const list = state.messagesBySession[sessionId] ?? buildMessageListCache([])
  const idIndex = list.byMessageId[message.id]
  const seqKey = messageGlobalSeqKey(message)
  const seqIndex = list.byGlobalSeq[seqKey]
  const nextItems = [...list.items]
  const inserted = idIndex === undefined && seqIndex === undefined

  if (idIndex !== undefined) {
    nextItems[idIndex] = message
  } else if (seqIndex !== undefined) {
    nextItems[seqIndex] = message
  } else {
    nextItems.push(message)
  }

  const sessionRecord = state.sessionsById[sessionId]
  const session = sessionRecord?.kind === 'full' ? sessionRecord.session : undefined
  const priorSourceMessageCount = list.sourceMessageCount ?? list.items.length
  const observedSourceMessageCount = Math.max(inserted ? priorSourceMessageCount + 1 : priorSourceMessageCount, nextItems.length)
  const sessionMessageCount = Number.isSafeInteger(session?.message_count) ? session?.message_count : undefined
  const observedLastMessageAt = Math.max(list.sourceLastMessageAt ?? 0, message.created_at)
  const sessionLastMessageAt = Number.isSafeInteger(session?.last_message_at) ? session?.last_message_at : undefined

  state.messagesBySession[sessionId] = buildMessageListCache(nextItems, {
    knownTail: list.knownTail,
    knownFull: list.knownFull,
    sourceMessageCount: Math.max(sessionMessageCount ?? 0, observedSourceMessageCount),
    sourceLastMessageAt: Math.max(sessionLastMessageAt ?? 0, observedLastMessageAt),
    sourceProjectionHighWatermarkSeq: Math.max(
      list.sourceProjectionHighWatermarkSeq ?? 0,
      state.projectionsBySession[sessionId]?.projection_high_watermark_seq ?? 0,
    ),
    hydratedAt: list.hydratedAt,
    tailHydratedAt: list.tailHydratedAt,
    source: 'network',
  })
  removeCommittedPendingForSession(state, sessionId, [message])
  finalizeLiveRunForCommittedMessage(state, sessionId, message, sourceRunId, sourceRunStatus)
}

export function upsertRunIntent(
  state: DesktopV3CacheState,
  sessionId: string,
  runIntent: V3SessionRunIntent,
): void {
  const byRunId = state.runIntentsBySession[sessionId] ?? {}
  const existing = byRunId[runIntent.run_id]
  if (existing && runIntent.event_seq < existing.event_seq) {
    return
  }

  byRunId[runIntent.run_id] = runIntent
  state.runIntentsBySession[sessionId] = byRunId

  if (ACTIVE_RUN_INTENT_STATUSES.has(runIntent.status)) {
    state.currentRunIntentBySession[sessionId] = runIntent
  } else if (TERMINAL_RUN_INTENT_STATUSES.has(runIntent.status)
    && state.currentRunIntentBySession[sessionId]?.run_id === runIntent.run_id) {
    delete state.currentRunIntentBySession[sessionId]
  }

  const liveRuns = state.liveRunsBySession[sessionId] ?? {}
  const liveRun = liveRuns[runIntent.run_id] ?? createLiveRunOverlay(sessionId, runIntent.run_id)
  liveRun.status = normalizeLiveRunStatus(runIntent.status)
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
  delete state.liveRunsBySession[sessionId]
  delete state.currentRunIntentBySession[sessionId]
  delete state.runIntentsBySession[sessionId]
}

export function applyWorksetSessionDiscovered(
  state: DesktopV3CacheState,
  frame: RealtimeMessage,
): void {
  const discoveredWorksetId = stringField(frame.workset_id)
  const sessionId = stringField(frame.session_id)
  if (!discoveredWorksetId || !sessionId) return

  if (!state.sessionsById[sessionId]) {
    state.sessionsById[sessionId] = {
      kind: 'stub',
      id: sessionId,
      needsHydrate: true,
      discoveredByWorksetId: discoveredWorksetId,
      discoveredAt: Date.now(),
    }
  }

  const scopeIds = new Set<string>([discoveredWorksetId])
  if (state.desktopSidebarBootstrap.scopeId) {
    scopeIds.add(state.desktopSidebarBootstrap.scopeId)
  }
  for (const scopeId of scopeIds) {
    const current = state.sessionOrderByScope[scopeId] ?? []
    state.sessionOrderByScope[scopeId] = [sessionId, ...current.filter((id) => id !== sessionId)]
  }

  const workset = state.worksetsById[discoveredWorksetId] ?? {
    workset_id: discoveredWorksetId,
    worksetId: discoveredWorksetId,
  }
  workset.sessionIds = [sessionId, ...(workset.sessionIds ?? []).filter((id) => id !== sessionId)]
  workset.inactiveSessionIds = (workset.inactiveSessionIds ?? []).filter((id) => id !== sessionId)
  state.worksetsById[discoveredWorksetId] = workset

  const subscriptionId = stringField(frame.subscription_id)
  if (subscriptionId) {
    state.subscriptionsById[subscriptionId] = {
      ...state.subscriptionsById[subscriptionId],
      subscription_id: subscriptionId,
      session_id: sessionId,
      workset_id: discoveredWorksetId,
      autoSubscribed: Boolean(frame.auto_subscribed),
      endpoint_cursor: state.realtime.endpointCursor,
      status: 'active',
    }
  }

  // Intentionally do not update state.realtime.endpointCursor here.
  // The matching durable event for this same endpoint record must be applied first.
}

export function applyWorksetSessionRemoved(state: DesktopV3CacheState, frame: RealtimeMessage): void {
  const worksetIdValue = stringField(frame.workset_id)
  const sessionId = stringField(frame.session_id)
  if (!worksetIdValue || !sessionId) return

  state.sessionOrderByScope[worksetIdValue] = (state.sessionOrderByScope[worksetIdValue] ?? []).filter((id) => id !== sessionId)
  if (state.desktopSidebarBootstrap.scopeId) {
    const scopeId = state.desktopSidebarBootstrap.scopeId
    state.sessionOrderByScope[scopeId] = (state.sessionOrderByScope[scopeId] ?? []).filter((id) => id !== sessionId)
  }
  const workset = state.worksetsById[worksetIdValue] ?? { workset_id: worksetIdValue, worksetId: worksetIdValue }
  workset.sessionIds = (workset.sessionIds ?? []).filter((id) => id !== sessionId)
  workset.inactiveSessionIds = appendUnique(workset.inactiveSessionIds ?? [], sessionId)
  state.worksetsById[worksetIdValue] = workset

  const subscriptionId = stringField(frame.subscription_id)
  if (subscriptionId) {
    delete state.subscriptionsById[subscriptionId]
  }
  // Discovery/removal frames do not advance the global realtime cursor.
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
    replaceMessagesForSession(state, sessionId, messages)
  }

  return state
}

function replaceMessagesForSession(
  state: DesktopV3CacheState,
  sessionId: string,
  messages: MessageSnapshot[],
): void {
  const sessionRecord = state.sessionsById[sessionId]
  const session = sessionRecord?.kind === 'full' ? sessionRecord.session : undefined
  state.messagesBySession[sessionId] = buildMessageListCache(messages, {
    sourceMessageCount: session?.message_count,
    sourceLastMessageAt: session?.last_message_at,
    sourceProjectionHighWatermarkSeq: state.projectionsBySession[sessionId]?.projection_high_watermark_seq,
    hydratedAt: Date.now(),
    tailHydratedAt: Date.now(),
    source: 'network',
  })
  removeCommittedPendingForSession(state, sessionId, messages)
}

function mergeHistoricalMessagesForSession(
  state: DesktopV3CacheState,
  sessionId: string,
  incoming: MessageSnapshot[],
): void {
  const existing = state.messagesBySession[sessionId]
  const mergedItems = [...(existing?.items ?? []), ...incoming]
  const merged = buildMessageListCache(mergedItems, {
    knownTail: existing?.knownTail,
    knownFull: existing?.knownFull,
    sourceMessageCount: Math.max(existing?.sourceMessageCount ?? 0, incoming.length, mergedItems.length),
    sourceLastMessageAt: Math.max(existing?.sourceLastMessageAt ?? 0, ...incoming.map((message) => message.created_at)),
    sourceProjectionHighWatermarkSeq: existing?.sourceProjectionHighWatermarkSeq,
    hydratedAt: Math.max(existing?.hydratedAt ?? 0, Date.now()),
    tailHydratedAt: existing?.tailHydratedAt,
    source: 'network',
  })
  state.messagesBySession[sessionId] = merged
  removeCommittedPendingForSession(state, sessionId, incoming)
}

function mergeMessagesBySessionFromSnapshot(
  state: DesktopV3CacheState,
  messagesBySession: Record<string, MessageSnapshot[]> | undefined,
): void {
  if (!messagesBySession) return
  for (const [sessionId, messages] of Object.entries(messagesBySession)) {
    if (messages.length > 0) mergeHistoricalMessagesForSession(state, sessionId, messages)
  }
}

interface BuildMessageListCacheOptions {
  knownTail?: MessageListCache['knownTail']
  knownFull?: boolean
  sourceMessageCount?: number
  sourceLastMessageAt?: number
  sourceProjectionHighWatermarkSeq?: number
  hydratedAt?: number
  tailHydratedAt?: number
  source?: MessageListCache['source']
}

export function buildMessageListCache(messages: MessageSnapshot[], options: BuildMessageListCacheOptions = {}): MessageListCache {
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
  return {
    items,
    byMessageId,
    byGlobalSeq,
    knownTail: options.knownTail,
    knownFull: options.knownFull,
    sourceMessageCount: options.sourceMessageCount,
    sourceLastMessageAt: options.sourceLastMessageAt,
    sourceProjectionHighWatermarkSeq: options.sourceProjectionHighWatermarkSeq,
    hydratedAt: options.hydratedAt,
    tailHydratedAt: options.tailHydratedAt,
    source: options.source,
  }
}

export function syncResourceSetContains(resourceSet: string | undefined, resource: string): boolean {
  if (!resourceSet) return false
  const wanted = resource.trim()
  if (!wanted) return false
  return resourceSet.split(',').some((part) => part.trim() === wanted)
}

export function hydrateResponseCompletesSession(snapshot: SyncSnapshotResponse, sessionId: string): boolean {
  const hasAuthoritativeMessages = syncResourceSetContains(snapshot.sync_scope?.resource_set, 'messages')
    && Object.prototype.hasOwnProperty.call(snapshot.messages_by_session ?? {}, sessionId)
  const hasAuthoritativeTombstone = Object.prototype.hasOwnProperty.call(snapshot.tombstones_by_session ?? {}, sessionId)
  return hasAuthoritativeMessages || hasAuthoritativeTombstone
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

function reconnectResourceSet(raw: SessionsReconnectResponse): Set<string> {
  const resources = new Set<string>()
  for (const workset of raw.worksets ?? []) {
    for (const resource of workset.resources ?? []) {
      const normalized = resource.trim()
      if (normalized) resources.add(normalized)
    }
  }
  return resources
}

function mergeReconnectOptionalResources(
  state: DesktopV3CacheState,
  raw: SessionsReconnectResponse,
  resources: Set<string>,
  authoritativeSessionIds: Set<string>,
): void {
  if (resources.has('active_plan')) {
    replaceRecordBySession(state.plansBySession, raw.plans_by_session, authoritativeSessionIds)
  } else {
    mergeRecord(state.plansBySession, raw.plans_by_session)
  }
  if (resources.has('plan_revisions')) {
    replaceRecordBySession(state.planRevisionsBySession, raw.plan_revisions_by_session, authoritativeSessionIds)
  } else {
    mergeRecord(state.planRevisionsBySession, raw.plan_revisions_by_session)
  }
  mergePermissionsBySession(state.permissionsBySession, raw.permissions_by_session)
  mergeUsageBySession(state, raw.usage_by_session)
  mergeRecord(state.preferencesBySession, raw.preferences_by_session)
  mergeRecord(state.agentModelPolicyBySession, raw.agent_model_policy_by_session)
  if (resources.has('messages') || resources.has('events')) {
    replaceRecordBySession(state.historyManifestsBySession, raw.history_manifests_by_session, authoritativeSessionIds)
    mergeRecord(state.historyChunksById, raw.history_chunks_by_id)
  } else {
    mergeRecord(state.historyManifestsBySession, raw.history_manifests_by_session)
    mergeRecord(state.historyChunksById, raw.history_chunks_by_id)
  }
}

function applyCurrentRunIntentsFromReconnect(
  state: DesktopV3CacheState,
  raw: SessionsReconnectResponse,
  authoritativeSessionIds: Set<string>,
): void {
  for (const sessionId of authoritativeSessionIds) {
    const explicit = raw.current_run_intent_by_session?.[sessionId]
    const derived = explicit ?? deriveCurrentRunIntent(raw.run_intents_by_session?.[sessionId] ?? [])
    if (derived) {
      state.currentRunIntentBySession[sessionId] = derived
    } else {
      delete state.currentRunIntentBySession[sessionId]
    }
  }
}

function deriveCurrentRunIntent(intents: V3SessionRunIntent[]): V3SessionRunIntent | undefined {
  return [...intents]
    .filter((intent) => ACTIVE_RUN_INTENT_STATUSES.has(intent.status.trim().toLowerCase()))
    .sort((left, right) =>
      right.updated_at - left.updated_at
      || right.event_seq - left.event_seq
      || right.run_id.localeCompare(left.run_id),
    )[0]
}

function mergeSnapshotResources(
  state: DesktopV3CacheState,
  snapshot: SyncSnapshotResponse,
  scopeId: string,
  requested?: Set<string>,
): void {
  const resourceSet = snapshot.sync_scope.resource_set
  const authoritativeSessionIds = requested ?? new Set(Object.keys(snapshot.sessions_by_id ?? {}))
  if (syncResourceSetContains(resourceSet, 'active_plan')) {
    replaceRecordBySession(state.plansBySession, snapshot.plans_by_session, authoritativeSessionIds)
  }
  if (syncResourceSetContains(resourceSet, 'plan_revisions')) {
    replaceRecordBySession(state.planRevisionsBySession, snapshot.plan_revisions_by_session, authoritativeSessionIds)
  }
  if (syncResourceSetContains(resourceSet, 'permissions')) {
    replacePermissionsBySession(state.permissionsBySession, snapshot.permissions_by_session, authoritativeSessionIds)
  }
  if (syncResourceSetContains(resourceSet, 'usage')) {
    replaceUsageBySession(state, snapshot.usage_by_session, authoritativeSessionIds)
  }
  if (syncResourceSetContains(resourceSet, 'preferences')) {
    replaceRecordBySession(state.preferencesBySession, snapshot.preferences_by_session, authoritativeSessionIds)
  }
  if (syncResourceSetContains(resourceSet, 'agent_model_policy')) {
    replaceRecordBySession(state.agentModelPolicyBySession, snapshot.agent_model_policy_by_session, authoritativeSessionIds)
  }
  if (syncResourceSetContains(resourceSet, 'messages') || syncResourceSetContains(resourceSet, 'events')) {
    replaceRecordBySession(state.historyManifestsBySession, snapshot.history_manifests_by_session, authoritativeSessionIds)
    mergeRecord(state.historyChunksById, snapshot.history_chunks_by_id)
  }
  if (snapshot.omissions !== undefined) state.omissionsByScope[scopeId] = snapshot.omissions
  if (snapshot.pagination !== undefined) state.paginationByScope[scopeId] = snapshot.pagination
  if (snapshot.watermarks !== undefined) state.watermarksByScope[scopeId] = snapshot.watermarks
}

function upsertSessions(state: DesktopV3CacheState, sessionsById: Record<string, SessionSnapshot> | undefined): void {
  if (!sessionsById) return
  for (const [sessionId, session] of Object.entries(sessionsById)) {
    const existing = state.sessionsById[sessionId]
    state.sessionsById[sessionId] = {
      kind: 'full',
      session,
      needsHydrate: existing?.needsHydrate ?? true,
    }
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

function replaceRunIntentsBySession(
  state: DesktopV3CacheState,
  runIntentsBySession: Record<string, V3SessionRunIntent[]> | undefined,
  authoritativeSessionIds: Set<string>,
): void {
  const scopedRunIntentsBySession = onlyRequested(runIntentsBySession, authoritativeSessionIds)

  for (const sessionId of authoritativeSessionIds) {
    replaceRunIntentsForSession(state, sessionId, scopedRunIntentsBySession?.[sessionId] ?? [])
  }
}

function replaceRunIntentsForSession(
  state: DesktopV3CacheState,
  sessionId: string,
  runIntents: V3SessionRunIntent[],
): void {
  delete state.runIntentsBySession[sessionId]
  delete state.currentRunIntentBySession[sessionId]

  const incomingRunIds = new Set(runIntents.map((intent) => intent.run_id))
  const liveRuns = state.liveRunsBySession[sessionId]
  if (liveRuns) {
    for (const runId of Object.keys(liveRuns)) {
      if (!incomingRunIds.has(runId) && !hasUncanonicalizedLiveState(liveRuns[runId])) {
        delete liveRuns[runId]
      }
    }
    if (Object.keys(liveRuns).length === 0) {
      delete state.liveRunsBySession[sessionId]
    }
  }

  for (const runIntent of runIntents) {
    upsertRunIntent(state, sessionId, runIntent)
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
    state.eventsBySession[sessionId] = sortEvents(dedupeEventsByIdentity(events))
  }
}

function mergeEventsBySessionFromSnapshot(state: DesktopV3CacheState, eventsBySession: Record<string, V3SessionEvent[]> | undefined): void {
  if (!eventsBySession) return
  for (const [sessionId, events] of Object.entries(eventsBySession)) {
    if (events.length > 0) mergeEventsForSession(state, sessionId, events)
  }
}

function mergeEventsForSession(state: DesktopV3CacheState, sessionId: string, incoming: V3SessionEvent[]): void {
  state.eventsBySession[sessionId] = sortEvents(dedupeEventsByIdentity([
    ...(state.eventsBySession[sessionId] ?? []),
    ...incoming,
  ]))
}

function dedupeEventsByIdentity(events: V3SessionEvent[]): V3SessionEvent[] {
  const deduped: V3SessionEvent[] = []
  const indexById = new Map<string, number>()
  const indexBySeq = new Map<string, number>()

  for (const event of events) {
    const id = event.id?.trim()
    const seqKey = eventSeqKey(event)
    const existingById = id ? indexById.get(id) : undefined
    const existingBySeq = indexBySeq.get(seqKey)
    const existingIndex = existingById ?? existingBySeq

    if (existingIndex !== undefined) {
      const previous = deduped[existingIndex]
      if (previous.id) indexById.delete(previous.id)
      indexBySeq.delete(eventSeqKey(previous))
      deduped[existingIndex] = event
      if (id) indexById.set(id, existingIndex)
      indexBySeq.set(seqKey, existingIndex)
      continue
    }

    const index = deduped.push(event) - 1
    if (id) indexById.set(id, index)
    indexBySeq.set(seqKey, index)
  }

  return deduped
}

function sortEvents(events: V3SessionEvent[]): V3SessionEvent[] {
  return [...events].sort((left, right) => left.seq - right.seq || left.id.localeCompare(right.id))
}

function applyScalarSessionPatchIfPresent(
  state: DesktopV3CacheState,
  sessionId: string,
  payload: SessionEventPayload,
  eventType: string,
): void {
  const record = state.sessionsById[sessionId]
  if (record?.kind !== 'full') return

  let nextSession = record.session
  if (typeof payload.title === 'string') nextSession = { ...nextSession, title: payload.title }
  if (typeof payload.mode === 'string') nextSession = { ...nextSession, mode: payload.mode }
  if (typeof payload.updated_at === 'number') nextSession = { ...nextSession, updated_at: payload.updated_at }
  const metadata = recordValue(payload.metadata)
  if (metadata) nextSession = { ...nextSession, metadata }
  if (payload.preference !== undefined) {
    nextSession = { ...nextSession, preference: payload.preference }
    state.preferencesBySession[sessionId] = payload.preference
  }
  if (nextSession !== record.session) {
    state.sessionsById[sessionId] = { ...record, session: nextSession }
  }
  if (payload.agent_model_policy !== undefined) {
    state.agentModelPolicyBySession[sessionId] = payload.agent_model_policy
  }
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

function finalizeLiveRunForCommittedMessage(
  state: DesktopV3CacheState,
  sessionId: string,
  message: MessageSnapshot,
  explicitRunId?: string,
  explicitRunStatus?: string,
): void {
  if (message.role !== 'assistant') return

  const runId = resolveCommittedMessageRunId(message, explicitRunId)
  if (!runId) return

  const runs = state.liveRunsBySession[sessionId]
  const run = runs?.[runId]
  if (!run) return

  delete run.assistantDraft
  delete run.assistantSegments

  const status = explicitRunStatus?.trim()
    || state.runIntentsBySession[sessionId]?.[runId]?.status
    || run.status

  cleanupTerminalLiveRunIfCanonicalized(state, sessionId, runId, status)
}

function reconcileLiveRunWithCommittedMessage(
  state: DesktopV3CacheState,
  sessionId: string,
  message: MessageSnapshot,
  explicitRunId?: string,
  explicitRunStatus?: string,
): void {
  const runId = resolveCommittedMessageRunId(message, explicitRunId)
  if (!runId) return

  const runs = state.liveRunsBySession[sessionId]
  const run = runs?.[runId]
  if (!run) return

  switch (message.role) {
    case 'assistant':
      delete run.assistantDraft
      delete run.assistantSegments
      break
    case 'tool':
      reconcileCommittedToolMessage(run, message)
      break
    case 'reasoning':
      reconcileCommittedReasoningMessage(run, message)
      break
    default:
      break
  }

  const status = explicitRunStatus?.trim()
    || state.runIntentsBySession[sessionId]?.[runId]?.status
    || run.status
  cleanupTerminalLiveRunIfCanonicalized(state, sessionId, runId, status)
}

function hasUncanonicalizedLiveState(run: LiveRunOverlay): boolean {
  return Boolean(run.assistantDraft?.content)
    || Boolean(run.assistantSegments?.some((segment) => segment.content))
    || Object.keys(run.toolCallsByCallId).length > 0
    || Boolean(run.reasoning)
    || Object.keys(run.reasoningByKey ?? {}).length > 0
}

function cleanupTerminalLiveRunIfCanonicalized(
  state: DesktopV3CacheState,
  sessionId: string,
  runId: string,
  status?: string,
): void {
  const runs = state.liveRunsBySession[sessionId]
  const run = runs?.[runId]
  if (!runs || !run) return
  if (status && TERMINAL_RUN_INTENT_STATUSES.has(status) && !hasUncanonicalizedLiveState(run)) {
    delete runs[runId]
  }
  if (Object.keys(runs).length === 0) {
    delete state.liveRunsBySession[sessionId]
  }
}

function reconcileCommittedToolMessage(run: LiveRunOverlay, message: MessageSnapshot): void {
  const metadata = message.metadata ?? {}
  const content = parseJsonRecord(message.content)
  const callId = stringFromMetadata(metadata, 'call_id')
    || stringFromMetadata(metadata, 'callId')
    || stringField(content?.call_id)
    || stringField(content?.callId)
  const toolInstanceId = stringFromMetadata(metadata, 'tool_instance_id')
    || stringFromMetadata(metadata, 'toolInstanceId')
    || stringField(content?.tool_instance_id)
    || stringField(content?.toolInstanceId)

  for (const [existingCallId, tool] of Object.entries(run.toolCallsByCallId)) {
    const callMatches = Boolean(callId) && tool.callId === callId
    const instanceMatches = Boolean(toolInstanceId) && tool.toolInstanceId === toolInstanceId
    if (callMatches || instanceMatches) {
      delete run.toolCallsByCallId[existingCallId]
    }
  }
}

function reconcileCommittedReasoningMessage(run: LiveRunOverlay, message: MessageSnapshot): void {
  const metadata = message.metadata ?? {}
  const key = stringFromMetadata(metadata, 'reasoning_overlay_key')
    || stringFromMetadata(metadata, 'reasoning_key')
    || stringFromMetadata(metadata, 'reasoningKey')
    || stringFromMetadata(metadata, 'reasoning_id')
    || stringFromMetadata(metadata, 'reasoningId')
  const normalizedContent = normalizeCommittedContent(message.content)
  const byKey = run.reasoningByKey ?? {}

  for (const [existingKey, reasoning] of Object.entries(byKey)) {
    const keyMatches = Boolean(key) && (
      existingKey === key
      || reasoning.key === key
      || reasoning.reasoningKey === key
      || reasoning.reasoningId === key
    )
    const contentMatches = normalizedContent !== '' && (
      normalizeCommittedContent(reasoning.text) === normalizedContent
      || normalizeCommittedContent(reasoning.summary) === normalizedContent
    )
    if (keyMatches || contentMatches) {
      delete byKey[existingKey]
    }
  }

  if (run.reasoning) {
    const activeKeyMatches = Boolean(key) && (
      run.reasoning.key === key
      || run.reasoning.reasoningKey === key
      || run.reasoning.reasoningId === key
    )
    const activeContentMatches = normalizedContent !== '' && (
      normalizeCommittedContent(run.reasoning.text) === normalizedContent
      || normalizeCommittedContent(run.reasoning.summary) === normalizedContent
    )
    if (activeKeyMatches || activeContentMatches) {
      delete run.reasoning
    }
  }

  if (Object.keys(byKey).length > 0) {
    run.reasoningByKey = byKey
    if (!run.reasoning) {
      run.reasoning = Object.values(byKey).sort((left, right) => (right.updatedSeq ?? 0) - (left.updatedSeq ?? 0))[0]
    }
  } else {
    delete run.reasoningByKey
    if (run.reasoning && key) {
      const activeKeyMatches = run.reasoning.key === key
        || run.reasoning.reasoningKey === key
        || run.reasoning.reasoningId === key
      if (activeKeyMatches) delete run.reasoning
    }
  }
}

function resolveCommittedMessageRunId(message: MessageSnapshot, explicitRunId?: string): string {
  const content = parseJsonRecord(message.content)
  return explicitRunId?.trim()
    || stringFromMetadata(message.metadata, 'run_id')
    || stringFromMetadata(message.metadata, 'runId')
    || stringField(content?.run_id)
    || stringField(content?.runId)
    || ''
}

function createLiveRunOverlay(sessionId: string, runId: string): LiveRunOverlay {
  return {
    sessionId,
    runId,
    status: 'pending_executor',
    toolCallsByCallId: {},
  }
}

export function resolveDesktopV3CacheEventRunId(event: CacheEvent): string {
  return event.payload.run_id?.trim()
    || event.payload.run_intent?.run_id?.trim()
    || stringFromMetadata(event.payload.message?.metadata, 'run_id')
    || stringFromMetadata(event.payload.message?.metadata, 'runId')
    || ''
}

function isLiveRunOutputEventType(eventType: string): boolean {
  switch (eventType) {
    case 'session.assistant.delta':
    case 'session.message.delta':
    case 'session.reasoning.started':
    case 'session.reasoning.delta':
    case 'session.reasoning.completed':
    case 'session.reasoning.failed':
    case 'session.reasoning.error':
    case 'session.tool.started':
    case 'session.tool.delta':
    case 'session.tool.completed':
    case 'session.tool.failed':
    case 'session.tool.cancelled':
    case 'session.tool.canceled':
      return true
    default:
      return false
  }
}

function mergeLiveRunRepairEvents(
  state: DesktopV3CacheState,
  sessionId: string,
  runId: string,
  events: CacheEvent[],
): void {
  for (const event of [...events].sort(
    (left, right) => (left.sessionEvent?.seq ?? 0) - (right.sessionEvent?.seq ?? 0),
  )) {
    if (event.sessionId !== sessionId) continue
    if (resolveDesktopV3CacheEventRunId(event) !== runId) continue
    applyCacheEvent(state, event)
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

  if (!isLiveRunOutputEventType(event.eventType)) {
    return
  }

  const liveRun = ensureLiveRunOverlay(state, sessionId, runId)
  const priorEventSeq = liveRun.lastEventSeqSeen ?? 0
  if (eventSeq > 0 && eventSeq <= priorEventSeq) {
    return
  }
  liveRun.lastEventSeqSeen = Math.max(priorEventSeq, eventSeq)

  switch (event.eventType) {
    case 'session.assistant.delta':
    case 'session.message.delta': {
      if (liveRun.reasoning?.state === 'running') {
        completeLiveReasoningOverlay(liveRun, updatedAt, eventSeq)
      }
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
        timelineSeq: liveRun.assistantDraft?.timelineSeq || eventSeq,
      }
      return
    }

    case 'session.reasoning.started':
    case 'session.reasoning.delta':
    case 'session.reasoning.completed':
    case 'session.reasoning.failed':
    case 'session.reasoning.error':
      if (liveRun.assistantDraft?.content) {
        flushLiveAssistantDraftToSegment(liveRun)
      }
      applyLiveReasoningOverlay(liveRun, payload, event.eventType, eventSeq, updatedAt)
      return

    case 'session.tool.started':
    case 'session.tool.delta':
    case 'session.tool.completed':
    case 'session.tool.failed':
    case 'session.tool.cancelled':
    case 'session.tool.canceled': {
      if (liveRun.assistantDraft?.content) {
        flushLiveAssistantDraftToSegment(liveRun)
      }
      const callId = stringValue(payload.call_id)
      if (!callId) {
        return
      }

      const isStarted = event.eventType === 'session.tool.started'
      const isDelta = event.eventType === 'session.tool.delta'
      const isFailed = event.eventType === 'session.tool.failed'
      const isCancelled = event.eventType === 'session.tool.cancelled' || event.eventType === 'session.tool.canceled'
      const isTerminal = event.eventType === 'session.tool.completed' || isFailed || isCancelled
      const tool = liveRun.toolCallsByCallId[callId] ?? {
        callId,
        createdAt: updatedAt,
        updatedAt,
      }

      tool.stepId = stringValue(payload.step_id) || tool.stepId
      tool.toolInstanceId =
        stringValue(payload.tool_instance_id) || tool.toolInstanceId
      tool.toolName = stringValue(payload.tool_name) || tool.toolName

      const argumentsText = stringValue(payload.arguments)
      const argumentsDelta = stringValue(payload.arguments_delta)
      const outputText = stringValue(payload.output)
      const outputDelta = stringValue(payload.output_delta) || stringValue(payload.delta)
      const rawOutput = stringValue(payload.raw_output)
      const completedOutput = stringValue(payload.completed_output)
      const errorText = stringValue(payload.error)

      if (isStarted && argumentsText) {
        tool.argumentsText = argumentsText
      } else if (argumentsDelta) {
        tool.argumentsText = `${tool.argumentsText ?? ''}${argumentsDelta}`
      } else if (!isDelta && argumentsText) {
        tool.argumentsText = argumentsText
      }

      if (rawOutput || completedOutput) {
        tool.outputText = rawOutput || completedOutput
      } else if (isTerminal && outputText) {
        tool.outputText = outputText
      } else if (outputDelta || (isDelta && outputText)) {
        tool.outputText = `${tool.outputText ?? ''}${outputDelta || outputText}`
      } else if (isStarted && outputText) {
        tool.outputText = outputText
      }

      tool.errorText = errorText || tool.errorText
      tool.durationMs = numberValue(payload.duration_ms) || tool.durationMs
      tool.status = stringValue(payload.status) || (isFailed ? 'failed' : isCancelled ? 'cancelled' : isTerminal ? 'completed' : 'running')
      tool.updatedAt = updatedAt
      tool.timelineSeq = tool.timelineSeq || eventSeq

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

function reasoningOverlayKey(payload: Record<string, unknown>): string {
  const reasoningId = stringValue(payload.reasoning_id).trim()
  if (reasoningId) return reasoningId
  const reasoningKey = stringValue(payload.reasoning_key).trim()
  const stepId = stringValue(payload.step_id).trim()
  if (stepId || reasoningKey) return `${stepId || 'step'}:${reasoningKey || 'reasoning'}`
  const step = numberValue(payload.step)
  return `step-${step > 0 ? step : 1}:reasoning`
}

function applyLiveReasoningOverlay(
  liveRun: LiveRunOverlay,
  payload: Record<string, unknown>,
  eventType: string,
  eventSeq: number,
  updatedAt: number,
): void {
  const key = reasoningOverlayKey(payload)
  const byKey = liveRun.reasoningByKey ?? {}
  const existing = byKey[key] ?? (liveRun.reasoning?.key === key ? liveRun.reasoning : undefined)
  const current: LiveRunReasoningOverlay = existing ?? {
    key,
    state: 'running',
    summary: '',
    text: '',
    startedAt: updatedAt,
    completedAt: null,
    updatedAt,
    timelineSeq: eventSeq,
    updatedSeq: eventSeq,
  }
  const isStarted = eventType === 'session.reasoning.started'
  const isCompleted = eventType === 'session.reasoning.completed'
  const isError = eventType === 'session.reasoning.failed' || eventType === 'session.reasoning.error'
  const textSnapshot = stringValue(payload.text) || stringValue(payload.delta)
  const textDelta = stringValue(payload.text_delta)
  const summarySnapshot = stringValue(payload.summary)
  const summaryDelta = stringValue(payload.summary_delta)
  const nextState: LiveRunReasoningOverlay['state'] = isError ? 'error' : isCompleted ? 'completed' : 'running'
  const next: LiveRunReasoningOverlay = {
    ...current,
    key,
    reasoningId: stringValue(payload.reasoning_id) || current.reasoningId,
    reasoningKey: stringValue(payload.reasoning_key) || current.reasoningKey,
    stepId: stringValue(payload.step_id) || current.stepId,
    step: numberValue(payload.step) || current.step,
    state: nextState,
    summary: summarySnapshot || (summaryDelta ? `${current.summary}${summaryDelta}` : current.summary),
    text: textSnapshot || (textDelta ? `${current.text}${textDelta}` : current.text),
    startedAt: current.startedAt ?? updatedAt,
    completedAt: isCompleted || isError ? updatedAt : current.completedAt ?? null,
    updatedAt,
    timelineSeq: current.timelineSeq || eventSeq,
    updatedSeq: eventSeq,
  }
  if (isStarted && existing) {
    next.text = existing.text
    next.summary = existing.summary
    next.state = existing.state === 'completed' || existing.state === 'error' ? existing.state : 'running'
    next.completedAt = existing.completedAt
  }
  byKey[key] = next
  liveRun.reasoningByKey = byKey
  liveRun.reasoning = next
  liveRun.status = liveRun.status === 'pending_executor' ? 'running' : liveRun.status
}

function completeLiveReasoningOverlay(liveRun: LiveRunOverlay, updatedAt: number, eventSeq: number): void {
  const current = liveRun.reasoning
  if (!current || current.state !== 'running') return
  const completed: LiveRunReasoningOverlay = {
    ...current,
    state: 'completed',
    completedAt: current.completedAt ?? updatedAt,
    updatedAt,
    updatedSeq: Math.max(current.updatedSeq ?? 0, eventSeq),
  }
  liveRun.reasoning = completed
  if (current.key) {
    liveRun.reasoningByKey = { ...(liveRun.reasoningByKey ?? {}), [current.key]: completed }
  }
}

function appendLiveAssistantOverlaySegment(
  liveRun: LiveRunOverlay,
  draft: NonNullable<LiveRunOverlay['assistantDraft']>,
): NonNullable<LiveRunOverlay['assistantSegments']> {
  const content = draft.content.trim()
  if (!content) return liveRun.assistantSegments ?? []
  const timelineSeq = draft.timelineSeq ?? liveRun.lastEventSeqSeen ?? 0
  const createdAt = draft.updatedAt || Date.now()
  return [
    ...(liveRun.assistantSegments ?? []),
    {
      id: `live-assistant:${liveRun.runId}:${timelineSeq}:${liveRun.assistantSegments?.length ?? 0}`,
      content,
      createdAt,
      updatedAt: draft.updatedAt,
      timelineSeq,
    },
  ]
}

function flushLiveAssistantDraftToSegment(liveRun: LiveRunOverlay): void {
  const draft = liveRun.assistantDraft
  if (!draft?.content.trim()) return
  liveRun.assistantSegments = appendLiveAssistantOverlaySegment(liveRun, draft)
  delete liveRun.assistantDraft
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

function mergeRecord<T>(target: Record<string, T>, source: Record<string, T | undefined> | undefined): void {
  if (!source) return
  for (const [key, value] of Object.entries(source)) {
    target[key] = value as T
  }
}

function mergeUsageBySession(state: DesktopV3CacheState, source: Record<string, unknown> | undefined): void {
  if (!source) return
  for (const [sessionId, value] of Object.entries(source)) {
    applyUsageSummaryFromUnknown(state, sessionId, value)
  }
}

function replaceUsageBySession(state: DesktopV3CacheState, source: Record<string, unknown> | undefined, sessionIds: Set<string>): void {
  for (const sessionId of sessionIds) {
    delete state.usageBySession[sessionId]
  }
  mergeUsageBySession(state, onlyRequested(source, sessionIds))
}

function applyUsageSummaryFromEventPayload(
  state: DesktopV3CacheState,
  fallbackSessionId: string,
  payload: SessionEventPayload,
): void {
  applyUsageSummaryFromUnknown(state, fallbackSessionId, payload.usage_summary)
  applyUsageSummaryFromUnknown(state, fallbackSessionId, recordValue(payload)?.usage_state)
}

function applyUsageSummaryFromUnknown(
  state: DesktopV3CacheState,
  fallbackSessionId: string,
  value: unknown,
): void {
  const summary = recordValue(value)
  if (!summary) return
  const sessionId = stringField(summary.session_id)
    || stringField(summary.sessionId)
    || fallbackSessionId.trim()
  if (!sessionId) return

  const existing = recordValue(state.usageBySession[sessionId])
  const incomingUpdatedAt = usageUpdatedAt(summary)
  const existingUpdatedAt = usageUpdatedAt(existing)
  if (existingUpdatedAt > 0 && incomingUpdatedAt > 0 && incomingUpdatedAt < existingUpdatedAt) {
    return
  }

  state.usageBySession[sessionId] = summary
}

function applyUsageContextBaselineFromSettings(
  state: DesktopV3CacheState,
  sessionId: string,
  raw: SessionSettingsMutationResponse,
): void {
  const contextWindow = usageNumber(raw.context_window)
    || usageNumber(recordValue(raw.agent_model_policy)?.context_window)
  if (contextWindow <= 0) return

  const existing = recordValue(state.usageBySession[sessionId]) ?? {}
  const preference = recordValue(raw.preference)
    ?? recordValue(recordValue(raw.agent_model_policy)?.preference)
    ?? recordValue(state.preferencesBySession[sessionId])
  const provider = stringValue(preference?.provider).trim() || stringValue(existing.provider).trim()
  const model = stringValue(preference?.model).trim() || stringValue(existing.model).trim()
  const totalTokens = usageNumber(existing.total_tokens ?? existing.totalTokens)
  const updatedAt = Math.max(
    usageUpdatedAt(existing),
    usageNumber(preference?.updated_at ?? preference?.updatedAt),
  )

  state.usageBySession[sessionId] = {
    ...existing,
    session_id: sessionId,
    provider,
    model,
    source: stringValue(existing.source).trim() || 'settings_mutation',
    context_window: contextWindow,
    total_tokens: totalTokens,
    remaining_tokens: Math.max(0, contextWindow - totalTokens),
    updated_at: updatedAt,
  }
}

function usageUpdatedAt(value: Record<string, unknown> | undefined): number {
  return usageNumber(value?.updated_at ?? value?.updatedAt)
}

function usageNumber(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(0, value) : 0
}

function replaceRecordBySession<T>(target: Record<string, T>, source: Record<string, T | undefined> | undefined, sessionIds: Set<string>): void {
  for (const sessionId of sessionIds) {
    delete target[sessionId]
  }
  mergeRecord(target, onlyRequested(source, sessionIds))
}

function messageGlobalSeqKey(message: MessageSnapshot): string {
  return `${message.session_id}:${message.global_seq}`
}

function resolveCommittedReasoningMessageSeq(
  state: DesktopV3CacheState,
  sessionId: string,
  messageId: string,
  preferredSeq: number,
): number {
  const list = state.messagesBySession[sessionId]
  const existingIndex = list?.byMessageId[messageId]
  if (existingIndex !== undefined) return list.items[existingIndex].global_seq

  const safePreferredSeq = preferredSeq > 0 ? preferredSeq : 1
  const preferredKey = `${sessionId}:${safePreferredSeq}`
  const conflictingIndex = list?.byGlobalSeq[preferredKey]
  if (conflictingIndex === undefined || list?.items[conflictingIndex]?.id === messageId) {
    return safePreferredSeq
  }

  return Math.max(safePreferredSeq, ...(list?.items.map((message) => message.global_seq) ?? [0])) + 1
}

function eventSeqKey(event: V3SessionEvent): string {
  return `${event.session_id}:${event.seq}`
}

function appendUnique(values: string[], value: string): string[] {
  return values.includes(value) ? values : [...values, value]
}

function prependUnique(items: string[], value: string): string[] {
  return [value, ...items.filter((item) => item !== value)]
}

function projectionSeq(projection: V3SessionProjection | undefined): number {
  return Math.max(
    projection?.last_event_seq ?? 0,
    projection?.projection_high_watermark_seq ?? 0,
  )
}

function hasOwn<T>(record: Record<string, T> | undefined, key: string): boolean {
  return Object.prototype.hasOwnProperty.call(record ?? {}, key)
}

function stringField(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() ? value : undefined
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function numberValue(value: unknown): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : 0
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as Record<string, unknown> : undefined
}

function stringFromMetadata(metadata: Record<string, unknown> | undefined, key: string): string | undefined {
  const value = metadata?.[key]
  return typeof value === 'string' && value.trim() ? value : undefined
}

function parseJsonRecord(value: string): Record<string, unknown> | undefined {
  try {
    const parsed = JSON.parse(value)
    return recordValue(parsed)
  } catch {
    return undefined
  }
}

function normalizeCommittedContent(value: string): string {
  return value.trim().replace(/\s+/g, ' ')
}

function subscriptionId(subscription: SubscriptionCache | Record<string, unknown>): string | undefined {
  return stringField(subscription.subscription_id) || stringField(subscription.subscriptionId)
}

function worksetId(workset: WorksetCache | Record<string, unknown>): string | undefined {
  return stringField(workset.workset_id) || stringField(workset.worksetId)
}

export function payloadFromEventPayload(payload: unknown): SessionEventPayload {
  return decodeSessionEventPayload({ id: '', session_id: '', seq: 0, event_type: '', payload, ts_unix_ms: 0 })
}
