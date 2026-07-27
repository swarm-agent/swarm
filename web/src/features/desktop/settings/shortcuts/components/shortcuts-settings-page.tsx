import { DESKTOP_SHORTCUTS, formatDesktopShortcutKeys } from '../../../shortcuts/desktop-shortcuts'

export function ShortcutsSettingsPage() {
  const groups = ['Navigation', 'Chat', 'Session mode'] as const

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 flex flex-col gap-3">
        <div>
          <h1 className="text-xl font-semibold text-[var(--app-text)]">Shortcuts</h1>
          <p className="mt-1 text-sm text-[var(--app-text-muted)]">
            Desktop shortcuts are scoped to the web/Desktop app. They are intentionally separate from TUI keybindings and may use different keys.
          </p>
        </div>
        <div className="rounded-xl border border-[var(--app-border)] bg-[var(--app-surface-subtle)] px-4 py-3 text-sm text-[var(--app-text-muted)]">
          Open quick actions with <strong className="text-[var(--app-text)]">⌘/Ctrl Alt K</strong> to run supported Desktop actions from a modal.
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
    </div>
  )
}
