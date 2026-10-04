import { useEffect, useMemo, useState } from 'react'
import { TaskSessionLeaseManager } from './desktop-projects-membership'
import { requireDesktopV3RealtimeControllerReady } from '../realtime/v3-realtime-controller'
import { requestStartupJson } from '../../../app/api'
import { compareProjectSessionRows, projectSessionRow } from '../state/project-session-rows'
import type { SessionSnapshot, V3SessionTombstone, DesktopV3SessionView } from '../state/desktop-v3-cache-types'
import { useDesktopV3CacheSelector, subscribeDesktopV3Cache } from '../state/desktop-v3-cache-store'
import { hydrateProjectConversationRows } from './project-conversation-hydration'
import { hydrateResponseToAction } from '../state/desktop-v3-cache-wire'
import { dispatchDesktopV3Cache } from '../state/desktop-v3-cache-store'
import { requireProjectConversation, conversationProjectId } from '../orchestrate/project-conversations'
import { desktopProjects } from './desktop-projects'

// Ordered display summaries publish immediately; attention hydration is independent
// and selected conversation history remains owned by the canonical session cache.
export function useProjectConversations(projectId: string) {
  const [archiveProject, setArchiveProject] = useState('')
  const includeArchived = Boolean(projectId) && archiveProject === projectId
  const [error, setError] = useState('')
  const [loadingProject, setLoadingProject] = useState('')
  const [revision, refresh] = useState(0)
  const [loadedProject, setLoadedProject] = useState('')
  useEffect(() => {
    setError('')
    setLoadedProject('')
    if (!projectId) { setLoadingProject(''); return }
    let active = true
    const controller = new AbortController()
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
          const [response, archived] = await Promise.all([
            requestStartupJson<{ sessions: Array<{ session: SessionSnapshot; attention?: DesktopV3SessionView }> }>(`/v3/projects/${encodeURIComponent(projectId)}/sessions?limit=200&view=summary`, { signal: controller.signal }),
            includeArchived ? requestStartupJson<{ tombstones: V3SessionTombstone[] }>(`/v3/projects/${encodeURIComponent(projectId)}/sessions?limit=200&archived_mode=only`, { signal: controller.signal }) : Promise.resolve({ tombstones: [] }),
          ])
          if (!active) return
          for (const { session } of response.sessions || []) requireProjectConversation(projectId, session)
          dispatchDesktopV3Cache({ type: 'projectConversations.applySummaries', projectId, sessions: (response.sessions || []).map(item => item.session), attention: Object.fromEntries((response.sessions || []).flatMap(item => item.attention ? [[item.session.id, item.attention]] : [])) })
          setLoadedProject(projectId)
          setLoadingProject('')
          const archivedIds = (archived.tombstones || []).flatMap(item => item.session && conversationProjectId(item.session) === projectId ? [item.session_id] : [])
          // Active rows already include scoped attention; only requested archive
          // details need the compatibility hydrate boundary.
          await hydrateProjectConversationRows(archivedIds, controller.signal, (hydrated, batch) => {
            if (active) dispatchDesktopV3Cache(hydrateResponseToAction(hydrated, batch))
          })
          if (active) { setError(''); setLoadedProject(projectId) }
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
    return () => { active = false; controller.abort(); unsubscribe(); unsubscribeCache() }
  }, [projectId, revision, includeArchived])
  const sessions = useDesktopV3CacheSelector(state => (state.projectConversationSummaries?.[projectId] || []).flatMap(summary => {
    const record = state.sessionsById[summary.id]
    const session = record?.kind === 'full' && record.session.updated_at >= summary.updated_at ? record.session : summary
    return !state.tombstonesBySession[session.id] && !session.navigation_hidden && conversationProjectId(session) === projectId ? [session] : []
  })
    .sort((a, b) => b.created_at - a.created_at || a.id.localeCompare(b.id)),
  (a, b) => a.length === b.length && a.every((item, index) => item === b[index]))
  // Session metadata (including Router titles) needs scoped durable delivery even
  // when that conversation is not selected. Do not poll or fetch transcripts here.
  const leases = useMemo(() => new TaskSessionLeaseManager({
    getControllerReady: requireDesktopV3RealtimeControllerReady,
    ownerKeyPrefix: `project-conversations:${projectId}`,
  }), [projectId])
  const rows = useDesktopV3CacheSelector(state => [
    ...sessions.filter(session => !state.tombstonesBySession[session.id]).map(session => projectSessionRow(state, session)),
    ...Object.values(state.tombstonesBySession).flatMap(item => item.archived && !item.deleted && item.session && !item.session.navigation_hidden && conversationProjectId(item.session) === projectId
      ? [projectSessionRow(state, item.session, item.updated_at)] : []),
  ].sort(compareProjectSessionRows), (a, b) => JSON.stringify(a) === JSON.stringify(b))
  const idsKey = JSON.stringify(sessions.map(session => session.id).sort())
  useEffect(() => () => leases.cleanup(), [leases])
  useEffect(() => {
    const acquisition = leases.reconcile(JSON.parse(idsKey) as string[])
    return acquisition.cancel
  }, [leases, idsKey])
  return { loadArchived: () => setArchiveProject(projectId), sessions, rows, error, ready: Boolean(projectId) && loadedProject === projectId, loading: loadingProject === projectId && Boolean(projectId), refresh: () => refresh(value => value + 1) }
}
