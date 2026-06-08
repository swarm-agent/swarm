import type { QueryClient } from '@tanstack/react-query'
import { requestJson } from '../../../app/api'
import { applyDesktopChatRouteToSession, desktopChatRouteFromSessionMetadata } from '../chat/services/chat-routing'
import { mapDesktopSession, mapDesktopSessionPermission, mapDesktopSessionPlan, mapDesktopSessionPlanRevision, mapDesktopSessionUsageSummary } from '../chat/queries/chat-queries'
import { parseStructuredToolMessage } from '../chat/services/tool-message'
import { countApprovalRequiredPermissions } from '../permissions/services/permission-payload'
import type { AgentModelPolicyRecord, ChatMessageRecord, ResolvedSessionPreference } from '../chat/types/chat'
import type { DesktopRunIntentRecord, DesktopSessionRecord } from '../types/realtime'
import {
  desktopV3SessionQueryKey,
  desktopV3SessionSnapshotQueryKey,
  mergeDesktopV3DurableCachePatch,
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
    messages: Array.isArray(response.messages) ? response.messages.map(mapChatMessage) : [],
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

function requireDesktopV3SessionSnapshot(response: V3HydratedSessionResponseWire, action: string): DesktopV3SessionSnapshot {
  const snapshot = mapDesktopV3SessionSnapshot(response)
  if (!snapshot) {
    throw new Error(`Desktop V3 ${action} requires a hydrated canonical session snapshot.`)
  }
  return snapshot
}

export async function hydrateDesktopV3SessionSnapshot(
  queryClient: QueryClient,
  sessionId: string,
  options: { signal?: AbortSignal } = {},
): Promise<DesktopV3SessionSnapshot | null> {
  const snapshot = await fetchDesktopV3SessionSnapshot(sessionId, options.signal)
  if (snapshot) {
    writeDesktopV3SessionSnapshot(queryClient, snapshot)
  }
  return snapshot
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
  const response = await requestJson<V3HydratedSessionResponseWire>(
    `/v3/sessions/${encodeURIComponent(normalizedSessionId)}`,
    { signal },
  )
  return mapDesktopV3SessionSnapshot(response)
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
    staleTime: 60_000,
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
  const cached = queryClient.getQueryData<DesktopV3SessionSnapshot>(desktopV3SessionSnapshotQueryKey(normalizedSessionId))
  if (cached) {
    writeDesktopV3SessionSnapshot(queryClient, cached)
    return cached
  }

  const fetched = await queryClient.fetchQuery(desktopV3SessionQueryOptions(normalizedSessionId))
  if (fetched) {
    writeDesktopV3SessionSnapshot(queryClient, fetched)
  }
  return fetched
}

export function getCachedDesktopV3SessionSnapshot(queryClient: QueryClient, sessionId: string): DesktopV3SessionSnapshot | null {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    return null
  }
  return queryClient.getQueryData<DesktopV3SessionSnapshot>(desktopV3SessionSnapshotQueryKey(normalizedSessionId)) ?? null
}

export function readDesktopV3CachedSession(queryClient: QueryClient, sessionId: string): DesktopSessionRecord | null {
  return getCachedDesktopV3SessionSnapshot(queryClient, sessionId)?.session ?? null
}
