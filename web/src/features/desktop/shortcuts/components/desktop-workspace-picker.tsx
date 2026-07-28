import { ArrowUpRight, CheckCircle2, Folder, Keyboard, LayoutGrid } from 'lucide-react'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import { resolveWorkspaceShortcutIndex } from '../desktop-shortcuts'

interface DesktopWorkspacePickerProps {
  open: boolean
  workspaces: WorkspaceEntry[]
  currentWorkspacePath: string
  onClose: () => void
  onSelect: (workspace: WorkspaceEntry) => void
}

function workspaceShortcutLabel(index: number): string | null {
  if (index < 9) return String(index + 1)
  if (index === 9) return '0'
  return null
}

export function DesktopWorkspacePicker({ open, workspaces, currentWorkspacePath, onClose, onSelect }: DesktopWorkspacePickerProps) {
  if (!open) return null

  return (
    <Dialog
      role="dialog"
      aria-modal="true"
      aria-label="Workspace picker"
      className="z-[85] grid place-items-center p-3 sm:p-6"
      onKeyDown={(event) => {
        if (event.altKey || event.ctrlKey || event.metaKey || event.shiftKey) return
        if (event.key === 'Escape') {
          event.preventDefault()
          onClose()
          return
        }
        const index = resolveWorkspaceShortcutIndex(event.key, workspaces.length)
        if (index === null) return
        event.preventDefault()
        onSelect(workspaces[index])
      }}
    >
      <DialogBackdrop onClick={onClose} />
      <DialogPanel className="h-[min(680px,calc(100vh-24px))] w-[min(760px,calc(100vw-24px))] max-h-[calc(100vh-24px)] max-w-[calc(100vw-24px)] place-self-center gap-0 overflow-hidden border-0 bg-transparent p-0 shadow-none">
        <div className="flex h-full min-h-0 w-full flex-col overflow-hidden rounded-3xl bg-[var(--app-surface)]">
          <div className="flex shrink-0 items-center justify-between gap-4 border-b border-[var(--app-border)] bg-[var(--app-bg-alt)] px-5 py-4">
            <div className="flex min-w-0 items-center gap-3.5">
              <div className="grid h-11 w-11 shrink-0 place-items-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-primary)] shadow-xs">
                <LayoutGrid size={22} />
              </div>
              <div className="min-w-0">
                <h2 className="text-lg font-semibold tracking-tight text-[var(--app-text)]">Workspaces</h2>
                <p className="mt-0.5 truncate text-xs text-[var(--app-text-muted)]">
                  Press a number key or click any card to switch workspace
                </p>
              </div>
            </div>
            <ModalCloseButton onClick={onClose} aria-label="Close workspace picker" />
          </div>

          <div className="min-h-0 flex-1 overflow-y-auto p-4 sm:p-5">
            {workspaces.length === 0 ? (
              <div className="flex h-full min-h-52 flex-col items-center justify-center rounded-2xl border border-dashed border-[var(--app-border)] px-4 py-12 text-center">
                <div className="mb-3 grid h-12 w-12 place-items-center rounded-2xl bg-[var(--app-surface-subtle)] text-[var(--app-text-subtle)]">
                  <Folder size={24} />
                </div>
                <p className="text-sm font-medium text-[var(--app-text)]">No workspaces available</p>
                <p className="mt-1 text-xs text-[var(--app-text-muted)]">Open or add a workspace to enable switching.</p>
              </div>
            ) : (
              <div className="grid grid-cols-1 gap-3.5 sm:grid-cols-2">
                {workspaces.map((workspace, index) => {
                  const shortcut = workspaceShortcutLabel(index)
                  const selected = workspace.path === currentWorkspacePath

                  return (
                    <button
                      key={workspace.path}
                      type="button"
                      autoFocus={index === 0}
                      onClick={() => onSelect(workspace)}
                      className={`group relative flex min-h-[120px] flex-col justify-between rounded-2xl border p-4 text-left transition-all duration-150 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] ${
                        selected
                          ? 'border-[var(--app-primary)] bg-[var(--app-surface-elevated)] shadow-md ring-1 ring-[var(--app-primary)]/30'
                          : 'border-[var(--app-border)] bg-[var(--app-surface-subtle)] shadow-xs hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)]'
                      }`}
                      aria-label={`${shortcut ? `Press ${shortcut} or click to switch to` : 'Switch to'} ${workspace.workspaceName}`}
                    >
                      <div className="flex items-start justify-between gap-3">
                        <div className="flex min-w-0 items-center gap-2.5">
                          <div
                            className={`grid h-8 w-8 shrink-0 place-items-center rounded-xl border transition-colors ${
                              selected
                                ? 'border-[var(--app-primary)]/30 bg-[var(--app-primary)]/10 text-[var(--app-primary)]'
                                : 'border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-subtle)] group-hover:text-[var(--app-text)]'
                            }`}
                          >
                            <Folder size={16} />
                          </div>
                          <span className="truncate text-sm font-semibold text-[var(--app-text)]">
                            {workspace.workspaceName}
                          </span>
                        </div>

                        {shortcut ? (
                          <kbd className="grid h-7 min-w-7 shrink-0 place-items-center rounded-lg border border-[var(--app-border-strong)] bg-[var(--app-bg)] px-1.5 font-mono text-xs font-bold text-[var(--app-text)] shadow-xs">
                            {shortcut}
                          </kbd>
                        ) : null}
                      </div>

                      <div className="my-3 min-w-0">
                        <p className="truncate font-mono text-xs text-[var(--app-text-subtle)]" title={workspace.path}>
                          {workspace.path}
                        </p>
                      </div>

                      <div className="flex items-center justify-between border-t border-[var(--app-border)]/50 pt-1">
                        {selected ? (
                          <span className="inline-flex items-center gap-1.5 rounded-full border border-[var(--app-primary)]/30 bg-[var(--app-primary)]/10 px-2.5 py-0.5 text-xs font-semibold text-[var(--app-primary)]">
                            <CheckCircle2 size={12} />
                            Active Workspace
                          </span>
                        ) : (
                          <span className="inline-flex items-center gap-1 text-xs font-medium text-[var(--app-text-subtle)] group-hover:text-[var(--app-text)]">
                            Select
                            <ArrowUpRight size={12} className="transition-transform group-hover:-translate-y-0.5 group-hover:translate-x-0.5" />
                          </span>
                        )}
                      </div>
                    </button>
                  )
                })}
              </div>
            )}
          </div>

          <div className="flex shrink-0 items-center justify-between border-t border-[var(--app-border)] bg-[var(--app-bg-alt)] px-5 py-3 text-xs text-[var(--app-text-muted)]">
            <div className="flex items-center gap-2">
              <Keyboard size={14} className="text-[var(--app-text-subtle)]" />
              <span>
                <strong className="font-semibold text-[var(--app-text)]">1–9 / 0</strong> to select
              </span>
            </div>
            <span className="font-mono text-[11px] text-[var(--app-text-subtle)]">
              {workspaces.length} workspace{workspaces.length === 1 ? '' : 's'}
            </span>
          </div>
        </div>
      </DialogPanel>
    </Dialog>
  )
}
