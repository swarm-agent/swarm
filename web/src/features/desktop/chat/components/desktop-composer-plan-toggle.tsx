import { NotepadText } from 'lucide-react'

export interface DesktopComposerPlanToggleProps {
  active: boolean
  onActiveChange?: (active: boolean) => void
  disabled?: boolean
  readOnly?: boolean
  className?: string
}

/** One-way Plan entry control that becomes a locked status indicator once active. */
export function DesktopComposerPlanToggle({
  active,
  onActiveChange,
  disabled = false,
  readOnly = false,
  className = '',
}: DesktopComposerPlanToggleProps) {
  const locked = readOnly || active
  const label = active
    ? 'Plan mode remains active until planning exits'
    : readOnly ? 'Plan mode disabled' : 'Enable plan mode'

  return (
    <button
      type="button"
      onClick={() => {
        if (!locked) onActiveChange?.(true)
      }}
      disabled={disabled || (!locked && !onActiveChange)}
      aria-label={label}
      aria-pressed={active}
      title={label}
      data-testid="desktop-composer-plan-toggle"
      data-plan-active={active ? 'true' : 'false'}
      className={`inline-flex min-h-9 min-w-9 shrink-0 items-center justify-center rounded-lg border bg-transparent p-2 text-[var(--app-text-muted)] transition-all hover:-translate-y-0.5 hover:text-[var(--app-text)] hover:shadow-sm disabled:cursor-not-allowed disabled:opacity-50 disabled:hover:translate-y-0 disabled:hover:shadow-none ${active ? 'border-[var(--app-border-accent)] bg-[var(--app-surface-hover)] text-[var(--app-text)] ring-1 ring-[var(--app-border-accent)]' : 'border-transparent'} ${className}`}
    >
      <NotepadText size={15} strokeWidth={active ? 2.25 : 1.75} aria-hidden="true" />
    </button>
  )
}
