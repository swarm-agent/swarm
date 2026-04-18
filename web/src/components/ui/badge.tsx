import { cn } from '../../lib/cn'
import type { HTMLAttributes, PropsWithChildren } from 'react'

type BadgeTone = 'live' | 'warning' | 'danger' | 'neutral'

interface BadgeProps extends HTMLAttributes<HTMLSpanElement> {
  tone?: BadgeTone
}

const toneClasses: Record<BadgeTone, string> = {
  live: 'border-[var(--app-success-border)] bg-[var(--app-success-bg)] text-[var(--app-success)]',
  warning: 'border-[var(--app-warning-border)] bg-[var(--app-warning-bg)] text-[var(--app-warning)]',
  danger: 'border-[var(--app-danger-border)] bg-[var(--app-danger-bg)] text-[var(--app-danger)]',
  neutral: 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text-muted)]',
}

export function Badge({ className, children, tone = 'neutral', ...props }: PropsWithChildren<BadgeProps>) {
  return (
    <span
      className={cn(
        'inline-flex min-h-[22px] items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-medium shadow-sm',
        toneClasses[tone],
        className,
      )}
      {...props}
    >
      {tone === 'live' && <span className="h-1.5 w-1.5 rounded-full bg-[var(--app-success)]" />}
      {tone === 'warning' && <span className="h-1.5 w-1.5 rounded-full bg-[var(--app-warning)]" />}
      {tone === 'danger' && <span className="h-1.5 w-1.5 rounded-full bg-[var(--app-danger)]" />}
      {tone === 'neutral' && <span className="h-1.5 w-1.5 rounded-full bg-[var(--app-text-muted)] opacity-50" />}
      {children}
    </span>
  )
}
