import { useCallback, useEffect, useId, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Bot, Check, ChevronUp, LoaderCircle, Zap } from 'lucide-react'
import { cn } from '../../../lib/cn'
import { fetchWorkspaceActions, type WorkspaceAction } from '../../workspaces/actions/types'

interface AICommitControlProps {
  workspacePath: string
  selectedAction: WorkspaceAction | null
  phase?: 'generating' | 'committing' | null
  disabled?: boolean
  compact?: boolean
  actionsOnly?: boolean
  onGenerate?: () => void
  onActionSelect?: (action: WorkspaceAction | null) => void
  onActionRun?: (action: WorkspaceAction) => void
}

function actionOptionsPreview(action: WorkspaceAction): string {
  const options = [...action.arguments]
  for (const input of action.inputs) {
    options.push(...input.arguments, `<${input.label}>`)
  }
  return options.length > 0 ? options.join(' ') : 'No options'
}

export function AICommitControl({
  workspacePath,
  selectedAction,
  phase = null,
  disabled = false,
  compact = false,
  actionsOnly = false,
  onGenerate,
  onActionSelect,
  onActionRun,
}: AICommitControlProps) {
  const [open, setOpen] = useState(false)
  const [actions, setActions] = useState<WorkspaceAction[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [request, setRequest] = useState(0)
  const [menuPosition, setMenuPosition] = useState({ bottom: 0, right: 0 })
  const rootRef = useRef<HTMLDivElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const menuId = useId()

  const positionMenu = useCallback(() => {
    const bounds = rootRef.current?.getBoundingClientRect()
    if (!bounds) return
    setMenuPosition({
      bottom: Math.max(8, window.innerHeight - bounds.top + 8),
      right: Math.max(8, window.innerWidth - bounds.right),
    })
  }, [])

  useEffect(() => {
    if (!open) return
    positionMenu()
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node
      if (!rootRef.current?.contains(target) && !menuRef.current?.contains(target)) setOpen(false)
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false)
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    window.addEventListener('resize', positionMenu)
    window.addEventListener('scroll', positionMenu, true)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
      window.removeEventListener('resize', positionMenu)
      window.removeEventListener('scroll', positionMenu, true)
    }
  }, [open, positionMenu])

  useEffect(() => {
    if (!open || !workspacePath.trim()) return
    const controller = new AbortController()
    setLoading(true)
    setError('')
    void fetchWorkspaceActions(workspacePath, controller.signal)
      .then(setActions)
      .catch((cause) => {
        if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : 'Could not load Actions.')
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [open, request, workspacePath])

  const choose = (action: WorkspaceAction | null) => {
    if (actionsOnly) {
      if (action) onActionRun?.(action)
    } else {
      onActionSelect?.(action)
    }
    setOpen(false)
  }

  return (
    <div ref={rootRef} className={cn('relative inline-flex min-w-0', compact ? 'shrink-0' : '')} data-ai-commit-control data-actions-only={actionsOnly || undefined}>
      {!actionsOnly ? (
        <button
          type="button"
          className={cn(
            'inline-flex min-h-9 min-w-0 items-center justify-center gap-1.5 rounded-l-lg border border-r-0 border-[var(--app-primary)] px-2.5 text-xs font-semibold text-[var(--app-primary)] hover:bg-[var(--app-selection-bg)] disabled:cursor-not-allowed disabled:opacity-60',
            compact ? 'w-9 px-0' : '',
          )}
          disabled={disabled || phase !== null}
          onClick={onGenerate}
          aria-label={phase === 'generating' ? 'AI Commit is generating a message' : phase === 'committing' ? 'AI Commit is committing changes' : selectedAction ? `Generate and commit changes, then run ${selectedAction.name}` : 'Generate a message and commit changes'}
          title={phase ? 'AI Commit is running; wait for it to finish' : selectedAction ? `AI Commit · then ${selectedAction.name}` : 'Generate a message and commit all changes'}
        >
          {phase ? <LoaderCircle size={14} className="shrink-0 animate-spin" aria-hidden="true" /> : <Bot size={14} className="shrink-0" aria-hidden="true" />}
          {!compact ? <span className="truncate">{phase === 'generating' ? 'Generating…' : phase === 'committing' ? 'Committing…' : selectedAction ? `AI Commit · ${selectedAction.name}` : 'AI Commit'}</span> : null}
        </button>
      ) : null}
      <button
        type="button"
        className={cn(
          'inline-flex min-h-9 shrink-0 items-center justify-center gap-1.5 border border-[var(--app-primary)] text-xs font-semibold text-[var(--app-primary)] hover:bg-[var(--app-selection-bg)] disabled:cursor-not-allowed disabled:opacity-60',
          actionsOnly ? 'rounded-lg px-2.5' : 'w-9 rounded-r-lg',
        )}
        disabled={disabled || phase !== null}
        onClick={() => {
          positionMenu()
          setOpen((current) => !current)
        }}
        aria-label={actionsOnly ? 'Open workspace Actions' : 'Choose post-commit Action'}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        title={actionsOnly ? 'Run workspace Action' : 'Choose post-commit Action'}
      >
        {actionsOnly ? <><Zap size={14} aria-hidden="true" /><span>Actions</span></> : null}
        <ChevronUp size={14} aria-hidden="true" />
      </button>
      {open ? createPortal(
        <div ref={menuRef} id={menuId} role="menu" aria-label={actionsOnly ? 'Workspace Actions' : 'Post-commit Actions'} className="fixed z-[100] w-72 overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-1.5 text-left shadow-2xl" style={{ bottom: menuPosition.bottom, right: menuPosition.right }} data-menu-direction="up">
          <div className="px-2 py-1.5 text-[10px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">{actionsOnly ? 'Actions' : 'After commit'}</div>
          {!actionsOnly ? <button type="button" role="menuitemradio" aria-checked={!selectedAction} className="flex h-9 w-full items-center gap-2 border-t border-[var(--app-border)] px-2 text-xs text-[var(--app-text)] first:border-t-0 hover:bg-[var(--app-surface-hover)]" onClick={() => choose(null)}>
            <span className="grid size-4 shrink-0 place-items-center">{!selectedAction ? <Check size={13} /> : null}</span>
            <span className="min-w-0 flex-1 truncate text-left">No post-commit Action</span>
            <span className="shrink-0 text-[10px] font-medium uppercase tracking-[0.06em] text-[var(--app-text-subtle)]">None</span>
          </button> : null}
          {loading ? <div className="flex items-center gap-2 px-2 py-3 text-xs text-[var(--app-text-muted)]"><LoaderCircle size={13} className="animate-spin" />Loading Actions…</div> : null}
          {!loading && !error && actions.length === 0 ? <div className="px-2 py-3 text-xs text-[var(--app-text-muted)]">No workspace Actions configured.</div> : null}
          {actions.length > 0 ? (
            <div className={cn('border-y border-[var(--app-border)]', actions.length > 5 && 'max-h-[240px] overflow-y-auto [scrollbar-gutter:stable]')} data-action-list-scroll={actions.length > 5 ? 'conditional' : undefined}>
              {actions.map((action) => (
                <button key={action.id} type="button" role={actionsOnly ? 'menuitem' : 'menuitemradio'} aria-checked={actionsOnly ? undefined : selectedAction?.id === action.id} className="flex h-12 w-full items-center gap-2 border-b border-[var(--app-border)] px-2 text-xs text-[var(--app-text)] last:border-b-0 hover:bg-[var(--app-surface-hover)]" onClick={() => choose(action)}>
                  <span className="grid size-4 shrink-0 place-items-center">{actionsOnly ? <Zap size={13} /> : selectedAction?.id === action.id ? <Check size={13} /> : null}</span>
                  <span className="flex min-w-0 flex-1 flex-col items-start justify-center leading-tight">
                    <strong className="w-full truncate text-left font-medium">{action.name}</strong>
                    <span className="mt-1 w-full truncate text-left text-[10px] font-normal text-[var(--app-text-subtle)]" title={actionOptionsPreview(action)}>{actionOptionsPreview(action)}</span>
                  </span>
                </button>
              ))}
            </div>
          ) : null}
          {error ? <div className="px-2 py-2 text-xs text-[var(--app-danger)]" role="alert">{error}<button type="button" className="ml-2 underline" onClick={() => setRequest((current) => current + 1)}>Retry</button></div> : null}
        </div>,
        document.body,
      ) : null}
    </div>
  )
}
