import { useState } from 'react'
import { Lightbulb, RotateCcw, TriangleAlert } from 'lucide-react'

import { cn } from '../../../../lib/cn'
import type { WorkspaceEntry } from '../../../workspaces/launcher/types/workspace'
import { DESKTOP_HOME_TIPS, selectDesktopHomeTipIndex } from '../services/home-tips'
import { WorkspaceHomeIdentity } from './workspace-home-identity'

export type DesktopV3RoutedPendingShellState = 'draft' | 'worktree-primed' | 'routing' | 'failed'
export type DesktopV3PendingStartPath = 'session' | 'router'

export interface DesktopV3RoutedPendingShellProps {
  state: DesktopV3RoutedPendingShellState
  startPath?: DesktopV3PendingStartPath
  pendingPrompt?: string
  error?: string
  onRetry?: () => void
  showTips?: boolean
  onDisableTips?: () => void
  workspace?: WorkspaceEntry
  workspaces?: WorkspaceEntry[]
  onOpenWorkspacePicker?: () => void
  onSetWorkspaceIcon?: (path: string, iconPNGDataURL: string) => Promise<void>
  className?: string
}

/**
 * Local-only presentation for a new chat before the routed session is durable.
 * Deliberately accepts no resolved session metadata, so it cannot imply that a
 * workspace, branch, title, mode, favorite, or model has already been chosen.
 */
export function DesktopV3RoutedPendingShell({
  state,
  startPath = 'router',
  pendingPrompt = '',
  error = '',
  onRetry,
  showTips = true,
  onDisableTips,
  workspace,
  workspaces = workspace ? [workspace] : [],
  onOpenWorkspacePicker,
  onSetWorkspaceIcon,
  className,
}: DesktopV3RoutedPendingShellProps) {
  const prompt = pendingPrompt.trim()
  const [tipIndex] = useState(() => selectDesktopHomeTipIndex())
  const submitted = (state === 'routing' || state === 'failed') && Boolean(prompt)
  const routing = state === 'routing'
  const routerPath = startPath === 'router'
  const pendingTitle = routerPath ? 'New worktree chat' : 'New chat'
  const pendingSubtitle = routing
    ? (routerPath ? 'Router setup' : 'Starting session')
    : (routerPath ? 'Router needs attention' : 'Start needs attention')
  const pendingBadge = routing
    ? (routerPath ? 'Routing…' : 'Starting…')
    : (routerPath ? 'Not routed' : 'Not started')
  const statusLabel = routing
    ? (routerPath ? 'Routing…' : 'Starting…')
    : (routerPath ? 'Router failed' : 'Start failed')
  const statusDetail = routing
    ? (routerPath ? 'Router is choosing the setup for this worktree chat…' : 'Creating and starting this chat…')
    : error.trim() || (routerPath ? 'Your message is still here. Try Router again.' : 'Your message is still here. Try starting it again.')

  if (!submitted) {
    return (
      <section
        className={cn('flex min-h-0 flex-1 flex-col bg-[var(--app-bg)]', className)}
        data-testid="desktop-v3-routed-pending-shell"
        data-pending-state={state}
        data-local-only="true"
      >
        <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-4 py-8 sm:px-6">
          <div className="mx-auto flex w-full max-w-3xl flex-1 flex-col items-center justify-center text-center">
            {workspace ? (
              <WorkspaceHomeIdentity
                workspace={workspace}
                workspaces={workspaces}
                onOpenWorkspacePicker={onOpenWorkspacePicker}
                onSetWorkspaceIcon={onSetWorkspaceIcon}
              />
            ) : null}
            {showTips ? (
              <button
                type="button"
                className="mt-4 inline-flex max-w-xl items-center justify-center gap-1.5 text-sm leading-6 text-[var(--app-text-muted)] transition-colors hover:text-[var(--app-text)] disabled:cursor-default"
                data-testid="desktop-home-tip"
                title="Click to disable tips"
                aria-label="Click to disable tips"
                disabled={!onDisableTips}
                onClick={onDisableTips}
              >
                <Lightbulb size={15} className="shrink-0 text-[var(--app-text-muted)]" aria-hidden="true" />
                <span>Tip: {DESKTOP_HOME_TIPS[tipIndex]}</span>
              </button>
            ) : null}
          </div>
        </div>
      </section>
    )
  }

  return (
    <section
      className={cn('flex min-h-0 flex-1 flex-col bg-[var(--app-bg)]', className)}
      data-testid="desktop-v3-routed-pending-shell"
      data-pending-state={state}
      data-local-only="true"
      data-start-path={startPath}
      aria-busy={routing || undefined}
    >
      <header className="shrink-0 border-b border-[var(--app-border)] bg-[var(--app-bg)] px-4 py-3 sm:px-6" data-testid="desktop-v3-pending-chat-header">
        <div className="mx-auto flex w-full max-w-[70rem] items-center justify-between gap-3">
          <div className="min-w-0">
            <div className="truncate text-sm font-semibold text-[var(--app-text)]">{pendingTitle}</div>
            <div className="text-xs text-[var(--app-text-muted)]">{pendingSubtitle}</div>
          </div>
          <span className={cn(
            'inline-flex shrink-0 items-center gap-1.5 text-[11px] font-medium',
            routing
              ? 'text-[var(--app-text-muted)]'
              : 'rounded-md border border-[var(--app-danger)]/30 bg-[var(--app-danger)]/10 px-2 py-1 text-[var(--app-danger)]',
          )}>
            {routing ? (
              <span className="size-1.5 rounded-full bg-current animate-pulse motion-reduce:animate-none" aria-hidden="true" />
            ) : (
              <TriangleAlert size={12} aria-hidden="true" />
            )}
            {pendingBadge}
          </span>
        </div>
      </header>

      <div className="min-h-0 flex-1 overflow-y-auto py-6">
        <div className="mx-auto flex min-h-full w-full max-w-[70rem] flex-col gap-5 px-8 sm:px-12">
          <div className="flex justify-end" data-testid="desktop-v3-local-pending-prompt">
            <div className="max-w-[70%] whitespace-pre-wrap break-words rounded-xl bg-[var(--app-primary)] px-4 py-3 text-sm leading-6 text-[var(--app-primary-text)] shadow-sm">
              {prompt}
            </div>
          </div>

          <div className="flex justify-start" data-testid="desktop-v3-routing-status" role={routing ? 'status' : 'alert'}>
            <div className="min-w-0 max-w-[calc(100%-2rem)] text-sm leading-6 text-[var(--app-text)]">
              <div className={cn(
                'mb-1 inline-flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-[0.12em]',
                routing ? 'text-[var(--app-text-subtle)]' : 'text-[var(--app-danger)]',
              )}>
                {routing ? (
                  <span className="size-1.5 rounded-full bg-current animate-pulse motion-reduce:animate-none" aria-hidden="true" />
                ) : (
                  <TriangleAlert size={12} aria-hidden="true" />
                )}
                {statusLabel}
              </div>
              <div className="text-[var(--app-text-muted)]">{statusDetail}</div>
              {!routing ? (
                <button
                  type="button"
                  className="mt-3 inline-flex h-9 items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 text-sm font-medium text-[var(--app-text)] shadow-sm transition-colors hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50"
                  onClick={onRetry}
                  disabled={!onRetry}
                >
                  <RotateCcw size={14} aria-hidden="true" />
                  Try again
                </button>
              ) : null}
            </div>
          </div>
        </div>
      </div>
    </section>
  )
}
