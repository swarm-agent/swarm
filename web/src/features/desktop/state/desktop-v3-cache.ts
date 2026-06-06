import type { QueryClient } from '@tanstack/react-query'
import { requestJson } from '../../../app/api'
import { applyDesktopChatRouteToSession, desktopChatRouteFromSessionMetadata } from '../chat/services/chat-routing'
import { mapDesktopSession } from '../chat/queries/chat-queries'
import { parseStructuredToolMessage } from '../chat/services/tool-message'
import type { ChatMessageRecord } from '../chat/types/chat'
import type { DesktopSessionRecord } from '../types/realtime'

interface V3SessionWire {
  id?: string
  session_api?: string
  last_event_seq?: number
  projection_high_watermark_seq?: number
  [key: string]: unknown
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
}

export interface DesktopV3SessionSnapshot {
  source: 'v3'
  session: DesktopSessionRecord
  messages: ChatMessageRecord[]
  events: unknown[]
  projection: V3SessionProjectionWire | null
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

export function mapDesktopV3SessionSnapshot(response: V3HydratedSessionResponseWire): DesktopV3SessionSnapshot | null {
  const mappedBaseSession = mapDesktopSession(mapProjectionToSession(response.session ?? {}, response.projection))
  if (!mappedBaseSession.id) {
    return null
  }
  const session = applyProjectionCursor(
    applyDesktopChatRouteToSession(mappedBaseSession, desktopChatRouteFromSessionMetadata(mappedBaseSession)),
    response.projection,
  )
  return {
    source: 'v3',
    session: {
      ...session,
      permissionsHydrated: false,
    },
    messages: Array.isArray(response.messages) ? response.messages.map(mapChatMessage) : [],
    events: Array.isArray(response.events) ? response.events : [],
    projection: response.projection ?? null,
    hydratedAt: Date.now(),
  }
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
