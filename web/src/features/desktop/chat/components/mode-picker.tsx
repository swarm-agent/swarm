import { ChevronsUp, NotepadText } from 'lucide-react'
import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'

interface ModePickerProps {
  mode: DesktopSessionMode
  onSelect: (mode: DesktopSessionMode) => void
  disabled?: boolean
  triggerClassName?: string
}

export function ModePicker({ mode, onSelect, disabled = false, triggerClassName = '' }: ModePickerProps) {
  const ModeIcon = mode === 'plan' ? NotepadText : ChevronsUp
  const nextMode: DesktopSessionMode = mode === 'plan' ? 'auto' : 'plan'

  return (
    <div className="inline-flex min-w-0 items-center">
      <button
        type="button"
        onClick={() => onSelect(nextMode)}
        disabled={disabled}
        aria-label={`Session mode: ${mode}. Switch to ${nextMode}`}
        title={`Switch to ${nextMode} mode`}
        className={`inline-flex min-h-9 min-w-0 items-center gap-2 rounded-none border-0 border-b-2 border-transparent bg-transparent px-3 py-2 text-xs font-medium text-[var(--app-text-muted)] transition hover:border-[var(--app-border-accent)] hover:text-[var(--app-text)] disabled:cursor-not-allowed disabled:opacity-50 ${triggerClassName}`}
      >
        <ModeIcon size={15} className="shrink-0 text-[var(--app-text-subtle)]" />
        <span className="font-semibold uppercase tracking-wider text-[var(--app-primary)]">{mode}</span>
      </button>
    </div>
  )
}
