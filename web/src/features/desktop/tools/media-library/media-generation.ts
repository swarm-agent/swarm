import type { MediaCatalogModelOption } from '../../settings/media/queries/get-media-settings'
import type { MediaLibraryItem } from './types'

export interface MediaGenerationSettings {
  aspectRatio?: string
  resolution?: string
  durationSeconds?: number
}

export type MediaGenerationAction = 'fine_tune' | 'iterate' | 'to_video' | 'next_scene'

export interface MediaGenerationRequest {
  item: MediaLibraryItem
  action: MediaGenerationAction
  deltaPrompt: string
  variantCount: number
  model: string
  settings: MediaGenerationSettings
}

export interface MediaGenerationJob {
  id: string
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
  billable: string
  unit: string
  priceUsd: number
  quantity: number
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
const KNOWN_CONDITION_KEYS = new Set(['resolution', 'image_size', 'tier', 'service_tier', 'includes_audio'])

export function normalizeResKey(raw: string | undefined): string {
  if (!raw) return ''
  const c = raw.toLowerCase().trim()
  if (c === '1k' || c === '1024x1024' || c === '1024×1024' || c === 'standard' || c === '1024') return '1k'
  if (c === '2k' || c === '2048x2048' || c === '2048×2048' || c === 'hd' || c === '2048') return '2k'
  if (c === '4k' || c === '4096x4096' || c === '4096×4096' || c === 'ultra_hd' || c === 'uhd' || c === '4096') return '4k'
  if (c === '720p' || c === '1280x720') return '720p'
  if (c === '1080p' || c === '1920x1080') return '1080p'
  return c
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

    const billable = typeof line.billable === 'string' ? line.billable : ''
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
      billable,
      unit: unitRaw,
      priceUsd: effectivePriceUsd,
      quantity,
      conditions: line.conditions as PricingLineCondition | undefined,
    })
  }
  return results
}

interface ConditionMatchResult {
  matches: boolean
  exactResMatch: boolean
}

function evaluateLineConditions(
  line: ParsedPricingLine,
  targetRes: string,
): ConditionMatchResult {
  const cond = line.conditions
  if (!cond) {
    return { matches: true, exactResMatch: false }
  }

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
  if (cond.includes_audio === true) {
    return { matches: false, exactResMatch: false }
  }

  // Resolution / Image size condition check
  const rawLineRes = cond.resolution || cond.image_size
  if (rawLineRes !== undefined && rawLineRes !== null && String(rawLineRes).trim() !== '') {
    const lineRes = normalizeResKey(String(rawLineRes))
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
  const isVideoAction =
    action === 'to_video' ||
    action === 'next_scene' ||
    (action === 'fine_tune' && modelOption?.kind === 'video_generation')

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
      // Image lines must have unit 'image' (allowlist strictly checked in extractBillingLines)
      const imageLines = lines.filter((l) => l.unit === 'image')
      if (imageLines.length === 0) {
        return defaultUnavailable
      }

      let matchedLine: ParsedPricingLine | undefined
      let matchedExact = false

      for (const line of imageLines) {
        const evalResult = evaluateLineConditions(line, targetRes)
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

      // If target resolution was requested but no exact or general line matched, it is a mismatch
      if (!matchedLine) {
        return defaultUnavailable
      }

      // If lines have resolution conditions but none matched the requested resolution, do not arbitrarily fall back
      const anyLineHasRes = imageLines.some(
        (l) => l.conditions?.resolution || l.conditions?.image_size,
      )
      if (anyLineHasRes && targetRes && !matchedExact) {
        return defaultUnavailable
      }

      const unitPrice = matchedLine.priceUsd
      const total = unitPrice * effectiveCount
      const perUnitStr = unitPrice === 0 ? '$0.00/image' : `$${unitPrice.toFixed(3)}/image`
      const totalStr = total === 0 ? '$0.00' : `$${total.toFixed(2)}`

      return {
        unitPrice,
        quantity: effectiveCount,
        unitLabel: effectiveCount === 1 ? 'image' : 'images',
        totalPrice: total,
        formattedPerUnit: perUnitStr,
        formattedTotal: totalStr,
        isAvailable: true,
        isVerified,
        matchedLineDescription: matchedLine.conditions?.resolution
          ? `${matchedLine.conditions.resolution} rate`
          : 'image rate',
      }
    } else {
      // Video lines: unit must be second, clip, or video
      const videoLines = lines.filter(
        (l) => l.unit === 'second' || l.unit === 'clip' || l.unit === 'video',
      )
      if (videoLines.length === 0) {
        return defaultUnavailable
      }

      let matchedLine: ParsedPricingLine | undefined
      let matchedExact = false

      for (const line of videoLines) {
        const evalResult = evaluateLineConditions(line, targetRes)
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
        return defaultUnavailable
      }

      const anyLineHasRes = videoLines.some((l) => l.conditions?.resolution)
      if (anyLineHasRes && targetRes && !matchedExact) {
        return defaultUnavailable
      }

      if (matchedLine.unit === 'second') {
        // Per-second video rate requires valid positive duration; do NOT assume 8 seconds!
        const duration = settings.durationSeconds
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
          totalPrice: total,
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
          totalPrice: total,
          formattedPerUnit: perUnitStr,
          formattedTotal: totalStr,
          isAvailable: true,
          isVerified,
          matchedLineDescription: 'per clip rate',
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
        totalPrice: total,
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
        totalPrice: total,
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
