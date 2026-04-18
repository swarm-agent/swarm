import { cn } from '../../lib/cn'
import type { HTMLAttributes, PropsWithChildren } from 'react'

export function Dialog({ className, children, ...props }: PropsWithChildren<HTMLAttributes<HTMLDivElement>>) {
  return <div className={cn('fixed inset-0 z-[60] grid place-items-center p-6 max-sm:p-3', className)} {...props}>{children}</div>
}

export function DialogBackdrop({ className, ...props }: HTMLAttributes<HTMLDivElement>) {
  return <div className={cn('absolute inset-0 bg-[var(--app-backdrop)]', className)} {...props} />
}

export function DialogPanel({ className, children, ...props }: PropsWithChildren<HTMLAttributes<HTMLElement>>) {
  return (
    <section
      className={cn(
        'relative flex max-h-[min(860px,calc(100vh-48px))] w-[min(1180px,100%)] flex-col overflow-hidden rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] p-5 max-sm:max-h-[calc(100vh-24px)] max-sm:p-4',
        className,
      )}
      {...props}
    >
      {children}
    </section>
  )
}
