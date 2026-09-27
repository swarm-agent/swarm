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
): T | undefined {
  if (!supportedValues || supportedValues.length === 0) {
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
): string {
  if (!availableModels || availableModels.length === 0) {
    return defaultModel || ''
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
