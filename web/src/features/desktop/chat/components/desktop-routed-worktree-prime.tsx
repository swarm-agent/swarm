import { GitBranch } from 'lucide-react'

export interface DesktopRoutedWorktreePrimeProps {
  disabled?: boolean
  className?: string
}

/** Read-only status: routed Desktop sessions always use managed worktree isolation. */
export function DesktopRoutedWorktreePrime({
  disabled = false,
  className = '',
}: DesktopRoutedWorktreePrimeProps) {
  return (
    <span
      aria-label="Managed worktree isolation active"
      title="Managed worktree isolation is always active"
      data-testid="desktop-routed-worktree-prime"
      className={`inline-flex min-h-9 min-w-9 shrink-0 items-center justify-center rounded-lg border border-[var(--app-border-accent)] bg-[var(--app-surface-hover)] p-2 text-[var(--app-text)] ring-1 ring-[var(--app-border-accent)] ${disabled ? 'opacity-50' : ''} ${className}`}
    >
      <GitBranch size={15} strokeWidth={2.25} aria-hidden="true" />
    </span>
  )
}
