import { useEffect, useMemo, useState } from 'react'
import { Clock3, RefreshCcw } from 'lucide-react'
import { cn } from '../../../../lib/cn'
import { desktopAutomationV2 } from '../../runtime/desktop-automation-v2'
import { useDesktopV3CacheSelector } from '../../state/desktop-v3-cache-store'
import type { DesktopV3CacheState } from '../../state/desktop-v3-cache-types'
import { automationV2PageKey } from '../../state/desktop-automation-v2-state'
import type { AutomationV2Record, AutomationV2Settings } from '../../state/desktop-automation-v2-api'
import { automationV2PermissionProposal } from './automation-v2-plan-review'
import { scheduleFrequency, scheduleLabel } from './automation-v2-schedule'
import { getOccurrenceDayKey } from './automation-v2-workspace'

export function AutomationSidebarMetadataRow({
  schedule,
  status,
  nextDueAt,
  running,
  runsToday,
  upcomingCount,
  workspaceSlug,
  onNavigateToAutomations,
}: {
  schedule?: AutomationV2Settings['schedule']
  status: string
  nextDueAt?: number
  running?: boolean
  runsToday?: number
  upcomingCount?: number
  workspaceSlug?: string
  onNavigateToAutomations?: () => void
}) {
  const isRunning = running || status === 'Running'
  const cadence = schedule ? scheduleLabel(schedule) : 'Automation'
  const runMetaParts: string[] = []
  if (typeof runsToday === 'number' && runsToday > 0) {
    runMetaParts.push(`${runsToday} ran today`)
  }
  if (typeof upcomingCount === 'number' && upcomingCount > 0) {
    runMetaParts.push(`${upcomingCount} upcoming`)
  }
  const runMeta = runMetaParts.join(' · ')

  const details = [
    cadence,
    isRunning ? 'Running now' : status,
    schedule?.timezone,
    schedule && scheduleFrequency(schedule),
    runMeta || undefined,
    nextDueAt ? `Next scheduled: ${new Date(nextDueAt).toLocaleString()} (local time; not a guaranteed start)` : undefined,
    onNavigateToAutomations || workspaceSlug ? 'Open Automations view' : undefined,
  ].filter(Boolean).join(' · ')

  const statusContent = (
    <span
      className={cn(
        'shrink-0 inline-flex items-center gap-1 rounded-full px-1.5 py-0.5 text-[9px] font-medium leading-none',
        isRunning
          ? 'bg-[var(--app-success-bg,rgba(34,197,94,0.14))] text-[var(--app-success)]'
          : status === 'Needs approval' || status === 'Awaiting acceptance'
            ? 'bg-[var(--app-warning-bg)] text-[var(--app-warning)]'
            : 'bg-[var(--app-surface-subtle)] text-[var(--app-text-muted)]',
      )}
    >
      {isRunning && (
        <span
          data-testid="automation-running-dot"
          className="h-1.5 w-1.5 rounded-full bg-[var(--app-success)] animate-pulse"
          aria-hidden="true"
        />
      )}
      <span>{status}</span>
    </span>
  )

  const statusAction = onNavigateToAutomations ? (
    <button
      type="button"
      onClick={(e) => {
        e.preventDefault()
        e.stopPropagation()
        onNavigateToAutomations()
      }}
      aria-label="Open Automations view"
      title="Open top-down Automations view"
      className="inline-flex shrink-0 items-center gap-1 rounded hover:opacity-85 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--app-focus-ring)] cursor-pointer"
    >
      {statusContent}
    </button>
  ) : workspaceSlug ? (
    <a
      href={`/${encodeURIComponent(workspaceSlug)}/automations`}
      onClick={(e) => {
        e.stopPropagation()
      }}
      aria-label="Open Automations view"
      title="Open top-down Automations view"
      className="inline-flex shrink-0 items-center gap-1 rounded hover:opacity-85 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--app-focus-ring)]"
    >
      {statusContent}
    </a>
  ) : (
    statusContent
  )

  return (
    <div
      aria-label="Automation metadata"
      title={details}
      className="mt-1 flex min-w-0 flex-col gap-1 text-[10px] leading-4 text-[var(--app-text-subtle)]"
    >
      {/* Row 2 of card: Cadence, timezone, and status pill */}
      <div className="flex min-w-0 items-center justify-between gap-1.5">
        <span className="flex min-w-0 items-center gap-1.5">
          <Clock3
            size={11}
            className={cn('shrink-0', isRunning ? 'text-[var(--app-success)]' : 'text-[var(--app-primary)]')}
            aria-hidden="true"
          />
          <span className="min-w-0 truncate font-medium text-[var(--app-text-muted)]">
            {cadence}{schedule?.timezone ? ` · ${schedule.timezone}` : ''}
          </span>
        </span>
        {statusAction}
      </div>

      {/* Row 3 of card: Activity stats (today/upcoming) and next run time or direct view prompt */}
      <div className="flex min-w-0 items-center justify-between gap-1.5 text-[9px] text-[var(--app-text-muted)]">
        <span className="flex min-w-0 items-center gap-1.5 truncate">
          {runMeta ? (
            <span className="truncate tabular-nums font-medium">{runMeta}</span>
          ) : schedule ? (
            <span className="truncate">{scheduleFrequency(schedule)}</span>
          ) : (
            <span className="truncate">{isRunning ? 'Running now' : status === 'Schedule unavailable' ? 'Status unavailable' : status}</span>
          )}
        </span>
        {nextDueAt ? (
          <span className="shrink-0 tabular-nums text-[var(--app-text-subtle)]" title={`Next scheduled: ${new Date(nextDueAt).toLocaleString()}`}>
            Next: {new Date(nextDueAt).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
          </span>
        ) : onNavigateToAutomations || workspaceSlug ? (
          <span className="shrink-0 text-[var(--app-primary)] hover:underline">
            View →
          </span>
        ) : null}
      </div>
    </div>
  )
}

export function AutomationV2SidebarMetadata({
  sessionId,
  identity,
  now,
  needsApproval,
  workspaceSlug,
  onNavigateToAutomations,
}: {
  sessionId: string
  identity: 'pending' | 'accepted'
  now: number
  needsApproval: boolean
  workspaceSlug?: string
  onNavigateToAutomations?: () => void
}) {
  const permission = useDesktopV3CacheSelector(state => state.permissionsBySession[sessionId]?.find(p => p.status === 'pending' && p.requirement === 'automation_v2_acceptance'))
  const proposal = useMemo(() => permission ? automationV2PermissionProposal(permission) : null, [permission])
  const workspaceId = useDesktopV3CacheSelector(state => {
    const session = state.sessionsById[sessionId]
    if (session?.kind === 'full' && session.session.automation_v2) return session.session.automation_v2.workspace_id
    for (const page of Object.values(state.automationV2Pages)) {
      const record = page.data?.record ?? page.data?.progress?.record
      if (record?.session_id === sessionId) return record.workspace_id
      const listed = page.data?.records?.find(record => record.session_id === sessionId)
      if (listed) return listed.workspace_id
    }
    return undefined
  })
  if (identity === 'pending') {
    return (
      <AutomationSidebarMetadataRow
        schedule={proposal?.document.automation_v2.schedule}
        status="Awaiting acceptance"
        workspaceSlug={workspaceSlug}
        onNavigateToAutomations={onNavigateToAutomations}
      />
    )
  }
  if (!workspaceId) {
    return (
      <AutomationSidebarMetadataRow
        status="Schedule unavailable"
        workspaceSlug={workspaceSlug}
        onNavigateToAutomations={onNavigateToAutomations}
      />
    )
  }
  return (
    <AcceptedAutomationMetadata
      workspaceId={workspaceId}
      sessionId={sessionId}
      now={now}
      needsApproval={needsApproval}
      workspaceSlug={workspaceSlug}
      onNavigateToAutomations={onNavigateToAutomations}
    />
  )
}

function AcceptedAutomationMetadata({
  workspaceId,
  sessionId,
  now,
  needsApproval,
  workspaceSlug,
  onNavigateToAutomations,
}: {
  workspaceId: string
  sessionId: string
  now: number
  needsApproval: boolean
  workspaceSlug?: string
  onNavigateToAutomations?: () => void
}) {
  const input = useMemo(() => ({ action: 'progress' as const, workspace_id: workspaceId, session_id: sessionId, timezone: 'UTC' }), [workspaceId, sessionId])
  const key = automationV2PageKey(input)
  const page = useDesktopV3CacheSelector(state => state.automationV2Pages[key])
  const [capacityError, setCapacityError] = useState(false)
  useEffect(() => {
    try { const lease = desktopAutomationV2.acquire(input); setCapacityError(false); return lease.release }
    catch { setCapacityError(true) }
  }, [input])
  const record = page?.data?.progress?.record
  const occurrences = page?.data?.progress?.occurrences
  const forecast = page?.data?.progress?.forecast
  const timezone = page?.data?.progress?.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone

  const todayKey = getOccurrenceDayKey(now, timezone)
  const runsToday = useMemo(() => {
    if (!occurrences) return 0
    return occurrences.filter(o => getOccurrenceDayKey(o.due_at || 0, timezone) === todayKey).length
  }, [occurrences, timezone, todayKey])

  const upcomingCount = useMemo(() => {
    if (forecast && forecast.length > 0) {
      return forecast.filter(ms => ms > now).length
    }
    return record && record.enabled && !record.cancelled && record.next_due_at && record.next_due_at > now ? 1 : 0
  }, [forecast, now, record])

  const isRunning = useDesktopV3CacheSelector(state => {
    if (occurrences?.some(o => o.state === 'running' || o.state === 'in_progress')) return true
    if (occurrences) {
      for (const occ of occurrences) {
        const intent = state.currentRunIntentBySession[occ.session_id]
        if (intent && ['pending_executor', 'running', 'dispatch_blocked'].includes(intent.status)) return true
        const sessionRecord = state.sessionsById[occ.session_id]
        if (sessionRecord?.kind === 'full') {
          const runIntents = state.runIntentsBySession[occ.session_id]
          if (runIntents && Object.values(runIntents).some(i => ['pending_executor', 'running', 'dispatch_blocked'].includes(i.status))) return true
        }
      }
    }
    const selfIntent = state.currentRunIntentBySession[sessionId]
    if (selfIntent && ['pending_executor', 'running', 'dispatch_blocked'].includes(selfIntent.status)) return true
    return false
  })

  const unavailable = capacityError || !!page?.error
  const refreshing = !page || page.loading || page.stale
  const status = unavailable
    ? 'Schedule unavailable'
    : refreshing
      ? 'Updating schedule…'
      : record
        ? automationSidebarStatus(record, now, needsApproval, isRunning)
        : 'Schedule unavailable'
  const running = status === 'Running'

  return (
    <AutomationSidebarMetadataRow
      schedule={record?.document.automation_v2.schedule}
      status={status}
      running={running}
      runsToday={runsToday}
      upcomingCount={upcomingCount}
      nextDueAt={!unavailable && !refreshing && record && (status === 'Scheduled' || running) ? record.next_due_at : undefined}
      workspaceSlug={workspaceSlug}
      onNavigateToAutomations={onNavigateToAutomations}
    />
  )
}

export function automationSidebarStatus(
  record: AutomationV2Record,
  now: number,
  needsApproval: boolean,
  runningOrOccurrences?: boolean | Array<{ state: string }>,
): string {
  if (record.cancelled) return 'Cancelled'
  if (record.authorization.kind === 'at' && record.authorization.expires_at !== undefined && record.authorization.expires_at <= now) return 'Expired'
  if (!record.enabled) return 'Paused'
  if (needsApproval) return 'Needs approval'
  const isRunning = Array.isArray(runningOrOccurrences)
    ? runningOrOccurrences.some(o => o.state === 'running' || o.state === 'in_progress')
    : Boolean(runningOrOccurrences)
  if (isRunning) return 'Running'
  return 'Scheduled'
}

export interface AutomationSummaryCounts {
  running: number
  scheduled: number
  paused: number
  pending: number
  total: number
  runsToday: number
  upcoming: number
}

export function selectAutomationSummaryCounts(
  state: DesktopV3CacheState,
  workspaceId?: string,
  now = Date.now(),
): AutomationSummaryCounts {
  let running = 0
  let scheduled = 0
  let paused = 0
  let pending = 0
  let runsToday = 0
  let upcoming = 0

  const seenAutomationIds = new Set<string>()
  const seenSessionIds = new Set<string>()

  for (const page of Object.values(state.automationV2Pages ?? {})) {
    if (workspaceId && page.input.workspace_id !== workspaceId) continue
    const records = page.data?.records ?? (page.data?.record ? [page.data.record] : (page.data?.progress?.record ? [page.data.progress.record] : []))
    for (const record of records) {
      if (seenAutomationIds.has(record.automation_id)) continue
      seenAutomationIds.add(record.automation_id)
      seenSessionIds.add(record.session_id)
      if (record.cancelled) continue
      if (record.authorization.kind === 'at' && record.authorization.expires_at !== undefined && record.authorization.expires_at <= now) continue
      if (!record.enabled) {
        paused++
        continue
      }
      // Check occurrences on this page or any progress page for this session
      let occurrences = page.data?.progress?.occurrences
      let forecast = page.data?.progress?.forecast
      let tz = page.data?.progress?.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone
      if (!occurrences) {
        for (const p of Object.values(state.automationV2Pages ?? {})) {
          if (p.input.session_id === record.session_id && p.data?.progress?.occurrences) {
            occurrences = p.data.progress.occurrences
            forecast = p.data.progress.forecast ?? forecast
            tz = p.data.progress.timezone ?? tz
            break
          }
        }
      }
      let isRunning = false
      if (occurrences?.some(o => o.state === 'running' || o.state === 'in_progress')) {
        isRunning = true
      } else if (occurrences) {
        for (const occ of occurrences) {
          const intent = state.currentRunIntentBySession[occ.session_id]
          if (intent && ['pending_executor', 'running', 'dispatch_blocked'].includes(intent.status)) {
            isRunning = true
            break
          }
        }
      }
      if (!isRunning) {
        const authorIntent = state.currentRunIntentBySession[record.session_id]
        if (authorIntent && ['pending_executor', 'running', 'dispatch_blocked'].includes(authorIntent.status)) {
          isRunning = true
        }
      }
      if (isRunning) {
        running++
      } else {
        scheduled++
      }
      if (occurrences) {
        const todayKey = getOccurrenceDayKey(now, tz)
        runsToday += occurrences.filter(o => getOccurrenceDayKey(o.due_at || 0, tz) === todayKey).length
      }
      if (forecast && forecast.length > 0) {
        upcoming += forecast.filter(ms => ms > now).length
      } else if (record.enabled && !record.cancelled && record.next_due_at && record.next_due_at > now) {
        upcoming++
      }
    }
  }

  for (const [sId, sessionRecord] of Object.entries(state.sessionsById ?? {})) {
    if (sessionRecord.kind !== 'full' || !sessionRecord.session.automation_v2) continue
    const av2 = sessionRecord.session.automation_v2
    if (workspaceId && av2.workspace_id !== workspaceId) continue
    if (seenSessionIds.has(sId)) continue
    seenSessionIds.add(sId)
    const permissions = state.permissionsBySession[sId]
    if (permissions?.some(p => p.status === 'pending' && p.requirement === 'automation_v2_acceptance')) {
      pending++
      continue
    }
    const intent = state.currentRunIntentBySession[sId]
    if (intent && ['pending_executor', 'running', 'dispatch_blocked'].includes(intent.status)) {
      running++
    } else {
      scheduled++
    }
  }

  for (const [sId, perms] of Object.entries(state.permissionsBySession ?? {})) {
    if (seenSessionIds.has(sId)) continue
    if (perms?.some(p => p.status === 'pending' && p.requirement === 'automation_v2_acceptance')) {
      pending++
    }
  }

  const total = running + scheduled + paused + pending
  return { running, scheduled, paused, pending, total, runsToday, upcoming }
}

export function AutomationSidebarSummaryBadge({
  counts,
  workspaceSlug,
  onNavigate,
  className,
}: {
  counts: AutomationSummaryCounts
  workspaceSlug?: string
  onNavigate?: () => void
  className?: string
}) {
  if (counts.total === 0) return null

  const isRunning = counts.running > 0
  const parts: string[] = []
  if (isRunning) {
    parts.push(`${counts.running} running`)
    if (counts.runsToday > 0) {
      parts.push(`${counts.runsToday} ran today`)
    }
    if (counts.upcoming > 0) {
      parts.push(`${counts.upcoming} upcoming`)
    } else if (counts.scheduled > 0) {
      parts.push(`${counts.scheduled} scheduled`)
    }
  } else if (counts.runsToday > 0) {
    parts.push(`${counts.runsToday} ran today`)
    if (counts.upcoming > 0) {
      parts.push(`${counts.upcoming} upcoming`)
    } else if (counts.scheduled > 0) {
      parts.push(`${counts.scheduled} scheduled`)
    }
  } else if (counts.scheduled > 0) {
    parts.push(`${counts.scheduled} scheduled`)
  } else if (counts.pending > 0) {
    parts.push(`${counts.pending} awaiting approval`)
  } else {
    parts.push(`${counts.paused} paused`)
  }
  const label = parts.join(' · ')

  const title = [
    counts.running > 0 ? `${counts.running} active running automation${counts.running === 1 ? '' : 's'}` : undefined,
    counts.runsToday > 0 ? `${counts.runsToday} ran today` : undefined,
    counts.upcoming > 0 ? `${counts.upcoming} upcoming` : undefined,
    counts.scheduled > 0 ? `${counts.scheduled} scheduled automation${counts.scheduled === 1 ? '' : 's'}` : undefined,
    counts.paused > 0 ? `${counts.paused} paused` : undefined,
    counts.pending > 0 ? `${counts.pending} awaiting approval` : undefined,
    'Click to open top-down Automations view',
  ].filter(Boolean).join(' · ')

  const badge = (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded-full px-1.5 py-0.5 text-[9px] font-medium leading-none tracking-normal transition-colors',
        isRunning
          ? 'bg-[var(--app-success-bg,rgba(34,197,94,0.14))] text-[var(--app-success)]'
          : 'bg-[var(--app-surface-subtle)] text-[var(--app-text-muted)]',
        className,
      )}
      aria-label={`Automations summary: ${label}`}
      title={title}
    >
      {isRunning ? (
        <span
          data-testid="summary-running-dot"
          className="h-1.5 w-1.5 shrink-0 rounded-full bg-[var(--app-success)] animate-pulse"
          aria-hidden="true"
        />
      ) : (
        <Clock3 size={10} className="shrink-0 text-[var(--app-primary)]" aria-hidden="true" />
      )}
      <span className="truncate">{label}</span>
    </span>
  )

  if (onNavigate) {
    return (
      <button
        type="button"
        onClick={(e) => {
          e.preventDefault()
          e.stopPropagation()
          onNavigate()
        }}
        aria-label={`Automations summary: ${label}. Open top-down Automations view`}
        title={title}
        className="inline-flex shrink-0 items-center focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--app-focus-ring)] rounded-full cursor-pointer"
      >
        {badge}
      </button>
    )
  }

  if (workspaceSlug) {
    return (
      <a
        href={`/${encodeURIComponent(workspaceSlug)}/automations`}
        onClick={(e) => {
          e.stopPropagation()
        }}
        aria-label={`Automations summary: ${label}. Open top-down Automations view`}
        title={title}
        className="inline-flex shrink-0 items-center focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--app-focus-ring)] rounded-full cursor-pointer"
      >
        {badge}
      </a>
    )
  }

  return badge
}

export function AutomationV2SidebarSummaryIndicator({
  workspaceId,
  workspaceSlug,
  onNavigate,
  className,
}: {
  workspaceId?: string
  workspaceSlug?: string
  onNavigate?: () => void
  className?: string
}) {
  const counts = useDesktopV3CacheSelector(state => selectAutomationSummaryCounts(state, workspaceId))
  return (
    <AutomationSidebarSummaryBadge
      counts={counts}
      workspaceSlug={workspaceSlug}
      onNavigate={onNavigate}
      className={className}
    />
  )
}

export const AutomationSummaryIndicator = AutomationV2SidebarSummaryIndicator

export function AutomationSidebarCompactCardView({
  counts,
  workspaceSlug,
  rootCount,
  onExpand,
  onOpenAutomations,
}: {
  counts: AutomationSummaryCounts
  workspaceSlug?: string
  rootCount: number
  onExpand: () => void
  onOpenAutomations?: () => void
}) {
  const isRunning = counts.running > 0
  const activeCount = rootCount || counts.total

  const runMetaParts: string[] = []
  if (counts.runsToday > 0) {
    runMetaParts.push(`${counts.runsToday} ran today`)
  }
  if (counts.upcoming > 0) {
    runMetaParts.push(`${counts.upcoming} upcoming`)
  }
  const runMeta = runMetaParts.join(' · ')

  return (
    <div
      data-testid="automation-sidebar-compact-card"
      role="button"
      tabIndex={0}
      aria-label={`Automations overview: ${activeCount} automations, ${isRunning ? `${counts.running} running` : 'none running'}. Click to expand list`}
      onClick={onExpand}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          onExpand()
        }
      }}
      className="group relative flex flex-col gap-1.5 rounded-lg border border-[var(--app-border)]/70 bg-[var(--app-surface-subtle)]/40 p-2.5 text-left transition-all hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] cursor-pointer"
    >
      <div className="flex min-w-0 items-center justify-between gap-1.5">
        <span className="flex min-w-0 items-center gap-1.5">
          <RefreshCcw
            size={12}
            className={cn('shrink-0', isRunning ? 'text-[var(--app-success)]' : 'text-[var(--app-primary)]')}
            aria-hidden="true"
          />
          <span className="truncate text-[11px] font-semibold text-[var(--app-text)]">
            Automations Overview
          </span>
          <span className="shrink-0 text-[10px] text-[var(--app-text-muted)] tabular-nums">
            ({activeCount})
          </span>
        </span>
        {isRunning ? (
          <span className="inline-flex shrink-0 items-center gap-1 rounded-full bg-[var(--app-success-bg,rgba(34,197,94,0.14))] px-1.5 py-0.5 text-[9px] font-semibold text-[var(--app-success)]">
            <span
              data-testid="compact-running-dot"
              className="h-1.5 w-1.5 rounded-full bg-[var(--app-success)] animate-pulse"
              aria-hidden="true"
            />
            <span>{counts.running} running</span>
          </span>
        ) : counts.scheduled > 0 ? (
          <span className="inline-flex shrink-0 items-center rounded-full bg-[var(--app-surface-subtle)] px-1.5 py-0.5 text-[9px] font-medium text-[var(--app-text-muted)]">
            {counts.scheduled} scheduled
          </span>
        ) : (
          <span className="inline-flex shrink-0 items-center rounded-full bg-[var(--app-surface-subtle)] px-1.5 py-0.5 text-[9px] font-medium text-[var(--app-text-subtle)]">
            Idle
          </span>
        )}
      </div>

      <div className="flex min-w-0 items-center justify-between gap-1 text-[9px] text-[var(--app-text-muted)]">
        <span className="min-w-0 truncate">
          {runMeta || (counts.scheduled > 0 ? `${counts.scheduled} scheduled on cadence` : 'Ready')}
        </span>
        <div className="flex items-center gap-2 shrink-0">
          {onOpenAutomations ? (
            <button
              type="button"
              onClick={(e) => {
                e.stopPropagation()
                onOpenAutomations()
              }}
              title="Open top-down Automations view"
              className="text-[9px] text-[var(--app-text-subtle)] hover:text-[var(--app-primary)] hover:underline"
            >
              View
            </button>
          ) : workspaceSlug ? (
            <a
              href={`/${encodeURIComponent(workspaceSlug)}/automations`}
              onClick={(e) => {
                e.stopPropagation()
              }}
              title="Open top-down Automations view"
              className="text-[9px] text-[var(--app-text-subtle)] hover:text-[var(--app-primary)] hover:underline"
            >
              View
            </a>
          ) : null}
          <span className="inline-flex items-center gap-0.5 font-medium text-[var(--app-primary)] group-hover:underline">
            <span>Expand</span>
            <span aria-hidden="true">▾</span>
          </span>
        </div>
      </div>
    </div>
  )
}

export function AutomationSidebarCompactCard({
  workspaceId,
  workspaceSlug,
  rootCount,
  onExpand,
  onOpenAutomations,
}: {
  workspaceId?: string
  workspaceSlug?: string
  rootCount: number
  onExpand: () => void
  onOpenAutomations?: () => void
}) {
  const counts = useDesktopV3CacheSelector(state => selectAutomationSummaryCounts(state, workspaceId))
  return (
    <AutomationSidebarCompactCardView
      counts={counts}
      workspaceSlug={workspaceSlug}
      rootCount={rootCount}
      onExpand={onExpand}
      onOpenAutomations={onOpenAutomations}
    />
  )
}
