import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { Archive, ArchiveRestore, ArrowLeft, ListChecks, LoaderCircle, MessageSquarePlus } from 'lucide-react'
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
  onCreate: () => void; onRetry: () => void; onSelect: () => void; onLoadArchived?: () => void
}) {
  // Project identity resets selection, pending feedback and archive view atomically.
  return <ProjectSessionList key={props.projectId} {...props} />
}

function ProjectSessionList({ projectId, projectName, selectedId, sessions, rows, loading, creating, error, onCreate, onRetry, onSelect, onLoadArchived }: Parameters<typeof ProjectConversationSidebar>[0]) {
  const navigate = useNavigate()
  const [archived, setArchived] = useState(false)
  const [selecting, setSelecting] = useState(false)
  const [selection, setSelection] = useState<Set<string>>(new Set())
  const [busy, setBusy] = useState(false)
  const selectionToggle = useRef<HTMLButtonElement>(null)
  const redirectedId = useRef('')
  const focusAfterMutation = useRef(false)
  useEffect(() => {
    if (!busy && focusAfterMutation.current) { focusAfterMutation.current = false; selectionToggle.current?.focus() }
  }, [busy])
  function cancelSelection() {
    setSelecting(false); setSelection(new Set()); setFailures([]); setFeedback('')
    selectionToggle.current?.focus()
  }
  useEffect(() => {
    // Preserve failed bulk selections when an archive receipt redirects the open row.
    if (!inFlight.current && !redirectedId.current) { setSelecting(false); setSelection(new Set()) }
    if (redirectedId.current !== selectedId) redirectedId.current = ''
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
  useEffect(() => {
    if (!selecting) return
    const escape = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !inFlight.current) { event.preventDefault(); cancelSelection() }
    }
    window.addEventListener('keydown', escape)
    return () => window.removeEventListener('keydown', escape)
  }, [selecting])
  // A remote archive also removes the open conversation from navigation. Do not
  // redirect merely because a still-loading list does not contain the route ID.
  useEffect(() => {
    if (selectedId && !inFlight.current && redirectedId.current !== selectedId && allRows.some(row => row.session.id === selectedId && row.archivedVersion)) {
      redirectedId.current = selectedId
      void navigate({ ...projectConversationLink(projectId), replace: true })
    }
  }, [selectedId, rows, projectId, navigate])

  async function mutate(targets: ProjectSessionRow[]) {
    if (inFlight.current || !targets.length) return
    inFlight.current = true
    setBusy(true); setFeedback(`${archived ? 'Restoring' : 'Archiving'} ${targets.length}…`); setFailures([])
    const succeeded = new Set<string>()
    const errors = new Map<string, string>()
    const eligible = [...new Map(targets.map(row => [row.session.id, row])).values()].filter(row => {
      if (!row.active || row.archivedVersion) return true
      errors.set(row.session.id, 'Active work: stop it explicitly in the conversation or wait, then retry.')
      return false
    })
    // The existing endpoint commits an atomic batch. Never retry an ambiguous
    // transport failure automatically, split a rejected batch, or implicitly stop.
    // Bound payloads to 50 IDs and keep only one batch in flight.
    const batchSize = archived ? 8 : 50 // Restore hydrates session views (eight-ID cap).
    for (let offset = 0; offset < eligible.length && mounted.current; offset += batchSize) {
      const batch = eligible.slice(offset, offset + batchSize)
      try {
        let confirmed: Array<string | undefined> | undefined
        if (archived) {
          const result = await unarchiveDesktopV3ReviewSessions(Object.fromEntries(batch.map(row => [row.session.id, row.archivedVersion!])))
          if (result.ok) confirmed = result.unarchived_session_ids
        } else {
          const result = await archiveDesktopV3Sessions(batch.map(row => row.session.id))
          if (result.ok) confirmed = result.results?.filter(item => item.archived).map(item => item.session_id)
        }
        for (const row of batch) {
          if (confirmed?.includes(row.session.id)) succeeded.add(row.session.id)
          else errors.set(row.session.id, 'Change was not confirmed. Refresh sessions before retrying.')
        }
      } catch (cause) {
        for (const row of batch) errors.set(row.session.id, cause instanceof Error ? cause.message : 'Request failed; retry.')
      }
    }
    if (mounted.current) {
      let navigationError = ''
      setSelection(previous => new Set([...previous].filter(id => !succeeded.has(id))))
      if (!archived && succeeded.has(currentId.current) && redirectedId.current !== currentId.current) {
        redirectedId.current = currentId.current
        try { await navigate({ ...projectConversationLink(projectId), replace: true }) }
        catch { navigationError = ' Navigation failed; choose another session.'; redirectedId.current = '' }
      }
      if (!errors.size) { setSelecting(false); setSelection(new Set()) }
      focusAfterMutation.current = true
      setFailures(targets.filter(row => errors.has(row.session.id)).map(row => `${row.session.title || 'New conversation'}: ${errors.get(row.session.id)}`))
      setFeedback(`${succeeded.size} ${archived ? 'restored' : 'archived'}${errors.size ? `; ${errors.size} not changed` : ''}.${navigationError}`)
      setBusy(false)
      // Exact archive receipts update the canonical cache; no full-list refresh.
    }
    inFlight.current = false
  }

  return <section aria-label="Project sessions" className="swarm-project-sessions" data-selecting={selecting || undefined}>
    <div className="swarm-session-heading">
      <h2>{archived ? 'Archived' : 'Sessions'}</h2>
      <div className="swarm-session-actions">
      {projectName && <button ref={selectionToggle} type="button" className="swarm-session-icon" disabled={busy}
        aria-label={selecting ? 'Cancel selection' : 'Select sessions'} title={selecting ? 'Cancel selection (Escape)' : 'Select sessions'} aria-pressed={selecting}
        onClick={() => { if (selecting) cancelSelection(); else { setSelecting(true); setFeedback(''); setFailures([]) } }}><ListChecks size={16} aria-hidden="true" /></button>}
      {selecting ? selected.length > 0 && <button type="button" className="swarm-session-bulk" disabled={busy} onClick={() => void mutate(selected)}
        aria-label={`${archived ? 'Restore' : 'Archive'} selected`}>{busy ? <LoaderCircle size={14} className="animate-spin" aria-hidden="true" /> : <Archive size={14} aria-hidden="true" />}{archived ? 'Restore' : 'Archive'} <span>{selected.length}</span></button> : <>
      {projectName && <button type="button" className="swarm-session-icon" disabled={busy} aria-label={archived ? 'Back to sessions' : 'Archived'} title={archived ? 'Back to sessions' : 'Archived sessions'}
        onClick={() => { if (!archived) onLoadArchived?.(); setArchived(!archived); setSelection(new Set()); setFailures([]); setFeedback('') }}>{archived ? <ArrowLeft size={16} aria-hidden="true" /> : <ArchiveRestore size={16} aria-hidden="true" />}</button>}
      <button type="button" disabled={!projectName || creating} onClick={onCreate}
        aria-label={creating ? 'Creating…' : 'New session'} title="New session" className="swarm-new-session">
        {creating ? <LoaderCircle size={16} aria-hidden="true" className="animate-spin" /> : <MessageSquarePlus size={16} aria-hidden="true" />}
      </button></>}
      </div>
    </div>
    {!projectName && <Link to="/projects">Choose a project</Link>}
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
          onClick={() => { if (!inFlight.current) { setSelecting(false); setSelection(new Set()) } onSelect() }} aria-label={`${title} ${projectName || ''}`.trim()} aria-current={selectedId === session.id ? 'page' : undefined}
          activeOptions={{ exact: true, includeSearch: false }} className="swarm-session-row">
          <span title={title}>{title}</span>
          <span className="swarm-session-metadata"><span title={projectName}>{projectName}</span><span className="swarm-session-activity" title={row.label}>{row.label}</span>{row.timer && <SessionTimer model={row.timer} />}</span>
        </Link>}
        {!selecting && <button type="button" disabled={busy} aria-label={`${archived ? 'Restore' : 'Archive'} ${title}`} title={row.active ? 'Active work must be stopped explicitly before archiving' : archived ? 'Restore session' : 'Archive session'}
          onClick={() => void mutate([row])}>{archived ? <ArchiveRestore size={14} aria-hidden="true" /> : <Archive size={14} aria-hidden="true" />}</button>}
      </div> })}
    </nav>
  </section>
}
