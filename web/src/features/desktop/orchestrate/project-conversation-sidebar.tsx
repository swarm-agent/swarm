import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { Archive, ArchiveRestore, LoaderCircle, MessageSquarePlus } from 'lucide-react'
import type { SessionSnapshot } from '../state/desktop-v3-cache-types'
import type { ProjectSessionRow } from '../state/project-session-rows'
import { archiveDesktopV3Sessions } from '../session-v3/plan-execution-api'
import { unarchiveDesktopV3ReviewSessions } from '../session-v3/review-worktrees-api'
import { DESKTOP_V3_RUN_TIMER_TOOLTIP, formatDesktopV3RunTimerLabel } from '../chat/components/desktop-v3-run-status'
import { projectConversationLink } from './project-conversations'

function SessionTimer({ model }: { model: NonNullable<ProjectSessionRow['timer']> }) {
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    if (!model.active) return
    setNow(Date.now())
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [model.active, model.startedAt])
  return <span className="swarm-session-timer" title={DESKTOP_V3_RUN_TIMER_TOOLTIP}>{formatDesktopV3RunTimerLabel(model, now)}</span>
}

export function ProjectConversationSidebar(props: {
  projectId: string; projectName?: string; selectedId: string; sessions: SessionSnapshot[]; rows?: ProjectSessionRow[]
  loading: boolean; creating: boolean; error: string
  onCreate: () => void; onRetry: () => void; onSelect: () => void
}) {
  // Project identity resets selection, pending feedback and archive view atomically.
  return <ProjectSessionList key={props.projectId} {...props} />
}

function ProjectSessionList({ projectId, projectName, selectedId, sessions, rows, loading, creating, error, onCreate, onRetry, onSelect }: Parameters<typeof ProjectConversationSidebar>[0]) {
  const navigate = useNavigate()
  const [archived, setArchived] = useState(false)
  const [selecting, setSelecting] = useState(false)
  const [selection, setSelection] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    // Preserve failed bulk selections when an archive receipt redirects the open row.
    if (!busy) { setSelecting(false); setSelection(new Set()) }
  }, [selectedId])
  const [feedback, setFeedback] = useState('')
  const [failures, setFailures] = useState<string[]>([])
  const inFlight = useRef(false)
  const mounted = useRef(true)
  const currentId = useRef(selectedId)
  currentId.current = selectedId
  useEffect(() => { mounted.current = true; return () => { mounted.current = false } }, [])
  const allRows = rows || sessions.map(session => ({ session, active: false, attention: false, label: 'Idle', timer: null, activityAt: session.last_message_at || session.created_at } as ProjectSessionRow))
  const visible = allRows.filter(row => Boolean(row.archivedVersion) === archived)
  const selected = visible.filter(row => selection.has(row.session.id))
  const visibleKey = visible.map(row => row.session.id).join('\n')
  useEffect(() => { setSelection(previous => new Set([...previous].filter(id => visible.some(row => row.session.id === id)))) }, [visibleKey])
  const selectAll = useRef<HTMLInputElement>(null)
  useEffect(() => { if (selectAll.current) selectAll.current.indeterminate = selected.length > 0 && selected.length < visible.length }, [selecting, selected.length, visible.length])
  // A remote archive also removes the open conversation from navigation. Do not
  // redirect merely because a still-loading list does not contain the route ID.
  useEffect(() => {
    if (selectedId && allRows.some(row => row.session.id === selectedId && row.archivedVersion)) {
      void navigate({ ...projectConversationLink(projectId), replace: true })
    }
  }, [selectedId, rows, projectId, navigate])

  async function mutate(targets: ProjectSessionRow[]) {
    if (inFlight.current || !targets.length) return
    inFlight.current = true
    setBusy(true); setFeedback(''); setFailures([])
    let succeeded = 0
    const errors: string[] = []
    // Sequential, bounded requests give each row an exact receipt and retain
    // failed selections. No optimistic removal and no implicit stop API.
    for (const row of targets) {
      if (!mounted.current) break
      const { session } = row
      try {
        if (row.active && !row.archivedVersion) throw new Error('Active work: stop it explicitly in the conversation or wait, then retry.')
        if (row.archivedVersion) {
          const result = await unarchiveDesktopV3ReviewSessions({ [session.id]: row.archivedVersion })
          if (!result.unarchived_session_ids?.includes(session.id)) throw new Error('Restore was not confirmed. Refresh sessions before retrying.')
        } else {
          const result = await archiveDesktopV3Sessions([session.id])
          if (!result.ok || !result.results?.some(item => item.session_id === session.id && item.archived)) throw new Error('Archive was not confirmed. Refresh sessions before retrying.')
          if (mounted.current && currentId.current === session.id) await navigate({ ...projectConversationLink(projectId), replace: true })
        }
        succeeded++
        if (mounted.current) setSelection(previous => { const next = new Set(previous); next.delete(session.id); return next })
      } catch (cause) { errors.push(`${session.title || 'New conversation'}: ${cause instanceof Error ? cause.message : 'Request failed; retry.'}`) }
    }
    inFlight.current = false
    if (mounted.current) {
      setBusy(false); setFailures(errors)
      setFeedback(`${succeeded} ${archived ? 'restored' : 'archived'}${errors.length ? `; ${errors.length} not changed` : ''}.`)
      onRetry()
    }
  }

  return <section aria-label="Project sessions" className="swarm-project-sessions" data-selecting={selecting || undefined}>
    <div className="swarm-session-heading">
      <h2>Sessions</h2>
      <button type="button" disabled={!projectName || creating} onClick={onCreate}
        aria-label={creating ? 'Creating…' : 'New session'} title="New session" className="swarm-new-session">
        {creating ? <LoaderCircle size={16} aria-hidden="true" className="animate-spin" /> : <MessageSquarePlus size={16} aria-hidden="true" />}
      </button>
    </div>
    {!projectName && <Link to="/projects">Choose a project</Link>}
    {projectName && <div className="swarm-session-selection">
      <button type="button" disabled={busy} aria-pressed={selecting} onClick={() => { setSelecting(!selecting); setSelection(new Set()) }}>{selecting ? 'Done selecting' : 'Select sessions'}</button>
      {selecting && <label title={`Select all loaded ${archived ? 'archived' : 'unarchived'} sessions in this project`}><input ref={selectAll} type="checkbox" disabled={busy || !visible.length}
        aria-label={`Select all loaded ${archived ? 'archived' : 'unarchived'} project sessions`} checked={visible.length > 0 && selected.length === visible.length}
        onChange={event => setSelection(new Set(event.target.checked ? visible.map(row => row.session.id) : []))} />Select all</label>}
      <button type="button" disabled={busy} aria-pressed={archived} onClick={() => { setArchived(!archived); setSelection(new Set()); setFailures([]); setFeedback('') }}>{archived ? 'Back to sessions' : 'Archived'}</button>
      {selecting && <span>{selected.length} selected · {visible.length} loaded</span>}
      {selected.length > 0 && <button type="button" disabled={busy} onClick={() => void mutate(selected)}>{busy ? 'Saving…' : `${archived ? 'Restore' : 'Archive'} selected`}</button>}
    </div>}
    {loading && <p role="status" className="text-xs text-slate-400">Loading sessions…</p>}
    {error && <div className="swarm-session-error"><p role="alert">{error}</p>
      <button type="button" onClick={onRetry} disabled={loading}>Retry sessions</button>
    </div>}
    {feedback && <p role="status" className="swarm-session-feedback">{feedback}</p>}
    {failures.length > 0 && <div role="alert" className="swarm-session-error">{failures.map((message, i) => <p key={i}>{message}</p>)}<button type="button" onClick={onRetry} disabled={loading || busy}>Refresh sessions</button></div>}
    {projectName && !loading && !error && !visible.length && <p className="text-xs text-slate-400">{archived ? 'No archived sessions.' : 'No conversations yet. Start a new session.'}</p>}
    <nav aria-label="Conversation sessions" className="swarm-session-list">
      {visible.map(row => { const { session } = row; const title = session.title || 'New conversation'; return <div key={session.id} className="swarm-session-item" data-attention={row.attention || undefined}>
        {selecting && <input type="checkbox" aria-label={`Select ${title}`} checked={selection.has(session.id)} disabled={busy}
          onChange={event => setSelection(previous => { const next = new Set(previous); if (event.target.checked) next.add(session.id); else next.delete(session.id); return next })} />}
        {archived ? <span className="swarm-session-row" title={`${title} — restore to open conversation`}><span>{title}</span><span className="swarm-session-metadata"><span>{projectName}</span><span>Archived</span></span></span> : <Link {...projectConversationLink(projectId, session.id)}
          onClick={() => { setSelecting(false); setSelection(new Set()); onSelect() }} aria-label={`${title} ${projectName || ''}`.trim()} aria-current={selectedId === session.id ? 'page' : undefined}
          activeOptions={{ exact: true, includeSearch: false }} className="swarm-session-row">
          <span title={title}>{title}</span>
          <span className="swarm-session-metadata"><span title={projectName}>{projectName}</span><span className="swarm-session-activity" title={row.label}>{row.label}</span>{row.timer && <SessionTimer model={row.timer} />}</span>
        </Link>}
        <button type="button" disabled={busy} aria-label={`${archived ? 'Restore' : 'Archive'} ${title}`} title={row.active ? 'Active work must be stopped explicitly before archiving' : archived ? 'Restore session' : 'Archive session'}
          onClick={() => void mutate([row])}>{archived ? <ArchiveRestore size={14} aria-hidden="true" /> : <Archive size={14} aria-hidden="true" />}</button>
      </div> })}
    </nav>
  </section>
}
