import { cn } from '../../lib/cn'
import type { SelectHTMLAttributes } from 'react'

export function Select({ className, children, ...props }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select
      className={cn(
        'w-full min-h-10 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2.5 text-[var(--app-text)] outline-none transition hover:border-[var(--app-border-strong)] focus-visible:border-[var(--app-border-accent)] focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--app-bg)] disabled:bg-[var(--app-bg-inset)]',
        className,
      )}
      {...props}
    >
      {children}
    </select>
  )
}
