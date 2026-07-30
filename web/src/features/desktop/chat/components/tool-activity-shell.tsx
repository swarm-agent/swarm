import type { ReactNode } from 'react'
import { CheckCircle2, CircleStop, LoaderCircle, XCircle } from 'lucide-react'
import { cn } from '../../../../lib/cn'
import { getToolTheme } from '../services/tool-theme'
import { toolActivityPresentation } from '../services/tool-activity'
import type { StructuredToolMessage } from '../types/chat'

export interface ToolActivityShellProps {
  toolMessage: StructuredToolMessage
  lifecycleStatus?: string
  summary?: string
  children?: ReactNode
  className?: string
  bodyClassName?: string
  testId?: string
}

function activityToneClass(state: ReturnType<typeof toolActivityPresentation>['state']): string {
  switch (state) {
    case 'error':
      return 'border-[color-mix(in_srgb,var(--app-danger)_38%,var(--app-border))]'
    case 'cancelled':
      return 'border-[color-mix(in_srgb,var(--app-text-muted)_34%,var(--app-border))]'
    case 'running':
      return 'border-[color-mix(in_srgb,var(--app-primary)_34%,var(--app-border))]'
    default:
      return 'border-[var(--app-border)]'
  }
}

export function ToolActivityShell({
  toolMessage,
  lifecycleStatus = '',
  summary = '',
  children,
  className,
  bodyClassName,
  testId = 'desktop-tool-activity-card',
}: ToolActivityShellProps) {
  const theme = getToolTheme(toolMessage.tool)
  const ToolIcon = theme.icon
  const activity = toolActivityPresentation(toolMessage.tool, toolMessage.state, lifecycleStatus)
  const StateIcon = activity.state === 'error'
    ? XCircle
    : activity.state === 'cancelled'
      ? CircleStop
      : activity.state === 'running'
        ? LoaderCircle
        : CheckCircle2
  const active = activity.state === 'running'
  const stateColor = activity.state === 'error'
    ? 'text-[var(--app-danger)]'
    : activity.state === 'cancelled'
      ? 'text-[var(--app-text-muted)]'
      : active
        ? 'text-[var(--app-primary)]'
        : 'text-[var(--app-success)]'

  return (
    <section
      className={cn(
        'tool-activity-card min-h-[4.25rem] w-full min-w-0 overflow-hidden rounded-xl border bg-[var(--app-surface-subtle)] shadow-sm transition-[border-color,background-color] duration-200',
        activityToneClass(activity.state),
        active && 'motion-safe:animate-[pulse_2.8s_ease-in-out_infinite] motion-reduce:animate-none',
        className,
      )}
      data-testid={testId}
      data-tool-activity-kind={activity.kind}
      data-tool-activity-state={activity.state}
      aria-busy={active || undefined}
      role={active ? 'status' : 'group'}
      aria-live={active ? 'polite' : undefined}
      aria-atomic={active ? 'true' : undefined}
    >
      <header className="flex min-w-0 items-center gap-2.5 px-3 py-2.5">
        <span
          className="grid size-7 shrink-0 place-items-center rounded-lg"
          style={{ color: theme.color, backgroundColor: `color-mix(in srgb, ${theme.color} 14%, transparent)` }}
          aria-hidden="true"
        >
          <ToolIcon size={13} />
        </span>
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13px] font-semibold leading-5 text-[var(--app-text)]" title={activity.title}>
            {activity.title}
          </div>
          {summary ? (
            <div className="truncate text-[11px] leading-4 text-[var(--app-text-muted)]" title={summary}>
              {summary}
            </div>
          ) : null}
        </div>
        <span className={cn('inline-flex shrink-0 items-center gap-1 text-[10px] font-medium', stateColor)}>
          <StateIcon size={12} className={cn(active && 'motion-safe:animate-spin motion-reduce:animate-none')} aria-hidden="true" />
          <span className="hidden min-[360px]:inline">{activity.statusLabel}</span>
        </span>
      </header>
      {children ? <div className={cn('min-w-0 border-t border-[var(--app-border)]', bodyClassName)}>{children}</div> : null}
    </section>
  )
}
