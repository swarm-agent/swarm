import { useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { Command, Keyboard } from 'lucide-react'
import { Button } from '../../../../../components/ui/button'
import { DESKTOP_SHORTCUTS, formatDesktopShortcutKeys } from '../../../shortcuts/desktop-shortcuts'
import { DesktopQuickActionsModal, type DesktopQuickActionItem } from '../../../shortcuts/components/desktop-quick-actions-modal'

interface ShortcutsSettingsPageProps {
  onOpenQuickActions?: () => void
}

export function ShortcutsSettingsPage({ onOpenQuickActions }: ShortcutsSettingsPageProps = {}) {
  const navigate = useNavigate()
  const [quickActionsOpen, setQuickActionsOpen] = useState(false)
  const groups = ['Navigation', 'Chat', 'Session mode'] as const

  const handleOpen = onOpenQuickActions ?? (() => setQuickActionsOpen(true))

  const quickActionItems: DesktopQuickActionItem[] = [
    {
      id: 'quick-actions',
      label: 'Open quick actions',
      description: 'Show Desktop shortcut actions and run the supported ones from one modal.',
      keys: ['⌘/Ctrl', 'Alt', 'K'],
      availability: 'Available anywhere in Desktop.',
      enabled: true,
      onRun: () => setQuickActionsOpen(true),
    },
    {
      id: 'workspace-picker',
      label: 'Switch workspace',
      description: 'Open the workspace picker to switch workspaces.',
      keys: ['Alt', 'W'],
      availability: 'Navigates to workspace launcher.',
      enabled: true,
      onRun: () => {
        setQuickActionsOpen(false)
        void navigate({ to: '/' })
      },
    },
    {
      id: 'new-session',
      label: 'New session',
      description: 'Start a fresh chat.',
      keys: ['⌘/Ctrl', 'Alt', 'N'],
      availability: 'Navigates to home/chat.',
      enabled: true,
      onRun: () => {
        setQuickActionsOpen(false)
        void navigate({ to: '/' })
      },
    },
    {
      id: 'settings',
      label: 'Open settings',
      description: 'Open Desktop Settings.',
      keys: ['⌘/Ctrl', 'Alt', 'S'],
      availability: 'Currently viewing settings.',
      enabled: true,
      onRun: () => {
        setQuickActionsOpen(false)
      },
    },
    {
      id: 'search-chats',
      label: 'Search chats',
      description: 'Open Desktop chat search.',
      keys: ['⌘/Ctrl', 'Alt', 'F'],
      availability: 'Navigates to chat search.',
      enabled: true,
      onRun: () => {
        setQuickActionsOpen(false)
        void navigate({ to: '/' })
      },
    },
    {
      id: 'latest-needs-approval',
      label: 'Latest needs approval',
      description: 'Jump to the newest visible chat that has a pending permission request.',
      keys: ['⌘/Ctrl', 'Alt', 'A'],
      availability: 'Available from the main chat sidebar.',
      enabled: false,
      disabledReason: 'Return to chat to review pending permissions.',
      onRun: () => {},
    },
    {
      id: 'previous-chat',
      label: 'Previous chat',
      description: 'Return to the previously selected Desktop chat in this window.',
      keys: ['⌘/Ctrl', 'Alt', 'P'],
      availability: 'Available after switching between chats.',
      enabled: false,
      disabledReason: 'Return to chat to switch between chats.',
      onRun: () => {},
    },
    {
      id: 'enable-new-session-plan',
      label: 'Enable plan mode',
      description: 'Enable plan mode for the new chat composer.',
      keys: ['Shift', 'Tab'],
      availability: 'Available only before a new chat is started.',
      enabled: false,
      disabledReason: 'Start a new chat to enable plan mode.',
      onRun: () => {},
    },
  ]

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 flex flex-col gap-3">
        <div>
          <h1 className="text-xl font-semibold text-[var(--app-text)]">Quick Actions &amp; Shortcuts</h1>
          <p className="mt-1 text-sm text-[var(--app-text-muted)]">
            Desktop quick actions and keyboard shortcuts are scoped to the web/Desktop app. They are intentionally separate from TUI keybindings and may use different keys.
          </p>
        </div>
        <div className="flex flex-col gap-3 rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface-subtle)] p-4 shadow-sm sm:flex-row sm:items-center sm:justify-between">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <Command size={18} className="text-[var(--app-primary)]" />
              <h2 className="text-sm font-semibold text-[var(--app-text)]">Desktop Quick Actions</h2>
            </div>
            <p className="mt-1 text-xs text-[var(--app-text-muted)]">
              Open quick actions anytime with <strong className="font-mono text-[var(--app-text)]">⌘/Ctrl Alt K</strong> to run supported Desktop actions from a palette modal.
            </p>
          </div>
          <Button
            variant="outline"
            size="sm"
            className="shrink-0 gap-2 rounded-xl text-xs font-semibold"
            onClick={handleOpen}
            aria-label="Open Quick Actions"
          >
            <Keyboard size={14} />
            Open Quick Actions
          </Button>
        </div>
      </div>

      <div className="grid gap-5 pb-12">
        {groups.map((group) => {
          const shortcuts = DESKTOP_SHORTCUTS.filter((shortcut) => shortcut.group === group)
          if (shortcuts.length === 0) return null
          return (
            <section key={group} className="grid gap-3">
              <h2 className="text-sm font-semibold uppercase tracking-[0.12em] text-[var(--app-text-subtle)]">{group}</h2>
              <div className="overflow-hidden rounded-2xl border border-[var(--app-border-strong)] bg-[var(--app-surface-subtle)] shadow-sm">
                {shortcuts.map((shortcut, index) => (
                  <div
                    key={shortcut.id}
                    className="grid gap-3 border-[var(--app-border)] px-4 py-4 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-start"
                    style={{ borderTopWidth: index === 0 ? 0 : 1 }}
                  >
                    <div className="min-w-0">
                      <div className="text-sm font-semibold text-[var(--app-text)]">{shortcut.label}</div>
                      <div className="mt-1 text-sm text-[var(--app-text-muted)]">{shortcut.description}</div>
                      <div className="mt-2 text-xs text-[var(--app-text-subtle)]">{shortcut.availability}</div>
                    </div>
                    <div className="flex flex-wrap gap-1.5 sm:justify-end" aria-label={`${shortcut.label} shortcut`}>
                      {shortcut.keys.map((key) => (
                        <kbd key={key} className="rounded-lg border border-[var(--app-border)] bg-[var(--app-bg)] px-2 py-1 font-mono text-xs font-semibold text-[var(--app-text)] shadow-sm">
                          {key}
                        </kbd>
                      ))}
                      <span className="sr-only">{formatDesktopShortcutKeys(shortcut.keys)}</span>
                    </div>
                  </div>
                ))}
              </div>
            </section>
          )
        })}
      </div>

      <DesktopQuickActionsModal
        open={quickActionsOpen}
        actions={quickActionItems}
        onClose={() => setQuickActionsOpen(false)}
        onOpenShortcutsSettings={() => setQuickActionsOpen(false)}
      />
    </div>
  )
}
