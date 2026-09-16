import React, { useEffect, useMemo, useState } from 'react'
import { AlertTriangle, ChevronDown, ChevronUp, Clock3, RefreshCcw } from 'lucide-react'
import { cn } from '../../../../lib/cn'
import { desktopAutomationV2 } from '../../runtime/desktop-automation-v2'
import { useDesktopV3CacheSelector } from '../../state/desktop-v3-cache-store'
import type { DesktopV3CacheState } from '../../state/desktop-v3-cache-types'
import { automationV2PageKey } from '../../state/desktop-automation-v2-state'
import type { AutomationV2Record, AutomationV2Settings } from '../../state/desktop-automation-v2-api'
import { automationV2PermissionProposal } from './automation-v2-plan-review'
import {
  formatScheduleDateTime,
  formatScheduleTime,
  getOccurrenceDayKey,
  getScheduleDailyTotal,
  getScheduleUpcomingCount,
  scheduleFrequency,
  scheduleLabel,
} from './automation-v2-schedule'

export function AutomationSidebarMetadataRow({
  schedule,
  status,
  nextDueAt,
  running,
  runsToday,
  upcomingCount,
  totalJobs,
  workspaceSlug,
  onNavigateToAutomations,
}: {
  schedule?: AutomationV2Settings['schedule']
  status: string
  nextDueAt?: number
  running?: boolean
  runsToday?: number
  upcomingCount?: number
  totalJobs?: number
  workspaceSlug?: string
  onNavigateToAutomations?: () => void
}) {
  const isRunning = running || status === 'Running'
  const cadence = schedule ? scheduleLabel(schedule) : 'Worker'
  const runMetaParts: string[] = []
  const done = typeof runsToday === 'number' ? runsToday : 0
  const upcoming = typeof upcomingCount === 'number' ? upcomingCount : 0

  if (typeof totalJobs === 'number' && totalJobs > 0) {
    runMetaParts.push(`${totalJobs} ${totalJobs === 1 ? 'job' : 'jobs'}`)
    runMetaParts.push(`${done} done`)
    if (upcoming > 0) {
      runMetaParts.push(`${upcoming} left`)
    }
  } else {
    if (done > 0) {
      runMetaParts.push(`${done} done today`)
    }
    if (upcoming > 0) {
      runMetaParts.push(`${upcoming} left`)
    }
  }
  const runMeta = runMetaParts.join(' · ')

  const details = [
    cadence,
    isRunning ? 'Running now' : status,
    schedule?.timezone,
    schedule && scheduleFrequency(schedule),
    runMeta || undefined,
    nextDueAt ? `Next scheduled: ${formatScheduleDateTime(nextDueAt, schedule?.timezone)} (${schedule?.timezone || 'local time'}; not a guaranteed start)` : undefined,
    onNavigateToAutomations || workspaceSlug ? 'Open Workers view' : undefined,
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
      aria-label="Open Workers view"
      title="Open top-down Workers view"
      className="inline-flex shrink-0 items-center gap-1 rounded hover:opacity-85 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--app-focus-ring)] cursor-pointer"
    >
      {statusContent}
    </button>
  ) : workspaceSlug ? (
    <a
      href={`/${encodeURIComponent(workspaceSlug)}/workers`}
      onClick={(e) => {
        e.stopPropagation()
      }}
      aria-label="Open Workers view"
      title="Open top-down Workers view"
      className="inline-flex shrink-0 items-center gap-1 rounded hover:opacity-85 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--app-focus-ring)]"
    >
      {statusContent}
    </a>
  ) : (
    statusContent
  )

  return (
    <div
      aria-label="Worker metadata"
      title={details}
      className="mt-1 flex min-w-0 flex-col gap-1 text-[10px] leading-4 text-[var(--app-text-subtle)]"
    >
      {/* Row 2 of card: Cadence, timezone, and status pill */}
      <div className="flex w-full min-w-0 max-w-full items-center justify-between gap-1.5">
        <span className="flex min-w-0 flex-1 items-center gap-1.5">
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
      <div className="flex w-full min-w-0 max-w-full items-center justify-between gap-1.5 text-[9px] text-[var(--app-text-muted)]">
        <span className="flex min-w-0 flex-1 items-center gap-1.5 truncate">
          {runMeta ? (
            <span className="truncate tabular-nums font-medium">{runMeta}</span>
          ) : schedule ? (
            <span className="truncate">{scheduleFrequency(schedule)}</span>
          ) : (
            <span className="truncate">{isRunning ? 'Running now' : status === 'Schedule unavailable' ? 'Status unavailable' : status}</span>
          )}
        </span>
        {nextDueAt ? (
          <span className="shrink-0 tabular-nums text-[var(--app-text-subtle)]" title={`Next scheduled: ${formatScheduleDateTime(nextDueAt, schedule?.timezone)}`}>
            Next: {formatScheduleTime(nextDueAt, schedule?.timezone)}
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
  const timezone = page?.data?.progress?.timezone || record?.document?.automation_v2?.schedule?.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone

  const todayKey = getOccurrenceDayKey(now, timezone)
  const schedule = record?.document?.automation_v2?.schedule
  const runsToday = useMemo(() => {
    if (!occurrences) return 0
    return occurrences.filter(o => getOccurrenceDayKey(o.due_at || 0, timezone) === todayKey).length
  }, [occurrences, timezone, todayKey])

  const totalJobs = useMemo(() => {
    const dailyTotal = getScheduleDailyTotal(schedule, now, timezone)
    return Math.max(dailyTotal, runsToday + upcomingCount)
  }, [schedule, now, timezone, runsToday, upcomingCount])

  const upcomingCount = useMemo(() => {
    if (record && (!record.enabled || record.cancelled)) return 0
    let count = getScheduleUpcomingCount(schedule, record?.next_due_at, now, timezone, forecast)
    const dailyTotal = getScheduleDailyTotal(schedule, now, timezone)
    if (dailyTotal > 0) {
      count = Math.max(count, Math.max(0, dailyTotal - runsToday))
    }
    return count
  }, [forecast, now, record, schedule, timezone, runsToday])

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
      totalJobs={totalJobs}
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
  totalJobs: number
  alerts: number
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
  let totalJobs = 0
  let alerts = 0

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
      let tz = page.data?.progress?.timezone || record.document?.automation_v2?.schedule?.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone
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
      const todayKey = getOccurrenceDayKey(now, tz)
      let recordRunsToday = 0
      if (occurrences) {
        const todayOccurrences = occurrences.filter(o => getOccurrenceDayKey(o.due_at || 0, tz) === todayKey)
        recordRunsToday = todayOccurrences.length
        runsToday += recordRunsToday
        alerts += todayOccurrences.filter(o =>
          o.closing_state === 'attention_alert' ||
          o.closing_state === 'blocked' ||
          o.state === 'failed' ||
          o.state === 'blocked'
        ).length
      }
      const schedule = record.document?.automation_v2?.schedule
      const recordDailyTotal = getScheduleDailyTotal(schedule, now, tz)
      let recordUpcoming = record.enabled && !record.cancelled
        ? getScheduleUpcomingCount(schedule, record.next_due_at, now, tz, forecast)
        : 0
      if (recordDailyTotal > 0 && record.enabled && !record.cancelled) {
        recordUpcoming = Math.max(recordUpcoming, Math.max(0, recordDailyTotal - recordRunsToday))
      }
      upcoming += recordUpcoming
      const recordTotalJobs = Math.max(recordDailyTotal, recordRunsToday + recordUpcoming)
      totalJobs += recordTotalJobs
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
  if (totalJobs === 0 && (runsToday > 0 || upcoming > 0)) {
    totalJobs = runsToday + upcoming
  }
  return { running, scheduled, paused, pending, total, runsToday, upcoming, totalJobs, alerts }
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
  const hasAlerts = (counts.alerts ?? 0) > 0
  const parts: string[] = []
  if (hasAlerts) {
    parts.push(`${counts.alerts} alert${counts.alerts === 1 ? '' : 's'}`)
  }
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
    counts.alerts > 0 ? `${counts.alerts} alert${counts.alerts === 1 ? '' : 's'}` : undefined,
    counts.running > 0 ? `${counts.running} active running worker${counts.running === 1 ? '' : 's'}` : undefined,
    counts.runsToday > 0 ? `${counts.runsToday} ran today` : undefined,
    counts.upcoming > 0 ? `${counts.upcoming} upcoming` : undefined,
    counts.scheduled > 0 ? `${counts.scheduled} scheduled worker${counts.scheduled === 1 ? '' : 's'}` : undefined,
    counts.paused > 0 ? `${counts.paused} paused` : undefined,
    counts.pending > 0 ? `${counts.pending} awaiting approval` : undefined,
    'Click to open top-down Workers view',
  ].filter(Boolean).join(' · ')

  const badge = (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded-full px-1.5 py-0.5 text-[9px] font-medium leading-none tracking-normal transition-colors',
        hasAlerts
          ? 'bg-[var(--app-warning-bg)] text-[var(--app-warning)]'
          : isRunning
            ? 'bg-[var(--app-success-bg,rgba(34,197,94,0.14))] text-[var(--app-success)]'
            : 'bg-[var(--app-surface-subtle)] text-[var(--app-text-muted)]',
        className,
      )}
      aria-label={`Workers summary: ${label}`}
      title={title}
    >
      {hasAlerts ? (
        <AlertTriangle size={10} className="shrink-0 text-[var(--app-warning)]" aria-hidden="true" />
      ) : isRunning ? (
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
        aria-label={`Workers summary: ${label}. Open top-down Workers view`}
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
        href={`/${encodeURIComponent(workspaceSlug)}/workers`}
        onClick={(e) => {
          e.stopPropagation()
        }}
        aria-label={`Workers summary: ${label}. Open top-down Workers view`}
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

export function formatAutomationHeadline(counts: AutomationSummaryCounts, rootCount: number): string {
  const workerCount = counts.total > 0 ? counts.total : rootCount
  if (workerCount > 0) {
    return `${workerCount} worker${workerCount === 1 ? '' : 's'} today`
  }
  return 'Workers'
}

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
  const alertCount = (counts.alerts ?? 0) + (counts.pending ?? 0)
  const hasAlerts = alertCount > 0
  const headline = formatAutomationHeadline(counts, rootCount)
  const totalJobs = counts.totalJobs ?? (counts.runsToday + counts.upcoming)
  const jobsLeft = counts.upcoming
  const jobsSummary = totalJobs > 0
    ? (jobsLeft > 0 ? `${totalJobs} jobs · ${jobsLeft} left` : `${totalJobs} ${totalJobs === 1 ? 'job' : 'jobs'}`)
    : (jobsLeft > 0 ? `${jobsLeft} jobs left` : '0 jobs')

  const doneTodaySummary = `${counts.runsToday} ${counts.runsToday === 1 ? 'job' : 'jobs'} done today`

  const handleCardClick = () => {
    if (onOpenAutomations) {
      onOpenAutomations()
    } else {
      onExpand()
    }
  }

  return (
    <div
      data-testid="automation-sidebar-compact-card"
      role="button"
      tabIndex={0}
      aria-label={`${headline}, ${jobsSummary}, ${doneTodaySummary}. Click to open top-down Workers view`}
      onClick={handleCardClick}
      onKeyDown={(e) => {
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault()
          handleCardClick()
        }
      }}
      className="group relative flex w-full min-w-0 max-w-full box-border flex-col gap-1.5 rounded-lg border border-[var(--app-border)]/70 bg-[var(--app-surface-subtle)]/40 p-2.5 text-left transition-all hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] cursor-pointer overflow-hidden"
    >
      {/* Top row: Left: X workers today; Right: how many jobs */}
      <div className="flex min-w-0 items-center justify-between gap-1.5">
        <span className="flex min-w-0 items-center gap-1.5 truncate">
          {hasAlerts ? (
            <AlertTriangle
              size={12}
              className="shrink-0 text-[var(--app-warning)]"
              aria-hidden="true"
            />
          ) : (
            <RefreshCcw
              size={12}
              className={cn('shrink-0', isRunning ? 'text-[var(--app-success)]' : 'text-[var(--app-primary)]')}
              aria-hidden="true"
            />
          )}
          <span className="truncate text-[11px] font-semibold text-[var(--app-text)]">
            {headline}
          </span>
        </span>
        <div className="flex items-center gap-1.5 shrink-0 text-[10px] font-medium text-[var(--app-text-muted)] tabular-nums">
          {hasAlerts && (
            <span className="inline-flex items-center gap-1 rounded-full bg-[var(--app-warning-bg)] px-1.5 py-0.5 text-[9px] font-semibold text-[var(--app-warning)]">
              <AlertTriangle size={8} aria-hidden="true" />
              <span>{alertCount} alert{alertCount === 1 ? '' : 's'}</span>
            </span>
          )}
          {isRunning && (
            <span
              data-testid="compact-running-dot"
              className="h-1.5 w-1.5 rounded-full bg-[var(--app-success)] animate-pulse shrink-0"
              aria-hidden="true"
              title={`${counts.running} running`}
            />
          )}
          <span data-testid="compact-jobs-summary">{jobsSummary}</span>
        </div>
      </div>

      {/* Bottom row: Left: how many jobs done today; Right: View and Expand buttons */}
      <div className="flex min-w-0 items-center justify-between gap-1 text-[9px] text-[var(--app-text-muted)]">
        <span data-testid="compact-done-summary" className="min-w-0 truncate font-medium">
          {doneTodaySummary}
        </span>
        <div className="flex items-center gap-2 shrink-0">
          {onOpenAutomations ? (
            <button
              type="button"
              data-testid="compact-view-button"
              onClick={(e) => {
                e.stopPropagation()
                onOpenAutomations()
              }}
              title="Open top-down Workers view"
              className="text-[9px] font-medium text-[var(--app-primary)] hover:underline cursor-pointer"
            >
              View →
            </button>
          ) : workspaceSlug ? (
            <a
              href={`/${encodeURIComponent(workspaceSlug)}/workers`}
              data-testid="compact-view-link"
              onClick={(e) => {
                e.stopPropagation()
              }}
              title="Open top-down Workers view"
              className="text-[9px] font-medium text-[var(--app-primary)] hover:underline"
            >
              View →
            </a>
          ) : null}
          <button
            type="button"
            data-testid="compact-expand-button"
            onClick={(e) => {
              e.stopPropagation()
              onExpand()
            }}
            aria-label="Expand workers in sidebar"
            title="Expand workers in sidebar"
            className="inline-flex items-center gap-0.5 rounded px-1 text-[9px] font-medium text-[var(--app-text-subtle)] hover:text-[var(--app-text)] hover:bg-[var(--app-surface-subtle)] cursor-pointer"
          >
            <span>Expand</span>
            <ChevronDown size={11} className="shrink-0" aria-hidden="true" />
          </button>
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

export function AutomationSidebarExpandedContainerView({
  counts,
  workspaceSlug,
  rootCount,
  onCollapse,
  onOpenAutomations,
  children,
}: {
  counts: AutomationSummaryCounts
  workspaceSlug?: string
  rootCount: number
  onCollapse: () => void
  onOpenAutomations?: () => void
  children: React.ReactNode
}) {
  const isRunning = counts.running > 0
  const alertCount = (counts.alerts ?? 0) + (counts.pending ?? 0)
  const hasAlerts = alertCount > 0
  const headline = formatAutomationHeadline(counts, rootCount)
  const totalJobs = counts.totalJobs ?? (counts.runsToday + counts.upcoming)
  const jobsLeft = counts.upcoming
  const jobsSummary = totalJobs > 0
    ? (jobsLeft > 0 ? `${totalJobs} jobs · ${jobsLeft} left` : `${totalJobs} ${totalJobs === 1 ? 'job' : 'jobs'}`)
    : (jobsLeft > 0 ? `${jobsLeft} jobs left` : '0 jobs')

  return (
    <div
      data-testid="automation-sidebar-expanded-container"
      className="w-full min-w-0 max-w-full box-border flex flex-col gap-1.5 rounded-lg border border-[var(--app-border)]/70 bg-[var(--app-surface-subtle)]/30 p-2 text-left overflow-hidden"
    >
      {/* Container Header */}
      <div className="flex w-full min-w-0 max-w-full items-center justify-between gap-1 pb-1.5 border-b border-[var(--app-border)]/40">
        <span className="flex min-w-0 items-center gap-1.5 truncate">
          {hasAlerts ? (
            <AlertTriangle
              size={12}
              className="shrink-0 text-[var(--app-warning)]"
              aria-hidden="true"
            />
          ) : (
            <RefreshCcw
              size={12}
              className={cn('shrink-0', isRunning ? 'text-[var(--app-success)]' : 'text-[var(--app-primary)]')}
              aria-hidden="true"
            />
          )}
          <span className="truncate text-[11px] font-semibold text-[var(--app-text)]">
            {headline}
          </span>
        </span>
        <div className="flex items-center gap-1.5 shrink-0 ml-auto text-[10px] font-medium text-[var(--app-text-muted)] tabular-nums">
          {hasAlerts && (
            <span className="inline-flex items-center gap-1 rounded-full bg-[var(--app-warning-bg)] px-1.5 py-0.5 text-[9px] font-semibold text-[var(--app-warning)] shrink-0">
              <AlertTriangle size={8} aria-hidden="true" />
              <span>{alertCount} alert{alertCount === 1 ? '' : 's'}</span>
            </span>
          )}
          {isRunning && (
            <span
              data-testid="expanded-running-dot"
              className="h-1.5 w-1.5 rounded-full bg-[var(--app-success)] animate-pulse shrink-0"
              aria-hidden="true"
              title={`${counts.running} running`}
            />
          )}
          <span data-testid="expanded-jobs-summary">{jobsSummary}</span>
        </div>
      </div>

      {/* Container Body holding session cards */}
      <div className="flex w-full min-w-0 max-w-full flex-col gap-1 overflow-hidden">
        {children}
      </div>

      {/* Container Footer: All workers button on left, Collapse button on far right */}
      <div className="flex w-full min-w-0 max-w-full items-center justify-between pt-1 border-t border-[var(--app-border)]/40 text-[9px] text-[var(--app-text-muted)]">
        {onOpenAutomations ? (
          <button
            type="button"
            data-testid="expanded-view-button"
            onClick={onOpenAutomations}
            title="Open top-down Workers view"
            className="inline-flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-[9px] font-medium text-[var(--app-primary)] hover:underline hover:bg-[var(--app-surface-subtle)] cursor-pointer truncate"
          >
            <span>All workers ({rootCount}) →</span>
          </button>
        ) : workspaceSlug ? (
          <a
            href={`/${encodeURIComponent(workspaceSlug)}/workers`}
            data-testid="expanded-view-link"
            title="Open top-down Workers view"
            className="inline-flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-[9px] font-medium text-[var(--app-primary)] hover:underline hover:bg-[var(--app-surface-subtle)] cursor-pointer truncate"
          >
            <span>All workers ({rootCount}) →</span>
          </a>
        ) : (
          <span />
        )}
        <button
          type="button"
          data-testid="expanded-collapse-button"
          onClick={(e) => {
            e.stopPropagation()
            onCollapse()
          }}
          aria-label="Collapse workers to summary card"
          title="Collapse workers to summary card"
          className="inline-flex shrink-0 items-center gap-0.5 rounded px-1 py-0.5 text-[9px] font-medium text-[var(--app-text-subtle)] hover:text-[var(--app-text)] hover:bg-[var(--app-surface-subtle)] cursor-pointer ml-auto"
        >
          <span>Collapse</span>
          <ChevronUp size={11} className="shrink-0" aria-hidden="true" />
        </button>
      </div>
    </div>
  )
}

export function AutomationSidebarExpandedContainer({
  workspaceId,
  workspaceSlug,
  rootCount,
  onCollapse,
  onOpenAutomations,
  children,
}: {
  workspaceId?: string
  workspaceSlug?: string
  rootCount: number
  onCollapse: () => void
  onOpenAutomations?: () => void
  children: React.ReactNode
}) {
  const counts = useDesktopV3CacheSelector(state => selectAutomationSummaryCounts(state, workspaceId))
  return (
    <AutomationSidebarExpandedContainerView
      counts={counts}
      workspaceSlug={workspaceSlug}
      rootCount={rootCount}
      onCollapse={onCollapse}
      onOpenAutomations={onOpenAutomations}
    >
      {children}
    </AutomationSidebarExpandedContainerView>
  )
}
