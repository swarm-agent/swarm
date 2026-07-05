import { MessageSquareText, Plus } from 'lucide-react'
import { DesktopV3RunStatusPill, formatDesktopV3RunTimerLabel, type DesktopV3RunStatusModel } from './desktop-v3-run-status'

export interface DesktopV3ChatHeaderProps {
  title: string
  workspaceName: string
  branchName?: string
  mode?: string
  runStatus?: DesktopV3RunStatusModel | null
  runStatusNow?: number
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

function normalizeBranchName(value: string | undefined): string {
  return value?.trim() ?? ''
}

export function DesktopV3ChatHeader({
  title,
  workspaceName,
  branchName,
  mode,
  runStatus = null,
  runStatusNow = Date.now(),
  onOpenChats,
  onNewSession,
}: DesktopV3ChatHeaderProps) {
  const displayTitle = normalizeTitle(title)
  const displayWorkspace = normalizeWorkspaceName(workspaceName)
  const displayBranch = normalizeBranchName(branchName)
  const displayMode = normalizeMode(mode)
  const mobileRunTimerLabel = runStatus ? formatDesktopV3RunTimerLabel(runStatus, runStatusNow) : ''

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
            <div className="relative mt-1 grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-center gap-2 text-[10px] font-medium text-[var(--app-text-muted)]" title={displayWorkspace}>
              <span className="min-w-0 truncate text-left">{displayWorkspace}</span>
              {displayBranch ? (
                <span className="pointer-events-none absolute left-1/2 max-w-[42vw] -translate-x-1/2 truncate text-center text-[var(--app-text-muted)]" title={displayBranch}>
                  {displayBranch}
                </span>
              ) : null}
              {mobileRunTimerLabel ? (
                <span className="shrink-0 justify-self-end tabular-nums text-[var(--app-text)]" title={runStatus?.label}>
                  {mobileRunTimerLabel}
                </span>
              ) : null}
            </div>
          </div>

          <div className="hidden min-w-0 sm:block">
            <h1 className="flex items-center gap-2 overflow-hidden text-sm font-semibold text-[var(--app-text)]">
              <span className="truncate" title={displayTitle}>{displayTitle}</span>
              <span className="shrink-0 font-normal text-[var(--app-text-subtle)]">/</span>
              <span className="truncate font-normal text-[var(--app-text-muted)]" title={displayWorkspace}>{displayWorkspace}</span>
            </h1>
            <div className="mt-1 flex max-w-full items-center gap-1.5 overflow-hidden text-[11px] font-medium text-[var(--app-text-muted)]">
              <span>{displayMode}</span>
            </div>
          </div>
        </div>

        <div className="hidden sm:block">
          <DesktopV3RunStatusPill model={runStatus} now={runStatusNow} />
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
