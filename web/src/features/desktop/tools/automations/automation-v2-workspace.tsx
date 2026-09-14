import { useEffect, useMemo, useRef, useState } from 'react'
import {
  AlertCircle,
  AlertTriangle,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Clock3,
  ExternalLink,
  FileText,
  Plus,
  RefreshCcw,
} from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { cn } from '../../../../lib/cn'
import { desktopAutomationV2, useAutomationV2Page } from '../../runtime/desktop-automation-v2'
import {
  automationV2Review,
  type AutomationV2Record,
  type AutomationV2Mutation,
  type AutomationV2Occurrence,
  type AutomationV2OccurrenceDeliverable,
} from '../../state/desktop-automation-v2-api'
import { AutomationV2PlanReview } from './automation-v2-plan-review'
import { AutomationConversations } from './automation-conversations'
import { DesktopPlanAgentSidecar } from '../../chat/components/desktop-plan-agent-sidecar'
import { scheduleLabel, scheduleFrequency } from './automation-v2-schedule'

export function AutomationV2Workspace({
  workspaceId,
  workspacePath,
  workspaceName,
  workspaceSlug,
  initialSessionId,
}: {
  workspaceId: string
  workspacePath: string
  workspaceName: string
  workspaceSlug?: string
  initialSessionId?: string
}) {
  const [cursor, setCursor] = useState<string>()
  const [selected, setSelected] = useState(initialSessionId || '')
  const [session, setSession] = useState('')
  const [createRequest, setCreateRequest] = useState(0)
  const input = { action: 'list' as const, workspace_id: workspaceId, cursor }
  const page = useAutomationV2Page(input)
  const records = page?.data?.records ?? []

  useEffect(() => {
    if (initialSessionId) {
      setSelected(initialSessionId)
    } else if (!selected && records.length > 0) {
      setSelected(records[0].session_id)
    }
  }, [initialSessionId, records, selected])
  return <div className="flex min-h-full min-w-0 flex-col bg-[var(--app-bg)] text-sm text-[var(--app-text)]">
    <header className="flex min-h-[60px] flex-wrap items-center justify-between gap-3 border-b border-[var(--app-border)] px-5 py-3"><div className="flex min-w-0 items-center gap-3"><RefreshCcw size={16} className="text-[var(--app-primary)]" /><h1 className="font-semibold">Automations</h1><span className="truncate text-xs text-[var(--app-text-muted)]">{workspaceName}</span></div><Button size="sm" onClick={() => setCreateRequest(n => n + 1)}><Plus size={15} />Add automation</Button></header>
    <div className="flex min-w-0 flex-1 flex-col xl:flex-row"><main className="min-w-0 flex-1 space-y-5 p-5 sm:p-8">
      <div className="flex flex-wrap items-center justify-between gap-3"><h2 className="text-lg font-semibold">Accepted recurring plans</h2><Button variant="ghost" size="sm" onClick={() => void desktopAutomationV2.refresh(input)}>Refresh automations</Button></div>
      <p className="text-xs text-[var(--app-text-muted)]">Only explicitly accepted V2 plans appear here. Legacy records are retained but cannot execute.</p>
      {(!page || page.loading || page.stale) && <p role="status">{page?.stale && page.data ? 'Updating automation state…' : 'Loading automations…'}</p>}
      {page?.error && <p role="alert">{page.error}</p>}
      <ul className="grid gap-3 sm:grid-cols-2">{records.map(record => <li key={record.automation_id}><button className={cn("flex w-full min-w-0 items-center gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4 text-left hover:bg-[var(--app-surface-hover)] transition-all", selected === record.session_id ? 'border-[var(--app-primary)] ring-1 ring-[var(--app-primary)] bg-[var(--app-surface-hover)]' : '')} onClick={() => setSelected(record.session_id)} aria-current={selected === record.session_id ? 'page' : undefined}><Clock3 className="shrink-0 text-[var(--app-primary)]" size={18} /><span className="min-w-0"><span className="block truncate font-medium">{record.document.title}</span><span className="text-xs text-[var(--app-text-muted)]">{record.cancelled ? 'Cancelled' : record.enabled ? 'Enabled' : 'Paused'} · Automation · revision {record.revision}</span></span></button></li>)}</ul>
      {page?.data && !page.loading && !page.stale && !records.length && <p className="rounded-2xl border border-dashed border-[var(--app-border)] p-8 text-center text-[var(--app-text-muted)]">No accepted automations on this page. Start a conversation to propose one.</p>}
      <div className="flex gap-2">{cursor && <Button variant="outline" size="sm" onClick={() => setCursor(undefined)}>First page</Button>}{page?.data?.next_cursor && <Button variant="outline" size="sm" disabled={page.loading || page.stale} onClick={() => setCursor(page.data?.next_cursor)}>More automations</Button>}</div>
      {selected && <AutomationV2Detail key={selected} workspaceId={workspaceId} sessionId={selected} onChat={setSession} workspaceSlug={workspaceSlug} />}
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
export function AutomationV2Detail({
  workspaceId,
  sessionId,
  onChat,
  workspaceSlug,
  onOpenSession,
}: {
  workspaceId: string
  sessionId: string
  onChat?: (id: string) => void
  workspaceSlug?: string
  onOpenSession?: (id: string) => void
}) {
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
    {progress && (
      <section aria-label="Run history & daily summaries" className="rounded-xl bg-[var(--app-bg-alt)] p-4 space-y-4">
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-[var(--app-border)]/40 pb-2">
          <div>
            <h3 className={eyebrow}>Run history & upcoming times</h3>
            <p className="text-[11px] text-[var(--app-text-muted)]">
              {progress.occurrences.length} {progress.occurrences.length === 1 ? 'run' : 'runs'} observed · Times shown in {timezone}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <label className="flex items-center gap-1.5 text-[11px]">
              <span className="text-[var(--app-text-subtle)]">Timezone:</span>
              <select className="rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-2 py-1 text-[11px]" value={timezone} onChange={e => { setTimezone(e.target.value); setCursor(undefined) }}>
                {[...new Set([Intl.DateTimeFormat().resolvedOptions().timeZone, 'UTC', schedule?.timezone].filter((v): v is string => !!v))].map(zone => <option key={zone}>{zone}</option>)}
              </select>
            </label>
          </div>
        </div>
        {progress.forecast && progress.forecast.length > 0 && (
          <details className="rounded-lg bg-[var(--app-surface)]/60 p-2.5">
            <summary className="cursor-pointer text-[11px] font-medium text-[var(--app-text-muted)]">
              Upcoming schedule forecast ({progress.forecast.length} {progress.forecast.length === 1 ? 'time' : 'times'})
            </summary>
            <div className="mt-2 space-y-1">
              {progress.no_next_reason && <p className="text-[11px] text-[var(--app-text-muted)]">{progress.no_next_reason.replace(/_/g, ' ')}</p>}
              <ul className="space-y-1">{progress.forecast.map(ms => <li key={ms} className="text-[11px] text-[var(--app-text-muted)] font-mono">{time(ms)}</li>)}</ul>
            </div>
          </details>
        )}
        <div className="space-y-3">
          <AutomationV2RunFeed
            occurrences={progress.occurrences}
            timezone={timezone}
            workspaceSlug={workspaceSlug}
            onOpenSession={onOpenSession}
            onChat={onChat}
          />
          {!progress.occurrences.length && (
            <p className="rounded-xl border border-dashed border-[var(--app-border)] p-4 text-center text-[11px] text-[var(--app-text-muted)]">
              No recorded runs yet.
            </p>
          )}
          <div className="flex flex-wrap items-center justify-between gap-2 pt-2 border-t border-[var(--app-border)]/30">
            <div className="flex gap-2">
              {cursor && <Button size="sm" variant="outline" onClick={() => setCursor(undefined)}>First observations</Button>}
              {progress.next_cursor && <Button size="sm" variant="outline" disabled={disabled} onClick={() => setCursor(progress.next_cursor)}>More observations</Button>}
            </div>
            <p className="text-[10px] text-[var(--app-text-subtle)]">{progress.complete ? 'End of run history.' : 'More observations may be available.'}</p>
          </div>
        </div>
      </section>
    )}
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

export function extractOccurrenceDeliverables(
  occurrence: AutomationV2Occurrence,
): AutomationV2OccurrenceDeliverable[] {
  if (occurrence.deliverables && occurrence.deliverables.length > 0) {
    return occurrence.deliverables
  }
  if (occurrence.artifacts && occurrence.artifacts.length > 0) {
    return occurrence.artifacts
  }
  const checkpoints = occurrence.accepted?.document?.checkpoints ?? []
  const checkpointDeliverables: AutomationV2OccurrenceDeliverable[] = []
  for (const cp of checkpoints) {
    if (Array.isArray(cp.artifacts)) {
      for (const a of cp.artifacts) {
        if (a && typeof a === 'object') {
          const item = a as Record<string, unknown>
          checkpointDeliverables.push({
            label: (item.label as string) || (item.filename as string) || (item.path as string) || cp.title,
            path: item.path as string | undefined,
            media_type: item.media_type as string | undefined,
            filename: (item.filename as string) || (item.path as string) || undefined,
            artifact_id: item.artifact_id as string | undefined,
            revision_ref: item.revision_ref as string | undefined,
            session_id: (item.session_id as string) || occurrence.session_id,
            collection_id: item.collection_id as string | undefined,
            variant_id: item.variant_id as string | undefined,
            source_ref: item.source_ref as string | undefined,
          })
        }
      }
    }
  }
  if (checkpointDeliverables.length > 0) {
    return checkpointDeliverables
  }
  if (
    occurrence.closing_state === 'deliverable_ready' ||
    (occurrence.detail && /deliverable|report|summary\s+document|output\s+ready/i.test(occurrence.detail))
  ) {
    return [
      {
        label: occurrence.accepted?.document?.title
          ? `${occurrence.accepted.document.title} - Output`
          : 'Execution Deliverable',
        session_id: occurrence.session_id,
        media_type: 'text/markdown',
      },
    ]
  }
  return []
}

export function formatCalmStatus(occurrence: AutomationV2Occurrence): string {
  if (occurrence.summary?.trim()) return occurrence.summary.trim()
  if (occurrence.detail?.trim()) {
    const detail = occurrence.detail.trim()
    if (detail.toLowerCase().includes('all canonical checkpoints completed')) {
      return 'All checkpoints completed · All good'
    }
    return detail
  }
  return 'Clean run · All good'
}

export function isOccurrenceAwaitingDocument(o: AutomationV2Occurrence): boolean {
  if (o.state === 'awaiting_document' || o.state === 'review_required' || o.state === 'blocked') return true
  if (o.closing_state === 'awaiting_document' || o.closing_state === 'review_required' || o.closing_state === 'blocked') return true
  if (typeof o.detail === 'string' && /awaits (resolution or )?review|awaiting document|blocked/i.test(o.detail)) return true
  return false
}

export function isOccurrenceDeliverableReady(o: AutomationV2Occurrence): boolean {
  if (o.closing_state === 'deliverable_ready') return true
  return extractOccurrenceDeliverables(o).length > 0
}

export function isOccurrenceRoutineClean(o: AutomationV2Occurrence): boolean {
  if (o.closing_state === 'routine_clean') return true
  if (o.state === 'succeeded' || o.state === 'completed') {
    if (o.closing_state === 'deliverable_ready' || o.closing_state === 'attention_alert' || o.closing_state === 'blocked') {
      return false
    }
    if (isOccurrenceAwaitingDocument(o)) return false
    if (extractOccurrenceDeliverables(o).length > 0) return false
    return true
  }
  return false
}

export function OpenExecutionSessionButton({
  sessionId,
  workspaceSlug,
  onOpenSession,
  onChat,
  className,
}: {
  sessionId: string
  workspaceSlug?: string
  onOpenSession?: (id: string) => void
  onChat?: (id: string) => void
  className?: string
}) {
  const handleClick = (e: React.MouseEvent) => {
    if (onOpenSession) {
      e.preventDefault()
      e.stopPropagation()
      onOpenSession(sessionId)
    } else if (onChat) {
      e.preventDefault()
      e.stopPropagation()
      onChat(sessionId)
    }
  }

  const href = workspaceSlug ? `/${encodeURIComponent(workspaceSlug)}/${encodeURIComponent(sessionId)}` : undefined

  const buttonContent = (
    <>
      <span>Open execution session</span>
      <ExternalLink size={11} className="shrink-0 opacity-70" aria-hidden="true" />
    </>
  )

  if (href) {
    return (
      <a
        href={href}
        onClick={handleClick}
        aria-label="Open execution session"
        title="Inspect raw messages, tool calls, and changes on demand"
        data-testid="open-execution-session-link"
        className={cn(
          'inline-flex items-center gap-1 rounded px-2 py-1 text-[11px] font-medium text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-primary)] focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--app-focus-ring)] cursor-pointer transition-colors',
          className,
        )}
      >
        {buttonContent}
      </a>
    )
  }

  return (
    <button
      type="button"
      onClick={handleClick}
      aria-label="Open execution session"
      title="Inspect raw messages, tool calls, and changes on demand"
      data-testid="open-execution-session-button"
      className={cn(
        'inline-flex items-center gap-1 rounded px-2 py-1 text-[11px] font-medium text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-primary)] focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--app-focus-ring)] cursor-pointer transition-colors',
        className,
      )}
    >
      {buttonContent}
    </button>
  )
}

export function DeliverablePreviewLink({
  deliverable,
  workspaceSlug,
  sessionId,
  onOpenSession,
  onChat,
}: {
  deliverable: AutomationV2OccurrenceDeliverable
  workspaceSlug?: string
  sessionId: string
  onOpenSession?: (id: string) => void
  onChat?: (id: string) => void
}) {
  const href = deliverable.url || (workspaceSlug && deliverable.artifact_id
    ? `/${encodeURIComponent(workspaceSlug)}/${encodeURIComponent(deliverable.session_id || sessionId)}?artifact=${encodeURIComponent(deliverable.artifact_id)}`
    : workspaceSlug
      ? `/${encodeURIComponent(workspaceSlug)}/${encodeURIComponent(deliverable.session_id || sessionId)}`
      : undefined)

  const handleClick = (e: React.MouseEvent) => {
    if (onOpenSession) {
      e.preventDefault()
      e.stopPropagation()
      onOpenSession(deliverable.session_id || sessionId)
    } else if (onChat) {
      e.preventDefault()
      e.stopPropagation()
      onChat(deliverable.session_id || sessionId)
    }
  }

  return (
    <a
      href={href || '#'}
      onClick={handleClick}
      aria-label={`Preview ${deliverable.label || deliverable.filename || 'deliverable'}`}
      title="Preview or open deliverable"
      data-testid="run-deliverable-link"
      className="inline-flex items-center gap-1 rounded border border-[var(--app-border)] bg-[var(--app-surface-hover)] px-2 py-0.5 text-[10px] font-medium text-[var(--app-primary)] hover:border-[var(--app-primary-border)] hover:bg-[var(--app-primary-soft)] transition-colors cursor-pointer"
    >
      <span>Preview</span>
      <ExternalLink size={10} className="shrink-0" aria-hidden="true" />
    </a>
  )
}

export function CalmRunCard({
  occurrence,
  timeStr,
  workspaceSlug,
  onOpenSession,
  onChat,
}: {
  occurrence: AutomationV2Occurrence
  timeStr: string
  workspaceSlug?: string
  onOpenSession?: (id: string) => void
  onChat?: (id: string) => void
}) {
  const [expanded, setExpanded] = useState(false)
  const calmText = formatCalmStatus(occurrence)

  return (
    <li
      className="group rounded-xl border border-[var(--app-border)]/50 bg-[var(--app-bg-alt)]/40 px-3 py-2 text-xs transition-colors hover:border-[var(--app-border)] hover:bg-[var(--app-bg-alt)]/70"
      data-testid="calm-run-card"
      data-run-id={occurrence.id}
    >
      <div className="flex min-w-0 items-center justify-between gap-3" data-testid="calm-run-status">
        <div className="flex min-w-0 items-center gap-2">
          <CheckCircle2
            size={13}
            className="shrink-0 text-[var(--app-success)]"
            aria-hidden="true"
          />
          <span className="truncate font-medium text-[var(--app-text)]" title={calmText}>
            {calmText}
          </span>
          <span className="shrink-0 text-[10px] text-[var(--app-text-subtle)]">
            {timeStr}
          </span>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          <OpenExecutionSessionButton
            sessionId={occurrence.session_id}
            workspaceSlug={workspaceSlug}
            onOpenSession={onOpenSession}
            onChat={onChat}
          />
          {occurrence.detail && occurrence.detail !== calmText && (
            <button
              type="button"
              onClick={() => setExpanded((v) => !v)}
              aria-label={expanded ? 'Hide run details' : 'Show run details'}
              title={expanded ? 'Hide run details' : 'Show run details'}
              className="text-[var(--app-text-muted)] hover:text-[var(--app-text)] focus-visible:outline-none"
            >
              {expanded ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
            </button>
          )}
        </div>
      </div>
      {expanded && occurrence.detail && (
        <div className="mt-2 border-t border-[var(--app-border)]/30 pt-2 text-[11px] text-[var(--app-text-muted)] leading-4">
          <p>{occurrence.detail}</p>
        </div>
      )}
    </li>
  )
}

export function DeliverableRunCard({
  occurrence,
  deliverables,
  timeStr,
  workspaceSlug,
  onOpenSession,
  onChat,
}: {
  occurrence: AutomationV2Occurrence
  deliverables: AutomationV2OccurrenceDeliverable[]
  timeStr: string
  workspaceSlug?: string
  onOpenSession?: (id: string) => void
  onChat?: (id: string) => void
}) {
  return (
    <li
      className="rounded-xl border border-[var(--app-primary-border)]/60 bg-[var(--app-primary-soft)]/20 p-3 text-xs shadow-xs space-y-2.5"
      data-testid="deliverable-run-card"
      data-run-id={occurrence.id}
    >
      <div className="flex items-center justify-between gap-2">
        <span className="inline-flex items-center gap-1 rounded-full bg-[var(--app-primary-soft)] px-2.5 py-0.5 text-[10px] font-semibold text-[var(--app-primary)] uppercase tracking-wider">
          <FileText size={11} aria-hidden="true" />
          Deliverable ready
        </span>
        <span className="text-[10px] text-[var(--app-text-subtle)]">{timeStr}</span>
      </div>

      {occurrence.detail && (
        <p className="text-xs font-medium text-[var(--app-text)] leading-4">{occurrence.detail}</p>
      )}

      <div className="space-y-1.5" data-testid="run-deliverables-list">
        {deliverables.map((deliv, index) => {
          const title = deliv.label || deliv.filename || deliv.path || 'Requested Deliverable'
          return (
            <div
              key={deliv.path || deliv.artifact_id || index}
              className="flex items-center justify-between gap-3 rounded-lg border border-[var(--app-border)]/70 bg-[var(--app-surface)] p-2 shadow-xs"
              data-testid="run-deliverable-item"
            >
              <div className="flex min-w-0 items-center gap-2">
                <FileText size={13} className="shrink-0 text-[var(--app-primary)]" aria-hidden="true" />
                <span className="truncate font-medium text-[var(--app-text)]" title={title}>
                  {title}
                </span>
                {deliv.media_type && (
                  <span className="shrink-0 rounded bg-[var(--app-bg-alt)] px-1.5 py-0.5 font-mono text-[9px] text-[var(--app-text-subtle)]">
                    {deliv.media_type}
                  </span>
                )}
              </div>
              <div className="flex shrink-0 items-center gap-2">
                <DeliverablePreviewLink
                  deliverable={deliv}
                  workspaceSlug={workspaceSlug}
                  sessionId={occurrence.session_id}
                  onOpenSession={onOpenSession}
                  onChat={onChat}
                />
              </div>
            </div>
          )
        })}
      </div>

      <div className="flex items-center justify-between border-t border-[var(--app-border)]/40 pt-2 text-[11px]">
        <span className="text-[10px] text-[var(--app-text-subtle)]">Occurrence receipt verified</span>
        <OpenExecutionSessionButton
          sessionId={occurrence.session_id}
          workspaceSlug={workspaceSlug}
          onOpenSession={onOpenSession}
          onChat={onChat}
        />
      </div>
    </li>
  )
}

export function AwaitingDocumentRunCard({
  occurrence,
  timeStr,
  workspaceSlug,
  onOpenSession,
  onChat,
}: {
  occurrence: AutomationV2Occurrence
  timeStr: string
  workspaceSlug?: string
  onOpenSession?: (id: string) => void
  onChat?: (id: string) => void
}) {
  const isBlocked = occurrence.closing_state === 'blocked' || occurrence.state === 'blocked' || /blocked/i.test(occurrence.detail || '')

  return (
    <li
      className="rounded-xl border border-[var(--app-warning,rgba(234,179,8,0.5))]/50 bg-[var(--app-warning-soft,rgba(234,179,8,0.12))] p-3 text-xs shadow-xs space-y-2"
      data-testid="awaiting-document-run-card"
      data-run-id={occurrence.id}
    >
      <div className="flex items-center justify-between gap-2">
        <span className="inline-flex items-center gap-1 rounded-full bg-[var(--app-warning,rgba(234,179,8,0.2))] px-2.5 py-0.5 text-[10px] font-semibold text-[var(--app-warning)] uppercase tracking-wider">
          <AlertCircle size={11} aria-hidden="true" />
          {isBlocked ? 'Blocked · Action needed' : 'Awaiting document review'}
        </span>
        <span className="text-[10px] text-[var(--app-text-subtle)]">{timeStr}</span>
      </div>

      <p className="text-xs text-[var(--app-text)] leading-4">
        {occurrence.detail || (isBlocked ? 'Execution is blocked: external dependency or permission required.' : 'Execution produced a document awaiting user review or checkpoint acceptance.')}
      </p>

      <div className="flex items-center justify-between border-t border-[var(--app-warning,rgba(234,179,8,0.3))]/30 pt-2 text-[11px]">
        <span className="text-[10px] text-[var(--app-text-muted)]">Action needed</span>
        <OpenExecutionSessionButton
          sessionId={occurrence.session_id}
          workspaceSlug={workspaceSlug}
          onOpenSession={onOpenSession}
          onChat={onChat}
        />
      </div>
    </li>
  )
}

export function StandardRunCard({
  occurrence,
  timeStr,
  workspaceSlug,
  onOpenSession,
  onChat,
}: {
  occurrence: AutomationV2Occurrence
  timeStr: string
  workspaceSlug?: string
  onOpenSession?: (id: string) => void
  onChat?: (id: string) => void
}) {
  const isRunning = occurrence.state === 'running' || occurrence.state === 'in_progress'
  const isFailed = occurrence.state === 'failed' || occurrence.closing_state === 'attention_alert'
  const isAdmitted = occurrence.state === 'admitted'

  return (
    <li
      className={cn(
        'rounded-xl border p-3 text-xs space-y-2 transition-colors',
        isRunning
          ? 'border-[var(--app-success-border,rgba(34,197,94,0.4))] bg-[var(--app-success-soft,rgba(34,197,94,0.08))]'
          : isFailed
            ? 'border-[var(--app-danger-border,rgba(239,68,68,0.4))] bg-[var(--app-danger-soft,rgba(239,68,68,0.08))]'
            : 'border-[var(--app-border)]/60 bg-[var(--app-bg-alt)]/40',
      )}
      data-testid="standard-run-card"
      data-run-id={occurrence.id}
      data-run-state={occurrence.state}
    >
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-1.5">
          {isRunning ? (
            <span className="flex items-center gap-1 text-[var(--app-success)] font-medium">
              <span
                data-testid="run-running-dot"
                className="h-1.5 w-1.5 rounded-full bg-[var(--app-success)] animate-pulse"
                aria-hidden="true"
              />
              <span className="capitalize">{occurrence.state.replace(/_/g, ' ')}</span>
            </span>
          ) : isFailed ? (
            <span className="flex items-center gap-1 text-[var(--app-danger)] font-medium">
              <AlertTriangle size={12} aria-hidden="true" />
              <span className="capitalize">Alert · {occurrence.state.replace(/_/g, ' ')}</span>
            </span>
          ) : (
            <span className="capitalize font-medium text-[var(--app-text)]">
              {occurrence.state.replace(/_/g, ' ')}
            </span>
          )}
        </div>
        <span className="text-[10px] text-[var(--app-text-subtle)]">{timeStr}</span>
      </div>

      {occurrence.detail && (
        <p className="text-[11px] text-[var(--app-text-muted)] leading-4">{occurrence.detail}</p>
      )}

      <div className="flex items-center justify-between border-t border-[var(--app-border)]/30 pt-2 text-[11px]">
        <span className="text-[10px] text-[var(--app-text-subtle)]">
          {isAdmitted
            ? 'Admitted means queued for dispatch.'
            : isRunning
              ? 'Active execution in progress.'
              : 'Completed observation.'}
        </span>
        <OpenExecutionSessionButton
          sessionId={occurrence.session_id}
          workspaceSlug={workspaceSlug}
          onOpenSession={onOpenSession}
          onChat={onChat}
        />
      </div>
    </li>
  )
}

export interface DayOccurrenceGroup {
  dayKey: string
  label: string
  occurrences: AutomationV2Occurrence[]
  stats: {
    total: number
    clean: number
    alerts: number
    deliverables: number
    blocked: number
  }
}

export function getOccurrenceDayKey(ms: number, timeZone: string): string {
  try {
    const formatter = new Intl.DateTimeFormat('en-CA', { timeZone, year: 'numeric', month: '2-digit', day: '2-digit' })
    return formatter.format(new Date(ms))
  } catch {
    return new Date(ms).toISOString().slice(0, 10)
  }
}

export function getOccurrenceDayDisplayLabel(dayKey: string, timeZone: string): string {
  const todayKey = getOccurrenceDayKey(Date.now(), timeZone)
  const yesterdayKey = getOccurrenceDayKey(Date.now() - 86400000, timeZone)
  if (dayKey === todayKey) return 'Today'
  if (dayKey === yesterdayKey) return 'Yesterday'
  try {
    const [year, month, day] = dayKey.split('-').map(Number)
    const date = new Date(Date.UTC(year, month - 1, day, 12, 0, 0))
    return new Intl.DateTimeFormat(undefined, { timeZone, weekday: 'short', month: 'short', day: 'numeric' }).format(date)
  } catch {
    return dayKey
  }
}

export function groupOccurrencesByDay(occurrences: AutomationV2Occurrence[], timezone: string): DayOccurrenceGroup[] {
  const groups: DayOccurrenceGroup[] = []
  const groupMap = new Map<string, DayOccurrenceGroup>()

  for (const o of occurrences) {
    const due = o.due_at || 0
    const dayKey = getOccurrenceDayKey(due, timezone)
    let group = groupMap.get(dayKey)
    if (!group) {
      group = {
        dayKey,
        label: getOccurrenceDayDisplayLabel(dayKey, timezone),
        occurrences: [],
        stats: { total: 0, clean: 0, alerts: 0, deliverables: 0, blocked: 0 },
      }
      groupMap.set(dayKey, group)
      groups.push(group)
    }
    group.occurrences.push(o)
    group.stats.total += 1
    if (isOccurrenceRoutineClean(o)) {
      group.stats.clean += 1
    }
    if (isOccurrenceAwaitingDocument(o)) {
      if (o.closing_state === 'blocked' || o.state === 'blocked') {
        group.stats.blocked += 1
      } else {
        group.stats.alerts += 1
      }
    } else if (o.closing_state === 'attention_alert' || o.state === 'failed') {
      group.stats.alerts += 1
    }
    if (isOccurrenceDeliverableReady(o) || extractOccurrenceDeliverables(o).length > 0) {
      group.stats.deliverables += 1
    }
  }

  return groups
}

export function AutomationV2RunFeed({
  occurrences,
  timezone,
  workspaceSlug,
  onOpenSession,
  onChat,
}: {
  occurrences: AutomationV2Occurrence[]
  timezone: string
  workspaceSlug?: string
  onOpenSession?: (id: string) => void
  onChat?: (id: string) => void
}) {
  const time = (ms: number) =>
    new Intl.DateTimeFormat(undefined, {
      timeZone: timezone,
      dateStyle: 'medium',
      timeStyle: 'short',
    }).format(ms)

  // Top-down chronological ordering: newest run first (by due_at descending)
  const sorted = useMemo(() => {
    return [...occurrences].sort((a, b) => (b.due_at || 0) - (a.due_at || 0))
  }, [occurrences])

  const dayGroups = useMemo(() => {
    return groupOccurrencesByDay(sorted, timezone)
  }, [sorted, timezone])

  if (!sorted.length) return null

  return (
    <div
      role="feed"
      aria-label="Run feed"
      data-testid="automation-run-feed"
      className="space-y-4"
    >
      {dayGroups.map((group) => (
        <section key={group.dayKey} aria-label={`Runs for ${group.label}`} className="space-y-2" data-testid="run-feed-day-group">
          <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg bg-[var(--app-surface)]/70 px-3 py-1.5 border border-[var(--app-border)]/50" data-testid="run-feed-day-header">
            <div className="flex items-center gap-2">
              <span className="text-xs font-semibold text-[var(--app-text)]">{group.label}</span>
              <span className="rounded-full bg-[var(--app-bg-alt)] px-2 py-0.5 font-mono text-[10px] text-[var(--app-text-muted)]">
                {group.stats.total} {group.stats.total === 1 ? 'run' : 'runs'}
              </span>
            </div>
            <div className="flex items-center gap-2 text-[10px]">
              {group.stats.clean > 0 && (
                <span className="font-medium text-[var(--app-success)]">✓ {group.stats.clean} clean</span>
              )}
              {group.stats.deliverables > 0 && (
                <span className="font-medium text-[var(--app-primary)]">★ {group.stats.deliverables} deliverable{group.stats.deliverables === 1 ? '' : 's'}</span>
              )}
              {group.stats.alerts > 0 && (
                <span className="font-medium text-[var(--app-warning)]">⚠ {group.stats.alerts} alert{group.stats.alerts === 1 ? '' : 's'}</span>
              )}
              {group.stats.blocked > 0 && (
                <span className="font-medium text-[var(--app-danger)]">✕ {group.stats.blocked} blocked</span>
              )}
            </div>
          </div>
          <ul className="space-y-2">
            {group.occurrences.map((o) => {
              const timeStr = time(o.due_at)
              const deliverables = extractOccurrenceDeliverables(o)

              if (isOccurrenceAwaitingDocument(o)) {
                return (
                  <AwaitingDocumentRunCard
                    key={o.id}
                    occurrence={o}
                    timeStr={timeStr}
                    workspaceSlug={workspaceSlug}
                    onOpenSession={onOpenSession}
                    onChat={onChat}
                  />
                )
              }

              if (isOccurrenceDeliverableReady(o) || deliverables.length > 0) {
                return (
                  <DeliverableRunCard
                    key={o.id}
                    occurrence={o}
                    deliverables={deliverables}
                    timeStr={timeStr}
                    workspaceSlug={workspaceSlug}
                    onOpenSession={onOpenSession}
                    onChat={onChat}
                  />
                )
              }

              if (isOccurrenceRoutineClean(o)) {
                return (
                  <CalmRunCard
                    key={o.id}
                    occurrence={o}
                    timeStr={timeStr}
                    workspaceSlug={workspaceSlug}
                    onOpenSession={onOpenSession}
                    onChat={onChat}
                  />
                )
              }

              return (
                <StandardRunCard
                  key={o.id}
                  occurrence={o}
                  timeStr={timeStr}
                  workspaceSlug={workspaceSlug}
                  onOpenSession={onOpenSession}
                  onChat={onChat}
                />
              )
            })}
          </ul>
        </section>
      ))}
    </div>
  )
}
