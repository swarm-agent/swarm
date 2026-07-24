import type {
  CacheEvent,
  DesktopNotificationSummaryWire,
  DesktopNotificationWire,
  DesktopPermissionSummary,
  DesktopV3CacheAction,
  DesktopV3CacheState,
  LiveRunOverlay,
  LiveRunReasoningOverlay,
  MessageListCache,
  MessageSnapshot,
  PendingUserMessage,
  RealtimeMessage,
  SessionEventPayload,
  SessionArchiveMutationResponse,
  SessionCreateMutationResponse,
  SessionMessageMutationResponse,
  SessionMutationErrorResponse,
  SessionSettingsMutationResponse,
  SessionsReconnectResponse,
  SubscriptionCache,
  SyncSnapshotResponse,
  V3ExecutionEpoch,
  V3SessionEvent,
  V3SessionProjection,
  V3SessionRunIntent,
  V3SessionTombstone,
  WorksetCache,
  SessionSnapshot,
  MessageMutationConflictResponse,
} from './desktop-v3-cache-types'
import type { DesktopNotificationCenterRecord, DesktopNotificationSummary, DesktopPermissionRecord } from '../types/realtime'
import type { SessionV3RealtimeLivePatchWire } from '../session-v3/types'
import { desktopPermissionIdentity, normalizeDesktopPermission, normalizeDesktopPendingPermissions, normalizeDesktopPermissionSummary, normalizeDesktopPermissionSummaries, safeString } from '../permissions/services/desktop-permission-normalization'
import { normalizeDesktopSessionPlan } from '../chat/services/session-plan-record'
import { decodeSessionEventPayload, normalizeRealtimeEventFrame } from './desktop-v3-cache-wire'
import { isDesktopV3NavigationHiddenRecord } from './desktop-v3-session-visibility'
import { mergeWorkspaceAITaskMonotonic } from '../../workspaces/todos/ai-task-reconciliation'
import type { WorkspaceTodoAIState, WorkspaceTodoItem } from '../../workspaces/todos/types'

export const DESKTOP_V3_MAX_RETAINED_BACKGROUND_TRANSCRIPTS = 5

const ACTIVE_RUN_INTENT_STATUSES = new Set(['pending_executor', 'running', 'dispatch_blocked'])
const TERMINAL_RUN_INTENT_STATUSES = new Set(['completed', 'failed', 'cancelled', 'interrupted', 'expired'])
const EMPTY_NOTIFICATION_SUMMARY: DesktopNotificationSummary = {
  accountScopeID: null,
  swarmID: '',
  totalCount: 0,
  unreadCount: 0,
  activeCount: 0,
  updatedAt: 0,
}
const utf8Encoder = new TextEncoder()


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
    sessionViewsById: {},
    tombstonesBySession: {},
    messagesBySession: {},
    eventsBySession: {},
    hydrateInFlightBySession: {},
    evictedTranscriptsBySession: {},
    runIntentsBySession: {},
    currentRunIntentBySession: {},
    currentExecutionEpochBySession: {},
    pendingUserByClientRequestId: {},
    liveRunsBySession: {},
    subscriptionsById: {},
    worksetsById: {},
    plansBySession: {},
    hasActivePlanBySession: {},
    planRevisionsBySession: {},
    permissionsBySession: {},
    permissionSummaryBySessionId: {},
    notificationsById: {},
    notificationSummary: { ...EMPTY_NOTIFICATION_SUMMARY },
    aiTasksById: {},
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
      touchSessionTranscript(state, state.selectedSessionId)
      enforceHydratedTranscriptRetention(state)
      return state
    case 'desktopV3Cache.applyHydrationPlan':
      return applyHydrationPlan(state, action.reusedSessionIds, action.hydrateSessionIds)
    case 'desktopV3Cache.markHydrateInFlight':
      markHydrateInFlight(state, action.sessionIds, action.inFlight)
      enforceHydratedTranscriptRetention(state)
      return state
    case 'snapshot.apply':
      return applyBootstrapSnapshot(state, action.snapshot)
    case 'hydrate.apply':
      return applyHydrateSnapshot(state, action.snapshot, action.requestedSessionIds)
    case 'messages.prependHistoryResult':
      prependHistoricalMessagesForSession(state, action.sessionId, action.messages, {
        sourceMessageCount: action.sourceMessageCount,
        knownFull: action.knownFull,
      })
      enforceHydratedTranscriptRetention(state)
      return state
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
    case 'realtime.applyNotificationResource':
      applyNotificationResourceFrame(state, action.frame)
      if (action.frame.endpoint_cursor) {
        state.realtime.endpointCursor = action.frame.endpoint_cursor
      }
      return state
    case 'realtime.applyAITaskResource':
      applyAITaskResourceFrame(state, action.frame)
      if (action.frame.endpoint_cursor) {
        state.realtime.endpointCursor = action.frame.endpoint_cursor
      }
      return state
    case 'aiTasks.mergeItems':
      mergeAITaskItems(state, action.items)
      return state
    case 'realtime.applyLivePatchBatch':
      return applyDesktopV3LivePatchBatch(state, action.patches)
    case 'permission.resolveResult':
      if (action.permission) {
        upsertPermissionRecord(state, action.permission)
      } else {
        removePermissionRecord(state, action.sessionId, action.permissionId)
      }
      return state
    case 'planSnapshot.apply':
      state.hasActivePlanBySession[action.sessionId] = action.hasActivePlan
      state.plansBySession[action.sessionId] = action.activePlan
      state.planRevisionsBySession[action.sessionId] = action.planRevisions
      return state
    case 'liveRun.mergeRepairEvents':
      mergeLiveRunRepairEvents(state, action.sessionId, action.runId, action.events)
      return state
    case 'realtime.worksetSessionDiscovered':
      applyWorksetSessionDiscovered(state, action.frame)
      return state
    case 'realtime.worksetSessionUpdated':
      applyWorksetSessionUpdated(state, action.frame)
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
    case 'mutation.sessionArchiveResult':
      return applySessionArchiveMutationResult(state, action.raw)
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
    touchSessionTranscript(state, sessionId)
  }
  for (const sessionId of hydrateSessionIds) {
    const record = state.sessionsById[sessionId]
    if (record) record.needsHydrate = true
  }
  markHydrateInFlight(state, hydrateSessionIds, true)
  enforceHydratedTranscriptRetention(state)
  return state
}

function markHydrateInFlight(
  state: DesktopV3CacheState,
  sessionIds: string[],
  inFlight: boolean,
): void {
  state.hydrateInFlightBySession ??= {}
  state.evictedTranscriptsBySession ??= {}
  const now = Date.now()
  for (const rawSessionId of sessionIds) {
    const sessionId = rawSessionId.trim()
    if (!sessionId) continue
    if (inFlight) {
      state.hydrateInFlightBySession[sessionId] = now
      delete state.evictedTranscriptsBySession[sessionId]
    } else {
      delete state.hydrateInFlightBySession[sessionId]
    }
  }
}

function touchSessionTranscript(state: DesktopV3CacheState, sessionId: string | undefined): void {
  const normalized = sessionId?.trim()
  if (!normalized) return
  const list = state.messagesBySession[normalized]
  if (!list) return
  state.messagesBySession[normalized] = buildMessageListCache(list.items, {
    knownTail: list.knownTail,
    knownFull: list.knownFull,
    sourceMessageCount: list.sourceMessageCount,
    sourceLastMessageAt: list.sourceLastMessageAt,
    sourceProjectionHighWatermarkSeq: list.sourceProjectionHighWatermarkSeq,
    oldestLoadedSeq: list.oldestLoadedSeq,
    hydratedAt: list.hydratedAt,
    tailHydratedAt: list.tailHydratedAt,
    lastAccessedAt: Date.now(),
    source: list.source,
  })
}

function enforceHydratedTranscriptRetention(state: DesktopV3CacheState): void {
  state.hydrateInFlightBySession ??= {}
  state.evictedTranscriptsBySession ??= {}
  const protectedSessionIds = new Set<string>()
  const selectedSessionId = state.selectedSessionId?.trim()
  if (selectedSessionId) protectedSessionIds.add(selectedSessionId)
  for (const sessionId of Object.keys(state.hydrateInFlightBySession)) {
    protectedSessionIds.add(sessionId)
  }

  const candidates = Object.entries(state.messagesBySession)
    .filter(([sessionId, list]) => isHydratedTranscript(list) && !protectedSessionIds.has(sessionId))
    .map(([sessionId, list]) => ({
      sessionId,
      lastAccessedAt: transcriptLastAccessedAt(list),
    }))
    .sort((left, right) => left.lastAccessedAt - right.lastAccessedAt || left.sessionId.localeCompare(right.sessionId))

  const overflow = candidates.length - DESKTOP_V3_MAX_RETAINED_BACKGROUND_TRANSCRIPTS
  if (overflow <= 0) return

  const now = Date.now()
  for (const candidate of candidates.slice(0, overflow)) {
    evictTranscriptHistory(state, candidate.sessionId, now)
  }
}

function evictTranscriptHistory(state: DesktopV3CacheState, sessionId: string, evictedAt: number): void {
  if (state.selectedSessionId?.trim() === sessionId) return
  if (state.hydrateInFlightBySession?.[sessionId] !== undefined) return

  delete state.messagesBySession[sessionId]
  delete state.eventsBySession[sessionId]
  evictHistoryChunksForSession(state, sessionId)
  delete state.historyManifestsBySession[sessionId]
  state.evictedTranscriptsBySession ??= {}
  state.evictedTranscriptsBySession[sessionId] = evictedAt

  const record = state.sessionsById[sessionId]
  if (record?.kind === 'full') record.needsHydrate = true
}

function evictHistoryChunksForSession(state: DesktopV3CacheState, sessionId: string): void {
  const manifests = state.historyManifestsBySession[sessionId]
  for (const chunkId of historyChunkIds(manifests)) {
    delete state.historyChunksById[chunkId]
  }
}

function historyChunkIds(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  const chunkIds: string[] = []
  for (const entry of value) {
    if (!entry || typeof entry !== 'object') continue
    const chunkId = (entry as { chunk_id?: unknown; chunkId?: unknown }).chunk_id
      ?? (entry as { chunk_id?: unknown; chunkId?: unknown }).chunkId
    if (typeof chunkId === 'string' && chunkId.trim()) chunkIds.push(chunkId.trim())
  }
  return chunkIds
}

function hydrateResponseCanApplyHistory(state: DesktopV3CacheState, sessionId: string): boolean {
  if (!state.evictedTranscriptsBySession?.[sessionId]) return true
  if (state.selectedSessionId?.trim() === sessionId) return true
  return state.hydrateInFlightBySession?.[sessionId] !== undefined
}

function isHydratedTranscript(list: MessageListCache | undefined): boolean {
  return Boolean(list?.knownFull)
    || Boolean(list?.knownTail)
    || Number.isSafeInteger(list?.tailHydratedAt)
}

function transcriptLastAccessedAt(list: MessageListCache): number {
  return list.lastAccessedAt
    ?? list.hydratedAt
    ?? list.tailHydratedAt
    ?? 0
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
  state.sessionOrderByScope[snapshot.scope_id] = navigationVisibleSessionIds(state, removeTombstonedIds(state, snapshot.session_order ?? []))
  applyTombstonesBySession(state, snapshot.tombstones_by_session)
  if (syncResourceSetContains(snapshot.sync_scope.resource_set, 'run_intents')) {
    const authoritativeRunIntentSessionIds = new Set([
      ...Object.keys(snapshot.sessions_by_id ?? {}),
      ...Object.keys(snapshot.tombstones_by_session ?? {}),
    ])
    replaceRunIntentsBySession(state, snapshot.run_intents_by_session, authoritativeRunIntentSessionIds)
  }
  mergeSnapshotResources(state, snapshot, snapshot.scope_id)
  applyNotificationsFromSyncSnapshot(state, snapshot)
  applyAITasksFromSyncSnapshot(state, snapshot)
  applyPermissionSummariesFromSyncSnapshot(state, snapshot)
  if (syncResourceSetContains(snapshot.sync_scope.resource_set, 'messages')) {
    applyMessagesBySessionFromSnapshot(state, snapshot.messages_by_session)
  }
  if (syncResourceSetContains(snapshot.sync_scope.resource_set, 'events')) {
    applyEventsBySessionFromSnapshot(state, snapshot.events_by_session)
  }
  applyCurrentRunStateFromSyncSnapshot(state, snapshot)
  applySessionViewsFromSyncSnapshot(state, snapshot, new Set(Object.keys(snapshot.sessions_by_id ?? {})))
  for (const sessionId of snapshot.session_order ?? []) {
    const record = state.sessionsById[sessionId]
    if (record?.kind === 'full' && hydrateResponseCompletesSession(snapshot, sessionId)) {
      record.needsHydrate = false
    }
  }

  enforceHydratedTranscriptRetention(state)

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
  assertMapSubset(snapshot.current_run_state_by_session, requested, 'current_run_state_by_session')
  assertMapSubset(snapshot.permission_summaries_by_session, requested, 'permission_summaries_by_session')
  assertMapSubset(snapshot.session_views_by_id, requested, 'session_views_by_id')
  assertObjectKeysSubset('tombstones_by_session', snapshot.tombstones_by_session, requested)
  assertObjectKeysSubset('history_manifests_by_session', snapshot.history_manifests_by_session, requested)

  requireProtocol(snapshot.scope_id, 'snapshot.scope_id')
  requireProtocol(snapshot.sync_scope, 'snapshot.sync_scope')
  requireProtocol(snapshot.snapshot_endpoint_cursor, 'snapshot.snapshot_endpoint_cursor')
  const preHydrateProjections = { ...state.projectionsBySession }
  const preHydrateSessions = { ...state.sessionsById }
  writeSyncScope(state, snapshot)
  applyHydrateSessionsAndProjections(state, snapshot, requested, preHydrateProjections, preHydrateSessions)
  for (const sessionId of requested) {
    if (hasOwn(snapshot.sessions_by_id, sessionId)
      && !hasOwn(snapshot.tombstones_by_session, sessionId)
      && hydrateProjectionIsFresh(snapshot, sessionId, preHydrateProjections)) {
      delete state.tombstonesBySession[sessionId]
      restoreSessionToSidebar(state, sessionId)
    }
  }
  applyTombstonesBySession(state, snapshot.tombstones_by_session)
  applyHydrateAuthoritativeResources(state, snapshot, requested, preHydrateProjections)

  const sidebarScopeId = state.desktopSidebarBootstrap.scopeId
  for (const sessionId of requested) {
    const record = state.sessionsById[sessionId]
    if (record?.kind === 'full'
      && hydrateResponseCompletesSession(snapshot, sessionId)
      && hydrateResponseCanApplyHistory(state, sessionId)) {
      record.needsHydrate = false
    }
    if (sidebarScopeId && record?.kind === 'full' && !isDesktopV3NavigationHiddenRecord(record) && !state.tombstonesBySession[sessionId]) {
      state.sessionOrderByScope[sidebarScopeId] = prependUnique(state.sessionOrderByScope[sidebarScopeId] ?? [], sessionId)
      const workset = state.worksetsById[sidebarScopeId]
      if (workset) {
        workset.sessionIds = prependUnique(workset.sessionIds ?? [], sessionId)
        workset.inactiveSessionIds = (workset.inactiveSessionIds ?? []).filter((id) => id !== sessionId)
      }
    }
  }

  markHydrateInFlight(state, action.requestedSessionIds, false)
  enforceHydratedTranscriptRetention(state)

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
    const existingSession = preHydrateSessions[sessionId]
    if (incomingSession && (fresh || !existingSession || existingSession.kind === 'stub')) {
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
      if (!hydrateResponseCanApplyHistory(state, sessionId)) continue
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
      if (!hydrateResponseCanApplyHistory(state, sessionId)) continue
      const incoming = snapshot.events_by_session?.[sessionId] ?? []
      replayDurableEventsForSession(state, sessionId, incoming)
      if (hydrateProjectionIsFresh(snapshot, sessionId, preHydrateProjections)) {
        replaceEventsForSession(state, sessionId, incoming)
      } else {
        mergeEventsForSession(state, sessionId, incoming)
      }
    }
  }

  enforceHydratedTranscriptRetention(state)
  applyPermissionSummariesFromSyncSnapshot(state, snapshot, requested)
  applyNotificationsFromSyncSnapshot(state, snapshot)
  applySessionViewsFromSyncSnapshot(state, snapshot, requested)

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
  applyCurrentRunStateFromSyncSnapshot(state, snapshot, requested)
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

function filterHydrateHistorySessionIds(state: DesktopV3CacheState, sessionIds: Set<string>): Set<string> {
  const filtered = new Set<string>()
  for (const sessionId of sessionIds) {
    if (hydrateResponseCanApplyHistory(state, sessionId)) filtered.add(sessionId)
  }
  return filtered
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
    ...Object.keys(raw.current_run_state_by_session ?? {}),
    ...Object.keys(raw.permission_summaries_by_session ?? {}),
    ...Object.keys(raw.run_intents_by_session ?? {}),
  ])

  upsertSessions(state, raw.sessions_by_id)
  mergeRecord(state.projectionsBySession, raw.projections_by_session)

  if (resources.has('run_intents')) {
    replaceRunIntentsBySession(state, raw.run_intents_by_session, authoritativeSessionIds)
    if (resources.has('current_run_state')) {
      applyCurrentRunStateFromReconnect(state, raw, authoritativeSessionIds)
    } else {
      applyCurrentRunIntentsFromReconnect(state, raw, authoritativeSessionIds)
    }
  } else if (resources.has('current_run_state')) {
    applyCurrentRunStateFromReconnect(state, raw, authoritativeSessionIds)
  } else {
    mergeRunIntentsBySession(state, raw.run_intents_by_session)
    mergeRecord(state.currentRunIntentBySession, raw.current_run_intent_by_session)
  }

  mergeReconnectOptionalResources(state, raw, resources, authoritativeSessionIds)
  if (resources.has('permission_summaries')) {
    applyPermissionSummaries(state, raw.permission_summaries_by_session, new Set([
      ...authoritativeSessionIds,
      ...Object.keys(state.permissionSummaryBySessionId ?? {}),
    ]))
  }
  if (resources.has('notifications') || resources.has('notification_summary')) {
    applyNotificationsFromResourcePayload(state, raw.notifications, raw.notification_summary, {
      replaceNotifications: resources.has('notifications'),
      replaceSummary: resources.has('notification_summary'),
    })
  }
  if (resources.has('tasks')) {
    mergeAITaskWireItems(state, raw.tasks)
  }
  if (resources.has('active_plan')) {
    applySessionViews(state, raw.session_views_by_id, authoritativeSessionIds, { clearMissing: false })
  }
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
    if (id) {
      state.worksetsById[id] = { ...state.worksetsById[id], ...workset }
      state.worksetsById[id].sessionIds = navigationVisibleSessionIds(state, state.worksetsById[id].sessionIds ?? [])
      state.worksetsById[id].inactiveSessionIds = navigationVisibleSessionIds(state, state.worksetsById[id].inactiveSessionIds ?? [])
    }
  }

  if (raw.workset_id) {
    const visibleSessionOrder = navigationVisibleSessionIds(state, raw.session_order ?? [])
    state.sessionOrderByScope[raw.workset_id] = visibleSessionOrder
    state.worksetsById[raw.workset_id] = {
      ...state.worksetsById[raw.workset_id],
      workset_id: raw.workset_id,
      sessionIds: visibleSessionOrder,
    }
  }

  if (raw.realtime?.resume) {
    state.realtime.resumeFrame = raw.realtime.resume
    state.realtime.streamPath = raw.realtime.stream_path
  }

  enforceHydratedTranscriptRetention(state)

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

    case 'notification.resource.updated':
      applyNotificationResourceFrame(state, frame)
      state.realtime.endpointCursor = frame.endpoint_cursor
      return state

    case 'task.lifecycle.updated':
      applyAITaskResourceFrame(state, frame)
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

    case 'workset.session.updated':
      applyWorksetSessionUpdated(state, frame)
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

  const message = messageFromMutationResponse(raw)
  const runIntent = runIntentFromMutationResponse(raw)
  if (message) {
    upsertCommittedMessage(state, raw.session_id || message.session_id, message)
  }
  const sessionId = raw.session_id || message?.session_id || runIntent?.session_id || ''
  const reactivatingArchivedSession = Boolean(sessionId && state.tombstonesBySession[sessionId]?.archived === true)
  if (sessionId && raw.session) {
    state.sessionsById[sessionId] = {
      kind: 'full',
      session: raw.session,
      needsHydrate: false,
    }
    delete state.tombstonesBySession[sessionId]
    if (reactivatingArchivedSession) restoreSessionToSidebar(state, sessionId)
  }
  if (sessionId && raw.projection) {
    state.projectionsBySession[sessionId] = raw.projection
  }
  applyUsageSummaryFromUnknown(state, sessionId, raw.usage_summary)
  applyUsageSummaryFromUnknown(state, sessionId, recordValue(raw.mutation)?.usage_summary)
  delete state.pendingUserByClientRequestId[clientRequestId]
  for (const [pendingClientRequestId, pending] of Object.entries(state.pendingUserByClientRequestId)) {
    if (pending.messageId === messageId) {
      delete state.pendingUserByClientRequestId[pendingClientRequestId]
    }
  }
  if (raw.current_run_state?.run_id?.trim()) {
    applyCurrentRunStateFrame(state, sessionId || raw.current_run_state.session_id || raw.session_id, raw.current_run_state)
  } else if (runIntent) {
    upsertRunIntent(state, runIntent.session_id || raw.session_id, runIntent)
  }
  return state
}

function messageFromMutationResponse(raw: SessionMessageMutationResponse): MessageSnapshot | undefined {
  const message = recordValue(raw.message)
  if (!message) return undefined
  return message as unknown as MessageSnapshot
}

function runIntentFromMutationResponse(raw: SessionMessageMutationResponse): V3SessionRunIntent | undefined {
  return raw.run_intent ?? undefined
}

export function applySessionArchiveMutationResult(
  state: DesktopV3CacheState,
  raw: SessionArchiveMutationResponse,
): DesktopV3CacheState {
  if (raw.ok === false) return state
  const tombstonesBySession: Record<string, V3SessionTombstone> = {}
  for (const result of raw.results ?? []) {
    if (result?.archived !== true) continue
    const sessionId = stringField(result.session_id) || stringField(recordValue(result.tombstone)?.session_id)
    if (!sessionId) continue
    tombstonesBySession[sessionId] = archiveTombstoneFromMutationResult(state, sessionId, result.tombstone)
  }
  applyTombstonesBySession(state, tombstonesBySession)
  return state
}

function archiveTombstoneFromMutationResult(
  state: DesktopV3CacheState,
  sessionId: string,
  rawTombstone: unknown,
): V3SessionTombstone {
  const tombstoneRecord = recordValue(rawTombstone)
  const tombstone: V3SessionTombstone = {
    ...(tombstoneRecord ?? {}),
    session_id: stringField(tombstoneRecord?.session_id) || sessionId,
    kind: stringField(tombstoneRecord?.kind) || 'archived',
    archived: true,
  }
  if (tombstoneRecord?.deleted !== true) {
    tombstone.deleted = false
  }
  return tombstoneWithRetainedSession(state, sessionId, tombstone)
}

function tombstoneWithRetainedSession(
  state: DesktopV3CacheState,
  sessionId: string,
  tombstone: V3SessionTombstone,
): V3SessionTombstone {
  if (tombstone.session) return tombstone
  if (tombstone.kind !== 'archived' || tombstone.archived !== true || tombstone.deleted === true) return tombstone
  const cachedSession = state.sessionsById[sessionId]
  if (cachedSession?.kind !== 'full') return tombstone
  return { ...tombstone, session: cachedSession.session }
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
    runId?: string
    createdAt: number
  },
): DesktopV3CacheState {
  const projection = state.projectionsBySession[input.sessionId]
  const latestProjectionSeq = Math.max(
    projection?.last_event_seq ?? 0,
    projection?.projection_high_watermark_seq ?? 0,
  )
  const latestCommittedSeq = state.messagesBySession[input.sessionId]?.items.reduce(
    (latest, message) => Math.max(latest, message.global_seq ?? 0),
    0,
  ) ?? 0
  const latestLiveSeq = Object.values(state.liveRunsBySession[input.sessionId] ?? {}).reduce(
    (latest, run) => Math.max(
      latest,
      run.lastEventSeqSeen ?? 0,
      run.assistantDraft?.timelineSeq ?? 0,
      ...(run.assistantSegments ?? []).map((segment) => segment.timelineSeq ?? 0),
      ...Object.values(run.toolCallsByCallId).map((tool) => tool.timelineSeq ?? 0),
      ...Object.values(run.reasoningByKey ?? {}).map((reasoning) => reasoning.timelineSeq ?? 0),
      run.reasoning?.timelineSeq ?? 0,
    ),
    0,
  )
  const latestPendingSeq = Object.values(state.pendingUserByClientRequestId).reduce(
    (latest, message) => message.sessionId === input.sessionId
      ? Math.max(latest, message.timelineSeq ?? 0)
      : latest,
    0,
  )
  const pending: PendingUserMessage = {
    clientRequestId: input.clientRequestId,
    messageId: input.messageId,
    sessionId: input.sessionId,
    role: 'user',
    content: input.content,
    metadata: input.metadata,
    runId: input.runId?.trim() || undefined,
    createdAt: input.createdAt,
    timelineSeq: Math.max(
      latestProjectionSeq,
      latestCommittedSeq,
      latestLiveSeq,
      latestPendingSeq,
    ) + 1,
    status: 'pending',
  }
  state.pendingUserByClientRequestId[input.clientRequestId] = pending
  if (pending.runId) {
    applyPendingUserRunTimelineFloor(state, pending.sessionId, pending.runId, (pending.timelineSeq ?? 0) + 1)
  }
  return state
}

function applyPendingUserRunTimelineFloor(
  state: DesktopV3CacheState,
  sessionId: string,
  runId: string,
  timelineFloor: number,
): void {
  if (timelineFloor <= 0) return
  const run = state.liveRunsBySession[sessionId]?.[runId]
  if (!run) return
  applyRunTimelineFloor(run, timelineFloor)
}

function applyPendingUserRunTimelineFloorToRun(
  state: DesktopV3CacheState,
  sessionId: string,
  run: LiveRunOverlay,
): void {
  for (const pending of Object.values(state.pendingUserByClientRequestId)) {
    if (pending.sessionId !== sessionId || pending.runId !== run.runId) continue
    applyRunTimelineFloor(run, (pending.timelineSeq ?? 0) + 1)
  }
}

function applyRunTimelineFloor(run: LiveRunOverlay, timelineFloor: number): void {
  if (timelineFloor <= 0) return
  run.timelineFloor = Math.max(run.timelineFloor ?? 0, timelineFloor)
  if (run.assistantDraft) {
    run.assistantDraft.timelineSeq = Math.max(run.assistantDraft.timelineSeq ?? 0, run.timelineFloor)
  }
  for (const segment of run.assistantSegments ?? []) {
    segment.timelineSeq = Math.max(segment.timelineSeq ?? 0, run.timelineFloor)
  }
  for (const tool of Object.values(run.toolCallsByCallId)) {
    tool.timelineSeq = Math.max(tool.timelineSeq ?? 0, run.timelineFloor)
  }
  for (const reasoning of Object.values(run.reasoningByKey ?? {})) {
    reasoning.timelineSeq = Math.max(reasoning.timelineSeq ?? 0, run.timelineFloor)
  }
  if (run.reasoning) {
    run.reasoning.timelineSeq = Math.max(run.reasoning.timelineSeq ?? 0, run.timelineFloor)
  }
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
    const retainEvent = shouldRetainRealtimeEvent(eventType)
    const existingEvents = state.eventsBySession[sessionId] ?? []
    const duplicateIndex = existingEvents.findIndex((entry) => (
      entry.id === event.sessionEvent?.id
      || entry.seq === event.sessionEvent?.seq
    ))
    if (duplicateIndex >= 0 && retainEvent) {
      // Durable retained session events are immutable. Do not apply the same event twice.
      return state
    }
    if (retainEvent) {
      state.eventsBySession[sessionId] = [...existingEvents, event.sessionEvent]
        .sort((left, right) => left.seq - right.seq)
    }
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

  applyExecutionEpochFromEvent(state, event)

  if (payload.session && eventType === 'session.reactivated') {
    delete state.tombstonesBySession[sessionId]
    restoreSessionToSidebar(state, sessionId)
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
  applyPlanSnapshotFromEventPayload(state, sessionId, payload)

  applyPermissionSummaryEvent(state, event)
  applyPermissionEvent(state, event)
  applyNotificationResourceEvent(state, event)
  if (event.eventType === 'task.lifecycle.updated') {
    const task = normalizeDesktopAITask(event.task)
    if (task) mergeAITaskItems(state, [task])
  }

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

function applyExecutionEpochFromEvent(state: DesktopV3CacheState, event: CacheEvent): void {
  const epoch = event.payload.execution_epoch
  const boundary = event.payload.execution_epoch_boundary
  if (!epoch && !boundary) return

  const incoming: V3ExecutionEpoch = epoch ?? {
    epoch_id: boundary!.epoch_id,
    epoch_ordinal: boundary!.epoch_ordinal,
    session_id: event.sessionId,
  }
  const boundaryKind = boundary?.kind?.toLowerCase()
  mergeCurrentExecutionEpoch(state, event.sessionId, {
    ...incoming,
    started_event_seq: incoming.started_event_seq
      ?? (boundaryKind === 'started' || boundaryKind === 'begin' ? event.sessionEvent?.seq : undefined),
    completed_event_seq: incoming.completed_event_seq
      ?? (boundaryKind === 'completed' || boundaryKind === 'ended' ? event.sessionEvent?.seq : undefined),
    status: incoming.status
      ?? (boundaryKind === 'completed' || boundaryKind === 'ended'
        ? 'completed'
        : boundaryKind === 'started' || boundaryKind === 'begin' ? 'active' : undefined),
  })
}

function mergeCurrentExecutionEpoch(state: DesktopV3CacheState, sessionId: string, incoming: V3ExecutionEpoch): void {
  const existing = state.currentExecutionEpochBySession[sessionId]
  const incomingOrdinal = incoming.epoch_ordinal ?? incoming.ordinal ?? 0
  const existingOrdinal = existing?.epoch_ordinal ?? existing?.ordinal ?? 0
  if (existing) {
    if (incomingOrdinal <= 0 && existing.epoch_id !== incoming.epoch_id) return
    if (existingOrdinal > 0 && incomingOrdinal < existingOrdinal) return
    if (incomingOrdinal === existingOrdinal && existing.epoch_id !== incoming.epoch_id) return
  }

  const sameEpoch = existing?.epoch_id === incoming.epoch_id
  const completedEventSeq = sameEpoch
    ? Math.max(existing?.completed_event_seq ?? 0, incoming.completed_event_seq ?? 0) || undefined
    : incoming.completed_event_seq
  const startedEventSeq = sameEpoch
    ? existing?.started_event_seq ?? incoming.started_event_seq
    : incoming.started_event_seq
  const existingTerminal = existing?.status === 'completed' || existing?.status === 'sealed'
  const incomingTerminal = incoming.status === 'completed' || incoming.status === 'sealed'
  const preserveTerminal = sameEpoch && existingTerminal && !incomingTerminal
  state.currentExecutionEpochBySession[sessionId] = {
    ...(sameEpoch ? existing : undefined),
    ...incoming,
    epoch_ordinal: incomingOrdinal || incoming.epoch_ordinal,
    session_id: incoming.session_id || sessionId,
    started_event_seq: startedEventSeq,
    completed_event_seq: completedEventSeq,
    status: preserveTerminal ? existing.status : incoming.status ?? existing?.status,
  }
}

export function shouldRetainRealtimeEvent(eventType: string): boolean {
  switch (eventType) {
    case 'session.assistant.delta':
    case 'session.message.delta':
    case 'session.reasoning.delta':
    case 'session.tool.delta':
      return false
    default:
      return true
  }
}

function applyPlanSnapshotFromEventPayload(state: DesktopV3CacheState, sessionId: string, payload: SessionEventPayload): void {
  if (payload.has_active_plan !== undefined) {
    state.hasActivePlanBySession[sessionId] = payload.has_active_plan
  }
  if (payload.active_plan !== undefined) {
    state.plansBySession[sessionId] = payload.active_plan === null ? null : normalizeDesktopSessionPlan(payload.active_plan)
  }
  if (payload.has_active_plan === false) {
    state.plansBySession[sessionId] = null
  }
}

function applyPermissionSummaryEvent(state: DesktopV3CacheState, event: CacheEvent): void {
  if (event.eventType !== 'permission.summary.updated') return
  const summary = normalizeDesktopPermissionSummary(
    event.payload.permission_summary ?? event.payload.summary ?? event.payload,
    event.sessionId,
  )
  if (summary) applyPermissionSummary(state, event.sessionId, summary)
}

function applyPermissionEvent(state: DesktopV3CacheState, event: CacheEvent): void {
  if (event.eventType !== 'permission.requested' && event.eventType !== 'permission.updated' && !event.payload.permission) return
  const permission = normalizeDesktopPermission(event.payload.permission, event.sessionId)
  if (permission) {
    upsertPermissionRecord(state, permission)
    return
  }
  const identity = desktopPermissionIdentity(event.payload.permission, event.sessionId)
  if (identity) removePermissionRecord(state, identity.sessionId, identity.id)
}

function applyNotificationResourceEvent(state: DesktopV3CacheState, event: CacheEvent): void {
  if (event.eventType !== 'notification.resource.updated' && !event.notification && !event.notificationSummary) return
  applyNotificationsFromResourcePayload(
    state,
    event.notification ? [event.notification] : notificationWireFromPayload(event.payload),
    event.notificationSummary ?? notificationSummaryWireFromPayload(event.payload),
    { replaceNotifications: false, replaceSummary: Boolean(event.notificationSummary ?? notificationSummaryWireFromPayload(event.payload)) },
  )

  const payloadEventType = stringField(recordValue(event.payload)?.event_type)
  const deleted = numberValue(recordValue(event.payload)?.deleted)
  if (payloadEventType === 'notification.cleared' || (deleted > 0 && !event.notification)) {
    state.notificationsById = {}
  }
}

function applyNotificationResourceFrame(state: DesktopV3CacheState, frame: RealtimeMessage): void {
  const eventPayload: SessionEventPayload = frame.event ? decodeSessionEventPayload(frame.event) : {}
  applyNotificationsFromResourcePayload(
    state,
    frame.notification ? [frame.notification] : notificationWireFromPayload(eventPayload),
    frame.notification_summary ?? notificationSummaryWireFromPayload(eventPayload),
    { replaceNotifications: false, replaceSummary: Boolean(frame.notification_summary ?? notificationSummaryWireFromPayload(eventPayload)) },
  )

  const payloadEventType = stringField(recordValue(eventPayload)?.event_type)
  const deleted = numberValue(recordValue(eventPayload)?.deleted)
  if (payloadEventType === 'notification.cleared' || (deleted > 0 && !frame.notification)) {
    state.notificationsById = {}
  }
}

function applyAITaskResourceFrame(state: DesktopV3CacheState, frame: RealtimeMessage): void {
  const task = normalizeDesktopAITask(frame.task)
  if (task) mergeAITaskItems(state, [task])
}

function applyAITasksFromSyncSnapshot(state: DesktopV3CacheState, snapshot: SyncSnapshotResponse): void {
  if (!syncResourceSetContains(snapshot.sync_scope.resource_set, 'tasks')) return
  mergeAITaskWireItems(state, snapshot.tasks)
}

function mergeAITaskWireItems(state: DesktopV3CacheState, items: SyncSnapshotResponse['tasks']): void {
  for (const raw of items ?? []) {
    const task = normalizeDesktopAITask(raw)
    if (task) mergeAITaskItems(state, [task])
  }
}

function mergeAITaskItems(state: DesktopV3CacheState, items: WorkspaceTodoItem[]): void {
  let next = state.aiTasksById
  let changed = false
  for (const incoming of items) {
    if (!incoming.id || !incoming.aiState) continue
    const current = next[incoming.id]
    const merged = mergeWorkspaceAITaskMonotonic(current, incoming)
    if (merged === current) continue
    if (!changed) {
      next = { ...next }
      changed = true
    }
    next[incoming.id] = merged
  }
  if (changed) state.aiTasksById = next
}

function normalizeDesktopAITask(raw: RealtimeMessage['task']): WorkspaceTodoItem | undefined {
  const id = stringField(raw?.task_id)
  const workspacePath = stringField(raw?.workspace_path)
  const aiState = stringField(raw?.state) as WorkspaceTodoAIState
  if (!id || !workspacePath || !['queued', 'preparing', 'in_progress', 'completed', 'failed', 'cancelled'].includes(aiState)) return undefined
  const createdAt = numberValue(raw?.created_at)
  const updatedAt = numberValue(raw?.updated_at)
  const requestTitle = stringValue(raw?.request_title)
  return {
    id,
    workspacePath,
    ownerKind: 'user',
    text: requestTitle,
    done: aiState === 'completed',
    priority: 'medium',
    group: '',
    tags: [],
    inProgress: aiState === 'in_progress',
    sessionId: '',
    parentId: '',
    aiState,
    aiMode: '',
    aiWorktree: false,
    aiRequest: requestTitle,
    aiError: stringValue(raw?.error),
    aiDisplayTitle: stringValue(raw?.display_title),
    aiResult: stringValue(raw?.result),
    managedSessionId: stringValue(raw?.managed_session_id),
    accountScopeId: stringValue(raw?.account_scope_id),
    workspaceId: stringValue(raw?.workspace_id),
    originSessionId: '',
    preparationSessionId: stringValue(raw?.preparation_session_id),
    preparationRunId: stringValue(raw?.preparation_run_id),
    preparationAttemptId: '',
    finalRunId: stringValue(raw?.managed_run_id),
    aiStateVersion: numberValue(raw?.version),
    sortIndex: 0,
    createdAt,
    updatedAt,
    completedAt: numberValue(raw?.completed_at),
  }
}

function applyNotificationsFromSyncSnapshot(state: DesktopV3CacheState, snapshot: SyncSnapshotResponse): void {
  const resourceSet = snapshot.sync_scope.resource_set
  if (!syncResourceSetContains(resourceSet, 'notifications') && !syncResourceSetContains(resourceSet, 'notification_summary')) return
  applyNotificationsFromResourcePayload(state, snapshot.notifications, snapshot.notification_summary, {
    replaceNotifications: syncResourceSetContains(resourceSet, 'notifications'),
    replaceSummary: syncResourceSetContains(resourceSet, 'notification_summary'),
  })
}

function applyNotificationsFromResourcePayload(
  state: DesktopV3CacheState,
  notifications: DesktopNotificationWire[] | undefined,
  summary: DesktopNotificationSummaryWire | undefined,
  options: { replaceNotifications: boolean; replaceSummary: boolean },
): void {
  if (options.replaceNotifications) {
    state.notificationsById = {}
  }
  for (const raw of notifications ?? []) {
    const notification = normalizeDesktopNotification(raw)
    if (notification) upsertNotificationRecord(state, notification)
  }
  if (options.replaceSummary) {
    state.notificationSummary = normalizeDesktopNotificationSummary(summary) ?? { ...EMPTY_NOTIFICATION_SUMMARY }
  }
}

function notificationWireFromPayload(payload: SessionEventPayload): DesktopNotificationWire[] | undefined {
  const notification = recordValue(payload.notification)
  return notification ? [notification as DesktopNotificationWire] : undefined
}

function notificationSummaryWireFromPayload(payload: SessionEventPayload): DesktopNotificationSummaryWire | undefined {
  return (recordValue(payload.notification_summary) ?? recordValue(payload.summary)) as DesktopNotificationSummaryWire | undefined
}

function upsertNotificationRecord(state: DesktopV3CacheState, notification: DesktopNotificationCenterRecord): void {
  const existing = state.notificationsById[notification.id]
  if (existing && existing.updatedAt > notification.updatedAt) return
  state.notificationsById[notification.id] = notification
}

function normalizeDesktopNotification(raw: DesktopNotificationWire | undefined): DesktopNotificationCenterRecord | undefined {
  if (!raw) return undefined
  const id = stringField(raw.id)
  const swarmID = stringField(raw.swarmID) || stringField(raw.swarm_id)
  if (!id || !swarmID) return undefined
  return {
    id,
    accountScopeID: nullableString(raw.accountScopeID ?? raw.account_scope_id),
    swarmID,
    originSwarmID: nullableString(raw.originSwarmID ?? raw.origin_swarm_id),
    sessionId: nullableString(raw.sessionId ?? raw.session_id),
    runId: nullableString(raw.runId ?? raw.run_id),
    category: stringField(raw.category) || 'system',
    severity: stringField(raw.severity) || 'info',
    title: stringField(raw.title) || 'Notification',
    body: stringField(raw.body) || '',
    status: stringField(raw.status) || 'active',
    sourceEventType: nullableString(raw.sourceEventType ?? raw.source_event_type),
    permissionId: nullableString(raw.permissionId ?? raw.permission_id),
    toolName: nullableString(raw.toolName ?? raw.tool_name),
    requirement: nullableString(raw.requirement),
    sessionTitle: nullableString(raw.sessionTitle ?? raw.session_title),
    sessionLabel: nullableString(raw.sessionLabel ?? raw.session_label),
    workspacePath: nullableString(raw.workspacePath ?? raw.workspace_path),
    workspaceName: nullableString(raw.workspaceName ?? raw.workspace_name),
    originLabel: nullableString(raw.originLabel ?? raw.origin_label),
    actionURL: nullableString(raw.actionURL ?? raw.action_url),
    readAt: nullableNumber(raw.readAt ?? raw.read_at),
    ackedAt: nullableNumber(raw.ackedAt ?? raw.acked_at),
    mutedAt: nullableNumber(raw.mutedAt ?? raw.muted_at),
    createdAt: numberValue(raw.createdAt ?? raw.created_at),
    updatedAt: numberValue(raw.updatedAt ?? raw.updated_at),
  }
}

function normalizeDesktopNotificationSummary(raw: DesktopNotificationSummaryWire | undefined): DesktopNotificationSummary | undefined {
  if (!raw) return undefined
  return {
    accountScopeID: nullableString(raw.accountScopeID ?? raw.account_scope_id),
    swarmID: stringField(raw.swarmID) || stringField(raw.swarm_id) || '',
    totalCount: numberValue(raw.totalCount ?? raw.total_count),
    unreadCount: numberValue(raw.unreadCount ?? raw.unread_count),
    activeCount: numberValue(raw.activeCount ?? raw.active_count),
    updatedAt: numberValue(raw.updatedAt ?? raw.updated_at),
  }
}

function nullableString(value: unknown): string | null {
  return stringField(value) ?? null
}

function nullableNumber(value: unknown): number | null {
  const number = finiteNumberValue(value)
  return number === undefined ? null : number
}

function applyPermissionSummary(
  state: DesktopV3CacheState,
  sessionId: string,
  summary: DesktopPermissionSummary | undefined,
): void {
  const normalizedSessionId = safeString(sessionId)
  if (!normalizedSessionId) return
  if (!summary || summary.pendingApprovalCount <= 0) {
    delete state.permissionSummaryBySessionId[normalizedSessionId]
    return
  }
  const existing = state.permissionSummaryBySessionId[normalizedSessionId]
  if (existing && summary.updatedAt > 0 && existing.updatedAt > summary.updatedAt) return
  state.permissionSummaryBySessionId[normalizedSessionId] = { ...summary }
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

function removePermissionRecord(state: DesktopV3CacheState, sessionId: string, permissionId: string): void {
  const current = state.permissionsBySession[sessionId]
  if (!current) return
  const next = current.filter((permission) => permission.id !== permissionId)
  if (next.length > 0) {
    state.permissionsBySession[sessionId] = next
  } else {
    delete state.permissionsBySession[sessionId]
  }
}

function permissionFreshness(permission: DesktopPermissionRecord): number {
  return Math.max(permission.resolvedAt || 0, permission.updatedAt || 0, permission.permissionRequestedAt || 0, permission.createdAt || 0)
}

function terminalPermissionRank(permission: DesktopPermissionRecord): number {
  return safeString(permission.status).toLowerCase() === 'pending' ? 0 : 1
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
    const existing = nextItems[idIndex]
    const existingSeq = Number(existing.global_seq ?? 0)
    const incomingSeq = Number(message.global_seq ?? 0)
    if (existingSeq > 0 && incomingSeq < existingSeq) {
      return
    }
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
    oldestLoadedSeq: minPositiveSeq(nextItems),
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

  const priorCumulativeDurationMs = Object.values(byRunId).reduce<number | undefined>((maximum, intent) => {
    const candidate = intent.cumulative_duration_ms
    if (typeof candidate !== 'number' || !Number.isFinite(candidate) || candidate < 0) return maximum
    return maximum === undefined ? candidate : Math.max(maximum, candidate)
  }, undefined)
  const enrichedRunIntent: V3SessionRunIntent = {
    ...runIntent,
    started_at: runIntent.started_at ?? existing?.started_at,
    completed_at: runIntent.completed_at ?? existing?.completed_at,
    duration_ms: runIntent.duration_ms ?? existing?.duration_ms,
    cumulative_duration_ms: runIntent.cumulative_duration_ms
      ?? existing?.cumulative_duration_ms
      ?? priorCumulativeDurationMs,
  }

  byRunId[runIntent.run_id] = enrichedRunIntent
  state.runIntentsBySession[sessionId] = byRunId

  if (ACTIVE_RUN_INTENT_STATUSES.has(enrichedRunIntent.status)) {
    state.currentRunIntentBySession[sessionId] = enrichedRunIntent
  } else if (TERMINAL_RUN_INTENT_STATUSES.has(enrichedRunIntent.status)
    && state.currentRunIntentBySession[sessionId]?.run_id === enrichedRunIntent.run_id) {
    delete state.currentRunIntentBySession[sessionId]
  }

  const liveRuns = state.liveRunsBySession[sessionId] ?? {}
  const liveRun = liveRuns[enrichedRunIntent.run_id] ?? createLiveRunOverlay(sessionId, enrichedRunIntent.run_id)
  applyPendingUserRunTimelineFloorToRun(state, sessionId, liveRun)
  liveRun.status = normalizeLiveRunStatus(enrichedRunIntent.status)
  liveRuns[enrichedRunIntent.run_id] = liveRun
  state.liveRunsBySession[sessionId] = liveRuns
  cleanupTerminalLiveRunIfCanonicalized(state, sessionId, enrichedRunIntent.run_id, enrichedRunIntent.status)
}

function restoreSessionToSidebar(state: DesktopV3CacheState, sessionId: string): void {
  const scopeIds = new Set<string>()
  if (state.desktopSidebarBootstrap.scopeId) scopeIds.add(state.desktopSidebarBootstrap.scopeId)
  for (const [scopeId, workset] of Object.entries(state.worksetsById)) {
    if ((workset.inactiveSessionIds ?? []).includes(sessionId)) scopeIds.add(scopeId)
    workset.inactiveSessionIds = (workset.inactiveSessionIds ?? []).filter((id) => id !== sessionId)
    if (scopeIds.has(scopeId)) workset.sessionIds = prependUnique(workset.sessionIds ?? [], sessionId)
  }
  for (const scopeId of scopeIds) {
    state.sessionOrderByScope[scopeId] = prependUnique(state.sessionOrderByScope[scopeId] ?? [], sessionId)
  }
}

export function applyTombstone(
  state: DesktopV3CacheState,
  sessionId: string,
  tombstone?: V3SessionTombstone,
): void {
  applyTombstonesBySession(state, {
    [sessionId]: tombstone ?? { session_id: sessionId, deleted: true },
  })
}

function applyTombstonesBySession(
  state: DesktopV3CacheState,
  tombstonesBySession: Record<string, V3SessionTombstone> | undefined,
): void {
  if (!tombstonesBySession) return

  const sessionIdsToClean = new Set<string>()
  for (const [sessionId, tombstone] of Object.entries(tombstonesBySession)) {
    if (!hasOwn(state.tombstonesBySession, sessionId)) sessionIdsToClean.add(sessionId)
    state.tombstonesBySession[sessionId] = tombstoneWithRetainedSession(state, sessionId, tombstone)
  }
  if (sessionIdsToClean.size === 0) return

  for (const [scopeId, order] of Object.entries(state.sessionOrderByScope)) {
    state.sessionOrderByScope[scopeId] = order.filter((id) => !sessionIdsToClean.has(id))
  }
  for (const [subscriptionId, subscription] of Object.entries(state.subscriptionsById)) {
    const sessionId = subscription.session_id || subscription.sessionId
    if (sessionId && sessionIdsToClean.has(sessionId)) delete state.subscriptionsById[subscriptionId]
  }
  for (const workset of Object.values(state.worksetsById)) {
    workset.sessionIds = (workset.sessionIds ?? []).filter((id) => !sessionIdsToClean.has(id))
    const inactiveSessionIds = new Set(workset.inactiveSessionIds ?? [])
    for (const sessionId of sessionIdsToClean) inactiveSessionIds.add(sessionId)
    workset.inactiveSessionIds = [...inactiveSessionIds]
  }
  for (const sessionId of sessionIdsToClean) {
    delete state.liveRunsBySession[sessionId]
    delete state.currentRunIntentBySession[sessionId]
    delete state.runIntentsBySession[sessionId]
    delete state.plansBySession[sessionId]
    delete state.hasActivePlanBySession[sessionId]
    delete state.planRevisionsBySession[sessionId]
  }
}

function navigationVisibleSessionIds(state: DesktopV3CacheState, sessionIds: string[]): string[] {
  return sessionIds.filter((sessionId) => !isDesktopV3NavigationHiddenRecord(state.sessionsById[sessionId]))
}

function removeSessionFromNavigationMembership(state: DesktopV3CacheState, sessionId: string): void {
  for (const [scopeId, order] of Object.entries(state.sessionOrderByScope)) {
    state.sessionOrderByScope[scopeId] = order.filter((id) => id !== sessionId)
  }
  for (const workset of Object.values(state.worksetsById)) {
    workset.sessionIds = (workset.sessionIds ?? []).filter((id) => id !== sessionId)
    workset.inactiveSessionIds = (workset.inactiveSessionIds ?? []).filter((id) => id !== sessionId)
  }
}

export function applyWorksetSessionDiscovered(
  state: DesktopV3CacheState,
  frame: RealtimeMessage,
): void {
  const discoveredWorksetId = stringField(frame.workset_id)
  const sessionId = stringField(frame.session_id)
  if (!discoveredWorksetId || !sessionId) return

  if (frame.session) {
    upsertWorksetSessionShell(state, sessionId, frame.session)
  } else if (!state.sessionsById[sessionId]) {
    state.sessionsById[sessionId] = {
      kind: 'stub',
      id: sessionId,
      needsHydrate: true,
      discoveredByWorksetId: discoveredWorksetId,
      discoveredAt: Date.now(),
    }
  }

  if (isDesktopV3NavigationHiddenRecord(state.sessionsById[sessionId])) {
    removeSessionFromNavigationMembership(state, sessionId)
    return
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

  if (frame.projection) state.projectionsBySession[sessionId] = frame.projection
  applyCurrentRunStateFrame(state, sessionId, frame.current_run_state)
  const summary = normalizeDesktopPermissionSummary(frame.permission_summary, sessionId)
  if (summary) applyPermissionSummary(state, sessionId, summary)
  applyPlanSnapshotFromWorksetFrame(state, sessionId, frame)

  // Intentionally do not update state.realtime.endpointCursor here.
  // The matching durable event for this same endpoint record must be applied first.
}

export function applyWorksetSessionUpdated(
  state: DesktopV3CacheState,
  frame: RealtimeMessage,
): void {
  const worksetIdValue = stringField(frame.workset_id)
  const sessionId = stringField(frame.session_id)
  if (!worksetIdValue || !sessionId) return

  if (frame.session) {
    upsertWorksetSessionShell(state, sessionId, frame.session)
  } else if (!state.sessionsById[sessionId]) {
    state.sessionsById[sessionId] = {
      kind: 'stub',
      id: sessionId,
      needsHydrate: true,
      discoveredByWorksetId: worksetIdValue,
      discoveredAt: Date.now(),
    }
  }

  if (isDesktopV3NavigationHiddenRecord(state.sessionsById[sessionId])) {
    removeSessionFromNavigationMembership(state, sessionId)
    state.realtime.endpointCursor = frame.endpoint_cursor ?? state.realtime.endpointCursor
    return
  }

  const scopeIds = new Set<string>([worksetIdValue])
  if (state.desktopSidebarBootstrap.scopeId) {
    scopeIds.add(state.desktopSidebarBootstrap.scopeId)
  }
  for (const scopeId of scopeIds) {
    const current = state.sessionOrderByScope[scopeId] ?? []
    state.sessionOrderByScope[scopeId] = [sessionId, ...current.filter((id) => id !== sessionId)]
  }

  const workset = state.worksetsById[worksetIdValue] ?? {
    workset_id: worksetIdValue,
    worksetId: worksetIdValue,
  }
  workset.sessionIds = [sessionId, ...(workset.sessionIds ?? []).filter((id) => id !== sessionId)]
  workset.inactiveSessionIds = (workset.inactiveSessionIds ?? []).filter((id) => id !== sessionId)
  state.worksetsById[worksetIdValue] = workset

  if (frame.projection) state.projectionsBySession[sessionId] = frame.projection
  applyCurrentRunStateFrame(state, sessionId, frame.current_run_state)
  const summary = normalizeDesktopPermissionSummary(frame.permission_summary, sessionId)
  if (summary) applyPermissionSummary(state, sessionId, summary)
  applyPlanSnapshotFromWorksetFrame(state, sessionId, frame)
  state.realtime.endpointCursor = frame.endpoint_cursor ?? state.realtime.endpointCursor
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
  state.realtime.endpointCursor = frame.endpoint_cursor ?? state.realtime.endpointCursor
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
  delete state.evictedTranscriptsBySession?.[sessionId]
  state.messagesBySession[sessionId] = buildMessageListCache(messages, {
    knownTail: { limit: messages.length, cursor: '' },
    sourceMessageCount: session?.message_count,
    sourceLastMessageAt: session?.last_message_at,
    sourceProjectionHighWatermarkSeq: state.projectionsBySession[sessionId]?.projection_high_watermark_seq,
    oldestLoadedSeq: minPositiveSeq(messages),
    hydratedAt: Date.now(),
    tailHydratedAt: Date.now(),
    lastAccessedAt: Date.now(),
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
    oldestLoadedSeq: minPositiveSeq(mergedItems),
    hydratedAt: Math.max(existing?.hydratedAt ?? 0, Date.now()),
    tailHydratedAt: existing?.tailHydratedAt,
    lastAccessedAt: Date.now(),
    source: 'network',
  })
  delete state.evictedTranscriptsBySession?.[sessionId]
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

function prependHistoricalMessagesForSession(
  state: DesktopV3CacheState,
  sessionId: string,
  incoming: MessageSnapshot[],
  options: { sourceMessageCount?: number; knownFull?: boolean } = {},
): void {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) return
  const existing = state.messagesBySession[normalizedSessionId]
  const mergedItems = [...incoming, ...(existing?.items ?? [])]
  const merged = buildMessageListCache(mergedItems, {
    knownTail: existing?.knownTail,
    knownFull: options.knownFull || existing?.knownFull,
    sourceMessageCount: Math.max(options.sourceMessageCount ?? 0, existing?.sourceMessageCount ?? 0, incoming.length, mergedItems.length),
    sourceLastMessageAt: Math.max(existing?.sourceLastMessageAt ?? 0, ...incoming.map((message) => message.created_at)),
    sourceProjectionHighWatermarkSeq: existing?.sourceProjectionHighWatermarkSeq,
    oldestLoadedSeq: minPositiveSeq(mergedItems),
    hydratedAt: Math.max(existing?.hydratedAt ?? 0, Date.now()),
    tailHydratedAt: existing?.tailHydratedAt,
    lastAccessedAt: Date.now(),
    source: 'network',
  })
  delete state.evictedTranscriptsBySession?.[normalizedSessionId]
  state.messagesBySession[normalizedSessionId] = merged
  removeCommittedPendingForSession(state, normalizedSessionId, incoming)
}

interface BuildMessageListCacheOptions {
  knownTail?: MessageListCache['knownTail']
  knownFull?: boolean
  sourceMessageCount?: number
  sourceLastMessageAt?: number
  sourceProjectionHighWatermarkSeq?: number
  hydratedAt?: number
  tailHydratedAt?: number
  oldestLoadedSeq?: number
  lastAccessedAt?: number
  source?: MessageListCache['source']
}

function minPositiveSeq(messages: MessageSnapshot[]): number {
  return messages.reduce((min, message) => {
    const seq = Number.isSafeInteger(message.global_seq) && message.global_seq > 0 ? message.global_seq : 0
    if (seq <= 0) return min
    return min === 0 ? seq : Math.min(min, seq)
  }, 0)
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
    oldestLoadedSeq: options.oldestLoadedSeq ?? minPositiveSeq(items),
    loadedCount: items.length,
    hydratedAt: options.hydratedAt,
    tailHydratedAt: options.tailHydratedAt,
    lastAccessedAt: options.lastAccessedAt,
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
  for (const workset of [
    ...(raw.worksets ?? []),
    ...(raw.realtime?.resume?.worksets ?? []),
  ]) {
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
  if (resources.has('messages') || resources.has('events')) {
    replaceRecordBySession(state.historyManifestsBySession, raw.history_manifests_by_session, authoritativeSessionIds)
    mergeRecord(state.historyChunksById, raw.history_chunks_by_id)
  } else {
    mergeRecord(state.historyManifestsBySession, raw.history_manifests_by_session)
    mergeRecord(state.historyChunksById, raw.history_chunks_by_id)
  }
}

function applyCurrentRunStateFromReconnect(
  state: DesktopV3CacheState,
  raw: SessionsReconnectResponse,
  authoritativeSessionIds: Set<string>,
): void {
  const snapshotLike: SyncSnapshotResponse = {
    ok: true,
    rev: raw.rev,
    snapshot_endpoint_cursor: raw.snapshot_endpoint_cursor,
    sessions_by_id: raw.sessions_by_id,
    projections_by_session: raw.projections_by_session,
    current_run_state_by_session: raw.current_run_state_by_session,
    session_order: raw.session_order,
    sync_scope: {
      surface: raw.surface ?? 'desktop',
      stream_kind: 'v3.sync.snapshot',
      selector_filter_hash: raw.workset_id ?? 'reconnect',
      resource_set: 'current_run_state',
    },
    scope_id: raw.workset_id ?? 'reconnect',
    selector: {},
    known_sessions: {},
    tombstones_by_session: {},
    replay_instructions: {
      stream_path: '/v3/sync/stream',
      transport: 'http_post',
      after_endpoint_cursor: raw.snapshot_endpoint_cursor,
      bootstrap_required_on_cursor_error: true,
    },
  }
  applyCurrentRunStateFromSyncSnapshot(state, snapshotLike, authoritativeSessionIds)
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

function applyCurrentRunStateFromSyncSnapshot(
  state: DesktopV3CacheState,
  snapshot: SyncSnapshotResponse,
  authoritativeSessionIds = new Set(Object.keys(snapshot.sessions_by_id ?? {})),
): void {
  if (!syncResourceSetContains(snapshot.sync_scope.resource_set, 'current_run_state')) return
  const states = snapshot.current_run_state_by_session ?? {}
  for (const sessionId of authoritativeSessionIds) {
    applyCurrentRunStateFrame(state, sessionId, states[sessionId])
  }
}

function applyCurrentRunStateFrame(
  state: DesktopV3CacheState,
  sessionId: string,
  runState: NonNullable<SyncSnapshotResponse['current_run_state_by_session']>[string] | undefined,
): void {
  if (runState?.run_id?.trim()) {
    const runIntent: V3SessionRunIntent = {
      session_id: runState.session_id,
      user_id: runState.user_id,
      account_scope_id: runState.account_scope_id,
      run_id: runState.run_id,
      status: runState.status,
      blocked_reason: runState.blocked_reason,
      created_at: runState.created_at,
      started_at: runState.started_at,
      completed_at: runState.completed_at,
      duration_ms: runState.duration_ms,
      cumulative_duration_ms: runState.cumulative_duration_ms,
      updated_at: runState.updated_at,
      event_seq: runState.event_seq ?? 0,
    }
    const byRunId = state.runIntentsBySession[sessionId] ?? {}
    byRunId[runIntent.run_id] = {
      ...byRunId[runIntent.run_id],
      ...runIntent,
    }
    state.runIntentsBySession[sessionId] = byRunId
    if (runState.active) {
      state.currentRunIntentBySession[sessionId] = runIntent
    } else {
      delete state.currentRunIntentBySession[sessionId]
    }
  } else {
    delete state.currentRunIntentBySession[sessionId]
  }
}

function applyPermissionSummariesFromSyncSnapshot(
  state: DesktopV3CacheState,
  snapshot: SyncSnapshotResponse,
  authoritativeSessionIds = new Set([
    ...Object.keys(snapshot.sessions_by_id ?? {}),
    ...Object.keys(snapshot.tombstones_by_session ?? {}),
    ...Object.keys(state.permissionSummaryBySessionId ?? {}),
  ]),
): void {
  if (!syncResourceSetContains(snapshot.sync_scope.resource_set, 'permission_summaries')) return
  applyPermissionSummaries(state, snapshot.permission_summaries_by_session, authoritativeSessionIds)
}

function applyPermissionSummaries(
  state: DesktopV3CacheState,
  summariesBySession: SyncSnapshotResponse['permission_summaries_by_session'],
  authoritativeSessionIds: Set<string>,
): void {
  const summaries = normalizeDesktopPermissionSummaries(summariesBySession)
  for (const sessionId of authoritativeSessionIds) {
    applyPermissionSummary(state, sessionId, summaries[sessionId])
  }
}

function mergeSnapshotResources(
  state: DesktopV3CacheState,
  snapshot: SyncSnapshotResponse,
  scopeId: string,
  requested?: Set<string>,
): void {
  const resourceSet = snapshot.sync_scope.resource_set
  const authoritativeSessionIds = requested ?? new Set(Object.keys(snapshot.sessions_by_id ?? {}))
  if (syncResourceSetContains(resourceSet, 'messages') || syncResourceSetContains(resourceSet, 'events')) {
    const historySessionIds = filterHydrateHistorySessionIds(state, authoritativeSessionIds)
    replaceRecordBySession(state.historyManifestsBySession, snapshot.history_manifests_by_session, historySessionIds)
    mergeHistoryChunksForSessionIds(state, snapshot.history_chunks_by_id, historySessionIds)
  }
  if (snapshot.omissions !== undefined) state.omissionsByScope[scopeId] = snapshot.omissions
  if (snapshot.pagination !== undefined) state.paginationByScope[scopeId] = snapshot.pagination
  if (snapshot.watermarks !== undefined) state.watermarksByScope[scopeId] = snapshot.watermarks
}

function applySessionViewsFromSyncSnapshot(
  state: DesktopV3CacheState,
  snapshot: SyncSnapshotResponse,
  authoritativeSessionIds: Set<string>,
): void {
  const resourceSet = snapshot.sync_scope.resource_set
  const hasSessionView = syncResourceSetContains(resourceSet, 'session_view')
  const hasActivePlan = syncResourceSetContains(resourceSet, 'active_plan')
  if (!hasSessionView && !hasActivePlan) return
  applySessionViews(state, snapshot.session_views_by_id, authoritativeSessionIds, { clearMissing: hasSessionView })
}

function applySessionViews(
  state: DesktopV3CacheState,
  viewsById: SyncSnapshotResponse['session_views_by_id'] | undefined,
  authoritativeSessionIds: Set<string>,
  options: { clearMissing: boolean },
): void {
  const views = onlyRequested(viewsById, authoritativeSessionIds)
  for (const sessionId of authoritativeSessionIds) {
    const view = views?.[sessionId]
    if (!view) {
      if (options.clearMissing) {
        delete state.sessionViewsById[sessionId]
        delete state.permissionsBySession[sessionId]
        delete state.usageBySession[sessionId]
        delete state.plansBySession[sessionId]
        delete state.hasActivePlanBySession[sessionId]
      }
      continue
    }

    state.sessionViewsById[sessionId] = { ...state.sessionViewsById[sessionId], ...view }
    if (view.current_execution_epoch) {
      mergeCurrentExecutionEpoch(state, sessionId, view.current_execution_epoch)
    }
    // A null recovery view may race a newer retained boundary. Boundaries are the
    // authoritative way to advance or complete the cached epoch, so do not erase it.
    if (view.pending_permissions !== undefined) state.permissionsBySession[sessionId] = normalizeDesktopPendingPermissions(view.pending_permissions, sessionId)
    if (view.usage_summary !== undefined) state.usageBySession[sessionId] = view.usage_summary
    applyPlanSnapshotFromSessionView(state, sessionId, view)

    const settings = view.agentic_settings
    const authoritativePreference = settings?.effective_preference ?? settings?.stored_preference
    if (authoritativePreference !== undefined) state.preferencesBySession[sessionId] = authoritativePreference
    if (settings?.agent_model_policy !== undefined) state.agentModelPolicyBySession[sessionId] = settings.agent_model_policy

    const record = state.sessionsById[sessionId]
    if (record?.kind === 'full' && settings) {
      state.sessionsById[sessionId] = {
        ...record,
        session: {
          ...record.session,
          mode: settings.mode || record.session.mode,
          preference: settings.stored_preference ?? record.session.preference,
          metadata: {
            ...(record.session.metadata ?? {}),
            agent_name: settings.agent_name || record.session.metadata?.agent_name,
            resolved_agent_name: settings.resolved_agent_name || record.session.metadata?.resolved_agent_name,
          },
        },
      }
    }
  }
}

function applyPlanSnapshotFromSessionView(state: DesktopV3CacheState, sessionId: string, view: Pick<NonNullable<SyncSnapshotResponse['session_views_by_id']>[string], 'has_active_plan' | 'active_plan'>): void {
  if (view.has_active_plan !== undefined) {
    state.hasActivePlanBySession[sessionId] = view.has_active_plan
  }
  if (view.active_plan !== undefined) {
    state.plansBySession[sessionId] = view.active_plan === null ? null : normalizeDesktopSessionPlan(view.active_plan)
  }
  if (view.has_active_plan === false) {
    state.plansBySession[sessionId] = null
  }
}

function applyPlanSnapshotFromWorksetFrame(state: DesktopV3CacheState, sessionId: string, frame: RealtimeMessage): void {
  applyPlanSnapshotFromSessionView(state, sessionId, {
    has_active_plan: frame.has_active_plan,
    active_plan: frame.active_plan,
  })
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

function upsertWorksetSessionShell(state: DesktopV3CacheState, sessionId: string, shell: SessionSnapshot): void {
  const existing = state.sessionsById[sessionId]
  if (existing?.kind !== 'full') {
    upsertSessions(state, { [sessionId]: shell })
    return
  }

  const existingSession = existing.session
  const shellPreference = recordValue(shell.preference)
  const nextSession: SessionSnapshot = {
    ...existingSession,
    ...shell,
    title: shell.title?.trim() ? shell.title : existingSession.title,
    mode: shell.mode?.trim() ? shell.mode : existingSession.mode,
    workspace_path: shell.workspace_path?.trim() ? shell.workspace_path : existingSession.workspace_path,
    workspace_name: shell.workspace_name?.trim() ? shell.workspace_name : existingSession.workspace_name,
    created_at: shell.created_at || existingSession.created_at,
    updated_at: shell.updated_at || existingSession.updated_at,
    message_count: shell.message_count || existingSession.message_count,
    last_message_at: shell.last_message_at || existingSession.last_message_at,
    metadata: shell.metadata === undefined
      ? existingSession.metadata
      : { ...(existingSession.metadata ?? {}), ...shell.metadata },
    preference: shellPreference && Object.keys(shellPreference).length > 0
      ? shell.preference
      : existingSession.preference,
    lifecycle: shell.lifecycle === undefined ? existingSession.lifecycle : shell.lifecycle,
    current_execution_epoch: shell.current_execution_epoch === undefined
      ? existingSession.current_execution_epoch
      : shell.current_execution_epoch,
  }
  state.sessionsById[sessionId] = { ...existing, session: nextSession }
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

function applyEventsBySessionFromSnapshot(state: DesktopV3CacheState, eventsBySession: Record<string, V3SessionEvent[]> | undefined): void {
  if (!eventsBySession) return
  for (const [sessionId, events] of Object.entries(eventsBySession)) {
    replayDurableEventsForSession(state, sessionId, events)
    replaceEventsForSession(state, sessionId, events)
  }
}

function mergeEventsBySessionFromSnapshot(state: DesktopV3CacheState, eventsBySession: Record<string, V3SessionEvent[]> | undefined): void {
  if (!eventsBySession) return
  for (const [sessionId, events] of Object.entries(eventsBySession)) {
    if (events.length > 0) {
      replayDurableEventsForSession(state, sessionId, events)
      mergeEventsForSession(state, sessionId, events)
    }
  }
}

function replaceEventsForSession(state: DesktopV3CacheState, sessionId: string, incoming: V3SessionEvent[]): void {
  state.eventsBySession[sessionId] = sortEvents(dedupeEventsByIdentity(incoming))
}

function mergeEventsForSession(state: DesktopV3CacheState, sessionId: string, incoming: V3SessionEvent[]): void {
  state.eventsBySession[sessionId] = sortEvents(dedupeEventsByIdentity([
    ...(state.eventsBySession[sessionId] ?? []),
    ...incoming,
  ]))
}

function replayDurableEventsForSession(state: DesktopV3CacheState, sessionId: string, incoming: V3SessionEvent[]): void {
  for (const event of sortEvents(dedupeEventsByIdentity(incoming))) {
    if (!shouldReplayDurableHydratedEvent(event.event_type)) continue
    applyCacheEvent(state, cacheEventFromDurableSessionEvent(sessionId, event))
  }
}

function shouldReplayDurableHydratedEvent(eventType: string): boolean {
  switch (eventType) {
    case 'session.reasoning.started':
    case 'session.reasoning.delta':
    case 'session.reasoning.completed':
    case 'session.reasoning.failed':
    case 'session.reasoning.error':
    case 'session.execution_epoch.started':
    case 'session.execution_epoch.completed':
    case 'session.execution_epoch.boundary':
      return true
    default:
      return false
  }
}

function cacheEventFromDurableSessionEvent(sessionId: string, event: V3SessionEvent): CacheEvent {
  return {
    source: 'sync-stream',
    sessionId: event.session_id || sessionId,
    eventType: event.event_type,
    sessionEvent: event,
    projection: {
      session_id: event.session_id || sessionId,
      last_event_seq: event.seq,
      projection_high_watermark_seq: event.seq,
      updated_at: event.ts_unix_ms,
    },
    payload: decodeSessionEventPayload(event),
  }
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
  if (eventType === 'session.title.updated' && typeof payload.title === 'string') {
    nextSession = { ...nextSession, title: payload.title }
  }
  if (eventType === 'session.mode.updated' && typeof payload.mode === 'string') {
    nextSession = { ...nextSession, mode: payload.mode }
  }
  if (eventType === 'session.metadata.updated' || eventType === 'session.agent.updated') {
    const metadata = recordValue(payload.metadata)
    if (metadata) nextSession = { ...nextSession, metadata }
  }
  if (eventType === 'session.model_profile.updated'
    && Object.prototype.hasOwnProperty.call(payload, 'model_profile')) {
    const metadata = { ...(recordValue(nextSession.metadata) ?? {}) }
    const modelProfile = recordValue(payload.model_profile)
    if (modelProfile) metadata.model_profile = modelProfile
    else delete metadata.model_profile
    nextSession = { ...nextSession, metadata }
  }
  if ((eventType === 'session.preference.updated' || eventType === 'session.mode.updated') && payload.preference !== undefined) {
    nextSession = { ...nextSession, preference: payload.preference }
    state.preferencesBySession[sessionId] = payload.preference
  }
  if (
    typeof payload.updated_at === 'number'
    && (eventType === 'session.title.updated'
      || eventType === 'session.mode.updated'
      || eventType === 'session.metadata.updated'
      || eventType === 'session.agent.updated'
      || eventType === 'session.model_profile.updated'
      || eventType === 'session.preference.updated')
  ) {
    nextSession = { ...nextSession, updated_at: payload.updated_at }
  }
  if (nextSession !== record.session) {
    state.sessionsById[sessionId] = { ...record, session: nextSession }
  }
  if (
    payload.agent_model_policy !== undefined
    && (eventType === 'session.agent_model_policy.updated' || eventType === 'session.mode.updated')
  ) {
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

  clearCommittedAssistantStream(run, message)

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
      clearCommittedAssistantStream(run, message)
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

function clearCommittedAssistantStream(run: LiveRunOverlay, message: MessageSnapshot): void {
  const streamId = stringFromMetadata(message.metadata, 'stream_id')
    || stringFromMetadata(message.metadata, 'streamId')
  if (!streamId) {
    delete run.assistantDraft
    delete run.assistantSegments
    return
  }
  if (run.assistantDraft?.streamId === streamId) delete run.assistantDraft
  if (run.assistantSegments) {
    const remaining = run.assistantSegments.filter((segment) => segment.streamId !== streamId)
    if (remaining.length > 0) run.assistantSegments = remaining
    else delete run.assistantSegments
  }
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
    case 'session.provider_tool_call.started':
    case 'session.provider_tool_call.arguments.delta':
    case 'session.provider_tool_call.arguments.snapshot':
    case 'session.provider_tool_call.completed':
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
  applyPendingUserRunTimelineFloorToRun(state, sessionId, liveRun)
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
      const streamId = stringValue(payload.stream_id)
      const offsetStart = finiteNumberValue(payload.offset_start)
      const offsetEnd = finiteNumberValue(payload.offset_end)
      if (streamId && offsetStart !== undefined && offsetEnd !== undefined) {
        applyStreamAwareDurableAssistantDelta(liveRun, {
          streamId,
          delta,
          offsetStart,
          offsetEnd,
          updatedAt,
          eventSeq,
          step: finiteNumberValue(payload.step),
          stepId: stringValue(payload.step_id) || undefined,
        })
        return
      }
      liveRun.assistantDraft = {
        content: `${liveRun.assistantDraft?.content ?? ''}${delta}`,
        updatedAt,
        timelineSeq: Math.max(liveRun.assistantDraft?.timelineSeq ?? 0, eventSeq, liveRun.timelineFloor ?? 0),
      }
      return
    }

    case 'session.reasoning.started':
    case 'session.reasoning.delta':
    case 'session.reasoning.completed':
    case 'session.reasoning.failed':
    case 'session.reasoning.error':
      raiseSameStepSpeculativeAssistantAfterReasoning(liveRun, payload, eventSeq)
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
    case 'session.tool.canceled':
    case 'session.provider_tool_call.started':
    case 'session.provider_tool_call.arguments.delta':
    case 'session.provider_tool_call.arguments.snapshot':
    case 'session.provider_tool_call.completed': {
      if (liveRun.assistantDraft?.content) {
        flushLiveAssistantDraftToSegment(liveRun)
      }
      const providerToolConstruction = event.eventType.startsWith('session.provider_tool_call.')
      const callId = stringValue(payload.call_id) || stringValue(payload.tool_call_id)
      if (!callId) {
        return
      }

      const isStarted = event.eventType === 'session.tool.started' || event.eventType === 'session.provider_tool_call.started'
      const isDelta = event.eventType === 'session.tool.delta' || event.eventType === 'session.provider_tool_call.arguments.delta'
      const isArgumentsSnapshot = event.eventType === 'session.provider_tool_call.arguments.snapshot'
      const isFailed = event.eventType === 'session.tool.failed'
      const isCancelled = event.eventType === 'session.tool.cancelled' || event.eventType === 'session.tool.canceled'
      const isTerminal = event.eventType === 'session.tool.completed' || event.eventType === 'session.provider_tool_call.completed' || isFailed || isCancelled
      const toolInstanceId = providerToolConstruction ? `provider-tool:${callId}` : stringValue(payload.tool_instance_id)
      const tool = liveRun.toolCallsByCallId[callId] ?? {
        callId,
        createdAt: updatedAt,
        updatedAt,
      }

      tool.stepId = stringValue(payload.step_id) || tool.stepId
      tool.toolInstanceId = toolInstanceId || tool.toolInstanceId
      tool.toolName = stringValue(payload.tool_name) || tool.toolName
      tool.toolIdentity = stringValue(payload.tool_identity) || tool.toolIdentity
      tool.toolRunCount = numberValue(payload.tool_run_count) || tool.toolRunCount
      tool.toolDisplay = stringValue(payload.tool_display) || tool.toolDisplay

      const argumentsText = stringValue(payload.arguments) || stringValue(payload.arguments_snapshot)
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
      } else if ((isArgumentsSnapshot || !isDelta) && argumentsText) {
        tool.argumentsText = argumentsText
      }

      if (isDelta && outputText && applyTaskStreamPatch(tool, outputText, updatedAt)) {
        // Task stream v2 is native keyed state; do not mirror patch JSON into outputText.
      } else if (rawOutput || completedOutput) {
        tool.outputText = rawOutput || completedOutput
      } else if (isTerminal && outputText) {
        tool.outputText = outputText
      } else if (isDelta && outputText && isTaskStreamSnapshotOutput(tool.toolName, outputText)) {
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
      tool.timelineSeq = Math.max(
        isTerminal && eventSeq > 0 ? eventSeq : tool.timelineSeq || eventSeq,
        liveRun.timelineFloor ?? 0,
      )

      liveRun.toolCallsByCallId[callId] = tool
      return
    }

    default:
      return
  }
}

type LiveAssistantDraft = NonNullable<LiveRunOverlay['assistantDraft']>
type LiveAssistantSegment = NonNullable<LiveRunOverlay['assistantSegments']>[number]
type LiveAssistantStreamNode = LiveAssistantDraft | LiveAssistantSegment

function applyStreamAwareDurableAssistantDelta(
  liveRun: LiveRunOverlay,
  input: {
    streamId: string
    delta: string
    offsetStart: number
    offsetEnd: number
    updatedAt: number
    eventSeq: number
    step?: number
    stepId?: string
  },
): void {
  const durableByteLength = utf8Encoder.encode(input.delta).byteLength
  if (input.offsetEnd < input.offsetStart || input.offsetEnd - input.offsetStart !== durableByteLength) {
    markAssistantStreamPaused(liveRun, input.streamId)
    return
  }

  const existing = findAssistantStreamNode(liveRun, input.streamId)
  if (!existing) {
    if (input.offsetStart !== 0) return
    if (liveRun.assistantDraft?.content) {
      liveRun.assistantSegments = upsertLiveAssistantSegment(liveRun, liveRun.assistantDraft)
    }
    liveRun.assistantDraft = {
      content: input.delta,
      updatedAt: input.updatedAt,
      timelineSeq: Math.max(input.eventSeq, liveRun.timelineFloor ?? 0),
      streamId: input.streamId,
      streamStep: input.step,
      stepId: input.stepId,
      liveSeqEnd: 0,
      offsetEnd: input.offsetEnd,
      durableOffsetEnd: input.offsetEnd,
      livePaused: false,
    }
    return
  }

  const node = existing.node
  const visibleOffsetEnd = node.offsetEnd ?? utf8Encoder.encode(node.content).byteLength
  const overlapStart = Math.max(input.offsetStart, 0)
  const overlapEnd = Math.min(input.offsetEnd, visibleOffsetEnd)
  if (overlapEnd > overlapStart && !utf8RangeEquals(node.content, 0, input.delta, input.offsetStart, overlapStart, overlapEnd)) {
    markAssistantStreamPaused(liveRun, input.streamId)
    return
  }

  if (input.offsetEnd <= visibleOffsetEnd) {
    if (input.offsetStart === 0 && input.offsetEnd === visibleOffsetEnd && durableByteLength === visibleOffsetEnd && input.delta !== node.content) {
      markAssistantStreamPaused(liveRun, input.streamId)
      return
    }
    updateAssistantStreamNode(liveRun, existing, {
      durableOffsetEnd: Math.max(node.durableOffsetEnd ?? 0, input.offsetEnd),
      updatedAt: input.updatedAt,
      timelineSeq: Math.max(node.timelineSeq || input.eventSeq, liveRun.timelineFloor ?? 0),
      streamStep: input.step ?? node.streamStep,
      stepId: input.stepId || node.stepId,
    })
    return
  }

  if (input.offsetStart > visibleOffsetEnd) {
    markAssistantStreamPaused(liveRun, input.streamId)
    return
  }

  let suffix = input.delta
  if (input.offsetStart < visibleOffsetEnd) {
    try {
      suffix = utf8SuffixAfterBytes(input.delta, visibleOffsetEnd - input.offsetStart)
    } catch {
      markAssistantStreamPaused(liveRun, input.streamId)
      return
    }
  }

  updateAssistantStreamNode(liveRun, existing, {
    content: `${node.content}${suffix}`,
    updatedAt: input.updatedAt,
    timelineSeq: Math.max(node.timelineSeq || input.eventSeq, liveRun.timelineFloor ?? 0),
    streamStep: input.step ?? node.streamStep,
    stepId: input.stepId || node.stepId,
    offsetEnd: input.offsetEnd,
    durableOffsetEnd: Math.max(node.durableOffsetEnd ?? 0, input.offsetEnd),
  })
}

function findAssistantStreamNode(
  liveRun: LiveRunOverlay,
  streamId: string,
): { kind: 'draft'; node: LiveAssistantDraft } | { kind: 'segment'; index: number; node: LiveAssistantSegment } | null {
  if (liveRun.assistantDraft?.streamId === streamId) return { kind: 'draft', node: liveRun.assistantDraft }
  const index = liveRun.assistantSegments?.findIndex((segment) => segment.streamId === streamId) ?? -1
  if (index >= 0 && liveRun.assistantSegments) return { kind: 'segment', index, node: liveRun.assistantSegments[index] }
  return null
}

function updateAssistantStreamNode(
  liveRun: LiveRunOverlay,
  ref: { kind: 'draft'; node: LiveAssistantDraft } | { kind: 'segment'; index: number; node: LiveAssistantSegment },
  patch: Partial<LiveAssistantStreamNode>,
): void {
  if (ref.kind === 'draft') {
    liveRun.assistantDraft = { ...ref.node, ...patch }
    return
  }
  if (!liveRun.assistantSegments) return
  liveRun.assistantSegments[ref.index] = { ...ref.node, ...patch }
}

function markAssistantStreamPaused(liveRun: LiveRunOverlay, streamId: string): void {
  const ref = findAssistantStreamNode(liveRun, streamId)
  if (!ref) return
  updateAssistantStreamNode(liveRun, ref, { livePaused: true })
}

export function utf8SuffixAfterBytes(text: string, skipBytes: number): string {
  const encoded = utf8Encoder.encode(text)
  const prefix = new TextDecoder('utf-8', { fatal: true }).decode(encoded.slice(0, skipBytes))
  if (utf8Encoder.encode(prefix).byteLength !== skipBytes) {
    throw new Error('UTF-8 byte offset splits a code point')
  }
  const decoder = new TextDecoder('utf-8', { fatal: true })
  return decoder.decode(encoded.slice(skipBytes))
}

export function utf8RangeEquals(
  visibleText: string,
  visibleRangeStart: number,
  durableText: string,
  durableRangeStart: number,
  overlapStart: number,
  overlapEnd: number,
): boolean {
  const visibleBytes = utf8Encoder.encode(visibleText)
  const durableBytes = utf8Encoder.encode(durableText)
  const visibleStart = overlapStart - visibleRangeStart
  const durableStart = overlapStart - durableRangeStart
  const length = overlapEnd - overlapStart
  if (visibleStart < 0 || durableStart < 0 || length < 0) return false
  if (visibleStart + length > visibleBytes.byteLength || durableStart + length > durableBytes.byteLength) return false
  if (!isUtf8Boundary(visibleBytes, visibleStart) || !isUtf8Boundary(visibleBytes, visibleStart + length)) return false
  if (!isUtf8Boundary(durableBytes, durableStart) || !isUtf8Boundary(durableBytes, durableStart + length)) return false
  for (let i = 0; i < length; i += 1) {
    if (visibleBytes[visibleStart + i] !== durableBytes[durableStart + i]) return false
  }
  return true
}

function isUtf8Boundary(bytes: Uint8Array, offset: number): boolean {
  if (offset < 0 || offset > bytes.byteLength) return false
  return offset === bytes.byteLength || (bytes[offset] & 0b1100_0000) !== 0b1000_0000
}

export function applyDesktopV3LivePatchBatch(
  state: DesktopV3CacheState,
  patches: SessionV3RealtimeLivePatchWire[],
): DesktopV3CacheState {
  if (patches.length === 0) return state
  let nextState = state
  let liveRunsBySession = state.liveRunsBySession
  const clonedSessions = new Set<string>()
  const clonedRuns = new Set<string>()

  const ensureMutableRun = (sessionId: string, runId: string): LiveRunOverlay => {
    if (nextState === state) nextState = { ...state }
    if (liveRunsBySession === state.liveRunsBySession) {
      liveRunsBySession = { ...state.liveRunsBySession }
      nextState.liveRunsBySession = liveRunsBySession
    }
    if (!clonedSessions.has(sessionId)) {
      liveRunsBySession[sessionId] = { ...(liveRunsBySession[sessionId] ?? {}) }
      clonedSessions.add(sessionId)
    }
    const sessionRuns = liveRunsBySession[sessionId]
    const cloneKey = `${sessionId}\u0000${runId}`
    if (!clonedRuns.has(cloneKey)) {
      const existing = sessionRuns[runId]
      sessionRuns[runId] = existing
        ? {
          ...existing,
          assistantDraft: existing.assistantDraft ? { ...existing.assistantDraft } : undefined,
          assistantSegments: existing.assistantSegments ? existing.assistantSegments.map((segment) => ({ ...segment })) : undefined,
          toolCallsByCallId: Object.fromEntries(
            Object.entries(existing.toolCallsByCallId).map(([callId, tool]) => [callId, { ...tool }]),
          ),
          reasoning: existing.reasoning ? { ...existing.reasoning } : undefined,
          reasoningByKey: existing.reasoningByKey ? Object.fromEntries(
            Object.entries(existing.reasoningByKey).map(([key, reasoning]) => [key, { ...reasoning }]),
          ) : undefined,
        }
        : {
          sessionId,
          runId,
          status: 'running',
          toolCallsByCallId: {},
        }
      clonedRuns.add(cloneKey)
    }
    return sessionRuns[runId]
  }

  for (const patch of patches) {
    const run = ensureMutableRun(patch.session_id, patch.run_id)
    applyPendingUserRunTimelineFloorToRun(nextState, patch.session_id, run)
    applyLivePatchToRun(nextState, run, patch)
  }

  return nextState
}

function applyLivePatchToRun(state: DesktopV3CacheState, run: LiveRunOverlay, patch: SessionV3RealtimeLivePatchWire): void {
  run.status = run.status === 'pending_executor' ? 'running' : run.status
  const updatedAt = patch.recorded_at
  const existingDraft = run.assistantDraft
  if (existingDraft?.streamId === patch.stream_id) {
    if (existingDraft.livePaused) return
    run.assistantDraft = {
      ...existingDraft,
      content: `${existingDraft.content}${patch.text}`,
      updatedAt,
      streamStep: patch.step,
      stepId: patch.step_id,
      liveSeqEnd: patch.live_seq_end,
      offsetEnd: patch.offset_end,
    }
    return
  }

  const segmentIndex = run.assistantSegments?.findIndex((segment) => segment.streamId === patch.stream_id) ?? -1
  if (segmentIndex >= 0 && run.assistantSegments) {
    const current = run.assistantSegments[segmentIndex]
    if (current.livePaused) return
    run.assistantSegments[segmentIndex] = {
      ...current,
      content: `${current.content}${patch.text}`,
      updatedAt,
      streamStep: patch.step,
      stepId: patch.step_id,
      liveSeqEnd: patch.live_seq_end,
      offsetEnd: patch.offset_end,
    }
    return
  }

  if (existingDraft?.content) {
    run.assistantSegments = upsertLiveAssistantSegment(run, existingDraft)
  }

  run.assistantDraft = {
    content: patch.text,
    updatedAt,
    timelineSeq: resolveLiveAssistantTimelineSeq(state, run, patch.session_id),
    streamId: patch.stream_id,
    streamStep: patch.step,
    stepId: patch.step_id,
    liveSeqEnd: patch.live_seq_end,
    offsetEnd: patch.offset_end,
    durableOffsetEnd: 0,
    livePaused: false,
  }
}

function resolveLiveAssistantTimelineSeq(state: DesktopV3CacheState, run: LiveRunOverlay, sessionId: string): number {
  let latestCommittedSeq = 0
  for (const message of state.messagesBySession[sessionId]?.items ?? []) {
    latestCommittedSeq = Math.max(latestCommittedSeq, finiteNumberValue(message.global_seq) ?? 0)
  }
  const runIntentSeq = state.runIntentsBySession[sessionId]?.[run.runId]?.event_seq ?? 0
  const currentRunIntent = state.currentRunIntentBySession[sessionId]
  const currentRunIntentSeq = currentRunIntent?.run_id === run.runId ? currentRunIntent.event_seq ?? 0 : 0
  const projection = state.projectionsBySession[sessionId]
  return Math.max(
    Math.max(
      latestCommittedSeq,
      runIntentSeq,
      currentRunIntentSeq,
      projection?.last_event_seq ?? 0,
      projection?.projection_high_watermark_seq ?? 0,
      run.lastEventSeqSeen ?? 0,
    ) + 1,
    run.timelineFloor ?? 0,
  )
}

function upsertLiveAssistantSegment(
  run: LiveRunOverlay,
  draft: NonNullable<LiveRunOverlay['assistantDraft']>,
): NonNullable<LiveRunOverlay['assistantSegments']> {
  const streamId = draft.streamId
  const existing = run.assistantSegments ?? []
  if (!streamId) {
    return appendLiveAssistantOverlaySegment(run, draft)
  }
  const segment = {
    id: `live-assistant:${run.runId}:${streamId}`,
    content: draft.content,
    createdAt: draft.updatedAt || Date.now(),
    updatedAt: draft.updatedAt,
    timelineSeq: draft.timelineSeq,
    streamId,
    streamStep: draft.streamStep,
    stepId: draft.stepId,
    liveSeqEnd: draft.liveSeqEnd,
    offsetEnd: draft.offsetEnd,
    durableOffsetEnd: draft.durableOffsetEnd,
    livePaused: draft.livePaused,
  }
  const index = existing.findIndex((item) => item.streamId === streamId)
  if (index < 0) return [...existing, segment]
  const next = existing.slice()
  next[index] = segment
  return next
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

function findCompatibleLiveReasoningOverlay(
  liveRun: LiveRunOverlay,
  payload: Record<string, unknown>,
  proposedKey: string,
): LiveRunReasoningOverlay | undefined {
  const byKey = liveRun.reasoningByKey ?? {}
  if (byKey[proposedKey]) return byKey[proposedKey]

  const reasoningId = stringValue(payload.reasoning_id).trim()
  const reasoningKey = stringValue(payload.reasoning_key).trim()
  const stepId = stringValue(payload.step_id).trim()
  const step = finiteNumberValue(payload.step)
  const compatible = Object.values(byKey).find((candidate) => {
    if (reasoningId && candidate.reasoningId) return reasoningId === candidate.reasoningId
    if (stepId && candidate.stepId) return stepId === candidate.stepId
    if (reasoningKey && candidate.reasoningKey) return reasoningKey === candidate.reasoningKey
    if (step !== undefined && candidate.step !== undefined) return step === candidate.step
    return false
  })
  if (compatible) return compatible

  const current = liveRun.reasoning
  if (!current) return undefined
  if (reasoningId && current.reasoningId && reasoningId !== current.reasoningId) return undefined
  if (stepId && current.stepId && stepId !== current.stepId) return undefined
  if (reasoningKey && current.reasoningKey && reasoningKey !== current.reasoningKey) return undefined
  if (step !== undefined && current.step !== undefined && step !== current.step) return undefined
  return current
}

function applyLiveReasoningOverlay(
  liveRun: LiveRunOverlay,
  payload: Record<string, unknown>,
  eventType: string,
  eventSeq: number,
  updatedAt: number,
): void {
  const proposedKey = reasoningOverlayKey(payload)
  const byKey = liveRun.reasoningByKey ?? {}
  const existing = findCompatibleLiveReasoningOverlay(liveRun, payload, proposedKey)
  const key = existing?.key || proposedKey
  const current: LiveRunReasoningOverlay = existing ?? {
    key,
    state: 'running',
    summary: '',
    text: '',
    startedAt: updatedAt,
    completedAt: null,
    updatedAt,
    timelineSeq: Math.max(eventSeq, liveRun.timelineFloor ?? 0),
    updatedSeq: eventSeq,
  }
  const isStarted = eventType === 'session.reasoning.started'
  const isCompleted = eventType === 'session.reasoning.completed'
  const isError = eventType === 'session.reasoning.failed' || eventType === 'session.reasoning.error'
  const explicitTextSnapshot = stringValue(payload.text)
  const canonicalDelta = stringValue(payload.delta)
  const deltaMode = stringValue(payload.delta_mode).trim()
  const textDelta = stringValue(payload.text_delta)
  const summarySnapshot = stringValue(payload.summary)
  const summaryDelta = stringValue(payload.summary_delta)
  const nextState: LiveRunReasoningOverlay['state'] = isError ? 'error' : isCompleted ? 'completed' : 'running'
  const nextSummary = summarySnapshot || (summaryDelta ? `${current.summary}${summaryDelta}` : current.summary)
  const nextText = explicitTextSnapshot
    || (canonicalDelta ? deltaMode === 'append' ? `${current.text}${canonicalDelta}` : canonicalDelta : '')
    || (textDelta ? `${current.text}${textDelta}` : current.text)
  const next: LiveRunReasoningOverlay = {
    ...current,
    key,
    reasoningId: stringValue(payload.reasoning_id) || current.reasoningId,
    reasoningKey: stringValue(payload.reasoning_key) || current.reasoningKey,
    stepId: stringValue(payload.step_id) || current.stepId,
    step: numberValue(payload.step) || current.step,
    state: nextState,
    summary: nextSummary,
    text: nextText,
    startedAt: current.startedAt ?? updatedAt,
    completedAt: isCompleted || isError ? updatedAt : current.completedAt ?? null,
    updatedAt,
    timelineSeq: Math.max(current.timelineSeq || eventSeq, liveRun.timelineFloor ?? 0),
    updatedSeq: eventSeq,
  }
  if (isStarted && existing) {
    next.text = existing.text
    next.summary = existing.summary
    next.state = existing.state === 'completed' || existing.state === 'error' ? existing.state : 'running'
    next.completedAt = existing.completedAt
  }
  if (existing && liveReasoningOverlayEqual(existing, next)) {
    byKey[key] = existing
    liveRun.reasoningByKey = byKey
    liveRun.reasoning = existing
    liveRun.status = liveRun.status === 'pending_executor' ? 'running' : liveRun.status
    return
  }
  byKey[key] = next
  liveRun.reasoningByKey = byKey
  liveRun.reasoning = next
  liveRun.status = liveRun.status === 'pending_executor' ? 'running' : liveRun.status
}

function liveReasoningOverlayEqual(left: LiveRunReasoningOverlay, right: LiveRunReasoningOverlay): boolean {
  return left.key === right.key
    && left.reasoningId === right.reasoningId
    && left.reasoningKey === right.reasoningKey
    && left.stepId === right.stepId
    && left.step === right.step
    && left.state === right.state
    && left.summary === right.summary
    && left.text === right.text
    && left.startedAt === right.startedAt
    && left.completedAt === right.completedAt
    && left.updatedAt === right.updatedAt
    && left.timelineSeq === right.timelineSeq
    && left.updatedSeq === right.updatedSeq
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
  const content = draft.content
  if (!content.trim()) return liveRun.assistantSegments ?? []
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

function raiseSameStepSpeculativeAssistantAfterReasoning(
  liveRun: LiveRunOverlay,
  reasoningPayload: Record<string, unknown>,
  reasoningSeq: number,
): void {
  if (reasoningSeq <= 0) return
  const reasoningStepId = stringValue(reasoningPayload.step_id) || undefined
  const reasoningStep = finiteNumberValue(reasoningPayload.step)
  const timelineSeq = reasoningSeq + 1
  const raise = <T extends LiveAssistantStreamNode>(node: T): T => {
    if (!node.streamId || (node.durableOffsetEnd ?? 0) > 0) return node
    if (!assistantNodeMatchesReasoningStep(node, reasoningStepId, reasoningStep)) return node
    if ((node.timelineSeq ?? 0) >= timelineSeq) return node
    return { ...node, timelineSeq }
  }

  if (liveRun.assistantDraft) liveRun.assistantDraft = raise(liveRun.assistantDraft)
  if (liveRun.assistantSegments) liveRun.assistantSegments = liveRun.assistantSegments.map(raise)
}

function assistantNodeMatchesReasoningStep(
  node: LiveAssistantStreamNode,
  reasoningStepId: string | undefined,
  reasoningStep: number | undefined,
): boolean {
  if (reasoningStepId && node.stepId) return reasoningStepId === node.stepId
  if (reasoningStep !== undefined && node.streamStep !== undefined) return reasoningStep === node.streamStep
  // Missing metadata cannot establish a different provider step. Preserve the
  // low-latency ordering correction unless an explicit step mismatch disproves it.
  if (reasoningStepId && node.stepId !== undefined) return false
  if (reasoningStep !== undefined && node.streamStep !== undefined) return false
  return true
}

function flushLiveAssistantDraftToSegment(liveRun: LiveRunOverlay): void {
  const draft = liveRun.assistantDraft
  if (!draft?.content.trim()) return
  liveRun.assistantSegments = draft.streamId
    ? upsertLiveAssistantSegment(liveRun, draft)
    : appendLiveAssistantOverlaySegment(liveRun, draft)
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

function mergeHistoryChunksForSessionIds(
  state: DesktopV3CacheState,
  source: Record<string, unknown> | undefined,
  sessionIds: Set<string>,
): void {
  if (!source) return
  const allowedChunkIds = new Set<string>()
  for (const sessionId of sessionIds) {
    for (const chunkId of historyChunkIds(state.historyManifestsBySession[sessionId])) {
      allowedChunkIds.add(chunkId)
    }
  }
  for (const [chunkId, chunk] of Object.entries(source)) {
    if (allowedChunkIds.has(chunkId)) state.historyChunksById[chunkId] = chunk
  }
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

function finiteNumberValue(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined
}

function isTaskStreamSnapshotOutput(toolName: string | undefined, output: string): boolean {
  if ((toolName ?? '').trim().toLowerCase() !== 'task') return false
  const parsed = parseJsonRecord(output)
  if (!parsed) return false
  return stringValue(parsed.path_id) === 'tool.task.stream.v1' && stringValue(parsed.tool) === 'task'
}

function applyTaskStreamPatch(
  tool: LiveRunOverlay['toolCallsByCallId'][string],
  output: string,
  updatedAt: number,
): boolean {
  if ((tool.toolName ?? '').trim().toLowerCase() !== 'task') return false
  const parsed = parseJsonRecord(output)
  if (!parsed) return false
  if (stringValue(parsed.path_id) !== 'tool.task.stream.v2' || stringValue(parsed.tool) !== 'task') return false
  const launchPatch = recordValue(parsed.launch)
  if (!launchPatch) return false
  const launchKey = stringValue(parsed.launch_key)
    || stringValue(launchPatch.launch_key)
    || stringValue(parsed.child_session_id)
    || stringValue(launchPatch.child_session_id)
    || (numberValue(parsed.launch_index) > 0 ? `launch:${numberValue(parsed.launch_index)}` : '')
    || (numberValue(launchPatch.launch_index) > 0 ? `launch:${numberValue(launchPatch.launch_index)}` : '')
  if (!launchKey) return false

  const stream = tool.taskStream ?? {
    pathId: 'tool.task.stream.v2',
    streamVersion: 2,
    updatedAt,
    launchesByKey: {},
    launchOrder: [],
  }
  const existing = stream.launchesByKey[launchKey] ?? {}
  stream.pathId = 'tool.task.stream.v2'
  stream.streamVersion = 2
  stream.status = stringValue(parsed.status) || stream.status
  stream.phase = stringValue(parsed.phase) || stream.phase
  stream.action = stringValue(parsed.action) || stream.action
  stream.description = stringValue(parsed.description) || stream.description
  stream.goal = stringValue(parsed.goal) || stream.goal
  stream.parentSessionId = stringValue(parsed.parent_session_id) || stream.parentSessionId
  stream.taskCallId = stringValue(parsed.task_call_id) || stream.taskCallId
  stream.launchCount = numberValue(parsed.launch_count) || stream.launchCount
  stream.updatedAt = updatedAt
  stream.launchesByKey[launchKey] = mergeTaskStreamLaunchPatch(existing, launchPatch, launchKey)
  if (!stream.launchOrder.includes(launchKey)) {
    stream.launchOrder = [...stream.launchOrder, launchKey]
  }
  stream.launchOrder = [...stream.launchOrder].sort((left, right) => {
    const leftIndex = numberValue(stream.launchesByKey[left]?.launch_index)
    const rightIndex = numberValue(stream.launchesByKey[right]?.launch_index)
    if (leftIndex && rightIndex && leftIndex !== rightIndex) return leftIndex - rightIndex
    if (leftIndex && !rightIndex) return -1
    if (!leftIndex && rightIndex) return 1
    return left.localeCompare(right)
  })
  tool.taskStream = stream
  return true
}

function mergeTaskStreamLaunchPatch(
  existing: Record<string, unknown>,
  launchPatch: Record<string, unknown>,
  launchKey: string,
): Record<string, unknown> {
  const merged = { ...existing, ...launchPatch, launch_key: launchKey }
  if (!stringValue(launchPatch.current_tool) && stringValue(existing.current_tool)) {
    merged.current_tool = existing.current_tool
    merged.current_tool_identity = existing.current_tool_identity
    merged.current_tool_run_count = existing.current_tool_run_count
    merged.current_tool_display = existing.current_tool_display
    merged.current_tool_started_at_ms = existing.current_tool_started_at_ms
    merged.current_tool_ms = existing.current_tool_ms
  }
  return merged
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
