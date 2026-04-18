import { cn } from '../../lib/cn'
import type { TextareaHTMLAttributes } from 'react'

export function Textarea({ className, wrap = 'soft', ...props }: TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return (
    <textarea
      wrap={wrap}
      className={cn(
        'min-h-[110px] min-w-0 w-full overflow-x-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2.5 whitespace-pre-wrap break-words [overflow-wrap:anywhere] text-[var(--app-text)] outline-none transition placeholder:text-[var(--app-text-subtle)] hover:border-[var(--app-border-strong)] focus-visible:border-[var(--app-border-accent)] focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--app-bg)] disabled:bg-[var(--app-bg-inset)]',
        className,
      )}
      {...props}
    />
  )
}
