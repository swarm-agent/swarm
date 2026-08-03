import { GitBranch } from 'lucide-react'

export interface DesktopRoutedWorktreePrimeProps {
  requested: boolean
  onRequestedChange: (requested: boolean) => void
  disabled?: boolean
  readOnly?: boolean
  className?: string
}

/**
 * Removable pre-route control for boolean managed-worktree intent. The Router
 * remains the sole owner of any eventual worktree name and branch selection.
 */
export function DesktopRoutedWorktreePrime({
  requested,
  onRequestedChange,
  disabled = false,
  readOnly = false,
  className = '',
}: DesktopRoutedWorktreePrimeProps) {
  const label = readOnly
    ? `Managed worktree ${requested ? 'active' : 'inactive'}`
    : requested ? 'Disable managed worktree' : 'Use managed worktree'

  return (
    <button
      type="button"
      onClick={() => {
        if (!readOnly) onRequestedChange(!requested)
      }}
      disabled={disabled}
      aria-readonly={readOnly || undefined}
      aria-label={label}
      aria-pressed={requested}
      title={label}
      data-testid="desktop-routed-worktree-prime"
      data-worktree-requested={requested ? 'true' : 'false'}
      className={`inline-flex min-h-9 min-w-9 shrink-0 items-center justify-center rounded-lg border bg-transparent p-2 text-[var(--app-text-muted)] transition-all hover:-translate-y-0.5 hover:text-[var(--app-text)] hover:shadow-sm disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:translate-y-0 disabled:hover:shadow-none ${requested ? 'border-[var(--app-border-accent)] bg-[var(--app-surface-hover)] text-[var(--app-text)] ring-1 ring-[var(--app-border-accent)]' : 'border-transparent'} ${className}`}
    >
      <GitBranch size={15} strokeWidth={requested ? 2.25 : 1.75} aria-hidden="true" />
    </button>
  )
}
