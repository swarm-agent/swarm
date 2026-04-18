import { GitBranch } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { cn } from '../../../../lib/cn'

interface WorkspaceWorktreeToggleProps {
  enabled: boolean
  busy?: boolean
  onToggle: () => void
  className?: string
}

export function WorkspaceWorktreeToggle({ enabled, busy = false, onToggle, className }: WorkspaceWorktreeToggleProps) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="md"
      className={cn(
        'h-12 w-12 min-h-12 min-w-12 rounded-xl p-0 transition-[background-color,border-color,color,opacity,box-shadow] duration-150 ease-out',
        enabled
          ? 'border border-[var(--app-border-strong)] bg-[color-mix(in_oklab,var(--app-surface-subtle)_82%,var(--app-selection)_18%)] text-[var(--app-text)] shadow-[inset_0_0_0_1px_color-mix(in_oklab,var(--app-border-strong)_38%,transparent)] hover:border-[var(--app-border-strong)] hover:bg-[color-mix(in_oklab,var(--app-surface-hover)_76%,var(--app-selection)_24%)]'
          : 'border border-[var(--app-border)] bg-transparent text-[var(--app-text-subtle)] opacity-55 hover:opacity-80 hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)]',
        busy && 'cursor-progress',
        className,
      )}
      onClick={(event) => {
        event.stopPropagation()
        if (busy) {
          return
        }
        onToggle()
      }}
      aria-busy={busy}
      aria-disabled={busy}
      aria-pressed={enabled}
      aria-label={enabled ? 'Turn worktrees off for this workspace' : 'Turn worktrees on for this workspace'}
      title={busy
        ? `Updating worktree setting. New sessions will use worktrees ${enabled ? 'on' : 'off'} when the save finishes.`
        : enabled
          ? 'Worktrees on for new sessions. Click to turn them off.'
          : 'Worktrees off for new sessions. Click to turn them on.'}
    >
      <GitBranch size={20} />
    </Button>
  )
}
