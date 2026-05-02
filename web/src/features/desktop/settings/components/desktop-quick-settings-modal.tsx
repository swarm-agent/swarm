import { ExternalLink, GitBranch, Palette, Shield, type LucideIcon } from 'lucide-react'
import { Dialog, DialogBackdrop, DialogPanel } from '../../../../components/ui/dialog'
import { Button } from '../../../../components/ui/button'
import { ModalCloseButton } from '../../../../components/ui/modal-close-button'
import { cn } from '../../../../lib/cn'
import { PermissionsSettingsPage } from '../permissions/components/permissions-settings-page'
import { ThemesSettingsPage } from '../themes/components/themes-settings-page'
import { WorktreeSettingsPage } from '../worktrees/components/worktree-settings-page'
import type { SettingsTabID } from '../types/settings-tabs'

export type QuickSettingsTabID = Extract<SettingsTabID, 'permissions' | 'themes' | 'worktrees'>

interface QuickSettingsConfig {
  title: string
  description: string
  settingsLabel: string
  icon: LucideIcon
}

const quickSettingsConfig: Record<QuickSettingsTabID, QuickSettingsConfig> = {
  permissions: {
    title: 'Permissions',
    description: 'Quick access to the same permission policy controls from Settings.',
    settingsLabel: 'Open full permission settings',
    icon: Shield,
  },
  themes: {
    title: 'Themes',
    description: 'Quick access to desktop theme and workspace theme controls.',
    settingsLabel: 'Open full theme settings',
    icon: Palette,
  },
  worktrees: {
    title: 'Worktrees',
    description: 'Quick access to the same worktree isolation controls from Settings.',
    settingsLabel: 'Open full worktree settings',
    icon: GitBranch,
  },
}

function QuickSettingsContent({ tab }: { tab: QuickSettingsTabID }) {
  switch (tab) {
    case 'permissions':
      return <PermissionsSettingsPage />
    case 'themes':
      return <ThemesSettingsPage />
    case 'worktrees':
      return <WorktreeSettingsPage />
  }
}

interface DesktopQuickSettingsModalProps {
  tab: QuickSettingsTabID | null
  onClose: () => void
  onOpenFullSettings: (tab: QuickSettingsTabID) => void
}

export function DesktopQuickSettingsModal({ tab, onClose, onOpenFullSettings }: DesktopQuickSettingsModalProps) {
  if (!tab) {
    return null
  }

  const config = quickSettingsConfig[tab]
  const Icon = config.icon

  return (
    <Dialog role="dialog" aria-modal="true" aria-label={`${config.title} quick settings`} className="z-[80] p-3 sm:p-6">
      <DialogBackdrop onClick={onClose} />
      <DialogPanel className="h-[min(820px,calc(100vh-24px))] w-[min(980px,calc(100vw-24px))] gap-0 rounded-3xl border border-[var(--app-border-strong)] bg-[var(--app-surface)] p-0 shadow-[var(--shadow-panel)] sm:h-[min(820px,calc(100vh-48px))] sm:w-[min(1040px,calc(100vw-48px))]">
        <div className="flex shrink-0 items-start justify-between gap-4 border-b border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-4 sm:px-5">
          <div className="flex min-w-0 items-start gap-3">
            <div className="grid h-11 w-11 shrink-0 place-items-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text)]">
              <Icon size={20} />
            </div>
            <div className="min-w-0">
              <div className="text-xs font-semibold uppercase tracking-[0.18em] text-[var(--app-text-subtle)]">Quick settings</div>
              <h2 className="mt-1 text-xl font-semibold text-[var(--app-text)]">{config.title}</h2>
              <p className="mt-1 text-sm text-[var(--app-text-muted)]">{config.description}</p>
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <Button
              variant="outline"
              className={cn(
                'hidden h-10 rounded-2xl border-[var(--app-border)] bg-[var(--app-surface)] px-3 text-sm text-[var(--app-text)] hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] sm:inline-flex',
              )}
              onClick={() => onOpenFullSettings(tab)}
            >
              <ExternalLink size={15} />
              Settings page
            </Button>
            <ModalCloseButton onClick={onClose} aria-label={`Close ${config.title} quick settings`} />
          </div>
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto px-4 py-5 sm:px-6">
          <QuickSettingsContent tab={tab} />
        </div>
        <div className="flex shrink-0 items-center justify-between gap-3 border-t border-[var(--app-border)] bg-[var(--app-bg-alt)] px-4 py-3 sm:hidden">
          <div className="text-xs text-[var(--app-text-muted)]">Need the full page?</div>
          <Button variant="outline" className="h-10 rounded-2xl" onClick={() => onOpenFullSettings(tab)}>
            <ExternalLink size={15} />
            Open settings
          </Button>
        </div>
      </DialogPanel>
    </Dialog>
  )
}
