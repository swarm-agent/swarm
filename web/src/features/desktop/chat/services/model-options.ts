import type { ModelOptionRecord, ModelPricingRecord } from '../types/chat'

const FALLBACK_SERVICE_TIER_LABELS: Record<string, string> = {
  priority: 'Priority',
  fast: 'Fast',
  flex: 'Flex',
  batch: 'Batch',
}

const MODEL_PRESETS_BY_PROVIDER: Record<string, string[]> = {
  codex: [
    'gpt-5.5',
    'gpt-5.4',
    'gpt-5.4-mini',
    'gpt-5.3-codex',
    'gpt-5.3-codex-spark',
    'gpt-5.2',
    'gpt-5.1-codex-max',
    'gpt-5.1-codex-mini',
  ],
  google: [
    'gemini-3.1-pro-preview',
    'gemini-3-flash-preview',
    'gemini-2.5-pro',
    'gemini-2.5-flash',
    'gemini-2.0-flash',
  ],
}

export function normalizeProviderID(value: string): string {
  switch (value.trim().toLowerCase()) {
    case 'openai':
      return 'codex'
    case 'github-copilot':
      return 'copilot'
    case 'fireworks-ai':
      return 'fireworks'
    default:
      return value.trim().toLowerCase()
  }
}

function modelPresetListForProvider(provider: string): string[] {
  return MODEL_PRESETS_BY_PROVIDER[normalizeProviderID(provider)] ?? []
}

export type ModelServiceTierOption = { label: string; value: string }

function normalizedServiceTiers(serviceTiers: string[] = []): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const tier of serviceTiers) {
    const normalized = tier.trim().toLowerCase()
    if (!normalized || normalized === 'standard' || normalized === 'off' || seen.has(normalized)) continue
    seen.add(normalized)
    out.push(normalized)
  }
  return out
}

function tierLabel(tier: string): string {
  return FALLBACK_SERVICE_TIER_LABELS[tier] ?? tier.replace(/(^|[-_\s])([a-z])/g, (_match, prefix: string, char: string) => `${prefix}${char.toUpperCase()}`)
}

export function normalizeModelServiceTier(provider: string, serviceTier: string): string {
  const normalizedProvider = normalizeProviderID(provider)
  const normalizedTier = serviceTier.trim().toLowerCase()
  if (normalizedProvider === 'codex' || normalizedProvider === 'fireworks') {
    return normalizedTier && normalizedTier !== 'standard' && normalizedTier !== 'off' ? normalizedTier : ''
  }
  return ''
}

export function supportsModelServiceTier(provider: string, _model: string, serviceTiers: string[] = [], requestedTier = ''): boolean {
  const tiers = normalizedServiceTiers(serviceTiers)
  const normalizedRequested = normalizeModelServiceTier(provider, requestedTier)
  if (normalizedRequested) return tiers.includes(normalizedRequested)
  return normalizeProviderID(provider) === 'codex' || normalizeProviderID(provider) === 'fireworks'
    ? tiers.length > 0
    : false
}

export function modelServiceTierOptions(_provider: string, _model: string, serviceTiers: string[] = []): ModelServiceTierOption[] {
  const tiers = normalizedServiceTiers(serviceTiers)
  return [
    { label: 'Off / standard', value: '' },
    ...tiers.map((tier) => ({ label: tierLabel(tier), value: tier })),
  ]
}

export function codexFastEnabled(provider: string, _model: string, serviceTier: string, serviceTiers: string[] = []): boolean {
  return normalizeProviderID(provider) === 'codex' && normalizedServiceTiers(serviceTiers).includes('fast') && normalizeModelServiceTier(provider, serviceTier) === 'fast'
}

export function displayModelName(provider: string, model: string, contextMode: string): string {
  const trimmedModel = model.trim()
  if (trimmedModel === '') {
    return ''
  }
  const normalizedProvider = normalizeProviderID(provider)
  let displayName = trimmedModel
  if (normalizedProvider === 'fireworks') {
    const lowerModel = trimmedModel.toLowerCase()
    const modelPrefix = 'accounts/fireworks/models/'
    const routerPrefix = 'accounts/fireworks/routers/'
    if (lowerModel.startsWith(modelPrefix)) {
      displayName = trimmedModel.slice(modelPrefix.length).trim()
    } else if (lowerModel.startsWith(routerPrefix)) {
      displayName = trimmedModel.slice(routerPrefix.length).trim()
    }
  }
  const mode = contextMode.trim().toLowerCase()
  return mode ? `${displayName} (${mode})` : displayName
}

export function effectiveContextWindow(_provider: string, _model: string, _contextMode: string, fallback: number): number {
  return fallback
}

export function modelAllowedByProviderPreset(provider: string, model: string): boolean {
  const normalizedProvider = normalizeProviderID(provider)
  const normalizedModel = model.trim()
  if (!normalizedModel) {
    return false
  }
  if (normalizedProvider !== 'codex') {
    return true
  }
  const presets = modelPresetListForProvider(normalizedProvider)
  if (presets.length === 0) {
    return true
  }
  return presets.some((preset) => preset.localeCompare(normalizedModel, undefined, { sensitivity: 'accent' }) === 0)
}

function modelIDLessForProvider(provider: string, left: string, right: string): boolean {
  const normalizedProvider = normalizeProviderID(provider)
  const leftModel = left.trim().toLowerCase()
  const rightModel = right.trim().toLowerCase()
  if (leftModel === rightModel) {
    return false
  }

  const presets = modelPresetListForProvider(normalizedProvider).map((preset) => preset.trim().toLowerCase())
  const leftPreset = presets.indexOf(leftModel)
  const rightPreset = presets.indexOf(rightModel)
  if (leftPreset >= 0 || rightPreset >= 0) {
    if (leftPreset < 0) {
      return false
    }
    if (rightPreset < 0) {
      return true
    }
    if (leftPreset !== rightPreset) {
      return leftPreset < rightPreset
    }
  }

  if (normalizedProvider === 'google') {
    return leftModel > rightModel
  }
  return leftModel < rightModel
}

export function sortModelOptions(options: ModelOptionRecord[]): ModelOptionRecord[] {
  return [...options].sort((left, right) => {
    const leftProvider = normalizeProviderID(left.provider)
    const rightProvider = normalizeProviderID(right.provider)
    if (leftProvider !== rightProvider) {
      return leftProvider.localeCompare(rightProvider)
    }
    if (left.favorite !== right.favorite) {
      return left.favorite ? -1 : 1
    }
    if (modelIDLessForProvider(leftProvider, left.model, right.model)) {
      return -1
    }
    if (modelIDLessForProvider(leftProvider, right.model, left.model)) {
      return 1
    }
    const leftContextMode = left.contextMode.trim().toLowerCase()
    const rightContextMode = right.contextMode.trim().toLowerCase()
    if (leftContextMode !== rightContextMode) {
      if (leftContextMode === '') {
        return -1
      }
      if (rightContextMode === '') {
        return 1
      }
      return leftContextMode.localeCompare(rightContextMode)
    }
    return left.label.localeCompare(right.label)
  })
}

export function formatContextWindow(value: number): string {
  if (!Number.isFinite(value) || value <= 0) {
    return ''
  }
  if (value >= 1_000_000) {
    const millions = value / 1_000_000
    return millions % 1 === 0 ? `${millions}m` : `${millions.toFixed(1)}m`
  }
  if (value >= 1_000) {
    const thousands = value / 1_000
    return thousands % 1 === 0 ? `${thousands}k` : `${thousands.toFixed(1)}k`
  }
  return `${value}`
}

function finitePricingNumber(value: unknown): number | null {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? value : null
}

function formatPricingAmount(value: number): string {
  if (value === 0) return '$0'
  if (value < 0.01) return `$${value.toFixed(4)}`
  if (value < 1) return `$${value.toFixed(3).replace(/0+$/u, '').replace(/\.$/u, '')}`
  return `$${value.toFixed(2).replace(/\.00$/u, '')}`
}

export function formatModelPricing(pricing: ModelPricingRecord | null | undefined): string {
  if (!pricing || typeof pricing !== 'object') return ''
  if (pricing.is_free === true) return 'Free'
  const input = finitePricingNumber(pricing.input_price_per_million_tokens)
  const output = finitePricingNumber(pricing.output_price_per_million_tokens)
  const cached = finitePricingNumber(pricing.cached_input_price_per_million_tokens)
  const parts: string[] = []
  if (input !== null) parts.push(`${formatPricingAmount(input)} in`)
  if (output !== null) parts.push(`${formatPricingAmount(output)} out`)
  if (cached !== null) parts.push(`${formatPricingAmount(cached)} cached`)
  return parts.length > 0 ? `${parts.join(' / ')} per 1M tokens` : ''
}
