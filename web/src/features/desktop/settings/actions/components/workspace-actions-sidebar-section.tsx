import { useEffect, useMemo, useState } from 'react'
import { Bot, ChevronDown, Play, Settings, X } from 'lucide-react'
import { Button } from '../../../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../../components/ui/dialog'
import { fetchWorkspaceActions, orderWorkspaceActionsForQuickAccess, type WorkspaceAction } from '../../../../workspaces/actions/types'
import { ActionsSettingsPage } from './actions-settings-page'
import { WorkspaceActionIcon } from './workspace-action-icons'

interface WorkspaceActionsSidebarSectionProps {
  workspacePath: string
  sessionId?: string
  workspaceName?: string
  canAICommit?: boolean
  onRun: (action: WorkspaceAction) => void
  onAICommitRun?: (action: WorkspaceAction) => void
}

export function WorkspaceActionsSidebarSection({ workspacePath, sessionId = '', workspaceName = '', canAICommit = false, onRun, onAICommitRun }: WorkspaceActionsSidebarSectionProps) {
  const [actions, setActions] = useState<WorkspaceAction[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [request, setRequest] = useState(0)
  const [managerOpen, setManagerOpen] = useState(false)
  const [actionMenuId, setActionMenuId] = useState('')

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    setError('')
    void fetchWorkspaceActions(workspacePath, controller.signal, sessionId)
      .then(setActions)
      .catch((cause) => {
        if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : 'Could not load Workspace Actions.')
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false)
      })
    return () => controller.abort()
  }, [request, sessionId, workspacePath])

  const pinnedActions = useMemo(
    () => orderWorkspaceActionsForQuickAccess(actions).filter((action) => action.pinned),
    [actions],
  )

  return (
    <>
      <section className="mt-3 shrink-0 border-t border-[var(--app-border)] pt-3" data-testid="desktop-plan-workspace-actions" data-plan-section="workspace-actions" data-plan-section-treatment="integrated">
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
          <div className="mt-2 grid gap-1" data-pinned-workspace-actions>
            {pinnedActions.map((action) => (
              <div key={action.id} className="relative flex min-w-0 items-center rounded-lg hover:bg-[var(--app-surface-hover)]" data-workspace-action-row>
                <button type="button" onClick={() => onRun(action)} className="flex min-h-9 min-w-0 flex-1 items-center gap-2 px-2.5 text-left text-xs font-semibold text-[var(--app-text)]" aria-label={`Run ${action.name}`}>
                  <WorkspaceActionIcon icon={action.icon} className="shrink-0 text-[var(--app-primary)]" />
                  <span className="min-w-0 flex-1 truncate">{action.name}</span>
                  <Play size={12} className="shrink-0 text-[var(--app-text-muted)]" aria-hidden="true" />
                </button>
                {onAICommitRun ? <button type="button" className="grid size-9 shrink-0 place-items-center rounded-lg text-[var(--app-text-muted)] hover:bg-[var(--app-bg-alt)] hover:text-[var(--app-text)] disabled:opacity-40" onClick={() => setActionMenuId((current) => current === action.id ? '' : action.id)} aria-label={`More options for ${action.name}`} aria-haspopup="menu" aria-expanded={actionMenuId === action.id}><ChevronDown size={13} /></button> : null}
                {actionMenuId === action.id ? <div role="menu" aria-label={`${action.name} options`} className="absolute bottom-full right-0 z-50 mb-1 w-56 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] p-1 shadow-xl">
                  <button type="button" role="menuitem" disabled={!canAICommit} className="flex min-h-9 w-full items-center gap-2 rounded-md px-2 text-left text-xs text-[var(--app-text)] hover:bg-[var(--app-surface-hover)] disabled:opacity-40" onClick={() => { setActionMenuId(''); onAICommitRun?.(action) }}><Bot size={13} />AI Commit, then run</button>
                  {!canAICommit ? <p className="px-2 py-1 text-[10px] text-[var(--app-text-subtle)]">Make a Git change to use this flow.</p> : null}
                </div> : null}
              </div>
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
