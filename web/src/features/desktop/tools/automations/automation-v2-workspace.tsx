import { useEffect, useMemo, useRef, useState } from 'react'
import {
  AlertCircle,
  AlertTriangle,
  Archive,
  ArchiveRestore,
  Calendar,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Clock3,
  ExternalLink,
  FileText,
  GitBranch,
  LoaderCircle,
  MessageSquare,
  Pause,
  Plus,
  RefreshCcw,
  Search,
  Sparkles,
  Trash2,
  XCircle,
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
  type AutomationV2Proposal,
  type AutomationV2Settings,
} from '../../state/desktop-automation-v2-api'
import { selectPendingAutomationV2Proposals } from '../../state/desktop-automation-v2-state'
import { dispatchDesktopV3Cache } from '../../state/desktop-v3-cache-store'
import { normalizeDesktopPermission } from '../../permissions/services/desktop-permission-normalization'
import { resolveSessionV3Permission } from '../../session-v3/api'
import { archiveDesktopV3Sessions } from '../../session-v3/plan-execution-api'
import { unarchiveDesktopV3ReviewSessions } from '../../session-v3/review-worktrees-api'
import { deleteDesktopSessions } from '../../session-search/session-search-api'
import { loadAutomationConversations } from '../../state/desktop-automation-conversations'
import { getDesktopV3CacheSnapshot, useDesktopV3CacheSelector } from '../../state/desktop-v3-cache-store'
import { AutomationV2PlanReview } from './automation-v2-plan-review'
import { AutomationV2Sidecar } from './automation-v2-sidecar'
import { scheduleLabel, scheduleFrequency } from './automation-v2-schedule'

export interface AutomationStarterTemplate {
  id: string
  title: string
  cadence: string
  description: string
  prompt: string
  icon: 'git' | 'test' | 'digest' | 'security' | 'clean' | 'custom'
}

export const AUTOMATION_STARTER_TEMPLATES: AutomationStarterTemplate[] = [
  {
    id: 'repo-health',
    title: 'Repository Health Check',
    cadence: 'Daily at 09:00 UTC',
    description: 'Inspect git status, branch tracking, and report routine_clean or alert on uncommitted drift.',
    prompt: 'Help me deploy a worker: daily repository health check at 09:00 UTC that inspects git status and uncommitted drift without modifying files.',
    icon: 'git',
  },
  {
    id: 'test-sentinel',
    title: 'Test & Build Sentinel',
    cadence: 'Every 2 hours',
    description: 'Run fast critical test suites regularly and alert immediately if any tests fail.',
    prompt: 'Help me deploy a worker that runs fast tests every 2 hours and reports routine_clean when passing or attention_alert when failing.',
    icon: 'test',
  },
  {
    id: 'daily-digest',
    title: 'Daily Commit & PR Digest',
    cadence: 'Daily at 18:00 UTC',
    description: 'Summarize recent commits, PRs, and changes into an executive deliverable markdown report.',
    prompt: 'Help me deploy a worker at 18:00 UTC daily to summarize recent commits and PRs into a deliverable markdown report.',
    icon: 'digest',
  },
  {
    id: 'dependency-audit',
    title: 'Dependency Security Audit',
    cadence: 'Weekly (Mondays at 09:00 UTC)',
    description: 'Audit project dependencies for security advisories and outdated packages.',
    prompt: 'Propose a weekly worker every Monday at 09:00 UTC to audit dependencies for security advisories and outdated versions.',
    icon: 'security',
  },
  {
    id: 'worktree-clean',
    title: 'Worktree Maintenance',
    cadence: 'Daily at 02:00 UTC',
    description: 'Check for dangling worktrees, stale temporary branches, and report cleanup status cleanly.',
    prompt: 'Help me deploy a daily worker to inspect stale worktrees and temporary branches, reporting status cleanly.',
    icon: 'clean',
  },
  {
    id: 'custom-workflow',
    title: 'Custom Recurring Routine',
    cadence: 'Flexible interval / cron',
    description: 'Describe any custom workflow, validation script, or recurring task to the assistant.',
    prompt: 'I want to deploy a new worker. Help me design the schedule, tasks, and acceptance criteria.',
    icon: 'custom',
  },
]

function TemplateIcon({ icon }: { icon: AutomationStarterTemplate['icon'] }) {
  switch (icon) {
    case 'git':
      return <GitBranch size={16} className="text-[var(--app-primary)]" />
    case 'test':
      return <CheckCircle2 size={16} className="text-[var(--app-success)]" />
    case 'digest':
      return <FileText size={16} className="text-[var(--app-primary)]" />
    case 'security':
      return <AlertTriangle size={16} className="text-[var(--app-warning)]" />
    case 'clean':
      return <RefreshCcw size={16} className="text-[var(--app-text-muted)]" />
    case 'custom':
    default:
      return <Sparkles size={16} className="text-[var(--app-primary)]" />
  }
}

export interface UpcomingAutomationEvent {
  automationId: string
  sessionId: string
  title: string
  dueAt: number
  cadence: string
  schedule?: AutomationV2Settings['schedule']
}

export function formatRelativeTime(ms: number, now: number = Date.now()): string {
  const diffSec = Math.round((ms - now) / 1000)
  if (diffSec <= 30) return 'due now'
  if (diffSec < 60) return `in ${diffSec}s`
  const diffMin = Math.round(diffSec / 60)
  if (diffMin < 60) return `in ${diffMin}m`
  const diffHours = Math.floor(diffMin / 60)
  const remainingMin = diffMin % 60
  if (diffHours < 24) {
    return remainingMin > 0 ? `in ${diffHours}h ${remainingMin}m` : `in ${diffHours}h`
  }
  const diffDays = Math.round(diffHours / 24)
  return `in ${diffDays}d`
}

export function computeUpcomingAutomationEvents(
  records: AutomationV2Record[],
  now: number = Date.now(),
  horizonMs: number = 48 * 3600 * 1000,
  maxItems: number = 12,
): UpcomingAutomationEvent[] {
  const events: UpcomingAutomationEvent[] = []
  for (const record of records) {
    if (!record.enabled || record.cancelled || record.archived) continue
    const schedule = record.document?.automation_v2?.schedule
    const cadence = schedule ? scheduleFrequency(schedule) : 'Scheduled'
    const nextDue = record.next_due_at
    if (typeof nextDue !== 'number' || nextDue <= now - 60000) continue

    events.push({
      automationId: record.automation_id,
      sessionId: record.session_id,
      title: record.document.title,
      dueAt: nextDue,
      cadence,
      schedule,
    })

    // If interval schedule, project upcoming occurrences within horizon
    if (schedule?.kind === 'interval' && typeof schedule.interval_seconds === 'number' && schedule.interval_seconds >= 60) {
      const intervalMs = schedule.interval_seconds * 1000
      let current = nextDue + intervalMs
      let projectedCount = 1
      while (current <= now + horizonMs && projectedCount < 4) {
        events.push({
          automationId: record.automation_id,
          sessionId: record.session_id,
          title: record.document.title,
          dueAt: current,
          cadence,
          schedule,
        })
        current += intervalMs
        projectedCount++
      }
    }
  }

  return events.sort((a, b) => a.dueAt - b.dueAt).slice(0, maxItems)
}

export function AutomationCardPulse({
  workspaceId,
  sessionId,
  timezone,
  enabled,
  cancelled,
  nextDueAt,
}: {
  workspaceId: string
  sessionId: string
  timezone?: string
  enabled: boolean
  cancelled: boolean
  nextDueAt?: number
}) {
  const tz = timezone || Intl.DateTimeFormat().resolvedOptions().timeZone
  const input = useMemo(() => ({ action: 'progress' as const, workspace_id: workspaceId, session_id: sessionId, timezone: tz }), [workspaceId, sessionId, tz])
  const page = useAutomationV2Page(input)
  const progress = page?.data?.progress
  const occurrences = progress?.occurrences

  const stats = useMemo(() => {
    if (!occurrences) return null
    const todayKey = getOccurrenceDayKey(Date.now(), tz)
    const todayOccurrences = occurrences.filter((o) => getOccurrenceDayKey(o.due_at || 0, tz) === todayKey)
    let clean = 0
    let deliverables = 0
    let alerts = 0
    let blocked = 0
    for (const o of todayOccurrences) {
      if (isOccurrenceRoutineClean(o)) clean++
      if (isOccurrenceDeliverableReady(o) || extractOccurrenceDeliverables(o).length > 0) deliverables++
      if (isOccurrenceAwaitingDocument(o)) {
        if (o.closing_state === 'blocked' || o.state === 'blocked') blocked++
        else alerts++
      } else if (o.closing_state === 'attention_alert' || o.state === 'failed') {
        alerts++
      }
    }
    return {
      total: todayOccurrences.length,
      clean,
      deliverables,
      alerts,
      blocked,
    }
  }, [occurrences, tz])

  if (!enabled || cancelled) {
    return null
  }

  if (!stats || stats.total === 0) {
    return (
      <div className="flex items-center gap-2 text-xs text-[var(--app-text-muted)]" data-testid="automation-card-pulse">
        <span className="inline-flex items-center gap-1.5 rounded-md border border-[var(--app-border)]/60 bg-[var(--app-surface-subtle)] px-2 py-0.5 text-[11px] text-[var(--app-text-muted)]">
          <Clock3 size={11} className="text-[var(--app-text-subtle)]" />
          <span>No runs yet today</span>
        </span>
        {nextDueAt && nextDueAt > Date.now() && (
          <span className="text-[11px] text-[var(--app-text-subtle)]">
            Next {formatRelativeTime(nextDueAt)}
          </span>
        )}
      </div>
    )
  }

  return (
    <div className="flex flex-wrap items-center gap-1.5 text-xs" data-testid="automation-card-pulse">
      <span className="text-[11px] font-medium text-[var(--app-text-muted)] mr-0.5">Today:</span>
      <span className="inline-flex items-center gap-1 rounded-md border border-[var(--app-success-border,rgba(16,185,129,0.3))] bg-[var(--app-success-bg,rgba(16,185,129,0.12))] px-2 py-0.5 text-[10.5px] font-medium text-[var(--app-success)]">
        ✓ {stats.clean} clean
      </span>
      {stats.deliverables > 0 && (
        <span className="inline-flex items-center gap-1 rounded-md border border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] px-2 py-0.5 text-[10.5px] font-medium text-[var(--app-primary)]">
          ★ {stats.deliverables} deliverable{stats.deliverables === 1 ? '' : 's'}
        </span>
      )}
      {stats.alerts > 0 && (
        <span className="inline-flex items-center gap-1 rounded-md border border-[var(--app-warning-border,rgba(245,158,11,0.4))] bg-[var(--app-warning-bg,rgba(245,158,11,0.12))] px-2 py-0.5 text-[10.5px] font-medium text-[var(--app-warning)]">
          ⚠ {stats.alerts} alert{stats.alerts === 1 ? '' : 's'}
        </span>
      )}
      {stats.blocked > 0 && (
        <span className="inline-flex items-center gap-1 rounded-md border border-[var(--app-danger-border,rgba(239,68,68,0.4))] bg-[var(--app-danger-bg,rgba(239,68,68,0.12))] px-2 py-0.5 text-[10.5px] font-medium text-[var(--app-danger)]">
          ✕ {stats.blocked} blocked
        </span>
      )}
    </div>
  )
}

export function AutomationUpcomingScheduleChart({
  records,
  timezone,
  onOpenSession,
  onChat,
  workspaceSlug,
}: {
  records: AutomationV2Record[]
  timezone: string
  onOpenSession?: (id: string) => void
  onChat?: (id: string) => void
  workspaceSlug?: string
}) {
  const now = Date.now()
  const upcomingEvents = useMemo(() => computeUpcomingAutomationEvents(records, now, 48 * 3600 * 1000, 8), [records, now])
  const activeCount = useMemo(() => records.filter((r) => r.enabled && !r.cancelled && !r.archived).length, [records])

  const timeFormatter = (ms: number) => {
    return new Intl.DateTimeFormat(undefined, {
      timeZone: timezone,
      hour: '2-digit',
      minute: '2-digit',
    }).format(ms)
  }

  const dayFormatter = (ms: number) => {
    const todayStr = getOccurrenceDayKey(now, timezone)
    const eventDayStr = getOccurrenceDayKey(ms, timezone)
    if (todayStr === eventDayStr) return 'Today'
    const tomorrowStr = getOccurrenceDayKey(now + 86400000, timezone)
    if (eventDayStr === tomorrowStr) return 'Tomorrow'
    return new Intl.DateTimeFormat(undefined, {
      timeZone: timezone,
      weekday: 'short',
      month: 'short',
      day: 'numeric',
    }).format(ms)
  }

  const dayBuckets = useMemo(() => {
    const buckets = [
      { id: 'night', label: 'Night', hours: '00:00 – 06:00', icon: '🌙', count: 0, titles: [] as string[] },
      { id: 'morning', label: 'Morning', hours: '06:00 – 12:00', icon: '🌅', count: 0, titles: [] as string[] },
      { id: 'afternoon', label: 'Afternoon', hours: '12:00 – 18:00', icon: '☀️', count: 0, titles: [] as string[] },
      { id: 'evening', label: 'Evening', hours: '18:00 – 24:00', icon: '🌆', count: 0, titles: [] as string[] },
    ]

    for (const ev of upcomingEvents) {
      try {
        const hourStr = new Intl.DateTimeFormat('en-US', { timeZone: timezone, hour: 'numeric', hour12: false }).format(ev.dueAt)
        const hour = parseInt(hourStr, 10) % 24
        let bIndex = 0
        if (hour >= 6 && hour < 12) bIndex = 1
        else if (hour >= 12 && hour < 18) bIndex = 2
        else if (hour >= 18 && hour < 24) bIndex = 3
        buckets[bIndex].count++
        if (!buckets[bIndex].titles.includes(ev.title)) {
          buckets[bIndex].titles.push(ev.title)
        }
      } catch {
        // fallback
      }
    }
    return buckets
  }, [upcomingEvents, timezone])

  if (activeCount === 0) {
    return (
      <section
        aria-label="Upcoming schedule and continuity"
        data-testid="automations-schedule-continuity"
        className="rounded-2xl border border-[var(--app-border)]/70 bg-[var(--app-surface-subtle)]/40 p-4"
      >
        <div className="flex items-center gap-2.5 text-xs text-[var(--app-text-muted)]">
          <Clock3 size={15} className="text-[var(--app-text-subtle)] shrink-0" />
          <p>
            No active schedules running. Deploy a worker or configure a new schedule to view upcoming schedule continuity.
          </p>
        </div>
      </section>
    )
  }

  return (
    <section
      aria-label="Upcoming schedule and continuity"
      data-testid="automations-schedule-continuity"
      className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4 sm:p-5 space-y-4 shadow-[0_1px_2px_color-mix(in_srgb,var(--app-text)_5%,transparent)]"
    >
      {/* Header */}
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-[var(--app-border)]/60 pb-3">
        <div className="flex items-center gap-2.5">
          <div className="flex size-7 items-center justify-center rounded-lg bg-[var(--app-primary-soft)] text-[var(--app-primary)]">
            <Calendar size={15} />
          </div>
          <div>
            <h3 className="text-sm font-semibold text-[var(--app-text)]">Upcoming Schedule &amp; Continuity</h3>
            <p className="text-[11px] text-[var(--app-text-muted)]">
              Scheduled timeline across active workspace workers · {timezone}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-2">
          <span className="rounded-full border border-[var(--app-primary-border)]/60 bg-[var(--app-primary-soft)] px-2.5 py-0.5 text-[11px] font-medium text-[var(--app-primary)]">
            {upcomingEvents.length} upcoming run{upcomingEvents.length === 1 ? '' : 's'}
          </span>
        </div>
      </div>

      {/* 24-Hour Horizon Visual Rhythm Bar */}
      <div className="space-y-2">
        <div className="flex items-center justify-between text-[11px] font-medium text-[var(--app-text-subtle)]">
          <span>24-Hour Schedule Rhythm</span>
          <span>{activeCount} active schedule{activeCount === 1 ? '' : 's'}</span>
        </div>
        <div className="grid grid-cols-2 sm:grid-cols-4 gap-2">
          {dayBuckets.map((bucket) => {
            const hasRuns = bucket.count > 0
            return (
              <div
                key={bucket.id}
                className={cn(
                  "rounded-xl border p-2.5 transition-colors",
                  hasRuns
                    ? 'border-[var(--app-primary-border)]/60 bg-[var(--app-primary-soft)]/25'
                    : 'border-[var(--app-border)]/50 bg-[var(--app-bg-alt)]/40'
                )}
                data-testid={`schedule-bucket-${bucket.id}`}
              >
                <div className="flex items-center justify-between gap-1 text-[11px]">
                  <span className="font-semibold text-[var(--app-text)] flex items-center gap-1">
                    <span>{bucket.icon}</span>
                    <span>{bucket.label}</span>
                  </span>
                  <span className={cn(
                    "text-[10px] font-mono px-1.5 py-0.5 rounded",
                    hasRuns ? 'bg-[var(--app-primary-soft)] text-[var(--app-primary)] font-bold' : 'text-[var(--app-text-subtle)]'
                  )}>
                    {bucket.count} run{bucket.count === 1 ? '' : 's'}
                  </span>
                </div>
                <div className="mt-1 text-[10px] text-[var(--app-text-subtle)] font-mono">
                  {bucket.hours}
                </div>
                {hasRuns && bucket.titles.length > 0 && (
                  <div className="mt-1.5 truncate text-[10px] text-[var(--app-text-muted)] font-medium" title={bucket.titles.join(', ')}>
                    {bucket.titles.join(', ')}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      </div>

      {/* Chronological Upcoming Queue */}
      {upcomingEvents.length > 0 && (
        <div className="space-y-2 pt-1">
          <div className="text-[11px] font-medium text-[var(--app-text-subtle)]">
            Upcoming Run Queue
          </div>
          <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-4" data-testid="upcoming-events-grid">
            {upcomingEvents.map((ev, index) => {
              const rel = formatRelativeTime(ev.dueAt, now)
              const timeDisplay = timeFormatter(ev.dueAt)
              const dayDisplay = dayFormatter(ev.dueAt)

              return (
                <div
                  key={`${ev.sessionId}-${ev.dueAt}-${index}`}
                  className="flex flex-col justify-between rounded-xl border border-[var(--app-border)]/70 bg-[var(--app-surface)] p-3 hover:border-[var(--app-primary-border)] hover:bg-[var(--app-surface-hover)] transition-all shadow-xs group"
                  data-testid="upcoming-event-item"
                >
                  <div>
                    <div className="flex items-center justify-between gap-1.5">
                      <span className="inline-flex items-center gap-1 rounded bg-[var(--app-primary-soft)] px-1.5 py-0.5 text-[10px] font-semibold text-[var(--app-primary)]">
                        <span className="size-1.5 rounded-full bg-[var(--app-primary)] animate-pulse" />
                        <span>{rel}</span>
                      </span>
                      <span className="text-[10px] font-mono text-[var(--app-text-muted)]">
                        {dayDisplay} {timeDisplay}
                      </span>
                    </div>
                    <h4 className="mt-2 text-xs font-semibold text-[var(--app-text)] truncate" title={ev.title}>
                      {ev.title}
                    </h4>
                    <p className="mt-0.5 text-[10px] text-[var(--app-text-muted)] truncate">
                      {ev.cadence}
                    </p>
                  </div>
                  <div className="mt-2.5 flex items-center justify-between border-t border-[var(--app-border)]/40 pt-1.5 text-[10px]">
                    <button
                      type="button"
                      className="text-[var(--app-primary)] hover:underline font-medium cursor-pointer"
                      onClick={() => onChat?.(ev.sessionId)}
                      title="Discuss this worker with Swarm"
                    >
                      Discuss
                    </button>
                    {workspaceSlug ? (
                      <a
                        href={`/${encodeURIComponent(workspaceSlug)}/${encodeURIComponent(ev.sessionId)}`}
                        onClick={(e) => {
                          if (onOpenSession) {
                            e.preventDefault()
                            onOpenSession(ev.sessionId)
                          }
                        }}
                        className="text-[var(--app-text-muted)] hover:text-[var(--app-text)] hover:underline flex items-center gap-0.5"
                        title="Open granular session"
                      >
                        <span>Session</span>
                        <ExternalLink size={9} />
                      </a>
                    ) : null}
                  </div>
                </div>
              )
            })}
          </div>
        </div>
      )}
    </section>
  )
}

export function PendingAutomationCard({
  proposal,
  workspaceId,
  isSelected,
  isExpanded,
  onToggleExpand,
  onAskForChanges,
  onOpenSession,
  workspaceSlug,
}: {
  proposal: AutomationV2Proposal
  workspaceId: string
  isSelected: boolean
  isExpanded: boolean
  onToggleExpand: () => void
  onAskForChanges: (sessionId: string) => void
  onOpenSession?: (sessionId: string) => void
  workspaceSlug?: string
}) {
  const [acceptBusy, setAcceptBusy] = useState(false)
  const [declineBusy, setDeclineBusy] = useState(false)
  const [acceptError, setAcceptError] = useState('')
  const [accepted, setAccepted] = useState<AutomationV2Record | null>(null)
  const [declined, setDeclined] = useState(false)

  const schedule = proposal.document.automation_v2.schedule
  const scheduleSettings = proposal.document.automation_v2

  const time = (ms: number) =>
    new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(ms)

  const handleAccept = async () => {
    if (acceptBusy || declineBusy || accepted || declined) return
    setAcceptBusy(true)
    setAcceptError('')
    try {
      const response = await desktopAutomationV2.mutate({
        action: 'accept_automation',
        workspace_id: workspaceId || proposal.workspace_id,
        session_id: proposal.session_id,
        review: automationV2Review(proposal),
      })
      if (!response.record || response.record.digest !== proposal.digest || response.record.session_id !== proposal.session_id || !response.record.automation_id) {
        throw new Error('Acceptance result unavailable. Refresh before retrying.')
      }
      setAccepted(response.record)
      desktopAutomationV2.invalidate(proposal.workspace_id, proposal.session_id)
    } catch (err) {
      setAcceptError(err instanceof Error ? err.message : 'Acceptance failed')
    } finally {
      setAcceptBusy(false)
    }
  }

  const handleDecline = async () => {
    if (declineBusy || acceptBusy || accepted || declined) return
    setDeclineBusy(true)
    setAcceptError('')
    try {
      await desktopAutomationV2.mutate({
        action: 'decline_automation',
        workspace_id: workspaceId || proposal.workspace_id,
        session_id: proposal.session_id,
        review: automationV2Review(proposal),
      })
      try {
        const resolved = await resolveSessionV3Permission(proposal.session_id, 'permission_' + proposal.proposal_id, {
          action: 'deny',
          reason: 'Declined by user',
        })
        const permRecord = normalizeDesktopPermission(resolved?.permission, proposal.session_id)
        dispatchDesktopV3Cache({
          type: 'permission.resolveResult',
          sessionId: proposal.session_id,
          permissionId: 'permission_' + proposal.proposal_id,
          permission: permRecord,
        })
      } catch {
        // Permission might already be resolved
      }
      setDeclined(true)
      desktopAutomationV2.invalidate(proposal.workspace_id, proposal.session_id)
    } catch (err) {
      setAcceptError(err instanceof Error ? err.message : 'Decline failed')
    } finally {
      setDeclineBusy(false)
    }
  }

  const handleReject = async () => {
    await handleDecline()
  }

  return (
    <article
      className={cn(
        "rounded-2xl border bg-[var(--app-surface)] p-5 transition-all shadow-[0_1px_2px_color-mix(in_srgb,var(--app-text)_5%,transparent)]",
        isSelected
          ? 'border-[var(--app-primary)] ring-1 ring-[var(--app-primary)]/40'
          : 'border-[var(--app-warning-border,rgba(245,158,11,0.35))] hover:border-[var(--app-warning)]'
      )}
      data-testid="automation-overview-card"
      data-pending-card="true"
      data-automation-id={proposal.proposal_id}
      data-session-id={proposal.session_id}
    >
      {/* Top row: Status pill, Cadence pill, Step count, and Next run / state */}
      <div className="flex flex-wrap items-center justify-between gap-2.5">
        <div className="flex flex-wrap items-center gap-2">
          <span
            className="inline-flex items-center gap-1.5 rounded-full border border-[var(--app-warning-border,rgba(245,158,11,0.3))] bg-[var(--app-warning-bg,rgba(245,158,11,0.12))] px-2.5 py-0.5 text-[11px] font-semibold text-[var(--app-warning)]"
            data-testid="automation-status-pending"
          >
            <Clock3 size={12} />
            <span>Pending</span>
          </span>
          <span className="inline-flex items-center gap-1 rounded-md border border-[var(--app-primary-border)]/45 bg-[var(--app-primary-soft)] px-2.5 py-0.5 text-[11px] font-medium text-[var(--app-primary)]">
            {scheduleFrequency(schedule)}
          </span>
          <span className="text-[11px] text-[var(--app-text-muted)]">
            {proposal.document.checkpoints.length} {proposal.document.checkpoints.length === 1 ? 'step' : 'steps'} · Rev {proposal.revision}
          </span>
        </div>
        <div className="text-xs font-medium text-[var(--app-warning)]">
          <span>Awaiting deployment</span>
        </div>
      </div>

      {/* Card Title & Expand trigger */}
      <div className="mt-3">
        <button
          type="button"
          className={cn(
            "flex w-full min-w-0 items-center justify-between gap-3 text-left transition-colors focus-visible:outline-none hover:text-[var(--app-primary)]",
            isSelected ? 'text-[var(--app-primary)]' : 'text-[var(--app-text)]'
          )}
          onClick={onToggleExpand}
          aria-current={isSelected ? 'page' : undefined}
          aria-expanded={isExpanded}
          title="Click to view details and review"
        >
          <div className="min-w-0">
            <span className="block truncate text-base font-semibold">{proposal.document.title}</span>
            <span className="sr-only"> · Pending · Worker proposal · revision {proposal.revision}</span>
          </div>
          <div className="flex shrink-0 items-center gap-1 text-xs text-[var(--app-text-muted)]">
            <span className="hidden sm:inline">{isExpanded ? 'Hide details' : 'View details'}</span>
            {isExpanded ? <ChevronDown size={15} /> : <ChevronRight size={15} />}
          </div>
        </button>
        <p className="mt-1.5 text-xs leading-relaxed text-[var(--app-text-muted)] line-clamp-2">
          {proposal.document.info.goal}
        </p>
      </div>

      {/* Schedule Metadata */}
      <div className="mt-3 flex flex-wrap items-center gap-2 text-xs text-[var(--app-text-subtle)]">
        <span className="font-mono text-[var(--app-text)] font-medium rounded-md border border-[var(--app-border)]/50 bg-[var(--app-bg-alt)] px-2 py-0.5 text-[11px]">
          {scheduleLabel(schedule)}
        </span>
        {schedule.timezone && <span>({schedule.timezone})</span>}
        <span>·</span>
        <span>
          {scheduleSettings.expiration?.kind === 'indefinite'
            ? 'Repeats indefinitely'
            : `Ends ${time(scheduleSettings.expiration?.expires_at!)}`}
        </span>
        <span>·</span>
        <span className="text-[var(--app-warning)] font-medium">Not yet activated</span>
      </div>

      {acceptError && (
        <div className="mt-2 text-xs text-[var(--app-danger)]" role="alert">
          {acceptError}
        </div>
      )}

      {accepted && (
        <div className="mt-3 rounded-xl border border-[var(--app-success-border,rgba(16,185,129,0.3))] bg-[var(--app-success-bg,rgba(16,185,129,0.12))] p-3 text-xs text-[var(--app-success)]">
          <p className="font-semibold">Worker accepted and deployed!</p>
          <p className="mt-0.5 text-[11px] text-[var(--app-text-muted)]">
            Worker is now deployed. First execution will follow the schedule.
          </p>
        </div>
      )}

      {declined && (
        <div className="mt-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-hover)] p-3 text-xs text-[var(--app-text-muted)]" data-testid="automation-declined-message">
          <p className="font-semibold text-[var(--app-text)]">Worker proposal declined</p>
          <p className="mt-0.5 text-[11px] text-[var(--app-text-muted)]">
            This pending worker has been declined and will not be scheduled.
          </p>
        </div>
      )}

      {/* Card Actions */}
      <div className="mt-4 flex flex-wrap items-center justify-between gap-2.5 border-t border-[var(--app-border)]/60 pt-3">
        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            variant="outline"
            className="h-8 gap-1.5 rounded-xl text-xs"
            onClick={() => onAskForChanges(proposal.session_id)}
            title="Ask the worker agent to make adjustments to this plan"
          >
            <MessageSquare size={13} className="text-[var(--app-primary)]" />
            <span>Ask the worker agent for any changes</span>
          </Button>
          {!accepted && !declined && (
            <>
              <Button
                size="sm"
                variant="outline"
                className="h-8 gap-1.5 rounded-xl text-xs text-[var(--app-text-muted)] hover:border-[var(--app-danger-border,rgba(239,68,68,0.4))] hover:bg-[var(--app-danger-bg,rgba(239,68,68,0.08))] hover:text-[var(--app-danger)]"
                disabled={declineBusy || acceptBusy}
                onClick={() => void handleDecline()}
                title="Decline this worker proposal"
                data-testid="decline-automation-button"
              >
                {declineBusy ? <LoaderCircle size={13} className="animate-spin" /> : <XCircle size={13} />}
                <span>Decline</span>
              </Button>
              <Button
                size="sm"
                className="h-8 gap-1.5 rounded-xl text-xs bg-[var(--app-primary)] text-white hover:bg-[var(--app-primary)]/90"
                disabled={acceptBusy || declineBusy}
                onClick={() => void handleAccept()}
                title="Deploy this worker"
                data-testid="accept-automation-button"
              >
                {acceptBusy ? <LoaderCircle size={13} className="animate-spin" /> : <CheckCircle2 size={13} />}
                <span>Deploy Worker</span>
              </Button>
            </>
          )}
        </div>
        <div className="flex items-center gap-2">
          {workspaceSlug ? (
            <a
              href={`/${encodeURIComponent(workspaceSlug)}/${encodeURIComponent(proposal.session_id)}`}
              onClick={(e) => {
                if (onOpenSession) {
                  e.preventDefault()
                  onOpenSession(proposal.session_id)
                }
              }}
              className="inline-flex items-center gap-1.5 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-hover)] px-2.5 py-1 text-xs font-medium text-[var(--app-text)] hover:border-[var(--app-primary-border)] hover:text-[var(--app-primary)] transition-colors"
              title="Open worker session"
            >
              <span>Open session</span>
              <ExternalLink size={11} className="opacity-70" />
            </a>
          ) : null}
          <Button
            size="sm"
            variant={isExpanded ? 'secondary' : 'outline'}
            className="h-8 gap-1.5 rounded-xl text-xs"
            onClick={onToggleExpand}
            aria-expanded={isExpanded}
          >
            <span>{isExpanded ? 'Hide details' : 'View details'}</span>
            {isExpanded ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
          </Button>
        </div>
      </div>

      {/* In-place expanded detail */}
      {isExpanded && (
        <div className="mt-4 border-t border-[var(--app-border)]/70 pt-4" data-testid="automation-pending-expanded-detail">
          <AutomationV2PlanReview
            key={`${proposal.proposal_id}-${proposal.revision}`}
            proposal={proposal}
            onReject={handleReject}
            rejectLabel="Decline"
            onAskForChanges={() => onAskForChanges(proposal.session_id)}
          />
        </div>
      )}
    </article>
  )
}

export function AutomationV2Workspace({
  workspaceId,
  workspacePath,
  workspaceName,
  workspaceBindingId,
  workspaceSlug,
  initialSessionId,
  onOpenSession,
}: {
  workspaceId: string
  workspacePath: string
  workspaceName: string
  workspaceBindingId?: string
  workspaceSlug?: string
  initialSessionId?: string
  onOpenSession?: (id: string) => void
}) {
  const [cursor, setCursor] = useState<string>()
  const [selected, setSelected] = useState(initialSessionId || '')
  const [expandedIds, setExpandedIds] = useState<Record<string, boolean>>(() => {
    if (initialSessionId) return { [initialSessionId]: true }
    return {}
  })
  const [session, setSession] = useState('')
  const [createRequest, setCreateRequest] = useState(0)
  const [draftPrompt, setDraftPrompt] = useState<string | undefined>()
  const [searchQuery, setSearchQuery] = useState('')
  const [statusFilter, setStatusFilter] = useState<'all' | 'pending' | 'enabled' | 'paused' | 'archived'>('all')
  const [actionLoadingId, setActionLoadingId] = useState<string | null>(null)
  const [deleteConfirmRecord, setDeleteConfirmRecord] = useState<AutomationV2Record | null>(null)
  const [deleteBusy, setDeleteBusy] = useState(false)

  const isArchivedTab = statusFilter === 'archived'
  const activeInput = useMemo(() => ({ action: 'list' as const, workspace_id: workspaceId, cursor: !isArchivedTab ? cursor : undefined }), [workspaceId, cursor, isArchivedTab])
  const activePage = useAutomationV2Page(activeInput)
  const activeRecords = activePage?.data?.records ?? []

  const archivedInput = useMemo(() => ({ action: 'list' as const, workspace_id: workspaceId, cursor: isArchivedTab ? cursor : undefined, archived_mode: 'only' as const }), [workspaceId, cursor, isArchivedTab])
  const archivedPage = useAutomationV2Page(archivedInput)
  const archivedRecords = archivedPage?.data?.records ?? []

  const page = isArchivedTab ? archivedPage : activePage
  const records = isArchivedTab ? archivedRecords : activeRecords

  const pendingProposals = useDesktopV3CacheSelector((state) =>
    selectPendingAutomationV2Proposals(state, workspaceId)
  )

  const pendingProposalBySessionId = useMemo(() => {
    const map = new Map<string, AutomationV2Proposal>()
    for (const p of pendingProposals) {
      map.set(p.session_id, p)
    }
    return map
  }, [pendingProposals])

  const newPendingProposals = useMemo(() => {
    return pendingProposals.filter((p) => !records.some((r) => r.session_id === p.session_id))
  }, [pendingProposals, records])

  const pendingCount = pendingProposals.length

  // Reconcile any unhydrated sessions in this workspace with pending approvals
  const unhydratedPendingSessionIds = useDesktopV3CacheSelector((state) => {
    const ids: string[] = []
    for (const [id, summary] of Object.entries(state.permissionSummaryBySessionId ?? {})) {
      if ((summary?.pendingApprovalCount ?? 0) <= 0) continue
      const perms = state.permissionsBySession[id]
      if (perms !== undefined && perms.length > 0) continue
      const rec = state.sessionsById[id]
      if (rec?.kind === 'full') {
        const belongs =
          rec.session.automation_v2?.workspace_id === workspaceId ||
          rec.session.automation?.workspace_id === workspaceId ||
          rec.session.workspace_grants?.some((g) => g.workspace_id === workspaceId) ||
          rec.session.metadata?.swarm_v3_purpose_workspace_id === workspaceId ||
          rec.session.metadata?.swarm_v3_session_purpose === 'automation_management' ||
          (Boolean(workspacePath) && rec.session.workspace_path === workspacePath)
        if (belongs) {
          ids.push(id)
        }
      } else {
        ids.push(id)
      }
    }
    for (const r of activeRecords) {
      if ((state.permissionSummaryBySessionId[r.session_id]?.pendingApprovalCount ?? 0) > 0) {
        if (!state.permissionsBySession[r.session_id] && !ids.includes(r.session_id)) {
          ids.push(r.session_id)
        }
      }
    }
    for (const r of archivedRecords) {
      if ((state.permissionSummaryBySessionId[r.session_id]?.pendingApprovalCount ?? 0) > 0) {
        if (!state.permissionsBySession[r.session_id] && !ids.includes(r.session_id)) {
          ids.push(r.session_id)
        }
      }
    }
    return ids
  })

  useEffect(() => {
    for (const id of unhydratedPendingSessionIds) {
      void desktopAutomationV2.reconcileSession(id).catch(() => {})
    }
  }, [unhydratedPendingSessionIds])

  // Discover and reconcile automation management conversations
  useEffect(() => {
    let cancelled = false
    void loadAutomationConversations(workspaceId, workspacePath).then((page) => {
      if (cancelled) return
      for (const id of page.session_order) {
        const snapshot = getDesktopV3CacheSnapshot()
        const perms = snapshot.permissionsBySession[id]
        if (!perms || perms.length === 0) {
          const summary = snapshot.permissionSummaryBySessionId[id]
          const session = page.sessions_by_id[id]
          if ((summary?.pendingApprovalCount ?? 0) > 0 || session?.metadata?.swarm_v3_session_purpose === 'automation_management') {
            void desktopAutomationV2.reconcileSession(id).catch(() => {})
          }
        }
      }
    }).catch(() => {})
    return () => {
      cancelled = true
    }
  }, [workspaceId, workspacePath])

  useEffect(() => {
    if (initialSessionId) {
      setSelected(initialSessionId)
      setExpandedIds((prev) => ({ ...prev, [initialSessionId]: true }))
    }
  }, [initialSessionId])

  const selectedRecord = useMemo(() => {
    if (!selected) return null
    return records.find((r) => r.session_id === selected) ?? null
  }, [records, selected])

  const toggleExpanded = (sessionId: string) => {
    setExpandedIds((prev) => {
      const next = !prev[sessionId]
      if (next) {
        setSelected(sessionId)
      }
      return { ...prev, [sessionId]: next }
    })
  }

  const handleChatWithAutomation = (sessionId: string) => {
    setSelected(sessionId)
    setSession(sessionId)
  }

  const handleDiscussAll = () => {
    setSelected('')
    setSession('')
  }

  const handleAskForChanges = (sessionId: string) => {
    setSelected(sessionId)
    setSession(sessionId)
    setTimeout(() => {
      const textarea = document.querySelector<HTMLTextAreaElement>('[data-testid="desktop-plan-composer"] textarea')
      if (textarea) {
        textarea.focus()
      }
    }, 50)
  }

  const handleUseTemplate = (prompt: string) => {
    setDraftPrompt(prompt)
    setSelected('')
    setSession('')
    setCreateRequest((n) => n + 1)
  }

  const handleControlRecord = async (record: AutomationV2Record, action: 'pause' | 'resume') => {
    try {
      await desktopAutomationV2.mutate({
        workspace_id: workspaceId,
        session_id: record.session_id,
        generation: record.generation,
        action,
      } as AutomationV2Mutation)
    } catch {
      // Handled via cache update
    }
  }

  const handleArchiveRecord = async (record: AutomationV2Record) => {
    setActionLoadingId(record.session_id)
    try {
      if (record.enabled && !record.cancelled) {
        try {
          await desktopAutomationV2.mutate({
            workspace_id: workspaceId,
            session_id: record.session_id,
            generation: record.generation,
            action: 'pause',
          } as AutomationV2Mutation)
        } catch {
          // ignore
        }
      }
      await archiveDesktopV3Sessions([record.session_id])
      desktopAutomationV2.invalidate(workspaceId)
    } catch (err) {
      console.error('Failed to archive automation', err)
    } finally {
      setActionLoadingId(null)
    }
  }

  const handleUnarchiveRecord = async (record: AutomationV2Record) => {
    setActionLoadingId(record.session_id)
    try {
      const version = record.archived_at || getDesktopV3CacheSnapshot().tombstonesBySession[record.session_id]?.updated_at || Date.now()
      await unarchiveDesktopV3ReviewSessions({ [record.session_id]: version })
      desktopAutomationV2.invalidate(workspaceId)
    } catch (err) {
      console.error('Failed to unarchive automation', err)
    } finally {
      setActionLoadingId(null)
    }
  }

  const handleDeleteRecord = (record: AutomationV2Record) => {
    setDeleteConfirmRecord(record)
  }

  const confirmDeleteRecord = async () => {
    if (!deleteConfirmRecord || deleteBusy) return
    const rec = deleteConfirmRecord
    setDeleteBusy(true)
    try {
      if (!rec.cancelled) {
        try {
          await desktopAutomationV2.mutate({
            workspace_id: workspaceId,
            session_id: rec.session_id,
            generation: rec.generation,
            action: 'cancel_all',
          } as AutomationV2Mutation)
        } catch {
          // ignore
        }
      }
      const preview = await deleteDesktopSessions({ session_ids: [rec.session_id], archived_mode: 'include', global: true, dry_run: true })
      await deleteDesktopSessions({
        session_ids: [rec.session_id],
        archived_mode: 'include',
        global: true,
        confirmation_token: preview.confirmation_token,
        confirm_recent: preview.recent_75_overlap_count > 0,
      })
      desktopAutomationV2.invalidate(workspaceId)
      setDeleteConfirmRecord(null)
    } catch (err) {
      console.error('Failed to delete automation', err)
    } finally {
      setDeleteBusy(false)
    }
  }

  const filteredPendingProposals = useMemo(() => {
    if (isArchivedTab || statusFilter === 'enabled' || statusFilter === 'paused') return []
    return newPendingProposals.filter((p) => {
      if (searchQuery.trim()) {
        const query = searchQuery.trim().toLowerCase()
        const titleMatch = (p.document.title || '').toLowerCase().includes(query)
        const goalMatch = (p.document.info?.goal || '').toLowerCase().includes(query)
        return titleMatch || goalMatch
      }
      return true
    })
  }, [isArchivedTab, statusFilter, newPendingProposals, searchQuery])

  const filteredRecords = useMemo(() => {
    const list = isArchivedTab ? archivedRecords : activeRecords
    return list.filter((r) => {
      if (!isArchivedTab) {
        if (statusFilter === 'pending') {
          return pendingProposalBySessionId.has(r.session_id)
        }
        if (statusFilter === 'enabled' && (!r.enabled || r.cancelled)) return false
        if (statusFilter === 'paused' && (r.enabled || r.cancelled)) return false
      }
      if (searchQuery.trim()) {
        const query = searchQuery.trim().toLowerCase()
        const titleMatch = r.document.title.toLowerCase().includes(query)
        const goalMatch = r.document.info.goal.toLowerCase().includes(query)
        return titleMatch || goalMatch
      }
      return true
    })
  }, [isArchivedTab, archivedRecords, activeRecords, statusFilter, searchQuery, pendingProposalBySessionId])

  const enabledCount = useMemo(() => activeRecords.filter((r) => r.enabled && !r.cancelled).length, [activeRecords])
  const pausedCount = useMemo(() => activeRecords.filter((r) => !r.enabled && !r.cancelled).length, [activeRecords])
  const archivedCount = useMemo(() => archivedRecords.length, [archivedRecords])

  const headingTitle = statusFilter === 'pending'
    ? 'Pending worker proposals'
    : statusFilter === 'archived'
      ? 'Archived workers'
      : statusFilter === 'enabled'
        ? 'Active deployed workers'
        : statusFilter === 'paused'
          ? 'Paused workers'
          : pendingCount > 0
            ? 'Workspace workers'
            : 'Deployed workers'

  const headingSubtitle = statusFilter === 'pending'
    ? 'Review and deploy pending worker proposals, or ask the worker agent for changes.'
    : statusFilter === 'archived'
      ? 'Paused and archived workers for this workspace.'
      : 'Flat overview of all deployed and scheduled workspace workers.'

  const hasAnyItems = (filteredRecords.length + filteredPendingProposals.length) > 0

  const time = (ms: number) =>
    new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(ms)

  return (
    <div className="flex h-full min-h-0 min-w-0 flex-1 flex-col bg-[var(--app-bg)] text-sm text-[var(--app-text)]">
      {/* Top Header */}
      <header className="flex min-h-[60px] flex-wrap items-center justify-between gap-3 border-b border-[var(--app-border)] px-5 py-3">
        <div className="flex min-w-0 items-center gap-3">
          <RefreshCcw size={16} className="text-[var(--app-primary)]" />
          <h1 className="font-semibold">Workers</h1>
          <span className="truncate text-xs text-[var(--app-text-muted)]">{workspaceName}</span>
        </div>
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            variant="outline"
            className="gap-1.5 text-xs rounded-xl"
            onClick={handleDiscussAll}
            title="Discuss all workers with the assistant"
          >
            <MessageSquare size={13} className="text-[var(--app-primary)]" />
            <span>Discuss with Swarm</span>
          </Button>
          <Button size="sm" onClick={() => setCreateRequest((n) => n + 1)}>
            <Plus size={15} />
            Deploy a Worker
          </Button>
        </div>
      </header>

      {/* Main Body + Sidecar Layout */}
      <div className="flex min-h-0 min-w-0 flex-1 flex-col xl:flex-row overflow-hidden">
        <main className="min-w-0 flex-1 space-y-6 p-5 sm:p-8 overflow-y-auto">
          {/* Overview Heading & Refresh */}
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div>
              <h2 className="text-lg font-semibold">{headingTitle}</h2>
              <p className="mt-0.5 text-xs text-[var(--app-text-muted)]">
                {headingSubtitle}
              </p>
            </div>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                void desktopAutomationV2.refresh(activeInput)
                void desktopAutomationV2.refresh(archivedInput)
                for (const id of unhydratedPendingSessionIds) {
                  void desktopAutomationV2.reconcileSession(id).catch(() => {})
                }
                void loadAutomationConversations(workspaceId, workspacePath).then((p) => {
                  for (const id of p.session_order) {
                    void desktopAutomationV2.reconcileSession(id).catch(() => {})
                  }
                }).catch(() => {})
              }}
            >
              <RefreshCcw size={13} />
              Refresh workers
            </Button>
          </div>

          {/* Status Messages */}
          {(!page || page.loading || page.stale) && (
            <p role="status" className="text-xs text-[var(--app-text-muted)]">
              {page?.stale && page.data ? 'Updating worker state…' : 'Loading workers…'}
            </p>
          )}
          {page?.error && <p role="alert" className="text-xs text-[var(--app-danger)]">{page.error}</p>}

          {/* Executive Pulse / Metrics Strip */}
          {(activeRecords.length > 0 || archivedRecords.length > 0 || pendingProposals.length > 0) && (
            <div className="grid gap-2.5 grid-cols-2 sm:grid-cols-5" data-testid="automations-summary-strip">
              <div className="rounded-xl border border-[var(--app-border)]/70 bg-[var(--app-surface)] p-3">
                <div className="text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-muted)]">Total</div>
                <div className="mt-1 text-lg font-semibold text-[var(--app-text)]">{activeRecords.length + newPendingProposals.length}</div>
              </div>
              <div className={cn(
                "rounded-xl border p-3",
                pendingCount > 0
                  ? "border-[var(--app-warning-border,rgba(245,158,11,0.4))] bg-[var(--app-warning-bg,rgba(245,158,11,0.08))]"
                  : "border-[var(--app-border)]/70 bg-[var(--app-surface)]"
              )} data-testid="summary-strip-pending">
                <div className={cn(
                  "text-[10px] font-semibold uppercase tracking-wider",
                  pendingCount > 0 ? "text-[var(--app-warning)]" : "text-[var(--app-text-muted)]"
                )}>Pending</div>
                <div className={cn(
                  "mt-1 text-lg font-semibold",
                  pendingCount > 0 ? "text-[var(--app-warning)]" : "text-[var(--app-text)]"
                )}>{pendingCount}</div>
              </div>
              <div className="rounded-xl border border-[var(--app-border)]/70 bg-[var(--app-surface)] p-3">
                <div className="text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-muted)]">Scheduled</div>
                <div className="mt-1 text-lg font-semibold text-[var(--app-success)]">{enabledCount}</div>
              </div>
              <div className="rounded-xl border border-[var(--app-border)]/70 bg-[var(--app-surface)] p-3">
                <div className="text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-muted)]">Paused</div>
                <div className="mt-1 text-lg font-semibold text-[var(--app-text-muted)]">{pausedCount}</div>
              </div>
              <div className="rounded-xl border border-[var(--app-border)]/70 bg-[var(--app-surface)] p-3">
                <div className="text-[10px] font-semibold uppercase tracking-wider text-[var(--app-text-muted)]">Archived</div>
                <div className="mt-1 text-lg font-semibold text-[var(--app-text-muted)]">{archivedCount}</div>
              </div>
            </div>
          )}

          {/* Upcoming Schedule & Continuity Timeline/Chart */}
          {activeRecords.length > 0 && (
            <AutomationUpcomingScheduleChart
              records={activeRecords}
              timezone={Intl.DateTimeFormat().resolvedOptions().timeZone}
              onOpenSession={onOpenSession}
              onChat={handleChatWithAutomation}
              workspaceSlug={workspaceSlug}
            />
          )}

          {/* Search & Status Filter Controls */}
          {(activeRecords.length > 0 || archivedRecords.length > 0 || pendingProposals.length > 0) && (
            <div className="flex flex-wrap items-center justify-between gap-2 pt-1">
              <div className="relative min-w-[200px] flex-1 max-w-sm">
                <Search size={13} className="pointer-events-none absolute left-2.5 top-1/2 -translate-y-1/2 text-[var(--app-text-muted)]" />
                <input
                  type="text"
                  placeholder="Filter workers by title or goal…"
                  value={searchQuery}
                  onChange={(e) => setSearchQuery(e.target.value)}
                  className="h-8 w-full rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] pl-8 pr-3 text-xs text-[var(--app-text)] outline-none focus:border-[var(--app-primary)]"
                />
              </div>
              <div className="flex items-center gap-1 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-0.5 text-xs">
                <button
                  type="button"
                  data-testid="filter-all"
                  className={cn(
                    "px-2.5 py-1 rounded-md text-xs font-medium transition-colors",
                    statusFilter === 'all'
                      ? 'bg-[var(--app-surface-hover)] text-[var(--app-text)] font-semibold'
                      : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
                  )}
                  onClick={() => setStatusFilter('all')}
                >
                  All ({activeRecords.length + newPendingProposals.length})
                </button>
                <button
                  type="button"
                  data-testid="filter-pending"
                  className={cn(
                    "px-2.5 py-1 rounded-md text-xs font-medium transition-colors",
                    statusFilter === 'pending'
                      ? 'bg-[var(--app-surface-hover)] text-[var(--app-warning)] font-semibold'
                      : pendingCount > 0
                        ? 'text-[var(--app-warning)] hover:text-[var(--app-warning)]/80'
                        : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
                  )}
                  onClick={() => setStatusFilter('pending')}
                >
                  Pending ({pendingCount})
                </button>
                <button
                  type="button"
                  data-testid="filter-enabled"
                  className={cn(
                    "px-2.5 py-1 rounded-md text-xs font-medium transition-colors",
                    statusFilter === 'enabled'
                      ? 'bg-[var(--app-surface-hover)] text-[var(--app-text)] font-semibold'
                      : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
                  )}
                  onClick={() => setStatusFilter('enabled')}
                >
                  Active ({enabledCount})
                </button>
                <button
                  type="button"
                  data-testid="filter-paused"
                  className={cn(
                    "px-2.5 py-1 rounded-md text-xs font-medium transition-colors",
                    statusFilter === 'paused'
                      ? 'bg-[var(--app-surface-hover)] text-[var(--app-text)] font-semibold'
                      : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
                  )}
                  onClick={() => setStatusFilter('paused')}
                >
                  Paused ({pausedCount})
                </button>
                <button
                  type="button"
                  data-testid="filter-archived"
                  className={cn(
                    "px-2.5 py-1 rounded-md text-xs font-medium transition-colors",
                    statusFilter === 'archived'
                      ? 'bg-[var(--app-surface-hover)] text-[var(--app-text)] font-semibold'
                      : 'text-[var(--app-text-muted)] hover:text-[var(--app-text)]'
                  )}
                  onClick={() => setStatusFilter('archived')}
                >
                  Archived ({archivedCount})
                </button>
              </div>
            </div>
          )}

          {/* Flat Overview of All Automations */}
          <div className="space-y-4" data-testid="automations-flat-overview">
            {filteredPendingProposals.map((proposal) => {
              const isSelected = selected === proposal.session_id
              const isExpanded = Boolean(expandedIds[proposal.session_id])
              return (
                <PendingAutomationCard
                  key={`pending-${proposal.proposal_id}-${proposal.revision}`}
                  proposal={proposal}
                  workspaceId={workspaceId}
                  isSelected={isSelected}
                  isExpanded={isExpanded}
                  onToggleExpand={() => toggleExpanded(proposal.session_id)}
                  onAskForChanges={handleAskForChanges}
                  onOpenSession={onOpenSession}
                  workspaceSlug={workspaceSlug}
                />
              )
            })}
            {filteredRecords.map((record) => {
              const schedule = record.document.automation_v2.schedule
              const isSelected = selected === record.session_id
              const isExpanded = Boolean(expandedIds[record.session_id])
              const isArchived = Boolean(record.archived)
              const pendingProposal = pendingProposalBySessionId.get(record.session_id)
              const hasPendingReview = Boolean(pendingProposal)
              const statusText = isArchived
                ? 'Archived'
                : record.cancelled
                  ? 'Cancelled'
                  : hasPendingReview
                    ? 'Pending review'
                    : record.enabled
                      ? 'Enabled'
                      : 'Paused'

              return (
                <article
                  key={record.automation_id}
                  className={cn(
                    "rounded-2xl border bg-[var(--app-surface)] p-5 transition-all shadow-[0_1px_2px_color-mix(in_srgb,var(--app-text)_5%,transparent)]",
                    isSelected
                      ? 'border-[var(--app-primary)] ring-1 ring-[var(--app-primary)]/40'
                      : 'border-[var(--app-border)]/80 hover:border-[var(--app-border-strong)]'
                  )}
                  data-testid="automation-overview-card"
                  data-automation-id={record.automation_id}
                >
                  {/* Top row: Status pill, Cadence pill, Step count, and Next run */}
                  <div className="flex flex-wrap items-center justify-between gap-2.5">
                    <div className="flex flex-wrap items-center gap-2">
                      <span
                        className={cn(
                          "inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-[11px] font-semibold",
                          isArchived
                            ? 'bg-[var(--app-surface-hover)] border border-[var(--app-border)] text-[var(--app-text-muted)]'
                            : record.cancelled
                            ? 'bg-[var(--app-danger-bg,rgba(239,68,68,0.12))] border border-[var(--app-danger-border,rgba(239,68,68,0.25))] text-[var(--app-danger)]'
                            : hasPendingReview
                            ? 'bg-[var(--app-warning-bg,rgba(245,158,11,0.12))] border border-[var(--app-warning-border,rgba(245,158,11,0.25))] text-[var(--app-warning)]'
                            : record.enabled
                            ? 'bg-[var(--app-success-bg,rgba(16,185,129,0.12))] border border-[var(--app-success-border,rgba(16,185,129,0.25))] text-[var(--app-success)]'
                            : 'bg-[var(--app-surface-hover)] border border-[var(--app-border)] text-[var(--app-text-muted)]'
                        )}
                      >
                        {isArchived ? (
                          <Archive size={12} />
                        ) : record.cancelled ? (
                          <AlertCircle size={12} />
                        ) : hasPendingReview ? (
                          <Clock3 size={12} />
                        ) : record.enabled ? (
                          <Clock3 size={12} />
                        ) : (
                          <Pause size={12} />
                        )}
                        <span>{statusText}</span>
                      </span>
                      {pendingProposal && (
                        <span className="inline-flex items-center gap-1 rounded-md border border-[var(--app-warning-border,rgba(245,158,11,0.3))] bg-[var(--app-warning-bg,rgba(245,158,11,0.12))] px-2 py-0.5 text-[10.5px] font-medium text-[var(--app-warning)]">
                          Revision {pendingProposal.revision} pending approval
                        </span>
                      )}
                      <span className="inline-flex items-center gap-1 rounded-md border border-[var(--app-primary-border)]/45 bg-[var(--app-primary-soft)] px-2.5 py-0.5 text-[11px] font-medium text-[var(--app-primary)]">
                        {scheduleFrequency(schedule)}
                      </span>
                      <span className="text-[11px] text-[var(--app-text-muted)]">
                        {record.document.checkpoints.length} {record.document.checkpoints.length === 1 ? 'step' : 'steps'} · Rev {record.revision}
                      </span>
                    </div>
                    <div className="text-xs font-medium text-[var(--app-text-muted)]">
                      {isArchived ? (
                        <span>Archived</span>
                      ) : record.enabled && !record.cancelled && record.next_due_at ? (
                        <span>Next: {time(record.next_due_at)}</span>
                      ) : (
                        <span>No upcoming run</span>
                      )}
                    </div>
                  </div>

                  {/* Card Title & Expand trigger */}
                  <div className="mt-3">
                    <button
                      type="button"
                      className={cn(
                        "flex w-full min-w-0 items-center justify-between gap-3 text-left transition-colors focus-visible:outline-none hover:text-[var(--app-primary)]",
                        isSelected ? 'text-[var(--app-primary)]' : 'text-[var(--app-text)]'
                      )}
                      onClick={() => toggleExpanded(record.session_id)}
                      aria-current={isSelected ? 'page' : undefined}
                      aria-expanded={isExpanded}
                      title="Click to view details and runs"
                    >
                      <div className="min-w-0">
                        <span className="block truncate text-base font-semibold">{record.document.title}</span>
                        <span className="sr-only"> · {statusText} · Worker · revision {record.revision}</span>
                      </div>
                      <div className="flex shrink-0 items-center gap-1 text-xs text-[var(--app-text-muted)]">
                        <span className="hidden sm:inline">{isExpanded ? 'Hide details' : 'View details'}</span>
                        {isExpanded ? <ChevronDown size={15} /> : <ChevronRight size={15} />}
                      </div>
                    </button>
                    <p className="mt-1.5 text-xs leading-relaxed text-[var(--app-text-muted)] line-clamp-2">
                      {record.document.info.goal}
                    </p>
                  </div>

                  {/* Schedule Metadata */}
                  <div className="mt-3 flex flex-wrap items-center gap-2 text-xs text-[var(--app-text-subtle)]">
                    <span className="font-mono text-[var(--app-text)] font-medium rounded-md border border-[var(--app-border)]/50 bg-[var(--app-bg-alt)] px-2 py-0.5 text-[11px]">
                      {scheduleLabel(schedule)}
                    </span>
                    {schedule.timezone && <span>({schedule.timezone})</span>}
                    <span>·</span>
                    <span>
                      {record.authorization.kind === 'indefinite'
                        ? 'Repeats indefinitely'
                        : `Ends ${time(record.authorization.expires_at!)}`}
                    </span>
                  </div>

                  {/* Today's Pulse Summary */}
                  <div className="mt-3">
                    <AutomationCardPulse
                      workspaceId={workspaceId}
                      sessionId={record.session_id}
                      enabled={record.enabled}
                      cancelled={record.cancelled}
                      nextDueAt={record.next_due_at}
                    />
                  </div>

                  {/* Card Actions */}
                  <div className="mt-4 flex flex-wrap items-center justify-between gap-2.5 border-t border-[var(--app-border)]/60 pt-3">
                    <div className="flex flex-wrap items-center gap-2">
                      {isArchived ? (
                        <>
                          <Button
                            size="sm"
                            variant="outline"
                            className="h-8 gap-1.5 rounded-xl text-xs text-[var(--app-primary)] border-[var(--app-primary-border)] hover:bg-[var(--app-primary-soft)]"
                            disabled={actionLoadingId === record.session_id}
                            onClick={() => void handleUnarchiveRecord(record)}
                            title="Unarchive worker"
                          >
                            {actionLoadingId === record.session_id ? <LoaderCircle size={13} className="animate-spin" /> : <ArchiveRestore size={13} />}
                            <span>Unarchive</span>
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            className="h-8 gap-1 rounded-xl text-xs text-[var(--app-text-muted)] hover:text-[var(--app-danger)]"
                            onClick={() => handleDeleteRecord(record)}
                            title="Permanently delete worker"
                          >
                            <Trash2 size={13} />
                            <span>Delete</span>
                          </Button>
                        </>
                      ) : (
                        <>
                          {hasPendingReview ? (
                            <Button
                              size="sm"
                              variant="outline"
                              className="h-8 gap-1.5 rounded-xl text-xs text-[var(--app-warning)] border-[var(--app-warning-border)] hover:bg-[var(--app-warning-soft)]"
                              onClick={() => handleAskForChanges(record.session_id)}
                              title="Ask the worker agent to make adjustments to this revision"
                            >
                              <MessageSquare size={13} />
                              <span>Ask the worker agent for any changes</span>
                            </Button>
                          ) : (
                            <Button
                              size="sm"
                              variant="outline"
                              className="h-8 gap-1.5 rounded-xl text-xs"
                              onClick={() => handleChatWithAutomation(record.session_id)}
                              title="Discuss or optimize this worker with Swarm"
                            >
                              <MessageSquare size={13} />
                              <span>Discuss with Swarm</span>
                            </Button>
                          )}
                          <Button
                            size="sm"
                            variant="ghost"
                            className="h-8 text-xs text-[var(--app-text-muted)] hover:text-[var(--app-text)]"
                            disabled={record.cancelled}
                            onClick={() => void handleControlRecord(record, record.enabled ? 'pause' : 'resume')}
                          >
                            {record.enabled ? 'Pause' : 'Resume'}
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            className="h-8 gap-1 rounded-xl text-xs text-[var(--app-text-muted)] hover:text-[var(--app-text)]"
                            disabled={actionLoadingId === record.session_id}
                            onClick={() => void handleArchiveRecord(record)}
                            title="Archive worker"
                          >
                            {actionLoadingId === record.session_id ? <LoaderCircle size={13} className="animate-spin" /> : <Archive size={13} />}
                            <span>Archive</span>
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            className="h-8 gap-1 rounded-xl text-xs text-[var(--app-text-muted)] hover:text-[var(--app-danger)]"
                            onClick={() => handleDeleteRecord(record)}
                            title="Permanently delete worker"
                          >
                            <Trash2 size={13} />
                            <span>Delete</span>
                          </Button>
                        </>
                      )}
                    </div>
                    <div className="flex items-center gap-2">
                      {workspaceSlug ? (
                        <a
                          href={`/${encodeURIComponent(workspaceSlug)}/${encodeURIComponent(record.session_id)}`}
                          onClick={(e) => {
                            if (onOpenSession) {
                              e.preventDefault()
                              onOpenSession(record.session_id)
                            }
                          }}
                          className="inline-flex items-center gap-1.5 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-hover)] px-2.5 py-1 text-xs font-medium text-[var(--app-text)] hover:border-[var(--app-primary-border)] hover:text-[var(--app-primary)] transition-colors"
                          title="Open dedicated worker session for full run history and chat"
                          data-testid="open-automation-session-link"
                        >
                          <span>Open session</span>
                          <ExternalLink size={11} className="opacity-70" />
                        </a>
                      ) : null}
                      <Button
                        size="sm"
                        variant={isExpanded ? 'secondary' : 'outline'}
                        className="h-8 gap-1.5 rounded-xl text-xs"
                        onClick={() => toggleExpanded(record.session_id)}
                        aria-expanded={isExpanded}
                      >
                        <span>{isExpanded ? 'Hide details' : 'View details'}</span>
                        {isExpanded ? <ChevronDown size={13} /> : <ChevronRight size={13} />}
                      </Button>
                    </div>
                  </div>

                  {/* In-place expanded detail */}
                  {isExpanded && (
                    <div className="mt-4 border-t border-[var(--app-border)]/70 pt-4 space-y-4">
                      {pendingProposal && (
                        <div className="rounded-xl border border-[var(--app-warning-border,rgba(245,158,11,0.4))] bg-[var(--app-warning-bg,rgba(245,158,11,0.06))] p-4">
                          <div className="flex items-center justify-between gap-2 mb-3">
                            <div className="flex items-center gap-2">
                              <Clock3 size={15} className="text-[var(--app-warning)]" />
                              <span className="text-xs font-semibold text-[var(--app-text)]">
                                Pending Revision {pendingProposal.revision} Review
                              </span>
                            </div>
                            <span className="text-[11px] text-[var(--app-text-muted)]">Awaiting your approval</span>
                          </div>
                          <AutomationV2PlanReview
                            key={`${pendingProposal.proposal_id}-${pendingProposal.revision}`}
                            proposal={pendingProposal}
                            onAskForChanges={() => handleAskForChanges(record.session_id)}
                          />
                        </div>
                      )}
                      <AutomationV2Detail
                        key={record.session_id}
                        workspaceId={workspaceId}
                        sessionId={record.session_id}
                        onChat={handleChatWithAutomation}
                        workspaceSlug={workspaceSlug}
                      />
                    </div>
                  )}
                </article>
              )
            })}
          </div>

          {/* Empty State */}
          {page?.data && !page.loading && !page.stale && !hasAnyItems && (
            <div className="rounded-2xl border border-dashed border-[var(--app-border)] p-8 text-center bg-[var(--app-surface)] space-y-3">
              <div className="mx-auto flex size-12 items-center justify-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-primary)]">
                {isArchivedTab ? <Archive size={24} /> : <Clock3 size={24} />}
              </div>
              <h3 className="text-base font-semibold text-[var(--app-text)]">
                {isArchivedTab
                  ? 'No archived workers'
                  : statusFilter === 'pending'
                    ? 'No pending worker proposals'
                    : 'No deployed workers on this page'}
              </h3>
              <p className="mx-auto max-w-md text-xs text-[var(--app-text-muted)] leading-relaxed">
                {isArchivedTab
                  ? 'Archived workers will appear here. Archiving a worker pauses its schedule and moves it out of your active workspace views.'
                  : statusFilter === 'pending'
                    ? 'No worker proposals are currently awaiting review. Ask the Worker Agent in the sidebar to design one, or pick a starter template below.'
                    : 'No deployed workers on this page. Start a conversation to propose one, or pick a starter template below.'}
              </p>
              {!isArchivedTab && (
                <Button size="sm" onClick={() => setCreateRequest((n) => n + 1)}>
                  <Plus size={15} />
                  Deploy a Worker
                </Button>
              )}
            </div>
          )}

          {/* Pagination */}
          <div className="flex gap-2">
            {cursor && (
              <Button variant="outline" size="sm" onClick={() => setCursor(undefined)}>
                First page
              </Button>
            )}
            {page?.data?.next_cursor && (
              <Button
                variant="outline"
                size="sm"
                disabled={page.loading || page.stale}
                onClick={() => setCursor(page.data?.next_cursor)}
              >
                More workers
              </Button>
            )}
          </div>

          {/* Consider Adding New Automations Section */}
          <section aria-label="Deploy a Worker" className="mt-8 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)]/70 p-5 sm:p-6 space-y-4">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div>
                <div className="flex items-center gap-2">
                  <Sparkles size={16} className="text-[var(--app-primary)]" />
                  <h2 className="text-sm font-semibold text-[var(--app-text)]">Deploy a Worker</h2>
                </div>
                <p className="mt-1 text-xs text-[var(--app-text-muted)]">
                  Choose a worker routine below to draft with the assistant, or ask Swarm for any custom deployed worker.
                </p>
              </div>
              <Button
                size="sm"
                variant="outline"
                className="gap-1.5 text-xs rounded-xl"
                onClick={() => handleUseTemplate('Help me design and deploy a custom worker for this workspace.')}
              >
                <Plus size={13} />
                <span>Custom worker</span>
              </Button>
            </div>

            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
              {AUTOMATION_STARTER_TEMPLATES.map((tpl) => (
                <div
                  key={tpl.id}
                  className="flex flex-col justify-between rounded-xl border border-[var(--app-border)]/70 bg-[var(--app-surface)] p-4 transition-all hover:border-[var(--app-border-strong)] hover:shadow-sm"
                >
                  <div>
                    <div className="flex items-center justify-between gap-2">
                      <div className="flex size-7 items-center justify-center rounded-lg bg-[var(--app-primary-soft)] text-[var(--app-primary)]">
                        <TemplateIcon icon={tpl.icon} />
                      </div>
                      <span className="rounded-md border border-[var(--app-border)]/60 bg-[var(--app-bg-alt)] px-2 py-0.5 text-[10px] font-medium text-[var(--app-text-muted)] font-mono">
                        {tpl.cadence}
                      </span>
                    </div>
                    <h4 className="mt-3 text-xs font-semibold text-[var(--app-text)]">{tpl.title}</h4>
                    <p className="mt-1 text-[11px] leading-relaxed text-[var(--app-text-muted)]">
                      {tpl.description}
                    </p>
                  </div>
                  <Button
                    size="sm"
                    variant="outline"
                    className="mt-3 h-7 w-full gap-1 text-[11px] font-medium hover:border-[var(--app-primary-border)] hover:bg-[var(--app-primary-soft)] hover:text-[var(--app-primary)]"
                    onClick={() => handleUseTemplate(tpl.prompt)}
                  >
                    <Sparkles size={12} />
                    <span>Propose with Swarm →</span>
                  </Button>
                </div>
              ))}
            </div>
          </section>
        </main>

        {/* Full-Height Right Aside: Automations Assistant */}
        <aside
          aria-label="Worker Agent"
          className="flex h-full w-full min-h-[420px] flex-col border-t border-[var(--app-border)] bg-[var(--app-surface)] xl:w-[400px] xl:max-w-[400px] xl:shrink-0 xl:border-t-0 xl:border-l"
        >
          <AutomationV2Sidecar
            workspaceId={workspaceId}
            workspacePath={workspacePath}
            workspaceBindingId={workspaceBindingId}
            selectedAutomation={selectedRecord}
            activeSessionId={session}
            onSelectSession={(id) => {
              setSession(id)
              setSelected(id)
            }}
            createRequest={createRequest}
            records={records}
            pendingProposals={pendingProposals}
            initialDraft={draftPrompt}
            onClearSelectedAutomation={() => {
              setSelected('')
              setSession('')
            }}
          />
        </aside>
      </div>

      {/* Delete Automation Confirmation Modal */}
      {deleteConfirmRecord && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-xs p-4"
          role="dialog"
          aria-modal="true"
          aria-labelledby="delete-automation-title"
        >
          <div className="w-full max-w-md rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface-elevated)] p-6 shadow-2xl space-y-4">
            <div className="flex items-center gap-3 text-[var(--app-danger)]">
              <Trash2 size={20} />
              <h3 id="delete-automation-title" className="text-base font-semibold text-[var(--app-text)]">
                Delete worker?
              </h3>
            </div>
            <p className="text-xs leading-relaxed text-[var(--app-text-muted)]">
              Are you sure you want to delete <strong className="text-[var(--app-text)]">“{deleteConfirmRecord.document.title}”</strong>? This will cancel all future recurring runs and permanently delete the worker session.
            </p>
            <div className="flex justify-end gap-2 pt-2">
              <Button variant="outline" size="sm" disabled={deleteBusy} onClick={() => setDeleteConfirmRecord(null)}>
                Cancel
              </Button>
              <Button
                size="sm"
                className="bg-[var(--app-danger)] text-white hover:bg-[var(--app-danger)]/90 gap-1.5"
                disabled={deleteBusy}
                onClick={() => void confirmDeleteRecord()}
              >
                {deleteBusy ? <LoaderCircle size={13} className="animate-spin" /> : null}
                <span>{deleteBusy ? 'Deleting…' : 'Delete worker'}</span>
              </Button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
// Persisted scheduling handoff survives permission removal, navigation and reload.
export function AutomationV2ScheduleHandoff({ workspaceId, sessionId }: { workspaceId: string; sessionId: string }) {
  const page = useAutomationV2Page({ action: 'progress', workspace_id: workspaceId, session_id: sessionId, timezone: 'UTC' })
  const record = page?.data?.progress?.record
  if (!record) return null
  return <section aria-label="Worker handoff" className="m-4 space-y-2 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-4"><h3 className="font-semibold">{record.cancelled ? 'Worker cancelled' : record.enabled ? 'Worker deployed' : 'Worker paused'}</h3><p className="text-sm">Deployed revision {record.revision}. Deployment does not start an immediate run.</p><p className="text-sm">{record.enabled && !record.cancelled && record.next_due_at ? `Next scheduled time: ${new Date(record.next_due_at).toISOString()} (UTC)` : 'No active next scheduled time.'}</p><p className="text-xs text-[var(--app-text-muted)]">{page?.stale || page?.loading ? 'Refreshing observed schedule… ' : ''}Deployed is not admitted or running. Worker details shows the schedule, expiration and observed work.</p></section>
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
  const todayStats = useMemo(() => {
    if (!progress?.occurrences) return null
    const todayKey = getOccurrenceDayKey(Date.now(), timezone)
    const todayOccurrences = progress.occurrences.filter(o => getOccurrenceDayKey(o.due_at || 0, timezone) === todayKey)
    let clean = 0
    let deliverables = 0
    let alerts = 0
    let blockedCount = 0
    for (const o of todayOccurrences) {
      if (isOccurrenceRoutineClean(o)) clean++
      if (isOccurrenceDeliverableReady(o) || extractOccurrenceDeliverables(o).length > 0) deliverables++
      if (isOccurrenceAwaitingDocument(o)) {
        if (o.closing_state === 'blocked' || o.state === 'blocked') blockedCount++
        else alerts++
      } else if (o.closing_state === 'attention_alert' || o.state === 'failed') {
        alerts++
      }
    }
    return {
      total: todayOccurrences.length,
      clean,
      deliverables,
      alerts,
      blocked: blockedCount,
    }
  }, [progress?.occurrences, timezone])

  return <section aria-label="Worker details" className="min-w-0 rounded-2xl border border-[var(--app-border)]/70 bg-[var(--app-surface)] font-sans text-xs shadow-[0_1px_2px_color-mix(in_srgb,var(--app-text)_5%,transparent)] [overflow-wrap:anywhere]">
    <div className="space-y-2.5 p-3 sm:p-4">
    <header className="border-b border-[var(--app-border)]/60 pb-2">
      <div className="flex items-center justify-between gap-2"><span className={`${eyebrow} text-[var(--app-primary)]`}>Worker</span><Button size="sm" variant="ghost" className="h-6 w-6 p-0" aria-label="Refresh progress" title="Refresh progress" disabled={busy || page?.loading} onClick={() => void desktopAutomationV2.refresh(input)}><RefreshCcw size={12} /></Button></div>
      <h2 className="mt-1 line-clamp-2 break-words text-sm font-semibold leading-5" title={record?.document.title}>{record?.document.title ?? 'Loading worker…'}</h2>
      <p className="mt-1 text-[10px] text-[var(--app-text-subtle)]">{record ? `${record.document.checkpoints.length} ${record.document.checkpoints.length === 1 ? 'step' : 'steps'} · Deployed worker` : 'Loading schedule'}</p>
    </header>
    {(page?.loading || page?.stale) && <p role="status" className="text-[11px] text-[var(--app-text-muted)]">Updating schedule…</p>}{(error || page?.error) && <p role="alert" className="text-[var(--app-danger)]">{error || page?.error}</p>}
    {record && schedule && <section aria-label="Worker schedule handoff">
      <div className="flex items-center justify-between gap-2"><h3 className={eyebrow}>Schedule</h3><span className={`text-[10px] font-semibold uppercase tracking-wider ${record.enabled && !record.cancelled ? 'text-[var(--app-primary)]' : 'text-[var(--app-text-muted)]'}`}>{status}</span></div>
      <div className="mt-1.5 rounded-xl border border-[var(--app-primary-border)]/45 bg-[var(--app-primary-soft)] px-3 py-2"><p className="font-mono text-[13px] font-medium">{scheduleLabel(schedule)}</p><p className="mt-0.5 text-[11px] text-[var(--app-primary)]">{scheduleFrequency(schedule)}</p>{schedule.timezone && <p className="mt-0.5 text-[10px] text-[var(--app-text-muted)]">Schedule timezone · {schedule.timezone}</p>}</div>
      <div className="mt-2 rounded-xl border border-[var(--app-border)]/50 bg-[var(--app-bg-alt)] p-2.5"><h3 className={eyebrow}>Next run</h3><p className="mt-1 font-medium">{record.enabled && !record.cancelled && record.next_due_at ? time(record.next_due_at) : 'No upcoming run'}</p><p className="mt-0.5 text-[10px] text-[var(--app-text-subtle)]">{timezone} · Scheduled time, not guaranteed start</p></div>
    </section>}
    {record && <>
      <div className="grid grid-cols-2 gap-2"><Button size="sm" variant="outline" className="h-8 rounded-xl text-xs" disabled={disabled || record.cancelled} onClick={() => setEditing(v => !v)} aria-expanded={editing}>Edit worker</Button><Button size="sm" variant="outline" className="h-8 rounded-xl text-xs" disabled={disabled || record.cancelled} onClick={() => void control(record.enabled ? 'pause' : 'resume')}>{record.enabled ? 'Pause schedule' : 'Resume schedule'}</Button></div>
      <p className="text-[11px] leading-4 text-[var(--app-text-subtle)]">{record.authorization.kind === 'indefinite' ? 'Repeats until stopped.' : `Ends ${time(record.authorization.expires_at!)}.`} Pausing leaves admitted work unchanged.</p>
      {editing && <AutomationV2Edit record={record} />}
      <details className="rounded-xl bg-[var(--app-bg-alt)] p-2.5"><summary className={disclosure}>Instructions · {record.document.checkpoints.length} {record.document.checkpoints.length === 1 ? 'step' : 'steps'}</summary><p className="mt-2 leading-5">{record.document.info.goal}</p><ol className="mt-2 space-y-2">{record.document.checkpoints.map(c => <li key={c.id}><p className="font-medium">{c.title}</p><ul className="mt-1 list-inside list-disc text-[var(--app-text-muted)]">{(c.tasks ?? [c.objective ?? '']).filter(Boolean).map((task, i) => <li key={i}>{task}</li>)}</ul></li>)}</ol></details>
    </>}
    {todayStats && (
      <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] p-2.5 flex flex-wrap items-center justify-between gap-2" data-testid="today-executive-strip">
        <div className="flex items-center gap-2">
          <div className="flex size-6 shrink-0 items-center justify-center rounded-lg bg-[var(--app-primary-soft)] text-[var(--app-primary)]">
            <Clock3 size={14} />
          </div>
          <div>
            <div className="text-xs font-semibold text-[var(--app-text)]">Today's Pulse · {new Intl.DateTimeFormat(undefined, { timeZone: timezone, month: 'short', day: 'numeric', year: 'numeric' }).format(Date.now())}</div>
            <p className="text-[10px] text-[var(--app-text-muted)]">
              {todayStats.total === 0
                ? 'No runs executed yet today.'
                : `${todayStats.total} ${todayStats.total === 1 ? 'run' : 'runs'} executed today · ${todayStats.alerts === 0 && todayStats.blocked === 0 ? 'All systems clean' : `${todayStats.alerts + todayStats.blocked} item(s) require attention`}`}
            </p>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-1.5 text-xs">
          <span className="inline-flex items-center gap-1 rounded-md border border-[var(--app-success-border,rgba(16,185,129,0.3))] bg-[var(--app-success-bg,rgba(16,185,129,0.12))] px-2 py-0.5 text-[10.5px] font-medium text-[var(--app-success)]">
            ✓ {todayStats.clean} clean
          </span>
          {todayStats.deliverables > 0 && (
            <span className="inline-flex items-center gap-1 rounded-md border border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] px-2 py-0.5 text-[10.5px] font-medium text-[var(--app-primary)]">
              ★ {todayStats.deliverables} deliverable{todayStats.deliverables === 1 ? '' : 's'}
            </span>
          )}
          {todayStats.alerts > 0 ? (
            <span className="inline-flex items-center gap-1 rounded-md border border-[var(--app-warning-border,rgba(245,158,11,0.4))] bg-[var(--app-warning-bg,rgba(245,158,11,0.12))] px-2 py-0.5 text-[10.5px] font-medium text-[var(--app-warning)]">
              ⚠ {todayStats.alerts} alert{todayStats.alerts === 1 ? '' : 's'}
            </span>
          ) : (
            <span className="inline-flex items-center gap-1 rounded-md border border-[var(--app-border)] bg-[var(--app-surface)] px-2 py-0.5 text-[10.5px] font-medium text-[var(--app-text-subtle)]">
              0 alerts
            </span>
          )}
          {todayStats.blocked > 0 && (
            <span className="inline-flex items-center gap-1 rounded-md border border-[var(--app-danger-border,rgba(239,68,68,0.4))] bg-[var(--app-danger-bg,rgba(239,68,68,0.12))] px-2 py-0.5 text-[10.5px] font-medium text-[var(--app-danger)]">
              ✕ {todayStats.blocked} blocked
            </span>
          )}
        </div>
      </div>
    )}
    {progress && (
      <details className="rounded-xl bg-[var(--app-bg-alt)] p-3" aria-label="Run history & daily summaries">
        <summary className={disclosure}>Run history & upcoming times</summary>
        <div className="mt-3 space-y-4">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-[var(--app-border)]/40 pb-2">
            <div>
              <h3 className={eyebrow}>Upcoming times & history</h3>
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
        </div>
      </details>
    )}
    </div>
    {record && <div className="space-y-3 border-t border-[var(--app-border)]/60 p-3">
      <Button variant="outline" size="sm" className="h-9 w-full rounded-xl text-xs" disabled={disabled} onClick={() => onChat?.(sessionId)}>Optimize with Swarm</Button>
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
      className="rounded-xl border border-[var(--app-warning-border,rgba(234,179,8,0.5))]/50 bg-[var(--app-warning-bg,rgba(234,179,8,0.12))] p-3 text-xs shadow-xs space-y-2"
      data-testid="awaiting-document-run-card"
      data-run-id={occurrence.id}
    >
      <div className="flex items-center justify-between gap-2">
        <span className="inline-flex items-center gap-1 rounded-full bg-[var(--app-warning-bg,rgba(234,179,8,0.2))] px-2.5 py-0.5 text-[10px] font-semibold text-[var(--app-warning)] uppercase tracking-wider">
          <AlertCircle size={11} aria-hidden="true" />
          {isBlocked ? 'Blocked · Action needed' : 'Awaiting document review'}
        </span>
        <span className="text-[10px] text-[var(--app-text-subtle)]">{timeStr}</span>
      </div>

      <p className="text-xs text-[var(--app-text)] leading-4">
        {occurrence.detail || (isBlocked ? 'Execution is blocked: external dependency or permission required.' : 'Execution produced a document awaiting user review or checkpoint acceptance.')}
      </p>

      <div className="flex items-center justify-between border-t border-[var(--app-warning-border,rgba(234,179,8,0.3))]/30 pt-2 text-[11px]">
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
      {dayGroups.map((group) => {
        const isToday = group.label === 'Today'
        return (
          <details
            key={group.dayKey}
            open={isToday}
            aria-label={`Runs for ${group.label}`}
            className="group/day rounded-xl border border-[var(--app-border)]/60 bg-[var(--app-surface)]/80 overflow-hidden space-y-0 transition-all"
            data-testid="run-feed-day-group"
          >
            <summary
              className="flex flex-wrap items-center justify-between gap-2 p-2.5 px-3 bg-[var(--app-surface-subtle)]/70 hover:bg-[var(--app-surface-hover)] cursor-pointer list-none select-none transition-colors"
              data-testid="run-feed-day-header"
            >
              <div className="flex items-center gap-2">
                <ChevronRight size={13} className="shrink-0 text-[var(--app-text-subtle)] transition-transform group-open/day:rotate-90" />
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
                {!isToday && (
                  <span className="text-[10px] text-[var(--app-text-subtle)] group-open/day:hidden">Click to expand</span>
                )}
              </div>
            </summary>
            <ul className="space-y-2 p-3 pt-2 border-t border-[var(--app-border)]/40">
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
          </details>
        )
      })}
    </div>
  )
}
