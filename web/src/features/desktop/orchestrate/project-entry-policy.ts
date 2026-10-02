import type { SessionSnapshot } from '../state/desktop-v3-cache-types'

// sessionsV3SyncHydrateOptions caps resources.session_view at eight IDs.
export function projectConversationBatches(ids: string[]): string[][] {
  const unique = [...new Set(ids.map(id => id.trim()).filter(Boolean))]
  return Array.from({ length: Math.ceil(unique.length / 8) }, (_, index) => unique.slice(index * 8, index * 8 + 8))
}

export function admittedConversationId(admission: { projectId: string; sessionId: string } | null, projectId: string, sessionId: string): string {
  return admission?.projectId === projectId && admission.sessionId === sessionId ? sessionId : ''
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
