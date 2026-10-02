import { useEffect, useMemo, useState } from 'react'
import { TaskSessionLeaseManager } from './desktop-projects-membership'
import { requireDesktopV3RealtimeControllerReady } from '../realtime/v3-realtime-controller'
import { requestJson } from '../../../app/api'
import type { SessionSnapshot } from '../state/desktop-v3-cache-types'
import { useDesktopV3CacheSelector, subscribeDesktopV3Cache } from '../state/desktop-v3-cache-store'
import { buildDesktopV3ChildCardHydrateInput, postDesktopV3SyncHydrate } from '../state/desktop-v3-sync-api'
import { hydrateResponseToAction } from '../state/desktop-v3-cache-wire'
import { dispatchDesktopV3Cache } from '../state/desktop-v3-cache-store'
import { requireProjectConversation, conversationProjectId } from '../orchestrate/project-conversations'
import { desktopProjects } from './desktop-projects'
import { projectConversationBatches } from '../orchestrate/project-entry-policy'

// The list endpoint discovers IDs only. Titles, permissions and conversations remain
// owned by the canonical cache, hydrated through the existing session boundary.
export function useProjectConversations(projectId: string) {
  const [error, setError] = useState('')
  const [loadingProject, setLoadingProject] = useState('')
  const [revision, refresh] = useState(0)
  useEffect(() => {
    setError('')
    if (!projectId) { setLoadingProject(''); return }
    let active = true
    let running = false
    let dirty = false
    const load = async () => {
      if (!active) return
      if (running) { dirty = true; return }
      running = true
      setLoadingProject(projectId)
      try {
        do {
          dirty = false
          const response = await requestJson<{ sessions: Array<{ session: SessionSnapshot }> }>(`/v3/projects/${encodeURIComponent(projectId)}/sessions?limit=200`)
          const ids = (response.sessions || []).flatMap(({ session }) => {
            if (conversationProjectId(session) !== projectId) return []
            requireProjectConversation(projectId, session)
            return [session.id]
          })
          for (const batch of projectConversationBatches(ids)) {
            if (!active) return
            const hydrated = await postDesktopV3SyncHydrate(buildDesktopV3ChildCardHydrateInput(batch, { permissionSummary: true }))
            if (active) dispatchDesktopV3Cache(hydrateResponseToAction(hydrated, batch))
          }
          if (active) setError('')
        } while (dirty && active)
      } catch (cause) { if (active) setError(cause instanceof Error ? cause.message : 'Unable to load conversations') }
      finally { running = false; if (active) setLoadingProject('') }
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
    projectId && record.kind === 'full' && !record.session.navigation_hidden && conversationProjectId(record.session) === projectId ? [record.session] : [])
    .sort((a, b) => b.created_at - a.created_at || a.id.localeCompare(b.id)),
  (a, b) => a.length === b.length && a.every((item, index) => item === b[index]))
  // Session metadata (including Router titles) needs scoped durable delivery even
  // when that conversation is not selected. Do not poll or fetch transcripts here.
  const leases = useMemo(() => new TaskSessionLeaseManager({
    getControllerReady: requireDesktopV3RealtimeControllerReady,
    ownerKeyPrefix: `project-conversations:${projectId}`,
  }), [projectId])
  const idsKey = JSON.stringify(sessions.slice(0, 200).map(session => session.id))
  useEffect(() => () => leases.cleanup(), [leases])
  useEffect(() => {
    const acquisition = leases.reconcile(JSON.parse(idsKey) as string[])
    return acquisition.cancel
  }, [leases, idsKey])
  return { sessions, error, loading: loadingProject === projectId && Boolean(projectId), refresh: () => refresh(value => value + 1) }
}
