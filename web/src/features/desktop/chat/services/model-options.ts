import type { ModelOptionRecord } from '../types/chat'

const CODEX_CONTEXT_MODE_1M = '1m'
const CODEX_GPT54_DEFAULT_CONTEXT_WINDOW = 272_000
const CODEX_GPT54_1M_CONTEXT_WINDOW = 1_050_000

const MODEL_PRESETS_BY_PROVIDER: Record<string, string[]> = {
  codex: [
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

function normalizeProviderID(value: string): string {
  switch (value.trim().toLowerCase()) {
    case 'openai':
      return 'codex'
    case 'github-copilot':
      return 'copilot'
    default:
      return value.trim().toLowerCase()
  }
}

function modelPresetListForProvider(provider: string): string[] {
  return MODEL_PRESETS_BY_PROVIDER[normalizeProviderID(provider)] ?? []
}

export function supportsCodexFastMode(provider: string, model: string): boolean {
  return normalizeProviderID(provider) === 'codex' && model.trim().toLowerCase() === 'gpt-5.4'
}

export function codexFastEnabled(provider: string, model: string, serviceTier: string): boolean {
  return supportsCodexFastMode(provider, model) && serviceTier.trim().toLowerCase() === 'fast'
}

export function supportsCodex1MMode(provider: string, model: string): boolean {
  return supportsCodexFastMode(provider, model)
}

export function codex1MEnabled(provider: string, model: string, contextMode: string): boolean {
  return supportsCodex1MMode(provider, model) && contextMode.trim().toLowerCase() === CODEX_CONTEXT_MODE_1M
}

export function displayModelName(provider: string, model: string, contextMode: string): string {
  const trimmedModel = model.trim()
  if (trimmedModel === '') {
    return ''
  }
  return codex1MEnabled(provider, trimmedModel, contextMode) ? `${trimmedModel} (1m)` : trimmedModel
}

export function effectiveContextWindow(provider: string, model: string, contextMode: string, fallback: number): number {
  if (supportsCodex1MMode(provider, model)) {
    return codex1MEnabled(provider, model, contextMode)
      ? CODEX_GPT54_1M_CONTEXT_WINDOW
      : (fallback > 0 ? fallback : CODEX_GPT54_DEFAULT_CONTEXT_WINDOW)
  }
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
