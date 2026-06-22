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
      return 'Turn failed'
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
      label: 'User paused session',
      startedAt: timestamp(input.currentRunIntent?.created_at),
      endedAt: timestamp(input.currentRunIntent?.updated_at),
      active: false,
    }
  }
  if (ACTIVE_RUN_INTENT_STATUSES.has(currentStatus)) {
    return {
      kind: currentStatus === 'pending_executor' ? 'starting' : 'active',
      label: currentStatus === 'pending_executor' ? 'Starting' : 'Swarming',
      startedAt: timestamp(input.currentRunIntent?.created_at),
      active: true,
    }
  }

  const latestStatus = runIntentStatus(input.latestRunIntent)
  if (latestStatus === 'dispatch_blocked') {
    return {
      kind: 'paused',
      label: 'User paused session',
      startedAt: timestamp(input.latestRunIntent?.created_at),
      endedAt: timestamp(input.latestRunIntent?.updated_at),
      active: false,
    }
  }
  if (ACTIVE_RUN_INTENT_STATUSES.has(latestStatus)) {
    return {
      kind: latestStatus === 'pending_executor' ? 'starting' : 'active',
      label: latestStatus === 'pending_executor' ? 'Starting' : 'Swarming',
      startedAt: timestamp(input.latestRunIntent?.created_at),
      active: true,
    }
  }

  const liveActive = input.liveRuns?.some(liveRunActive) ?? false
  if (liveActive) {
    return { kind: 'active', label: 'Swarming', active: true }
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

export function DesktopV3RunStatusBar({ model, now }: { model: DesktopV3RunStatusModel | null; now: number }) {
  if (!model) return null
  const timer = formatDesktopV3RunTimer(model, now)
  const spinning = model.kind === 'starting' || model.kind === 'active'
  return (
    <div className="shrink-0 border-t border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-2 sm:px-6" aria-live="polite" data-testid="desktop-v3-run-status-bar">
      <div className="mx-auto flex w-full max-w-3xl items-center justify-center">
        <div className="inline-flex items-center gap-2 rounded-full border border-[var(--app-border)] bg-[var(--app-surface)] px-3 py-1 text-[11px] font-medium text-[var(--app-text-muted)] shadow-sm">
          {spinning ? <LoaderCircle size={12} className="animate-spin text-[var(--app-primary)]" /> : <span className={cn('h-1.5 w-1.5 rounded-full', model.kind === 'paused' ? 'bg-[var(--app-warning)]' : 'bg-[var(--app-text-subtle)]')} />}
          <span>{model.label}</span>
          {timer ? (
            <>
              <span className="text-[var(--app-text-subtle)]">·</span>
              <span className="tabular-nums text-[var(--app-text)]">{timer}</span>
            </>
          ) : null}
        </div>
      </div>
    </div>
  )
}
