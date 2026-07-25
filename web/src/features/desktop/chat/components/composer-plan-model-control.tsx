import { ChevronDown, NotepadText } from 'lucide-react'
import type { DesktopSessionMode } from '../../settings/swarm/types/swarm-settings'

export interface ComposerPlanModelControlProps {
  mode: DesktopSessionMode
  provider?: string
  model?: string
  thinking?: string
  serviceTier?: string
  planDisabled?: boolean
  pickerDisabled?: boolean
  open?: boolean
  onPlanToggle: () => void
  onPickerOpen: () => void
}

function modelShorthand(provider: string, model: string): string {
  const normalizedModel = model.trim().split('/').filter(Boolean).pop() ?? ''
  if (!normalizedModel) return provider.trim() || 'Default model'
  return provider.trim() ? `${provider.trim()}/${normalizedModel}` : normalizedModel
}

/** Joined plan/model control with the model policy progressively collapsed on narrow screens. */
export function ComposerPlanModelControl({ mode, provider = '', model = '', thinking = '', serviceTier = '', planDisabled = false, pickerDisabled = false, open = false, onPlanToggle, onPickerOpen }: ComposerPlanModelControlProps) {
  const planEnabled = mode === 'plan'
  const modelLabel = modelShorthand(provider, model)
  const detailLabel = [thinking.trim(), serviceTier.trim()].filter(Boolean).join(' · ')
  const selectionLabel = [modelLabel, detailLabel].filter(Boolean).join(', ')

  return <div className="inline-flex min-w-0 max-w-full items-stretch overflow-hidden rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] shadow-sm" data-composer-plan-model-control>
    <button type="button" onClick={onPlanToggle} disabled={planDisabled} aria-label={`${planEnabled ? 'Disable' : 'Enable'} plan mode`} aria-pressed={planEnabled} title={`${planEnabled ? 'Disable' : 'Enable'} plan mode`} className={`inline-flex min-h-9 min-w-9 shrink-0 items-center justify-center border-r border-[var(--app-border)] px-2 text-[var(--app-text-subtle)] transition-colors hover:bg-[var(--app-surface-hover)] disabled:opacity-50 ${planEnabled ? 'bg-[var(--app-surface-hover)]' : ''}`}><NotepadText size={14} strokeWidth={planEnabled ? 2.25 : 1.75} aria-hidden="true" /></button>
    <button type="button" onClick={onPickerOpen} disabled={pickerDisabled} aria-label={`Choose profile, agent, or model. Current selection: ${selectionLabel}`} aria-haspopup="menu" aria-expanded={open} title={selectionLabel} className="group inline-flex min-h-9 min-w-0 max-w-[8.5rem] items-center gap-1 px-1.5 text-left transition-colors hover:bg-[var(--app-surface-hover)] disabled:opacity-50 sm:max-w-[18rem] sm:gap-1.5 sm:px-2">
      <span className="min-w-0 truncate text-xs font-semibold text-[var(--app-text-subtle)]">{modelLabel}</span>
      {detailLabel ? <span className="hidden shrink-0 text-[10px] text-[var(--app-text-subtle)] lg:inline">{detailLabel}</span> : null}
      <ChevronDown size={13} className="shrink-0 text-[var(--app-text-muted)]" aria-hidden="true" />
    </button>
  </div>
}
