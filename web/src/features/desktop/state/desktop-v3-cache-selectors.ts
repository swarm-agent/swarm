import type { DesktopPermissionRecord } from '../types/realtime'
import { safeString } from '../permissions/services/desktop-permission-normalization'
import type { DesktopV3CacheState, LiveRunOverlay, MessageListCache, MessageSnapshot, PendingUserMessage, SessionCacheRecord, V3SessionProjection, V3SessionRunIntent, V3SessionTombstone } from './desktop-v3-cache-types'

export interface DesktopV3SidebarRow {
  sessionId: string
  record: SessionCacheRecord
  projection?: V3SessionProjection
  tombstone?: V3SessionTombstone
  runIntents: Record<string, V3SessionRunIntent>
  currentRunIntent?: V3SessionRunIntent
  pendingPermissions: DesktopPermissionRecord[]
  pendingPermissionCount: number
}

export interface RenderedSessionMessages {
  committed: MessageSnapshot[]
  pendingUser: PendingUserMessage[]
  liveRuns: LiveRunOverlay[]
  runIntents: V3SessionRunIntent[]
  currentRunIntent?: V3SessionRunIntent
  latestRunIntent?: V3SessionRunIntent
}

export interface DesktopV3HydratedTranscriptDiagnostics {
  hydratedSessionCount: number
  hydratedMessageCount: number
  retainedBackgroundHydratedSessionCount: number
  inFlightHydrateSessionCount: number
  evictedTranscriptCount: number
}

export function selectScopeEndpointCursor(state: DesktopV3CacheState, scopeId: string): string | undefined {
  return state.syncScopesById[scopeId]?.endpointCursor
}

export function selectSessionOrder(state: DesktopV3CacheState, scopeId: string): string[] {
  return state.sessionOrderByScope[scopeId] ?? []
}

export function selectDesktopSidebarScopeId(state: DesktopV3CacheState): string | undefined {
  return state.desktopSidebarBootstrap.scopeId
}

export function selectDesktopSidebarRows(state: DesktopV3CacheState, scopeId = state.desktopSidebarBootstrap.scopeId): DesktopV3SidebarRow[] {
  const resolvedScopeId = scopeId ?? Object.keys(state.sessionOrderByScope)[0]
  if (!resolvedScopeId) return []
  const rows: DesktopV3SidebarRow[] = []
  for (const sessionId of selectSessionOrder(state, resolvedScopeId)) {
    const record = state.sessionsById[sessionId]
    if (!record) continue
    rows.push({
      sessionId,
      record: cloneSessionCacheRecord(record),
      projection: state.projectionsBySession[sessionId] ? { ...state.projectionsBySession[sessionId] } : undefined,
      tombstone: state.tombstonesBySession[sessionId] ? { ...state.tombstonesBySession[sessionId] } : undefined,
      runIntents: cloneRunIntentRecord(state.runIntentsBySession[sessionId]),
      currentRunIntent: cloneRunIntent(state.currentRunIntentBySession[sessionId]),
      pendingPermissions: clonePendingPermissions(state.permissionsBySession[sessionId]),
      pendingPermissionCount: countPendingPermissions(state.permissionsBySession[sessionId]),
    })
  }
  return rows
}

export function selectCommittedMessages(state: DesktopV3CacheState, sessionId: string): MessageSnapshot[] {
  return state.messagesBySession[sessionId]?.items ?? []
}

export function selectPendingUserMessages(state: DesktopV3CacheState, sessionId: string): PendingUserMessage[] {
  return Object.values(state.pendingUserByClientRequestId)
    .filter((pending) => pending.sessionId === sessionId)
    .map((pending) => ({ ...pending, metadata: pending.metadata ? { ...pending.metadata } : undefined }))
    .sort((left, right) => left.createdAt - right.createdAt || left.messageId.localeCompare(right.messageId))
}

export function selectLiveRuns(state: DesktopV3CacheState, sessionId: string): LiveRunOverlay[] {
  return Object.values(state.liveRunsBySession[sessionId] ?? {}).map(cloneLiveRun).sort((a, b) => {
    const aSeq = a.lastEventSeqSeen ?? 0
    const bSeq = b.lastEventSeqSeen ?? 0

    if (aSeq !== bSeq) {
      return aSeq - bSeq
    }

    const aUpdated = a.assistantDraft?.updatedAt ?? 0
    const bUpdated = b.assistantDraft?.updatedAt ?? 0

    if (aUpdated !== bUpdated) {
      return aUpdated - bUpdated
    }

    return a.runId.localeCompare(b.runId)
  })
}

export function selectSessionRunIntents(state: DesktopV3CacheState, sessionId: string): V3SessionRunIntent[] {
  return Object.values(state.runIntentsBySession[sessionId] ?? {}).sort((left, right) => {
    const leftSeq = typeof left.event_seq === 'number' ? left.event_seq : 0
    const rightSeq = typeof right.event_seq === 'number' ? right.event_seq : 0
    if (leftSeq !== rightSeq) return leftSeq - rightSeq

    const leftUpdated = typeof left.updated_at === 'number' ? left.updated_at : 0
    const rightUpdated = typeof right.updated_at === 'number' ? right.updated_at : 0
    if (leftUpdated !== rightUpdated) return leftUpdated - rightUpdated

    return left.run_id.localeCompare(right.run_id)
  })
}

export function selectDesktopV3HydratedTranscriptDiagnostics(state: DesktopV3CacheState): DesktopV3HydratedTranscriptDiagnostics {
  const selectedSessionId = state.selectedSessionId?.trim()
  let hydratedSessionCount = 0
  let hydratedMessageCount = 0
  let retainedBackgroundHydratedSessionCount = 0

  for (const [sessionId, list] of Object.entries(state.messagesBySession)) {
    if (!isHydratedTranscript(list)) continue
    hydratedSessionCount += 1
    hydratedMessageCount += list.items.length
    if (sessionId !== selectedSessionId && (state.hydrateInFlightBySession ?? {})[sessionId] === undefined) {
      retainedBackgroundHydratedSessionCount += 1
    }
  }

  return {
    hydratedSessionCount,
    hydratedMessageCount,
    retainedBackgroundHydratedSessionCount,
    inFlightHydrateSessionCount: Object.keys(state.hydrateInFlightBySession ?? {}).length,
    evictedTranscriptCount: Object.keys(state.evictedTranscriptsBySession ?? {}).length,
  }
}

export function selectRenderedSessionMessages(state: DesktopV3CacheState, sessionId: string): RenderedSessionMessages {
  const runIntents = selectSessionRunIntents(state, sessionId)
  return {
    committed: selectCommittedMessages(state, sessionId),
    pendingUser: selectPendingUserMessages(state, sessionId),
    liveRuns: selectLiveRuns(state, sessionId),
    runIntents,
    currentRunIntent: state.currentRunIntentBySession[sessionId],
    latestRunIntent: runIntents[runIntents.length - 1],
  }
}

export function selectSessionNeedsHydrate(state: DesktopV3CacheState, sessionId: string): boolean {
  return state.sessionsById[sessionId]?.needsHydrate ?? true
}

export function isDesktopV3SessionViewReady(state: DesktopV3CacheState, sessionId: string): boolean {
  const normalized = sessionId.trim()
  if (!normalized) return false
  return Boolean(state.sessionViewsById[normalized])
}

export function isDesktopV3SessionTailReady(
  state: DesktopV3CacheState,
  sessionId: string,
): boolean {
  const normalized = sessionId.trim()
  if (!normalized) return false
  if (state.tombstonesBySession[normalized]) return false

  const record = state.sessionsById[normalized]
  const messages = state.messagesBySession[normalized]
  const session = record?.kind === 'full' ? record.session : undefined

  if (!session || !messages) return false

  const hasAuthoritativeTail = messages.knownFull === true
    || Boolean(messages.knownTail)
    || Number.isSafeInteger(messages.tailHydratedAt)

  return hasAuthoritativeTail
    && Number.isSafeInteger(messages.sourceMessageCount)
    && Number.isSafeInteger(messages.sourceLastMessageAt)
    && (messages.sourceMessageCount ?? -1) >= session.message_count
    && (messages.sourceLastMessageAt ?? -1) >= session.last_message_at
}

function isHydratedTranscript(list: MessageListCache | undefined): boolean {
  return Boolean(list?.knownFull)
    || Boolean(list?.knownTail)
    || Number.isSafeInteger(list?.tailHydratedAt)
}

function cloneSessionCacheRecord(record: SessionCacheRecord): SessionCacheRecord {
  if (record.kind === 'stub') return { ...record }
  return {
    kind: 'full',
    session: {
      ...record.session,
      metadata: record.session.metadata ? { ...record.session.metadata } : undefined,
      temporary_workspace_roots: record.session.temporary_workspace_roots ? [...record.session.temporary_workspace_roots] : undefined,
    },
    needsHydrate: record.needsHydrate,
  }
}

function cloneRunIntent(runIntent: V3SessionRunIntent | undefined): V3SessionRunIntent | undefined {
  return runIntent ? { ...runIntent } : undefined
}

function cloneRunIntentRecord(runIntents: Record<string, V3SessionRunIntent> | undefined): Record<string, V3SessionRunIntent> {
  if (!runIntents) return {}
  const output: Record<string, V3SessionRunIntent> = {}
  for (const [runId, runIntent] of Object.entries(runIntents)) {
    output[runId] = { ...runIntent }
  }
  return output
}

function clonePendingPermissions(permissions: DesktopPermissionRecord[] | undefined): DesktopPermissionRecord[] {
  return (permissions ?? [])
    .filter((permission) => safeString(permission.status).toLowerCase() === 'pending')
    .map((permission) => ({ ...permission, savedRule: permission.savedRule ? { ...permission.savedRule } : undefined }))
}

function countPendingPermissions(permissions: DesktopPermissionRecord[] | undefined): number {
  let count = 0
  for (const permission of permissions ?? []) {
    if (safeString(permission.status).toLowerCase() === 'pending') count += 1
  }
  return count
}

function cloneLiveRun(run: LiveRunOverlay): LiveRunOverlay {
  return {
    ...run,
    assistantDraft: run.assistantDraft ? { ...run.assistantDraft } : undefined,
    assistantSegments: run.assistantSegments?.map((segment) => ({ ...segment })),
    toolCallsByCallId: Object.fromEntries(
      Object.entries(run.toolCallsByCallId).map(([callId, tool]) => [callId, { ...tool }]),
    ),
    reasoning: run.reasoning ? { ...run.reasoning } : undefined,
    reasoningByKey: run.reasoningByKey ? Object.fromEntries(
      Object.entries(run.reasoningByKey).map(([key, reasoning]) => [key, { ...reasoning }]),
    ) : undefined,
  }
}
