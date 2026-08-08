import { ChevronDown, Settings2 } from 'lucide-react'

export interface ComposerPlanModelControlProps {
  provider?: string
  model?: string
  thinking?: string
  serviceTier?: string
  statusLabel?: string
  disabled?: boolean
  open?: boolean
  popoverAnchorId?: string
  onOpen: () => void
}

function modelShorthand(provider: string, model: string): string {
  const normalizedModel = model.trim().split('/').filter(Boolean).pop() ?? ''
  if (!normalizedModel) return provider.trim() || 'Model settings'
  return provider.trim() ? `${provider.trim()}/${normalizedModel}` : normalizedModel
}

/** Canonical agent/model settings trigger used at every composer breakpoint. */
export function ComposerPlanModelControl({
  provider = '',
  model = '',
  thinking = '',
  serviceTier = '',
  statusLabel = '',
  disabled = false,
  open = false,
  popoverAnchorId,
  onOpen,
}: ComposerPlanModelControlProps) {
  const modelLabel = statusLabel.trim() || modelShorthand(provider, model)
  const detailLabel = statusLabel.trim()
    ? ''
    : [thinking.trim(), serviceTier.trim()].filter(Boolean).join(' · ')
  const selectionLabel = [modelLabel, detailLabel].filter(Boolean).join(', ')

  return <button
    type="button"
    onClick={onOpen}
    disabled={disabled}
    aria-label={`Open agent and model settings. Current selection: ${selectionLabel}`}
    aria-haspopup="dialog"
    aria-expanded={open}
    title={selectionLabel}
    data-composer-agent-model-control
    data-model-favorites-anchor={popoverAnchorId}
    className="group inline-flex min-h-9 min-w-0 max-w-[9.5rem] items-center gap-1.5 rounded-lg border border-[var(--app-border)] bg-[var(--app-surface)] px-2 text-left shadow-sm transition-colors hover:bg-[var(--app-surface-hover)] disabled:cursor-not-allowed disabled:opacity-50 sm:max-w-[20rem]"
  >
    <Settings2 size={13} className="shrink-0 text-[var(--app-text-subtle)]" aria-hidden="true" />
    <span className="min-w-0 truncate text-xs font-semibold text-[var(--app-text-subtle)]">{modelLabel}</span>
    {detailLabel ? <span className="hidden shrink-0 text-[10px] text-[var(--app-text-subtle)] lg:inline">{detailLabel}</span> : null}
    <ChevronDown size={13} className="shrink-0 text-[var(--app-text-muted)]" aria-hidden="true" />
  </button>
}
