import { Bot, LoaderCircle, RotateCcw } from 'lucide-react'

import { cn } from '../../../../lib/cn'

export type DesktopV3RoutedPendingShellState = 'draft' | 'worktree-primed' | 'routing' | 'failed'

export interface DesktopV3RoutedPendingShellProps {
  state: DesktopV3RoutedPendingShellState
  pendingPrompt?: string
  onRetry?: () => void
  className?: string
}

const PRESENTATION: Record<DesktopV3RoutedPendingShellState, { heading: string; detail: string }> = {
  draft: {
    heading: 'Swarm',
    detail: 'What would you like to work on?',
  },
  'worktree-primed': {
    heading: 'Preparing your chat',
    detail: 'Your message is ready. Swarm will choose the setup when you send it.',
  },
  routing: {
    heading: 'Choosing setup',
    detail: 'Swarm is selecting the right setup for this chat.',
  },
  failed: {
    heading: 'Setup not chosen',
    detail: 'Your message is still here. Try choosing a setup again.',
  },
}

/**
 * Local-only presentation for a new chat before the routed session is durable.
 * Deliberately accepts no resolved session metadata, so it cannot imply that a
 * workspace, branch, title, mode, favorite, or model has already been chosen.
 */
export function DesktopV3RoutedPendingShell({
  state,
  pendingPrompt = '',
  onRetry,
  className,
}: DesktopV3RoutedPendingShellProps) {
  const presentation = PRESENTATION[state]
  const prompt = pendingPrompt.trim()
  const pending = state === 'worktree-primed' || state === 'routing'
  const showPrompt = state !== 'draft' && Boolean(prompt)

  return (
    <section
      className={cn('flex min-h-0 flex-1 flex-col bg-[var(--app-bg)]', className)}
      data-testid="desktop-v3-routed-pending-shell"
      data-pending-state={state}
      data-local-only="true"
      aria-busy={pending || undefined}
      aria-disabled={pending || undefined}
    >
      <div className="flex min-h-0 flex-1 flex-col overflow-y-auto px-4 py-8 sm:px-6">
        <div className="mx-auto flex w-full max-w-3xl flex-1 flex-col justify-center gap-8">
          <div className="flex flex-col items-center text-center">
            <span
              className="mb-4 grid size-11 place-items-center rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] text-[var(--app-text-muted)] shadow-sm"
              aria-hidden="true"
            >
              {pending ? <LoaderCircle className="animate-spin motion-reduce:animate-none" size={20} /> : <Bot size={20} />}
            </span>
            <h1 className="text-lg font-semibold text-[var(--app-text)]">{presentation.heading}</h1>
            <p className="mt-1 max-w-md text-sm leading-6 text-[var(--app-text-muted)]">{presentation.detail}</p>
          </div>

          {showPrompt ? (
            <div className="flex justify-end" data-testid="desktop-v3-local-pending-prompt">
              <div className="max-w-[85%] whitespace-pre-wrap break-words rounded-2xl rounded-br-md bg-[var(--app-primary)] px-4 py-3 text-sm leading-6 text-[var(--app-primary-text)]">
                {prompt}
              </div>
            </div>
          ) : null}

          {state === 'failed' ? (
            <div className="flex justify-center">
              <button
                type="button"
                className="inline-flex h-9 items-center gap-2 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-3 text-sm font-medium text-[var(--app-text)] shadow-sm transition-colors hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50"
                onClick={onRetry}
                disabled={!onRetry}
              >
                <RotateCcw size={14} aria-hidden="true" />
                Try again
              </button>
            </div>
          ) : null}
        </div>
      </div>
    </section>
  )
}
