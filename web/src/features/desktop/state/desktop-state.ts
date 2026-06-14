import type {
  AgentModelPolicyRecord,
  ChatMessageRecord,
  DesktopSessionPlanRecord,
  DesktopSessionPlanRevisionRecord,
  ResolvedSessionPreference,
} from '../chat/types/chat'
import { dedupeAndTrimMessages, mergeMessageIntoCache } from '../chat/services/message-cache'
import { parseStructuredToolMessage } from '../chat/services/tool-message'
import { countApprovalRequiredPermissions } from '../permissions/services/permission-payload'
import { appendLiveAssistantSegment } from './live-assistant-segments'
import { applyCanonicalReasoningEventToLiveHistory, completeLiveReasoningHistory } from './live-reasoning-history'
import { mergeSessionRecords } from './session-records'
import type {
  DesktopNotificationCenterRecord,
  DesktopNotificationSummary,
  DesktopPermissionRecord,
  DesktopLiveToolRecord,
  DesktopRunIntentRecord,
  DesktopSessionRecord,
  DesktopSessionUsageRecord,
} from '../types/realtime'

export type DesktopStateStatus = 'idle' | 'loading' | 'ready' | 'stale' | 'error'

export interface DesktopWorkspaceRecord {
  workspacePath: string
  workspaceName: string
  sessionIds: string[]
  updatedAt: number
}

export interface DesktopSessionReadinessRecord {
  sessionId: string
  status: 'loading' | 'ready' | 'omitted' | 'missing' | 'error'
  ready: boolean
  missingResources: string[]
  omittedResources: string[]
  error: string | null
  updatedAt: number
}

export interface DesktopState {
  rev: number
  status: DesktopStateStatus
  staleReason: string | null
  resyncRequested: boolean
  lastError: string | null
  sessionsById: Record<string, DesktopSessionRecord>
  sessionOrder: string[]
  messagesBySessionId: Record<string, ChatMessageRecord[]>
  permissionsById: Record<string, DesktopPermissionRecord>
  plansBySessionId: Record<string, DesktopSessionPlanRecord | null>
  planRevisionsBySessionId: Record<string, DesktopSessionPlanRevisionRecord[]>
  usageBySessionId: Record<string, DesktopSessionUsageRecord>
  runIntentsBySessionId: Record<string, DesktopRunIntentRecord>
  workspacesByPath: Record<string, DesktopWorkspaceRecord>
  notificationsById: Record<string, DesktopNotificationCenterRecord>
  notificationSummary: DesktopNotificationSummary
  preferencesBySessionId: Record<string, ResolvedSessionPreference>
  agentModelPolicyBySessionId: Record<string, AgentModelPolicyRecord | null>
  routeReadinessBySessionId: Record<string, DesktopSessionReadinessRecord>
}

export interface DesktopDaemonSnapshot {
  rev: number
  snapshotEndpointCursor?: string
  sessionsById?: Record<string, DesktopSessionRecord>
  sessionOrder?: string[]
  messagesBySessionId?: Record<string, ChatMessageRecord[]>
  permissionsById?: Record<string, DesktopPermissionRecord>
  plansBySessionId?: Record<string, DesktopSessionPlanRecord | null>
  planRevisionsBySessionId?: Record<string, DesktopSessionPlanRevisionRecord[]>
  usageBySessionId?: Record<string, DesktopSessionUsageRecord>
  runIntentsBySessionId?: Record<string, DesktopRunIntentRecord>
  workspacesByPath?: Record<string, DesktopWorkspaceRecord>
  notificationsById?: Record<string, DesktopNotificationCenterRecord>
  notificationSummary?: DesktopNotificationSummary
  preferencesBySessionId?: Record<string, ResolvedSessionPreference>
  agentModelPolicyBySessionId?: Record<string, AgentModelPolicyRecord | null>
  routeReadinessBySessionId?: Record<string, DesktopSessionReadinessRecord>
}

export interface DesktopDaemonEvent {
  rev: number
  prevRev: number
  type: string
  payload?: unknown
  stream?: string
  entityId?: string
  globalSeq?: number
  sourceSeq?: number
  tsUnixMs?: number
}

export type DesktopStateAction =
  | { type: 'snapshot/replace'; snapshot: DesktopDaemonSnapshot }
  | { type: 'snapshot/merge'; snapshot: DesktopDaemonSnapshot }
  | { type: 'daemon/event'; event: DesktopDaemonEvent }
  | { type: 'connection/stale'; reason: string }
  | { type: 'connection/status'; status: DesktopStateStatus; error?: string | null }

const EMPTY_NOTIFICATION_SUMMARY: DesktopNotificationSummary = {
  swarmID: '',
  totalCount: 0,
  unreadCount: 0,
  activeCount: 0,
  updatedAt: 0,
}

export function createEmptyDesktopState(): DesktopState {
  return {
    rev: 0,
    status: 'loading',
    staleReason: 'snapshot not loaded',
    resyncRequested: true,
    lastError: null,
    sessionsById: {},
    sessionOrder: [],
    messagesBySessionId: {},
    permissionsById: {},
    plansBySessionId: {},
    planRevisionsBySessionId: {},
    usageBySessionId: {},
    runIntentsBySessionId: {},
    workspacesByPath: {},
    notificationsById: {},
    notificationSummary: { ...EMPTY_NOTIFICATION_SUMMARY },
    preferencesBySessionId: {},
    agentModelPolicyBySessionId: {},
    routeReadinessBySessionId: {},
  }
}

export function desktopReducer(state: DesktopState, action: DesktopStateAction): DesktopState {
  switch (action.type) {
    case 'snapshot/replace':
      return replaceFromSnapshot(state, action.snapshot)
    case 'snapshot/merge':
      return mergeFromSnapshot(state, action.snapshot)
    case 'daemon/event':
      return applyDaemonEvent(state, action.event)
    case 'connection/stale':
      return markStale(state, action.reason)
    case 'connection/status':
      return {
        ...state,
        status: action.status,
        lastError: action.error ?? (action.status === 'error' ? state.lastError : null),
      }
    default:
      return state
  }
}

function replaceFromSnapshot(state: DesktopState, snapshot: DesktopDaemonSnapshot): DesktopState {
  if (!isValidRevision(snapshot.rev)) {
    return markStale(state, 'snapshot missing valid rev')
  }

  const usageBySessionId = cloneRecord(snapshot.usageBySessionId)
  const sessionsById = attachUsageToSessions(cloneRecord(snapshot.sessionsById), usageBySessionId)

  return {
    rev: snapshot.rev,
    status: 'ready',
    staleReason: null,
    resyncRequested: false,
    lastError: null,
    sessionsById,
    sessionOrder: normalizeSessionOrder(sessionsById, snapshot.sessionOrder),
    messagesBySessionId: cloneMessageRecord(snapshot.messagesBySessionId),
    permissionsById: cloneRecord(snapshot.permissionsById),
    plansBySessionId: cloneRecord(snapshot.plansBySessionId),
    planRevisionsBySessionId: cloneArrayRecord(snapshot.planRevisionsBySessionId),
    usageBySessionId,
    runIntentsBySessionId: cloneRecord(snapshot.runIntentsBySessionId),
    workspacesByPath: cloneWorkspaceRecord(snapshot.workspacesByPath),
    notificationsById: cloneRecord(snapshot.notificationsById),
    notificationSummary: snapshot.notificationSummary ? { ...snapshot.notificationSummary } : { ...EMPTY_NOTIFICATION_SUMMARY },
    preferencesBySessionId: cloneRecord(snapshot.preferencesBySessionId),
    agentModelPolicyBySessionId: cloneRecord(snapshot.agentModelPolicyBySessionId),
    routeReadinessBySessionId: cloneReadinessRecord(snapshot.routeReadinessBySessionId),
  }
}

function mergeFromSnapshot(state: DesktopState, snapshot: DesktopDaemonSnapshot): DesktopState {
  if (!isValidRevision(snapshot.rev)) {
    return markStale(state, 'snapshot missing valid rev')
  }

  const incomingUsageBySessionId = cloneRecord(snapshot.usageBySessionId)
  const incomingSessionsById = attachUsageToSessions(cloneRecord(snapshot.sessionsById), incomingUsageBySessionId)
  const sessionsById = { ...state.sessionsById }
  for (const [sessionId, incomingSession] of Object.entries(incomingSessionsById)) {
    sessionsById[sessionId] = mergeSnapshotSessionRecord(state.sessionsById[sessionId] ?? null, incomingSession)
  }
  const runIntentScopedSessionIds = Object.entries(incomingSessionsById)
    .filter(([sessionId, incomingSession]) => {
      const existing = state.sessionsById[sessionId]
      return !existing || sessionSequence(existing) <= sessionSequence(incomingSession)
    })
    .map(([sessionId]) => sessionId)
  const runIntentsBySessionId = mergeRunIntentRecord(state.runIntentsBySessionId, snapshot.runIntentsBySessionId, runIntentScopedSessionIds)

  return {
    ...state,
    rev: Math.max(state.rev, snapshot.rev),
    status: 'ready',
    staleReason: null,
    resyncRequested: false,
    lastError: null,
    sessionsById,
    sessionOrder: normalizeSessionOrder(sessionsById, mergeSessionOrder(state.sessionOrder, snapshot.sessionOrder)),
    messagesBySessionId: mergeMessageRecord(state.messagesBySessionId, snapshot.messagesBySessionId),
    permissionsById: {
      ...state.permissionsById,
      ...cloneRecord(snapshot.permissionsById),
    },
    plansBySessionId: {
      ...state.plansBySessionId,
      ...cloneRecord(snapshot.plansBySessionId),
    },
    planRevisionsBySessionId: mergeArrayRecord(state.planRevisionsBySessionId, snapshot.planRevisionsBySessionId),
    usageBySessionId: {
      ...state.usageBySessionId,
      ...incomingUsageBySessionId,
    },
    runIntentsBySessionId,
    workspacesByPath: mergeWorkspaceRecord(state.workspacesByPath, snapshot.workspacesByPath),
    notificationsById: {
      ...state.notificationsById,
      ...cloneRecord(snapshot.notificationsById),
    },
    notificationSummary: snapshot.notificationSummary ? { ...snapshot.notificationSummary } : state.notificationSummary,
    preferencesBySessionId: {
      ...state.preferencesBySessionId,
      ...cloneRecord(snapshot.preferencesBySessionId),
    },
    agentModelPolicyBySessionId: {
      ...state.agentModelPolicyBySessionId,
      ...cloneRecord(snapshot.agentModelPolicyBySessionId),
    },
    routeReadinessBySessionId: mergeReadinessRecord(state.routeReadinessBySessionId, snapshot.routeReadinessBySessionId),
  }
}

function mergeSnapshotSessionRecord(existing: DesktopSessionRecord | null, incoming: DesktopSessionRecord): DesktopSessionRecord {
  const merged = mergeSessionRecords(existing, incoming)
  if (!existing) {
    return merged
  }
  if (sessionSequence(existing) <= sessionSequence(incoming)) {
    return merged
  }
  return {
    ...merged,
    lifecycle: existing.lifecycle,
    runIntent: existing.runIntent,
    live: existing.live,
  }
}

function sessionSequence(session: DesktopSessionRecord): number {
  return Math.max(session.lastEventSeq ?? 0, session.projectionHighWatermarkSeq ?? 0, session.live.seq ?? 0)
}

function applyDaemonEvent(state: DesktopState, event: DesktopDaemonEvent): DesktopState {
  if (!isValidRevision(event.rev) || !isValidRevision(event.prevRev)) {
    return markStale(state, 'daemon event missing valid rev metadata')
  }

  if (event.rev <= state.rev) {
    return state
  }

  if (event.prevRev !== state.rev) {
    return markStale(state, `daemon event rev mismatch: expected prevRev ${state.rev}, received ${event.prevRev}`)
  }

  const applied = applyDaemonPayload(state, event)
  if (!applied) {
    return markStale(state, `unsupported daemon event type: ${event.type}`)
  }
  return {
    ...applied,
    rev: event.rev,
    status: state.status === 'loading' ? 'ready' : state.status,
    staleReason: null,
    resyncRequested: false,
    lastError: null,
  }
}

function applyDaemonPayload(state: DesktopState, event: DesktopDaemonEvent): DesktopState | null {
  const payload = asRecord(event.payload)
  if (!payload) {
    return null
  }

  switch (event.type) {
    case 'desktop/session/upsert':
      return upsertSessionPayload(state, payload)
    case 'desktop/session/delete':
      return deleteSessionPayload(state, payload)
    case 'desktop/messages/replace':
      return replaceMessagesPayload(state, payload)
    case 'desktop/message/upsert':
      return upsertMessagePayload(state, payload)
    case 'desktop/message/delete':
      return deleteMessagePayload(state, payload)
    case 'desktop/permission/upsert':
      return upsertByIdPayload(state, payload, 'permission', 'permissionsById')
    case 'desktop/permission/delete':
      return deleteByIdPayload(state, payload, 'permissionId', 'permissionsById')
    case 'desktop/plan/set':
      return setSessionValuePayload(state, payload, 'plan', 'plansBySessionId')
    case 'desktop/plan-revisions/replace':
      return replaceSessionArrayPayload(state, payload, 'revisions', 'planRevisionsBySessionId')
    case 'desktop/usage/set':
      return applyUsageSetPayload(state, payload)
    case 'run.usage.updated':
      return applyUsageUpdatedPayload(state, payload)
    case 'desktop/run-intent/set':
      return setSessionValuePayload(state, payload, 'runIntent', 'runIntentsBySessionId')
    case 'desktop/workspace/upsert':
      return upsertWorkspacePayload(state, payload)
    case 'desktop/notification/upsert':
      return upsertByIdPayload(state, payload, 'notification', 'notificationsById')
    case 'desktop/notification/delete':
      return deleteByIdPayload(state, payload, 'notificationId', 'notificationsById')
    case 'desktop/notification-summary/set':
      return setNotificationSummaryPayload(state, payload)
    case 'desktop/preference/set':
      return setSessionValuePayload(state, payload, 'preference', 'preferencesBySessionId')
    case 'desktop/agent-model-policy/set':
      return setSessionValuePayload(state, payload, 'policy', 'agentModelPolicyBySessionId')
    case 'desktop/route-readiness/set':
      return setSessionValuePayload(state, payload, 'readiness', 'routeReadinessBySessionId')
    default:
      return applyDurableSessionPayload(state, event, payload)
  }
}

function applyUsageUpdatedPayload(state: DesktopState, payload: Record<string, unknown>): DesktopState {
  const usage = usageFromPayload(payload)
  const sessionId = usage?.sessionId || payloadString(payload, 'session_id')
  if (!sessionId) {
    return state
  }

  const usageBySessionId = usage
    ? {
        ...state.usageBySessionId,
        [sessionId]: usage,
      }
    : state.usageBySessionId
  const existing = state.sessionsById[sessionId]
  if (!existing) {
    return {
      ...state,
      usageBySessionId,
    }
  }

  const eventSeq = payloadNumber(payload, 'source_seq') || payloadNumber(payload, 'global_seq')
  const ts = payloadNumber(payload, 'ts_unix_ms') || Date.now()
  const session: DesktopSessionRecord = {
    ...existing,
    usage: usage ?? existing.usage,
    updatedAt: Math.max(existing.updatedAt, usage?.updatedAt ?? 0, ts),
    lastEventSeq: eventSeq > 0 ? Math.max(existing.lastEventSeq ?? 0, eventSeq) : existing.lastEventSeq,
    projectionHighWatermarkSeq: eventSeq > 0 ? Math.max(existing.projectionHighWatermarkSeq ?? 0, eventSeq) : existing.projectionHighWatermarkSeq,
    live: {
      ...existing.live,
      lastEventType: 'run.usage.updated',
      lastEventAt: ts,
      seq: eventSeq > 0 ? Math.max(existing.live.seq ?? 0, eventSeq) : existing.live.seq,
    },
  }

  return {
    ...state,
    sessionsById: {
      ...state.sessionsById,
      [sessionId]: session,
    },
    sessionOrder: sortSessionOrder({ ...state.sessionsById, [sessionId]: session }),
    usageBySessionId,
    routeReadinessBySessionId: {
      ...state.routeReadinessBySessionId,
      [sessionId]: readySession(sessionId, ts),
    },
  }
}

function applyUsageSetPayload(state: DesktopState, payload: Record<string, unknown>): DesktopState | null {
  const sessionId = stringValue(payload.sessionId)
  if (!sessionId) {
    return null
  }

  const value = payload.usage
  if (value === undefined) {
    return null
  }

  if (value === null) {
    const existing = state.sessionsById[sessionId]
    return {
      ...state,
      sessionsById: existing
        ? {
            ...state.sessionsById,
            [sessionId]: {
              ...existing,
              usage: null,
            },
          }
        : state.sessionsById,
      usageBySessionId: omitKey(state.usageBySessionId, sessionId),
    }
  }

  const usage = value as DesktopSessionUsageRecord
  const existing = state.sessionsById[sessionId]
  return {
    ...state,
    sessionsById: existing
      ? {
          ...state.sessionsById,
          [sessionId]: {
            ...existing,
            usage,
            updatedAt: Math.max(existing.updatedAt, usage.updatedAt),
          },
        }
      : state.sessionsById,
    usageBySessionId: {
      ...state.usageBySessionId,
      [sessionId]: usage,
    },
  }
}

function markStale(state: DesktopState, reason: string): DesktopState {
  return {
    ...state,
    status: 'stale',
    staleReason: reason,
    resyncRequested: true,
  }
}

function upsertSessionPayload(state: DesktopState, payload: Record<string, unknown>): DesktopState | null {
  const session = payload.session
  if (!isObjectWithStringId(session)) {
    return null
  }

  const sessionsById = {
    ...state.sessionsById,
    [session.id]: session as unknown as DesktopSessionRecord,
  }

  return {
    ...state,
    sessionsById,
    sessionOrder: sortSessionOrder(sessionsById),
  }
}

function deleteSessionPayload(state: DesktopState, payload: Record<string, unknown>): DesktopState | null {
  const sessionId = stringValue(payload.sessionId)
  if (!sessionId) {
    return null
  }

  const sessionsById = omitKey(state.sessionsById, sessionId)
  return {
    ...state,
    sessionsById,
    sessionOrder: state.sessionOrder.filter((id) => id !== sessionId),
    messagesBySessionId: omitKey(state.messagesBySessionId, sessionId),
    plansBySessionId: omitKey(state.plansBySessionId, sessionId),
    planRevisionsBySessionId: omitKey(state.planRevisionsBySessionId, sessionId),
    usageBySessionId: omitKey(state.usageBySessionId, sessionId),
    runIntentsBySessionId: omitKey(state.runIntentsBySessionId, sessionId),
    preferencesBySessionId: omitKey(state.preferencesBySessionId, sessionId),
    agentModelPolicyBySessionId: omitKey(state.agentModelPolicyBySessionId, sessionId),
    routeReadinessBySessionId: omitKey(state.routeReadinessBySessionId, sessionId),
  }
}

function replaceMessagesPayload(state: DesktopState, payload: Record<string, unknown>): DesktopState | null {
  const sessionId = stringValue(payload.sessionId)
  if (!sessionId || !Array.isArray(payload.messages)) {
    return null
  }

  return {
    ...state,
    messagesBySessionId: {
      ...state.messagesBySessionId,
      [sessionId]: dedupeAndTrimMessages(payload.messages as ChatMessageRecord[]),
    },
  }
}

function upsertMessagePayload(state: DesktopState, payload: Record<string, unknown>): DesktopState | null {
  const message = payload.message
  if (!isObjectWithStringId(message) || !stringValue(message.sessionId)) {
    return null
  }

  const typedMessage = message as unknown as ChatMessageRecord
  return {
    ...state,
    messagesBySessionId: {
      ...state.messagesBySessionId,
      [typedMessage.sessionId]: mergeMessageIntoCache(state.messagesBySessionId[typedMessage.sessionId], typedMessage),
    },
  }
}

function deleteMessagePayload(state: DesktopState, payload: Record<string, unknown>): DesktopState | null {
  const sessionId = stringValue(payload.sessionId)
  const messageId = stringValue(payload.messageId)
  if (!sessionId || !messageId) {
    return null
  }

  return {
    ...state,
    messagesBySessionId: {
      ...state.messagesBySessionId,
      [sessionId]: (state.messagesBySessionId[sessionId] ?? []).filter((message) => message.id !== messageId),
    },
  }
}

function upsertWorkspacePayload(state: DesktopState, payload: Record<string, unknown>): DesktopState | null {
  const workspace = payload.workspace
  if (!isRecord(workspace) || !stringValue(workspace.workspacePath)) {
    return null
  }

  const typedWorkspace = workspace as unknown as DesktopWorkspaceRecord
  return {
    ...state,
    workspacesByPath: {
      ...state.workspacesByPath,
      [typedWorkspace.workspacePath]: cloneWorkspace(typedWorkspace),
    },
  }
}

function upsertByIdPayload<StateKey extends 'permissionsById' | 'notificationsById'>(
  state: DesktopState,
  payload: Record<string, unknown>,
  payloadKey: string,
  stateKey: StateKey,
): DesktopState | null {
  const record = payload[payloadKey]
  if (!isObjectWithStringId(record)) {
    return null
  }

  return {
    ...state,
    [stateKey]: {
      ...state[stateKey],
      [record.id]: record,
    },
  }
}

function deleteByIdPayload<StateKey extends 'permissionsById' | 'notificationsById'>(
  state: DesktopState,
  payload: Record<string, unknown>,
  payloadKey: string,
  stateKey: StateKey,
): DesktopState | null {
  const id = stringValue(payload[payloadKey])
  if (!id) {
    return null
  }

  return {
    ...state,
    [stateKey]: omitKey(state[stateKey] as unknown as Record<string, never>, id) as DesktopState[StateKey],
  }
}

function setSessionValuePayload<
  StateKey extends
    | 'plansBySessionId'
    | 'usageBySessionId'
    | 'runIntentsBySessionId'
    | 'preferencesBySessionId'
    | 'agentModelPolicyBySessionId'
    | 'routeReadinessBySessionId',
>(state: DesktopState, payload: Record<string, unknown>, payloadKey: string, stateKey: StateKey): DesktopState | null {
  const sessionId = stringValue(payload.sessionId)
  if (!sessionId) {
    return null
  }

  const value = payload[payloadKey]
  if (value === undefined) {
    return null
  }

  if (value === null) {
    return {
      ...state,
      [stateKey]: omitKey(state[stateKey] as unknown as Record<string, never>, sessionId) as DesktopState[StateKey],
    }
  }

  return {
    ...state,
    [stateKey]: {
      ...state[stateKey],
      [sessionId]: value,
    },
  }
}

function applyDurableSessionPayload(state: DesktopState, event: DesktopDaemonEvent, rawPayload: Record<string, unknown>): DesktopState | null {
  const eventType = event.type
  if (eventType.startsWith('session.diagnostic')) {
    return state
  }
  if (!eventType.startsWith('session.') && !eventType.startsWith('permission.')) {
    return null
  }
  const payload = normalizeDurablePayload(eventType, rawPayload)
  const sessionId = durableSessionIdFromEvent(eventType, payload, event)
  if (!sessionId) {
    return null
  }

  let next = state
  const currentSession = ensureReducerSession(next, sessionId)
  const session = durableSessionPatch(currentSession, eventType, payload, sessionId)
  next = {
    ...next,
    sessionsById: {
      ...next.sessionsById,
      [sessionId]: session,
    },
    sessionOrder: sortSessionOrder({ ...next.sessionsById, [sessionId]: session }),
    routeReadinessBySessionId: {
      ...next.routeReadinessBySessionId,
      [sessionId]: readySession(sessionId, Date.now()),
    },
  }

  const messages = durableMessages(eventType, payload, sessionId)
  if (messages.length > 0) {
    next = mergeReducerMessages(next, sessionId, messages)
  }

  const permission = durablePermission(payload)
  if (permission) {
    next = applyReducerPermission(next, permission, sessionId)
  }

  const preference = durablePreference(payload)
  if (preference) {
    next = {
      ...next,
      preferencesBySessionId: {
        ...next.preferencesBySessionId,
        [sessionId]: preference,
      },
    }
  }

  const runIntent = durableRunIntent(payload, eventType, sessionId)
  if (runIntent) {
    const runIntentsBySessionId = { ...next.runIntentsBySessionId }
    if (runIntentStatusTerminal(runIntent.status)) {
      delete runIntentsBySessionId[runIntent.sessionId]
    } else {
      runIntentsBySessionId[runIntent.sessionId] = runIntent
    }
    next = { ...next, runIntentsBySessionId }
  }
  const lifecycle = durableLifecycle(payload, sessionId)
  if (lifecycle && !lifecycle.active) {
    const runIntentsBySessionId = { ...next.runIntentsBySessionId }
    delete runIntentsBySessionId[sessionId]
    next = { ...next, runIntentsBySessionId }
  }

  return next
}

function durableSessionIdFromEvent(eventType: string, payload: Record<string, unknown>, event: DesktopDaemonEvent): string {
  const envelopeSessionId = eventType.startsWith('session.') || eventType.startsWith('permission.')
    ? payloadString({ entity_id: event.entityId }, 'entity_id') || durableSessionIdFromStream(event.stream)
    : ''
  if (envelopeSessionId) {
    return envelopeSessionId
  }
  return payloadString(payload, 'session_id')
    || (eventType.startsWith('session.') ? payloadString(payload, 'id') : '')
    || payloadString(payloadRecord(payload, 'session'), 'id')
    || payloadString(payloadRecord(payload, 'message'), 'session_id')
    || payloadString(payloadRecord(payload, 'permission'), 'session_id')
}

function durableSessionIdFromStream(stream: unknown): string {
  const value = typeof stream === 'string' ? stream.trim() : ''
  const sessionPrefix = 'session:'
  if (value.startsWith(sessionPrefix)) {
    return value.slice(sessionPrefix.length).trim()
  }
  const v3SessionPrefix = 'v3/session:'
  if (value.startsWith(v3SessionPrefix)) {
    return value.slice(v3SessionPrefix.length).trim()
  }
  return ''
}

function normalizeDurablePayload(eventType: string, payload: Record<string, unknown>): Record<string, unknown> {
  if (!eventType.startsWith('session.')) {
    return payload
  }
  const normalized: Record<string, unknown> = { ...payload }
  const nestedSession = payloadRecord(normalized, 'session')
  const nestedMessage = payloadRecord(normalized, 'message')
  const nestedLifecycle = payloadRecord(normalized, 'lifecycle')
  const nestedRunIntent = payloadRecord(normalized, 'run_intent')
  if (typeof normalized.session_id !== 'string') {
    normalized.session_id = payloadString(nestedSession, 'id')
      || payloadString(nestedMessage, 'session_id')
      || payloadString(nestedLifecycle, 'session_id')
      || payloadString(nestedRunIntent, 'session_id')
      || normalized.session_id
  }
  if ((eventType === 'session.created' || eventType === 'session.updated') && nestedSession) {
    return { ...nestedSession, ...normalized, session: nestedSession }
  }
  if (eventType === 'session.run_intent.recorded' && nestedRunIntent) {
    const status = payloadString(nestedRunIntent, 'status').toLowerCase()
    if (status === 'pending_executor') {
      normalized.status = 'starting'
      normalized.summary = normalized.summary ?? 'Pending executor…'
    } else if (status === 'dispatch_blocked') {
      normalized.status = 'blocked'
      normalized.summary = normalized.summary ?? nestedRunIntent.blocked_reason ?? 'Dispatch blocked'
      normalized.error = normalized.error ?? nestedRunIntent.blocked_reason ?? null
    } else if (status === 'running') {
      normalized.status = 'running'
      normalized.summary = normalized.summary ?? 'Assistant responding…'
    } else if (status === 'completed' || status === 'cancelled') {
      normalized.status = 'idle'
      normalized.error = status === 'cancelled' ? null : normalized.error
    } else if (status === 'failed' || status === 'expired' || status === 'interrupted') {
      normalized.status = 'error'
      normalized.error = normalized.error ?? nestedRunIntent.blocked_reason ?? 'Run failed'
    }
    normalized.run_id = payloadString(nestedRunIntent, 'run_id') || normalized.run_id
  }
  return normalized
}

function ensureReducerSession(state: DesktopState, sessionId: string): DesktopSessionRecord {
  return state.sessionsById[sessionId] ?? {
    id: sessionId,
    title: 'New Session',
    workspacePath: '',
    workspaceName: '',
    mode: 'auto',
    metadata: undefined,
    messageCount: 0,
    updatedAt: 0,
    createdAt: 0,
    permissionsHydrated: false,
    gitCommitDetected: false,
    gitCommitCount: 0,
    gitCommittedFileCount: 0,
    gitCommittedAdditions: 0,
    gitCommittedDeletions: 0,
    lifecycle: null,
    runIntent: null,
    live: emptyLiveState(),
    pendingPermissions: [],
    pendingPermissionCount: 0,
    usage: null,
  }
}

function emptyLiveState(): DesktopSessionRecord['live'] {
  return {
    runId: null,
    agentName: null,
    startedAt: null,
    status: 'idle',
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
    reasoningState: 'idle',
    reasoningSegment: 0,
    reasoningStartedAt: null,
    reasoningCompletedAt: null,
    reasoningTimelineSeq: 0,
    reasoningHistory: [],
    awaitingAck: false,
  }
}

function durableSessionPatch(existing: DesktopSessionRecord, eventType: string, payload: Record<string, unknown>, sessionId: string): DesktopSessionRecord {
  const ts = payloadNumber(payload, 'ts_unix_ms') || Date.now()
  const eventSeq = payloadNumber(payload, 'source_seq') || payloadNumber(payload, 'global_seq')
  const session: DesktopSessionRecord = {
    ...existing,
    live: { ...existing.live },
    pendingPermissions: [...existing.pendingPermissions],
    updatedAt: Math.max(existing.updatedAt, payloadNumber(payload, 'updated_at'), ts),
  }
  if (eventSeq > 0) {
    session.lastEventSeq = Math.max(session.lastEventSeq ?? 0, eventSeq)
    session.projectionHighWatermarkSeq = Math.max(session.projectionHighWatermarkSeq ?? 0, eventSeq)
    session.live.seq = Math.max(session.live.seq, eventSeq)
  }
  const nestedSession = payloadRecord(payload, 'session')
  const sessionSource = eventType === 'session.created' || eventType === 'session.updated' ? payload : nestedSession
  if (sessionSource) {
    session.title = payloadString(sessionSource, 'title') || session.title
    session.workspacePath = payloadString(sessionSource, 'workspace_path') || session.workspacePath
    session.workspaceName = payloadString(sessionSource, 'workspace_name') || session.workspaceName
    session.mode = payloadString(sessionSource, 'mode') || session.mode
    session.metadata = isRecord(sessionSource.metadata) ? sessionSource.metadata : session.metadata
    session.sessionApi = payloadString(sessionSource, 'session_api') || session.sessionApi || 'v3'
    session.messageCount = Math.max(session.messageCount, payloadNumber(sessionSource, 'message_count'))
    session.createdAt = payloadNumber(sessionSource, 'created_at') || session.createdAt || ts
    session.lastEventSeq = Math.max(session.lastEventSeq ?? 0, payloadNumber(sessionSource, 'last_event_seq'))
    session.projectionHighWatermarkSeq = Math.max(session.projectionHighWatermarkSeq ?? 0, payloadNumber(sessionSource, 'projection_high_watermark_seq'))
  }
  const lifecycle = durableLifecycle(payload, sessionId)
  if (lifecycle) {
    session.lifecycle = lifecycle
    session.live.runId = lifecycle.active ? lifecycle.runId : null
    session.live.startedAt = lifecycle.active && lifecycle.startedAt > 0 ? lifecycle.startedAt : null
    session.live.status = lifecycle.active ? (lifecycle.phase === 'blocked' ? 'blocked' : lifecycle.phase === 'starting' ? 'starting' : 'running') : lifecycle.phase === 'errored' ? 'error' : 'idle'
    session.live.error = lifecycle.phase === 'errored' ? lifecycle.error || lifecycle.stopReason : null
    if (!lifecycle.active) {
      session.runIntent = null
    }
  }
  const runIntent = durableRunIntent(payload, eventType, sessionId)
  if (runIntent) {
    if (runIntentStatusActive(runIntent.status)) {
      session.runIntent = runIntent
      session.live.runId = runIntent.runId
      session.live.startedAt = runIntent.createdAt > 0 ? runIntent.createdAt : session.live.startedAt
    } else if (runIntentStatusTerminal(runIntent.status)) {
      session.runIntent = null
      session.lifecycle = null
    }
  }
  const status = payloadString(payload, 'status')
  if (status) {
    const normalizedStatus = status.toLowerCase()
    session.live.status = normalizedStatus === 'blocked' || normalizedStatus === 'dispatch_blocked'
      ? 'blocked'
      : normalizedStatus === 'starting' || normalizedStatus === 'pending_executor' || normalizedStatus === 'queued'
        ? 'starting'
        : normalizedStatus === 'running'
          ? 'running'
          : normalizedStatus === 'error' || normalizedStatus === 'failed' || normalizedStatus === 'expired' || normalizedStatus === 'interrupted'
            ? 'error'
            : normalizedStatus === 'idle' || normalizedStatus === 'completed' || normalizedStatus === 'cancelled'
              ? 'idle'
              : session.live.status
    session.live.summary = payloadString(payload, 'summary') || session.live.summary
    session.live.error = payloadString(payload, 'error')
      || payloadString(payload, 'blocked_reason')
      || payloadString(payloadRecord(payload, 'run_intent'), 'blocked_reason')
      || (session.live.status === 'error' ? 'Run failed' : null)
    if (session.live.status === 'idle' || session.live.status === 'error') {
      session.live.runId = null
      session.live.startedAt = null
    }
  }

  switch (eventType) {
    case 'session.title.updated':
      session.title = payloadString(payload, 'title') || session.title
      break
    case 'session.assistant.queued':
      session.live.runId = payloadString(payload, 'run_id') || payloadString(payloadRecord(payload, 'run_intent'), 'run_id') || session.live.runId
      session.live.status = 'starting'
      session.live.awaitingAck = false
      session.live.startedAt = session.live.startedAt ?? ts
      session.live.summary = 'Pending executor…'
      session.live.error = null
      resetLiveTool(session.live)
      resetLiveReasoning(session.live)
      break
    case 'session.assistant.started':
      session.live.runId = payloadString(payload, 'run_id') || payloadString(payloadRecord(payload, 'run_intent'), 'run_id') || session.live.runId
      session.live.status = 'running'
      session.live.awaitingAck = false
      session.live.startedAt = session.live.startedAt ?? ts
      session.live.summary = 'Assistant responding…'
      session.live.error = null
      resetLiveTool(session.live)
      resetLiveReasoning(session.live)
      break
    case 'session.assistant.delta': {
      const delta = typeof payload.delta === 'string' ? payload.delta : ''
      session.live.runId = payloadString(payload, 'run_id') || session.live.runId
      if (delta) {
        session.live.assistantDraft += delta
      }
      session.live.status = 'running'
      session.live.awaitingAck = false
      session.live.startedAt = session.live.startedAt ?? ts
      session.live.summary = 'Streaming response…'
      session.live.error = null
      break
    }
    case 'session.reasoning.started':
    case 'session.reasoning.delta':
    case 'session.reasoning.completed':
      applyReasoning(session, payload, eventType, ts, eventSeq)
      break
    case 'session.tool.started':
    case 'session.tool.delta':
    case 'session.tool.completed':
    case 'session.tool.failed':
    case 'session.tool.cancelled':
    case 'session.tool.canceled':
      applyTool(session, payload, eventType, ts, eventSeq, sessionId)
      break
    case 'session.run.started':
    case 'session.run.running':
      session.live.runId = payloadString(payload, 'run_id') || session.live.runId
      session.live.status = 'running'
      session.live.awaitingAck = false
      session.live.startedAt = session.live.startedAt ?? ts
      session.live.summary = 'Assistant responding…'
      session.live.error = null
      break
    case 'session.run.completed':
    case 'session.assistant.completed': {
      const durableTerminalStatus = runTerminalStatusFromEvent(eventType, payload, sessionId)
      const terminalStatus = durableTerminalStatus || terminalStatusFromEventType(eventType)
      const shouldUnlock = session.sessionApi?.trim().toLowerCase() === 'v3' ? durableTerminalStatus === 'completed' : terminalStatus === 'completed'
      if (eventType === 'session.assistant.completed' && durableMessageFromWire(payload.message, sessionId)) {
        resetLiveAssistant(session.live)
        session.messageCount += 1
      }
      if (shouldUnlock) {
        session.runIntent = null
        session.lifecycle = null
        session.live.status = 'idle'
        session.live.runId = null
        session.live.startedAt = null
        session.live.awaitingAck = false
        resetSidebarLiveToolName(session.live)
        session.live.summary = null
        session.live.error = null
        retainLiveTool(session.live, 'done')
        resetLiveTool(session.live)
        completeLiveReasoning(session.live, ts, eventSeq)
        completeLiveReasoningHistory(session.live, ts, eventSeq)
      }
      break
    }
    case 'session.run.failed':
    case 'session.run.cancelled':
    case 'session.run.expired':
    case 'session.run.interrupted':
    case 'session.assistant.failed': {
      const durableTerminalStatus = runTerminalStatusFromEvent(eventType, payload, sessionId)
      const terminalStatus = durableTerminalStatus || terminalStatusFromEventType(eventType)
      const shouldApplyTerminal = session.sessionApi?.trim().toLowerCase() === 'v3' ? durableTerminalStatus !== '' && durableTerminalStatus !== 'completed' : terminalStatus !== '' && terminalStatus !== 'completed'
      if (!shouldApplyTerminal) {
        break
      }
      const rawError = payloadString(payload, 'error')
        || payloadString(payload, 'blocked_reason')
        || payloadString(payloadRecord(payload, 'run_intent'), 'blocked_reason')
      const isUserCancellation = eventType === 'session.run.cancelled' || terminalStatus === 'cancelled'
      const error = isUserCancellation ? userFacingStopReason(rawError) : rawError || 'Run failed'
      session.runIntent = null
      session.lifecycle = null
      session.live.status = isUserCancellation ? 'idle' : 'error'
      session.live.runId = null
      session.live.startedAt = null
      session.live.awaitingAck = false
      resetSidebarLiveToolName(session.live)
      session.live.summary = error
      session.live.error = isUserCancellation ? null : error
      retainLiveTool(session.live, isUserCancellation ? 'done' : 'error')
      resetLiveTool(session.live)
      completeLiveReasoning(session.live, ts, eventSeq, isUserCancellation ? 'done' : 'error')
      completeLiveReasoningHistory(session.live, ts, eventSeq, isUserCancellation ? 'done' : 'error')
      break
    }
    default:
      break
  }
  session.live.lastEventType = eventType || session.live.lastEventType
  session.live.lastEventAt = ts
  session.live.awaitingAck = false
  return session
}

function durableMessages(eventType: string, payload: Record<string, unknown>, fallbackSessionId: string): ChatMessageRecord[] {
  const messages: ChatMessageRecord[] = []
  const nestedMessage = durableMessageFromWire(payload.message, fallbackSessionId)
  if (nestedMessage) {
    messages.push(nestedMessage)
  }
  if ((eventType === 'session.message.appended' || eventType === 'run.message.stored' || eventType === 'run.message.updated') && messages.length === 0) {
    const directMessage = durableMessageFromWire(payload, fallbackSessionId)
    if (directMessage) {
      messages.push(directMessage)
    }
  }
  return messages
}

function durableMessageFromWire(message: unknown, fallbackSessionId: string): ChatMessageRecord | null {
  if (!isRecord(message)) {
    return null
  }
  const sessionId = payloadString(message, 'session_id') || payloadString(message, 'sessionId') || fallbackSessionId
  const role = payloadString(message, 'role')
  const content = typeof message.content === 'string' ? message.content : ''
  if (!sessionId || !role || content === '') {
    return null
  }
  const globalSeq = payloadNumber(message, 'global_seq') || payloadNumber(message, 'globalSeq')
  return {
    id: payloadString(message, 'id') || `${sessionId}:${globalSeq}`,
    sessionId,
    globalSeq,
    role,
    content,
    createdAt: payloadNumber(message, 'created_at') || payloadNumber(message, 'createdAt') || Date.now(),
    metadata: isRecord(message.metadata) ? message.metadata : undefined,
    toolMessage: parseStructuredToolMessage(content),
  }
}

function durableRunIntent(payload: Record<string, unknown>, eventType: string, fallbackSessionId: string): DesktopRunIntentRecord | null {
  const intent = payloadRecord(payload, 'run_intent') ?? payloadRecord(payload, 'runIntent')
  const source = intent ?? payload
  const runId = payloadString(source, 'run_id') || payloadString(source, 'runId')
  const status = payloadString(source, 'status')
  if (!runId || !status || (eventType !== 'session.run_intent.recorded' && !intent)) {
    return null
  }
  return {
    sessionId: payloadString(source, 'session_id') || payloadString(source, 'sessionId') || fallbackSessionId,
    runId,
    status: status.toLowerCase(),
    blockedReason: payloadString(source, 'blocked_reason') || payloadString(source, 'blockedReason') || payloadString(payload, 'error'),
    createdAt: payloadNumber(source, 'created_at') || payloadNumber(source, 'createdAt'),
    updatedAt: payloadNumber(source, 'updated_at') || payloadNumber(source, 'updatedAt') || payloadNumber(payload, 'updated_at'),
    eventSeq: payloadNumber(source, 'event_seq') || payloadNumber(source, 'eventSeq'),
  }
}

function durableLifecycle(payload: Record<string, unknown>, fallbackSessionId: string): DesktopSessionRecord['lifecycle'] {
  const source = payloadRecord(payload, 'lifecycle')
  if (!source) {
    return null
  }
  const sessionId = payloadString(source, 'session_id') || fallbackSessionId
  if (!sessionId) {
    return null
  }
  return {
    sessionId,
    runId: payloadString(source, 'run_id') || null,
    active: Boolean(source.active),
    phase: payloadString(source, 'phase'),
    startedAt: payloadNumber(source, 'started_at'),
    endedAt: payloadNumber(source, 'ended_at'),
    updatedAt: payloadNumber(source, 'updated_at'),
    generation: payloadNumber(source, 'generation'),
    stopReason: payloadString(source, 'stop_reason') || null,
    error: payloadString(source, 'error') || null,
    ownerTransport: payloadString(source, 'owner_transport') || null,
  }
}

function durablePreference(payload: Record<string, unknown>): ResolvedSessionPreference | null {
  const source = payloadRecord(payload, 'preference')
  if (!source) {
    return null
  }
  return {
    preference: {
      provider: payloadString(source, 'provider'),
      model: payloadString(source, 'model'),
      thinking: payloadString(source, 'thinking'),
      serviceTier: payloadString(source, 'service_tier'),
      contextMode: payloadString(source, 'context_mode'),
      updatedAt: payloadNumber(source, 'updated_at') || Date.now(),
    },
    contextWindow: 0,
    maxOutputTokens: 0,
  }
}

function durablePermission(payload: Record<string, unknown>): DesktopPermissionRecord | null {
  const source = payloadRecord(payload, 'permission') ?? payload
  const id = payloadString(source, 'id')
  const sessionId = payloadString(source, 'session_id')
  if (!id || !sessionId) {
    return null
  }
  return {
    id,
    sessionId,
    runId: payloadString(source, 'run_id'),
    callId: payloadString(source, 'call_id'),
    toolName: payloadString(source, 'tool_name'),
    toolArguments: payloadString(source, 'tool_arguments'),
    status: payloadString(source, 'status'),
    decision: payloadString(source, 'decision'),
    reason: payloadString(source, 'reason'),
    requirement: payloadString(source, 'requirement'),
    mode: payloadString(source, 'mode'),
    createdAt: payloadNumber(source, 'created_at'),
    updatedAt: payloadNumber(source, 'updated_at'),
    resolvedAt: payloadNumber(source, 'resolved_at'),
    permissionRequestedAt: payloadNumber(source, 'permission_requested_at'),
  }
}

function applyReducerPermission(state: DesktopState, permission: DesktopPermissionRecord, sessionId: string): DesktopState {
  const permissionsById = { ...state.permissionsById }
  if (permission.status.trim().toLowerCase() === 'pending') {
    permissionsById[permission.id] = permission
  } else {
    delete permissionsById[permission.id]
  }
  const existing = state.sessionsById[sessionId] ?? ensureReducerSession(state, sessionId)
  const pendingPermissions = existing.pendingPermissions.filter((item) => item.id !== permission.id)
  if (permission.status.trim().toLowerCase() === 'pending') {
    pendingPermissions.unshift(permission)
  }
  const pendingPermissionCount = countApprovalRequiredPermissions(pendingPermissions, existing.mode)
  const session: DesktopSessionRecord = {
    ...existing,
    permissionsHydrated: true,
    pendingPermissions,
    pendingPermissionCount,
    updatedAt: Math.max(existing.updatedAt, permission.updatedAt, permission.permissionRequestedAt, Date.now()),
    live: { ...existing.live },
  }
  if (!session.lifecycle && pendingPermissionCount > 0) {
    session.live.status = 'blocked'
  } else if (!session.lifecycle && session.live.status === 'blocked') {
    session.live.status = session.runIntent ? 'running' : 'idle'
  }
  session.live.lastEventType = permission.status.trim().toLowerCase() === 'pending' ? 'permission.requested' : 'permission.updated'
  session.live.lastEventAt = Date.now()
  return {
    ...state,
    permissionsById,
    sessionsById: {
      ...state.sessionsById,
      [sessionId]: session,
    },
    sessionOrder: sortSessionOrder({ ...state.sessionsById, [sessionId]: session }),
  }
}

function readySession(sessionId: string, updatedAt: number): DesktopSessionReadinessRecord {
  return { sessionId, status: 'ready', ready: true, missingResources: [], omittedResources: [], error: null, updatedAt }
}

function usageFromPayload(payload: Record<string, unknown>): DesktopSessionUsageRecord | null {
  const source = payloadRecord(payload, 'usage_summary') ?? payload
  const sessionId = payloadString(source, 'session_id') || payloadString(payload, 'session_id')
  if (!sessionId) {
    return null
  }
  return {
    sessionId,
    provider: payloadString(source, 'provider'),
    model: payloadString(source, 'model'),
    source: payloadString(source, 'source'),
    contextWindow: payloadNumber(source, 'context_window'),
    totalTokens: payloadNumber(source, 'total_tokens'),
    remainingTokens: payloadNumber(source, 'remaining_tokens'),
    updatedAt: payloadNumber(source, 'updated_at'),
  }
}

function payloadRecord(record: Record<string, unknown> | null | undefined, key: string): Record<string, unknown> | null {
  const value = record?.[key]
  return isRecord(value) ? value : null
}

function payloadString(record: Record<string, unknown> | null | undefined, key: string): string {
  const value = record?.[key]
  return typeof value === 'string' ? value.trim() : ''
}

function payloadNumber(record: Record<string, unknown> | null | undefined, key: string): number {
  const value = record?.[key]
  return typeof value === 'number' && Number.isFinite(value) ? Math.max(0, value) : 0
}

function runIntentStatusTerminal(status: string): boolean {
  switch (status.trim().toLowerCase()) {
    case 'completed':
    case 'failed':
    case 'cancelled':
    case 'expired':
    case 'interrupted':
    case 'dispatch_blocked':
      return true
    default:
      return false
  }
}

function runIntentStatusActive(status: string): boolean {
  switch (status.trim().toLowerCase()) {
    case 'pending_executor':
    case 'running':
    case 'blocked':
      return true
    default:
      return false
  }
}

function terminalStatusFromEventType(eventType: string): string {
  switch (eventType) {
    case 'session.run.completed':
    case 'session.assistant.completed':
      return 'completed'
    case 'session.run.cancelled':
      return 'cancelled'
    case 'session.run.expired':
      return 'expired'
    case 'session.run.interrupted':
      return 'interrupted'
    case 'session.run.failed':
    case 'session.assistant.failed':
      return 'failed'
    default:
      return ''
  }
}

function runTerminalStatusFromEvent(eventType: string, payload: Record<string, unknown>, sessionId: string): string {
  const runIntent = durableRunIntent(payload, eventType, sessionId)
  return runIntent?.status.trim().toLowerCase() || payloadString(payload, 'status').toLowerCase()
}

function userFacingStopReason(reason: string): string {
  const trimmed = reason.trim()
  return !trimmed || trimmed === 'run_stopped' ? 'Run stopped' : trimmed
}

function resetLiveTool(live: DesktopSessionRecord['live']): void {
  live.toolName = null
  live.toolCallId = null
  live.toolArguments = null
  live.toolOutput = ''
}

function resetSidebarLiveToolName(live: DesktopSessionRecord['live']): void {
  live.sidebarToolName = null
}

function retainLiveTool(live: DesktopSessionRecord['live'], state: DesktopSessionRecord['live']['retainedToolState']): void {
  const toolName = live.toolName?.trim() ?? ''
  const toolCallId = live.toolCallId?.trim() ?? ''
  const toolArguments = live.toolArguments?.trim() ?? ''
  const toolOutput = live.toolOutput.trim()
  if (!toolName && !toolCallId && !toolArguments && !toolOutput) {
    return
  }
  live.retainedToolName = toolName || live.retainedToolName
  live.retainedToolCallId = toolCallId || live.retainedToolCallId
  live.retainedToolArguments = toolArguments || live.retainedToolArguments
  live.retainedToolOutput = toolOutput || live.retainedToolOutput
  live.retainedToolState = state
}

function resetRetainedLiveTool(live: DesktopSessionRecord['live']): void {
  live.retainedToolName = null
  live.retainedToolCallId = null
  live.retainedToolArguments = null
  live.retainedToolOutput = ''
  live.retainedToolState = null
}

function resetLiveAssistant(live: DesktopSessionRecord['live']): void {
  live.assistantDraft = ''
  live.retainedAssistantSegments = []
}

function resetLiveReasoning(live: DesktopSessionRecord['live']): void {
  live.reasoningSummary = ''
  live.reasoningText = ''
  live.reasoningState = 'idle'
  live.reasoningStartedAt = null
  live.reasoningCompletedAt = null
  live.reasoningTimelineSeq = 0
  live.reasoningHistory = []
}

function completeLiveReasoning(live: DesktopSessionRecord['live'], ts: number, seq: number, state: 'done' | 'error' = 'done'): void {
  if (live.reasoningState === 'idle') {
    return
  }
  live.reasoningState = state === 'error' ? 'error' : 'done'
  live.reasoningCompletedAt = live.reasoningCompletedAt ?? ts
  if ((live.reasoningTimelineSeq ?? 0) <= 0 && seq > 0) {
    live.reasoningTimelineSeq = seq
  }
}

function flushAssistantDraftToSegment(live: DesktopSessionRecord['live'], createdAt: number): void {
  const draft = live.assistantDraft.trim()
  if (!draft) {
    return
  }
  live.retainedAssistantSegments = appendLiveAssistantSegment(live.retainedAssistantSegments, draft, createdAt, live.seq)
  live.assistantDraft = ''
}

function applyReasoning(session: DesktopSessionRecord, payload: Record<string, unknown>, eventType: string, ts: number, eventSeq: number): void {
  const next = applyCanonicalReasoningEventToLiveHistory(session.live, payload, eventType, ts, eventSeq)
  if (!next) {
    session.live.seq = Math.max(session.live.seq, eventSeq)
    return
  }
  session.live.runId = next.runId
  if (eventType === 'session.reasoning.started') {
    flushAssistantDraftToSegment(session.live, ts)
    session.live.reasoningSegment = Math.max(session.live.reasoningSegment, (session.live.reasoningHistory ?? []).length)
  }
  session.live.reasoningText = next.text
  session.live.reasoningSummary = next.summary
  session.live.reasoningState = next.state
  session.live.reasoningStartedAt = next.startedAt
  session.live.reasoningCompletedAt = next.completedAt
  session.live.reasoningTimelineSeq = next.timelineSeq
  session.live.status = 'running'
  session.live.awaitingAck = false
  session.live.startedAt = session.live.startedAt ?? ts
  session.live.summary = eventType === 'session.reasoning.completed' ? 'Thinking complete' : 'Thinking…'
  session.live.error = null
  session.live.seq = Math.max(session.live.seq, eventSeq)
}

function applyTool(session: DesktopSessionRecord, payload: Record<string, unknown>, eventType: string, ts: number, eventSeq: number, sessionId: string): void {
  const runId = payloadString(payload, 'run_id')
  const toolName = payloadString(payload, 'tool_name')
  const callId = payloadString(payload, 'call_id')
  const stepId = payloadString(payload, 'step_id')
  const toolInstanceId = payloadString(payload, 'tool_instance_id')
  const pathId = liveToolPathId(payloadString(payload, 'path_id'))
  const isToolStarted = eventType === 'session.tool.started'
  const isToolDelta = eventType === 'session.tool.delta'
  const isToolCompleted = eventType === 'session.tool.completed'
  const isToolFailed = eventType === 'session.tool.failed'
  const isToolCancelled = eventType === 'session.tool.cancelled' || eventType === 'session.tool.canceled'
  const isToolTerminal = isToolCompleted || isToolFailed || isToolCancelled
  session.live.runId = runId || session.live.runId
  session.live.status = 'running'
  session.live.awaitingAck = false
  session.live.startedAt = session.live.startedAt ?? ts
  session.live.error = null
  if (typeof payload.step === 'number') {
    session.live.step = payload.step
  }
  if (isToolStarted) {
    flushAssistantDraftToSegment(session.live, ts)
    resetRetainedLiveTool(session.live)
    session.live.toolOutput = ''
  }
  session.live.sidebarToolName = toolName || null
  session.live.toolName = toolName || session.live.toolName
  session.live.toolCallId = callId || session.live.toolCallId
  if (typeof payload.arguments === 'string') {
    session.live.toolArguments = payload.arguments.trim() || null
  }
  session.live.summary = payloadString(payload, 'summary') || session.live.toolName?.trim() || session.live.summary
  upsertToolHistory(session.live, {
    sessionId,
    runId,
    stepId,
    callId,
    toolInstanceId,
    pathId,
    toolName,
    toolArguments: typeof payload.arguments === 'string' ? payload.arguments.trim() || null : null,
    output: typeof payload.output === 'string' ? payload.output : null,
    rawOutput: typeof payload.raw_output === 'string' ? payload.raw_output : null,
    completedOutput: typeof payload.completed_output === 'string' ? payload.completed_output : null,
    state: isToolTerminal ? (isToolFailed ? 'error' : 'done') : 'running',
    step: typeof payload.step === 'number' ? payload.step : null,
    seq: eventSeq,
    ts,
  })
  if (isToolDelta && typeof payload.output === 'string') {
    session.live.toolOutput = appendLiveToolOutput(session.live.toolOutput, payload.output)
  } else if (isToolTerminal) {
    session.live.toolOutput = typeof payload.raw_output === 'string'
      ? replaceLiveToolOutput(payload.raw_output)
      : typeof payload.completed_output === 'string'
        ? replaceLiveToolOutput(payload.completed_output)
      : typeof payload.output === 'string'
        ? replaceLiveToolOutput(payload.output)
        : session.live.toolOutput
    retainLiveTool(session.live, isToolFailed ? 'error' : 'done')
    resetLiveTool(session.live)
  }
}

function upsertToolHistory(live: DesktopSessionRecord['live'], input: {
  sessionId: string
  runId: string
  stepId: string
  callId: string
  toolInstanceId: string
  pathId?: DesktopLiveToolRecord['pathId']
  toolName: string
  toolArguments?: string | null
  output?: string | null
  rawOutput?: string | null
  completedOutput?: string | null
  state: DesktopSessionRecord['live']['retainedToolState'] & ('running' | 'done' | 'error')
  step?: number | null
  seq?: number | null
  ts: number
}): void {
  if (!input.runId || !input.stepId || !input.callId || !input.toolInstanceId) {
    return
  }
  const key = [input.sessionId, input.runId, input.stepId, input.callId, input.toolInstanceId].join('\u001f')
  const existing = (live.toolHistory ?? []).find((item) => item.key === key)
  const outputDelta = input.rawOutput ?? input.completedOutput ?? input.output ?? ''
  const existingOutput = existing?.toolOutput ?? ''
  const nextOutput = input.rawOutput !== undefined && input.rawOutput !== null
    ? replaceLiveToolOutput(input.rawOutput)
    : input.completedOutput !== undefined && input.completedOutput !== null
      ? replaceLiveToolOutput(input.completedOutput)
    : input.output
      ? appendLiveToolOutput(existingOutput, input.output)
      : existingOutput
  const next = {
    key,
    sessionId: input.sessionId,
    runId: input.runId,
    stepId: input.stepId,
    callId: input.callId,
    toolInstanceId: input.toolInstanceId,
    pathId: input.pathId ?? existing?.pathId,
    toolName: input.toolName || existing?.toolName || null,
    toolArguments: input.toolArguments ?? existing?.toolArguments ?? null,
    toolOutput: outputDelta ? nextOutput : existing?.toolOutput ?? '',
    state: input.state,
    step: input.step ?? existing?.step ?? null,
    seq: existing?.seq ?? input.seq ?? undefined,
    startedAt: existing?.startedAt ?? input.ts,
    updatedAt: input.ts,
    completedAt: input.state === 'done' || input.state === 'error' ? input.ts : existing?.completedAt ?? null,
  }
  const rest = (live.toolHistory ?? []).filter((item) => item.key !== key)
  live.toolHistory = [next, ...rest].slice(0, 20)
}

function liveToolPathId(value: string): DesktopLiveToolRecord['pathId'] | undefined {
  if (value === 'run.tool-history.v2' || value === 'run.v3.provider-tool-result.v1') {
    return value
  }
  return undefined
}

function appendLiveToolOutput(current: string, chunk: string): string {
  const normalized = chunk.replace(/\r\n/g, '\n').replace(/\r/g, '\n')
  if (normalized.trim() === '') {
    return current
  }
  return retainTail(current + normalized, 4000)
}

function replaceLiveToolOutput(value: string): string {
  const normalized = value.replace(/\r\n/g, '\n').replace(/\r/g, '\n').trim()
  return normalized ? retainTail(normalized, 4000) : ''
}

function retainTail(value: string, maxChars: number): string {
  return value.length <= maxChars ? value : '…' + value.slice(value.length - maxChars + 1)
}

function replaceSessionArrayPayload<StateKey extends 'planRevisionsBySessionId'>(
  state: DesktopState,
  payload: Record<string, unknown>,
  payloadKey: string,
  stateKey: StateKey,
): DesktopState | null {
  const sessionId = stringValue(payload.sessionId)
  const records = payload[payloadKey]
  if (!sessionId || !Array.isArray(records)) {
    return null
  }

  return {
    ...state,
    [stateKey]: {
      ...state[stateKey],
      [sessionId]: [...records] as DesktopState[StateKey][string],
    },
  }
}

function setNotificationSummaryPayload(state: DesktopState, payload: Record<string, unknown>): DesktopState | null {
  const summary = payload.summary
  if (!isRecord(summary)) {
    return null
  }

  return {
    ...state,
    notificationSummary: summary as unknown as DesktopNotificationSummary,
  }
}

function normalizeSessionOrder(sessionsById: Record<string, DesktopSessionRecord> | undefined, providedOrder: string[] | undefined): string[] {
  const sessions = sessionsById ?? {}
  const allIds = new Set(Object.keys(sessions))
  const order: string[] = []
  for (const id of providedOrder ?? []) {
    if (allIds.delete(id)) {
      order.push(id)
    }
  }
  return [...order, ...sortSessionOrder(pickKeys(sessions, allIds))]
}

function mergeSessionOrder(currentOrder: string[], incomingOrder: string[] | undefined): string[] {
  const order: string[] = []
  const seen = new Set<string>()
  for (const id of incomingOrder ?? []) {
    if (!seen.has(id)) {
      seen.add(id)
      order.push(id)
    }
  }
  for (const id of currentOrder) {
    if (!seen.has(id)) {
      seen.add(id)
      order.push(id)
    }
  }
  return order
}

function sortSessionOrder(sessionsById: Record<string, DesktopSessionRecord>): string[] {
  return Object.values(sessionsById)
    .sort((left, right) => (right.updatedAt - left.updatedAt) || left.id.localeCompare(right.id))
    .map((session) => session.id)
}

function mergeReducerMessages(state: DesktopState, sessionId: string, incoming: ChatMessageRecord[]): DesktopState {
  const merged = incoming.reduce<ChatMessageRecord[]>((messages, message) => mergeMessageIntoCache(messages, message), state.messagesBySessionId[sessionId] ?? [])
  return {
    ...state,
    messagesBySessionId: {
      ...state.messagesBySessionId,
      [sessionId]: dedupeAndTrimMessages(merged),
    },
  }
}

function isValidRevision(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value : ''
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return isRecord(value) ? value : null
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function isObjectWithStringId(value: unknown): value is { id: string } & Record<string, unknown> {
  return isRecord(value) && typeof value.id === 'string' && value.id.length > 0
}

function cloneRecord<T>(record: Record<string, T> | undefined): Record<string, T> {
  return record ? { ...record } : {}
}

function attachUsageToSessions(
  sessionsById: Record<string, DesktopSessionRecord>,
  usageBySessionId: Record<string, DesktopSessionUsageRecord>,
): Record<string, DesktopSessionRecord> {
  let next = sessionsById
  for (const [sessionId, usage] of Object.entries(usageBySessionId)) {
    const session = next[sessionId]
    if (!session || session.usage === usage) {
      continue
    }
    if (next === sessionsById) {
      next = { ...sessionsById }
    }
    next[sessionId] = {
      ...session,
      usage,
      updatedAt: Math.max(session.updatedAt, usage.updatedAt),
    }
  }
  return next
}

function cloneMessageRecord(record: Record<string, ChatMessageRecord[]> | undefined): Record<string, ChatMessageRecord[]> {
  if (!record) {
    return {}
  }
  return Object.fromEntries(Object.entries(record).map(([key, value]) => [key, dedupeAndTrimMessages(value)]))
}

function mergeMessageRecord(
  current: Record<string, ChatMessageRecord[]>,
  incoming: Record<string, ChatMessageRecord[]> | undefined,
): Record<string, ChatMessageRecord[]> {
  if (!incoming) {
    return current
  }
  const next = { ...current }
  for (const [key, value] of Object.entries(incoming)) {
    if (value.length > 0) {
      next[key] = dedupeAndTrimMessages([...(current[key] ?? []), ...value])
    }
  }
  return next
}

function cloneArrayRecord<T>(record: Record<string, T[]> | undefined): Record<string, T[]> {
  if (!record) {
    return {}
  }
  return Object.fromEntries(Object.entries(record).map(([key, value]) => [key, [...value]]))
}

function mergeArrayRecord<T>(current: Record<string, T[]>, incoming: Record<string, T[]> | undefined): Record<string, T[]> {
  if (!incoming) {
    return current
  }
  return {
    ...current,
    ...cloneArrayRecord(incoming),
  }
}

function mergeRunIntentRecord(
  current: Record<string, DesktopRunIntentRecord>,
  incoming: Record<string, DesktopRunIntentRecord> | undefined,
  scopedSessionIds: string[],
): Record<string, DesktopRunIntentRecord> {
  const incomingRecords = cloneRecord(incoming)
  const next = { ...current }
  for (const sessionId of scopedSessionIds) {
    if (incomingRecords[sessionId] || !runIntentStatusActive(next[sessionId]?.status ?? '')) {
      delete next[sessionId]
    }
  }
  return {
    ...next,
    ...incomingRecords,
  }
}

function cloneWorkspaceRecord(record: Record<string, DesktopWorkspaceRecord> | undefined): Record<string, DesktopWorkspaceRecord> {
  if (!record) {
    return {}
  }
  return Object.fromEntries(Object.entries(record).map(([key, value]) => [key, cloneWorkspace(value)]))
}

function mergeWorkspaceRecord(current: Record<string, DesktopWorkspaceRecord>, incoming: Record<string, DesktopWorkspaceRecord> | undefined): Record<string, DesktopWorkspaceRecord> {
  if (!incoming) {
    return current
  }
  const next = { ...current }
  for (const [path, workspace] of Object.entries(incoming)) {
    const existing = next[path]
    if (!existing) {
      next[path] = cloneWorkspace(workspace)
      continue
    }
    next[path] = {
      ...existing,
      ...workspace,
      sessionIds: mergeStringList(workspace.sessionIds, existing.sessionIds),
      updatedAt: Math.max(existing.updatedAt, workspace.updatedAt),
    }
  }
  return next
}

function cloneReadinessRecord(record: Record<string, DesktopSessionReadinessRecord> | undefined): Record<string, DesktopSessionReadinessRecord> {
  if (!record) {
    return {}
  }
  return Object.fromEntries(Object.entries(record).map(([key, value]) => [key, cloneReadiness(value)]))
}

function mergeReadinessRecord(current: Record<string, DesktopSessionReadinessRecord>, incoming: Record<string, DesktopSessionReadinessRecord> | undefined): Record<string, DesktopSessionReadinessRecord> {
  if (!incoming) {
    return current
  }
  return {
    ...current,
    ...cloneReadinessRecord(incoming),
  }
}

function cloneWorkspace(workspace: DesktopWorkspaceRecord): DesktopWorkspaceRecord {
  return {
    ...workspace,
    sessionIds: [...workspace.sessionIds],
  }
}

function mergeStringList(primary: string[], secondary: string[]): string[] {
  const seen = new Set<string>()
  const merged: string[] = []
  for (const value of [...primary, ...secondary]) {
    if (!seen.has(value)) {
      seen.add(value)
      merged.push(value)
    }
  }
  return merged
}

function cloneReadiness(readiness: DesktopSessionReadinessRecord): DesktopSessionReadinessRecord {
  return {
    ...readiness,
    missingResources: [...readiness.missingResources],
    omittedResources: [...readiness.omittedResources],
  }
}

function omitKey<T>(record: Record<string, T>, key: string): Record<string, T> {
  const next = { ...record }
  delete next[key]
  return next
}

function pickKeys<T>(record: Record<string, T>, keys: Set<string>): Record<string, T> {
  const picked: Record<string, T> = {}
  for (const key of keys) {
    const value = record[key]
    if (value !== undefined) {
      picked[key] = value
    }
  }
  return picked
}
