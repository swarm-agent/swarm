import type { QueryClient } from '@tanstack/react-query'
import { requestJson } from '../../../app/api'
import { applyDesktopChatRouteToSession, desktopChatRouteFromSessionMetadata } from '../chat/services/chat-routing'
import { mapDesktopSession, mapDesktopSessionPermission, mapDesktopSessionPlan, mapDesktopSessionPlanRevision, mapDesktopSessionUsageSummary } from '../chat/queries/chat-queries'
import { parseStructuredToolMessage } from '../chat/services/tool-message'
import { countApprovalRequiredPermissions } from '../permissions/services/permission-payload'
import type { ChatMessageRecord, DesktopSessionPlanRecord, DesktopSessionPlanRevisionRecord, ResolvedSessionPreference } from '../chat/types/chat'
import type { DesktopSessionRecord } from '../types/realtime'

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

interface V3SessionProjectionWire {
  session_id?: string
  last_event_seq?: number
  projection_high_watermark_seq?: number
  updated_at?: number
}

interface V3MessageWire {
  id?: string
  session_id?: string
  global_seq?: number
  role?: string
  content?: string
  created_at?: number
  metadata?: Record<string, unknown>
}

interface V3HydratedSessionResponseWire {
  session?: V3SessionWire
  projection?: V3SessionProjectionWire
  messages?: V3MessageWire[]
  events?: unknown[]
  pending_permissions?: unknown[]
  usage_summary?: unknown
  preference?: V3PreferenceWire
  context_window?: number
  max_output_tokens?: number
  has_active_plan?: boolean
  active_plan?: unknown
  plan_revisions?: unknown[]
}

export interface DesktopV3SessionSnapshot {
  source: 'v3'
  session: DesktopSessionRecord
  messages: ChatMessageRecord[]
  events: unknown[]
  projection: V3SessionProjectionWire | null
  preference: ResolvedSessionPreference
  hasActivePlan: boolean
  activePlan: DesktopSessionPlanRecord | null
  planRevisions: DesktopSessionPlanRevisionRecord[]
  hydratedAt: number
}

export function assertRawCanonicalDesktopV3SessionId(sessionId: string): string {
  const normalizedSessionId = sessionId.trim()
  if (!normalizedSessionId) {
    throw new Error('Desktop V3 requires a raw canonical session id.')
  }
  return normalizedSessionId
}

export function desktopV3SessionSnapshotQueryKey(sessionId: string) {
  return ['desktop-v3-session-snapshot', sessionId.trim()] as const
}

export const desktopV3SessionQueryKey = desktopV3SessionSnapshotQueryKey

function sessionMessagesQueryKey(sessionId: string) {
  return ['session-messages', sessionId.trim()] as const
}

function sessionPreferenceQueryKey(sessionId: string) {
  return ['session-preference', sessionId.trim()] as const
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

function mapDesktopV3SessionPreference(response: V3HydratedSessionResponseWire): ResolvedSessionPreference {
  const sessionSource = response.session?.preference
  const source: V3PreferenceWire = response.preference ?? sessionSource ?? {}
  return {
    preference: {
      provider: String(source.provider ?? '').trim(),
      model: String(source.model ?? '').trim(),
      thinking: String(source.thinking ?? '').trim(),
      serviceTier: String(source.service_tier ?? '').trim(),
      contextMode: String(source.context_mode ?? '').trim(),
      updatedAt: typeof source.updated_at === 'number' ? source.updated_at : 0,
    },
    contextWindow: typeof response.context_window === 'number' ? response.context_window : 0,
    maxOutputTokens: typeof response.max_output_tokens === 'number' ? response.max_output_tokens : 0,
  }
}

export function mapDesktopV3SessionSnapshot(response: V3HydratedSessionResponseWire): DesktopV3SessionSnapshot | null {
  const mappedBaseSession = mapDesktopSession(mapProjectionToSession(response.session ?? {}, response.projection))
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

export async function fetchDesktopV3SessionSnapshot(sessionId: string, signal?: AbortSignal): Promise<DesktopV3SessionSnapshot | null> {
  const normalizedSessionId = assertRawCanonicalDesktopV3SessionId(sessionId)
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

  queryClient.setQueryData(desktopV3SessionSnapshotQueryKey(sessionId), snapshot)
  queryClient.setQueryData(sessionMessagesQueryKey(sessionId), snapshot.messages)
  queryClient.setQueryData(sessionPreferenceQueryKey(sessionId), snapshot.preference)
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
