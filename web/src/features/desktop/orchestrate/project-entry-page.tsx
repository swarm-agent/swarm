import { useEffect, useState } from 'react'
import { Link, useNavigate, useRouterState } from '@tanstack/react-router'
import { requestJson, getDesktopSessionIdentitySnapshot } from '../../../app/api'
import type { SessionSnapshot } from '../state/desktop-v3-cache-types'
import { conversationProjectId, projectConversationLink } from './project-conversations'
import { OrchestrateView } from './OrchestrateView'

export function lastProjectKey() {
  return `swarm:last-project:${getDesktopSessionIdentitySnapshot()?.accountScopeId || ''}`
}

export function ProjectEntryPage() {
  const navigate = useNavigate()
  const route = useRouterState({ select: state => ({ pathname: state.location.pathname, params: state.matches[state.matches.length - 1]?.params as { sessionId?: string; workspaceSlug?: string } }) })
  const [projects, setProjects] = useState<Array<{ id: string; name: string }> | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    let active = true
    setProjects(null); setError('')
    void (async () => {
      const response = await requestJson<{ projects: Array<{ id: string; name: string }> }>('/v3/projects')
      if (!active) return
      setProjects(response.projects || [])
      if (route.params?.sessionId) {
        const { session } = await requestJson<{ session: SessionSnapshot }>(`/v3/sessions/${encodeURIComponent(route.params.sessionId)}`)
        if (!active) return
        const owner = conversationProjectId(session)
        if (response.projects.some(project => project.id === owner)) {
          void navigate({ ...projectConversationLink(owner, session.id), replace: true })
        }
      } else if (route.pathname === '/') {
        let last: string | null = null
        try { last = localStorage.getItem(lastProjectKey()) } catch { /* Selection remains available without browser storage. */ }
        if (response.projects.some(project => project.id === last)) void navigate({ ...projectConversationLink(last!), replace: true })
      }
    })().catch(cause => { if (active) setError(cause instanceof Error ? cause.message : 'Unable to load projects') })
    return () => { active = false }
  }, [route.pathname, route.params?.sessionId, navigate])
  if (projects?.length === 0 && !error && !route.params?.sessionId) return <OrchestrateView />
  return <main className="p-6 space-y-4"><h1>Swarm projects</h1>
    {error && <p role="alert">{error}</p>}
    {!projects && !error && <p role="status">Loading projects…</p>}
    {route.params?.sessionId && <p>This history link has no verified project conversation owner. Choose a project or open the original history; nothing will be moved.</p>}
    <ul>{projects?.map(project => <li key={project.id}><Link {...projectConversationLink(project.id)}>{project.name}</Link></li>)}</ul>
    {route.params?.sessionId && route.params.workspaceSlug && <Link to="/history/$workspaceSlug/$sessionId" params={{ workspaceSlug: route.params.workspaceSlug, sessionId: route.params.sessionId }}>Open original session history</Link>}
    <Link to="/swarm">Create a project</Link>
  </main>
}
