import type { DesktopV3CacheState, LiveRunOverlay, MessageSnapshot, PendingUserMessage, SessionCacheRecord, V3SessionProjection, V3SessionRunIntent, V3SessionTombstone } from './desktop-v3-cache-types'

export interface DesktopV3SidebarRow {
  sessionId: string
  record: SessionCacheRecord
  projection?: V3SessionProjection
  tombstone?: V3SessionTombstone
  runIntents: Record<string, V3SessionRunIntent>
  currentRunIntent?: V3SessionRunIntent
}

export interface RenderedSessionMessages {
  committed: MessageSnapshot[]
  pendingUser: PendingUserMessage[]
  liveRuns: LiveRunOverlay[]
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
      record,
      projection: state.projectionsBySession[sessionId],
      tombstone: state.tombstonesBySession[sessionId],
      runIntents: state.runIntentsBySession[sessionId] ?? {},
      currentRunIntent: state.currentRunIntentBySession[sessionId],
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
    .sort((left, right) => left.createdAt - right.createdAt || left.messageId.localeCompare(right.messageId))
}

export function selectLiveRuns(state: DesktopV3CacheState, sessionId: string): LiveRunOverlay[] {
  return Object.values(state.liveRunsBySession[sessionId] ?? {}).sort((a, b) => {
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

export function selectRenderedSessionMessages(state: DesktopV3CacheState, sessionId: string): RenderedSessionMessages {
  return {
    committed: selectCommittedMessages(state, sessionId),
    pendingUser: selectPendingUserMessages(state, sessionId),
    liveRuns: selectLiveRuns(state, sessionId),
  }
}

export function selectSessionNeedsHydrate(state: DesktopV3CacheState, sessionId: string): boolean {
  return state.sessionsById[sessionId]?.needsHydrate ?? true
}
