import { Link } from '@tanstack/react-router'
import type { SessionSnapshot } from '../state/desktop-v3-cache-types'
import { projectConversationLink } from './project-conversations'

export function ProjectConversationSidebar({ projectId, projectName, selectedId, sessions, loading, creating, error, onCreate, onRetry, onSelect }: {
  projectId: string; projectName?: string; selectedId: string; sessions: SessionSnapshot[]
  loading: boolean; creating: boolean; error: string
  onCreate: () => void; onRetry: () => void; onSelect: () => void
}) {
  return <section aria-label="Project conversations" className="flex min-h-0 flex-col gap-2 border-b border-slate-800/80 p-3" style={{ maxHeight: '40vh', flexShrink: 0 }}>
    <div className="flex items-center justify-between gap-2">
      <h2 className="text-xs font-semibold text-slate-300">Conversations</h2>
      <button type="button" disabled={!projectName || creating} onClick={onCreate} className="rounded-lg border border-blue-400/40 bg-blue-500/15 px-3 py-2 text-xs font-semibold text-blue-200 disabled:opacity-50">{creating ? 'Creating…' : 'New session'}</button>
    </div>
    {!projectName && <Link to="/projects">Choose a project</Link>}
    {loading && <p role="status" className="text-xs text-slate-400">Loading sessions…</p>}
    {error && <p role="alert" className="text-xs text-red-300">{error}</p>}
    <button type="button" onClick={onRetry} disabled={loading} className="self-start text-xs text-slate-400 underline">{error ? 'Retry sessions' : 'Refresh sessions'}</button>
    {projectName && !loading && !error && !sessions.length && <p className="text-xs text-slate-400">No conversations yet. Start a new session.</p>}
    <nav aria-label="Conversation sessions" className="min-h-0 overflow-y-auto" style={{ overflowY: 'auto', minHeight: 0 }}>
      {sessions.map(session => <Link key={session.id} {...projectConversationLink(projectId, session.id)}
        onClick={onSelect} aria-current={selectedId === session.id ? 'page' : undefined}
        activeOptions={{ exact: true }}
        className={`mb-1 block rounded-xl border px-3 py-2 ${selectedId === session.id ? 'border-blue-400/40 bg-blue-500/15 text-white' : 'border-transparent text-slate-300 hover:bg-white/5'}`}>
        <span className="block truncate text-xs font-medium" title={session.title || 'New conversation'}>{session.title || 'New conversation'}</span>
        <span className="block truncate text-[10px] text-slate-400">{projectName}</span>
      </Link>)}
    </nav>
  </section>
}
