import type {
  AgentModelPolicyRecord,
  ChatMessageRecord,
  DesktopSessionPlanRecord,
  DesktopSessionPlanRevisionRecord,
  ResolvedSessionPreference,
} from '../chat/types/chat'
import type {
  DesktopNotificationCenterRecord,
  DesktopNotificationSummary,
  DesktopPermissionRecord,
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
}

export type DesktopStateAction =
  | { type: 'snapshot/replace'; snapshot: DesktopDaemonSnapshot }
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

  return {
    rev: snapshot.rev,
    status: 'ready',
    staleReason: null,
    resyncRequested: false,
    lastError: null,
    sessionsById: cloneRecord(snapshot.sessionsById),
    sessionOrder: normalizeSessionOrder(snapshot.sessionsById, snapshot.sessionOrder),
    messagesBySessionId: cloneArrayRecord(snapshot.messagesBySessionId),
    permissionsById: cloneRecord(snapshot.permissionsById),
    plansBySessionId: cloneRecord(snapshot.plansBySessionId),
    planRevisionsBySessionId: cloneArrayRecord(snapshot.planRevisionsBySessionId),
    usageBySessionId: cloneRecord(snapshot.usageBySessionId),
    runIntentsBySessionId: cloneRecord(snapshot.runIntentsBySessionId),
    workspacesByPath: cloneWorkspaceRecord(snapshot.workspacesByPath),
    notificationsById: cloneRecord(snapshot.notificationsById),
    notificationSummary: snapshot.notificationSummary ? { ...snapshot.notificationSummary } : { ...EMPTY_NOTIFICATION_SUMMARY },
    preferencesBySessionId: cloneRecord(snapshot.preferencesBySessionId),
    agentModelPolicyBySessionId: cloneRecord(snapshot.agentModelPolicyBySessionId),
    routeReadinessBySessionId: cloneReadinessRecord(snapshot.routeReadinessBySessionId),
  }
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
      return setSessionValuePayload(state, payload, 'usage', 'usageBySessionId')
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
      return null
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
      [sessionId]: sortMessages(payload.messages as ChatMessageRecord[]),
    },
  }
}

function upsertMessagePayload(state: DesktopState, payload: Record<string, unknown>): DesktopState | null {
  const message = payload.message
  if (!isObjectWithStringId(message) || !stringValue(message.sessionId)) {
    return null
  }

  const typedMessage = message as unknown as ChatMessageRecord
  const current = state.messagesBySessionId[typedMessage.sessionId] ?? []
  const withoutExisting = current.filter((existing) => existing.id !== typedMessage.id)

  return {
    ...state,
    messagesBySessionId: {
      ...state.messagesBySessionId,
      [typedMessage.sessionId]: sortMessages([...withoutExisting, typedMessage]),
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

function sortSessionOrder(sessionsById: Record<string, DesktopSessionRecord>): string[] {
  return Object.values(sessionsById)
    .sort((left, right) => (right.updatedAt - left.updatedAt) || left.id.localeCompare(right.id))
    .map((session) => session.id)
}

function sortMessages(messages: ChatMessageRecord[]): ChatMessageRecord[] {
  return [...messages].sort((left, right) => (left.globalSeq - right.globalSeq) || (left.createdAt - right.createdAt) || left.id.localeCompare(right.id))
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

function cloneArrayRecord<T>(record: Record<string, T[]> | undefined): Record<string, T[]> {
  if (!record) {
    return {}
  }
  return Object.fromEntries(Object.entries(record).map(([key, value]) => [key, [...value]]))
}

function cloneWorkspaceRecord(record: Record<string, DesktopWorkspaceRecord> | undefined): Record<string, DesktopWorkspaceRecord> {
  if (!record) {
    return {}
  }
  return Object.fromEntries(Object.entries(record).map(([key, value]) => [key, cloneWorkspace(value)]))
}

function cloneReadinessRecord(record: Record<string, DesktopSessionReadinessRecord> | undefined): Record<string, DesktopSessionReadinessRecord> {
  if (!record) {
    return {}
  }
  return Object.fromEntries(Object.entries(record).map(([key, value]) => [key, cloneReadiness(value)]))
}

function cloneWorkspace(workspace: DesktopWorkspaceRecord): DesktopWorkspaceRecord {
  return {
    ...workspace,
    sessionIds: [...workspace.sessionIds],
  }
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
