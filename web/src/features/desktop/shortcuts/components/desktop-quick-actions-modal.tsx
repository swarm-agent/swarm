import { CheckCircle2, Command, ExternalLink, type LucideIcon } from 'lucide-react'
import { Button } from '../../../../components/ui/button'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { cn } from '../../../../lib/cn'
import { DESKTOP_SHORTCUTS, type DesktopShortcutActionID } from '../desktop-shortcuts'

export interface DesktopQuickActionItem {
  id: DesktopShortcutActionID
  label: string
  description: string
  keys: string[]
  availability: string
  enabled: boolean
  disabledReason?: string
  icon?: LucideIcon
  onRun: () => void
}

interface DesktopQuickActionsModalProps {
  open: boolean
  actions: DesktopQuickActionItem[]
  onClose: () => void
  onOpenShortcutsSettings: () => void
}

export function DesktopQuickActionsModal({ open, actions, onClose, onOpenShortcutsSettings }: DesktopQuickActionsModalProps) {
  if (!open) return null

  const actionsByID = new Map(actions.map((action) => [action.id, action]))

  return (
    <Dialog role="dialog" aria-modal="true" aria-label="Desktop quick actions" className="z-[85] p-3 sm:p-6">
      <DialogBackdrop onClick={onClose} />
      <DialogPanel className="h-[min(680px,calc(100vh-24px))] w-[min(720px,calc(100vw-24px))] gap-0 rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:h-[min(720px,calc(100vh-48px))] sm:w-[min(820px,calc(100vw-48px))]">
        <div className="flex shrink-0 items-start justify-between gap-4 border-b border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-4 sm:px-5">
          <div className="flex min-w-0 items-start gap-3">
            <div className="grid h-11 w-11 shrink-0 place-items-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text)]">
              <Command size={20} />
            </div>
            <div className="min-w-0">
              <div className="text-xs font-semibold uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Desktop</div>
              <h2 className="mt-1 text-xl font-semibold text-[var(--app-text)]">Quick actions</h2>
              <p className="mt-1 text-sm text-[var(--app-text-muted)]">Run Desktop actions and review their shortcuts. These are not TUI keybindings.</p>
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <Button variant="outline" className="hidden h-10 rounded-2xl px-3 text-sm sm:inline-flex" onClick={onOpenShortcutsSettings}>
              <ExternalLink size={15} />
              Shortcuts
            </Button>
            <ModalCloseButton onClick={onClose} aria-label="Close Desktop quick actions" />
          </div>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto p-3 sm:p-4">
          <div className="grid gap-2">
            {DESKTOP_SHORTCUTS.map((definition) => {
              const action = actionsByID.get(definition.id)
              const enabled = action?.enabled ?? false
              const Icon = action?.icon ?? CheckCircle2
              return (
                <button
                  key={definition.id}
                  type="button"
                  disabled={!enabled}
                  onClick={() => {
                    if (!action?.enabled) return
                    action.onRun()
                  }}
                  className={cn(
                    'grid w-full gap-3 rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-left transition sm:grid-cols-[32px_minmax(0,1fr)_auto] sm:items-start',
                    enabled
                      ? 'hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)]'
                      : 'cursor-not-allowed opacity-60',
                  )}
                >
                  <span className="hidden h-8 w-8 place-items-center rounded-xl border border-[var(--app-border)] bg-[var(--app-bg)] text-[var(--app-text-muted)] sm:grid">
                    <Icon size={16} />
                  </span>
                  <span className="min-w-0">
                    <span className="block text-sm font-semibold text-[var(--app-text)]">{definition.label}</span>
                    <span className="mt-1 block text-sm text-[var(--app-text-muted)]">{definition.description}</span>
                    <span className="mt-2 block text-xs text-[var(--app-text-subtle)]">{action?.disabledReason || definition.availability}</span>
                  </span>
                  <span className="flex flex-wrap gap-1.5 sm:justify-end">
                    {definition.keys.map((key) => (
                      <kbd key={key} className="rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-2 py-1 font-mono text-xs font-semibold text-[var(--app-text)] shadow-sm">
                        {key}
                      </kbd>
                    ))}
                  </span>
                </button>
              )
            })}
          </div>
        </div>

        <div className="flex shrink-0 items-center justify-between gap-3 border-t border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-3 sm:hidden">
          <div className="text-xs text-[var(--app-text-muted)]">Need all shortcuts?</div>
          <Button variant="outline" className="h-10 rounded-2xl" onClick={onOpenShortcutsSettings}>
            <ExternalLink size={15} />
            Open settings
          </Button>
        </div>
      </DialogPanel>
    </Dialog>
  )
}
