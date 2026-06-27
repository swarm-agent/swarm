import {
  PlayCircle,
  RefreshCcw,
  ShieldCheck,
  SquarePen,
} from 'lucide-react'
import { cn } from '../../../../lib/cn'
import { Button } from '../../../../components/ui/button'
import type { DesktopSessionPlanCheckpoint } from '../types/chat'
import type { DesktopPlanExecutionView } from '../../state/desktop-v3-cache-selectors'
import type { DesktopPlanExecutionAction } from '../../session-v3/plan-execution-api'

interface DesktopPlanExecutionSidebarActionInput {
  action: DesktopPlanExecutionAction
  checkpointId?: string
  continueAutomatically?: boolean
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

function actionBusyKey(action: DesktopPlanExecutionAction, checkpointId?: string): string {
  return `${action}:${checkpointId ?? ''}`
}

function checkpointIsRunnable(checkpoint?: DesktopSessionPlanCheckpoint): boolean {
  const status = checkpoint?.status.trim().toLowerCase() ?? ''
  return status === '' || status === 'pending' || status === 'idle' || status === 'ready'
}

function statusLabel(view: DesktopPlanExecutionView, checkpoint?: DesktopSessionPlanCheckpoint): string {
  if (view.reviewRequired) return 'Waiting review'
  if (view.completed) return 'Completed'
  if (view.blocked) return 'Blocked'
  if (view.failed) return 'Failed'
  return humanize(checkpoint?.status || view.status || 'Ready')
}

function StatusBadge({ label, tone }: { label: string; tone: Tone }) {
  return (
    <span className={cn('inline-flex min-w-0 items-center rounded-full border px-2 py-0.5 text-[10px] font-semibold uppercase tracking-[0.08em]', toneBadgeClass(tone))}>
      <span className="truncate">{label}</span>
    </span>
  )
}

function ActiveCheckpointCard({ view, checkpoints, completedCount, totalCount, activeIndex, onOpenPlan }: { view: DesktopPlanExecutionView; checkpoints: DesktopSessionPlanCheckpoint[]; completedCount: number; totalCount: number; activeIndex: number; onOpenPlan?: () => void }) {
  const checkpoint = view.activeCheckpoint
  const fallbackIndex = activeIndex >= 0 ? activeIndex : 0
  const checkpointId = checkpoint ? displayCheckpointId(checkpoint.id, fallbackIndex) : 'None'
  const title = checkpoint?.title || 'No active checkpoint'
  const description = checkpoint?.objective || checkpoint?.tasks?.[0] || checkpoint?.acceptanceCriteria?.[0] || 'No checkpoint description is available.'
  const nextCheckpoint = checkpoints.find((candidate, index) => index > fallbackIndex && candidate.status.toLowerCase() !== 'completed')
  const nextIndex = nextCheckpoint ? checkpoints.findIndex((candidate) => candidate === nextCheckpoint) : -1
  const progressValue = totalCount > 0 ? Math.max(0, Math.min(100, (completedCount / totalCount) * 100)) : 0
  const tone = statusTone(view.reviewRequired ? 'needs_review' : checkpoint?.status || view.status, Boolean(checkpoint))

  return (
    <section className="min-w-0 overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-3">
      <div className="flex min-w-0 items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Active checkpoint</div>
          <div className="mt-1 flex min-w-0 items-center gap-2">
            <span className="shrink-0 font-mono text-xs font-semibold text-[var(--app-primary)]">{checkpointId}</span>
            <h3 className="truncate text-sm font-semibold leading-snug text-[var(--app-text)]">{title}</h3>
          </div>
        </div>
        <StatusBadge label={statusLabel(view, checkpoint)} tone={tone} />
      </div>

      <p className="mt-2 line-clamp-2 text-xs leading-5 text-[var(--app-text-muted)]">{description}</p>

      <div className="mt-3">
        <div className="mb-1 flex items-center justify-between text-[11px] text-[var(--app-text-muted)]">
          <span>Progress</span>
          <span className="font-medium text-[var(--app-text)]">{completedCount} / {totalCount}</span>
        </div>
        <div className="h-1.5 overflow-hidden rounded-full bg-[var(--app-surface-subtle)]">
          <div className="h-full rounded-full bg-[var(--app-primary)]" style={{ width: `${progressValue}%` }} />
        </div>
      </div>

      <div className="mt-3 min-w-0 overflow-hidden border-t border-[var(--app-border)] pt-2 text-xs">
        <span className="text-[var(--app-text-subtle)]">Next up</span>{' '}
        {nextCheckpoint ? (
          <span className="block min-w-0 truncate text-[var(--app-text-muted)]">
            <span className="font-mono font-semibold text-[var(--app-text)]">{displayCheckpointId(nextCheckpoint.id, nextIndex)}</span>
            {' '}{nextCheckpoint.title || 'Untitled checkpoint'}
          </span>
        ) : (
          <span className="text-[var(--app-text-muted)]">No remaining checkpoint</span>
        )}
      </div>

      <Button type="button" size="sm" variant="outline" onClick={onOpenPlan} disabled={!onOpenPlan} className="mt-3 w-full">
        Open full plan
      </Button>
    </section>
  )
}

function PlanModeSwitch({ checked, busy, disabled, onToggle }: { checked: boolean; busy: boolean; disabled: boolean; onToggle: () => void }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      disabled={disabled || busy}
      onClick={onToggle}
      className={cn(
        'relative inline-flex h-6 w-11 shrink-0 items-center rounded-full border transition disabled:cursor-not-allowed disabled:opacity-60',
        checked ? 'border-[var(--app-primary-border)] bg-[var(--app-primary)]' : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)]',
      )}
    >
      <span className={cn('inline-block size-4 rounded-full bg-[var(--app-surface)] transition', checked ? 'translate-x-5' : 'translate-x-1')} />
    </button>
  )
}

function ActionsCard({ view, busyAction, canStop, onAction, onEditPlan }: DesktopPlanExecutionSidebarProps & { view: DesktopPlanExecutionView }) {
  const checkpointId = view.activeCheckpointId || view.activeCheckpoint?.id || ''
  const automatic = view.policyMode === 'automatic'
  const autoBusy = busyAction === actionBusyKey('set_automatic_mode', checkpointId)
  const acceptBusy = busyAction === actionBusyKey('accept_checkpoint', checkpointId)
  const acceptContinueBusy = busyAction === actionBusyKey('accept_and_continue', checkpointId)
  const continueBusy = busyAction === actionBusyKey('continue_checkpoint', checkpointId)
  const restartBusy = busyAction === actionBusyKey('restart_checkpoint', checkpointId)
  const canAccept = Boolean(onAction && checkpointId && view.reviewRequired && !view.blocked && !view.failed && !view.completed && !canStop)
  const canContinue = Boolean(onAction && checkpointId && !view.reviewRequired && !view.blocked && !view.failed && !view.completed && !canStop && checkpointIsRunnable(view.activeCheckpoint))
  const canRestart = Boolean(onAction && checkpointId && !view.completed && !canStop)
  const canToggleAutomatic = Boolean(onAction && view.policyShape !== 'single_run' && !view.completed && !canStop)

  return (
    <section className="min-w-0 overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] p-3">
      <div className="text-[11px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">Actions / Plan Mode</div>
      <div className="mt-3 flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="text-sm font-medium text-[var(--app-text)]">Automatic mode</div>
          <p className="mt-0.5 line-clamp-2 text-xs leading-5 text-[var(--app-text-muted)]">Advance automatically when the plan policy allows it.</p>
        </div>
        <PlanModeSwitch
          checked={automatic}
          busy={autoBusy}
          disabled={!canToggleAutomatic}
          onToggle={() => void onAction?.({ action: 'set_automatic_mode', checkpointId, continueAutomatically: !automatic })}
        />
      </div>

      <div className="mt-3 grid gap-2">
        <Button
          type="button"
          size="sm"
          variant="primary"
          onClick={() => void onAction?.({ action: 'accept_and_continue', checkpointId })}
          disabled={!canAccept || acceptContinueBusy}
        >
          <ShieldCheck className={cn('size-4', acceptContinueBusy ? 'animate-pulse' : '')} />
          Accept &amp; move to next
        </Button>
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => void onAction?.({ action: 'accept_checkpoint', checkpointId })}
          disabled={!canAccept || acceptBusy}
        >
          <ShieldCheck className={cn('size-4', acceptBusy ? 'animate-pulse' : '')} />
          Accept this checkpoint
        </Button>
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => void onAction?.({ action: 'continue_checkpoint', checkpointId })}
          disabled={!canContinue || continueBusy}
        >
          <PlayCircle className={cn('size-4', continueBusy ? 'animate-pulse' : '')} />
          Continue checkpoint
        </Button>
      </div>

      <div className="mt-3 grid grid-cols-2 gap-2 border-t border-[var(--app-border)] pt-3">
        <Button
          type="button"
          size="sm"
          variant="ghost"
          onClick={() => void onAction?.({ action: 'restart_checkpoint', checkpointId })}
          disabled={!canRestart || restartBusy}
        >
          <RefreshCcw className={cn('size-4', restartBusy ? 'animate-pulse' : '')} />
          Restart
        </Button>
        <Button type="button" size="sm" variant="ghost" onClick={onEditPlan} disabled={!onEditPlan}>
          <SquarePen className="size-4" />
          Edit plan
        </Button>
      </div>
    </section>
  )
}

export function DesktopPlanExecutionSidebar({ view, busyAction, canStop = false, onAction, onStop: _onStop, onEditPlan }: DesktopPlanExecutionSidebarProps) {
  const document = view?.plan.document ?? null
  if (!view || !document) return null

  const checkpoints = document.checkpoints
  const completedCount = checkpoints.filter((checkpoint) => checkpoint.status.toLowerCase() === 'completed').length
  const totalCount = checkpoints.length
  const activeIndex = view.activeCheckpoint
    ? checkpoints.findIndex((checkpoint) => checkpoint.id === view.activeCheckpoint?.id)
    : -1

  return (
    <aside
      className="hidden min-h-0 min-w-0 w-[360px] max-w-[360px] overflow-hidden border-l border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-4 xl:flex xl:flex-col xl:justify-center"
      aria-label="Plan execution sidebar"
      data-testid="desktop-plan-execution-sidebar"
    >
      <div className="grid min-w-0 max-w-full gap-3 overflow-hidden [&_*]:min-w-0">
        <ActiveCheckpointCard
          view={view}
          checkpoints={checkpoints}
          completedCount={completedCount}
          totalCount={totalCount}
          activeIndex={activeIndex}
          onOpenPlan={onEditPlan}
        />
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
}
