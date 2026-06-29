import { memo } from 'react'
import { cn } from '../../../../lib/cn'
import { Button } from '../../../../components/ui/button'
import type { DesktopSessionPlanCheckpoint } from '../types/chat'
import type { DesktopPlanExecutionView } from '../../state/desktop-v3-cache-selectors'
type DesktopPlanExecutionSidebarAction =
  | 'accept_checkpoint'
  | 'resume_automatic'
  | 'archive_plan'

export interface DesktopPlanExecutionSidebarActionInput {
  action: DesktopPlanExecutionSidebarAction
  checkpointId?: string
}

interface DesktopPlanExecutionSidebarProps {
  view: DesktopPlanExecutionView | null
  busyAction?: string | null
  canStop?: boolean
  onAction?: (input: DesktopPlanExecutionSidebarActionInput) => void | Promise<void>
  onStop?: () => void | Promise<void>
  onEditPlan?: () => void
}

type Tone = 'muted' | 'primary' | 'success' | 'warning' | 'danger'

function humanize(value: string): string {
  const trimmed = value.trim()
  if (!trimmed) return 'Unknown'
  return trimmed
    .replace(/[-_]+/g, ' ')
    .replace(/\w\S*/g, (word) => word.charAt(0).toUpperCase() + word.slice(1).toLowerCase())
}

function displayCheckpointId(value: string, fallback: number): string {
  const trimmed = value.trim()
  if (!trimmed) return `CP-${fallback + 1}`
  const match = trimmed.match(/^cp[-_ ]?(\d+)$/i)
  if (match) return `CP-${match[1]}`
  return trimmed.toUpperCase().startsWith('CP-') ? trimmed.toUpperCase() : trimmed
}

function statusTone(status: string, active = false): Tone {
  const normalized = status.trim().toLowerCase()
  if (normalized === 'completed' || normalized === 'done' || normalized === 'success') return 'success'
  if (normalized === 'needs_review' || normalized === 'waiting_review' || normalized === 'review') return 'warning'
  if (normalized === 'blocked' || normalized === 'failed' || normalized === 'error') return 'danger'
  if (active || normalized === 'in_progress' || normalized === 'in-progress' || normalized === 'running' || normalized === 'active') return 'primary'
  return 'muted'
}

function toneBadgeClass(tone: Tone): string {
  switch (tone) {
    case 'success':
      return 'border-[var(--app-success-border)] bg-[var(--app-success-bg)] text-[var(--app-success)]'
    case 'warning':
      return 'border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] text-[var(--app-warning-text)]'
    case 'danger':
      return 'border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] text-[var(--app-danger)]'
    case 'primary':
      return 'border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] text-[var(--app-primary)]'
    default:
      return 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text-muted)]'
  }
}

const waitingReviewBadgeClass = 'rounded-md border border-[var(--app-border)] bg-[linear-gradient(135deg,var(--app-warning-bg),var(--app-primary-soft))] px-2 py-0.5 text-[9px] font-semibold uppercase leading-4 tracking-[0.06em] text-[var(--app-text)] shadow-[0_0_18px_color-mix(in_oklab,var(--app-warning-text)_12%,transparent)]'

function actionBusyKey(action: DesktopPlanExecutionSidebarAction, checkpointId?: string): string {
  return `${action}:${checkpointId ?? ''}`
}

function checkpointIsIncomplete(checkpoint: DesktopSessionPlanCheckpoint): boolean {
  return checkpoint.status.trim().toLowerCase() !== 'completed'
}

function statusLabel(view: DesktopPlanExecutionView, checkpoint?: DesktopSessionPlanCheckpoint): string {
  if (view.reviewRequired) return 'Waiting review'
  if (view.completed) return 'Completed'
  if (view.blocked) return 'Blocked'
  if (view.failed) return 'Failed'
  return humanize(checkpoint?.status || view.status || 'Ready')
}

function isSingleRunView(view: DesktopPlanExecutionView): boolean {
  return (view.policyShape || view.plan.document?.executionPolicy?.shape || '').trim().toLowerCase() === 'single_run'
}

function StatusBadge({ label, tone }: { label: string; tone: Tone }) {
  const isWaitingReview = label.trim().toLowerCase() === 'waiting review'
  return (
    <span className={cn('inline-flex max-w-[132px] shrink-0 items-center', isWaitingReview ? waitingReviewBadgeClass : cn('rounded-md border px-1.5 py-px text-[9px] font-semibold uppercase leading-4 tracking-[0.06em]', toneBadgeClass(tone)))}>
      <span className="truncate">{label}</span>
    </span>
  )
}

function PlanRunCard({ view, onOpenPlan }: { view: DesktopPlanExecutionView; onOpenPlan?: () => void }) {
  const title = view.plan.document?.title?.trim() || view.plan.title?.trim() || 'Approved plan'
  const tone = statusTone(view.status, !view.completed && !view.blocked && !view.failed)

  return (
    <section className="min-w-0 overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-3.5 shadow-[0_12px_34px_rgba(0,0,0,0.16)]">
      <div className="flex min-w-0 items-start justify-between gap-3">
        <div className="text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Plan execution</div>
        <StatusBadge label={statusLabel(view)} tone={tone} />
      </div>
      <h3
        className="mt-1 min-w-0 break-words text-sm font-semibold leading-snug text-[var(--app-text)] [overflow-wrap:anywhere]"
        title={title}
      >
        {title}
      </h3>
      <p className="mt-2 text-xs leading-5 text-[var(--app-text-muted)]">
        Running the approved plan normally as one fresh-context execution.
      </p>
      <Button type="button" size="sm" variant="outline" onClick={onOpenPlan} disabled={!onOpenPlan} className="mt-3 w-full rounded-lg">
        Open full plan
      </Button>
    </section>
  )
}

function ActiveCheckpointCard({ view, checkpoints, completedCount, totalCount, activeIndex, onOpenPlan }: { view: DesktopPlanExecutionView; checkpoints: DesktopSessionPlanCheckpoint[]; completedCount: number; totalCount: number; activeIndex: number; onOpenPlan?: () => void }) {
  const checkpoint = view.activeCheckpoint
  const activePosition = activeIndex >= 0 ? activeIndex : -1
  const checkpointFallbackIndex = activeIndex >= 0 ? activeIndex : 0
  const checkpointId = checkpoint ? displayCheckpointId(checkpoint.id, checkpointFallbackIndex) : 'None'
  const title = checkpoint?.title || 'No active checkpoint'
  const activeTitle = checkpoint ? `${checkpointId} ${title}` : title
  const nextCheckpoint = checkpoints.find((candidate, index) => index > activePosition && checkpointIsIncomplete(candidate))
  const nextIndex = nextCheckpoint ? checkpoints.findIndex((candidate) => candidate === nextCheckpoint) : -1
  const progressValue = totalCount > 0 ? Math.max(0, Math.min(100, (completedCount / totalCount) * 100)) : 0
  const tone = statusTone(view.reviewRequired ? 'needs_review' : checkpoint?.status || view.status, Boolean(checkpoint))

  return (
    <section className="min-w-0 overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-3.5 shadow-[0_12px_34px_rgba(0,0,0,0.16)]">
      <div className="flex min-w-0 items-start justify-between gap-3">
        <div className="text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Active checkpoint</div>
        <StatusBadge label={statusLabel(view, checkpoint)} tone={tone} />
      </div>
      <h3
        className="mt-1 min-w-0 break-words text-sm font-semibold leading-snug text-[var(--app-text)] [overflow-wrap:anywhere]"
        title={activeTitle}
      >
        <span className="font-mono text-xs font-semibold text-[var(--app-primary)]">{checkpointId}</span>{' '}
        {title}
      </h3>

      <div className="mt-3.5">
        <div className="mb-1.5 flex items-center justify-between text-[11px] text-[var(--app-text-muted)]">
          <span>Progress</span>
          <span className="font-medium text-[var(--app-text-muted)]">{completedCount} / {totalCount}</span>
        </div>
        <div className="h-1.5 overflow-hidden rounded-full bg-[var(--app-surface-subtle)]">
          <div className="h-full rounded-full bg-[var(--app-primary)] opacity-80" style={{ width: `${progressValue}%` }} />
        </div>
      </div>

      <div className="mt-3.5 min-w-0 border-t border-[var(--app-border)] px-2 pt-2.5 text-xs">
        <div className="text-[11px] font-semibold uppercase tracking-[0.06em] text-[var(--app-text-subtle)]">Next up</div>
        {nextCheckpoint ? (
          <div className="mt-0.5 break-words text-[var(--app-text-muted)]" title={`${displayCheckpointId(nextCheckpoint.id, nextIndex)} ${nextCheckpoint.title || 'Untitled checkpoint'}`}>
            <span className="font-mono font-semibold text-[var(--app-text)]">{displayCheckpointId(nextCheckpoint.id, nextIndex)}</span>
            {' '}{nextCheckpoint.title || 'Untitled checkpoint'}
          </div>
        ) : (
          <div className="mt-0.5 text-[var(--app-text-muted)]">No remaining checkpoint</div>
        )}
      </div>

      <Button type="button" size="sm" variant="outline" onClick={onOpenPlan} disabled={!onOpenPlan} className="mt-3 w-full rounded-lg">
        Open full plan
      </Button>
    </section>
  )
}

function ActionsCard({ view, busyAction, canStop, onAction }: DesktopPlanExecutionSidebarProps & { view: DesktopPlanExecutionView }) {
  const checkpointId = view.activeCheckpointId || view.activeCheckpoint?.id || ''
  const automatic = view.policyMode === 'automatic'
  const acceptBusy = busyAction === actionBusyKey('accept_checkpoint', checkpointId)
  const archiveBusy = busyAction === actionBusyKey('archive_plan')
  const checkpoints = view.plan.document?.checkpoints ?? []
  const activeIndex = view.activeCheckpoint
    ? checkpoints.findIndex((checkpoint) => checkpoint.id === view.activeCheckpoint?.id)
    : -1
  const hasNextCheckpoint = checkpoints.some((checkpoint, index) => index > activeIndex && checkpointIsIncomplete(checkpoint))
  const canAccept = Boolean(onAction && checkpointId && view.reviewRequired && !view.blocked && !view.failed && !canStop)
  const canArchive = Boolean(onAction && !canStop && !archiveBusy)
  const acceptReviewBusy = acceptBusy
  const acceptReviewLabel = hasNextCheckpoint ? 'Accept & start next checkpoint' : 'Accept & archive plan'
  const acceptReviewHelp = hasNextCheckpoint
    ? 'Accepting review starts the next checkpoint. You can keep chatting first or ask the AI to add or adjust checkpoints.'
    : 'Accept & archive plan accepts this review and moves the completed plan to Archived.'
  const showDirectArchiveAction = !view.reviewRequired || hasNextCheckpoint

  if (automatic && !view.reviewRequired && !view.blocked && !view.failed && !view.completed) {
    const singleRun = isSingleRunView(view)
    return (
      <section className="min-w-0 overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-3.5 shadow-[0_12px_34px_rgba(0,0,0,0.14)]">
        <div className="text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Actions</div>
        <div className="mt-3 rounded-xl border border-[var(--app-primary-border)] bg-[var(--app-primary-soft)] px-3 py-2.5">
          <div className="text-sm font-semibold text-[var(--app-text)]">{singleRun ? 'Continue normally' : 'Automatic mode on'}</div>
          <p className="mt-1 text-xs leading-5 text-[var(--app-text-muted)]">
            {singleRun
              ? 'Execution will continue as one plan run until it completes or stops for review, a blocker, or a failure.'
              : 'Execution will continue automatically through checkpoint, pause the chat at any time to change agent settings and continue'}
          </p>
        </div>
        <div className="mt-3 grid gap-1.5">
          <Button
            type="button"
            size="sm"
            variant="outline"
            className={cn('rounded-lg', archiveBusy ? 'animate-pulse' : '')}
            onClick={() => void onAction?.({ action: 'archive_plan' })}
            disabled={!canArchive}
          >
            Archive plan
          </Button>
          <p className="px-1 text-[11px] leading-4 text-[var(--app-text-muted)]">
            Archive this plan when you no longer need the chat in your active workspace.
          </p>
        </div>
      </section>
    )
  }

  return (
    <section className="min-w-0 overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-3.5 shadow-[0_12px_34px_rgba(0,0,0,0.14)]">
      <div className="text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Actions</div>
      <div className="mt-3 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2.5">
        <div className="text-sm font-medium text-[var(--app-text)]">Review Mode</div>
        <p className="mt-0.5 text-xs leading-5 text-[var(--app-text-muted)]">
          You can keep chatting and ask the AI to continue work or add checkpoints. Accept and archive the completed plan when you’re done.
        </p>
      </div>

      <div className="mt-3 grid gap-2">
        <div className="grid gap-1.5">
          <Button
            type="button"
            size="sm"
            variant="primary"
            className={cn('rounded-lg', acceptReviewBusy ? 'animate-pulse' : '')}
            onClick={() => void onAction?.({ action: 'accept_checkpoint', checkpointId })}
            disabled={!canAccept || acceptReviewBusy}
          >
            {acceptReviewLabel}
          </Button>
          {view.reviewRequired ? (
            <p className="px-1 text-[11px] leading-4 text-[var(--app-text-muted)]">
              {acceptReviewHelp}
            </p>
          ) : null}
        </div>
        {showDirectArchiveAction ? (
          <div className="grid gap-1.5">
            <Button
              type="button"
              size="sm"
              variant="outline"
              className={cn('rounded-lg', archiveBusy ? 'animate-pulse' : '')}
              onClick={() => void onAction?.({ action: 'archive_plan' })}
              disabled={!canArchive}
            >
              Archive plan
            </Button>
            <p className="px-1 text-[11px] leading-4 text-[var(--app-text-muted)]">
              Move this plan to Archived without starting another checkpoint.
            </p>
          </div>
        ) : null}
      </div>
    </section>
  )
}

export const DesktopPlanExecutionSidebar = memo(function DesktopPlanExecutionSidebar({ view, busyAction, canStop = false, onAction, onStop: _onStop, onEditPlan }: DesktopPlanExecutionSidebarProps) {
  const document = view?.plan.document ?? null
  if (!view || !document) return null

  const checkpoints = document.checkpoints
  const completedCount = checkpoints.filter((checkpoint) => checkpoint.status.toLowerCase() === 'completed').length
  const totalCount = checkpoints.length
  const activeIndex = view.activeCheckpoint
    ? checkpoints.findIndex((checkpoint) => checkpoint.id === view.activeCheckpoint?.id)
    : -1
  const singleRun = isSingleRunView(view)

  return (
    <aside
      className="hidden min-h-0 min-w-0 w-[360px] max-w-[360px] overflow-hidden border-l border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-4 xl:flex xl:flex-col xl:justify-center"
      aria-label="Plan execution sidebar"
      data-testid="desktop-plan-execution-sidebar"
    >
      <div className="grid min-w-0 max-w-full gap-3 overflow-hidden [&_*]:min-w-0">
        {singleRun ? (
          <PlanRunCard view={view} onOpenPlan={onEditPlan} />
        ) : (
          <ActiveCheckpointCard
            view={view}
            checkpoints={checkpoints}
            completedCount={completedCount}
            totalCount={totalCount}
            activeIndex={activeIndex}
            onOpenPlan={onEditPlan}
          />
        )}
        <ActionsCard
          view={view}
          busyAction={busyAction}
          canStop={canStop}
          onAction={onAction}
          onEditPlan={onEditPlan}
        />
      </div>
    </aside>
  )
})
