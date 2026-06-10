import type { QueryClient } from '@tanstack/react-query'
import type { AgentModelPolicyRecord, ChatMessageRecord, DesktopSessionPlanRecord, DesktopSessionPlanRevisionRecord, ResolvedSessionPreference } from '../chat/types/chat'
import { mergeMessageIntoCache } from '../chat/services/message-cache'
import type { DesktopSessionRecord } from '../types/realtime'
import { mergeSessionRecords } from './session-records'

export interface DesktopV3ProjectionCursor {
  session_id?: string
  last_event_seq?: number
  projection_high_watermark_seq?: number
  updated_at?: number
}

export interface DesktopDBSessionSnapshot {
  source: 'v3'
  session: DesktopSessionRecord
  messages: ChatMessageRecord[]
  events: unknown[]
  projection: DesktopV3ProjectionCursor | null
  preference: ResolvedSessionPreference
  agentModelPolicy: AgentModelPolicyRecord | null
  hasActivePlan: boolean
  activePlan: DesktopSessionPlanRecord | null
  planRevisions: DesktopSessionPlanRevisionRecord[]
  appliedSeq: number
  highWatermark: number
  hydratedAt: number
}

export interface DesktopDBSnapshotPatch {
  sessionId?: string
  snapshot?: DesktopDBSessionSnapshot | null
  session?: DesktopSessionRecord | null
  messages?: ChatMessageRecord[]
  events?: unknown[]
  projection?: DesktopV3ProjectionCursor | null
  preference?: ResolvedSessionPreference
  agentModelPolicy?: AgentModelPolicyRecord | null
  hasActivePlan?: boolean
  activePlan?: DesktopSessionPlanRecord | null
  planRevisions?: DesktopSessionPlanRevisionRecord[]
  appliedSeq?: number
  highWatermark?: number
  hydratedAt?: number
}

const EMPTY_PREFERENCE: ResolvedSessionPreference = {
  preference: { provider: '', model: '', thinking: '', serviceTier: '', contextMode: '', updatedAt: 0 },
  contextWindow: 0,
  maxOutputTokens: 0,
}

export function desktopDBSessionSnapshotQueryKey(sessionId: string) {
  return ['desktop-v3-session-snapshot', sessionId.trim()] as const
}

export const desktopDBSessionQueryKey = desktopDBSessionSnapshotQueryKey

export function desktopV3SessionMessagesQueryKey(sessionId: string) {
  return ['session-messages', sessionId.trim()] as const
}

export function desktopV3SessionPreferenceQueryKey(sessionId: string) {
  return ['session-preference', sessionId.trim()] as const
}

export function mergeDesktopV3Messages(current: ChatMessageRecord[] | undefined, incoming: ChatMessageRecord[] | undefined): ChatMessageRecord[] {
  return (incoming ?? []).reduce<ChatMessageRecord[]>((merged, message) => mergeMessageIntoCache(merged, message), current ?? [])
}

function resolvePatchSessionId(patch: DesktopDBSnapshotPatch): string {
  return (patch.sessionId
    ?? patch.snapshot?.session.id
    ?? patch.session?.id
    ?? patch.messages?.find((message) => message.sessionId.trim() !== '')?.sessionId
    ?? '').trim()
}

function projectionAppliedSeq(projection: DesktopV3ProjectionCursor | null | undefined): number {
  return typeof projection?.last_event_seq === 'number' ? Math.max(0, projection.last_event_seq) : 0
}

function projectionHighWatermark(projection: DesktopV3ProjectionCursor | null | undefined): number {
  return typeof projection?.projection_high_watermark_seq === 'number'
    ? Math.max(0, projection.projection_high_watermark_seq)
    : projectionAppliedSeq(projection)
}

function mergeEvents(current: unknown[] | undefined, incoming: unknown[] | undefined): unknown[] {
  const events = current ?? []
  if (!incoming || incoming.length === 0) {
    return events
  }
  const seen = new Set<string>()
  const merged: unknown[] = []
  for (const event of [...events, ...incoming]) {
    const record = event && typeof event === 'object' ? event as Record<string, unknown> : null
    const key = record
      ? `${String(record.event_type ?? '')}:${String(record.global_seq ?? record.source_seq ?? record.seq ?? '')}:${String(record.id ?? record.entity_id ?? '')}`
      : JSON.stringify(event)
    if (seen.has(key)) {
      continue
    }
    seen.add(key)
    merged.push(event)
  }
  return merged
}

function emptySnapshot(session: DesktopSessionRecord): DesktopDBSessionSnapshot {
  return {
    source: 'v3',
    session: { ...session, sessionApi: session.sessionApi || 'v3' },
    messages: [],
    events: [],
    projection: null,
    preference: EMPTY_PREFERENCE,
    agentModelPolicy: null,
    hasActivePlan: false,
    activePlan: null,
    planRevisions: [],
    appliedSeq: Math.max(0, session.lastEventSeq ?? 0),
    highWatermark: Math.max(0, session.projectionHighWatermarkSeq ?? session.lastEventSeq ?? 0),
    hydratedAt: Date.now(),
  }
}

export function reduceDesktopV3DurableCache(
  current: DesktopDBSessionSnapshot | null | undefined,
  patch: DesktopDBSnapshotPatch,
  currentMessages?: ChatMessageRecord[],
): DesktopDBSessionSnapshot | null {
  const incoming = patch.snapshot ?? null
  const session = patch.session ?? incoming?.session ?? null
  const base = incoming ?? current ?? (session ? emptySnapshot(session) : null)
  if (!base) {
    return null
  }

  const incomingSession = patch.session ?? incoming?.session ?? base.session
  const mergedSession = mergeSessionRecords(current?.session ?? null, incomingSession)
  const projection = patch.projection !== undefined ? patch.projection : incoming?.projection ?? current?.projection ?? base.projection
  const appliedSeq = Math.max(
    current?.appliedSeq ?? 0,
    incoming?.appliedSeq ?? 0,
    typeof patch.appliedSeq === 'number' ? Math.max(0, patch.appliedSeq) : 0,
    projectionAppliedSeq(projection),
    mergedSession.lastEventSeq ?? 0,
  )
  const highWatermark = Math.max(
    current?.highWatermark ?? 0,
    incoming?.highWatermark ?? 0,
    typeof patch.highWatermark === 'number' ? Math.max(0, patch.highWatermark) : 0,
    projectionHighWatermark(projection),
    mergedSession.projectionHighWatermarkSeq ?? 0,
    appliedSeq,
  )
  const seedMessages = mergeDesktopV3Messages(current?.messages ?? [], currentMessages)
  const withSnapshotMessages = mergeDesktopV3Messages(seedMessages, incoming?.messages)
  const messages = mergeDesktopV3Messages(withSnapshotMessages, patch.messages)

  return {
    ...base,
    session: {
      ...mergedSession,
      sessionApi: mergedSession.sessionApi || 'v3',
      lastEventSeq: Math.max(mergedSession.lastEventSeq ?? 0, appliedSeq),
      projectionHighWatermarkSeq: Math.max(mergedSession.projectionHighWatermarkSeq ?? 0, highWatermark),
    },
    messages,
    events: mergeEvents(current?.events ?? incoming?.events ?? base.events, patch.events),
    projection,
    preference: patch.preference ?? incoming?.preference ?? current?.preference ?? base.preference,
    agentModelPolicy: patch.agentModelPolicy !== undefined ? patch.agentModelPolicy : incoming?.agentModelPolicy ?? current?.agentModelPolicy ?? base.agentModelPolicy,
    hasActivePlan: patch.hasActivePlan ?? incoming?.hasActivePlan ?? current?.hasActivePlan ?? base.hasActivePlan,
    activePlan: patch.activePlan !== undefined ? patch.activePlan : incoming?.activePlan ?? current?.activePlan ?? base.activePlan,
    planRevisions: patch.planRevisions ?? incoming?.planRevisions ?? current?.planRevisions ?? base.planRevisions,
    appliedSeq,
    highWatermark,
    hydratedAt: patch.hydratedAt ?? incoming?.hydratedAt ?? Date.now(),
  }
}

export function mergeDesktopDBSnapshotPatch(queryClient: QueryClient, patch: DesktopDBSnapshotPatch): DesktopDBSessionSnapshot | null {
  const sessionId = resolvePatchSessionId(patch)
  if (!sessionId) {
    return null
  }
  const snapshotKey = desktopDBSessionSnapshotQueryKey(sessionId)
  const messagesKey = desktopV3SessionMessagesQueryKey(sessionId)
  const currentSnapshot = queryClient.getQueryData<DesktopDBSessionSnapshot>(snapshotKey) ?? null
  const currentMessages = queryClient.getQueryData<ChatMessageRecord[]>(messagesKey) ?? undefined
  const next = reduceDesktopV3DurableCache(currentSnapshot, { ...patch, sessionId }, currentMessages)
  if (!next) {
    return null
  }
  queryClient.setQueryData(snapshotKey, next)
  queryClient.setQueryData(messagesKey, next.messages)
  queryClient.setQueryData(desktopV3SessionPreferenceQueryKey(sessionId), next.preference)
  return next
}
