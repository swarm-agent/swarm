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
  kind?: string
  variant?: string
  conditions?: PricingLineCondition
  [key: string]: unknown
}

export interface ParsedPricingLine {
  billable: string
  unit: string
  priceUsd: number
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
    const billable = typeof line.billable === 'string' ? line.billable : ''
    const unit = typeof line.unit === 'string' ? line.unit : ''
    const priceRaw = line.price_usd
    const priceUsd = typeof priceRaw === 'number' ? priceRaw : typeof priceRaw === 'string' ? parseFloat(priceRaw) : NaN
    if (!isNaN(priceUsd) && priceUsd >= 0) {
      results.push({
        billable,
        unit,
        priceUsd,
        conditions: line.conditions as PricingLineCondition | undefined,
      })
    }
  }
  return results
}

/**
 * Calculates genuine pricing estimate from real model metadata lines.
 * Strictly uses catalog metadata without hardcoded fallback rates.
 * Returns isAvailable: false when price is unknown, non-existent, or token-based only.
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
  const isVideoAction = action === 'to_video' || action === 'next_scene' || (action === 'fine_tune' && modelOption?.kind === 'video_generation')
  const defaultUnavailable: CalculatedCostEstimate = {
    unitPrice: 0,
    quantity: count,
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

  // 1. Direct fixed properties if provided (per_image, per_video, image, video_output)
  if (!isVideoAction) {
    const directPerImage = typeof p.per_image === 'number' ? p.per_image : typeof p.image === 'number' ? p.image : undefined
    if (typeof directPerImage === 'number' && directPerImage > 0) {
      const total = directPerImage * count
      return {
        unitPrice: directPerImage,
        quantity: count,
        unitLabel: count === 1 ? 'image' : 'images',
        totalPrice: total,
        formattedPerUnit: `$${directPerImage.toFixed(3)}/image`,
        formattedTotal: `$${total.toFixed(2)}`,
        isAvailable: true,
        isVerified,
        matchedLineDescription: 'per image rate',
      }
    }
  } else {
    const directPerVideo = typeof p.per_video === 'number' ? p.per_video : typeof p.video_output === 'number' ? p.video_output : undefined
    if (typeof directPerVideo === 'number' && directPerVideo > 0) {
      const total = directPerVideo * count
      return {
        unitPrice: directPerVideo,
        quantity: count,
        unitLabel: count === 1 ? 'clip' : 'clips',
        totalPrice: total,
        formattedPerUnit: `$${directPerVideo.toFixed(2)}/clip`,
        formattedTotal: `$${total.toFixed(2)}`,
        isAvailable: true,
        isVerified,
        matchedLineDescription: 'fixed clip rate',
      }
    }
  }

  // 2. Billing lines matching
  if (lines.length > 0) {
    if (!isVideoAction) {
      // Find image line matching resolution if possible
      const targetRes = normalizeResKey(settings.resolution)
      let matchedLine: ParsedPricingLine | undefined

      // Filter lines where unit is 'image' or billable is 'image_output'
      const imageLines = lines.filter((l) => l.unit === 'image' || l.billable === 'image_output')
      
      // Token-only rates (e.g. unit === 'token') are NOT flat output generation costs!
      // If only token lines exist, price is token-based and unavailable as flat image rate.
      if (imageLines.length === 0) {
        return defaultUnavailable
      }

      if (targetRes) {
        matchedLine = imageLines.find((l) => {
          const lineRes = normalizeResKey(l.conditions?.resolution || l.conditions?.image_size)
          return lineRes === targetRes
        })
      }
      if (!matchedLine) {
        matchedLine = imageLines[0]
      }

      if (matchedLine && matchedLine.priceUsd > 0) {
        const total = matchedLine.priceUsd * count
        return {
          unitPrice: matchedLine.priceUsd,
          quantity: count,
          unitLabel: count === 1 ? 'image' : 'images',
          totalPrice: total,
          formattedPerUnit: `$${matchedLine.priceUsd.toFixed(3)}/image`,
          formattedTotal: `$${total.toFixed(2)}`,
          isAvailable: true,
          isVerified,
          matchedLineDescription: matchedLine.conditions?.resolution ? `${matchedLine.conditions.resolution} rate` : 'image rate',
        }
      }
    } else {
      // Video lines matching (unit can be 'second' or 'clip' / 'video')
      const targetRes = normalizeResKey(settings.resolution)
      const duration = settings.durationSeconds && settings.durationSeconds > 0 ? settings.durationSeconds : 8
      const videoLines = lines.filter((l) => l.unit === 'second' || l.unit === 'clip' || l.unit === 'video' || l.billable === 'video_output')

      if (videoLines.length === 0) {
        return defaultUnavailable
      }

      let matchedLine: ParsedPricingLine | undefined
      if (targetRes) {
        matchedLine = videoLines.find((l) => {
          const lineRes = normalizeResKey(l.conditions?.resolution)
          return lineRes === targetRes
        })
      }
      if (!matchedLine) {
        matchedLine = videoLines[0]
      }

      if (matchedLine && matchedLine.priceUsd > 0) {
        if (matchedLine.unit === 'second') {
          const perClipCost = matchedLine.priceUsd * duration
          const total = perClipCost * count
          return {
            unitPrice: matchedLine.priceUsd,
            quantity: count,
            unitLabel: `${duration}s clip`,
            totalPrice: total,
            formattedPerUnit: `$${matchedLine.priceUsd.toFixed(3)}/sec ($${perClipCost.toFixed(2)}/clip)`,
            formattedTotal: `$${total.toFixed(2)}`,
            isAvailable: true,
            isVerified,
            matchedLineDescription: `${duration}s at $${matchedLine.priceUsd.toFixed(3)}/s`,
          }
        } else {
          const total = matchedLine.priceUsd * count
          return {
            unitPrice: matchedLine.priceUsd,
            quantity: count,
            unitLabel: count === 1 ? 'clip' : 'clips',
            totalPrice: total,
            formattedPerUnit: `$${matchedLine.priceUsd.toFixed(2)}/clip`,
            formattedTotal: `$${total.toFixed(2)}`,
            isAvailable: true,
            isVerified,
            matchedLineDescription: 'per clip rate',
          }
        }
      }
    }
  }

  return defaultUnavailable
}
