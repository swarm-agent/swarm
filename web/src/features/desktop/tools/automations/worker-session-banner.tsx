import React, { useMemo } from 'react'
import {
  AlertCircle,
  AlertTriangle,
  Bot,
  CheckCircle2,
  ChevronLeft,
  Clock3,
  ExternalLink,
  FileText,
  RefreshCcw,
} from 'lucide-react'
import { cn } from '../../../../lib/cn'
import { useDesktopV3CacheSelector } from '../../state/desktop-v3-cache-store'
import { selectAutomationV2Identity } from '../../state/desktop-automation-v2-state'
import type { AutomationV2Occurrence, AutomationV2Record } from '../../state/desktop-automation-v2-api'
import { formatScheduleDateTime, formatScheduleTime, scheduleLabel } from './automation-v2-schedule'
import {
  formatCalmStatus,
  isOccurrenceDeliverableReady,
  isOccurrenceRoutineClean,
} from './automation-v2-workspace'

function metadataString(metadata: Record<string, unknown> | undefined, key: string): string {
  if (!metadata) return ''
  const val = metadata[key]
  return typeof val === 'string' ? val.trim() : ''
}

export function WorkerSessionBanner({
  sessionId,
  workspaceSlug,
}: {
  sessionId: string
  workspaceSlug?: string
}) {
  const sessionInfo = useDesktopV3CacheSelector((state) => {
    const sessionRecord = state.sessionsById?.[sessionId]
    if (!sessionRecord || sessionRecord.kind !== 'full') return null

    const metadata = sessionRecord.session.metadata
    const occurrenceId = metadataString(metadata, 'automation_v2_occurrence_id')
    const authoringSessionId = metadataString(metadata, 'automation_v2_authoring_session_id')
    const occurrenceDigest = metadataString(metadata, 'automation_v2_digest')
    const occurrenceRevision = metadataString(metadata, 'automation_v2_revision')
    const isOccurrence = Boolean(occurrenceId)

    const isAuthoring =
      Boolean(sessionRecord.session.automation_v2) ||
      selectAutomationV2Identity(state, sessionId) === 'accepted'

    if (!isOccurrence && !isAuthoring) return null

    // Find the associated worker definition record from automation pages
    let workerRecord: AutomationV2Record | undefined
    let matchedOccurrence: AutomationV2Occurrence | undefined

    const targetAuthorSessionId = authoringSessionId || sessionId
    for (const page of Object.values(state.automationV2Pages ?? {})) {
      const records = page.data?.records ?? (page.data?.record ? [page.data.record] : (page.data?.progress?.record ? [page.data.progress.record] : []))
      const found = records.find((r) => r.session_id === targetAuthorSessionId)
      if (found) {
        workerRecord = found
      }
      if (page.data?.progress?.occurrences) {
        const occ = page.data.progress.occurrences.find((o) => o.id === occurrenceId || o.session_id === sessionId)
        if (occ) {
          matchedOccurrence = occ
        }
      }
    }

    // Also check authoring session record in sessionsById
    const authorSessionRecord = state.sessionsById?.[targetAuthorSessionId]
    const authorTitle =
      workerRecord?.document.title ||
      (authorSessionRecord?.kind === 'full' ? authorSessionRecord.session.title : undefined) ||
      sessionRecord.session.title ||
      'Worker'

    const schedule =
      workerRecord?.document.automation_v2.schedule ||
      (sessionRecord.session.automation_v2?.schedule)

    const workspaceId =
      workerRecord?.workspace_id ||
      sessionRecord.session.workspace_grants?.find((g) => g.kind === 'primary')?.workspace_id ||
      sessionRecord.session.automation_v2?.workspace_id

    return {
      isOccurrence,
      isAuthoring,
      occurrenceId,
      authoringSessionId: targetAuthorSessionId,
      workerTitle: authorTitle,
      schedule,
      workspaceId,
      occurrence: matchedOccurrence,
      enabled: workerRecord?.enabled ?? true,
      cancelled: workerRecord?.cancelled ?? false,
      nextDueAt: workerRecord?.next_due_at,
    }
  })

  if (!sessionInfo) return null

  const {
    isOccurrence,
    authoringSessionId,
    workerTitle,
    schedule,
    occurrence,
  } = sessionInfo

  const timezone = schedule?.timezone
  const closingState = occurrence?.closing_state
  const isClean = occurrence ? isOccurrenceRoutineClean(occurrence) : false
  const isDeliverable = occurrence ? isOccurrenceDeliverableReady(occurrence) : false
  const isAlert = closingState === 'attention_alert' || occurrence?.state === 'failed'
  const isBlocked = closingState === 'blocked' || occurrence?.state === 'blocked'
  const isRunning = occurrence?.state === 'running' || occurrence?.state === 'in_progress'

  const statusLabel = isRunning
    ? 'Running now'
    : isClean
    ? 'Routine Clean'
    : isDeliverable
    ? 'Deliverable Ready'
    : isAlert
    ? 'Attention Alert'
    : isBlocked
    ? 'Blocked'
    : occurrence
    ? formatCalmStatus(occurrence)
    : isOccurrence
    ? 'Completed Run'
    : 'Active Worker'

  const effectiveSlug = workspaceSlug || 'workspace'

  return (
    <div
      role="region"
      aria-label="Worker execution context"
      data-testid="worker-session-banner"
      className="shrink-0 border-b border-[var(--app-border)] bg-[var(--app-surface-subtle)]/90 px-4 py-2 text-xs backdrop-blur-sm sm:px-6"
    >
      <div className="flex flex-wrap items-center justify-between gap-2.5">
        {/* Left: Breadcrumbs and worker identification */}
        <div className="flex min-w-0 flex-1 flex-wrap items-center gap-2">
          <a
            href={`/${encodeURIComponent(effectiveSlug)}/workers`}
            className="inline-flex shrink-0 items-center gap-1 rounded-md px-1.5 py-0.5 font-medium text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-primary)] transition-colors"
            title="Back to all Workers"
          >
            <ChevronLeft size={13} className="shrink-0" />
            <span>Workers</span>
          </a>

          <span className="text-[var(--app-border)] select-none">/</span>

          <div className="flex min-w-0 items-center gap-1.5">
            <span
              className={cn(
                'shrink-0 inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[9.5px] font-semibold uppercase tracking-wider',
                isOccurrence
                  ? 'bg-[var(--app-primary-soft)] text-[var(--app-primary)] border border-[var(--app-primary-border)]/40'
                  : 'bg-[var(--app-surface)] text-[var(--app-text-muted)] border border-[var(--app-border)]'
              )}
            >
              <Bot size={10} className="shrink-0" />
              {isOccurrence ? 'Scheduled Run' : 'Worker Session'}
            </span>
            <span className="truncate font-semibold text-[var(--app-text)]" title={workerTitle}>
              {workerTitle}
            </span>
          </div>

          {schedule && (
            <span className="hidden sm:inline-flex items-center gap-1 rounded-md bg-[var(--app-surface)] px-2 py-0.5 text-[11px] text-[var(--app-text-muted)] border border-[var(--app-border)]/60">
              <Clock3 size={11} className="shrink-0 text-[var(--app-text-subtle)]" />
              <span>
                {scheduleLabel(schedule)}
                {timezone ? ` · ${timezone}` : ''}
              </span>
            </span>
          )}

          {/* Status Badge */}
          <span
            className={cn(
              'inline-flex shrink-0 items-center gap-1 rounded-full px-2 py-0.5 text-[10px] font-medium border leading-none',
              isRunning && 'border-[var(--app-success-border,rgba(34,197,94,0.4))] bg-[var(--app-success-bg,rgba(34,197,94,0.12))] text-[var(--app-success)]',
              isClean && 'border-[var(--app-success-border,rgba(34,197,94,0.3))] bg-[var(--app-success-bg,rgba(34,197,94,0.08))] text-[var(--app-success)]',
              isDeliverable && 'border-[var(--app-primary-border,rgba(59,130,246,0.4))] bg-[var(--app-primary-soft)] text-[var(--app-primary)]',
              isAlert && 'border-[var(--app-warning-border,rgba(245,158,11,0.4))] bg-[var(--app-warning-bg,rgba(245,158,11,0.12))] text-[var(--app-warning)]',
              isBlocked && 'border-[var(--app-danger-border,rgba(239,68,68,0.4))] bg-[var(--app-danger-bg,rgba(239,68,68,0.12))] text-[var(--app-danger)]',
              !isRunning && !isClean && !isDeliverable && !isAlert && !isBlocked && 'border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)]'
            )}
          >
            {isRunning && (
              <span className="h-1.5 w-1.5 rounded-full bg-[var(--app-success)] animate-pulse" aria-hidden="true" />
            )}
            {isClean && <CheckCircle2 size={10} className="shrink-0" />}
            {isDeliverable && <FileText size={10} className="shrink-0" />}
            {isAlert && <AlertTriangle size={10} className="shrink-0" />}
            {isBlocked && <AlertCircle size={10} className="shrink-0" />}
            <span>{statusLabel}</span>
          </span>
        </div>

        {/* Right: Quick action back to Worker details */}
        <div className="flex shrink-0 items-center gap-1.5">
          <a
            href={`/${encodeURIComponent(effectiveSlug)}/workers?sessionId=${encodeURIComponent(authoringSessionId)}`}
            className="inline-flex items-center gap-1 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 py-1 text-[11px] font-medium text-[var(--app-text)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-primary)] transition-colors shadow-xs"
            title="Open worker dashboard and configuration"
          >
            <span>Worker Details</span>
            <ExternalLink size={10} className="shrink-0 opacity-70" />
          </a>
        </div>
      </div>
    </div>
  )
}
