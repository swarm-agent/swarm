import { useEffect, useState } from 'react'
import { Link, useNavigate, useRouterState } from '@tanstack/react-router'
import { requestJson, getDesktopSessionIdentitySnapshot } from '../../../app/api'
import type { SessionSnapshot } from '../state/desktop-v3-cache-types'
import { conversationProjectId, projectConversationLink } from './project-conversations'
import { postDesktopV3SyncBootstrap } from '../state/desktop-v3-sync-api'
import { dispatchDesktopV3Cache, useDesktopV3CacheSelector } from '../state/desktop-v3-cache-store'
import { bootstrapResponseToAction } from '../state/desktop-v3-cache-wire'
import { workspaceRouteSlugBase } from '../../workspaces/launcher/services/workspace-route'
import { legacyHistorySessions } from './project-entry-policy'
import { projectRouteSegment } from './project-route'
import { ProjectCreationFlow } from './project-creation-flow'
import type { CreationProject } from '../state/project-creation'
import { desktopProjects } from '../runtime/desktop-projects'

export function lastProjectKey() {
  return `swarm:last-project:${getDesktopSessionIdentitySnapshot()?.accountScopeId || ''}`
}

export function ProjectEntryPage() {
  const navigate = useNavigate()
  const route = useRouterState({ select: state => ({ pathname: state.location.pathname, params: state.matches[state.matches.length - 1]?.params as { sessionId?: string; workspaceSlug?: string } }) })
  const [projects, setProjects] = useState<CreationProject[] | null>(null)
  const [error, setError] = useState('')
  const [reload, setReload] = useState(0)
  const showingCreation = projects?.length === 0 && !route.params?.workspaceSlug
  useEffect(() => {
    if (showingCreation) return
    return desktopProjects.onProjectUpdate(() => setReload(value => value + 1))
  }, [showingCreation])
  const [historyIds, setHistoryIds] = useState<string[]>([])
  const [historyLoading, setHistoryLoading] = useState(false)
  const history = useDesktopV3CacheSelector(state => legacyHistorySessions(historyIds.flatMap(id => {
    const record = state.sessionsById[id]
    return record?.kind === 'full' ? [record.session] : []
  })), (a, b) => a.length === b.length && a.every((session, index) => session === b[index]))
  useEffect(() => {
    let active = true
    setError(''); setHistoryIds([]); setHistoryLoading(Boolean(route.params?.workspaceSlug && !route.params.sessionId))
    void (async () => {
      const response = await requestJson<{ projects: CreationProject[] }>('/v3/projects')
      if (!active) return
      setProjects(response.projects || [])
      if (route.params?.workspaceSlug && !route.params.sessionId) {
        // Bounded account history is explicit selection, never inferred project ownership.
        const snapshot = await postDesktopV3SyncBootstrap({
          selector: { kind: 'recent', global: true, recent: { limit: 50 } },
          history: { mode: 'none' }, resources: {}, include_active: false,
        })
        if (!active) return
        dispatchDesktopV3Cache(bootstrapResponseToAction(snapshot))
        setHistoryIds(snapshot.session_order)
      }
      if (route.params?.sessionId) {
        const { session } = await requestJson<{ session: SessionSnapshot }>(`/v3/sessions/${encodeURIComponent(route.params.sessionId)}`)
        if (!active) return
        const owner = conversationProjectId(session)
        const project = response.projects.find(project => project.id === owner)
        if (project) {
          void navigate({ ...projectConversationLink(projectRouteSegment(project, response.projects), session.id), replace: true })
        }
      } else if (route.pathname === '/') {
        let last: string | null = null
        try { last = localStorage.getItem(lastProjectKey()) } catch { /* Selection remains available without browser storage. */ }
        const project = response.projects.find(project => project.id === last)
        if (project) void navigate({ ...projectConversationLink(projectRouteSegment(project, response.projects)), replace: true })
      }
    })().catch(cause => { if (active) setError(cause instanceof Error ? cause.message : 'Unable to load projects') }).finally(() => { if (active) setHistoryLoading(false) })
    return () => { active = false }
  }, [route.pathname, route.params?.sessionId, navigate, reload])
  if (projects?.length === 0 && !error && !route.params?.workspaceSlug) return <ProjectCreationFlow
    onSaved={() => { /* Keep the active flow mounted through generation. */ }}
    onCancel={() => setReload(value => value + 1)}
    onOpen={(project, sessionId) => { void navigate(projectConversationLink(projectRouteSegment(project, [project]), sessionId)) }} />
  return <main className="p-6 space-y-4"><h1>Swarm projects</h1>
    {error && <div role="alert">{error}<button onClick={() => setReload(value => value + 1)}>Retry loading projects</button></div>}
    {!projects && !error && <p role="status">Loading projects…</p>}
    {route.params?.sessionId && <p>This history link has no verified project conversation owner. Choose a project or open the original history; nothing will be moved.</p>}
    <ul>{projects?.map(project => <li key={project.id}><Link {...projectConversationLink(projectRouteSegment(project, projects))}>{project.name}{project.context_generation && project.context_generation.status !== 'ready' ? ` — ${project.context_generation.status === 'failed' ? 'Context failed · resume' : 'Generating context · resume'}` : ''}</Link></li>)}</ul>
    {route.params?.sessionId && route.params.workspaceSlug && <Link to="/history/$workspaceSlug/$sessionId" params={{ workspaceSlug: route.params.workspaceSlug, sessionId: route.params.sessionId }}>Open original session history</Link>}
    {route.params?.workspaceSlug && !route.params.sessionId && <section aria-label="Existing session history">
      <h2>Recent session history</h2>
      <p>Up to 50 recent account sessions. Select the original history without moving it into a project.</p>
      <ul>{history.map(session => <li key={session.id}><Link to="/history/$workspaceSlug/$sessionId" params={{ workspaceSlug: workspaceRouteSlugBase({ path: session.workspace_path, workspaceName: session.workspace_name }), sessionId: session.id }}>{session.title || 'Untitled session'} — {session.workspace_name || session.workspace_path}</Link></li>)}</ul>
      {historyLoading && <p role="status">Loading history…</p>}
      {projects && !historyLoading && !history.length && !error && <p>No recent workspace history found.</p>}
    </section>}
    <Link to="/swarm" search={{ createProject: true }}>Create a project</Link>
  </main>
}
