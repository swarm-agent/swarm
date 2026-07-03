import type { ModelOptionRecord, ModelPricingRecord } from '../types/chat'

const CODEX_CONTEXT_MODE_1M = '1m'
const CODEX_GPT54_DEFAULT_CONTEXT_WINDOW = 272_000
const CODEX_GPT54_1M_CONTEXT_WINDOW = 1_050_000
const CODEX_GPT55_DEFAULT_CONTEXT_WINDOW = 272_000
const FIREWORKS_MODEL_PREFIX = 'accounts/fireworks/models/'

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
  return serviceTiers.map((tier) => tier.trim().toLowerCase()).filter(Boolean)
}

export function normalizeModelServiceTier(provider: string, serviceTier: string): string {
  const normalizedProvider = normalizeProviderID(provider)
  const normalizedTier = serviceTier.trim().toLowerCase()
  if (normalizedProvider === 'codex') {
    return normalizedTier === 'priority' || normalizedTier === 'fast' || normalizedTier === 'flex' ? normalizedTier : ''
  }
  if (normalizedProvider === 'fireworks') {
    return normalizedTier === 'priority' || normalizedTier === 'fast' ? normalizedTier : ''
  }
  return ''
}

export function supportsCodexFastMode(provider: string, _model: string, serviceTiers: string[] = []): boolean {
  return normalizeProviderID(provider) === 'codex' && normalizedServiceTiers(serviceTiers).includes('fast')
}

export function supportsModelServiceTier(provider: string, _model: string, serviceTiers: string[] = [], requestedTier = ''): boolean {
  const normalizedProvider = normalizeProviderID(provider)
  const tiers = normalizedServiceTiers(serviceTiers)
  const normalizedRequested = normalizeModelServiceTier(provider, requestedTier)
  if (normalizedProvider === 'codex') {
    if (normalizedRequested) return tiers.includes(normalizedRequested)
    return tiers.includes('priority') || tiers.includes('fast') || tiers.includes('flex')
  }
  if (normalizedProvider === 'fireworks') {
    if (normalizedRequested) return tiers.includes(normalizedRequested)
    return tiers.includes('priority') || tiers.includes('fast')
  }
  return false
}

export function modelServiceTierOptions(provider: string, _model: string, serviceTiers: string[] = []): ModelServiceTierOption[] {
  const normalizedProvider = normalizeProviderID(provider)
  const tiers = normalizedServiceTiers(serviceTiers)
  const options: ModelServiceTierOption[] = [{ label: 'Off / standard', value: '' }]
  if (normalizedProvider === 'codex') {
    if (tiers.includes('priority')) options.push({ label: 'Priority', value: 'priority' })
    if (tiers.includes('fast')) options.push({ label: 'Fast', value: 'fast' })
    if (tiers.includes('flex')) options.push({ label: 'Flex', value: 'flex' })
    return options
  }
  if (normalizedProvider === 'fireworks') {
    if (tiers.includes('priority')) options.push({ label: 'Priority', value: 'priority' })
    if (tiers.includes('fast')) options.push({ label: 'Fast', value: 'fast' })
    return options
  }
  return options
}

export function codexFastEnabled(provider: string, _model: string, serviceTier: string, serviceTiers: string[] = []): boolean {
  return supportsCodexFastMode(provider, _model, serviceTiers) && normalizeModelServiceTier(provider, serviceTier) === 'fast'
}

export function supportsCodex1MMode(provider: string, model: string): boolean {
  return normalizeProviderID(provider) === 'codex' && model.trim().toLowerCase() === 'gpt-5.4'
}

export function codex1MEnabled(provider: string, model: string, contextMode: string): boolean {
  return supportsCodex1MMode(provider, model) && contextMode.trim().toLowerCase() === CODEX_CONTEXT_MODE_1M
}

export function displayModelName(provider: string, model: string, contextMode: string): string {
  const trimmedModel = model.trim()
  if (trimmedModel === '') {
    return ''
  }
  const normalizedProvider = normalizeProviderID(provider)
  let displayName = trimmedModel
  if (normalizedProvider === 'fireworks' && trimmedModel.toLowerCase().startsWith(FIREWORKS_MODEL_PREFIX)) {
    displayName = trimmedModel.slice(FIREWORKS_MODEL_PREFIX.length).trim()
  }
  return codex1MEnabled(provider, trimmedModel, contextMode) ? `${displayName} (1m)` : displayName
}

export function effectiveContextWindow(provider: string, model: string, contextMode: string, fallback: number): number {
  const normalizedModel = model.trim().toLowerCase()
  if (normalizeProviderID(provider) === 'codex' && normalizedModel === 'gpt-5.5') {
    return CODEX_GPT55_DEFAULT_CONTEXT_WINDOW
  }
  if (!supportsCodex1MMode(provider, model)) {
    return fallback
  }
  return codex1MEnabled(provider, model, contextMode)
    ? CODEX_GPT54_1M_CONTEXT_WINDOW
    : (fallback > 0 ? fallback : CODEX_GPT54_DEFAULT_CONTEXT_WINDOW)
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
