import { ChevronDown } from 'lucide-react'

export interface ComposerPlanModelControlProps {
  provider?: string
  model?: string
  thinking?: string
  serviceTier?: string
  pickerDisabled?: boolean
  open?: boolean
  onPickerOpen: () => void
}

function modelShorthand(provider: string, model: string): string {
  const normalizedModel = model.trim().split('/').filter(Boolean).pop() ?? ''
  if (!normalizedModel) return provider.trim() || 'Default model'
  return provider.trim() ? `${provider.trim()}/${normalizedModel}` : normalizedModel
}

/** Compact model control with policy details progressively collapsed on narrow screens. */
export function ComposerPlanModelControl({ provider = '', model = '', thinking = '', serviceTier = '', pickerDisabled = false, open = false, onPickerOpen }: ComposerPlanModelControlProps) {
  const modelLabel = modelShorthand(provider, model)
  const detailLabel = [thinking.trim(), serviceTier.trim()].filter(Boolean).join(' · ')
  const selectionLabel = [modelLabel, detailLabel].filter(Boolean).join(', ')

  return <div className="inline-flex min-w-0 max-w-full" data-composer-plan-model-control>
    <button type="button" onClick={onPickerOpen} disabled={pickerDisabled} aria-label={`Choose profile, agent, or model. Current selection: ${selectionLabel}`} aria-haspopup="menu" aria-expanded={open} title={selectionLabel} className="group inline-flex min-h-9 min-w-0 max-w-[9.5rem] items-center gap-1.5 rounded-full border border-[var(--app-border)]/70 bg-[var(--app-surface)] px-3 text-left shadow-sm transition-all hover:-translate-y-px hover:border-[var(--app-border-strong)] hover:bg-[var(--app-surface-hover)] hover:shadow-md focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--app-primary)]/35 disabled:translate-y-0 disabled:cursor-not-allowed disabled:opacity-50 disabled:shadow-none sm:max-w-[20rem]">
      <span className="min-w-0 truncate text-xs font-semibold text-[var(--app-text)]/90">{modelLabel}</span>
      {detailLabel ? <span className="hidden shrink-0 text-[10px] text-[var(--app-text-subtle)] lg:inline">{detailLabel}</span> : null}
      <ChevronDown size={13} className="shrink-0 text-[var(--app-text-muted)] transition-transform group-aria-expanded:rotate-180" aria-hidden="true" />
    </button>
  </div>
}
