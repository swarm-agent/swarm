import { NotepadText } from 'lucide-react'
import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'

interface ModePickerProps {
  mode: DesktopSessionMode
  onSelect: (mode: DesktopSessionMode) => void
  disabled?: boolean
  triggerClassName?: string
}

export function ModePicker({ mode, onSelect, disabled = false, triggerClassName = '' }: ModePickerProps) {
  const planEnabled = mode === 'plan'
  const nextMode: DesktopSessionMode = planEnabled ? 'auto' : 'plan'

  return (
    <div className="inline-flex min-w-0 items-center">
      <button
        type="button"
        onClick={() => onSelect(nextMode)}
        disabled={disabled}
        aria-label={`${planEnabled ? 'Disable' : 'Enable'} plan mode`}
        aria-pressed={planEnabled}
        title={`${planEnabled ? 'Disable' : 'Enable'} plan mode`}
        className={`inline-flex min-h-9 min-w-0 items-center gap-2 rounded-none border-0 border-b-2 px-3 py-2 text-xs font-medium transition disabled:cursor-not-allowed disabled:opacity-50 ${planEnabled ? 'border-transparent bg-transparent text-[var(--app-text)] hover:border-[var(--app-border-accent)]' : 'border-transparent bg-transparent text-[var(--app-text-muted)] opacity-60 hover:border-[var(--app-border-accent)] hover:text-[var(--app-text)] hover:opacity-100'} ${triggerClassName}`}
      >
        <NotepadText size={15} className="shrink-0" />
        <span className="font-semibold tracking-wide">plan</span>
      </button>
    </div>
  )
}
