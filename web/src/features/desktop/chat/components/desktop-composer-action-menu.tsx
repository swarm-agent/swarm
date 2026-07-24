import { useEffect, useId, useRef, useState } from 'react'
import { ListChecks, ListTodo, Plus } from 'lucide-react'

export type DesktopComposerTaskMode = 'action' | 'plan'

interface DesktopComposerActionMenuProps {
  disabled?: boolean
  onPrimeTask: (mode: DesktopComposerTaskMode) => void
}

const TASK_EXPLANATION = 'Your next message will be sent to a background agent in a worktree.'

export function DesktopComposerActionMenu({ disabled = false, onPrimeTask }: DesktopComposerActionMenuProps) {
  const [open, setOpen] = useState(false)
  const menuId = useId()
  const rootRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return

    const handlePointerDown = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false)
    }
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false)
    }

    document.addEventListener('pointerdown', handlePointerDown)
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('pointerdown', handlePointerDown)
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [open])

  useEffect(() => {
    if (disabled) setOpen(false)
  }, [disabled])

  const primeTask = (mode: DesktopComposerTaskMode) => {
    setOpen(false)
    onPrimeTask(mode)
  }

  return (
    <div ref={rootRef} className="relative shrink-0 self-end pb-0.5" data-testid="desktop-composer-action-menu">
      <button
        type="button"
        disabled={disabled}
        aria-label="Open composer actions"
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={() => setOpen((current) => !current)}
        className="inline-flex h-9 w-9 items-center justify-center rounded-lg border-0 bg-transparent p-0 text-[var(--app-text-muted)] shadow-none transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:bg-[var(--app-surface-hover)] focus-visible:text-[var(--app-text)] focus-visible:outline-none focus-visible:ring-0 disabled:cursor-not-allowed disabled:opacity-50"
      >
        <Plus size={18} aria-hidden="true" />
      </button>

      {open ? (
        <div
          id={menuId}
          role="dialog"
          aria-label="Composer actions"
          className="absolute bottom-full left-0 z-40 mb-2 w-[min(24rem,calc(100vw-2rem))] rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-2 shadow-[var(--shadow-panel)]"
        >
          <div className="flex min-w-0 items-center justify-between gap-3 rounded-lg px-2 py-1.5" data-testid="desktop-composer-task-row">
            <span className="group/task relative flex min-w-0 items-center gap-2 text-sm font-medium text-[var(--app-text)]">
              <ListTodo size={16} className="shrink-0 text-[var(--app-text-muted)]" aria-hidden="true" />
              <span>Task</span>
              <span
                role="tooltip"
                className="pointer-events-none absolute bottom-full left-0 z-50 mb-2 hidden w-64 rounded-lg bg-[var(--app-text)] px-3 py-2 text-xs font-normal leading-5 text-[var(--app-surface)] shadow-[var(--shadow-panel)] group-hover/task:block group-focus-within/task:block"
              >
                {TASK_EXPLANATION}
              </span>
            </span>
            <span className="flex shrink-0 items-center gap-1">
              <button
                type="button"
                onClick={() => primeTask('action')}
                className="inline-flex h-8 items-center gap-1.5 rounded-lg border-0 bg-transparent px-2.5 text-xs font-medium text-[var(--app-text-muted)] transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--app-border-accent)]"
              >
                <ListTodo size={14} aria-hidden="true" />
                <span>Action</span>
              </button>
              <button
                type="button"
                title="Prime a task for plan review before it starts"
                onClick={() => primeTask('plan')}
                className="inline-flex h-8 items-center gap-1.5 rounded-lg border-0 bg-transparent px-2.5 text-xs font-medium text-[var(--app-text-muted)] transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--app-border-accent)]"
              >
                <ListChecks size={14} aria-hidden="true" />
                <span>Plan</span>
              </button>
            </span>
          </div>
        </div>
      ) : null}
    </div>
  )
}
