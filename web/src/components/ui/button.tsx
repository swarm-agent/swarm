import { cn } from '../../lib/cn'
import type { ButtonHTMLAttributes, PropsWithChildren } from 'react'

type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'outline'

type ButtonSize = 'sm' | 'md'

interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant
  size?: ButtonSize
}

const variantClasses: Record<ButtonVariant, string> = {
  primary:
    'border border-transparent bg-[var(--app-primary)] text-[var(--app-primary-text)] hover:bg-[var(--app-primary-hover)] active:bg-[var(--app-primary-active)]',
  secondary:
    'border border-[var(--app-border)] bg-[var(--app-surface-subtle)] text-[var(--app-text)] hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] active:bg-[var(--app-surface-active)]',
  ghost:
    'border border-transparent bg-transparent text-[var(--app-text-muted)] hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)] active:bg-[var(--app-surface-hover)]',
  outline:
    'border border-[var(--app-border)] bg-transparent text-[var(--app-text)] hover:bg-[var(--app-surface-subtle)] active:bg-[var(--app-surface-hover)]',
}

const sizeClasses: Record<ButtonSize, string> = {
  sm: 'min-h-9 px-3 text-sm',
  md: 'min-h-10 px-4 text-sm',
}

export function Button({ className, variant = 'secondary', size = 'md', type = 'button', children, ...props }: PropsWithChildren<ButtonProps>) {
  return (
    <button
      type={type}
      className={cn(
        'inline-flex items-center justify-center gap-2 rounded-xl font-medium transition duration-150 disabled:cursor-not-allowed disabled:opacity-60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--app-bg)]',
        variantClasses[variant],
        sizeClasses[size],
        className,
      )}
      {...props}
    >
      {children}
    </button>
  )
}
