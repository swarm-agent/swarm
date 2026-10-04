import type { DesktopV3CacheState, SessionSnapshot } from '../state/desktop-v3-cache-types'
import type { ProjectSessionRow } from '../state/project-session-rows'
import { conversationProjectId } from './project-conversations'

// Reuse only canonical, visible project ownership; unknown/deep links still need
// server admission. Tombstones must never be bypassed by a retained full record.
export function cachedProjectConversation(state: DesktopV3CacheState, projectId: string, sessionId: string): SessionSnapshot | undefined {
  const record = state.sessionsById[sessionId]
  return projectId && record?.kind === 'full' && record.session.id === sessionId
    && !state.tombstonesBySession[sessionId] && !record.session.navigation_hidden
    && conversationProjectId(record.session) === projectId ? record.session : undefined
}

// Match the default (unarchived) sidebar order, not creation order or a legacy
// primary-session pointer. A partial/failed list must not choose the wrong row.
export function firstProjectConversation(rows: ProjectSessionRow[], ready: boolean, loading: boolean, error: string): string {
  return ready && !loading && !error ? rows.find(row => !row.archivedVersion)?.session.id || '' : ''
}

// sessionsV3SyncHydrateOptions caps resources.session_view at eight IDs.
export function projectConversationBatches(ids: string[]): string[][] {
  const unique = [...new Set(ids.map(id => id.trim()).filter(Boolean))]
  return Array.from({ length: Math.ceil(unique.length / 8) }, (_, index) => unique.slice(index * 8, index * 8 + 8))
}

export function admittedConversationId(admission: { projectId: string; sessionId: string; accountScopeId?: string } | null, projectId: string, sessionId: string, accountScopeId?: string): string {
  return admission?.projectId === projectId && admission.sessionId === sessionId
    && admission.accountScopeId === accountScopeId ? sessionId : ''
}

export function legacyHistorySessions(sessions: SessionSnapshot[]): SessionSnapshot[] {
  return sessions.filter(session => session.workspace_path && !session.navigation_hidden && !session.system_session && !session.system_sidechat)
    .sort((a, b) => b.updated_at - a.updated_at || a.id.localeCompare(b.id)).slice(0, 50)
}

export async function completeAccountOnboarding<T extends { needsOnboarding: boolean }>(steps: {
  finalize: () => Promise<T>; refreshAuth: () => Promise<unknown>; openProjects: () => Promise<unknown>; complete: (status: T) => void
}): Promise<void> {
  const status = await steps.finalize()
  await steps.refreshAuth()
  if (status.needsOnboarding) throw new Error('Swarm is still finishing onboarding. Please retry.')
  await steps.openProjects()
  steps.complete(status)
}
