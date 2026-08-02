import { useEffect, useId, useRef, useState } from 'react'
import { Bot, Check, ChevronDown, LoaderCircle } from 'lucide-react'
import { cn } from '../../../lib/cn'
import { fetchWorkspaceActions, type WorkspaceAction } from '../../workspaces/actions/types'

interface AICommitControlProps {
  workspacePath: string
  selectedAction: WorkspaceAction | null
  phase?: 'generating' | 'committing' | null
  disabled?: boolean
  compact?: boolean
  onGenerate: () => void
  onActionSelect: (action: WorkspaceAction | null) => void
}

export function AICommitControl({
  workspacePath,
  selectedAction,
  phase = null,
  disabled = false,
  compact = false,
  onGenerate,
  onActionSelect,
}: AICommitControlProps) {
  const [open, setOpen] = useState(false)
  const [actions, setActions] = useState<WorkspaceAction[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [request, setRequest] = useState(0)
  const rootRef = useRef<HTMLDivElement>(null)
  const menuId = useId()

  useEffect(() => {
    if (!open) return
    const onPointerDown = (event: PointerEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false)
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false)
    }
    document.addEventListener('pointerdown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
    }
  }, [open])

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
    onActionSelect(action)
    setOpen(false)
  }

  return (
    <div ref={rootRef} className="relative inline-flex min-w-0" data-ai-commit-control>
      <button
        type="button"
        className={cn(
          'inline-flex min-h-9 min-w-0 items-center justify-center gap-1.5 rounded-l-lg border border-r-0 border-[var(--app-primary)] px-2.5 text-xs font-semibold text-[var(--app-primary)] hover:bg-[var(--app-selection-bg)] disabled:cursor-not-allowed disabled:opacity-60',
          compact ? 'flex-1' : '',
        )}
        disabled={disabled || phase !== null}
        onClick={onGenerate}
        aria-label={phase === 'generating' ? 'AI Commit is generating a message' : phase === 'committing' ? 'AI Commit is committing changes' : selectedAction ? `Generate and commit changes, then run ${selectedAction.name}` : 'Generate a message and commit changes'}
        title={phase ? 'AI Commit is running; wait for it to finish' : selectedAction ? `AI Commit · then ${selectedAction.name}` : 'Generate a message and commit all changes'}
      >
        {phase ? <LoaderCircle size={14} className="shrink-0 animate-spin" aria-hidden="true" /> : <Bot size={14} className="shrink-0" aria-hidden="true" />}
        <span className="truncate">{phase === 'generating' ? 'Generating…' : phase === 'committing' ? 'Committing…' : selectedAction ? `AI Commit · ${selectedAction.name}` : 'AI Commit'}</span>
      </button>
      <button
        type="button"
        className="grid min-h-9 w-9 shrink-0 place-items-center rounded-r-lg border border-[var(--app-primary)] text-[var(--app-primary)] hover:bg-[var(--app-selection-bg)] disabled:cursor-not-allowed disabled:opacity-60"
        disabled={disabled || phase !== null}
        onClick={() => setOpen((current) => !current)}
        aria-label="Choose post-commit Action"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        title="Choose post-commit Action"
      >
        <ChevronDown size={14} aria-hidden="true" />
      </button>
      {open ? (
        <div id={menuId} role="menu" aria-label="Post-commit Actions" className="absolute bottom-full right-0 z-50 mb-2 w-72 overflow-hidden rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] p-1.5 text-left shadow-2xl">
          <div className="px-2 py-1.5 text-[10px] font-semibold uppercase tracking-[0.08em] text-[var(--app-text-subtle)]">After commit</div>
          <button type="button" role="menuitemradio" aria-checked={!selectedAction} className="flex w-full items-center gap-2 rounded-lg px-2 py-2 text-xs text-[var(--app-text)] hover:bg-[var(--app-surface-hover)]" onClick={() => choose(null)}>
            <span className="grid size-4 place-items-center">{!selectedAction ? <Check size={13} /> : null}</span>
            <span>No post-commit Action</span>
          </button>
          {loading ? <div className="flex items-center gap-2 px-2 py-3 text-xs text-[var(--app-text-muted)]"><LoaderCircle size={13} className="animate-spin" />Loading Actions…</div> : null}
          {!loading && !error && actions.length === 0 ? <div className="px-2 py-3 text-xs text-[var(--app-text-muted)]">No workspace Actions configured.</div> : null}
          {actions.map((action) => (
            <button key={action.id} type="button" role="menuitemradio" aria-checked={selectedAction?.id === action.id} className="flex w-full items-start gap-2 rounded-lg px-2 py-2 text-xs text-[var(--app-text)] hover:bg-[var(--app-surface-hover)]" onClick={() => choose(action)}>
              <span className="mt-0.5 grid size-4 shrink-0 place-items-center">{selectedAction?.id === action.id ? <Check size={13} /> : null}</span>
              <span className="min-w-0"><strong className="block truncate">{action.name}</strong>{action.description ? <span className="mt-0.5 block line-clamp-2 text-[11px] font-normal text-[var(--app-text-muted)]">{action.description}</span> : null}</span>
            </button>
          ))}
          {error ? <div className="px-2 py-2 text-xs text-[var(--app-danger)]" role="alert">{error}<button type="button" className="ml-2 underline" onClick={() => setRequest((current) => current + 1)}>Retry</button></div> : null}
        </div>
      ) : null}
    </div>
  )
}
