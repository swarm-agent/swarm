import { useEffect, useMemo, useState } from 'react'
import { Play, Settings, X } from 'lucide-react'
import { Button } from '../../../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../../components/ui/dialog'
import { fetchWorkspaceActions, orderWorkspaceActionsForQuickAccess, type WorkspaceAction } from '../../../../workspaces/actions/types'
import { ActionsSettingsPage } from './actions-settings-page'
import { WorkspaceActionIcon } from './workspace-action-icons'

interface WorkspaceActionsSidebarSectionProps {
  workspacePath: string
  workspaceName?: string
  onRun: (action: WorkspaceAction) => void
}

export function WorkspaceActionsSidebarSection({ workspacePath, workspaceName = '', onRun }: WorkspaceActionsSidebarSectionProps) {
  const [actions, setActions] = useState<WorkspaceAction[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [request, setRequest] = useState(0)
  const [managerOpen, setManagerOpen] = useState(false)

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError('')
    void fetchWorkspaceActions(workspacePath, controller.signal)
      .then(setActions)
      .catch((cause) => {
        if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : 'Could not load Workspace Actions.')
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [request, workspacePath])

  const pinnedActions = useMemo(
    () => orderWorkspaceActionsForQuickAccess(actions).filter((action) => action.pinned),
    [actions],
  )

  return (
    <>
      <section className="mt-3 shrink-0 rounded-xl border border-[var(--app-primary-border)]/45 bg-[var(--app-primary-soft)]/30 p-3" data-testid="desktop-plan-workspace-actions" data-plan-section="workspace-actions">
        <div className="flex items-center gap-2">
          <div className="min-w-0 flex-1">
            <h2 className="text-[11px] font-semibold uppercase tracking-[0.1em] text-[var(--app-text)]">Workspace Actions</h2>
            <p className="mt-0.5 text-[10px] text-[var(--app-text-subtle)]">Pinned run shortcuts</p>
          </div>
          <button type="button" onClick={() => setManagerOpen(true)} className="grid size-8 place-items-center rounded-lg text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)]" aria-label="Manage Workspace Actions" title="Manage Workspace Actions">
            <Settings size={14} aria-hidden="true" />
          </button>
        </div>
        {loading ? <p className="mt-2 text-xs text-[var(--app-text-muted)]">Loading Workspace Actions…</p> : null}
        {error ? <p className="mt-2 text-xs text-[var(--app-danger)]" role="alert">{error}<button type="button" className="ml-2 underline" onClick={() => setRequest((current) => current + 1)}>Retry</button></p> : null}
        {!loading && !error && pinnedActions.length === 0 ? <p className="mt-2 text-xs text-[var(--app-text-muted)]">No pinned Actions. Use settings to pin a shortcut.</p> : null}
        {pinnedActions.length > 0 ? (
          <div className="mt-2 grid gap-1.5" data-pinned-workspace-actions>
            {pinnedActions.map((action) => (
              <button key={action.id} type="button" onClick={() => onRun(action)} className="flex min-h-9 w-full items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 text-left text-xs font-semibold text-[var(--app-text)] hover:bg-[var(--app-surface-hover)]" aria-label={`Run ${action.name}`}>
                <WorkspaceActionIcon icon={action.icon} className="shrink-0 text-[var(--app-primary)]" />
                <span className="min-w-0 flex-1 truncate">{action.name}</span>
                <Play size={12} className="shrink-0 text-[var(--app-text-muted)]" aria-hidden="true" />
              </button>
            ))}
          </div>
        ) : null}
      </section>

      {managerOpen ? (
        <Dialog>
          <DialogBackdrop onClick={() => setManagerOpen(false)} />
          <DialogPanel className="max-h-[min(88vh,52rem)] w-[min(760px,100%)] overflow-y-auto p-4" data-workspace-actions-quick-manager>
            <div className="mb-3 flex justify-end">
              <Button variant="ghost" size="sm" onClick={() => setManagerOpen(false)} aria-label="Close Workspace Actions manager"><X size={15} />Close</Button>
            </div>
            <ActionsSettingsPage
              workspacePath={workspacePath}
              workspaceName={workspaceName}
              compact
              onRun={(action) => {
                setManagerOpen(false)
                onRun(action)
              }}
              onMutated={setActions}
            />
          </DialogPanel>
        </Dialog>
      ) : null}
    </>
  )
}
