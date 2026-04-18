import { X } from 'lucide-react'
import type { ButtonHTMLAttributes } from 'react'
import { cn } from '../../lib/cn'
import { Button } from './button'

export function ModalCloseButton({ className, ...props }: ButtonHTMLAttributes<HTMLButtonElement>) {
  return (
    <Button
      variant="ghost"
      className={cn(
        'h-11 w-11 min-w-11 shrink-0 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-elevated)] p-0 text-[var(--app-text-muted)] shadow-[var(--shadow-soft)] hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] active:bg-[var(--app-surface-active)]',
        className,
      )}
      {...props}
    >
      <X size={21} strokeWidth={2.25} />
    </Button>
  )
}
