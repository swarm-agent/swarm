import type { DesktopV3CacheState, LiveRunOverlay, MessageSnapshot, PendingUserMessage } from './desktop-v3-cache-types'

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
