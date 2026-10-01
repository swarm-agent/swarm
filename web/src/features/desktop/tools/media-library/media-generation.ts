import type { MediaCatalogModelOption } from '../../settings/media/queries/get-media-settings'
import type { MediaLibraryItem } from './types'

export interface MediaGenerationSettings {
  aspectRatio?: string
  resolution?: string
  durationSeconds?: number
  includesAudio?: boolean
}

export type MediaGenerationAction = 'fine_tune' | 'iterate' | 'to_video' | 'next_scene'

export interface MediaGenerationRequest {
  requestId?: string
  item: MediaLibraryItem
  action: MediaGenerationAction
  deltaPrompt: string
  variantCount: number
  model: string
  settings: MediaGenerationSettings
}

export interface MediaGenerationJob {
  id: string
  taskId?: string
  prompt?: string
  createdAt?: number
  outputIds?: string[]
  sourceId: string
  title: string
  count: number
  status: string
  error?: string
  sessionId?: string
}

export interface PricingLineCondition {
  resolution?: string
  image_size?: string
  includes_audio?: boolean
  tier?: string
  service_tier?: string
  duration_seconds?: number
  charged_only_on_success?: boolean
  output_tokens?: number
  output_tokens_per_second?: number
  [key: string]: unknown
}

export interface BillingLineRecord {
  billable?: string
  unit?: string
  price_usd?: number | string
  quantity?: number | string
  billing_quantity?: number | string
  kind?: string
  variant?: string
  conditions?: PricingLineCondition
  [key: string]: unknown
}

export interface ParsedPricingLine {
  kind?: string
  billable: string
  unit: string
  priceUsd: number
  quantity: number
  variant?: string
  conditions?: PricingLineCondition
}

export interface CalculatedCostEstimate {
  unitPrice: number
  quantity: number
  unitLabel: string
  totalPrice: number
  formattedPerUnit: string
  formattedTotal: string
  isAvailable: boolean
  isVerified: boolean
  matchedLineDescription?: string
}

const ALLOWED_MEDIA_UNITS = new Set(['image', 'second', 'video', 'clip'])
const KNOWN_CONDITION_KEYS = new Set([
  'resolution',
  'image_size',
  'tier',
  'service_tier',
  'includes_audio',
  'charged_only_on_success',
  'output_tokens',
  'output_tokens_per_second',
  'duration_seconds',
])

export function normalizeResKey(raw: string | undefined): string {
  if (!raw) return ''
  const c = raw.toLowerCase().trim()
  if (
    c === '1k' ||
    c === '1024x1024' ||
    c === '1024×1024' ||
    c === 'standard' ||
    c === '1024' ||
    c === 'up_to_1024x1024' ||
    c === 'up to 1024x1024'
  ) {
    return '1k'
  }
  if (
    c === '2k' ||
    c === '2048x2048' ||
    c === '2048×2048' ||
    c === 'hd' ||
    c === '2048' ||
    c === 'up_to_2048x2048' ||
    c === 'up to 2048x2048'
  ) {
    return '2k'
  }
  if (
    c === '4k' ||
    c === '4096x4096' ||
    c === '4096×4096' ||
    c === 'ultra_hd' ||
    c === 'uhd' ||
    c === '4096' ||
    c === 'up_to_4096x4096' ||
    c === 'up to 4096x4096'
  ) {
    return '4k'
  }
  if (c === '720p' || c === '1280x720') return '720p'
  if (c === '1080p' || c === '1920x1080') return '1080p'
  return c
}

export function getLineResolution(line: ParsedPricingLine): string | undefined {
  const condRes = line.conditions?.resolution || line.conditions?.image_size
  if (condRes !== undefined && condRes !== null && String(condRes).trim() !== '') {
    return normalizeResKey(String(condRes))
  }
  if (line.variant) {
    const v = line.variant.trim()
    const lower = v.toLowerCase()
    if (
      lower === 'batch' ||
      lower === 'flex' ||
      lower === 'priority' ||
      lower.includes('input') ||
      lower === 'standard_output'
    ) {
      return undefined
    }
    const norm = normalizeResKey(v)
    if (
      norm === '1k' ||
      norm === '2k' ||
      norm === '4k' ||
      norm === '720p' ||
      norm === '1080p' ||
      /^\d+[kp]$/i.test(norm) ||
      /^\d+x\d+$/i.test(norm)
    ) {
      // If variant was "standard" but line is for video, "standard" is not a video resolution
      if (lower === 'standard' && (line.unit === 'second' || line.unit === 'clip' || line.unit === 'video')) {
        return undefined
      }
      return norm
    }
  }
  return undefined
}

export function extractBillingLines(pricing: unknown): ParsedPricingLine[] {
  if (!pricing || typeof pricing !== 'object') return []
  const p = pricing as Record<string, unknown>
  const billing = p.billing as Record<string, unknown> | undefined
  const linesRaw = billing && Array.isArray(billing.lines) ? billing.lines : Array.isArray(p.lines) ? p.lines : []

  const results: ParsedPricingLine[] = []
  for (const line of linesRaw) {
    if (!line || typeof line !== 'object') continue
    const unitRaw = typeof line.unit === 'string' ? line.unit.trim().toLowerCase() : ''
    // Strict allowlist: only image, second, video, clip. Tokens are rejected.
    if (!ALLOWED_MEDIA_UNITS.has(unitRaw)) {
      continue
    }

    const billable = typeof line.billable === 'string' ? line.billable.trim() : ''
    // Output billable not input: input rates are strictly rejected
    if (billable.toLowerCase().includes('input')) {
      continue
    }

    const priceRaw = line.price_usd
    const priceNum = typeof priceRaw === 'number' ? priceRaw : typeof priceRaw === 'string' ? parseFloat(priceRaw) : NaN

    // Valid prices must be finite and >= 0 (zero prices are valid finite rates)
    if (!Number.isFinite(priceNum) || priceNum < 0) {
      continue
    }

    // Billing quantity > 0 division
    const rawQty = line.quantity ?? line.billing_quantity
    let quantity = 1
    if (rawQty !== undefined && rawQty !== null) {
      const parsedQty = typeof rawQty === 'number' ? rawQty : parseFloat(String(rawQty))
      if (!Number.isFinite(parsedQty) || parsedQty <= 0) {
        // Non-positive or invalid quantity rejects line
        continue
      }
      quantity = parsedQty
    }

    const effectivePriceUsd = priceNum / quantity

    results.push({
      kind: typeof line.kind === 'string' ? line.kind : undefined,
      billable,
      unit: unitRaw,
      priceUsd: effectivePriceUsd,
      quantity,
      variant: typeof line.variant === 'string' ? line.variant.trim() : undefined,
      conditions: line.conditions as PricingLineCondition | undefined,
    })
  }
  return results
}

export function resolveAudioContext(
  settingsAudio: boolean | undefined,
  modelOption: MediaCatalogModelOption | undefined,
  videoLines: ParsedPricingLine[],
): { audio: boolean | undefined; ambiguous: boolean } {
  if (typeof settingsAudio === 'boolean') {
    return { audio: settingsAudio, ambiguous: false }
  }

  // Check explicit model capabilities / modalities on modelOption if present
  const rawModel = modelOption as Record<string, unknown> | undefined
  if (rawModel) {
    const caps = rawModel.capabilities as Record<string, unknown> | undefined
    if (caps && typeof caps.supports_audio_output === 'boolean') {
      return { audio: caps.supports_audio_output, ambiguous: false }
    }
    const modalities = rawModel.modalities as { output?: string[] } | undefined
    if (modalities && Array.isArray(modalities.output)) {
      const hasAudioOut = modalities.output.includes('audio')
      const hasVideoOut = modalities.output.includes('video')
      if (hasVideoOut) {
        return { audio: hasAudioOut, ambiguous: false }
      }
    }
  }

  // Inspect video lines
  const linesWithAudioTrue = videoLines.filter((l) => l.conditions?.includes_audio === true)
  const linesWithAudioFalse = videoLines.filter((l) => l.conditions?.includes_audio === false)
  const linesWithoutAudio = videoLines.filter(
    (l) => l.conditions?.includes_audio === undefined || l.conditions?.includes_audio === null,
  )

  if (linesWithAudioTrue.length > 0 && linesWithAudioFalse.length === 0 && linesWithoutAudio.length === 0) {
    // Unambiguous: all video lines on this model require/include audio (e.g. Google Veo)
    return { audio: true, ambiguous: false }
  }

  if (linesWithAudioFalse.length > 0 && linesWithAudioTrue.length === 0 && linesWithoutAudio.length === 0) {
    // Unambiguous: all video lines on this model exclude audio
    return { audio: false, ambiguous: false }
  }

  if (linesWithAudioTrue.length > 0 && (linesWithAudioFalse.length > 0 || linesWithoutAudio.length > 0)) {
    // Ambiguous: some lines have audio condition and some do not, and caller didn't specify
    return { audio: undefined, ambiguous: true }
  }

  // No audio conditions on any video lines
  return { audio: undefined, ambiguous: false }
}

export interface ConditionMatchResult {
  matches: boolean
  exactResMatch: boolean
}

export function evaluateLineConditions(
  line: ParsedPricingLine,
  targetRes: string,
  audioContext: { audio: boolean | undefined; ambiguous: boolean },
  targetDur?: number,
): ConditionMatchResult {
  // Reject non-standard serving tiers marked on variant
  if (line.variant) {
    const v = line.variant.trim().toLowerCase()
    if (v === 'batch' || v === 'flex' || v === 'priority') {
      return { matches: false, exactResMatch: false }
    }
  }

  const cond = line.conditions

  if (cond) {
    // Reject malformed / unknown conditions
    for (const key of Object.keys(cond)) {
      if (!KNOWN_CONDITION_KEYS.has(key)) {
        const val = cond[key]
        if (val !== undefined && val !== null && val !== '') {
          return { matches: false, exactResMatch: false }
        }
      }
    }

    // Standard service tier condition check
    if (cond.service_tier !== undefined && cond.service_tier !== null) {
      const st = String(cond.service_tier).trim().toLowerCase()
      if (st !== '' && st !== 'standard' && st !== 'default') {
        return { matches: false, exactResMatch: false }
      }
    }

    // Tier condition check
    if (cond.tier !== undefined && cond.tier !== null) {
      const t = String(cond.tier).trim().toLowerCase()
      if (t !== '' && t !== 'standard' && t !== 'paid') {
        return { matches: false, exactResMatch: false }
      }
    }

    // Audio inclusion condition check
    if (cond.includes_audio !== undefined && cond.includes_audio !== null) {
      if (typeof cond.includes_audio === 'boolean') {
        if (audioContext.ambiguous) {
          // Line has an audio condition, but audio inclusion is ambiguous from settings & metadata
          return { matches: false, exactResMatch: false }
        }
        if (audioContext.audio !== undefined && cond.includes_audio !== audioContext.audio) {
          return { matches: false, exactResMatch: false }
        }
      }
    }

    // Duration condition check
    if (cond.duration_seconds !== undefined && cond.duration_seconds !== null) {
      const durNum =
        typeof cond.duration_seconds === 'number'
          ? cond.duration_seconds
          : parseFloat(String(cond.duration_seconds))
      if (Number.isFinite(durNum) && durNum > 0) {
        if (!targetDur || targetDur !== durNum) {
          return { matches: false, exactResMatch: false }
        }
      }
    }
  }

  // Resolution / Image size condition & Variant resolution check
  const lineRes = getLineResolution(line)
  if (lineRes) {
    if (!targetRes) {
      // Resolution condition specified on line, but no resolution selected/provided -> ambiguous
      return { matches: false, exactResMatch: false }
    }
    if (lineRes === targetRes) {
      return { matches: true, exactResMatch: true }
    }
    // Resolution mismatch: line requires different resolution
    return { matches: false, exactResMatch: false }
  }

  return { matches: true, exactResMatch: false }
}

function findMatchingLine(
  lines: ParsedPricingLine[],
  targetRes: string,
  audioContext: { audio: boolean | undefined; ambiguous: boolean },
  targetDur?: number,
): ParsedPricingLine | undefined {
  let matchedLine: ParsedPricingLine | undefined
  let matchedExact = false

  for (const line of lines) {
    const evalResult = evaluateLineConditions(line, targetRes, audioContext, targetDur)
    if (evalResult.matches) {
      if (evalResult.exactResMatch) {
        matchedLine = line
        matchedExact = true
        break
      }
      if (!matchedLine) {
        matchedLine = line
      }
    }
  }

  if (!matchedLine) {
    return undefined
  }

  // If any candidate line has a resolution constraint but none matched the requested resolution, do not fall back
  const anyLineHasRes = lines.some((l) => getLineResolution(l) !== undefined)
  if (anyLineHasRes && targetRes && !matchedExact) {
    return undefined
  }

  return matchedLine
}

/**
 * Calculates genuine pricing estimate from real model metadata lines.
 * Strictly uses catalog metadata without hardcoded fallback rates or assumptions.
 * Returns isAvailable: false when price is unknown, non-existent, token-based only,
 * or when required duration or resolution is missing/mismatched.
 */
export function calculateGenerationCost({
  modelOption,
  action,
  count,
  settings,
}: {
  modelOption: MediaCatalogModelOption | undefined
  action: MediaGenerationAction
  count: number
  settings: MediaGenerationSettings
}): CalculatedCostEstimate {
  const isVideoModel = typeof modelOption?.kind === 'string' && modelOption.kind.includes('video')

  const isVideoAction =
    action === 'to_video' ||
    action === 'next_scene' ||
    isVideoModel

  const effectiveCount = Math.max(1, count)

  const defaultUnavailable: CalculatedCostEstimate = {
    unitPrice: 0,
    quantity: effectiveCount,
    unitLabel: isVideoAction ? 'video' : 'image',
    totalPrice: 0,
    formattedPerUnit: 'Pricing unavailable',
    formattedTotal: 'Pricing unavailable',
    isAvailable: false,
    isVerified: false,
  }

  if (!modelOption || !modelOption.pricing) {
    return defaultUnavailable
  }

  const p = modelOption.pricing as Record<string, unknown>
  const billing = p.billing as Record<string, unknown> | undefined
  const isVerified = billing?.status === 'verified'
  const lines = extractBillingLines(modelOption.pricing)

  // 1. Billing lines matching
  if (lines.length > 0) {
    const targetRes = normalizeResKey(settings.resolution)

    if (!isVideoAction) {
      // Image lines must have unit 'image' and billable output (not input)
      const imageLines = lines.filter((l) => l.unit === 'image' && !l.billable.toLowerCase().includes('input'))
      if (imageLines.length === 0) {
        return defaultUnavailable
      }

      const audioContext = { audio: false, ambiguous: false }
      const matchedLine = findMatchingLine(imageLines, targetRes, audioContext)
      if (!matchedLine) {
        return defaultUnavailable
      }

      const unitPrice = matchedLine.priceUsd
      const total = unitPrice * effectiveCount
      const perUnitStr = unitPrice === 0 ? '$0.00/image' : `$${unitPrice.toFixed(3)}/image`
      const totalStr = total === 0 ? '$0.00' : `$${total.toFixed(2)}`
      const resLabel = getLineResolution(matchedLine)

      return {
        unitPrice,
        quantity: effectiveCount,
        unitLabel: effectiveCount === 1 ? 'image' : 'images',
        totalPrice: Number(total.toFixed(10)),
        formattedPerUnit: perUnitStr,
        formattedTotal: totalStr,
        isAvailable: true,
        isVerified,
        matchedLineDescription: resLabel ? `${resLabel} rate` : 'image rate',
      }
    } else {
      // Video lines: unit must be second, clip, or video and billable output (not input)
      const videoLines = lines.filter(
        (l) =>
          (l.unit === 'second' || l.unit === 'clip' || l.unit === 'video') &&
          !l.billable.toLowerCase().includes('input'),
      )
      if (videoLines.length === 0) {
        return defaultUnavailable
      }

      const duration = settings.durationSeconds
      const audioContext = resolveAudioContext(settings.includesAudio, modelOption, videoLines)
      const matchedLine = findMatchingLine(videoLines, targetRes, audioContext, duration)
      if (!matchedLine) {
        return defaultUnavailable
      }

      if (matchedLine.unit === 'second') {
        // Per-second video rate requires valid positive duration; do NOT assume 8 seconds!
        if (!duration || duration <= 0) {
          return defaultUnavailable
        }

        const unitPrice = matchedLine.priceUsd
        const perClipCost = unitPrice * duration
        const total = perClipCost * effectiveCount
        const perUnitStr =
          unitPrice === 0
            ? '$0.00/sec ($0.00/clip)'
            : `$${unitPrice.toFixed(3)}/sec ($${perClipCost.toFixed(2)}/clip)`
        const totalStr = total === 0 ? '$0.00' : `$${total.toFixed(2)}`

        return {
          unitPrice,
          quantity: effectiveCount,
          unitLabel: `${duration}s clip`,
          totalPrice: Number(total.toFixed(10)),
          formattedPerUnit: perUnitStr,
          formattedTotal: totalStr,
          isAvailable: true,
          isVerified,
          matchedLineDescription: `${duration}s at $${unitPrice.toFixed(3)}/s`,
        }
      } else {
        // Fixed clip rate (unit 'clip' or 'video')
        const unitPrice = matchedLine.priceUsd
        const total = unitPrice * effectiveCount
        const perUnitStr = unitPrice === 0 ? '$0.00/clip' : `$${unitPrice.toFixed(2)}/clip`
        const totalStr = total === 0 ? '$0.00' : `$${total.toFixed(2)}`

        return {
          unitPrice,
          quantity: effectiveCount,
          unitLabel: effectiveCount === 1 ? 'clip' : 'clips',
          totalPrice: Number(total.toFixed(10)),
          formattedPerUnit: perUnitStr,
          formattedTotal: totalStr,
          isAvailable: true,
          isVerified,
          matchedLineDescription: matchedLine.variant ? `${matchedLine.variant} rate` : 'per clip rate',
        }
      }
    }
  }

  // 2. Direct fixed rate properties fallback (if billing lines are absent)
  if (!isVideoAction) {
    const rawDirect = p.per_image ?? p.image
    const directPerImage =
      typeof rawDirect === 'number'
        ? rawDirect
        : typeof rawDirect === 'string'
          ? parseFloat(rawDirect)
          : NaN
    if (Number.isFinite(directPerImage) && directPerImage >= 0) {
      const total = directPerImage * effectiveCount
      const perUnitStr = directPerImage === 0 ? '$0.00/image' : `$${directPerImage.toFixed(3)}/image`
      const totalStr = total === 0 ? '$0.00' : `$${total.toFixed(2)}`
      return {
        unitPrice: directPerImage,
        quantity: effectiveCount,
        unitLabel: effectiveCount === 1 ? 'image' : 'images',
        totalPrice: Number(total.toFixed(10)),
        formattedPerUnit: perUnitStr,
        formattedTotal: totalStr,
        isAvailable: true,
        isVerified,
        matchedLineDescription: 'per image rate',
      }
    }
  } else {
    const rawDirect = p.per_video ?? p.video_output
    const directPerVideo =
      typeof rawDirect === 'number'
        ? rawDirect
        : typeof rawDirect === 'string'
          ? parseFloat(rawDirect)
          : NaN
    if (Number.isFinite(directPerVideo) && directPerVideo >= 0) {
      const total = directPerVideo * effectiveCount
      const perUnitStr = directPerVideo === 0 ? '$0.00/clip' : `$${directPerVideo.toFixed(2)}/clip`
      const totalStr = total === 0 ? '$0.00' : `$${total.toFixed(2)}`
      return {
        unitPrice: directPerVideo,
        quantity: effectiveCount,
        unitLabel: effectiveCount === 1 ? 'clip' : 'clips',
        totalPrice: Number(total.toFixed(10)),
        formattedPerUnit: perUnitStr,
        formattedTotal: totalStr,
        isAvailable: true,
        isVerified,
        matchedLineDescription: 'fixed clip rate',
      }
    }
  }

  return defaultUnavailable
}

/**
 * Resolves an initial option from supported options metadata.
 * Only preselects source ratio/resolution/duration if in supported options
 * (case normalized but returning actual metadata spelling).
 * Falls back to model default if supported, or undefined when metadata is absent.
 */
export function resolveInitialSetting<T extends string | number>(
  sourceValue: unknown,
  supportedValues: readonly T[] | undefined,
  defaultValue: T | undefined,
  options?: { preserveUnsupported?: boolean },
): T | undefined {
  if (options?.preserveUnsupported && sourceValue !== undefined && sourceValue !== null && sourceValue !== '') {
    if (typeof sourceValue === 'string') {
      const trimmed = sourceValue.trim()
      if (trimmed) return trimmed as T
    } else if (typeof sourceValue === 'number' && Number.isFinite(sourceValue)) {
      return sourceValue as T
    }
  }

  if (!supportedValues || supportedValues.length === 0) {
    if (options?.preserveUnsupported && sourceValue !== undefined && sourceValue !== null && sourceValue !== '') {
      return sourceValue as T
    }
    return undefined
  }

  // 1. Try to match sourceValue in supportedValues
  if (sourceValue !== undefined && sourceValue !== null && sourceValue !== '') {
    if (typeof sourceValue === 'string') {
      const normSource = normalizeResKey(sourceValue)
      const matched = supportedValues.find((v) => {
        if (typeof v === 'string') {
          return (
            normalizeResKey(v) === normSource ||
            v.toLowerCase().trim() === sourceValue.toLowerCase().trim()
          )
        }
        return false
      })
      if (matched !== undefined) return matched
    } else if (typeof sourceValue === 'number' && Number.isFinite(sourceValue)) {
      const matched = supportedValues.find((v) => typeof v === 'number' && v === sourceValue)
      if (matched !== undefined) return matched
    }
  }

  // 2. Try defaultValue if supported
  if (defaultValue !== undefined && defaultValue !== null && defaultValue !== '') {
    if (typeof defaultValue === 'string') {
      const normDef = normalizeResKey(defaultValue)
      const matched = supportedValues.find((v) => {
        if (typeof v === 'string') {
          return (
            normalizeResKey(v) === normDef ||
            v.toLowerCase().trim() === defaultValue.toLowerCase().trim()
          )
        }
        return false
      })
      if (matched !== undefined) return matched
    } else if (typeof defaultValue === 'number' && Number.isFinite(defaultValue)) {
      const matched = supportedValues.find((v) => typeof v === 'number' && v === defaultValue)
      if (matched !== undefined) return matched
    }
  }

  return undefined
}

/**
 * Preselects source model if available in availableModels, else falls back to default.
 */
export function resolveInitialModel(
  sourceModel: string | undefined,
  availableModels: readonly MediaCatalogModelOption[] | undefined,
  defaultModel: string | undefined,
  options?: { preserveUnavailable?: boolean },
): string {
  if (options?.preserveUnavailable && sourceModel && sourceModel.trim()) {
    return sourceModel.trim()
  }

  if (!availableModels || availableModels.length === 0) {
    return sourceModel?.trim() || defaultModel || ''
  }

  if (sourceModel) {
    const trimmed = sourceModel.trim().toLowerCase()
    const matched = availableModels.find(
      (m) => m.id.toLowerCase() === trimmed || m.model.toLowerCase() === trimmed,
    )
    if (matched) {
      return matched.id
    }
  }

  if (defaultModel) {
    const matchedDefault = availableModels.find((m) => m.id === defaultModel)
    if (matchedDefault) {
      return matchedDefault.id
    }
  }

  const firstReady = availableModels.find((m) => m.ready)
  if (firstReady) return firstReady.id

  return availableModels[0]?.id || ''
}

export function resolveVideoContinuationModel(
  sourceModel: string | undefined,
  allVideoModels: readonly MediaCatalogModelOption[] | undefined,
  sourceProvider?: string,
): { modelId: string; modelOption?: MediaCatalogModelOption; isUnknown: boolean } {
  if (!sourceModel || !sourceModel.trim()) {
    return { modelId: '', isUnknown: true }
  }
  const trimmed = sourceModel.trim()
  const lower = trimmed.toLowerCase()
  const pNorm = (sourceProvider || '').trim().toLowerCase()

  // First check if trimmed already carries a provider prefix (e.g. google:veo-3.1...)
  let explicitProvider = pNorm
  let modelBare = lower
  if (lower.includes(':')) {
    const parts = lower.split(':')
    if (pNorm && pNorm !== parts[0]) return { modelId: trimmed, isUnknown: false }
    explicitProvider = parts[0]
    modelBare = parts.slice(1).join(':')
  }

  // 1. If provider is known (or inferred from prefix), match option requiring exact provider match
  if (explicitProvider) {
    const exactMatch = allVideoModels?.find((m) => {
      const optProvider = (m.provider || '').toLowerCase().trim()
      if (optProvider !== explicitProvider) return false
      const optId = m.id.toLowerCase().trim()
      const optModel = m.model.toLowerCase().trim()
      return optId === lower || optModel === lower || optId === modelBare || optModel === modelBare
    })
    if (exactMatch) {
      return {
        modelId: exactMatch.id,
        modelOption: exactMatch,
        isUnknown: false,
      }
    }
  }

  if (explicitProvider) return { modelId: trimmed, isUnknown: false }

  // 2. If no provider was known, check if multiple options from different providers share the bare model name
  const candidateMatches = allVideoModels?.filter(
    (m) => m.id.toLowerCase() === lower || m.model.toLowerCase() === lower,
  ) ?? []

  if (candidateMatches.length === 1) {
    return {
      modelId: candidateMatches[0].id,
      modelOption: candidateMatches[0],
      isUnknown: false,
    }
  }

  // If candidateMatches has >1 providers or is empty, bare model is ambiguous or uncataloged;
  // do NOT silently resolve to the first OpenRouter or direct counterpart.
  return {
    modelId: trimmed,
    modelOption: undefined,
    isUnknown: false,
  }
}

export interface VideoActionSupportResult {
  supported: boolean
  reason?: string
  lockedOptions?: {
    durationSeconds?: number
    resolution?: string
    aspectRatio?: string
    supportsDuration?: boolean
    explanation?: string
  }
}

export function evaluateVideoActionSupport(
  action: 'fine_tune' | 'iterate' | 'to_video' | 'next_scene',
  item: Pick<MediaLibraryItem, 'kind' | 'model' | 'aspectRatio' | 'resolution' | 'durationSeconds' | 'videoProvenance'>,
  modelOption?: MediaCatalogModelOption,
  options?: { nowMs?: number },
): VideoActionSupportResult {
  if (item.kind !== 'video' && action !== 'to_video') {
    return { supported: true }
  }

  if (action === 'to_video') {
    const initImg = modelOption?.generation_options?.initial_image
    const initConstraint = modelOption?.constraints?.create?.initial_image_supported
    if (!modelOption?.constraints?.create?.supported || (initConstraint !== true && initImg?.supported !== true)) {
      return { supported: false, reason: 'Selected video model does not support initial image input.' }
    }
    return { supported: true }
  }

  // Video operations (fine_tune, iterate, next_scene) require constraints from modelOption
  const constraints = modelOption?.constraints
  if (!constraints) {
    return {
      supported: false,
      reason: 'Model operation constraints unavailable; action cannot be validated.',
    }
  }

  const prov = item.videoProvenance
  const now = options?.nowMs ?? Date.now()

  if (!modelOption?.ready) return { supported: false, reason: 'Source model is unavailable.' }
  if (!prov || !prov.model?.trim() || !prov.provider?.trim() || !prov.transport?.trim()) {
    return { supported: false, reason: 'Verified source provenance is incomplete.' }
  }
  const sourceMatch = resolveVideoContinuationModel(prov.model, [modelOption], prov.provider)
  if (!sourceMatch.modelOption) return { supported: false, reason: 'Source and action model must match.' }
  if (!Number.isFinite(prov.observed_width) || !Number.isFinite(prov.observed_height) || (prov.observed_width ?? 0) <= 0 || (prov.observed_height ?? 0) <= 0) {
    return { supported: false, reason: 'Observed source dimensions are required.' }
  }

  // Verify timestamps and expiry on provenance if present
  if (prov) {
    if (prov.expires_at && now > prov.expires_at) {
      return { supported: false, reason: 'Video source reference has expired (exceeds provider validity period).' }
    }
    if (prov.created_at && now < prov.created_at - 60000) {
      return { supported: false, reason: 'Video source creation timestamp is in the future.' }
    }
  }

  // Observed duration from playback source or exact provenance, rejecting NaN / negative / non-finite
  const rawObservedMs = prov?.observed_duration_ms
  const observedDurSec = typeof rawObservedMs === 'number' && Number.isFinite(rawObservedMs) && rawObservedMs > 0
    ? rawObservedMs / 1000
    : 0

  if (!Number.isFinite(observedDurSec) || observedDurSec <= 0) {
    return { supported: false, reason: 'Source video duration is invalid or not finite.' }
  }

  if (action === 'fine_tune' || action === 'iterate') {
    const editC = constraints.edit
    if (!editC || !editC.supported) {
      return {
        supported: false,
        reason: editC?.reason || 'Selected model does not support video editing; select an iteration model.',
      }
    }

    if (editC.supported_providers && editC.supported_providers.length > 0 && modelOption?.provider) {
      const pNorm = modelOption.provider.toLowerCase().trim()
      if (!editC.supported_providers.map((p) => p.toLowerCase().trim()).includes(pNorm)) {
        return {
          supported: false,
          reason: `Video editing is not supported on provider "${modelOption.provider}".`,
        }
      }
    }

    if (editC.required_source_provider && prov?.provider) {
      if (prov.provider.toLowerCase().trim() !== editC.required_source_provider.toLowerCase().trim()) {
        return {
          supported: false,
          reason: `Source provider "${prov.provider}" does not match required provider "${editC.required_source_provider}".`,
        }
      }
    }

    if (editC.required_source_transport && prov?.transport) {
      if (prov.transport.toLowerCase().trim() !== editC.required_source_transport.toLowerCase().trim()) {
        return {
          supported: false,
          reason: `Source transport "${prov.transport}" does not match required transport "${editC.required_source_transport}".`,
        }
      }
    }

    const hasHandle = prov.has_interaction === true
    if (!hasHandle) return { supported: false, reason: 'Native fine-tuning requires the saved interaction handle.' }

    if (editC.requires_interaction_handle && !hasHandle) {
      return {
        supported: false,
        reason: 'Video interaction continuation requires interaction handle.',
      }
    }

    if (hasHandle) {
      if (editC.requires_handle_match && editC.source_model_match && prov?.model) {
        if (prov.model.toLowerCase().trim() !== editC.source_model_match.toLowerCase().trim()) {
          return {
            supported: false,
            reason: `Video interaction model mismatch: source was created with "${prov.model}", cannot continue with "${editC.source_model_match}".`,
          }
        }
      }
    } else {
      const maxExt = editC.max_external_duration_sec ?? editC.max_source_duration_sec ?? 10.0
      if (observedDurSec > maxExt) {
        return {
          supported: false,
          reason: `Source video duration exceeds maximum allowed for external video editing (${maxExt}s).`,
        }
      }
    }

    return {
      supported: true,
      lockedOptions: {
        supportsDuration: editC.supports_duration ?? false,
        explanation: 'Duration is managed automatically by the iteration model.',
      },
    }
  }

  if (action === 'next_scene') {
    const extC = constraints.extend
    if (!extC || !extC.supported) {
      return {
        supported: false,
        reason: extC?.reason || 'This model does not support video extension.',
      }
    }

    if (extC.requires_source_provenance && !prov) {
      return {
        supported: false,
        reason: 'Next scene extension requires verified source provenance.',
      }
    }

    if (prov) {
      if (extC.max_reference_age_ms && extC.max_reference_age_ms > 0) {
        if (!Number.isFinite(prov.created_at) || prov.created_at <= 0 || !Number.isFinite(prov.expires_at) || (prov.expires_at ?? 0) <= 0) {
          return { supported: false, reason: 'Source reference creation and expiry timestamps are required.' }
        }
        if ((now - prov.created_at) > extC.max_reference_age_ms) {
          return {
            supported: false,
            reason: `Video source reference has expired (exceeds ${Math.round(extC.max_reference_age_ms / (3600 * 1000))}-hour validity period).`,
          }
        }
      }

      if (extC.requires_known_extension_count && !prov.extension_count_known) {
        return {
          supported: false,
          reason: 'Video extension requires known extension count in source provenance.',
        }
      }

      if (prov.extension_count !== undefined && (!Number.isInteger(prov.extension_count) || prov.extension_count < 0)) {
        return { supported: false, reason: 'Source extension count is invalid.' }
      }
      if (typeof extC.max_extension_count === 'number' && typeof prov.extension_count === 'number') {
        if (prov.extension_count >= extC.max_extension_count) {
          return {
            supported: false,
            reason: `Video extension limit reached (${extC.max_extension_count} extensions maximum).`,
          }
        }
      }

      if (extC.required_source_provider && prov.provider) {
        if (prov.provider.toLowerCase().trim() !== extC.required_source_provider.toLowerCase().trim()) {
          return {
            supported: false,
            reason: `Extension requires a source generated by ${extC.required_source_provider}; source was ${prov.provider}.`,
          }
        }
      }

      if (extC.required_source_transport && prov.transport) {
        if (prov.transport.toLowerCase().trim() !== extC.required_source_transport.toLowerCase().trim()) {
          return {
            supported: false,
            reason: `Source requires ${extC.required_source_transport} transport; got "${prov.transport}".`,
          }
        }
      }

      if (extC.requires_provider_resource) {
        const hasRes = prov.has_provider_resource === true
        if (!hasRes) {
          return {
            supported: false,
            reason: 'Video source requires valid provider resource URI.',
          }
        }
      }

      if (extC.requires_interaction_handle) {
        const hasHandle = prov.has_interaction === true
        if (!hasHandle) {
          return {
            supported: false,
            reason: 'Extension source provenance is missing interaction handle; cannot extend.',
          }
        }
      }

      if (extC.requires_output_digest) {
        if (!prov.output_digest_sha256 || !prov.output_digest_sha256.trim()) {
          return {
            supported: false,
            reason: 'Video source requires non-empty output digest.',
          }
        }
      }

      const sourceModelNorm = prov.model.toLowerCase().trim()

      if (extC.disallowed_source_models && extC.disallowed_source_models.length > 0) {
        if (extC.disallowed_source_models.some((m) => sourceModelNorm.includes(m.toLowerCase().trim()))) {
          return {
            supported: false,
            reason: `Extension cannot extend videos generated by ${prov.model}.`,
          }
        }
      }

      if (extC.allowed_source_models && extC.allowed_source_models.length > 0) {
        const allowed = extC.allowed_source_models.map((m) => m.toLowerCase().trim())
        if (!allowed.some((m) => sourceModelNorm === m || sourceModelNorm.endsWith(`/${m}`) || sourceModelNorm.endsWith(`:${m}`))) {
          return {
            supported: false,
            reason: `Extension requires source model in [${extC.allowed_source_models.join(', ')}]; source was "${prov.model}".`,
          }
        }
      }

      if (extC.observed_dimension_pairs && extC.observed_dimension_pairs.length > 0) {
        const width = prov.observed_width ?? 0
        const height = prov.observed_height ?? 0
        const matchesPair = extC.observed_dimension_pairs.some(
          ([w, h]) => width === w && height === h
        )
        if (!matchesPair) {
          return {
            supported: false,
            reason: `Source requires observed dimensions matching [${extC.observed_dimension_pairs.map(([w, h]) => `${w}x${h}`).join(' or ')}]; got ${width}x${height}.`,
          }
        }
      }

      if (typeof extC.max_source_duration_sec === 'number' && observedDurSec > extC.max_source_duration_sec) {
        return {
          supported: false,
          reason: `Source video duration (${observedDurSec.toFixed(1)}s) exceeds maximum allowed for extension (${extC.max_source_duration_sec}s).`,
        }
      }

      // max_source_duration_sec is the server's authoritative input limit.
      // Request duration is not the appended playback length (overlap/provider-managed output).
    }

    const targetAR = extC.locked_aspect_ratio_matches_source
      ? extC.supported_aspect_ratios?.find((ratio) => {
          const [w, h] = ratio.split(':').map(Number)
          return w > 0 && h > 0 && (prov.observed_width ?? 0) * h === (prov.observed_height ?? 0) * w
        })
      : undefined
    if (extC.locked_aspect_ratio_matches_source && !targetAR) {
      return { supported: false, reason: 'Source dimensions do not match a supported aspect ratio.' }
    }

    return {
      supported: true,
      lockedOptions: {
        durationSeconds: extC.locked_duration_seconds !== undefined && extC.locked_duration_seconds > 0 ? extC.locked_duration_seconds : undefined,
        resolution: extC.locked_resolution || undefined,
        aspectRatio: targetAR,
        supportsDuration: extC.supports_duration ?? false,
        explanation: extC.locked_duration_seconds
          ? `Video extension is fixed at ${extC.locked_duration_seconds}s duration, ${extC.locked_resolution || 'source'} resolution, matching source aspect ratio.`
          : 'Extension duration is managed automatically.',
      },
    }
  }

  return { supported: true }
}

export function getSupportedDurationsForResolution(
  modelOption: MediaCatalogModelOption | undefined,
  resolution: string | undefined,
): number[] {
  if (!modelOption?.generation_options) return []
  const { resolution_durations, durations } = modelOption.generation_options
  if (resolution_durations && resolution) {
    const matchKey = Object.keys(resolution_durations).find(
      (k) => k.toLowerCase() === resolution.toLowerCase(),
    )
    if (matchKey && resolution_durations[matchKey]?.length > 0) {
      return resolution_durations[matchKey]
    }
  }
  return durations || []
}

export function extractGenerationDurationSeconds(
  item: Pick<MediaLibraryItem, 'durationSeconds' | 'videoProvenance'> | undefined,
): number | undefined {
  if (!item) return undefined
  if (typeof item.durationSeconds === 'number' && item.durationSeconds > 0) {
    return item.durationSeconds
  }
  const prov = item.videoProvenance
  if (typeof prov?.duration_seconds === 'number' && prov.duration_seconds > 0) {
    return prov.duration_seconds
  }
  return undefined
}

export interface MediaGenerationValidationResult {
  valid: boolean
  error?: string
}

export function validateMediaGenerationRequest(options: {
  action: MediaGenerationAction
  item: MediaLibraryItem
  model: string
  modelOption?: MediaCatalogModelOption
  prompt: string
  settings: MediaGenerationSettings
  variantCount?: number
  nowMs?: number
}): MediaGenerationValidationResult {
  const { action, item, model, modelOption, prompt, settings, variantCount, nowMs } = options

  if (!prompt || !prompt.trim()) {
    return { valid: false, error: 'Please enter prompt instructions to proceed.' }
  }

  if (!model || !model.trim()) {
    return { valid: false, error: 'A valid AI model must be selected.' }
  }

  if (!modelOption) {
    return { valid: false, error: 'Selected model option is not found in the media catalog.' }
  }

  if (!modelOption.ready) {
    return { valid: false, error: `Selected model "${modelOption.display_name || modelOption.model}" is unavailable.` }
  }

  // Exact model match: model identifier must match modelOption id or model
  const trimmedModel = model.trim().toLowerCase()
  const optId = modelOption.id.toLowerCase().trim()
  const optModel = modelOption.model.toLowerCase().trim()
  const optQualified = modelOption.provider ? `${modelOption.provider.toLowerCase().trim()}:${optModel}` : ''
  const isExactModelMatch = trimmedModel === optId || trimmedModel === optModel || (optQualified && trimmedModel === optQualified)
  if (!isExactModelMatch) {
    return { valid: false, error: `Selected model identifier "${model}" does not match catalog option "${modelOption.id}".` }
  }

  if ((item.kind !== 'image' && item.kind !== 'video') || (action === 'to_video' && item.kind !== 'image') || (action === 'next_scene' && item.kind !== 'video')) {
    return { valid: false, error: 'Action is incompatible with this source media.' }
  }
  const genOptions = modelOption.generation_options
  if (!genOptions) return { valid: false, error: 'Model option metadata is unavailable.' }
  for (const [label, value, allowed] of [
    ['Aspect ratio', settings.aspectRatio, genOptions.aspect_ratios],
    ['Resolution', settings.resolution, genOptions.resolutions],
  ] as const) {
    if (allowed?.length && (!value || !allowed.some((v) => v.toLowerCase() === value.toLowerCase()))) {
      return { valid: false, error: `${label} must be selected from supported options.` }
    }
    if (value && !allowed?.length) return { valid: false, error: `${label} metadata is unavailable.` }
  }
  if (action === 'to_video') {
    const allowed = getSupportedDurationsForResolution(modelOption, settings.resolution)
    if (modelOption.constraints?.create.supports_duration) {
      if (!settings.durationSeconds || !allowed.includes(settings.durationSeconds)) return { valid: false, error: 'Select a supported duration for this resolution.' }
    } else if (settings.durationSeconds !== undefined) return { valid: false, error: 'Duration is managed automatically by this model.' }
  }

  // Validate variant count
  if (variantCount !== undefined) {
    if (!Number.isFinite(variantCount) || variantCount <= 0 || !Number.isInteger(variantCount)) {
      return { valid: false, error: 'Variant count must be a positive integer.' }
    }
    const maxCount = item.kind === 'video' || action === 'to_video' ? 8 : 50
    if (variantCount > maxCount) {
      return { valid: false, error: `Variant count exceeds maximum allowed (${maxCount}).` }
    }
  }

  // Validate durationSeconds numeric integrity if present
  if (settings.durationSeconds !== undefined) {
    if (!Number.isFinite(settings.durationSeconds) || settings.durationSeconds <= 0 || !Number.isInteger(settings.durationSeconds)) {
      return { valid: false, error: 'Duration seconds must be a positive integer.' }
    }
  }

  if (item.kind === 'video') {
    const support = evaluateVideoActionSupport(action, item, modelOption, { nowMs })
    if (!support.supported) {
      return { valid: false, error: support.reason || 'This action is not supported for the selected video.' }
    }

    const locks = support.lockedOptions
    const extC = modelOption.constraints?.extend
    const editC = modelOption.constraints?.edit

    if (action === 'next_scene') {
      // Must enforce locked duration
      if (locks?.durationSeconds !== undefined) {
        if (settings.durationSeconds !== locks.durationSeconds) {
          return { valid: false, error: `Video extension only supports ${locks.durationSeconds}s duration.` }
        }
      } else if (locks?.supportsDuration === false) {
        if (settings.durationSeconds !== undefined && settings.durationSeconds > 0) {
          return { valid: false, error: 'Duration selection is not accepted for this video extension model.' }
        }
      }

      // Must enforce locked resolution
      if (locks?.resolution) {
        if (settings.resolution?.toLowerCase() !== locks.resolution.toLowerCase()) {
          return { valid: false, error: `Video extension requires ${locks.resolution} resolution.` }
        }
      }

      // Must enforce locked aspect ratio
      if (locks?.aspectRatio) {
        if (settings.aspectRatio !== locks.aspectRatio) {
          return { valid: false, error: `Video extension requires matching source aspect ratio (${locks.aspectRatio}).` }
        }
      }

      if (extC?.supported_aspect_ratios && extC.supported_aspect_ratios.length > 0) {
        if (settings.aspectRatio && !extC.supported_aspect_ratios.includes(settings.aspectRatio)) {
          return { valid: false, error: `Video extension requires aspect ratio in [${extC.supported_aspect_ratios.join(', ')}].` }
        }
      }
    } else if (action === 'fine_tune' || action === 'iterate') {
      if (locks?.supportsDuration === false || editC?.supports_duration === false) {
        if (settings.durationSeconds !== undefined && settings.durationSeconds > 0) {
          return { valid: false, error: 'Video fine-tuning does not accept duration selection.' }
        }
      }
    }
  } else if (action === 'to_video') {
    const support = evaluateVideoActionSupport(action, item, modelOption, { nowMs })
    if (!support.supported) {
      return { valid: false, error: support.reason || 'Selected video model does not support image-to-video.' }
    }

    const genOpts = modelOption.generation_options
    if (genOpts?.aspect_ratios && genOpts.aspect_ratios.length > 0 && settings.aspectRatio) {
      if (!genOpts.aspect_ratios.includes(settings.aspectRatio)) {
        return { valid: false, error: `Aspect ratio "${settings.aspectRatio}" is not supported by selected model.` }
      }
    }

    if (genOpts?.resolutions && genOpts.resolutions.length > 0 && settings.resolution) {
      const normRes = settings.resolution.toLowerCase()
      if (!genOpts.resolutions.map((r) => r.toLowerCase()).includes(normRes)) {
        return { valid: false, error: `Resolution "${settings.resolution}" is not supported by selected model.` }
      }
    }

    if (modelOption.generation_options?.resolution_durations && settings.resolution) {
      const allowed = getSupportedDurationsForResolution(modelOption, settings.resolution)
      if (allowed.length > 0 && settings.durationSeconds && !allowed.includes(settings.durationSeconds)) {
        return { valid: false, error: `Duration ${settings.durationSeconds}s is not supported for ${settings.resolution} resolution.` }
      }
    } else if (genOpts?.durations && genOpts.durations.length > 0 && settings.durationSeconds) {
      if (!genOpts.durations.includes(settings.durationSeconds)) {
        return { valid: false, error: `Duration ${settings.durationSeconds}s is not supported by selected model.` }
      }
    }
  } else {
    // Image editing / iteration
    const genOpts = modelOption.generation_options
    if (genOpts?.aspect_ratios && genOpts.aspect_ratios.length > 0 && settings.aspectRatio) {
      if (!genOpts.aspect_ratios.includes(settings.aspectRatio)) {
        return { valid: false, error: `Aspect ratio "${settings.aspectRatio}" is not supported by selected model.` }
      }
    }

    if (genOpts?.resolutions && genOpts.resolutions.length > 0 && settings.resolution) {
      const normRes = settings.resolution.toLowerCase()
      if (!genOpts.resolutions.map((r) => r.toLowerCase()).includes(normRes)) {
        return { valid: false, error: `Resolution "${settings.resolution}" is not supported by selected model.` }
      }
    }
  }

  return { valid: true }
}
