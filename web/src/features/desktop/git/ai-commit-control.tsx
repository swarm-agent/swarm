import { Bot, LoaderCircle } from 'lucide-react'
import { cn } from '../../../lib/cn'

interface AICommitButtonProps {
  phase?: 'generating' | 'committing' | null
  disabled?: boolean
  compact?: boolean
  onGenerate: () => void
}

export function AICommitButton({ phase = null, disabled = false, compact = false, onGenerate }: AICommitButtonProps) {
  return (
    <button
      type="button"
      data-ai-commit-button
      className={cn('inline-flex min-h-9 min-w-0 items-center justify-center gap-1.5 rounded-lg bg-[var(--app-bg-alt)] px-2.5 text-xs font-semibold text-[var(--app-text-muted)] hover:bg-[var(--app-surface-hover)] hover:text-[var(--app-text)] disabled:cursor-not-allowed disabled:opacity-60', compact && 'w-9 px-0')}
      disabled={disabled || phase !== null}
      onClick={onGenerate}
      aria-label={phase === 'generating' ? 'AI Commit is generating a message' : phase === 'committing' ? 'AI Commit is committing changes' : 'Generate a message and commit changes'}
      title={phase ? 'AI Commit is running; wait for it to finish' : 'Generate a message and commit all changes'}
    >
      {phase ? <LoaderCircle size={14} className="shrink-0 animate-spin" aria-hidden="true" /> : <Bot size={14} className="shrink-0" aria-hidden="true" />}
      {!compact ? <span className="truncate">{phase === 'generating' ? 'Generating…' : phase === 'committing' ? 'Committing…' : 'AI Commit'}</span> : null}
    </button>
  )
}
