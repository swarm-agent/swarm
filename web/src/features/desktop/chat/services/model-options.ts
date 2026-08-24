import type { ModelOptionRecord, ModelPricingRecord } from '../types/chat'

const FALLBACK_SERVICE_TIER_LABELS: Record<string, string> = {
  priority: 'Priority',
  fast: 'Fast',
  flex: 'Flex',
  batch: 'Batch',
}

const FALLBACK_THINKING_OPTIONS = ['off', 'low', 'medium', 'high', 'xhigh']

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
    case 'github-copilot':
      return 'copilot'
    case 'fireworks-ai':
      return 'fireworks'
    default:
      return value.trim().toLowerCase()
  }
}

export function normalizeModelThinking(value: string): string {
  return value.trim().toLowerCase() || 'off'
}

export function modelOptionKey(provider: string, model: string, contextMode = ''): string {
  return `${normalizeProviderID(provider)}:${model.trim()}:${contextMode.trim().toLowerCase()}`
}

export function modelProviderLabel(provider: string): string {
  const normalized = normalizeProviderID(provider)
  if (normalized === 'openrouter') return 'OpenRouter (routed)'
  return normalized
}

export function modelUpstreamFamily(provider: string, model: string): string {
  if (normalizeProviderID(provider) !== 'openrouter') return ''
  const family = model.trim().split('/', 1)[0]?.trim().toLowerCase() ?? ''
  return family || 'other'
}

export function modelUpstreamFamilyLabel(family: string): string {
  const normalized = family.trim().toLowerCase()
  if (!normalized || normalized === 'other') return 'Other'
  return normalized.replace(/(^|[-_\s])([a-z])/g, (_match, prefix: string, char: string) => `${prefix}${char.toUpperCase()}`)
}

export function modelOptionUpstreamFamily(option: Pick<ModelOptionRecord, 'provider' | 'model' | 'upstreamFamily'>): string {
  return option.upstreamFamily?.trim().toLowerCase() || modelUpstreamFamily(option.provider, option.model)
}

export function modelOptionGroupKey(option: Pick<ModelOptionRecord, 'provider' | 'model' | 'upstreamFamily'>): string {
  const provider = normalizeProviderID(option.provider)
  const family = modelOptionUpstreamFamily(option)
  return family ? `${provider}::upstream::${family}` : `${provider}::direct`
}

export function modelOptionRouteLabel(option: Pick<ModelOptionRecord, 'provider' | 'model' | 'upstreamFamily'>): string {
  const provider = normalizeProviderID(option.provider)
  if (provider !== 'openrouter') return modelProviderLabel(provider)
  return `OpenRouter → ${modelUpstreamFamilyLabel(modelOptionUpstreamFamily(option))}`
}

export function normalizeModelID(provider: string, model: string): string {
  const trimmedModel = model.trim()
  if (trimmedModel === '') return ''
  const normalizedProvider = normalizeProviderID(provider)
  if (normalizedProvider !== 'fireworks') return trimmedModel
  const lowerModel = trimmedModel.toLowerCase()
  for (const prefix of ['accounts/fireworks/models/', 'accounts/fireworks/routers/', 'fireworks/']) {
    if (!lowerModel.startsWith(prefix)) continue
    const suffix = trimmedModel.slice(prefix.length).trim()
    if (suffix && !suffix.includes('/')) return suffix
  }
  return trimmedModel
}

export function modelThinkingOptions(option: Pick<ModelOptionRecord, 'thinkingOptions'> | null | undefined): string[] {
  const seen = new Set<string>()
  const source = option?.thinkingOptions?.length ? option.thinkingOptions : FALLBACK_THINKING_OPTIONS
  const out: string[] = []
  for (const item of source) {
    const normalized = normalizeModelThinking(item)
    if (!normalized || seen.has(normalized)) continue
    seen.add(normalized)
    out.push(normalized)
  }
  return out.length > 0 ? out : FALLBACK_THINKING_OPTIONS
}

export function defaultModelThinking(option: Pick<ModelOptionRecord, 'thinkingOptions' | 'defaultThinking' | 'thinking'> | null | undefined): string {
  const options = modelThinkingOptions(option)
  const declaredDefault = normalizeModelThinking(option?.defaultThinking ?? '')
  if (options.includes(declaredDefault)) return declaredDefault
  const favoriteDefault = normalizeModelThinking(option?.thinking ?? '')
  if (options.includes(favoriteDefault)) return favoriteDefault
  if (options.includes('off')) return 'off'
  return options[0] ?? 'off'
}

function modelPresetListForProvider(provider: string): string[] {
  return MODEL_PRESETS_BY_PROVIDER[normalizeProviderID(provider)] ?? []
}

export type ModelServiceTierOption = { label: string; value: string }

function normalizedServiceTiers(provider: string, serviceTiers: string[] = []): string[] {
  const normalizedProvider = normalizeProviderID(provider)
  const seen = new Set<string>()
  const out: string[] = []
  for (const tier of serviceTiers) {
    const normalized = tier.trim().toLowerCase()
    if (!normalized || normalized === 'standard' || normalized === 'off' || seen.has(normalized)) continue
    if ((normalizedProvider === 'anthropic' || normalizedProvider === 'openai') && normalized === 'batch') continue
    if (normalizedProvider === 'google' && normalized !== 'fast' && normalized !== 'priority') continue
    seen.add(normalized)
    out.push(normalized)
  }
  return out
}

function serviceTierCandidates(option: Pick<ModelOptionRecord, 'serviceTiers' | 'serviceTierMappings'> | null | undefined): string[] {
  const values = [...(option?.serviceTiers ?? [])]
  for (const mapping of option?.serviceTierMappings ?? []) {
    if (mapping.tier) values.push(mapping.tier)
  }
  return values
}

function tierLabel(tier: string): string {
  return FALLBACK_SERVICE_TIER_LABELS[tier] ?? tier.replace(/(^|[-_\s])([a-z])/g, (_match, prefix: string, char: string) => `${prefix}${char.toUpperCase()}`)
}

export function normalizeModelServiceTier(provider: string, serviceTier: string): string {
  const normalizedProvider = normalizeProviderID(provider)
  const normalizedTier = serviceTier.trim().toLowerCase()
  if (normalizedTier === '' || normalizedTier === 'standard' || normalizedTier === 'off') return ''
  if (normalizedProvider === 'google') return normalizedTier === 'fast' || normalizedTier === 'priority' ? normalizedTier : ''
  if (normalizedProvider === 'codex' || normalizedProvider === 'fireworks' || normalizedProvider === 'openai') return normalizedTier === 'batch' ? '' : normalizedTier
  if (normalizedProvider === 'anthropic') return normalizedTier === 'batch' ? '' : normalizedTier
  return ''
}

export function supportsModelServiceTier(provider: string, _model: string, serviceTiers: string[] | Pick<ModelOptionRecord, 'serviceTiers' | 'serviceTierMappings'> = [], requestedTier = ''): boolean {
  const tiers = normalizedServiceTiers(provider, Array.isArray(serviceTiers) ? serviceTiers : serviceTierCandidates(serviceTiers))
  const requestedRaw = requestedTier.trim().toLowerCase()
  const normalizedRequested = normalizeModelServiceTier(provider, requestedTier)
  if (normalizedRequested) return tiers.includes(normalizedRequested)
  if (requestedRaw && requestedRaw !== 'standard' && requestedRaw !== 'off') return false
  return ['codex', 'fireworks', 'google', 'anthropic', 'openai'].includes(normalizeProviderID(provider)) ? tiers.length > 0 : false
}

export function modelServiceTierOptions(provider: string, _model: string, serviceTiers: string[] | Pick<ModelOptionRecord, 'serviceTiers' | 'serviceTierMappings'> = []): ModelServiceTierOption[] {
  const tiers = normalizedServiceTiers(provider, Array.isArray(serviceTiers) ? serviceTiers : serviceTierCandidates(serviceTiers))
  return [
    { label: 'Off / standard', value: '' },
    ...tiers.map((tier) => ({ label: tierLabel(tier), value: tier })),
  ]
}

export function codexFastEnabled(provider: string, _model: string, serviceTier: string, serviceTiers: string[] | Pick<ModelOptionRecord, 'serviceTiers' | 'serviceTierMappings'> = []): boolean {
  return normalizeProviderID(provider) === 'codex' && normalizedServiceTiers(provider, Array.isArray(serviceTiers) ? serviceTiers : serviceTierCandidates(serviceTiers)).includes('fast') && normalizeModelServiceTier(provider, serviceTier) === 'fast'
}

export function displayModelName(provider: string, model: string, contextMode: string): string {
  const displayName = normalizeModelID(provider, model)
  if (displayName === '') {
    return ''
  }
  const mode = contextMode.trim().toLowerCase()
  return mode ? `${displayName} (${mode})` : displayName
}

export function effectiveContextWindow(_provider: string, _model: string, _contextMode: string, fallback: number): number {
  return fallback
}

export function modelAllowedByProviderPreset(_provider: string, model: string): boolean {
  return model.trim() !== ''
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

function formatCompactContextWindow(value: number, unit: number, suffix: string): string {
  const truncated = Math.floor(value / (unit / 100)) / 100
  return `${truncated.toFixed(2).replace(/\.?0+$/u, '')}${suffix}`
}

export function formatContextWindow(value: number): string {
  if (!Number.isFinite(value) || value <= 0) {
    return ''
  }
  if (value >= 1_000_000) {
    return formatCompactContextWindow(value, 1_000_000, 'm')
  }
  if (value >= 1_000) {
    return formatCompactContextWindow(value, 1_000, 'k')
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
