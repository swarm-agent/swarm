import { useEffect, useState } from 'react'
import { requestJson } from '../../../app/api'
import type { SessionSnapshot } from '../state/desktop-v3-cache-types'
import { useDesktopV3CacheSelector, subscribeDesktopV3Cache } from '../state/desktop-v3-cache-store'
import { buildDesktopV3ChildCardHydrateInput, postDesktopV3SyncHydrate } from '../state/desktop-v3-sync-api'
import { hydrateResponseToAction } from '../state/desktop-v3-cache-wire'
import { dispatchDesktopV3Cache } from '../state/desktop-v3-cache-store'
import { requireProjectConversation, conversationProjectId } from '../orchestrate/project-conversations'
import { desktopProjects } from './desktop-projects'

// The list endpoint discovers IDs only. Titles, permissions and conversations remain
// owned by the canonical cache, hydrated through the existing session boundary.
export function useProjectConversations(projectId: string) {
  const [error, setError] = useState('')
  const [revision, refresh] = useState(0)
  useEffect(() => {
    if (!projectId) return
    let active = true
    let running = false
    let dirty = false
    const load = async () => {
      if (running) { dirty = true; return }
      running = true
      try {
        do {
          dirty = false
          const response = await requestJson<{ sessions: Array<{ session: SessionSnapshot }> }>(`/v3/projects/${encodeURIComponent(projectId)}/sessions?limit=200`)
          for (const { session } of response.sessions || []) {
            if (!active) return
            if (conversationProjectId(session) !== projectId) continue
            requireProjectConversation(projectId, session)
            const hydrated = await postDesktopV3SyncHydrate(buildDesktopV3ChildCardHydrateInput([session.id], { permissionSummary: true }))
            if (active) dispatchDesktopV3Cache(hydrateResponseToAction(hydrated, [session.id]))
          }
          if (active) setError('')
        } while (dirty && active)
      } catch (cause) { if (active) setError(cause instanceof Error ? cause.message : 'Unable to load conversations') }
      finally { running = false }
    }
    void load()
    const unsubscribe = desktopProjects.onProjectUpdate(id => { if (!id || id === projectId) void load() })
    const unsubscribeCache = subscribeDesktopV3Cache(mutation => {
      // Reconnect repair may reveal conversations created in another window.
      if (!mutation || mutation.action.type === 'reconnect.applySnapshot') void load()
    })
    return () => { active = false; unsubscribe(); unsubscribeCache() }
  }, [projectId, revision])
  const sessions = useDesktopV3CacheSelector(state => Object.values(state.sessionsById).flatMap(record =>
    projectId && record.kind === 'full' && conversationProjectId(record.session) === projectId ? [record.session] : []),
  (a, b) => a.length === b.length && a.every((item, index) => item === b[index]))
  return { sessions, error, refresh: () => refresh(value => value + 1) }
}
