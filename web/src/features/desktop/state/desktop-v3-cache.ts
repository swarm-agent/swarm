import type { QueryClient } from '@tanstack/react-query'
import { requestJson } from '../../../app/api'
import { applyDesktopChatRouteToSession, desktopChatRouteFromSessionMetadata } from '../chat/services/chat-routing'
import { mapDesktopSession, mapDesktopSessionPermission, mapDesktopSessionPlan, mapDesktopSessionPlanRevision, mapDesktopSessionUsageSummary } from '../chat/queries/chat-queries'
import { parseStructuredToolMessage } from '../chat/services/tool-message'
import { countApprovalRequiredPermissions } from '../permissions/services/permission-payload'
import type { AgentModelPolicyRecord, ChatMessageRecord, DesktopSessionPlanRecord, DesktopSessionPlanRevisionRecord, ResolvedSessionPreference } from '../chat/types/chat'
import type { DesktopRunIntentRecord, DesktopSessionRecord } from '../types/realtime'
import {
  desktopV3SessionQueryKey,
  desktopV3SessionSnapshotQueryKey,
  mergeDesktopV3DurableCachePatch,
  mergeDesktopV3Messages,
  type DesktopV3ProjectionCursor,
  type DesktopV3SessionSnapshot,
} from './desktop-v3-durable-reducer'
export {
  desktopV3SessionQueryKey,
  desktopV3SessionSnapshotQueryKey,
  type DesktopV3SessionSnapshot,
} from './desktop-v3-durable-reducer'

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

export interface DesktopV3WorksetRequest {
  sessionIds?: string[]
  workspacePath?: string
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
  }
}

interface V3WorksetRequestWire {
  session_ids?: string[]
  workspace?: { workspace_path?: string }
  recent?: { limit?: number; before_updated_at?: number | null; before_session_id?: string }
  history?: { mode?: string; max_messages_per_session?: number; max_events_per_session?: number; manifest_policy?: string }
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
  messages?: V3MessageWire[]
  events?: unknown[]
}

export interface DesktopV3WorksetOmission {
  session_id?: string
  resource?: string
  reason?: string
  next_cursor?: string
  manifest_ref?: string
}

export interface DesktopV3WorksetPagination {
  next_before_updated_at?: number | null
  next_before_session_id?: string
  has_more?: boolean
}

export interface DesktopV3WorksetWatermarks {
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
  omissions?: DesktopV3WorksetOmission[]
  pagination?: DesktopV3WorksetPagination
  watermarks?: DesktopV3WorksetWatermarks
  session_order?: string[]
}

export interface DesktopV3Workset {
  source: 'v3-workset'
  sessionsById: Record<string, DesktopSessionRecord>
  projectionsBySession: Record<string, DesktopV3ProjectionCursor>
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
  historyChunksById: Record<string, DesktopV3HistoryChunk>
  omissions: DesktopV3WorksetOmission[]
  pagination: DesktopV3WorksetPagination
  watermarks: DesktopV3WorksetWatermarks
  sessionOrder: string[]
  loadedAt: number
}

const DEFAULT_WORKSET_HISTORY = {
  mode: 'full' as const,
  maxMessagesPerSession: 200,
  maxEventsPerSession: 500,
  manifestPolicy: 'manifest' as const,
}

export function desktopV3WorksetCacheQueryKey() {
  return ['desktop-v3-workset-cache'] as const
}

export function desktopV3WorksetQueryKey(scope: string) {
  return ['desktop-v3-workset', scope.trim()] as const
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

export function mapDesktopV3SessionSnapshot(response: V3HydratedSessionResponseWire): DesktopV3SessionSnapshot | null {
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

export function mapDesktopV3Workset(response: V3WorksetResponseWire): DesktopV3Workset {
  const sessionOrder = sessionOrderFromWorkset(response)
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
    const snapshot = mapDesktopV3SessionSnapshot(worksetSessionResponse(response, sessionId))
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
    historyChunksById: response.history_chunks_by_id ?? {},
    omissions: response.omissions ?? [],
    pagination: response.pagination ?? { has_more: false },
    watermarks: response.watermarks ?? {},
    sessionOrder: sessionOrder.filter((sessionId) => Boolean(sessionsById[sessionId])),
    loadedAt: Date.now(),
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
    omissions: [...current.omissions, ...incoming.omissions],
    sessionOrder: mergeSessionOrder(current.sessionOrder, incoming.sessionOrder),
    loadedAt: incoming.loadedAt,
  }
}

export function desktopV3SessionSnapshotFromWorkset(workset: DesktopV3Workset | null | undefined, sessionId: string): DesktopV3SessionSnapshot | null {
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

export function writeDesktopV3Workset(queryClient: QueryClient, workset: DesktopV3Workset): DesktopV3Workset {
  const cacheKey = desktopV3WorksetCacheQueryKey()
  const current = queryClient.getQueryData<DesktopV3Workset>(cacheKey) ?? null
  const merged = mergeDesktopV3Worksets(current, workset)
  queryClient.setQueryData(cacheKey, merged)
  for (const sessionId of workset.sessionOrder) {
    const snapshot = desktopV3SessionSnapshotFromWorkset(merged, sessionId)
    if (snapshot) {
      mergeDesktopV3DurableCachePatch(queryClient, { sessionId, snapshot })
    }
  }
  return merged
}

export function getCachedDesktopV3Workset(queryClient: QueryClient): DesktopV3Workset | null {
  return queryClient.getQueryData<DesktopV3Workset>(desktopV3WorksetCacheQueryKey()) ?? null
}

export function getCachedDesktopV3WorksetSession(queryClient: QueryClient, sessionId: string): DesktopSessionRecord | null {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    return null
  }
  return getCachedDesktopV3Workset(queryClient)?.sessionsById[normalizedSessionId] ?? null
}

export function desktopV3WorksetHasOmission(queryClient: QueryClient, sessionId: string, resource?: string): boolean {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    return false
  }
  const workset = getCachedDesktopV3Workset(queryClient)
  return Boolean(workset?.omissions.some((omission) => {
    if (String(omission.session_id ?? '').trim() !== normalizedSessionId) {
      return false
    }
    return !resource || String(omission.resource ?? '').trim() === resource
  }))
}

function toWorksetRequestWire(input: DesktopV3WorksetRequest): V3WorksetRequestWire {
  const sessionIds = (input.sessionIds ?? []).map((sessionId) => sessionId.trim()).filter(Boolean)
  const workspacePath = input.workspacePath?.trim() ?? ''
  const recent = input.recent
  const history = { ...DEFAULT_WORKSET_HISTORY, ...(input.history ?? {}) }
  return {
    session_ids: sessionIds.length > 0 ? sessionIds : undefined,
    workspace: workspacePath ? { workspace_path: workspacePath } : undefined,
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
    },
  }
}

function assertWorksetSelector(input: DesktopV3WorksetRequest): void {
  const hasSessionIds = (input.sessionIds ?? []).some((sessionId) => sessionId.trim() !== '')
  const hasWorkspace = Boolean(input.workspacePath?.trim())
  const hasRecent = typeof input.recent?.limit === 'number' && input.recent.limit > 0
  if (!hasSessionIds && !hasWorkspace && !hasRecent) {
    throw new Error('Desktop V3 workset request requires session ids, workspace path, or recent limit.')
  }
}

export async function fetchDesktopV3Workset(input: DesktopV3WorksetRequest, signal?: AbortSignal): Promise<DesktopV3Workset> {
  assertWorksetSelector(input)
  const response = await requestJson<V3WorksetResponseWire>(
    '/v3/sessions:workset',
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(toWorksetRequestWire(input)),
      signal,
    },
  )
  return mapDesktopV3Workset(response)
}

export async function hydrateDesktopV3Workset(
  queryClient: QueryClient,
  input: DesktopV3WorksetRequest,
  options: { signal?: AbortSignal } = {},
): Promise<DesktopV3Workset> {
  const workset = await fetchDesktopV3Workset(input, options.signal)
  return writeDesktopV3Workset(queryClient, workset)
}

function requireDesktopV3SessionSnapshot(response: V3HydratedSessionResponseWire, action: string): DesktopV3SessionSnapshot {
  const snapshot = mapDesktopV3SessionSnapshot(response)
  if (!snapshot) {
    throw new Error(`Desktop V3 ${action} requires a hydrated canonical session snapshot.`)
  }
  return snapshot
}

export async function hydrateDesktopV3WorksetSession(
  queryClient: QueryClient,
  sessionId: string,
  options: { signal?: AbortSignal } = {},
): Promise<DesktopV3SessionSnapshot | null> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const workset = await hydrateDesktopV3Workset(queryClient, { sessionIds: [normalizedSessionId] }, options)
  return desktopV3SessionSnapshotFromWorkset(workset, normalizedSessionId)
}

export async function hydrateDesktopV3SessionSnapshot(
  queryClient: QueryClient,
  sessionId: string,
  options: { signal?: AbortSignal } = {},
): Promise<DesktopV3SessionSnapshot | null> {
  return hydrateDesktopV3WorksetSession(queryClient, sessionId, options)
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

export async function fetchDesktopV3SessionSnapshot(sessionId: string, signal?: AbortSignal): Promise<DesktopV3SessionSnapshot | null> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const preloadedResponse = await readPreloadedDesktopV3SessionResponse(normalizedSessionId)
  if (preloadedResponse) {
    return mapDesktopV3SessionSnapshot(preloadedResponse)
  }
  const workset = await fetchDesktopV3Workset({ sessionIds: [normalizedSessionId] }, signal)
  return desktopV3SessionSnapshotFromWorkset(workset, normalizedSessionId)
}

export async function updateDesktopV3SessionMode(
  queryClient: QueryClient,
  sessionId: string,
  mode: string,
): Promise<DesktopV3SessionSnapshot> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const response = await requestJson<V3HydratedSessionResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/mode`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode }),
    },
  )
  const snapshot = requireDesktopV3SessionSnapshot(response, 'mode update')
  writeDesktopV3SessionSnapshot(queryClient, snapshot)
  return snapshot
}

export async function updateDesktopV3SessionPreference(
  queryClient: QueryClient,
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
  const snapshot = requireDesktopV3SessionSnapshot(response, 'preference update')
  writeDesktopV3SessionSnapshot(queryClient, snapshot)
  return snapshot.preference
}

export async function updateDesktopV3SessionAgent(
  queryClient: QueryClient,
  sessionId: string,
  agentName: string,
): Promise<DesktopV3SessionSnapshot> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const response = await requestJson<V3HydratedSessionResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/agent`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ agent_name: agentName.trim() }),
    },
  )
  const snapshot = requireDesktopV3SessionSnapshot(response, 'agent update')
  writeDesktopV3SessionSnapshot(queryClient, snapshot)
  return snapshot
}

export async function updateDesktopV3SessionMetadata(
  queryClient: QueryClient,
  sessionId: string,
  metadata: Record<string, unknown>,
): Promise<DesktopV3SessionSnapshot> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const response = await requestJson<V3HydratedSessionResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}/metadata`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ metadata }),
    },
  )
  const snapshot = requireDesktopV3SessionSnapshot(response, 'metadata update')
  writeDesktopV3SessionSnapshot(queryClient, snapshot)
  return snapshot
}

export async function saveDesktopV3SessionPlan(
  queryClient: QueryClient,
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
): Promise<DesktopV3SessionSnapshot> {
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
  const snapshot = requireDesktopV3SessionSnapshot(response, 'plan save')
  writeDesktopV3SessionSnapshot(queryClient, snapshot)
  return snapshot
}

export function desktopV3SessionQueryOptions(sessionId: string) {
  const normalizedSessionId = sessionId.trim()
  return {
    queryKey: desktopV3SessionQueryKey(normalizedSessionId),
    queryFn: ({ signal }: { signal?: AbortSignal }) => fetchDesktopV3SessionSnapshot(normalizedSessionId, signal),
    staleTime: Number.POSITIVE_INFINITY,
    enabled: normalizedSessionId !== '',
  }
}

export function writeDesktopV3SessionSnapshot(
  queryClient: QueryClient,
  snapshot: DesktopV3SessionSnapshot,
): void {
  const sessionId = snapshot.session.id.trim()
  if (!sessionId) {
    return
  }

  mergeDesktopV3DurableCachePatch(queryClient, { sessionId, snapshot })
}

export async function ensureDesktopV3SessionSnapshot(
  queryClient: QueryClient,
  sessionId: string,
): Promise<DesktopV3SessionSnapshot | null> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
  const cached = getCachedDesktopV3SessionSnapshot(queryClient, normalizedSessionId)
  if (cached) {
    writeDesktopV3SessionSnapshot(queryClient, cached)
    return cached
  }

  return hydrateDesktopV3WorksetSession(queryClient, normalizedSessionId)
}

export function getCachedDesktopV3SessionSnapshot(queryClient: QueryClient, sessionId: string): DesktopV3SessionSnapshot | null {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    return null
  }
  const worksetSnapshot = desktopV3SessionSnapshotFromWorkset(getCachedDesktopV3Workset(queryClient), normalizedSessionId)
  if (worksetSnapshot) {
    return worksetSnapshot
  }
  return queryClient.getQueryData<DesktopV3SessionSnapshot>(desktopV3SessionSnapshotQueryKey(normalizedSessionId)) ?? null
}

export function getCachedDesktopV3SessionSnapshotOnly(queryClient: QueryClient, sessionId: string): DesktopV3SessionSnapshot | null {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    return null
  }
  return queryClient.getQueryData<DesktopV3SessionSnapshot>(desktopV3SessionSnapshotQueryKey(normalizedSessionId)) ?? null
}

export function readDesktopV3CachedSession(queryClient: QueryClient, sessionId: string): DesktopSessionRecord | null {
  return getCachedDesktopV3WorksetSession(queryClient, sessionId) ?? getCachedDesktopV3SessionSnapshot(queryClient, sessionId)?.session ?? null
}
