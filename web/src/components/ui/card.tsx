import { cn } from '../../lib/cn'
import type { HTMLAttributes, PropsWithChildren } from 'react'

export function Card({ className, children, ...props }: PropsWithChildren<HTMLAttributes<HTMLDivElement>>) {
  return (
    <div
      className={cn(
        'rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)]',
        className,
      )}
      {...props}
    >
      {children}
    </div>
  )
}
