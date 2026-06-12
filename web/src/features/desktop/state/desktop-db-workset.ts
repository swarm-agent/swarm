import type { QueryClient } from '@tanstack/react-query'
import { apiFetch, readErrorMessage, requestJson } from '../../../app/api'
import { loadDesktopStateSnapshot } from './desktop-state-snapshot'
import { createDebugTimer, debugLog } from '../../../lib/debug-log'
import {
  applyWorksetToDesktopDB,
  desktopAgentModelPolicyCollection,
  desktopDbHistoryChunkKey,
  desktopDbOmissionKey,
  desktopEventsCollection,
  desktopMessagesCollection,
  desktopPlansCollection,
  desktopPlanRevisionsCollection,
  desktopPreferencesCollection,
  desktopProjectionsCollection,
  desktopRunIntentsCollection,
  desktopSessionReadinessCollection,
  desktopSessionsCollection,
  desktopWorksetOmissionsCollection,
  readDesktopDbMessages,
  readDesktopDbSession,
  upsertDesktopDbRecord,
  type DesktopV3HistoryChunkRecord,
  type DesktopV3Workset,
  type DesktopV3WorksetOmissionRecord,
  type DesktopV3WorksetPaginationRecord,
  type DesktopV3WorksetRequest,
  type DesktopV3WorksetWatermarksRecord,
} from './desktop-db'
import { applyDesktopChatRouteToSession, desktopChatRouteFromSessionMetadata } from '../chat/services/chat-routing'
import { mapDesktopSession, mapDesktopSessionPermission, mapDesktopSessionPlan, mapDesktopSessionPlanRevision, mapDesktopSessionUsageSummary } from '../chat/queries/chat-queries'
import { parseStructuredToolMessage } from '../chat/services/tool-message'
import { countApprovalRequiredPermissions } from '../permissions/services/permission-payload'
import type { AgentModelPolicyRecord, ChatMessageRecord, DesktopSessionPlanRecord, DesktopSessionPlanRevisionRecord, ResolvedSessionPreference } from '../chat/types/chat'
import type { DesktopRunIntentRecord, DesktopSessionRecord } from '../types/realtime'
import {
  desktopDBSessionQueryKey,
  desktopDBSessionSnapshotQueryKey,
  mergeDesktopDBSnapshotPatch,
  mergeDesktopV3Messages,
  type DesktopV3ProjectionCursor,
  type DesktopDBSessionSnapshot,
} from './desktop-db-snapshot'
export {
  desktopDBSessionQueryKey,
  desktopDBSessionSnapshotQueryKey,
  type DesktopDBSessionSnapshot,
} from './desktop-db-snapshot'

interface V3SessionWire {
  id?: string
  session_api?: string
  last_event_seq?: number
  projection_high_watermark_seq?: number
  preference?: V3PreferenceWire
  [key: string]: unknown
}

interface V3PreferenceWire {
  provider?: string
  model?: string
  thinking?: string
  service_tier?: string
  context_mode?: string
  updated_at?: number
}

type V3SessionProjectionWire = DesktopV3ProjectionCursor

interface V3MessageWire {
  id?: string
  session_id?: string
  global_seq?: number
  role?: string
  content?: string
  created_at?: number
  metadata?: Record<string, unknown>
}

interface V3AgentModelPolicyWire {
  agent_name?: string
  resolved_agent_name?: string
  source?: string
  locked?: boolean
  reason?: string
  preference?: V3PreferenceWire
  context_window?: number
  max_output_tokens?: number
}

interface V3RunIntentWire {
  session_id?: string
  run_id?: string
  status?: string
  blocked_reason?: string
  created_at?: number
  updated_at?: number
  event_seq?: number
}

declare global {
  interface Window {
    __swarmV3SessionPreload?: {
      sessionId?: string
      startedAt?: number
      promise?: Promise<V3HydratedSessionResponseWire | null>
    }
  }
}

interface V3HydratedSessionResponseWire {
  session?: V3SessionWire
  projection?: V3SessionProjectionWire
  messages?: V3MessageWire[]
  events?: unknown[]
  pending_permissions?: unknown[]
  usage_summary?: unknown
  active_run_intent?: V3RunIntentWire | null
  applied_seq?: number
  high_watermark?: number
  preference?: V3PreferenceWire
  context_window?: number
  max_output_tokens?: number
  agent_model_policy?: V3AgentModelPolicyWire
  has_active_plan?: boolean
  active_plan?: unknown
  plan_revisions?: unknown[]
}

interface V3WorksetRequestWire {
  session_ids?: string[]
  workspace?: { workspace_path?: string; workspace_paths?: string[] }
  recent?: { limit?: number; before_updated_at?: number | null; before_session_id?: string }
  history?: { mode?: string; max_messages_per_session?: number; max_events_per_session?: number; manifest_policy?: string; include_events?: boolean }
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

export interface DesktopV3HistoryChunk {
  chunk_id?: string
  resource?: string
  session_id?: string
  messages?: V3MessageWire[]
  events?: unknown[]
}

export interface DesktopV3WorksetOmissionWire {
  session_id?: string
  resource?: string
  reason?: string
  next_cursor?: string
  manifest_ref?: string
}

export interface DesktopV3WorksetPaginationWire {
  next_before_updated_at?: number | null
  next_before_session_id?: string
  has_more?: boolean
}

export interface DesktopV3WorksetWatermarksWire {
  loaded_at?: number
  max_updated_at?: number
}

interface V3WorksetResponseWire {
  sessions_by_id?: Record<string, V3SessionWire>
  projections_by_session?: Record<string, V3SessionProjectionWire>
  messages_by_session?: Record<string, V3MessageWire[]>
  events_by_session?: Record<string, unknown[]>
  plans_by_session?: Record<string, unknown>
  plan_revisions_by_session?: Record<string, unknown[]>
  permissions_by_session?: Record<string, unknown[]>
  usage_by_session?: Record<string, unknown>
  preferences_by_session?: Record<string, V3PreferenceWire>
  agent_model_policy_by_session?: Record<string, V3AgentModelPolicyWire>
  run_intents_by_session?: Record<string, V3RunIntentWire[]>
  history_manifests_by_session?: Record<string, DesktopV3HistoryChunkDescriptor[]>
  history_chunks_by_id?: Record<string, DesktopV3HistoryChunk>
  omissions?: DesktopV3WorksetOmissionWire[]
  pagination?: DesktopV3WorksetPaginationWire
  watermarks?: DesktopV3WorksetWatermarksWire
  session_order?: string[]
}

const DEFAULT_WORKSET_HISTORY = {
  mode: 'full' as const,
  maxEventsPerSession: 0,
  manifestPolicy: 'manifest' as const,
  includeEvents: false,
}

const DESKTOP_DB_WORKSET_DURABLE_DB_NAME = 'swarm-desktop-v3-workset-cache'
const DESKTOP_DB_WORKSET_DURABLE_DB_VERSION = 1
const DESKTOP_DB_WORKSET_DURABLE_STORE = 'worksets'

interface DesktopDBDurableWorksetEntry {
  id: string
  request: DesktopV3WorksetRequest
  workset: DesktopV3Workset
  savedAt: number
}

const memoryDurableWorksets = new Map<string, DesktopDBDurableWorksetEntry>()
let durableWorksetDBPromise: Promise<IDBDatabase> | null = null

export function desktopDBWorksetCacheQueryKey() {
  return ['desktop-v3-workset-cache'] as const
}

export function desktopDBWorksetQueryKey(scope: string) {
  return ['desktop-v3-workset', scope.trim()] as const
}

function normalizedWorksetRequest(input: DesktopV3WorksetRequest): DesktopV3WorksetRequest {
  return {
    sessionIds: (input.sessionIds ?? []).map((sessionId) => sessionId.trim()).filter(Boolean),
    workspacePath: input.workspacePath?.trim() || undefined,
    workspacePaths: (input.workspacePaths ?? []).map((workspacePath) => workspacePath.trim()).filter(Boolean).sort(),
    recent: input.recent
      ? {
          limit: input.recent.limit,
          beforeUpdatedAt: input.recent.beforeUpdatedAt ?? undefined,
          beforeSessionId: input.recent.beforeSessionId?.trim() || undefined,
        }
      : undefined,
    history: input.history
      ? {
          mode: input.history.mode,
          maxMessagesPerSession: input.history.maxMessagesPerSession,
          maxEventsPerSession: input.history.maxEventsPerSession,
          manifestPolicy: input.history.manifestPolicy,
          includeEvents: input.history.includeEvents,
        }
      : undefined,
  }
}

export function desktopDBWorksetDurableCacheKey(input: DesktopV3WorksetRequest): string {
  return JSON.stringify(normalizedWorksetRequest(input))
}

function openDesktopDBWorksetDurableDB(): Promise<IDBDatabase | null> {
  if (typeof indexedDB === 'undefined') {
    return Promise.resolve(null)
  }
  if (durableWorksetDBPromise) {
    return durableWorksetDBPromise
  }
  const promise = new Promise<IDBDatabase>((resolve, reject) => {
    const request = indexedDB.open(DESKTOP_DB_WORKSET_DURABLE_DB_NAME, DESKTOP_DB_WORKSET_DURABLE_DB_VERSION)
    request.onupgradeneeded = () => {
      const db = request.result
      if (!db.objectStoreNames.contains(DESKTOP_DB_WORKSET_DURABLE_STORE)) {
        db.createObjectStore(DESKTOP_DB_WORKSET_DURABLE_STORE, { keyPath: 'id' })
      }
    }
    request.onsuccess = () => resolve(request.result)
    request.onerror = () => reject(request.error ?? new Error('failed to open Desktop V3 workset durable cache'))
    request.onblocked = () => reject(new Error('Desktop V3 workset durable cache open was blocked'))
  }).catch((error) => {
    durableWorksetDBPromise = null
    throw error
  })
  durableWorksetDBPromise = promise
  return promise
}

export async function readDesktopDBDurableWorkset(input: DesktopV3WorksetRequest): Promise<DesktopV3Workset | null> {
  const id = desktopDBWorksetDurableCacheKey(input)
  const db = await openDesktopDBWorksetDurableDB()
  if (!db) {
    return memoryDurableWorksets.get(id)?.workset ?? null
  }
  return new Promise((resolve, reject) => {
    const transaction = db.transaction(DESKTOP_DB_WORKSET_DURABLE_STORE, 'readonly')
    const store = transaction.objectStore(DESKTOP_DB_WORKSET_DURABLE_STORE)
    const request = store.get(id)
    request.onsuccess = () => resolve((request.result as DesktopDBDurableWorksetEntry | undefined)?.workset ?? null)
    request.onerror = () => reject(request.error ?? new Error('failed to read Desktop V3 workset durable cache'))
  })
}

export async function writeDesktopDBDurableWorkset(input: DesktopV3WorksetRequest, workset: DesktopV3Workset): Promise<void> {
  const entry: DesktopDBDurableWorksetEntry = {
    id: desktopDBWorksetDurableCacheKey(input),
    request: normalizedWorksetRequest(input),
    workset,
    savedAt: Date.now(),
  }
  const db = await openDesktopDBWorksetDurableDB()
  if (!db) {
    memoryDurableWorksets.set(entry.id, entry)
    return
  }
  await new Promise<void>((resolve, reject) => {
    const transaction = db.transaction(DESKTOP_DB_WORKSET_DURABLE_STORE, 'readwrite')
    const store = transaction.objectStore(DESKTOP_DB_WORKSET_DURABLE_STORE)
    const request = store.put(entry)
    request.onsuccess = () => resolve()
    request.onerror = () => reject(request.error ?? new Error('failed to write Desktop V3 workset durable cache'))
  })
}

export async function seedDesktopDBWorksetFromDurableCache(queryClient: QueryClient, input: DesktopV3WorksetRequest): Promise<DesktopV3Workset | null> {
  const finishRead = createDebugTimer('desktop-db-workset', 'durable-cache-read', { key: desktopDBWorksetDurableCacheKey(input) })
  const cached = await readDesktopDBDurableWorkset(input)
  finishRead({ hit: Boolean(cached), sessionCount: cached?.sessionOrder.length ?? 0 })
  if (!cached) {
    return null
  }
  const finishApply = createDebugTimer('desktop-db-workset', 'durable-cache-apply', { sessionCount: cached.sessionOrder.length })
  writeDesktopDBWorkset(queryClient, cached, input, { persist: false })
  finishApply({ sessionCount: cached.sessionOrder.length })
  return cached
}

export function assertRawCanonicalDesktopV3SessionId(sessionId: string): string {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    throw new Error('Desktop V3 requires a raw canonical session id.')
  }
  return normalizedSessionId
}

function mapProjectionToSession(session: V3SessionWire, projection: V3SessionProjectionWire | null | undefined): V3SessionWire {
  if (!projection || typeof projection !== 'object') {
    return {
      ...session,
      session_api: String(session.session_api ?? '').trim() || 'v3',
    }
  }
  return {
    ...session,
    session_api: String(session.session_api ?? '').trim() || 'v3',
    last_event_seq: typeof projection.last_event_seq === 'number' ? projection.last_event_seq : session.last_event_seq,
    projection_high_watermark_seq: typeof projection.projection_high_watermark_seq === 'number'
      ? projection.projection_high_watermark_seq
      : session.projection_high_watermark_seq,
  }
}

function applyProjectionCursor(session: DesktopSessionRecord, projection: V3SessionProjectionWire | null | undefined): DesktopSessionRecord {
  if (!projection || typeof projection !== 'object') {
    return {
      ...session,
      sessionApi: session.sessionApi || 'v3',
    }
  }
  return {
    ...session,
    sessionApi: session.sessionApi || 'v3',
    lastEventSeq: typeof projection.last_event_seq === 'number' ? projection.last_event_seq : (session.lastEventSeq ?? 0),
    projectionHighWatermarkSeq: typeof projection.projection_high_watermark_seq === 'number'
      ? projection.projection_high_watermark_seq
      : (session.projectionHighWatermarkSeq ?? 0),
  }
}

function mapChatMessage(message: V3MessageWire): ChatMessageRecord {
  const content = String(message.content ?? '')
  return {
    id: String(message.id ?? '').trim(),
    sessionId: String(message.session_id ?? '').trim(),
    globalSeq: typeof message.global_seq === 'number' ? message.global_seq : 0,
    role: String(message.role ?? '').trim(),
    content,
    createdAt: typeof message.created_at === 'number' ? message.created_at : 0,
    metadata: message.metadata,
    toolMessage: parseStructuredToolMessage(content),
  }
}

function mapPreferenceWire(source: V3PreferenceWire | null | undefined) {
  return {
    provider: String(source?.provider ?? '').trim(),
    model: String(source?.model ?? '').trim(),
    thinking: String(source?.thinking ?? '').trim(),
    serviceTier: String(source?.service_tier ?? '').trim(),
    contextMode: String(source?.context_mode ?? '').trim(),
    updatedAt: typeof source?.updated_at === 'number' ? source.updated_at : 0,
  }
}

function mapDesktopV3SessionPreference(response: V3HydratedSessionResponseWire): ResolvedSessionPreference {
  const sessionSource = response.session?.preference
  const source: V3PreferenceWire = response.preference ?? sessionSource ?? {}
  return {
    preference: mapPreferenceWire(source),
    contextWindow: typeof response.context_window === 'number' ? response.context_window : 0,
    maxOutputTokens: typeof response.max_output_tokens === 'number' ? response.max_output_tokens : 0,
  }
}

function mapDesktopV3AgentModelPolicy(policy: V3AgentModelPolicyWire | null | undefined): AgentModelPolicyRecord | null {
  if (!policy || typeof policy !== 'object') {
    return null
  }
  return {
    agentName: String(policy.agent_name ?? '').trim(),
    resolvedAgentName: String(policy.resolved_agent_name ?? '').trim(),
    source: String(policy.source ?? '').trim(),
    locked: Boolean(policy.locked),
    reason: String(policy.reason ?? '').trim(),
    preference: mapPreferenceWire(policy.preference),
    contextWindow: typeof policy.context_window === 'number' ? policy.context_window : 0,
    maxOutputTokens: typeof policy.max_output_tokens === 'number' ? policy.max_output_tokens : 0,
  }
}

function mapV3RunIntent(intent: V3RunIntentWire | null | undefined): DesktopRunIntentRecord | null {
  if (!intent || typeof intent !== 'object') {
    return null
  }
  return {
    sessionId: String(intent.session_id ?? '').trim(),
    runId: String(intent.run_id ?? '').trim(),
    status: String(intent.status ?? '').trim(),
    blockedReason: String(intent.blocked_reason ?? '').trim(),
    createdAt: typeof intent.created_at === 'number' ? intent.created_at : 0,
    updatedAt: typeof intent.updated_at === 'number' ? intent.updated_at : 0,
    eventSeq: typeof intent.event_seq === 'number' ? intent.event_seq : 0,
  }
}

function v3RunIntentStatusActive(status: string): boolean {
  const normalized = status.trim().toLowerCase()
  return normalized === 'pending_executor' || normalized === 'running'
}

function mapLiveStatusFromRunIntent(status: string): DesktopSessionRecord['live']['status'] {
  switch (status.trim().toLowerCase()) {
    case 'pending_executor':
      return 'starting'
    case 'running':
      return 'running'
    case 'dispatch_blocked':
      return 'blocked'
    case 'failed':
    case 'cancelled':
    case 'expired':
    case 'interrupted':
      return 'error'
    default:
      return 'idle'
  }
}

function applyActiveRunIntent(session: DesktopSessionRecord, runIntent: DesktopRunIntentRecord | null): DesktopSessionRecord {
  if (!runIntent || !v3RunIntentStatusActive(runIntent.status)) {
    return { ...session, runIntent: null }
  }
  return {
    ...session,
    sessionApi: session.sessionApi || 'v3',
    runIntent,
    live: {
      ...session.live,
      runId: runIntent.runId || session.live.runId,
      startedAt: runIntent.createdAt > 0 ? runIntent.createdAt : session.live.startedAt,
      status: mapLiveStatusFromRunIntent(runIntent.status),
      summary: runIntent.status.trim().toLowerCase() === 'pending_executor'
        ? 'Pending executor…'
        : session.live.summary,
      error: null,
      lastEventAt: runIntent.updatedAt > 0 ? runIntent.updatedAt : session.live.lastEventAt,
    },
  }
}

function latestActiveRunIntent(intents: V3RunIntentWire[] | undefined): V3RunIntentWire | null {
  if (!Array.isArray(intents) || intents.length === 0) {
    return null
  }
  const active = intents.filter((intent) => v3RunIntentStatusActive(String(intent.status ?? '')))
  const candidates = active.length > 0 ? active : intents
  return candidates.reduce<V3RunIntentWire | null>((latest, intent) => {
    if (!latest) {
      return intent
    }
    const latestSeq = typeof latest.event_seq === 'number' ? latest.event_seq : 0
    const intentSeq = typeof intent.event_seq === 'number' ? intent.event_seq : 0
    return intentSeq >= latestSeq ? intent : latest
  }, null)
}

export function mapDesktopDBSessionSnapshot(response: V3HydratedSessionResponseWire): DesktopDBSessionSnapshot | null {
  const mappedBaseSession = applyActiveRunIntent(
    mapDesktopSession(mapProjectionToSession(response.session ?? {}, response.projection)),
    mapV3RunIntent(response.active_run_intent),
  )
  if (!mappedBaseSession.id) {
    return null
  }
  const session = applyProjectionCursor(
    applyDesktopChatRouteToSession(mappedBaseSession, desktopChatRouteFromSessionMetadata(mappedBaseSession)),
    response.projection,
  )
  const pendingPermissions = Array.isArray(response.pending_permissions)
    ? response.pending_permissions
        .map((permission) => mapDesktopSessionPermission(permission))
        .filter((permission) => permission.id !== '' && permission.sessionId !== '' && permission.status === 'pending')
    : []
  return {
    source: 'v3',
    session: {
      ...session,
      permissionsHydrated: true,
      pendingPermissions,
      pendingPermissionCount: countApprovalRequiredPermissions(pendingPermissions, session.mode),
      usage: mapDesktopSessionUsageSummary(response.usage_summary),
    },
    messages: Array.isArray(response.messages) ? response.messages.map(mapChatMessage).filter((message) => message.id !== '' && message.sessionId !== '') : [],
    events: Array.isArray(response.events) ? response.events : [],
    projection: response.projection ?? null,
    preference: mapDesktopV3SessionPreference(response),
    agentModelPolicy: mapDesktopV3AgentModelPolicy(response.agent_model_policy),
    appliedSeq: Math.max(0, response.applied_seq ?? response.projection?.last_event_seq ?? session.lastEventSeq ?? 0),
    highWatermark: Math.max(0, response.high_watermark ?? response.projection?.projection_high_watermark_seq ?? session.projectionHighWatermarkSeq ?? 0),
    hasActivePlan: Boolean(response.has_active_plan),
    activePlan: response.has_active_plan ? mapDesktopSessionPlan(response.active_plan) : null,
    planRevisions: Array.isArray(response.plan_revisions)
      ? response.plan_revisions.map((revision, index) => mapDesktopSessionPlanRevision(revision, index))
      : [],
    hydratedAt: Date.now(),
  }
}

function firstPlanForSession(value: unknown): unknown {
  return Array.isArray(value) ? value[0] : value
}

function worksetChunkMessagesForSession(response: V3WorksetResponseWire, sessionId: string): V3MessageWire[] {
  const chunks = response.history_chunks_by_id ?? {}
  const messages: V3MessageWire[] = []
  for (const chunk of Object.values(chunks)) {
    if (chunk?.resource && chunk.resource !== 'messages') {
      continue
    }
    for (const message of chunk?.messages ?? []) {
      if (String(message.session_id ?? '').trim() === sessionId) {
        messages.push(message)
      }
    }
  }
  return messages
}

function worksetChunkEventsForSession(response: V3WorksetResponseWire, sessionId: string): unknown[] {
  const chunks = response.history_chunks_by_id ?? {}
  const events: unknown[] = []
  for (const chunk of Object.values(chunks)) {
    if (chunk?.resource && chunk.resource !== 'events') {
      continue
    }
    for (const event of chunk?.events ?? []) {
      const record = event && typeof event === 'object' ? event as Record<string, unknown> : null
      if (!record || String(record.session_id ?? '').trim() === sessionId) {
        events.push(event)
      }
    }
  }
  return events
}

function worksetSessionResponse(response: V3WorksetResponseWire, sessionId: string): V3HydratedSessionResponseWire {
  const session = response.sessions_by_id?.[sessionId]
  const projection = response.projections_by_session?.[sessionId]
  const inlineMessages = response.messages_by_session?.[sessionId] ?? []
  const chunkMessages = worksetChunkMessagesForSession(response, sessionId)
  const inlineEvents = response.events_by_session?.[sessionId] ?? []
  const chunkEvents = worksetChunkEventsForSession(response, sessionId)
  const activePlan = firstPlanForSession(response.plans_by_session?.[sessionId])
  const runIntent = latestActiveRunIntent(response.run_intents_by_session?.[sessionId])
  return {
    session,
    projection,
    messages: [...inlineMessages, ...chunkMessages],
    events: [...inlineEvents, ...chunkEvents],
    pending_permissions: response.permissions_by_session?.[sessionId] ?? [],
    usage_summary: response.usage_by_session?.[sessionId],
    preference: response.preferences_by_session?.[sessionId] ?? session?.preference,
    agent_model_policy: response.agent_model_policy_by_session?.[sessionId],
    active_run_intent: runIntent,
    has_active_plan: Boolean(activePlan),
    active_plan: activePlan,
    plan_revisions: response.plan_revisions_by_session?.[sessionId] ?? [],
  }
}

function sessionOrderFromWorkset(response: V3WorksetResponseWire): string[] {
  const seen = new Set<string>()
  const ordered: string[] = []
  for (const sessionId of response.session_order ?? []) {
    const normalized = sessionId.trim()
    if (normalized && !seen.has(normalized)) {
      seen.add(normalized)
      ordered.push(normalized)
    }
  }
  for (const sessionId of Object.keys(response.sessions_by_id ?? {})) {
    const normalized = sessionId.trim()
    if (normalized && !seen.has(normalized)) {
      seen.add(normalized)
      ordered.push(normalized)
    }
  }
  return ordered
}

function mapWorksetHistoryChunks(response: V3WorksetResponseWire): Record<string, DesktopV3HistoryChunkRecord> {
  const records: Record<string, DesktopV3HistoryChunkRecord> = {}
  for (const [chunkId, chunk] of Object.entries(response.history_chunks_by_id ?? {})) {
    const normalizedChunkId = String(chunk.chunk_id ?? chunkId).trim() || chunkId
    const resource = String(chunk.resource ?? '').trim() || 'messages'
    const sessionId = String(chunk.session_id ?? '').trim()
      || chunk.messages?.map((message) => String(message.session_id ?? '').trim()).find(Boolean)
      || ''
    const id = desktopDbHistoryChunkKey(sessionId, normalizedChunkId, resource)
    records[id] = {
      id,
      sessionId,
      chunkId: normalizedChunkId,
      resource,
      messages: (chunk.messages ?? []).map(mapChatMessage).filter((message) => message.id !== '' && message.sessionId !== ''),
      events: chunk.events ?? [],
    }
  }
  return records
}

function mapWorksetOmissions(response: V3WorksetResponseWire): DesktopV3WorksetOmissionRecord[] {
  return (response.omissions ?? []).map((omission) => {
    const record = {
      sessionId: String(omission.session_id ?? '').trim(),
      resource: String(omission.resource ?? '').trim(),
      reason: String(omission.reason ?? '').trim(),
      nextCursor: String(omission.next_cursor ?? '').trim(),
      manifestRef: String(omission.manifest_ref ?? '').trim(),
    }
    return {
      id: desktopDbOmissionKey(record),
      ...record,
    }
  }).filter((omission) => omission.sessionId !== '' || omission.resource !== '' || omission.reason !== '')
}

function mapWorksetPagination(response: V3WorksetResponseWire): DesktopV3WorksetPaginationRecord {
  const pagination = response.pagination ?? {}
  return {
    id: 'desktop-v3-workset-pagination',
    nextBeforeUpdatedAt: typeof pagination.next_before_updated_at === 'number' ? pagination.next_before_updated_at : null,
    nextBeforeSessionId: String(pagination.next_before_session_id ?? '').trim(),
    hasMore: Boolean(pagination.has_more),
  }
}

function mapWorksetWatermarks(response: V3WorksetResponseWire, loadedAt: number): DesktopV3WorksetWatermarksRecord {
  const watermarks = response.watermarks ?? {}
  return {
    id: 'desktop-v3-workset-watermarks',
    loadedAt: typeof watermarks.loaded_at === 'number' ? watermarks.loaded_at : loadedAt,
    maxUpdatedAt: typeof watermarks.max_updated_at === 'number' ? watermarks.max_updated_at : 0,
  }
}

export function mapDesktopV3Workset(response: V3WorksetResponseWire): DesktopV3Workset {
  const sessionOrder = sessionOrderFromWorkset(response)
  const loadedAt = Date.now()
  const sessionsById: Record<string, DesktopSessionRecord> = {}
  const projectionsBySession: Record<string, DesktopV3ProjectionCursor> = {}
  const messagesBySession: Record<string, ChatMessageRecord[]> = {}
  const eventsBySession: Record<string, unknown[]> = {}
  const preferencesBySession: Record<string, ResolvedSessionPreference> = {}
  const agentModelPolicyBySession: Record<string, AgentModelPolicyRecord | null> = {}
  const hasActivePlanBySession: Record<string, boolean> = {}
  const plansBySession: Record<string, DesktopSessionPlanRecord | null> = {}
  const planRevisionsBySession: Record<string, DesktopSessionPlanRevisionRecord[]> = {}
  const appliedSeqBySession: Record<string, number> = {}
  const highWatermarkBySession: Record<string, number> = {}

  for (const sessionId of sessionOrder) {
    const snapshot = mapDesktopDBSessionSnapshot(worksetSessionResponse(response, sessionId))
    if (!snapshot?.session.id) {
      continue
    }
    sessionsById[snapshot.session.id] = snapshot.session
    if (snapshot.projection) {
      projectionsBySession[snapshot.session.id] = snapshot.projection
    }
    messagesBySession[snapshot.session.id] = snapshot.messages
    eventsBySession[snapshot.session.id] = snapshot.events
    preferencesBySession[snapshot.session.id] = snapshot.preference
    agentModelPolicyBySession[snapshot.session.id] = snapshot.agentModelPolicy
    hasActivePlanBySession[snapshot.session.id] = snapshot.hasActivePlan
    plansBySession[snapshot.session.id] = snapshot.activePlan
    planRevisionsBySession[snapshot.session.id] = snapshot.planRevisions
    appliedSeqBySession[snapshot.session.id] = snapshot.appliedSeq
    highWatermarkBySession[snapshot.session.id] = snapshot.highWatermark
  }

  return {
    source: 'v3-workset',
    sessionsById,
    projectionsBySession,
    messagesBySession,
    eventsBySession,
    preferencesBySession,
    agentModelPolicyBySession,
    hasActivePlanBySession,
    plansBySession,
    planRevisionsBySession,
    appliedSeqBySession,
    highWatermarkBySession,
    historyManifestsBySession: response.history_manifests_by_session ?? {},
    historyChunksById: mapWorksetHistoryChunks(response),
    omissions: mapWorksetOmissions(response),
    pagination: mapWorksetPagination(response),
    watermarks: mapWorksetWatermarks(response, loadedAt),
    sessionOrder: sessionOrder.filter((sessionId) => Boolean(sessionsById[sessionId])),
    loadedAt,
  }
}

function mergeRecord<T>(current: Record<string, T>, incoming: Record<string, T>): Record<string, T> {
  return { ...current, ...incoming }
}

function mergeWorksetMessages(current: Record<string, ChatMessageRecord[]>, incoming: Record<string, ChatMessageRecord[]>): Record<string, ChatMessageRecord[]> {
  const next = { ...current }
  for (const [sessionId, messages] of Object.entries(incoming)) {
    next[sessionId] = mergeDesktopV3Messages(next[sessionId], messages)
  }
  return next
}

function mergeWorksetEvents(current: Record<string, unknown[]>, incoming: Record<string, unknown[]>): Record<string, unknown[]> {
  return { ...current, ...incoming }
}

function mergeWorksetOmissions(current: DesktopV3WorksetOmissionRecord[], incoming: DesktopV3WorksetOmissionRecord[]): DesktopV3WorksetOmissionRecord[] {
  const byId = new Map<string, DesktopV3WorksetOmissionRecord>()
  for (const omission of [...current, ...incoming]) {
    byId.set(omission.id, omission)
  }
  return Array.from(byId.values())
}

function mergeSessionOrder(current: string[], incoming: string[]): string[] {
  const seen = new Set<string>()
  const next: string[] = []
  for (const sessionId of [...incoming, ...current]) {
    const normalized = sessionId.trim()
    if (normalized && !seen.has(normalized)) {
      seen.add(normalized)
      next.push(normalized)
    }
  }
  return next
}

function mergeDesktopV3Worksets(current: DesktopV3Workset | null | undefined, incoming: DesktopV3Workset): DesktopV3Workset {
  if (!current) {
    return incoming
  }
  return {
    ...incoming,
    sessionsById: mergeRecord(current.sessionsById, incoming.sessionsById),
    projectionsBySession: mergeRecord(current.projectionsBySession, incoming.projectionsBySession),
    messagesBySession: mergeWorksetMessages(current.messagesBySession, incoming.messagesBySession),
    eventsBySession: mergeWorksetEvents(current.eventsBySession, incoming.eventsBySession),
    preferencesBySession: mergeRecord(current.preferencesBySession, incoming.preferencesBySession),
    agentModelPolicyBySession: mergeRecord(current.agentModelPolicyBySession, incoming.agentModelPolicyBySession),
    hasActivePlanBySession: mergeRecord(current.hasActivePlanBySession, incoming.hasActivePlanBySession),
    plansBySession: mergeRecord(current.plansBySession, incoming.plansBySession),
    planRevisionsBySession: mergeRecord(current.planRevisionsBySession, incoming.planRevisionsBySession),
    appliedSeqBySession: mergeRecord(current.appliedSeqBySession, incoming.appliedSeqBySession),
    highWatermarkBySession: mergeRecord(current.highWatermarkBySession, incoming.highWatermarkBySession),
    historyManifestsBySession: mergeRecord(current.historyManifestsBySession, incoming.historyManifestsBySession),
    historyChunksById: mergeRecord(current.historyChunksById, incoming.historyChunksById),
    omissions: mergeWorksetOmissions(current.omissions, incoming.omissions),
    sessionOrder: mergeSessionOrder(current.sessionOrder, incoming.sessionOrder),
    loadedAt: incoming.loadedAt,
  }
}

export function desktopDBSessionSnapshotFromWorkset(workset: DesktopV3Workset | null | undefined, sessionId: string): DesktopDBSessionSnapshot | null {
  const normalizedSessionId = sessionId.trim()
  if (!workset || !normalizedSessionId) {
    return null
  }
  const session = workset.sessionsById[normalizedSessionId]
  if (!session) {
    return null
  }
  return {
    source: 'v3',
    session,
    messages: workset.messagesBySession[normalizedSessionId] ?? [],
    events: workset.eventsBySession[normalizedSessionId] ?? [],
    projection: workset.projectionsBySession[normalizedSessionId] ?? null,
    preference: workset.preferencesBySession[normalizedSessionId] ?? mapDesktopV3SessionPreference({ session: { id: session.id, preference: undefined } }),
    agentModelPolicy: workset.agentModelPolicyBySession[normalizedSessionId] ?? null,
    hasActivePlan: workset.hasActivePlanBySession[normalizedSessionId] ?? false,
    activePlan: workset.plansBySession[normalizedSessionId] ?? null,
    planRevisions: workset.planRevisionsBySession[normalizedSessionId] ?? [],
    appliedSeq: workset.appliedSeqBySession[normalizedSessionId] ?? Math.max(0, session.lastEventSeq ?? 0),
    highWatermark: workset.highWatermarkBySession[normalizedSessionId] ?? Math.max(0, session.projectionHighWatermarkSeq ?? session.lastEventSeq ?? 0),
    hydratedAt: workset.loadedAt,
  }
}

export function writeDesktopDBWorkset(
  queryClient: QueryClient,
  workset: DesktopV3Workset,
  request: DesktopV3WorksetRequest | null = null,
  options: { persist?: boolean } = {},
): DesktopV3Workset {
  const cacheKey = desktopDBWorksetCacheQueryKey()
  const current = queryClient.getQueryData<DesktopV3Workset>(cacheKey) ?? null
  const merged = mergeDesktopV3Worksets(current, workset)
  const finishApply = createDebugTimer('desktop-db-workset', 'tanstack-db-write', { sessionCount: merged.sessionOrder.length })
  applyWorksetToDesktopDB(merged, request)
  finishApply({ sessionCount: merged.sessionOrder.length })
  queryClient.setQueryData(cacheKey, merged)
  if (options.persist !== false && request) {
    void writeDesktopDBDurableWorkset(request, merged).catch((error) => {
      debugLog('desktop-db-workset', 'durable-cache-write:error', { message: error instanceof Error ? error.message : String(error) })
    })
  }
  return merged
}

export function readDesktopDBWorkset(queryClient: QueryClient): DesktopV3Workset | null {
  return queryClient.getQueryData<DesktopV3Workset>(desktopDBWorksetCacheQueryKey()) ?? null
}

export function readDesktopDBWorksetSession(queryClient: QueryClient, sessionId: string): DesktopSessionRecord | null {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    return null
  }
  return readDesktopDbSession(normalizedSessionId) ?? readDesktopDBWorkset(queryClient)?.sessionsById[normalizedSessionId] ?? null
}

export function desktopV3WorksetHasOmission(queryClient: QueryClient, sessionId: string, resource?: string): boolean {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    return false
  }
  const dbOmission = Array.from(desktopWorksetOmissionsCollection.values()).some((omission) => {
    if (omission.sessionId !== normalizedSessionId) {
      return false
    }
    return !resource || omission.resource === resource
  })
  if (dbOmission) {
    return true
  }
  const workset = readDesktopDBWorkset(queryClient)
  return Boolean(workset?.omissions.some((omission) => {
    if (omission.sessionId !== normalizedSessionId) {
      return false
    }
    return !resource || omission.resource === resource
  }))
}

function toWorksetRequestWire(input: DesktopV3WorksetRequest): V3WorksetRequestWire {
  const sessionIds = (input.sessionIds ?? []).map((sessionId) => sessionId.trim()).filter(Boolean)
  const workspacePath = input.workspacePath?.trim() ?? ''
  const workspacePaths = (input.workspacePaths ?? []).map((path) => path.trim()).filter(Boolean)
  const recent = input.recent
  const history = { ...DEFAULT_WORKSET_HISTORY, ...(input.history ?? {}) }
  return {
    session_ids: sessionIds.length > 0 ? sessionIds : undefined,
    workspace: workspacePath || workspacePaths.length > 0 ? {
      workspace_path: workspacePath || undefined,
      workspace_paths: workspacePaths.length > 0 ? workspacePaths : undefined,
    } : undefined,
    recent: recent
      ? {
          limit: recent.limit,
          before_updated_at: recent.beforeUpdatedAt ?? undefined,
          before_session_id: recent.beforeSessionId?.trim() || undefined,
        }
      : undefined,
    history: {
      mode: history.mode,
      max_messages_per_session: history.maxMessagesPerSession,
      max_events_per_session: history.maxEventsPerSession,
      manifest_policy: history.manifestPolicy,
      include_events: history.includeEvents,
    },
  }
}

function assertWorksetSelector(input: DesktopV3WorksetRequest): void {
  const hasSessionIds = (input.sessionIds ?? []).some((sessionId) => sessionId.trim() !== '')
  const hasWorkspace = Boolean(input.workspacePath?.trim()) || Boolean(input.workspacePaths?.some((path) => path.trim() !== ''))
  const hasRecent = typeof input.recent?.limit === 'number' && input.recent.limit > 0
  if (!hasSessionIds && !hasWorkspace && !hasRecent) {
    throw new Error('Desktop V3 workset request requires session ids, workspace path, or recent limit.')
  }
}

export async function fetchDesktopDBWorkset(input: DesktopV3WorksetRequest, signal?: AbortSignal): Promise<DesktopV3Workset> {
  assertWorksetSelector(input)
  const body = JSON.stringify(toWorksetRequestWire(input))
  const finishRequest = createDebugTimer('desktop-db-workset', 'workset-request', { endpoint: '/v3/sessions:workset', requestBytes: body.length })
  const response = await apiFetch(
    '/v3/sessions:workset',
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body,
      signal,
    },
  )
  finishRequest({ ok: response.ok, status: response.status })
  if (!response.ok) {
    throw new Error(await readErrorMessage(response))
  }
  const finishText = createDebugTimer('desktop-db-workset', 'workset-response-text')
  const text = await response.text()
  finishText({ responseBytes: text.length })
  const finishParse = createDebugTimer('desktop-db-workset', 'workset-json-parse', { responseBytes: text.length })
  const parsed = JSON.parse(text) as V3WorksetResponseWire
  finishParse({ sessionCount: Object.keys(parsed.sessions_by_id ?? {}).length })
  const finishMap = createDebugTimer('desktop-db-workset', 'workset-map')
  const workset = mapDesktopV3Workset(parsed)
  finishMap({ sessionCount: workset.sessionOrder.length, messageCount: Object.values(workset.messagesBySession).reduce((total, messages) => total + messages.length, 0) })
  return workset
}

export async function fetchAndApplyDesktopDBWorkset(
  queryClient: QueryClient,
  input: DesktopV3WorksetRequest,
  options: { signal?: AbortSignal } = {},
): Promise<DesktopV3Workset> {
  const workset = await fetchDesktopDBWorkset(input, options.signal)
  return writeDesktopDBWorkset(queryClient, workset, input)
}

function requireDesktopDBSessionSnapshot(response: V3HydratedSessionResponseWire, action: string): DesktopDBSessionSnapshot {
  const snapshot = mapDesktopDBSessionSnapshot(response)
  if (!snapshot) {
    throw new Error(`Desktop V3 ${action} requires a hydrated canonical session snapshot.`)
  }
  return snapshot
}

export async function fetchAndApplyDesktopDBWorksetSession(
  queryClient: QueryClient,
  sessionId: string,
  options: { signal?: AbortSignal } = {},
): Promise<DesktopDBSessionSnapshot | null> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const workset = await fetchAndApplyDesktopDBWorkset(queryClient, { sessionIds: [normalizedSessionId] }, options)
  return desktopDBSessionSnapshotFromWorkset(workset, normalizedSessionId)
}

export async function fetchAndApplyDesktopDBSessionSnapshot(
  queryClient: QueryClient,
  sessionId: string,
  options: { signal?: AbortSignal } = {},
): Promise<DesktopDBSessionSnapshot | null> {
  return fetchAndApplyDesktopDBWorksetSession(queryClient, sessionId, options)
}

function readPreloadedDesktopV3SessionResponse(sessionId: string): Promise<V3HydratedSessionResponseWire | null> | null {
  if (typeof window === 'undefined') {
    return null
  }
  const preload = window.__swarmV3SessionPreload
  if (!preload?.promise || String(preload.sessionId ?? '').trim() !== sessionId) {
    return null
  }
  window.__swarmV3SessionPreload = undefined
  return preload.promise
}

export async function fetchDesktopDBSessionSnapshot(sessionId: string, signal?: AbortSignal): Promise<DesktopDBSessionSnapshot | null> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const preloadedResponse = await readPreloadedDesktopV3SessionResponse(normalizedSessionId)
  if (preloadedResponse) {
    return mapDesktopDBSessionSnapshot(preloadedResponse)
  }
  const request = { sessionIds: [normalizedSessionId] }
  const workset = await fetchDesktopDBWorkset(request, signal)
  applyWorksetToDesktopDB(workset, request)
  return desktopDBSessionSnapshotFromWorkset(workset, normalizedSessionId)
}

export async function updateDesktopV3SessionMode(
  _queryClient: QueryClient,
  sessionId: string,
  mode: string,
): Promise<DesktopDBSessionSnapshot> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const response = await requestJson<V3HydratedSessionResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/mode`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode }),
    },
  )
  const snapshot = requireDesktopDBSessionSnapshot(response, 'mode update')
  await loadDesktopStateSnapshot({ sessionIds: [normalizedSessionId] })
  return snapshot
}

export async function updateDesktopV3SessionPreference(
  _queryClient: QueryClient,
  sessionId: string,
  input: Partial<ResolvedSessionPreference['preference']>,
): Promise<ResolvedSessionPreference> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const response = await requestJson<V3HydratedSessionResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/preference`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        provider: input.provider,
        model: input.model,
        thinking: input.thinking,
        service_tier: input.serviceTier,
        context_mode: input.contextMode,
      }),
    },
  )
  const snapshot = requireDesktopDBSessionSnapshot(response, 'preference update')
  await loadDesktopStateSnapshot({ sessionIds: [normalizedSessionId] })
  return snapshot.preference
}

export async function updateDesktopV3SessionAgent(
  _queryClient: QueryClient,
  sessionId: string,
  agentName: string,
): Promise<DesktopDBSessionSnapshot> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const response = await requestJson<V3HydratedSessionResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/agent`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ agent_name: agentName.trim() }),
    },
  )
  const snapshot = requireDesktopDBSessionSnapshot(response, 'agent update')
  await loadDesktopStateSnapshot({ sessionIds: [normalizedSessionId] })
  return snapshot
}

export async function updateDesktopV3SessionMetadata(
  _queryClient: QueryClient,
  sessionId: string,
  metadata: Record<string, unknown>,
): Promise<DesktopDBSessionSnapshot> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const response = await requestJson<V3HydratedSessionResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/metadata`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ metadata }),
    },
  )
  const snapshot = requireDesktopDBSessionSnapshot(response, 'metadata update')
  await loadDesktopStateSnapshot({ sessionIds: [normalizedSessionId] })
  return snapshot
}

export async function saveDesktopV3SessionPlan(
  _queryClient: QueryClient,
  sessionId: string,
  input: {
    id?: string;
    title?: string;
    plan?: string;
    document?: unknown;
    documentPatch?: unknown;
    status?: string;
    approvalState?: string;
  },
): Promise<DesktopDBSessionSnapshot> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const response = await requestJson<V3HydratedSessionResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/plans`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        id: input.id?.trim() || undefined,
        plan_id: input.id?.trim() || undefined,
        title: input.title?.trim() || undefined,
        plan: input.plan,
        document: input.document ?? undefined,
        document_patch: input.documentPatch ?? undefined,
        status: input.status?.trim() || undefined,
        approval_state: input.approvalState?.trim() || undefined,
      }),
    },
  )
  const snapshot = requireDesktopDBSessionSnapshot(response, 'plan save')
  await loadDesktopStateSnapshot({ sessionIds: [normalizedSessionId] })
  return snapshot
}

export function desktopDBSessionQueryOptions(sessionId: string) {
  const normalizedSessionId = sessionId.trim()
  return {
    queryKey: desktopDBSessionQueryKey(normalizedSessionId),
    queryFn: ({ signal }: { signal?: AbortSignal }) => fetchDesktopDBSessionSnapshot(normalizedSessionId, signal),
    staleTime: Number.POSITIVE_INFINITY,
    enabled: normalizedSessionId !== '',
  }
}

export function writeDesktopDBSessionSnapshot(
  queryClient: QueryClient,
  snapshot: DesktopDBSessionSnapshot,
): void {
  const sessionId = snapshot.session.id.trim()
  if (!sessionId) {
    return
  }

  upsertDesktopDbRecord(desktopSessionsCollection, snapshot.session)
  upsertDesktopDbRecord(desktopSessionReadinessCollection, {
    sessionId,
    status: 'ready',
    ready: true,
    missingResources: [],
    omittedResources: [],
    error: null,
    updatedAt: Date.now(),
  })
  for (const message of snapshot.messages) {
    upsertDesktopDbRecord(desktopMessagesCollection, message)
  }
  upsertDesktopDbRecord(desktopPreferencesCollection, { ...snapshot.preference, sessionId })
  upsertDesktopDbRecord(desktopAgentModelPolicyCollection, { sessionId, policy: snapshot.agentModelPolicy })
  upsertDesktopDbRecord(desktopPlansCollection, { sessionId, plan: snapshot.activePlan, hasActivePlan: snapshot.hasActivePlan })
  for (const revision of snapshot.planRevisions) {
    upsertDesktopDbRecord(desktopPlanRevisionsCollection, { ...revision, sessionId })
  }
  if (snapshot.session.runIntent) {
    upsertDesktopDbRecord(desktopRunIntentsCollection, snapshot.session.runIntent)
  }

  mergeDesktopDBSnapshotPatch(queryClient, { sessionId, snapshot })
}

export async function ensureDesktopDBSessionSnapshot(
  queryClient: QueryClient,
  sessionId: string,
): Promise<DesktopDBSessionSnapshot | null> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const cached = readDesktopDBSessionSnapshot(queryClient, normalizedSessionId)
  if (cached) {
    writeDesktopDBSessionSnapshot(queryClient, cached)
    return cached
  }

  return fetchAndApplyDesktopDBWorksetSession(queryClient, normalizedSessionId)
}

function desktopDBSessionSnapshotFromDesktopDB(sessionId: string): DesktopDBSessionSnapshot | null {
  const normalizedSessionId = sessionId.trim()
  const session = readDesktopDbSession(normalizedSessionId)
  if (!session) {
    return null
  }
  const projection = desktopProjectionsCollection.get(normalizedSessionId) ?? null
  const preference = desktopPreferencesCollection.get(normalizedSessionId) ?? mapDesktopV3SessionPreference({ session: { id: session.id, preference: undefined } })
  const agentModelPolicy = desktopAgentModelPolicyCollection.get(normalizedSessionId)?.policy ?? null
  const plan = desktopPlansCollection.get(normalizedSessionId)
  const events = Array.from(desktopEventsCollection.values())
    .filter((event) => event.sessionId === normalizedSessionId)
    .map((event) => event.event)
  return {
    source: 'v3',
    session,
    messages: readDesktopDbMessages(normalizedSessionId),
    events,
    projection,
    preference,
    agentModelPolicy,
    hasActivePlan: plan?.hasActivePlan ?? false,
    activePlan: plan?.plan ?? null,
    planRevisions: Array.from(desktopPlanRevisionsCollection.values()).filter((revision) => revision.sessionId === normalizedSessionId),
    appliedSeq: Math.max(0, projection?.last_event_seq ?? session.lastEventSeq ?? 0),
    highWatermark: Math.max(0, projection?.projection_high_watermark_seq ?? session.projectionHighWatermarkSeq ?? session.lastEventSeq ?? 0),
    hydratedAt: Date.now(),
  }
}

export function readDesktopDBSessionSnapshot(queryClient: QueryClient, sessionId: string): DesktopDBSessionSnapshot | null {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    return null
  }
  const worksetSnapshot = desktopDBSessionSnapshotFromWorkset(readDesktopDBWorkset(queryClient), normalizedSessionId)
  if (worksetSnapshot) {
    return worksetSnapshot
  }
  return desktopDBSessionSnapshotFromDesktopDB(normalizedSessionId)
    ?? queryClient.getQueryData<DesktopDBSessionSnapshot>(desktopDBSessionSnapshotQueryKey(normalizedSessionId))
    ?? null
}

export function readDesktopDBSessionSnapshotOnly(queryClient: QueryClient, sessionId: string): DesktopDBSessionSnapshot | null {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    return null
  }
  return desktopDBSessionSnapshotFromDesktopDB(normalizedSessionId)
    ?? queryClient.getQueryData<DesktopDBSessionSnapshot>(desktopDBSessionSnapshotQueryKey(normalizedSessionId))
    ?? null
}

export function readDesktopDBHydratedSession(queryClient: QueryClient, sessionId: string): DesktopSessionRecord | null {
  return readDesktopDBWorksetSession(queryClient, sessionId) ?? readDesktopDBSessionSnapshot(queryClient, sessionId)?.session ?? null
}
