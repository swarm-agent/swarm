import { Link } from '@tanstack/react-router'
import { LoaderCircle, MessageSquarePlus } from 'lucide-react'
import type { SessionSnapshot } from '../state/desktop-v3-cache-types'
import { projectConversationLink } from './project-conversations'

export function ProjectConversationSidebar({ projectId, projectName, selectedId, sessions, loading, creating, error, onCreate, onRetry, onSelect }: {
  projectId: string; projectName?: string; selectedId: string; sessions: SessionSnapshot[]
  loading: boolean; creating: boolean; error: string
  onCreate: () => void; onRetry: () => void; onSelect: () => void
}) {
  return <section aria-label="Project sessions" className="swarm-project-sessions">
    <div className="swarm-session-heading">
      <h2>Sessions</h2>
      <button type="button" disabled={!projectName || creating} onClick={onCreate}
        aria-label={creating ? 'Creating…' : 'New session'} title="New session" className="swarm-new-session">
        {creating ? <LoaderCircle size={16} aria-hidden="true" className="animate-spin" /> : <MessageSquarePlus size={16} aria-hidden="true" />}
      </button>
    </div>
    {!projectName && <Link to="/projects">Choose a project</Link>}
    {loading && <p role="status" className="text-xs text-slate-400">Loading sessions…</p>}
    {error && <div className="swarm-session-error"><p role="alert">{error}</p>
      <button type="button" onClick={onRetry} disabled={loading}>Retry sessions</button>
    </div>}
    {projectName && !loading && !error && !sessions.length && <p className="text-xs text-slate-400">No conversations yet. Start a new session.</p>}
    <nav aria-label="Conversation sessions" className="swarm-session-list">
      {sessions.map(session => <Link key={session.id} {...projectConversationLink(projectId, session.id)}
        onClick={onSelect} aria-label={`${session.title || 'New conversation'} ${projectName || ''}`.trim()} aria-current={selectedId === session.id ? 'page' : undefined}
        activeOptions={{ exact: true, includeSearch: false }} className="swarm-session-row">
        <span title={session.title || 'New conversation'}>{session.title || 'New conversation'}</span>
      </Link>)}
    </nav>
  </section>
}
