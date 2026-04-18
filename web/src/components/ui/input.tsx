import { forwardRef, type InputHTMLAttributes } from 'react'
import { cn } from '../../lib/cn'

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(function Input(
  { className, ...props },
  ref,
) {
  return (
    <input
      ref={ref}
      className={cn(
        'w-full min-h-10 rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-3 py-2.5 text-[var(--app-text)] outline-none transition placeholder:text-[var(--app-text-subtle)] hover:border-[var(--app-border-strong)] focus-visible:border-[var(--app-border-accent)] focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--app-bg)] disabled:bg-[var(--app-bg-inset)]',
        className,
      )}
      {...props}
    />
  )
})
