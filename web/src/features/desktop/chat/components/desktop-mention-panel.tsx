import { Bot } from 'lucide-react'
import { cn } from '../../../../lib/cn'

interface DesktopMentionPanelProps {
  matches: string[]
  selectedIndex: number
  onHover: (index: number) => void
  onSelect: (name: string) => void
}

export function DesktopMentionPanel({ matches, selectedIndex, onHover, onSelect }: DesktopMentionPanelProps) {
  if (matches.length === 0) {
    return (
      <div className="rounded-2xl border border-[var(--app-border)] bg-[var(--app-surface)] px-4 py-3 text-sm text-[var(--app-text-muted)]">
        No matching subagents. Keep typing or press Backspace.
      </div>
    )
  }

  return (
    <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
      {matches.map((name, index) => {
        const selected = index === selectedIndex
        return (
          <button
            key={name}
            type="button"
            className={cn(
              'group rounded-2xl border px-4 py-3 text-left transition',
              selected
                ? 'border-[var(--app-border-accent)] bg-[var(--app-bg-alt)] shadow-[var(--shadow-panel)]'
                : 'border-[var(--app-border)] bg-[var(--app-surface)] hover:border-[var(--app-border-accent)] hover:bg-[var(--app-bg-alt)]',
            )}
            onMouseDown={(event) => event.preventDefault()}
            onMouseEnter={() => onHover(index)}
            onFocus={() => onHover(index)}
            onClick={() => onSelect(name)}
          >
            <div className="mb-3 flex h-10 w-10 items-center justify-center rounded-xl border border-[var(--app-border)] bg-[var(--app-bg-alt)] text-[var(--app-text)]">
              <Bot size={18} />
            </div>
            <div className="truncate text-sm font-semibold text-[var(--app-text)]">@{name}</div>
            <div className="mt-1 line-clamp-2 text-xs leading-5 text-[var(--app-text-muted)]">
              Route this prompt directly to the subagent.
            </div>
          </button>
        )
      })}
    </div>
  )
}
