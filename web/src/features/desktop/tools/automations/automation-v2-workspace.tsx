import { useRef, useState } from 'react'
import { Clock3, Plus, RefreshCcw } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { desktopAutomationV2, useAutomationV2Page } from '../../runtime/desktop-automation-v2'
import { automationV2Review, type AutomationV2Record, type AutomationV2Mutation } from '../../state/desktop-automation-v2-api'
import { AutomationV2PlanReview } from './automation-v2-plan-review'
import { AutomationConversations } from './automation-conversations'

export function AutomationV2Workspace({ workspaceId, workspacePath, workspaceName }: { workspaceId: string; workspacePath: string; workspaceName: string; workspaceSlug?: string }) {
  const [cursor, setCursor] = useState<string>()
  const [selected, setSelected] = useState('')
  const [session, setSession] = useState('')
  const [createRequest, setCreateRequest] = useState(0)
  const input = { action: 'list' as const, workspace_id: workspaceId, cursor }
  const page = useAutomationV2Page(input)
  const records = page?.data?.records ?? []
  return <div className="flex min-h-full min-w-0 flex-col bg-[var(--app-bg)] text-sm text-[var(--app-text)]">
    <header className="flex min-h-[60px] flex-wrap items-center justify-between gap-3 border-b border-[var(--app-border)] px-5 py-3"><div className="flex min-w-0 items-center gap-3"><RefreshCcw size={16} className="text-[var(--app-primary)]" /><h1 className="font-semibold">Automations</h1><span className="truncate text-xs text-[var(--app-text-muted)]">{workspaceName}</span></div><Button size="sm" onClick={() => setCreateRequest(n => n + 1)}><Plus size={15} />Add automation</Button></header>
    <div className="flex min-w-0 flex-1 flex-col xl:flex-row"><main className="min-w-0 flex-1 space-y-5 p-5 sm:p-8">
      <div className="flex flex-wrap items-center justify-between gap-3"><h2 className="text-lg font-semibold">Accepted recurring plans</h2><Button variant="ghost" size="sm" onClick={() => void desktopAutomationV2.refresh(input)}>Refresh automations</Button></div>
      <p className="text-xs text-[var(--app-text-muted)]">Only explicitly accepted V2 plans appear here. Legacy records are retained but cannot execute.</p>
      {(!page || page.loading || page.stale) && <p role="status">{page?.stale && page.data ? 'Updating automation state…' : 'Loading automations…'}</p>}
      {page?.error && <p role="alert">{page.error}</p>}
      <ul className="grid gap-3 sm:grid-cols-2">{records.map(record => <li key={record.automation_id}><button className="flex w-full min-w-0 items-center gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4 text-left hover:bg-[var(--app-surface-hover)]" onClick={() => setSelected(record.session_id)} aria-current={selected === record.session_id ? 'page' : undefined}><Clock3 className="shrink-0 text-[var(--app-primary)]" size={18} /><span className="min-w-0"><span className="block truncate font-medium">{record.document.title}</span><span className="text-xs text-[var(--app-text-muted)]">{record.cancelled ? 'Cancelled' : record.enabled ? 'Enabled' : 'Paused'} · Automation · revision {record.revision}</span></span></button></li>)}</ul>
      {page?.data && !page.loading && !page.stale && !records.length && <p className="rounded-2xl border border-dashed border-[var(--app-border)] p-8 text-center text-[var(--app-text-muted)]">No accepted automations on this page. Start a conversation to propose one.</p>}
      <div className="flex gap-2">{cursor && <Button variant="outline" size="sm" onClick={() => setCursor(undefined)}>First page</Button>}{page?.data?.next_cursor && <Button variant="outline" size="sm" disabled={page.loading || page.stale} onClick={() => setCursor(page.data?.next_cursor)}>More automations</Button>}</div>
      {selected && <AutomationV2Detail key={selected} workspaceId={workspaceId} sessionId={selected} onChat={setSession} />}
    </main><AutomationConversations workspaceId={workspaceId} workspacePath={workspacePath} selected={session} onSelect={setSession} createRequest={createRequest} /></div>
  </div>
}
export function AutomationV2Detail({ workspaceId, sessionId, onChat }: { workspaceId: string; sessionId: string; onChat?: (id: string) => void }) {
  const [timezone, setTimezone] = useState(() => Intl.DateTimeFormat().resolvedOptions().timeZone)
  const [cursor, setCursor] = useState<string>()
  const [editing, setEditing] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const lock = useRef(false)
  const input = { action: 'progress' as const, workspace_id: workspaceId, session_id: sessionId, timezone, cursor }
  const page = useAutomationV2Page(input)
  const progress = page?.data?.progress, record = progress?.record
  const disabled = busy || !record || !!page?.stale || !!page?.loading
  async function control(action: 'pause' | 'resume' | 'cancel_future' | 'cancel_all') {
    if (lock.current || disabled || !record) return
    lock.current = true; setBusy(true); setError('')
    try { await desktopAutomationV2.mutate({ workspace_id: workspaceId, session_id: sessionId, generation: record.generation, action } as AutomationV2Mutation) }
    catch (cause) { setError(cause instanceof Error ? cause.message : 'Control failed') }
    finally { lock.current = false; setBusy(false) }
  }
  const time = (ms: number) => new Intl.DateTimeFormat(undefined, { timeZone: timezone, dateStyle: 'medium', timeStyle: 'long' }).format(ms)
  return <section aria-label="Automation details" className="min-w-0 space-y-4 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4">
    <h2 className="break-words text-lg font-semibold">Automation · {record?.document.title ?? 'Loading'}</h2>
    {(page?.loading || page?.stale) && <p role="status">Refreshing observed state…</p>}{(error || page?.error) && <p role="alert">{error || page?.error}</p>}
    <div className="flex flex-wrap gap-2"><Button size="sm" variant="outline" onClick={() => void desktopAutomationV2.refresh(input)}>Refresh progress</Button><Button size="sm" disabled={disabled || record?.cancelled} onClick={() => setEditing(v => !v)}>Edit automation plan</Button>{onChat && <Button size="sm" variant="ghost" onClick={() => onChat(sessionId)}>Open authoring conversation</Button>}</div>
    {record && <><p className="text-sm">{record.cancelled ? 'Cancelled' : record.enabled ? 'Enabled' : 'Paused'} · accepted revision {record.revision} · {record.authorization.kind === 'indefinite' ? 'Indefinite' : `Expires ${time(record.authorization.expires_at!)}`}</p>
      <div className="flex flex-wrap gap-2"><Button size="sm" variant="outline" disabled={disabled || record.cancelled} onClick={() => void control(record.enabled ? 'pause' : 'resume')}>{record.enabled ? 'Pause future occurrences' : 'Resume schedule'}</Button><Button size="sm" variant="outline" disabled={disabled || record.cancelled} onClick={() => void control('cancel_future')}>Cancel future occurrences</Button><Button size="sm" variant="outline" disabled={disabled} onClick={() => void control('cancel_all')}>Cancel future and in-flight work</Button></div>
      <p className="text-xs text-[var(--app-text-muted)]">Pause and cancel-future leave admitted work unchanged. Cancel-all requests cancellation of admitted work; it is not immediate confirmation that it stopped.</p>
    </>}
    {editing && record && <AutomationV2Edit record={record} />}
    <label className="block text-xs">Display timezone<select className="ml-2 rounded border border-[var(--app-border)] bg-[var(--app-bg)] p-2" value={timezone} onChange={e => { setTimezone(e.target.value); setCursor(undefined) }}>{[...new Set([Intl.DateTimeFormat().resolvedOptions().timeZone, 'UTC', record?.document.automation_v2.schedule.timezone].filter((v): v is string => !!v))].map(zone => <option key={zone}>{zone}</option>)}</select></label>
    {progress && <><p className="text-xs text-[var(--app-text-muted)]">Observed {time(progress.observed_at)}. Forecasts are not admissions or guaranteed start times.</p><h3 className="font-semibold">Next forecast slots</h3>{progress.no_next_reason && <p>No next slot: {progress.no_next_reason.replace(/_/g, ' ')}</p>}<ul className="space-y-1 text-xs">{progress.forecast.map(ms => <li key={ms}>{time(ms)}</li>)}</ul>
      <h3 className="font-semibold">Observed occurrences</h3><p className="text-xs text-[var(--app-text-muted)]">Admitted is not running. Succeeded means canonical checkpoints completed, not independently verified task correctness. {progress.complete ? 'End of this occurrence listing.' : 'Partial page; more observations available.'}</p>
      <ul className="space-y-3">{progress.occurrences.map(o => <li key={o.id} className="rounded-xl border border-[var(--app-border)] p-3"><p>{o.state} · accepted revision {o.accepted.revision}</p><p className="break-words text-xs text-[var(--app-text-muted)]">{o.detail}</p></li>)}</ul>{!progress.occurrences.length && <p>No observed occurrences on this page.</p>}<div className="flex gap-2">{cursor && <Button size="sm" variant="outline" onClick={() => setCursor(undefined)}>First observations</Button>}{progress.next_cursor && <Button size="sm" variant="outline" disabled={disabled} onClick={() => setCursor(progress.next_cursor)}>More observations</Button>}</div></>}
  </section>
}
function AutomationV2Edit({ record }: { record: AutomationV2Record }) {
  const page = useAutomationV2Page({ action: 'review', workspace_id: record.workspace_id, session_id: record.session_id })
  const proposal = page?.data?.proposal
  // The exact current proposal (including any AI changes), never the accepted
  // record's old review token, is the edit base. Server CAS fences all updates.
  return <section aria-label="Edit recurring plan"><p className="mb-3 text-sm">Unaccepted changes do not change active execution. Review the current proposal before replacing future settings.</p>{page?.error && <p role="alert">{page.error} <Button size="sm" variant="outline" onClick={() => void desktopAutomationV2.refresh({ action: 'review', workspace_id: record.workspace_id, session_id: record.session_id })}>Refresh current review</Button></p>}{proposal ? <AutomationV2PlanReview key={proposal.proposal_id} proposal={{ ...proposal, ...automationV2Review(proposal) }} disabled={page?.loading || page?.stale} /> : <p role="status">Loading current review…</p>}</section>
}
