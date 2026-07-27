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
        className={`inline-flex min-h-9 min-w-9 items-center justify-center rounded-lg border-0 bg-transparent p-2 transition-all hover:-translate-y-0.5 hover:shadow-sm disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:translate-y-0 disabled:hover:shadow-none ${planEnabled ? 'text-[var(--app-text)]' : 'text-[var(--app-text-muted)] opacity-60 hover:text-[var(--app-text)] hover:opacity-100'} ${triggerClassName}`}
      >
        <NotepadText size={15} className="shrink-0" />
      </button>
    </div>
  )
}
