import { cn } from '../../../../lib/cn'
import type { LiveRunOverlay, V3SessionRunIntent } from '../../state/desktop-v3-cache-types'

export type DesktopV3RunStatusKind = 'starting' | 'active' | 'paused' | 'completed' | 'failed' | 'stopped' | 'interrupted' | 'expired'

export interface DesktopV3RunStatusModel {
  kind: DesktopV3RunStatusKind
  label: string
  startedAt?: number
  endedAt?: number
  durationMs?: number
  cumulativeDurationMs?: number
  active: boolean
}

const ACTIVE_RUN_INTENT_STATUSES = new Set(['pending_executor', 'running'])
const TERMINAL_RUN_INTENT_STATUSES = new Set(['completed', 'failed', 'cancelled', 'interrupted', 'expired'])

function timestamp(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : undefined
}

function duration(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : undefined
}

function runIntentStatus(intent: V3SessionRunIntent | null | undefined): string {
  return intent?.status?.trim().toLowerCase() ?? ''
}

function liveRunActive(run: LiveRunOverlay): boolean {
  return run.status === 'pending_executor' || run.status === 'running'
}

function timingPresent(timing: Pick<DesktopV3RunStatusModel, 'startedAt' | 'endedAt' | 'durationMs' | 'cumulativeDurationMs'>): boolean {
  return Boolean(timing.startedAt) || timing.durationMs !== undefined || timing.cumulativeDurationMs !== undefined
}

function runTiming(intent: V3SessionRunIntent | null | undefined, options: { active?: boolean } = {}): Pick<DesktopV3RunStatusModel, 'startedAt' | 'endedAt' | 'durationMs' | 'cumulativeDurationMs'> {
  const startedAt = timestamp(intent?.started_at)
    || (options.active ? timestamp(intent?.created_at) || timestamp(intent?.updated_at) : undefined)
  return {
    startedAt,
    endedAt: timestamp(intent?.completed_at),
    durationMs: duration(intent?.duration_ms),
    cumulativeDurationMs: duration(intent?.cumulative_duration_ms),
  }
}

function terminalKind(status: string): DesktopV3RunStatusKind {
  switch (status) {
    case 'failed':
      return 'failed'
    case 'cancelled':
      return 'stopped'
    case 'interrupted':
      return 'interrupted'
    case 'expired':
      return 'expired'
    default:
      return 'completed'
  }
}

function terminalLabel(kind: DesktopV3RunStatusKind): string {
  switch (kind) {
    case 'failed':
      return 'Failed'
    case 'stopped':
      return 'Stopped'
    case 'interrupted':
      return 'Interrupted'
    case 'expired':
      return 'Expired'
    default:
      return 'Completed'
  }
}

export function buildDesktopV3RunStatusModel(input: {
  currentRunIntent?: V3SessionRunIntent | null
  latestRunIntent?: V3SessionRunIntent | null
  liveRuns?: LiveRunOverlay[]
}): DesktopV3RunStatusModel | null {
  const currentStatus = runIntentStatus(input.currentRunIntent)
  if (currentStatus === 'dispatch_blocked') {
    return {
      kind: 'paused',
      label: 'Paused',
      ...runTiming(input.currentRunIntent),
      active: false,
    }
  }
  if (ACTIVE_RUN_INTENT_STATUSES.has(currentStatus)) {
    const timing = runTiming(input.currentRunIntent, { active: true })
    if (!timingPresent(timing)) return null
    return {
      kind: 'active',
      label: 'Running',
      ...timing,
      active: true,
    }
  }

  const latestStatus = runIntentStatus(input.latestRunIntent)
  if (latestStatus === 'dispatch_blocked') {
    return {
      kind: 'paused',
      label: 'Paused',
      ...runTiming(input.latestRunIntent),
      active: false,
    }
  }
  if (ACTIVE_RUN_INTENT_STATUSES.has(latestStatus)) {
    const timing = runTiming(input.latestRunIntent, { active: true })
    if (!timingPresent(timing)) return null
    return {
      kind: 'active',
      label: 'Running',
      ...timing,
      active: true,
    }
  }

  const liveActive = input.liveRuns?.some(liveRunActive) ?? false
  if (liveActive) {
    const timing = runTiming(input.currentRunIntent ?? input.latestRunIntent, { active: true })
    if (!timingPresent(timing)) {
      return null
    }
    return {
      kind: 'active',
      label: 'Running',
      ...timing,
      active: true,
    }
  }

  if (TERMINAL_RUN_INTENT_STATUSES.has(latestStatus)) {
    const kind = terminalKind(latestStatus)
    return {
      kind,
      label: terminalLabel(kind),
      ...runTiming(input.latestRunIntent),
      active: false,
    }
  }

  return null
}

function formatDurationMs(elapsedMs: number | undefined): string {
  if (elapsedMs === undefined) return ''
  const totalSeconds = Math.floor(elapsedMs / 1000)
  const seconds = totalSeconds % 60
  const minutes = Math.floor(totalSeconds / 60) % 60
  const hours = Math.floor(totalSeconds / 3600)
  if (hours > 0) {
    return `${hours}:${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`
  }
  return `${minutes}:${String(seconds).padStart(2, '0')}`
}

function currentRunDurationMs(model: DesktopV3RunStatusModel, now: number): number | undefined {
  const storedRunDurationMs = duration(model.durationMs)
  if (model.active) {
    const startedAt = timestamp(model.startedAt)
    return startedAt ? Math.max(0, now - startedAt) : storedRunDurationMs
  }
  return storedRunDurationMs
}

function totalRunDurationMs(model: DesktopV3RunStatusModel, now: number): number | undefined {
  const storedCumulativeMs = duration(model.cumulativeDurationMs)
  const runDurationMs = currentRunDurationMs(model, now)
  if (model.active && runDurationMs !== undefined) {
    return (storedCumulativeMs ?? 0) + runDurationMs
  }
  return storedCumulativeMs ?? runDurationMs
}

export function formatDesktopV3RunTimer(model: DesktopV3RunStatusModel, now: number): string {
  return formatDurationMs(totalRunDurationMs(model, now))
}

export function formatDesktopV3CurrentRunTimer(model: DesktopV3RunStatusModel, now: number): string {
  return formatDurationMs(currentRunDurationMs(model, now))
}

export const DESKTOP_V3_RUN_TIMER_TOOLTIP = 'Loop timer is the current run. Overall timer is cumulative agent run time for this session.'

export function formatDesktopV3RunTimerLabel(model: DesktopV3RunStatusModel, now: number): string {
  const runTimer = formatDesktopV3CurrentRunTimer(model, now)
  const totalTimer = formatDesktopV3RunTimer(model, now)
  const showTotalTimer = Boolean(totalTimer && totalTimer !== runTimer && duration(model.cumulativeDurationMs) !== undefined)
  if (!runTimer) return totalTimer
  return showTotalTimer ? `${runTimer} (${totalTimer})` : runTimer
}

export function DesktopV3RunStatusPill({ model, now, className }: { model: DesktopV3RunStatusModel | null; now: number; className?: string }) {
  if (!model) return null
  const runTimer = formatDesktopV3CurrentRunTimer(model, now)
  const totalTimer = formatDesktopV3RunTimer(model, now)
  const showTotalTimer = Boolean(totalTimer && totalTimer !== runTimer && duration(model.cumulativeDurationMs) !== undefined)
  const title = [
    model.label,
    runTimer ? `Loop timer: ${runTimer}` : '',
    showTotalTimer ? `Overall timer: ${totalTimer}` : '',
    runTimer ? DESKTOP_V3_RUN_TIMER_TOOLTIP : '',
  ].filter(Boolean).join(' · ')
  return (
    <div
      className={cn(
        'inline-flex h-8 max-w-[15rem] shrink-0 items-center gap-1.5 rounded-lg border border-[var(--app-border)] bg-transparent px-2.5 text-[11px] font-medium text-[var(--app-text-muted)] sm:max-w-none sm:gap-2 sm:px-3',
        className,
      )}
      aria-live="polite"
      data-testid="desktop-v3-run-status-pill"
      title={title || model.label}
    >
      <span className="truncate">{model.label}</span>
      {runTimer ? (
        <span className="shrink-0 tabular-nums text-[var(--app-text)]">
          {runTimer}
          {showTotalTimer ? <span className="text-[var(--app-text-subtle)]"> ({totalTimer})</span> : null}
        </span>
      ) : null}
    </div>
  )
}
