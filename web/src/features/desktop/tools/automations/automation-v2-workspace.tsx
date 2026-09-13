import { useRef, useState } from 'react'
import { Clock3, Plus, RefreshCcw } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { desktopAutomationV2, useAutomationV2Page } from '../../runtime/desktop-automation-v2'
import { automationV2Review, type AutomationV2Record, type AutomationV2Mutation } from '../../state/desktop-automation-v2-api'
import { AutomationV2PlanReview } from './automation-v2-plan-review'
import { AutomationConversations } from './automation-conversations'
import { DesktopPlanAgentSidecar } from '../../chat/components/desktop-plan-agent-sidecar'
import { scheduleLabel, scheduleFrequency } from './automation-v2-schedule'

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
// Persisted scheduling handoff survives permission removal, navigation and reload.
export function AutomationV2ScheduleHandoff({ workspaceId, sessionId }: { workspaceId: string; sessionId: string }) {
  const page = useAutomationV2Page({ action: 'progress', workspace_id: workspaceId, session_id: sessionId, timezone: 'UTC' })
  const record = page?.data?.progress?.record
  if (!record) return null
  return <section aria-label="Automation handoff" className="m-4 space-y-2 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4"><h3 className="font-semibold">{record.cancelled ? 'Automation cancelled' : record.enabled ? 'Automation scheduled' : 'Automation paused'}</h3><p className="text-sm">Accepted revision {record.revision}. Acceptance does not start an immediate run.</p><p className="text-sm">{record.enabled && !record.cancelled && record.next_due_at ? `Next scheduled time: ${new Date(record.next_due_at).toISOString()} (UTC)` : 'No active next scheduled time.'}</p><p className="text-xs text-[var(--app-text-muted)]">{page?.stale || page?.loading ? 'Refreshing observed schedule… ' : ''}Scheduled is not admitted or running. Automation details shows the schedule, expiration and observed work.</p></section>
}
export function AutomationV2Detail({ workspaceId, sessionId, onChat }: { workspaceId: string; sessionId: string; onChat?: (id: string) => void }) {
  const [timezone, setTimezone] = useState(() => Intl.DateTimeFormat().resolvedOptions().timeZone)
  const [cursor, setCursor] = useState<string>()
  const [editing, setEditing] = useState(false)
  const [optimizing, setOptimizing] = useState(false)
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
  const time = (ms: number) => new Intl.DateTimeFormat(undefined, { timeZone: timezone, dateStyle: 'medium', timeStyle: 'short' }).format(ms)
  const status = record?.cancelled ? 'Cancelled' : record?.enabled ? 'Scheduled' : 'Paused'
  const eyebrow = 'text-[10px] font-semibold uppercase tracking-[0.16em] text-[var(--app-text-subtle)]'
  const disclosure = 'cursor-pointer text-xs font-medium text-[var(--app-text-muted)]'
  const schedule = record?.document.automation_v2.schedule
  return <section aria-label="Automation details" className="min-w-0 rounded-2xl border border-[var(--app-border)]/70 bg-[var(--app-surface)] font-sans text-xs shadow-[0_1px_2px_color-mix(in_srgb,var(--app-text)_5%,transparent)] [overflow-wrap:anywhere]">
    <div className="space-y-3 p-4">
    <header className="border-b border-[var(--app-border)]/60 pb-3">
      <div className="flex items-center justify-between gap-2"><span className={`${eyebrow} text-[var(--app-primary)]`}>Automation</span><Button size="sm" variant="ghost" className="h-6 w-6 p-0" aria-label="Refresh progress" title="Refresh progress" disabled={busy || page?.loading} onClick={() => void desktopAutomationV2.refresh(input)}><RefreshCcw size={12} /></Button></div>
      <h2 className="mt-1 line-clamp-2 break-words text-sm font-semibold leading-5" title={record?.document.title}>{record?.document.title ?? 'Loading automation…'}</h2>
      <p className="mt-1 text-[10px] text-[var(--app-text-subtle)]">{record ? `${record.document.checkpoints.length} ${record.document.checkpoints.length === 1 ? 'step' : 'steps'} · Recurring plan` : 'Loading schedule'}</p>
    </header>
    {(page?.loading || page?.stale) && <p role="status" className="text-[11px] text-[var(--app-text-muted)]">Updating schedule…</p>}{(error || page?.error) && <p role="alert" className="text-[var(--app-danger)]">{error || page?.error}</p>}
    {record && schedule && <section aria-label="Automation schedule handoff">
      <div className="flex items-center justify-between gap-2"><h3 className={eyebrow}>Schedule</h3><span className={`text-[10px] font-semibold uppercase tracking-wider ${record.enabled && !record.cancelled ? 'text-[var(--app-primary)]' : 'text-[var(--app-text-muted)]'}`}>{status}</span></div>
      <div className="mt-2 rounded-xl border border-[var(--app-primary-border)]/45 bg-[var(--app-primary-soft)] px-3 py-2.5"><p className="font-mono text-[13px] font-medium">{scheduleLabel(schedule)}</p><p className="mt-1 text-[11px] text-[var(--app-primary)]">{scheduleFrequency(schedule)}</p>{schedule.timezone && <p className="mt-1 text-[10px] text-[var(--app-text-muted)]">Schedule timezone · {schedule.timezone}</p>}</div>
      <div className="mt-3 rounded-xl border border-[var(--app-border)]/50 bg-[var(--app-bg-alt)] p-3"><h3 className={eyebrow}>Next run</h3><p className="mt-1.5 font-medium">{record.enabled && !record.cancelled && record.next_due_at ? time(record.next_due_at) : 'No upcoming run'}</p><p className="mt-1 text-[10px] text-[var(--app-text-subtle)]">{timezone} · Scheduled time, not guaranteed start</p></div>
    </section>}
    {record && <>
      <div className="grid grid-cols-2 gap-2"><Button size="sm" variant="outline" className="h-9 rounded-xl text-xs" disabled={disabled || record.cancelled} onClick={() => setEditing(v => !v)} aria-expanded={editing}>Edit automation</Button><Button size="sm" variant="outline" className="h-9 rounded-xl text-xs" disabled={disabled || record.cancelled} onClick={() => void control(record.enabled ? 'pause' : 'resume')}>{record.enabled ? 'Pause schedule' : 'Resume schedule'}</Button></div>
      <p className="text-[11px] leading-4 text-[var(--app-text-subtle)]">{record.authorization.kind === 'indefinite' ? 'Repeats until stopped.' : `Ends ${time(record.authorization.expires_at!)}.`} Pausing leaves admitted work unchanged.</p>
      {editing && <AutomationV2Edit record={record} />}
      <details className="rounded-xl bg-[var(--app-bg-alt)] p-3"><summary className={disclosure}>Instructions · {record.document.checkpoints.length} {record.document.checkpoints.length === 1 ? 'step' : 'steps'}</summary><p className="mt-2 leading-5">{record.document.info.goal}</p><ol className="mt-2 space-y-2">{record.document.checkpoints.map(c => <li key={c.id}><p className="font-medium">{c.title}</p><ul className="mt-1 list-inside list-disc text-[var(--app-text-muted)]">{(c.tasks ?? [c.objective ?? '']).filter(Boolean).map((task, i) => <li key={i}>{task}</li>)}</ul></li>)}</ol></details>
    </>}
    {progress && <details className="rounded-xl bg-[var(--app-bg-alt)] p-3"><summary className={disclosure}>Run history & upcoming times</summary><div className="mt-3 space-y-3">
      <label className="block text-[11px]">Display timezone<select className="mt-1 w-full min-w-0 rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] p-2" value={timezone} onChange={e => { setTimezone(e.target.value); setCursor(undefined) }}>{[...new Set([Intl.DateTimeFormat().resolvedOptions().timeZone, 'UTC', schedule?.timezone].filter((v): v is string => !!v))].map(zone => <option key={zone}>{zone}</option>)}</select></label>
      <p className="text-[11px] text-[var(--app-text-muted)]">Observed {time(progress.observed_at)}. Forecasts are not guaranteed starts.</p><h3 className={eyebrow}>Upcoming times</h3>{progress.no_next_reason && <p>{progress.no_next_reason.replace(/_/g, ' ')}</p>}<ul className="space-y-1">{progress.forecast.map(ms => <li key={ms}>{time(ms)}</li>)}</ul>
      <h3 className={eyebrow}>Observed runs</h3><p className="text-[11px] text-[var(--app-text-muted)]">Admitted means queued, not running. Succeeded means checkpoints completed, not independently verified results.</p>
      <ul className="space-y-2">{progress.occurrences.map(o => <li key={o.id} className="rounded-lg border border-[var(--app-border)]/60 p-2"><p className="font-medium">{o.state.replace(/_/g, ' ')} · {time(o.due_at)}</p><p className="mt-1 text-[11px] text-[var(--app-text-muted)]">{o.detail}</p></li>)}</ul>{!progress.occurrences.length && <p>No recorded runs yet.</p>}<div className="flex flex-wrap gap-2">{cursor && <Button size="sm" variant="outline" onClick={() => setCursor(undefined)}>First observations</Button>}{progress.next_cursor && <Button size="sm" variant="outline" disabled={disabled} onClick={() => setCursor(progress.next_cursor)}>More observations</Button>}</div><p className="text-[10px] text-[var(--app-text-subtle)]">{progress.complete ? 'End of run history.' : 'More observations may be available.'}</p>
    </div></details>}
    </div>
    {record && <div className="space-y-3 border-t border-[var(--app-border)]/60 p-3">
      <Button variant="outline" size="sm" className="h-9 w-full rounded-xl text-xs" disabled={disabled} onClick={() => setOptimizing(v => !v)} aria-expanded={optimizing}>Optimize with Swarm</Button>
      {optimizing && <DesktopPlanAgentSidecar parentSessionId={sessionId} automation={{ automation_v2: true, automation_id: record.automation_id, automation_revision: record.revision, workspace_id: workspaceId }} embedded mobileInline onClose={() => setOptimizing(false)} />}
      <details><summary className={disclosure}>More controls</summary><div className="mt-3 grid gap-2">
        {onChat && <Button size="sm" variant="ghost" onClick={() => onChat(sessionId)}>Open conversation</Button>}
        <Button size="sm" variant="outline" disabled={disabled || record.cancelled} onClick={() => void control('cancel_future')}>Cancel future runs</Button><Button size="sm" variant="outline" disabled={disabled} onClick={() => void control('cancel_all')}>Cancel future & in-flight work</Button>
        <p className="text-[11px] leading-4 text-[var(--app-text-muted)]">Cancel future leaves admitted work unchanged. Cancelling in-flight work requests a stop; it does not confirm that work has stopped. Edits and optimization require review and acceptance.</p><p className="text-[10px] text-[var(--app-text-subtle)]">Accepted revision {record.revision}</p>
      </div></details>
    </div>}
  </section>
}
function AutomationV2Edit({ record }: { record: AutomationV2Record }) {
  const page = useAutomationV2Page({ action: 'review', workspace_id: record.workspace_id, session_id: record.session_id })
  const proposal = page?.data?.proposal
  // The exact current proposal (including any AI changes), never the accepted
  // record's old review token, is the edit base. Server CAS fences all updates.
  return <section aria-label="Edit recurring plan"><p className="mb-3 text-sm">Unaccepted changes do not change active execution. Review the current proposal before replacing future settings.</p>{page?.error && <p role="alert">{page.error} <Button size="sm" variant="outline" onClick={() => void desktopAutomationV2.refresh({ action: 'review', workspace_id: record.workspace_id, session_id: record.session_id })}>Refresh current review</Button></p>}{proposal ? <AutomationV2PlanReview key={proposal.proposal_id} proposal={{ ...proposal, ...automationV2Review(proposal) }} disabled={page?.loading || page?.stale} /> : <p role="status">Loading current review…</p>}</section>
}
