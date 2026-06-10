import { createCollection, localOnlyCollectionOptions, type Collection } from '@tanstack/db'
import { useLiveQuery } from '@tanstack/react-db'
import type {
  AgentModelPolicyRecord,
  ChatMessageRecord,
  DesktopSessionPlanRecord,
  DesktopSessionPlanRevisionRecord,
  ResolvedSessionPreference,
} from '../chat/types/chat'
import type {
  DesktopNotificationCenterRecord,
  DesktopNotificationRecord,
  DesktopNotificationSummary,
  DesktopPermissionRecord,
  DesktopRunIntentRecord,
  DesktopSessionRecord,
  DesktopSessionUsageRecord,
} from '../types/realtime'
import { parseStructuredToolMessage } from '../chat/services/tool-message'
import { mergeSessionRecords } from './session-records'

export interface DesktopV3WorksetRequest {
  sessionIds?: string[]
  workspacePath?: string
  workspacePaths?: string[]
  recent?: {
    limit?: number
    beforeUpdatedAt?: number | null
    beforeSessionId?: string
  }
  history?: {
    mode?: 'none' | 'tail' | 'full'
    maxMessagesPerSession?: number
    maxEventsPerSession?: number
    manifestPolicy?: 'error' | 'omit' | 'manifest'
    includeEvents?: boolean
  }
}

export interface DesktopV3ProjectionRecord {
  sessionId?: string
  session_id?: string
  last_event_seq?: number
  projection_high_watermark_seq?: number
  updated_at?: number
}

export interface DesktopV3HistoryChunkDescriptor {
  chunk_id?: string
  resource?: string
  from_seq?: number
  to_seq?: number
  message_count?: number
  event_count?: number
  complete?: boolean
}

export interface DesktopV3HistoryChunkRecord {
  id: string
  sessionId: string
  chunkId: string
  resource: string
  messages: unknown[]
  events: unknown[]
}

export interface DesktopV3WorksetOmissionRecord {
  id: string
  sessionId: string
  resource: string
  reason: string
  nextCursor: string
  manifestRef: string
}

export interface DesktopV3WorksetPaginationRecord {
  id: 'desktop-v3-workset-pagination'
  nextBeforeUpdatedAt: number | null
  nextBeforeSessionId: string
  hasMore: boolean
}

export interface DesktopV3WorksetWatermarksRecord {
  id: 'desktop-v3-workset-watermarks'
  loadedAt: number
  maxUpdatedAt: number
}

export interface DesktopV3WorksetMetaRecord {
  id: 'desktop-v3-workset'
  source: 'v3-workset'
  endpoint: '/v3/sessions:workset'
  sessionOrder: string[]
  request: DesktopV3WorksetRequest | null
  loadedAt: number
}

export type DesktopWorkspaceScope = string | {
  workspacePath?: string | null
  workspacePaths?: Array<string | null | undefined>
}

export interface DesktopV3Workset {
  source: 'v3-workset'
  sessionsById: Record<string, DesktopSessionRecord>
  projectionsBySession: Record<string, DesktopV3ProjectionRecord>
  messagesBySession: Record<string, ChatMessageRecord[]>
  eventsBySession: Record<string, unknown[]>
  preferencesBySession: Record<string, ResolvedSessionPreference>
  agentModelPolicyBySession: Record<string, AgentModelPolicyRecord | null>
  hasActivePlanBySession: Record<string, boolean>
  plansBySession: Record<string, DesktopSessionPlanRecord | null>
  planRevisionsBySession: Record<string, DesktopSessionPlanRevisionRecord[]>
  appliedSeqBySession: Record<string, number>
  highWatermarkBySession: Record<string, number>
  historyManifestsBySession: Record<string, DesktopV3HistoryChunkDescriptor[]>
  historyChunksById: Record<string, DesktopV3HistoryChunkRecord>
  omissions: DesktopV3WorksetOmissionRecord[]
  pagination: DesktopV3WorksetPaginationRecord
  watermarks: DesktopV3WorksetWatermarksRecord
  sessionOrder: string[]
  loadedAt: number
}

export interface DesktopDbPreferenceRecord extends ResolvedSessionPreference {
  sessionId: string
}

export interface DesktopDbAgentModelPolicyRecord {
  sessionId: string
  policy: AgentModelPolicyRecord | null
}

export interface DesktopDbSessionPlanRecord {
  sessionId: string
  plan: DesktopSessionPlanRecord | null
  hasActivePlan: boolean
}

export interface DesktopDbSessionPlanRevisionRecord extends DesktopSessionPlanRevisionRecord {
  sessionId: string
}

export interface DesktopDbSessionEventRecord {
  id: string
  sessionId: string
  event: unknown
}

export interface DesktopDbSessionReadinessRecord {
  sessionId: string
  status: 'loading' | 'ready' | 'omitted' | 'missing' | 'error'
  ready: boolean
  missingResources: string[]
  omittedResources: string[]
  error: string | null
  updatedAt: number
}

export interface DesktopDbWorkspaceRecord {
  workspacePath: string
  workspaceName: string
  sessionIds: string[]
  updatedAt: number
}

export interface DesktopDbNotificationSummaryRecord extends DesktopNotificationSummary {
  id: 'desktop-notification-summary'
}

export type DesktopDbCollection<T extends object> = Collection<T, string>

function createDesktopCollection<T extends object>(id: string, getKey: (item: T) => string): DesktopDbCollection<T> {
  return createCollection<T, string>(
    localOnlyCollectionOptions({
      id,
      getKey,
    }),
  )
}

export const desktopSessionsCollection = createDesktopCollection<DesktopSessionRecord>('desktop-v3-sessions', (session) => session.id)
export const desktopMessagesCollection = createDesktopCollection<ChatMessageRecord>('desktop-v3-messages', desktopDbMessageKey)
export const desktopPermissionsCollection = createDesktopCollection<DesktopPermissionRecord>('desktop-v3-permissions', (permission) => permission.id)
export const desktopUsageCollection = createDesktopCollection<DesktopSessionUsageRecord>('desktop-v3-usage', (usage) => usage.sessionId)
export const desktopPreferencesCollection = createDesktopCollection<DesktopDbPreferenceRecord>('desktop-v3-preferences', (preference) => preference.sessionId)
export const desktopAgentModelPolicyCollection = createDesktopCollection<DesktopDbAgentModelPolicyRecord>('desktop-v3-agent-model-policy', (policy) => policy.sessionId)
export const desktopRunIntentsCollection = createDesktopCollection<DesktopRunIntentRecord>('desktop-v3-run-intents', (intent) => intent.sessionId)
export const desktopProjectionsCollection = createDesktopCollection<DesktopV3ProjectionRecord>('desktop-v3-projections', (projection) => projection.sessionId ?? projection.session_id ?? '')
export const desktopPlansCollection = createDesktopCollection<DesktopDbSessionPlanRecord>('desktop-v3-plans', (plan) => plan.sessionId)
export const desktopPlanRevisionsCollection = createDesktopCollection<DesktopDbSessionPlanRevisionRecord>('desktop-v3-plan-revisions', desktopDbPlanRevisionKey)
export const desktopEventsCollection = createDesktopCollection<DesktopDbSessionEventRecord>('desktop-v3-events', (event) => event.id)
export const desktopHistoryChunksCollection = createDesktopCollection<DesktopV3HistoryChunkRecord>('desktop-v3-history-chunks', (chunk) => chunk.id)
export const desktopWorksetOmissionsCollection = createDesktopCollection<DesktopV3WorksetOmissionRecord>('desktop-v3-workset-omissions', (omission) => omission.id)
export const desktopWorksetMetaCollection = createDesktopCollection<DesktopV3WorksetMetaRecord>('desktop-v3-workset-meta', (meta) => meta.id)
export const desktopWorksetPaginationCollection = createDesktopCollection<DesktopV3WorksetPaginationRecord>('desktop-v3-workset-pagination', (pagination) => pagination.id)
export const desktopWorksetWatermarksCollection = createDesktopCollection<DesktopV3WorksetWatermarksRecord>('desktop-v3-workset-watermarks', (watermarks) => watermarks.id)
export const desktopSessionReadinessCollection = createDesktopCollection<DesktopDbSessionReadinessRecord>('desktop-v3-session-readiness', (readiness) => readiness.sessionId)
export const desktopWorkspacesCollection = createDesktopCollection<DesktopDbWorkspaceRecord>('desktop-v3-workspaces', (workspace) => workspace.workspacePath)
export const desktopNotificationsCollection = createDesktopCollection<DesktopNotificationRecord>('desktop-v3-notifications', (notification) => notification.id)
export const desktopNotificationCenterCollection = createDesktopCollection<DesktopNotificationCenterRecord>('desktop-v3-notification-center', (notification) => notification.id)
export const desktopNotificationSummaryCollection = createDesktopCollection<DesktopDbNotificationSummaryRecord>('desktop-v3-notification-summary', (summary) => summary.id)

export const desktopDb = {
  sessions: desktopSessionsCollection,
  messages: desktopMessagesCollection,
  permissions: desktopPermissionsCollection,
  usage: desktopUsageCollection,
  preferences: desktopPreferencesCollection,
  agentModelPolicy: desktopAgentModelPolicyCollection,
  runIntents: desktopRunIntentsCollection,
  projections: desktopProjectionsCollection,
  plans: desktopPlansCollection,
  planRevisions: desktopPlanRevisionsCollection,
  events: desktopEventsCollection,
  historyChunks: desktopHistoryChunksCollection,
  worksetOmissions: desktopWorksetOmissionsCollection,
  worksetMeta: desktopWorksetMetaCollection,
  worksetPagination: desktopWorksetPaginationCollection,
  worksetWatermarks: desktopWorksetWatermarksCollection,
  sessionReadiness: desktopSessionReadinessCollection,
  workspaces: desktopWorkspacesCollection,
  notifications: desktopNotificationsCollection,
  notificationCenter: desktopNotificationCenterCollection,
  notificationSummary: desktopNotificationSummaryCollection,
} as const

export function desktopDbMessageKey(message: ChatMessageRecord): string {
  return `${message.sessionId}:${message.id}`
}

export function desktopDbPlanRevisionKey(revision: DesktopDbSessionPlanRevisionRecord): string {
  return revision.key || `${revision.sessionId}:${revision.id}:${revision.version}`
}

export function desktopDbEventKey(sessionId: string, index: number, event: unknown): string {
  const record = event && typeof event === 'object' ? event as Record<string, unknown> : null
  const eventID = String(record?.id ?? record?.event_id ?? record?.seq ?? index).trim()
  return `${sessionId}:${eventID || index}`
}

export function desktopDbOmissionKey(omission: Omit<DesktopV3WorksetOmissionRecord, 'id'>): string {
  return [omission.sessionId, omission.resource, omission.reason, omission.nextCursor, omission.manifestRef].join(':')
}

export function desktopDbHistoryChunkKey(sessionId: string, chunkId: string, resource: string): string {
  return `${sessionId}:${resource}:${chunkId}`
}

export function readDesktopDbSession(sessionId: string): DesktopSessionRecord | null {
  return desktopSessionsCollection.get(sessionId.trim()) ?? null
}

export function readDesktopDbMessages(sessionId: string): ChatMessageRecord[] {
  const normalizedSessionId = sessionId.trim()
  return Array.from(desktopMessagesCollection.values())
    .filter((message) => message.sessionId === normalizedSessionId)
    .sort((left, right) => left.globalSeq - right.globalSeq || left.createdAt - right.createdAt)
}

export function readDesktopDbSessionReadiness(sessionId: string): DesktopDbSessionReadinessRecord | null {
  return desktopSessionReadinessCollection.get(sessionId.trim()) ?? null
}

export function applyWorksetToDesktopDB(workset: DesktopV3Workset, request: DesktopV3WorksetRequest | null = null): void {
  const now = Date.now()
  const requestSessionIds = (request?.sessionIds ?? []).map((sessionId) => sessionId.trim()).filter(Boolean)
  const sessionScoped = requestSessionIds.length > 0
  const worksetSessionIds = new Set([...requestSessionIds, ...(workset.sessionOrder.length > 0 ? workset.sessionOrder : Object.keys(workset.sessionsById))])
  const sessions = Object.values(workset.sessionsById)
  const messages = Object.values(workset.messagesBySession).flat()
  const projections = Object.entries(workset.projectionsBySession).map(([sessionId, projection]) => ({ ...projection, sessionId }))
  const preferences = Object.entries(workset.preferencesBySession).map(([sessionId, preference]) => ({ ...preference, sessionId }))
  const policies = Object.entries(workset.agentModelPolicyBySession).map(([sessionId, policy]) => ({ sessionId, policy }))
  const plans = Object.entries(workset.plansBySession).map(([sessionId, plan]) => ({ sessionId, plan, hasActivePlan: workset.hasActivePlanBySession[sessionId] ?? Boolean(plan) }))
  const planRevisions = Object.entries(workset.planRevisionsBySession).flatMap(([sessionId, revisions]) => revisions.map((revision) => ({ ...revision, sessionId })))
  const permissions = sessions.flatMap((session) => session.pendingPermissions)
  const usages = sessions.map((session) => session.usage).filter((usage): usage is DesktopSessionUsageRecord => Boolean(usage))
  const runIntents = sessions.map((session) => session.runIntent).filter((intent): intent is DesktopRunIntentRecord => Boolean(intent))
  const events = Object.entries(workset.eventsBySession).flatMap(([sessionId, items]) => items.map((event, index) => ({ id: desktopDbEventKey(sessionId, index, event), sessionId, event })))
  const historyChunks = Object.entries(workset.historyChunksById).map(([chunkId, chunk]) => ({ ...chunk, id: chunk.id || chunkId }))
  const workspaces = desktopDbWorkspacesFromSessions(sessions)
  const readiness = workset.sessionOrder.map((sessionId) => desktopDbReadySession(sessionId, now))

  if (sessionScoped) {
    replaceDesktopDbRecordsForSessions(desktopSessionsCollection, worksetSessionIds, (session) => session.id, sessions)
    replaceDesktopDbRecordsForSessions(desktopMessagesCollection, worksetSessionIds, (message) => message.sessionId, messages)
    replaceDesktopDbRecordsForSessions(desktopProjectionsCollection, worksetSessionIds, (projection) => projection.sessionId ?? projection.session_id ?? '', projections)
    replaceDesktopDbRecordsForSessions(desktopPreferencesCollection, worksetSessionIds, (preference) => preference.sessionId, preferences)
    replaceDesktopDbRecordsForSessions(desktopAgentModelPolicyCollection, worksetSessionIds, (policy) => policy.sessionId, policies)
    replaceDesktopDbRecordsForSessions(desktopPlansCollection, worksetSessionIds, (plan) => plan.sessionId, plans)
    replaceDesktopDbRecordsForSessions(desktopPlanRevisionsCollection, worksetSessionIds, (revision) => revision.sessionId, planRevisions)
    replaceDesktopDbRecordsForSessions(desktopPermissionsCollection, worksetSessionIds, (permission) => permission.sessionId, permissions)
    replaceDesktopDbRecordsForSessions(desktopUsageCollection, worksetSessionIds, (usage) => usage.sessionId, usages)
    replaceDesktopDbRecordsForSessions(desktopRunIntentsCollection, worksetSessionIds, (intent) => intent.sessionId, runIntents)
    replaceDesktopDbRecordsForSessions(desktopEventsCollection, worksetSessionIds, (event) => event.sessionId, events)
    replaceDesktopDbRecordsForSessions(desktopHistoryChunksCollection, worksetSessionIds, (chunk) => chunk.sessionId, historyChunks)
    replaceDesktopDbRecordsForSessions(desktopWorksetOmissionsCollection, worksetSessionIds, (omission) => omission.sessionId, workset.omissions)
    upsertDesktopDbWorkspaces(workspaces)
    upsertDesktopDbRecords(desktopSessionReadinessCollection, readiness)
  } else {
    replaceDesktopDbCollection(desktopSessionsCollection, sessions)
    replaceDesktopDbCollection(desktopMessagesCollection, messages)
    replaceDesktopDbCollection(desktopProjectionsCollection, projections)
    replaceDesktopDbCollection(desktopPreferencesCollection, preferences)
    replaceDesktopDbCollection(desktopAgentModelPolicyCollection, policies)
    replaceDesktopDbCollection(desktopPlansCollection, plans)
    replaceDesktopDbCollection(desktopPlanRevisionsCollection, planRevisions)
    replaceDesktopDbCollection(desktopPermissionsCollection, permissions)
    replaceDesktopDbCollection(desktopUsageCollection, usages)
    replaceDesktopDbCollection(desktopRunIntentsCollection, runIntents)
    replaceDesktopDbCollection(desktopEventsCollection, events)
    replaceDesktopDbCollection(desktopHistoryChunksCollection, historyChunks)
    replaceDesktopDbCollection(desktopWorksetOmissionsCollection, workset.omissions)
    replaceDesktopDbCollection(desktopWorkspacesCollection, workspaces)
    replaceDesktopDbCollection(desktopSessionReadinessCollection, readiness)
  }

  const meta: DesktopV3WorksetMetaRecord = {
    id: 'desktop-v3-workset',
    source: 'v3-workset',
    endpoint: '/v3/sessions:workset',
    sessionOrder: sessionScoped ? mergeDesktopDbSessionOrder(workset.sessionOrder) : workset.sessionOrder,
    request,
    loadedAt: workset.loadedAt,
  }
  upsertDesktopDbRecord(desktopWorksetMetaCollection, meta)
  upsertDesktopDbRecord(desktopWorksetPaginationCollection, workset.pagination)
  upsertDesktopDbRecord(desktopWorksetWatermarksCollection, workset.watermarks)
}

export interface DesktopDbDurablePatch {
  sessionId?: string
  session?: DesktopSessionRecord | null
  messages?: ChatMessageRecord[]
  appliedSeq?: number
  highWatermark?: number
}

export function mergeDesktopDBDurablePatch(patch: DesktopDbDurablePatch): DesktopSessionRecord | null {
  const normalizedSessionId = (patch.sessionId || patch.session?.id || '').trim()
  if (!normalizedSessionId) {
    return null
  }

  for (const message of patch.messages ?? []) {
    upsertDesktopDbRecord(desktopMessagesCollection, message)
  }

  const appliedSeq = typeof patch.appliedSeq === 'number' ? Math.max(0, patch.appliedSeq) : 0
  const highWatermark = typeof patch.highWatermark === 'number' ? Math.max(0, patch.highWatermark) : appliedSeq
  const currentSession = desktopSessionsCollection.get(normalizedSessionId) ?? null
  const incomingSession = patch.session ?? null
  let mergedSession = incomingSession
    ? mergeSessionRecords(currentSession, incomingSession)
    : currentSession
  if (mergedSession && (appliedSeq > 0 || highWatermark > 0)) {
    mergedSession = {
      ...mergedSession,
      lastEventSeq: Math.max(mergedSession.lastEventSeq ?? 0, appliedSeq),
      projectionHighWatermarkSeq: Math.max(mergedSession.projectionHighWatermarkSeq ?? 0, highWatermark),
      live: {
        ...mergedSession.live,
        seq: Math.max(mergedSession.live.seq ?? 0, appliedSeq),
      },
    }
  }
  if (mergedSession) {
    upsertDesktopDbRecord(desktopSessionsCollection, mergedSession)
    upsertDesktopDbRecord(desktopSessionReadinessCollection, desktopDbReadySession(normalizedSessionId, Date.now()))
  }
  upsertDesktopDbRecord(desktopProjectionsCollection, {
    sessionId: normalizedSessionId,
    session_id: normalizedSessionId,
    last_event_seq: Math.max(appliedSeq, currentSession?.lastEventSeq ?? 0, incomingSession?.lastEventSeq ?? 0),
    projection_high_watermark_seq: Math.max(highWatermark, currentSession?.projectionHighWatermarkSeq ?? 0, incomingSession?.projectionHighWatermarkSeq ?? 0),
    updated_at: Date.now(),
  })
  return mergedSession
}

export function applyDurableEventToDesktopDB(event: unknown): void {
  const envelope = event && typeof event === 'object' ? event as Record<string, unknown> : null
  const eventType = typeof envelope?.event_type === 'string' ? envelope.event_type : ''
  const ts = typeof envelope?.ts_unix_ms === 'number' ? envelope.ts_unix_ms : Date.now()
  const eventSeq = typeof envelope?.source_seq === 'number' && envelope.source_seq > 0
    ? Math.max(0, envelope.source_seq)
    : typeof envelope?.global_seq === 'number'
      ? Math.max(0, envelope.global_seq)
      : 0
  const payload = envelope?.payload && typeof envelope.payload === 'object'
    ? envelope.payload as Record<string, unknown>
    : envelope
  if (!payload) {
    return
  }

  const normalizedPayload = normalizeDesktopDbDurablePayload(eventType, payload)
  const sessionId = desktopDbPayloadString(normalizedPayload, 'session_id')
    || (eventType.startsWith('session.') ? desktopDbPayloadString(normalizedPayload, 'id') : '')
    || desktopDbPayloadString(envelope, 'entity_id')
    || desktopDbPayloadString(normalizedPayload.session as Record<string, unknown> | null, 'id')

  const sessionPatch = desktopDbSessionPatchFromDurablePayload(eventType, normalizedPayload, sessionId, ts, eventSeq)
  const messages = desktopDbMessagesFromDurablePayload(eventType, normalizedPayload, sessionId)
  if (sessionPatch || messages.length > 0 || eventSeq > 0) {
    mergeDesktopDBDurablePatch({
      sessionId,
      session: sessionPatch,
      messages,
      appliedSeq: eventSeq,
      highWatermark: eventSeq,
    })
  }

  const preference = desktopDbPreferenceFromDurablePayload(normalizedPayload, sessionId, ts)
  if (preference) {
    upsertDesktopDbRecord(desktopPreferencesCollection, preference)
  }

  const permission = desktopDbPermissionFromDurablePayload(normalizedPayload)
  if (permission) {
    if (permission.status.trim().toLowerCase() === 'pending') {
      upsertDesktopDbRecord(desktopPermissionsCollection, permission)
    } else if (desktopPermissionsCollection.has(permission.id)) {
      desktopPermissionsCollection.delete(permission.id)
    }
  }

  const runIntent = desktopDbRunIntentFromDurablePayload(normalizedPayload, eventType, sessionId)
  if (runIntent) {
    if (desktopDbRunIntentStatusTerminal(runIntent.status)) {
      if (desktopRunIntentsCollection.has(runIntent.sessionId)) {
        desktopRunIntentsCollection.delete(runIntent.sessionId)
      }
    } else {
      upsertDesktopDbRecord(desktopRunIntentsCollection, runIntent)
    }
  }

  if (isDesktopNotificationRecord(normalizedPayload.notification)) {
    upsertDesktopDbRecord(desktopNotificationsCollection, normalizedPayload.notification)
  }
}

export async function ensureDesktopDBRouteSession(_workspaceScope: DesktopWorkspaceScope, sessionId: string): Promise<DesktopDbSessionReadinessRecord> {
  const normalizedSessionId = sessionId.trim()
  const existingReadiness = readDesktopDbSessionReadiness(normalizedSessionId)
  if (existingReadiness?.ready || readDesktopDbSession(normalizedSessionId)) {
    const ready = desktopDbReadySession(normalizedSessionId, Date.now())
    upsertDesktopDbRecord(desktopSessionReadinessCollection, ready)
    return ready
  }

  upsertDesktopDbRecord(desktopSessionReadinessCollection, desktopDbPendingSession(normalizedSessionId, Date.now()))
  try {
    const { fetchDesktopV3Workset } = await import('./desktop-v3-cache')
    const request = desktopDbRouteWorksetRequest(normalizedSessionId)
    const workset = await fetchDesktopV3Workset(request)
    applyWorksetToDesktopDB(workset, request)
    if (readDesktopDbSession(normalizedSessionId)) {
      const ready = desktopDbReadySession(normalizedSessionId, Date.now())
      upsertDesktopDbRecord(desktopSessionReadinessCollection, ready)
      return ready
    }
    const missing = desktopDbMissingSession(normalizedSessionId, Date.now(), workset.omissions.filter((omission) => omission.sessionId === normalizedSessionId).map((omission) => omission.resource))
    upsertDesktopDbRecord(desktopSessionReadinessCollection, missing)
    return missing
  } catch (error) {
    const failed = desktopDbErrorSession(normalizedSessionId, Date.now(), error)
    upsertDesktopDbRecord(desktopSessionReadinessCollection, failed)
    throw error
  }
}

export function useDesktopRouteReadiness(_workspaceScope: DesktopWorkspaceScope, sessionId: string | null | undefined): DesktopDbSessionReadinessRecord | null {
  const state = useDesktopCollectionState(desktopSessionReadinessCollection)
  const normalizedSessionId = sessionId?.trim() ?? ''
  return normalizedSessionId ? state.get(normalizedSessionId) ?? null : null
}

export function useDesktopWorkspaceSessions(workspaceScope: DesktopWorkspaceScope): DesktopSessionRecord[] {
  const sessions = useDesktopCollectionData(desktopSessionsCollection)
  const workspacePaths = desktopDbWorkspacePaths(workspaceScope)
  return sessions
    .filter((session) => workspacePaths.size === 0 || workspacePaths.has(session.workspacePath))
    .sort((left, right) => right.updatedAt - left.updatedAt || left.id.localeCompare(right.id))
}

export function useDesktopSession(sessionId: string | null | undefined): DesktopSessionRecord | null {
  const state = useDesktopCollectionState(desktopSessionsCollection)
  const normalizedSessionId = sessionId?.trim() ?? ''
  return normalizedSessionId ? state.get(normalizedSessionId) ?? null : null
}

export function useDesktopMessages(sessionId: string | null | undefined): ChatMessageRecord[] {
  const messages = useDesktopCollectionData(desktopMessagesCollection)
  const normalizedSessionId = sessionId?.trim() ?? ''
  return normalizedSessionId
    ? messages.filter((message) => message.sessionId === normalizedSessionId).sort((left, right) => left.globalSeq - right.globalSeq || left.createdAt - right.createdAt)
    : []
}

export function useDesktopPreference(sessionId: string | null | undefined): ResolvedSessionPreference | null {
  const state = useDesktopCollectionState(desktopPreferencesCollection)
  const normalizedSessionId = sessionId?.trim() ?? ''
  return normalizedSessionId ? state.get(normalizedSessionId) ?? null : null
}

export function useDesktopActiveRun(sessionId: string | null | undefined): DesktopRunIntentRecord | null {
  const state = useDesktopCollectionState(desktopRunIntentsCollection)
  const normalizedSessionId = sessionId?.trim() ?? ''
  return normalizedSessionId ? state.get(normalizedSessionId) ?? null : null
}

export function useDesktopAgentModelPolicy(sessionId: string | null | undefined): AgentModelPolicyRecord | null {
  const state = useDesktopCollectionState(desktopAgentModelPolicyCollection)
  const normalizedSessionId = sessionId?.trim() ?? ''
  return normalizedSessionId ? state.get(normalizedSessionId)?.policy ?? null : null
}

export function useDesktopPlan(sessionId: string | null | undefined): DesktopDbSessionPlanRecord | null {
  const state = useDesktopCollectionState(desktopPlansCollection)
  const normalizedSessionId = sessionId?.trim() ?? ''
  return normalizedSessionId ? state.get(normalizedSessionId) ?? null : null
}

export function useDesktopPlanRevisions(sessionId: string | null | undefined): DesktopDbSessionPlanRevisionRecord[] {
  const revisions = useDesktopCollectionData(desktopPlanRevisionsCollection)
  const normalizedSessionId = sessionId?.trim() ?? ''
  return normalizedSessionId
    ? revisions.filter((revision) => revision.sessionId === normalizedSessionId).sort((left, right) => right.updatedAt - left.updatedAt || right.version - left.version)
    : []
}

function useDesktopCollectionData<T extends object>(collection: DesktopDbCollection<T>): T[] {
  return useLiveQuery(() => collection, [collection]).data ?? []
}

function useDesktopCollectionState<T extends object>(collection: DesktopDbCollection<T>): Map<string, T> {
  return useLiveQuery(() => collection, [collection]).state ?? new Map<string, T>()
}

function desktopDbReadySession(sessionId: string, updatedAt: number): DesktopDbSessionReadinessRecord {
  return { sessionId, status: 'ready', ready: true, missingResources: [], omittedResources: [], error: null, updatedAt }
}

function desktopDbPendingSession(sessionId: string, updatedAt: number): DesktopDbSessionReadinessRecord {
  return { sessionId, status: 'loading', ready: false, missingResources: [], omittedResources: [], error: null, updatedAt }
}

function desktopDbMissingSession(sessionId: string, updatedAt: number, omittedResources: string[] = []): DesktopDbSessionReadinessRecord {
  return { sessionId, status: omittedResources.length > 0 ? 'omitted' : 'missing', ready: false, missingResources: ['session'], omittedResources, error: null, updatedAt }
}

function desktopDbErrorSession(sessionId: string, updatedAt: number, error: unknown): DesktopDbSessionReadinessRecord {
  return { sessionId, status: 'error', ready: false, missingResources: [], omittedResources: [], error: error instanceof Error ? error.message : String(error), updatedAt }
}

function desktopDbRouteWorksetRequest(sessionId: string): DesktopV3WorksetRequest {
  return {
    sessionIds: [sessionId],
    history: { mode: 'full', maxMessagesPerSession: 200, maxEventsPerSession: 0, manifestPolicy: 'manifest', includeEvents: false },
  }
}

function desktopDbWorkspacePaths(workspaceScope: DesktopWorkspaceScope): Set<string> {
  if (typeof workspaceScope === 'string') {
    const normalized = workspaceScope.trim()
    return new Set(normalized ? [normalized] : [])
  }
  const paths = [workspaceScope.workspacePath, ...(workspaceScope.workspacePaths ?? [])]
    .map((workspacePath) => workspacePath?.trim() ?? '')
    .filter(Boolean)
  return new Set(paths)
}

function desktopDbWorkspacesFromSessions(sessions: DesktopSessionRecord[]): DesktopDbWorkspaceRecord[] {
  const byWorkspace = new Map<string, DesktopDbWorkspaceRecord>()
  for (const session of sessions) {
    const workspacePath = session.workspacePath.trim()
    if (!workspacePath) {
      continue
    }
    const current = byWorkspace.get(workspacePath) ?? {
      workspacePath,
      workspaceName: session.workspaceName,
      sessionIds: [],
      updatedAt: 0,
    }
    current.workspaceName = current.workspaceName || session.workspaceName
    current.sessionIds.push(session.id)
    current.updatedAt = Math.max(current.updatedAt, session.updatedAt)
    byWorkspace.set(workspacePath, current)
  }
  return Array.from(byWorkspace.values())
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value && typeof value === 'object')
}

function desktopDbPayloadString(record: Record<string, unknown> | null | undefined, key: string): string {
  const value = record?.[key]
  return typeof value === 'string' ? value.trim() : ''
}

function desktopDbPayloadNumber(record: Record<string, unknown> | null | undefined, key: string): number {
  const value = record?.[key]
  return typeof value === 'number' ? value : 0
}

function desktopDbNestedRecord(record: Record<string, unknown>, key: string): Record<string, unknown> | null {
  const value = record[key]
  return isRecord(value) ? value : null
}

function normalizeDesktopDbDurablePayload(eventType: string, payload: Record<string, unknown>): Record<string, unknown> {
  if (!eventType.startsWith('session.')) {
    return payload
  }
  const normalized: Record<string, unknown> = { ...payload }
  const nestedSession = desktopDbNestedRecord(normalized, 'session')
  const nestedMessage = desktopDbNestedRecord(normalized, 'message')
  const nestedLifecycle = desktopDbNestedRecord(normalized, 'lifecycle')
  const nestedRunIntent = desktopDbNestedRecord(normalized, 'run_intent')
  if (typeof normalized.session_id !== 'string') {
    normalized.session_id = desktopDbPayloadString(nestedSession, 'id')
      || desktopDbPayloadString(nestedMessage, 'session_id')
      || desktopDbPayloadString(nestedLifecycle, 'session_id')
      || desktopDbPayloadString(nestedRunIntent, 'session_id')
      || normalized.session_id
  }
  if ((eventType === 'session.created' || eventType === 'session.updated') && nestedSession) {
    return { ...nestedSession, ...normalized, session: nestedSession }
  }
  if (eventType === 'session.run_intent.recorded' && nestedRunIntent) {
    const status = desktopDbPayloadString(nestedRunIntent, 'status').toLowerCase()
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
    normalized.run_id = desktopDbPayloadString(nestedRunIntent, 'run_id') || normalized.run_id
  }
  return normalized
}

function desktopDbEmptyLiveState(): DesktopSessionRecord['live'] {
  return {
    runId: null,
    agentName: null,
    startedAt: null,
    status: 'idle',
    step: 0,
    toolName: null,
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
    awaitingAck: false,
  }
}

function desktopDbEnsureSession(sessionId: string): DesktopSessionRecord {
  return desktopSessionsCollection.get(sessionId) ?? {
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
    live: desktopDbEmptyLiveState(),
    pendingPermissions: [],
    pendingPermissionCount: 0,
    usage: null,
  }
}

function desktopDbMessageFromWire(message: unknown, fallbackSessionId: string): ChatMessageRecord | null {
  if (!isRecord(message)) {
    return null
  }
  const sessionId = desktopDbPayloadString(message, 'session_id') || desktopDbPayloadString(message, 'sessionId') || fallbackSessionId
  const role = desktopDbPayloadString(message, 'role')
  const content = typeof message.content === 'string' ? message.content : ''
  if (!sessionId || !role || content === '') {
    return null
  }
  const globalSeq = desktopDbPayloadNumber(message, 'global_seq') || desktopDbPayloadNumber(message, 'globalSeq')
  return {
    id: desktopDbPayloadString(message, 'id') || `${sessionId}:${globalSeq}`,
    sessionId,
    globalSeq,
    role,
    content,
    createdAt: desktopDbPayloadNumber(message, 'created_at') || desktopDbPayloadNumber(message, 'createdAt') || Date.now(),
    metadata: isRecord(message.metadata) ? message.metadata : undefined,
    toolMessage: parseStructuredToolMessage(content),
  }
}

function desktopDbMessagesFromDurablePayload(eventType: string, payload: Record<string, unknown>, fallbackSessionId: string): ChatMessageRecord[] {
  const messages: ChatMessageRecord[] = []
  const nestedMessage = desktopDbMessageFromWire(payload.message, fallbackSessionId)
  if (nestedMessage) {
    messages.push(nestedMessage)
  }
  if ((eventType === 'session.message.appended' || eventType === 'run.message.stored' || eventType === 'run.message.updated') && messages.length === 0) {
    const directMessage = desktopDbMessageFromWire(payload, fallbackSessionId)
    if (directMessage) {
      messages.push(directMessage)
    }
  }
  return messages
}

function desktopDbRunIntentStatusTerminal(status: string): boolean {
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

function desktopDbRunIntentStatusActive(status: string): boolean {
  switch (status.trim().toLowerCase()) {
    case 'pending_executor':
    case 'running':
    case 'blocked':
      return true
    default:
      return false
  }
}

function desktopDbRunIntentFromDurablePayload(payload: Record<string, unknown>, eventType: string, fallbackSessionId: string): DesktopRunIntentRecord | null {
  const intent = desktopDbNestedRecord(payload, 'run_intent') ?? desktopDbNestedRecord(payload, 'runIntent')
  const source = intent ?? payload
  const runId = desktopDbPayloadString(source, 'run_id') || desktopDbPayloadString(source, 'runId')
  const status = desktopDbPayloadString(source, 'status')
  if (!runId || !status || (eventType !== 'session.run_intent.recorded' && !intent)) {
    return null
  }
  return {
    sessionId: desktopDbPayloadString(source, 'session_id') || desktopDbPayloadString(source, 'sessionId') || fallbackSessionId,
    runId,
    status: status.toLowerCase(),
    blockedReason: desktopDbPayloadString(source, 'blocked_reason') || desktopDbPayloadString(source, 'blockedReason') || desktopDbPayloadString(payload, 'error'),
    createdAt: desktopDbPayloadNumber(source, 'created_at') || desktopDbPayloadNumber(source, 'createdAt'),
    updatedAt: desktopDbPayloadNumber(source, 'updated_at') || desktopDbPayloadNumber(source, 'updatedAt') || desktopDbPayloadNumber(payload, 'updated_at'),
    eventSeq: desktopDbPayloadNumber(source, 'event_seq') || desktopDbPayloadNumber(source, 'eventSeq'),
  }
}

function desktopDbLifecycleFromDurablePayload(payload: Record<string, unknown>, fallbackSessionId: string): DesktopSessionRecord['lifecycle'] {
  const source = desktopDbNestedRecord(payload, 'lifecycle')
  if (!source) {
    return null
  }
  const sessionId = desktopDbPayloadString(source, 'session_id') || fallbackSessionId
  if (!sessionId) {
    return null
  }
  return {
    sessionId,
    runId: desktopDbPayloadString(source, 'run_id') || null,
    active: Boolean(source.active),
    phase: desktopDbPayloadString(source, 'phase'),
    startedAt: desktopDbPayloadNumber(source, 'started_at'),
    endedAt: desktopDbPayloadNumber(source, 'ended_at'),
    updatedAt: desktopDbPayloadNumber(source, 'updated_at'),
    generation: desktopDbPayloadNumber(source, 'generation'),
    stopReason: desktopDbPayloadString(source, 'stop_reason') || null,
    error: desktopDbPayloadString(source, 'error') || null,
    ownerTransport: desktopDbPayloadString(source, 'owner_transport') || null,
  }
}

function desktopDbSessionPatchFromDurablePayload(
  eventType: string,
  payload: Record<string, unknown>,
  sessionId: string,
  ts: number,
  eventSeq: number,
): DesktopSessionRecord | null {
  if (!sessionId) {
    return null
  }
  const existing = desktopDbEnsureSession(sessionId)
  const session = { ...existing, live: { ...existing.live }, pendingPermissions: [...existing.pendingPermissions] }
  session.updatedAt = Math.max(session.updatedAt, desktopDbPayloadNumber(payload, 'updated_at'), ts)
  if (eventSeq > 0) {
    session.lastEventSeq = Math.max(session.lastEventSeq ?? 0, eventSeq)
    session.projectionHighWatermarkSeq = Math.max(session.projectionHighWatermarkSeq ?? 0, eventSeq)
    session.live.seq = Math.max(session.live.seq, eventSeq)
  }
  const nestedSession = desktopDbNestedRecord(payload, 'session')
  const sessionSource = eventType === 'session.created' || eventType === 'session.updated' ? payload : nestedSession
  if (sessionSource) {
    session.title = desktopDbPayloadString(sessionSource, 'title') || session.title
    session.workspacePath = desktopDbPayloadString(sessionSource, 'workspace_path') || session.workspacePath
    session.workspaceName = desktopDbPayloadString(sessionSource, 'workspace_name') || session.workspaceName
    session.mode = desktopDbPayloadString(sessionSource, 'mode') || session.mode
    session.metadata = isRecord(sessionSource.metadata) ? sessionSource.metadata : session.metadata
    session.sessionApi = desktopDbPayloadString(sessionSource, 'session_api') || session.sessionApi || 'v3'
    session.messageCount = Math.max(session.messageCount, desktopDbPayloadNumber(sessionSource, 'message_count'))
    session.createdAt = desktopDbPayloadNumber(sessionSource, 'created_at') || session.createdAt || ts
    session.lastEventSeq = Math.max(session.lastEventSeq ?? 0, desktopDbPayloadNumber(sessionSource, 'last_event_seq'))
    session.projectionHighWatermarkSeq = Math.max(session.projectionHighWatermarkSeq ?? 0, desktopDbPayloadNumber(sessionSource, 'projection_high_watermark_seq'))
  }
  const lifecycle = desktopDbLifecycleFromDurablePayload(payload, sessionId)
  if (lifecycle) {
    session.lifecycle = lifecycle
    session.live.runId = lifecycle.active ? lifecycle.runId : null
    session.live.startedAt = lifecycle.active && lifecycle.startedAt > 0 ? lifecycle.startedAt : null
    session.live.status = lifecycle.active ? (lifecycle.phase === 'blocked' ? 'blocked' : lifecycle.phase === 'starting' ? 'starting' : 'running') : lifecycle.phase === 'errored' ? 'error' : 'idle'
    session.live.error = lifecycle.phase === 'errored' ? lifecycle.error || lifecycle.stopReason : null
  }
  const runIntent = desktopDbRunIntentFromDurablePayload(payload, eventType, sessionId)
  if (runIntent) {
    if (desktopDbRunIntentStatusActive(runIntent.status)) {
      session.runIntent = runIntent
      session.live.runId = runIntent.runId
      session.live.startedAt = runIntent.createdAt > 0 ? runIntent.createdAt : session.live.startedAt
    } else if (desktopDbRunIntentStatusTerminal(runIntent.status)) {
      session.runIntent = null
      session.lifecycle = null
    }
  }
  const status = desktopDbPayloadString(payload, 'status')
  if (status) {
    const normalizedStatus = status.toLowerCase()
    session.live.status = normalizedStatus === 'blocked' ? 'blocked' : normalizedStatus === 'starting' ? 'starting' : normalizedStatus === 'running' ? 'running' : normalizedStatus === 'error' ? 'error' : normalizedStatus === 'idle' ? 'idle' : session.live.status
    session.live.summary = desktopDbPayloadString(payload, 'summary') || session.live.summary
    session.live.error = desktopDbPayloadString(payload, 'error') || (normalizedStatus === 'error' ? 'Run failed' : null)
    if (normalizedStatus === 'idle' || normalizedStatus === 'error') {
      session.live.runId = null
      session.live.startedAt = null
    }
  }
  if (eventType === 'session.title.updated') {
    session.title = desktopDbPayloadString(payload, 'title') || session.title
  }
  if (eventType === 'session.message.appended' || eventType === 'run.message.stored' || eventType === 'run.message.updated') {
    session.messageCount += 1
  }
  session.live.lastEventType = eventType || session.live.lastEventType
  session.live.lastEventAt = ts
  session.live.awaitingAck = false
  return session
}

function desktopDbPreferenceFromDurablePayload(payload: Record<string, unknown>, sessionId: string, ts: number): DesktopDbPreferenceRecord | null {
  const source = desktopDbNestedRecord(payload, 'preference')
  if (!source || !sessionId) {
    return null
  }
  return {
    sessionId,
    preference: {
      provider: desktopDbPayloadString(source, 'provider'),
      model: desktopDbPayloadString(source, 'model'),
      thinking: desktopDbPayloadString(source, 'thinking'),
      serviceTier: desktopDbPayloadString(source, 'service_tier'),
      contextMode: desktopDbPayloadString(source, 'context_mode'),
      updatedAt: desktopDbPayloadNumber(source, 'updated_at') || ts,
    },
    contextWindow: 0,
    maxOutputTokens: 0,
  }
}

function desktopDbPermissionFromDurablePayload(payload: Record<string, unknown>): DesktopPermissionRecord | null {
  const source = desktopDbNestedRecord(payload, 'permission') ?? payload
  const id = desktopDbPayloadString(source, 'id')
  const sessionId = desktopDbPayloadString(source, 'session_id')
  if (!id || !sessionId) {
    return null
  }
  return {
    id,
    sessionId,
    runId: desktopDbPayloadString(source, 'run_id'),
    callId: desktopDbPayloadString(source, 'call_id'),
    toolName: desktopDbPayloadString(source, 'tool_name'),
    toolArguments: desktopDbPayloadString(source, 'tool_arguments'),
    status: desktopDbPayloadString(source, 'status'),
    decision: desktopDbPayloadString(source, 'decision'),
    reason: desktopDbPayloadString(source, 'reason'),
    requirement: desktopDbPayloadString(source, 'requirement'),
    mode: desktopDbPayloadString(source, 'mode'),
    createdAt: desktopDbPayloadNumber(source, 'created_at'),
    updatedAt: desktopDbPayloadNumber(source, 'updated_at'),
    resolvedAt: desktopDbPayloadNumber(source, 'resolved_at'),
    permissionRequestedAt: desktopDbPayloadNumber(source, 'permission_requested_at'),
  }
}

function isDesktopNotificationRecord(value: unknown): value is DesktopNotificationRecord {
  return isRecord(value) && typeof value.id === 'string' && typeof value.title === 'string' && typeof value.eventType === 'string'
}

export function upsertDesktopDbRecord<T extends object>(collection: DesktopDbCollection<T>, record: T): void {
  const key = collection.getKeyFromItem(record)
  if (collection.has(key)) {
    collection.update(key, (draft) => {
      Object.assign(draft, record)
    })
    return
  }
  collection.insert(record)
}

export function replaceDesktopDbCollection<T extends object>(collection: DesktopDbCollection<T>, records: T[]): void {
  const nextKeys = new Set(records.map((record) => collection.getKeyFromItem(record)))
  for (const key of collection.keys()) {
    if (!nextKeys.has(key)) {
      collection.delete(key)
    }
  }
  for (const record of records) {
    upsertDesktopDbRecord(collection, record)
  }
}

function upsertDesktopDbRecords<T extends object>(collection: DesktopDbCollection<T>, records: T[]): void {
  for (const record of records) {
    upsertDesktopDbRecord(collection, record)
  }
}

function replaceDesktopDbRecordsForSessions<T extends object>(collection: DesktopDbCollection<T>, sessionIds: Set<string>, getSessionId: (record: T) => string, records: T[]): void {
  for (const record of Array.from(collection.values())) {
    if (sessionIds.has(getSessionId(record))) {
      collection.delete(collection.getKeyFromItem(record))
    }
  }
  upsertDesktopDbRecords(collection, records)
}

function upsertDesktopDbWorkspaces(records: DesktopDbWorkspaceRecord[]): void {
  for (const record of records) {
    const current = desktopWorkspacesCollection.get(record.workspacePath)
    if (!current) {
      upsertDesktopDbRecord(desktopWorkspacesCollection, record)
      continue
    }
    upsertDesktopDbRecord(desktopWorkspacesCollection, {
      ...current,
      workspaceName: record.workspaceName || current.workspaceName,
      sessionIds: Array.from(new Set([...record.sessionIds, ...current.sessionIds])),
      updatedAt: Math.max(current.updatedAt, record.updatedAt),
    })
  }
}

function mergeDesktopDbSessionOrder(sessionOrder: string[]): string[] {
  const existing = desktopWorksetMetaCollection.get('desktop-v3-workset')?.sessionOrder ?? []
  return Array.from(new Set([...sessionOrder, ...existing]))
}
