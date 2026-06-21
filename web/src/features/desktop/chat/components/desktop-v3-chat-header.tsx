import { Clock3, MessageSquareText, Plus } from 'lucide-react'

export interface DesktopV3ChatHeaderProps {
  title: string
  workspaceName: string
  mode?: string
  running?: boolean
  onOpenChats?: () => void
  onNewSession?: () => void
}

function normalizeTitle(value: string): string {
  return value.trim() || 'New conversation'
}

function normalizeWorkspaceName(value: string): string {
  return value.trim() || 'Workspace'
}

function normalizeMode(value: string | undefined): string {
  return value?.trim().toLowerCase() === 'plan' ? 'plan' : 'auto'
}

export function DesktopV3ChatHeader({
  title,
  workspaceName,
  mode,
  running = false,
  onOpenChats,
  onNewSession,
}: DesktopV3ChatHeaderProps) {
  const displayTitle = normalizeTitle(title)
  const displayWorkspace = normalizeWorkspaceName(workspaceName)
  const displayMode = normalizeMode(mode)
  const statusLabel = running ? 'Running' : displayMode

  return (
    <header className="min-h-[60px] shrink-0 border-b border-[var(--app-border)] bg-[var(--app-surface)] px-2.5 pb-2 pt-[calc(var(--app-safe-area-top)_+_0.5rem)] sm:h-[60px] sm:px-4 sm:py-0">
      <div className="flex h-full min-w-0 items-center gap-1.5 sm:gap-2">
        {onOpenChats ? (
          <button
            type="button"
            className="inline-flex h-9 w-9 shrink-0 touch-manipulation items-center justify-center rounded-xl border border-transparent bg-transparent text-[var(--app-text-muted)] transition duration-150 hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)] active:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--app-bg)] sm:hidden"
            onClick={onOpenChats}
            aria-label="Open chats"
            title="Chats"
          >
            <MessageSquareText size={18} />
          </button>
        ) : null}

        <div className="min-w-0 flex-1">
          <div className="sm:hidden">
            <h1 className="truncate text-[13px] font-semibold leading-tight text-[var(--app-text)]" title={displayTitle}>
              {displayTitle}
            </h1>
            <div className="mt-1 grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-2 overflow-hidden">
              <div className="inline-flex min-w-0 items-center text-[10px] font-medium text-[var(--app-text-muted)]" title={displayWorkspace}>
                <span className="truncate text-left">{displayWorkspace}</span>
              </div>
              <div className="inline-flex h-[18px] shrink-0 items-center gap-1 rounded-full border border-transparent bg-transparent text-[10px] font-medium tabular-nums text-[var(--app-text-muted)]" title={running ? 'Run active' : 'Session mode'}>
                {running ? <Clock3 size={10} className="shrink-0" /> : null}
                <span>{statusLabel}</span>
              </div>
            </div>
          </div>

          <div className="hidden min-w-0 sm:block">
            <h1 className="flex items-center gap-2 overflow-hidden text-sm font-semibold text-[var(--app-text)]">
              <span className="truncate" title={displayTitle}>{displayTitle}</span>
              <span className="shrink-0 font-normal text-[var(--app-text-subtle)]">/</span>
              <span className="truncate font-normal text-[var(--app-text-muted)]" title={displayWorkspace}>{displayWorkspace}</span>
            </h1>
            <div className="mt-1 flex max-w-full items-center gap-1.5 overflow-hidden text-[11px] font-medium text-[var(--app-text-muted)]">
              {running ? <Clock3 size={12} className="shrink-0 text-[var(--app-text-subtle)]" /> : null}
              <span className={running ? 'text-[var(--app-primary)]' : undefined}>{statusLabel}</span>
            </div>
          </div>
        </div>

        {onNewSession ? (
          <button
            type="button"
            className="inline-flex h-9 w-9 shrink-0 touch-manipulation items-center justify-center rounded-xl border border-transparent bg-transparent text-[var(--app-text-muted)] transition duration-150 hover:bg-[var(--app-surface-subtle)] hover:text-[var(--app-text)] active:bg-[var(--app-surface-hover)] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-focus-ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--app-bg)]"
            onClick={onNewSession}
            aria-label="New session"
            title="New session"
          >
            <Plus size={19} />
          </button>
        ) : null}
      </div>
    </header>
  )
}
