import { useEffect, useState } from 'react'
import { LoaderCircle, Pin, Settings, X, Zap } from 'lucide-react'

import {
  fetchWorkspaceActions,
  orderWorkspaceActionsForQuickAccess,
  type WorkspaceAction,
} from '../../../workspaces/actions/types'

interface DesktopWorkspaceActionChooserProps {
  workspacePath: string
  onSelect: (action: WorkspaceAction) => void
  onOpenSettings?: () => void
  onClose: () => void
}

export function DesktopWorkspaceActionChooser({
  workspacePath,
  onSelect,
  onOpenSettings,
  onClose,
}: DesktopWorkspaceActionChooserProps) {
  const [actions, setActions] = useState<WorkspaceAction[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [request, setRequest] = useState(0)

  useEffect(() => {
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
  }, [request, workspacePath])

  const orderedActions = orderWorkspaceActionsForQuickAccess(actions)

  return (
    <section className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface)] shadow-sm" data-testid="desktop-workspace-action-chooser" aria-label="Workspace Actions">
      <div className="flex items-start gap-3 px-4 py-3">
        <span className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-[var(--app-bg-alt)] text-[var(--app-primary)]"><Zap size={16} aria-hidden="true" /></span>
        <div className="min-w-0 flex-1">
          <h3 className="text-sm font-semibold text-[var(--app-text)]">Workspace Actions</h3>
          <p className="mt-0.5 text-xs text-[var(--app-text-muted)]">Pinned Actions appear first. Choose one to review its inputs and command before it runs.</p>
        </div>
        <button type="button" onClick={onClose} aria-label="Close Action chooser" className="grid h-8 w-8 place-items-center rounded-lg text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)]"><X size={15} /></button>
      </div>
      <div className="border-t border-[var(--app-border)] px-3 py-2">
        {loading ? (
          <div className="flex items-center gap-2 px-2 py-4 text-xs text-[var(--app-text-muted)]" role="status"><LoaderCircle size={15} className="animate-spin" />Loading Actions…</div>
        ) : error ? (
          <div className="px-2 py-3" role="alert">
            <p className="text-xs text-[var(--app-danger)]">{error}</p>
            <button type="button" onClick={() => setRequest((value) => value + 1)} className="mt-2 text-xs font-semibold text-[var(--app-primary)]">Try again</button>
          </div>
        ) : orderedActions.length === 0 ? (
          <p className="px-2 py-4 text-center text-xs text-[var(--app-text-muted)]">No Actions are saved for this workspace.</p>
        ) : (
          <div className="grid max-h-64 gap-1 overflow-y-auto">
            {orderedActions.map((action) => (
              <button key={action.id} type="button" onClick={() => onSelect(action)} className="flex min-w-0 items-center gap-3 rounded-lg px-2.5 py-2.5 text-left hover:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]">
                <span className="min-w-0 flex-1">
                  <span className="flex items-center gap-1.5 text-sm font-semibold text-[var(--app-text)]">
                    <span className="truncate">{action.name}</span>
                    {action.pinned ? <Pin size={12} className="shrink-0 fill-current text-[var(--app-primary)]" aria-label="Pinned" /> : null}
                  </span>
                  <span className="mt-0.5 block truncate text-[11px] text-[var(--app-text-muted)]">{action.description || action.entrypoint}</span>
                </span>
                <span className="shrink-0 text-[10px] font-semibold uppercase tracking-wide text-[var(--app-text-subtle)]">Review</span>
              </button>
            ))}
          </div>
        )}
        {onOpenSettings ? (
          <button type="button" onClick={onOpenSettings} className="mt-2 flex w-full items-center justify-center gap-2 border-t border-[var(--app-border)] px-2 pt-3 pb-1 text-xs font-semibold text-[var(--app-text-muted)] hover:text-[var(--app-text)]">
            <Settings size={14} aria-hidden="true" />Manage Actions in Settings
          </button>
        ) : null}
      </div>
    </section>
  )
}
