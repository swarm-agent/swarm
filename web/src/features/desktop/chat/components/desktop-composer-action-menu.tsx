import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { ChevronLeft, ChevronRight, ListChecks, ListTodo, Plus } from 'lucide-react'

export type DesktopComposerTaskMode = 'action' | 'plan'

interface DesktopComposerActionMenuProps {
  disabled?: boolean
  onPrimeTask: (mode: DesktopComposerTaskMode) => void
}

type ComposerActionMenuView = 'root' | 'task'

const TASK_EXPLANATION = 'Send your next message to a background agent in a managed worktree.'

export function DesktopComposerActionMenu({ disabled = false, onPrimeTask }: DesktopComposerActionMenuProps) {
  const [open, setOpen] = useState(false)
  const [view, setView] = useState<ComposerActionMenuView>('root')
  const menuId = useId()
  const rootRef = useRef<HTMLDivElement>(null)

  const closeMenu = useCallback(() => {
    setOpen(false)
    setView('root')
  }, [])

  useEffect(() => {
    if (!open) return

    const handlePointerDown = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) closeMenu()
    }
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') closeMenu()
    }

    document.addEventListener('pointerdown', handlePointerDown)
    document.addEventListener('keydown', handleKeyDown)
    return () => {
      document.removeEventListener('pointerdown', handlePointerDown)
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [closeMenu, open])

  useEffect(() => {
    if (disabled) closeMenu()
  }, [closeMenu, disabled])

  const toggleMenu = () => {
    if (open) {
      closeMenu()
      return
    }
    setView('root')
    setOpen(true)
  }

  const primeTask = (mode: DesktopComposerTaskMode) => {
    closeMenu()
    onPrimeTask(mode)
  }

  return (
    <div ref={rootRef} className="relative shrink-0 self-end pb-0.5" data-testid="desktop-composer-action-menu">
      <button
        type="button"
        disabled={disabled}
        aria-label="Open composer actions"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={toggleMenu}
        className="inline-flex h-9 w-9 items-center justify-center rounded-lg border-0 bg-transparent p-0 text-[var(--app-text-muted)] shadow-none transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:bg-[var(--app-surface-hover)] focus-visible:text-[var(--app-text)] focus-visible:outline-none focus-visible:ring-0 disabled:cursor-not-allowed disabled:opacity-50"
      >
        <Plus size={18} aria-hidden="true" />
      </button>

      {open ? (
        <div
          id={menuId}
          role="menu"
          aria-label={view === 'task' ? 'Task type' : 'Composer actions'}
          className="absolute bottom-full left-0 z-40 mb-2 w-[min(18rem,calc(100vw-2rem))] overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-1.5 shadow-[var(--shadow-panel)]"
          data-testid="desktop-composer-actions-menu"
        >
          {view === 'root' ? (
            <button
              type="button"
              role="menuitem"
              aria-haspopup="menu"
              onClick={() => setView('task')}
              className="flex w-full items-center gap-3 rounded-lg px-2.5 py-2.5 text-left text-sm text-[var(--app-text)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] focus-visible:bg-[var(--app-surface-hover)]"
              data-testid="desktop-composer-task-menu-item"
            >
              <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-[var(--app-bg-alt)] text-[var(--app-primary)]">
                <ListTodo size={16} aria-hidden="true" />
              </span>
              <span className="min-w-0 flex-1">
                <span className="block font-semibold">Task</span>
                <span className="block text-[11px] leading-4 text-[var(--app-text-subtle)]">Run work in the background</span>
              </span>
              <ChevronRight size={16} className="shrink-0 text-[var(--app-text-subtle)]" aria-hidden="true" />
            </button>
          ) : (
            <div data-testid="desktop-composer-task-submenu">
              <button
                type="button"
                onClick={() => setView('root')}
                className="mb-1 flex w-full items-center gap-2 rounded-lg px-2.5 py-2 text-left text-xs font-semibold text-[var(--app-text-muted)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:bg-[var(--app-surface-hover)]"
                aria-label="Back to composer actions"
              >
                <ChevronLeft size={15} aria-hidden="true" />
                <span>Task</span>
              </button>
              <p className="px-2.5 pb-2 text-[11px] leading-4 text-[var(--app-text-subtle)]">{TASK_EXPLANATION}</p>
              <button
                type="button"
                role="menuitem"
                onClick={() => primeTask('plan')}
                className="flex w-full items-center gap-3 rounded-lg px-2.5 py-2.5 text-left text-sm text-[var(--app-text-muted)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:bg-[var(--app-surface-hover)] focus-visible:text-[var(--app-text)]"
              >
                <ListChecks size={17} className="shrink-0" aria-hidden="true" />
                <span className="min-w-0 flex-1">
                  <span className="block font-medium">Plan</span>
                  <span className="block text-[11px] leading-4 text-[var(--app-text-subtle)]">Review the approach before work starts</span>
                </span>
              </button>
              <button
                type="button"
                role="menuitem"
                onClick={() => primeTask('action')}
                className="flex w-full items-center gap-3 rounded-lg px-2.5 py-2.5 text-left text-sm text-[var(--app-text-muted)] outline-none transition-colors hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] focus-visible:bg-[var(--app-surface-hover)] focus-visible:text-[var(--app-text)]"
              >
                <ListTodo size={17} className="shrink-0" aria-hidden="true" />
                <span className="min-w-0 flex-1">
                  <span className="block font-medium">Action</span>
                  <span className="block text-[11px] leading-4 text-[var(--app-text-subtle)]">Start the work right away</span>
                </span>
              </button>
            </div>
          )}
        </div>
      ) : null}
    </div>
  )
}
