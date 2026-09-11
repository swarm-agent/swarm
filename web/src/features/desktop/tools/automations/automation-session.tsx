import { useRef, useState } from 'react'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { desktopAutomations } from '../../runtime/desktop-automations'
import { usePage, PolicyPanel, UpdateFeed } from './automation-workspace'
import { dayKey } from './automation-view'

export function AutomationSidebar({ workspaceId, workspaceSlug }: { workspaceId: string; workspaceSlug: string }) {
  const [cursor, setCursor] = useState<string>()
  const input = { workspace_id: workspaceId, action: 'list' as const, limit: 20, cursor }
  const page = usePage(input)
  return <section aria-label="Automation" className="max-h-64 shrink-0 overflow-y-auto border-b border-[var(--app-border)] p-3 text-sm">
    <h2 className="font-semibold">Automation</h2>
    <Link to="/$workspaceSlug/automations" params={{ workspaceSlug }}>Manage automations</Link>
    {page?.data?.records?.filter(row => row.definition?.session_id).map(row => <Link key={row.id} className="my-2 block break-words" to="/$workspaceSlug/$sessionId" params={{ workspaceSlug, sessionId: row.definition!.session_id! }}>{row.definition!.name}<span className="block text-xs">{row.definition!.enabled ? 'Enabled' : 'Paused · execution not enabled'}</span></Link>)}
    {(!page || page.loading) && <p role="status">Loading automations…</p>}
    {page?.stale && <p role="status">Automation list is stale.</p>}
    {page?.error && <p role="alert">{page.error}</p>}
    <button onClick={() => void desktopAutomations.refresh(input)}>Refresh</button>{' '}
    {cursor && <button onClick={() => setCursor(undefined)}>First page</button>}
    {page?.data?.next_cursor && <button disabled={page.loading || page.stale} onClick={() => setCursor(page.data?.next_cursor)}>More</button>}
  </section>
}

export function AutomationSessionPanel({ workspaceId, id }: { workspaceId: string; id: string }) {
  const navigate = useNavigate()
  const { workspaceSlug } = useParams({ strict: false }) as { workspaceSlug?: string }
  const input = { workspace_id: workspaceId, action: 'get' as const, id }
  const page = usePage(input)
  const record = page?.data?.records?.[0]
  const [expanded, setExpanded] = useState(false)
  const lock = useRef(false)
  const [pending, setPending] = useState(false)
  const [message, setMessage] = useState('')
  async function act(action: 'run' | 'enable' | 'pause') {
    if (!record || lock.current || pending || page?.loading || page?.stale) return
    lock.current = true
    setPending(true); setMessage('')
    try {
      await desktopAutomations.mutate({ workspace_id: workspaceId, id, expected_revision: record.revision, mutation_id: crypto.randomUUID(), ...(action === 'run' ? { action, scheduled_at: Date.now() } : { action }) })
      setMessage(action === 'run' ? 'Run admitted; this does not mean execution started or succeeded.' : 'Request recorded.')
    } catch (error) { setMessage(error instanceof Error ? error.message : 'Request failed') }
    finally { lock.current = false; setPending(false) }
  }
  return <section aria-label="Automation session" className="max-h-[45vh] shrink-0 overflow-y-auto border-b border-[var(--app-border)] p-3 text-sm">
    <strong>{record?.definition?.name ?? 'Automation'}</strong>{' · '}{record?.definition?.enabled ? 'Enabled' : 'Paused / awaiting approval'}
    <p>Execution and discussion stay in this conversation. Live execution and permission state appear below.</p>
    {record?.definition && <p>Schedule: {record.definition.schedule.kind} {record.definition.schedule.expression} {record.definition.schedule.interval_seconds ? `every ${record.definition.schedule.interval_seconds}s` : ''} {record.definition.schedule.timezone}</p>}
    {(['run', record?.definition?.enabled ? 'pause' : 'enable'] as const).map(action => <button className="mr-3 underline" key={action} disabled={pending || !record || page?.stale || page?.loading} onClick={() => void act(action)}>{action === 'run' ? 'Run now' : action === 'pause' ? 'Pause' : 'Enable'}</button>)}
    <button aria-expanded={expanded} onClick={() => setExpanded(!expanded)}>Policy and occurrence history</button>
    <p role="status">{message || (page?.stale ? 'Automation state is stale.' : page?.loading ? 'Loading…' : '')}</p>
    {page?.error && <p role="alert">{page.error} <button onClick={() => void desktopAutomations.refresh(input)}>Retry</button></p>}
    {expanded && <><PolicyPanel workspaceId={workspaceId} id={id} /><UpdateFeed workspaceId={workspaceId} id={id} timezone="UTC" today={dayKey(Date.now(), 'UTC')} history onChat={sessionId => { if (workspaceSlug) void navigate({ to: '/$workspaceSlug/$sessionId', params: { workspaceSlug, sessionId } }) }} /></>}
  </section>
}
