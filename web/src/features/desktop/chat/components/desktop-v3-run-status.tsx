import { LoaderCircle } from 'lucide-react'
import { cn } from '../../../../lib/cn'
import type { LiveRunOverlay, V3SessionRunIntent } from '../../state/desktop-v3-cache-types'

export type DesktopV3RunStatusKind = 'starting' | 'active' | 'paused' | 'completed' | 'failed' | 'stopped' | 'interrupted' | 'expired'

export interface DesktopV3RunStatusModel {
  kind: DesktopV3RunStatusKind
  label: string
  startedAt?: number
  endedAt?: number
  active: boolean
}

const ACTIVE_RUN_INTENT_STATUSES = new Set(['pending_executor', 'running'])
const TERMINAL_RUN_INTENT_STATUSES = new Set(['completed', 'failed', 'cancelled', 'interrupted', 'expired'])

function timestamp(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? value : undefined
}

function runIntentStatus(intent: V3SessionRunIntent | null | undefined): string {
  return intent?.status?.trim().toLowerCase() ?? ''
}

function liveRunActive(run: LiveRunOverlay): boolean {
  return run.status === 'pending_executor' || run.status === 'running'
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
  pendingStartAt?: number | null
}): DesktopV3RunStatusModel | null {
  const pendingStartAt = timestamp(input.pendingStartAt)
  if (pendingStartAt) {
    return { kind: 'starting', label: 'Starting', startedAt: pendingStartAt, active: true }
  }

  const currentStatus = runIntentStatus(input.currentRunIntent)
  if (currentStatus === 'dispatch_blocked') {
    return {
      kind: 'paused',
      label: 'Paused',
      startedAt: timestamp(input.currentRunIntent?.created_at),
      endedAt: timestamp(input.currentRunIntent?.updated_at),
      active: false,
    }
  }
  if (ACTIVE_RUN_INTENT_STATUSES.has(currentStatus)) {
    return {
      kind: currentStatus === 'pending_executor' ? 'starting' : 'active',
      label: currentStatus === 'pending_executor' ? 'Starting' : 'Running',
      startedAt: timestamp(input.currentRunIntent?.created_at),
      active: true,
    }
  }

  const latestStatus = runIntentStatus(input.latestRunIntent)
  if (latestStatus === 'dispatch_blocked') {
    return {
      kind: 'paused',
      label: 'Paused',
      startedAt: timestamp(input.latestRunIntent?.created_at),
      endedAt: timestamp(input.latestRunIntent?.updated_at),
      active: false,
    }
  }
  if (ACTIVE_RUN_INTENT_STATUSES.has(latestStatus)) {
    return {
      kind: latestStatus === 'pending_executor' ? 'starting' : 'active',
      label: latestStatus === 'pending_executor' ? 'Starting' : 'Running',
      startedAt: timestamp(input.latestRunIntent?.created_at),
      active: true,
    }
  }

  const liveActive = input.liveRuns?.some(liveRunActive) ?? false
  if (liveActive) {
    return { kind: 'active', label: 'Running', active: true }
  }

  if (TERMINAL_RUN_INTENT_STATUSES.has(latestStatus)) {
    const kind = terminalKind(latestStatus)
    return {
      kind,
      label: terminalLabel(kind),
      startedAt: timestamp(input.latestRunIntent?.created_at),
      endedAt: timestamp(input.latestRunIntent?.updated_at),
      active: false,
    }
  }

  return null
}

export function formatDesktopV3RunTimer(model: DesktopV3RunStatusModel, now: number): string {
  const startedAt = timestamp(model.startedAt)
  if (!startedAt) return ''
  const endAt = model.active ? now : timestamp(model.endedAt) ?? now
  const elapsedMs = Math.max(0, endAt - startedAt)
  const totalSeconds = Math.floor(elapsedMs / 1000)
  const seconds = totalSeconds % 60
  const minutes = Math.floor(totalSeconds / 60) % 60
  const hours = Math.floor(totalSeconds / 3600)
  if (hours > 0) {
    return `${hours}:${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`
  }
  return `${minutes}:${String(seconds).padStart(2, '0')}`
}

export function DesktopV3RunStatusPill({ model, now }: { model: DesktopV3RunStatusModel | null; now: number }) {
  if (!model) return null
  const timer = formatDesktopV3RunTimer(model, now)
  const spinning = model.kind === 'starting' || model.kind === 'active'
  return (
    <div
      className="inline-flex h-9 max-w-[12rem] shrink-0 items-center gap-1.5 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 text-[11px] font-medium text-[var(--app-text-muted)] shadow-sm sm:max-w-none sm:gap-2 sm:px-3"
      aria-live="polite"
      data-testid="desktop-v3-run-status-pill"
      title={timer ? `${model.label} · ${timer}` : model.label}
    >
      {spinning ? <LoaderCircle size={12} className="shrink-0 animate-spin text-[var(--app-primary)]" /> : <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', model.kind === 'paused' ? 'bg-[var(--app-warning)]' : 'bg-[var(--app-text-subtle)]')} />}
      <span className="truncate">{model.label}</span>
      {timer ? (
        <>
          <span className="shrink-0 text-[var(--app-text-subtle)]">·</span>
          <span className="shrink-0 tabular-nums text-[var(--app-text)]">{timer}</span>
        </>
      ) : null}
    </div>
  )
}
