/**
 * Authoritative video modal options, resolution-dependent constraints,
 * and snapshot-backed pricing resolution for Desktop project tasks.
 */

export interface MediaInitialImageOption {
  supported: boolean
  max_inputs?: number
  supported_mime_types?: string[]
  notes?: string
}

export interface MediaOptionVariant {
  mode?: string
  supported_values?: any[]
  conditions?: Record<string, any>
  notes?: string
}

export interface MediaOptionSetting {
  status?: string
  default_value?: any
  supported_values?: any[]
  provider_documented_values?: any[]
  variants?: MediaOptionVariant[]
  notes?: string
}

export interface MediaCatalogGenerationOptions {
  aspect_ratios?: string[]
  resolutions?: string[]
  durations?: number[]
  default_ratio?: string
  default_resolution?: string
  default_duration?: number
  max_outputs?: number
  resolution_durations?: Record<string, number[]>
  initial_image?: MediaInitialImageOption
  settings?: Record<string, MediaOptionSetting>
  features?: Record<string, any>
}

export interface ModelBillingCondition {
  resolution?: string
  variant?: string
  sku?: string
  includes_audio?: boolean
  service_tier?: string
  [key: string]: any
}

export interface ModelBillingLine {
  billable?: string
  unit?: string
  price_usd?: number | string
  conditions?: ModelBillingCondition
  variant?: string
  sku?: string
  service_tier?: string
}

export interface ModelPricing {
  currency?: string
  is_free?: boolean
  billing?: {
    status?: string
    lines?: ModelBillingLine[]
  }
  video_output?: number
  per_video?: number
  [key: string]: any
}

export interface TaskModalModelOption {
  id: string
  label: string
  ready: boolean
  reason?: string
  pricing?: ModelPricing | any
  provider?: string
  model?: string
  generationOptions?: MediaCatalogGenerationOptions
}

export interface VideoPricingResult {
  ratePerSec?: number
  rateForClip?: number
  totalPrice?: number
  fixedPrice?: number
  formattedSummary: string
  ratesByResolution: Record<string, string>
  unitRatesByResolution: Record<string, number | undefined>
  totalsByResolution: Record<string, number | undefined>
  isVerified: boolean
  priceStatus: 'verified' | 'free' | 'unknown'
}

export const SUPPORTED_VIDEO_RESOLUTIONS = ['720p', '1080p', '4k'] as const
export type SupportedVideoResolution = (typeof SUPPORTED_VIDEO_RESOLUTIONS)[number]

export const SUPPORTED_VIDEO_IMAGE_EXTENSIONS = [
  '.png',
  '.jpg',
  '.jpeg',
  '.webp',
  '.heic',
  '.heif',
  '.svg',
] as const

/**
 * Normalizes user- or catalog-specified resolution keys into standard canonical strings.
 */
export function normalizeVideoResKey(cond: string): '720p' | '1080p' | '4k' | string {
  const c = (cond || '').toLowerCase().trim()
  if (c === '720p' || c === '720' || c === 'hd') return '720p'
  if (c === '1080p' || c === '1080' || c === 'fhd' || c === 'standard') return '1080p'
  if (c === '4k' || c === '2160p' || c === 'uhd') return '4k'
  return c
}

export interface ResolveVideoPricingOptions {
  serviceTier?: string
  includesAudio?: boolean
}

/**
 * Resolves video pricing exclusively from verified USD catalog billing lines.
 * Never seeds guesses (.05/.08/.20) and never falls back to arbitrary default durations.
 * Multiplies exact duration and clip count (1..8).
 * If catalog pricing is unverified, missing, or ambiguous, returns priceStatus: 'unknown' (unavailable).
 */
export function resolveVideoPricing(
  option: TaskModalModelOption | undefined,
  resolution: string,
  durationSeconds: number,
  clipCount: number = 1,
  options?: ResolveVideoPricingOptions
): VideoPricingResult {
  const emptyRates: Record<string, string> = {
    '720p': 'Unavailable',
    '1080p': 'Unavailable',
    '4k': 'Unavailable',
  }
  const emptyUnitRates: Record<string, number | undefined> = {
    '720p': undefined,
    '1080p': undefined,
    '4k': undefined,
  }
  const emptyTotals: Record<string, number | undefined> = {
    '720p': undefined,
    '1080p': undefined,
    '4k': undefined,
  }

  const count = Math.max(1, Math.min(8, Math.floor(clipCount || 1)))
  const reqTier = (options?.serviceTier || 'standard').toLowerCase()
  const reqAudio = options?.includesAudio !== undefined ? options.includesAudio : true

  if (!option || !option.pricing || durationSeconds <= 0) {
    return {
      ratePerSec: undefined,
      rateForClip: undefined,
      totalPrice: undefined,
      fixedPrice: undefined,
      formattedSummary: 'Pricing unavailable · Model pricing unverified or unsupported',
      ratesByResolution: emptyRates,
      unitRatesByResolution: emptyUnitRates,
      totalsByResolution: emptyTotals,
      isVerified: false,
      priceStatus: 'unknown',
    }
  }

  const p = option.pricing as ModelPricing

  // Strict currency validation: only USD is supported for known billing
  if (typeof p.currency === 'string' && p.currency.trim() !== '' && p.currency.toUpperCase() !== 'USD') {
    return {
      ratePerSec: undefined,
      rateForClip: undefined,
      totalPrice: undefined,
      fixedPrice: undefined,
      formattedSummary: `Pricing unavailable · Unsupported currency ${p.currency}`,
      ratesByResolution: emptyRates,
      unitRatesByResolution: emptyUnitRates,
      totalsByResolution: emptyTotals,
      isVerified: false,
      priceStatus: 'unknown',
    }
  }

  if (p.is_free === true) {
    const freeRates: Record<string, string> = {
      '720p': '$0.00',
      '1080p': '$0.00',
      '4k': '$0.00',
    }
    const freeTotals: Record<string, number | undefined> = {
      '720p': 0,
      '1080p': 0,
      '4k': 0,
    }
    const freeUnits: Record<string, number | undefined> = {
      '720p': 0,
      '1080p': 0,
      '4k': 0,
    }
    return {
      ratePerSec: 0,
      rateForClip: 0,
      totalPrice: 0,
      fixedPrice: 0,
      formattedSummary: 'Free ($0.00 billed)',
      ratesByResolution: freeRates,
      unitRatesByResolution: freeUnits,
      totalsByResolution: freeTotals,
      isVerified: true,
      priceStatus: 'free',
    }
  }

  const billingStatus = (p.billing?.status || '').toLowerCase().trim()
  if (billingStatus !== 'verified' && billingStatus !== 'partially_verified') {
    return {
      ratePerSec: undefined,
      rateForClip: undefined,
      totalPrice: undefined,
      fixedPrice: undefined,
      formattedSummary: 'Pricing unavailable · Model pricing unverified or unsupported',
      ratesByResolution: emptyRates,
      unitRatesByResolution: emptyUnitRates,
      totalsByResolution: emptyTotals,
      isVerified: false,
      priceStatus: 'unknown',
    }
  }

  const rawLines = Array.isArray(p.billing?.lines) ? p.billing.lines : []

  interface MatchedCandidate {
    perSecRate?: number
    fixedPrice?: number
    rateForClip: number
  }

  const matchCandidateForRes = (targetRes: string): MatchedCandidate | null | 'ambiguous' => {
    const normalizedTarget = normalizeVideoResKey(targetRes)
    const matches: MatchedCandidate[] = []

    for (const line of rawLines) {
      if (!line || typeof line !== 'object') continue
      const billable = (line.billable || '').toLowerCase().trim()
      if (billable !== 'video_output' && billable !== 'video') continue

      const pUSD = typeof line.price_usd === 'number' ? line.price_usd : parseFloat(String(line.price_usd))
      if (isNaN(pUSD) || pUSD < 0) continue

      const conds = (line.conditions || {}) as ModelBillingCondition
      const resCond = normalizeVideoResKey(conds.resolution || '')
      const lineVariant = normalizeVideoResKey(line.variant || conds.variant || '')
      const lineSKU = line.sku || conds.sku || ''

      // Resolution condition matching
      if (resCond !== '') {
        if (resCond !== normalizedTarget) continue
      } else if (lineVariant === '720p' || lineVariant === '1080p' || lineVariant === '4k') {
        if (lineVariant !== normalizedTarget) continue
      } else if ((lineVariant !== '' || lineSKU !== '') && normalizedTarget === '') {
        continue
      } else if (lineVariant !== '' || lineSKU !== '') {
        // Line has an unresolved variant or SKU condition not matching resolution
        continue
      }

      // Audio condition matching
      if (conds.includes_audio !== undefined) {
        if (Boolean(conds.includes_audio) !== reqAudio) continue
      }

      // Service tier condition matching
      const lineTier = (conds.service_tier || line.service_tier || '').toLowerCase().trim()
      if (lineTier !== '' && lineTier !== reqTier) continue

      // Compute rate for clip based on unit
      const unit = (line.unit || '').toLowerCase().trim()
      let perSec: number | undefined
      let fixed: number | undefined
      let clipPrice = 0

      switch (unit) {
        case 'second':
        case 'sec':
          perSec = pUSD
          clipPrice = pUSD * durationSeconds
          break
        case 'minute':
        case 'min':
          perSec = pUSD / 60
          clipPrice = (pUSD / 60) * durationSeconds
          break
        case 'video':
        case 'generation':
          fixed = pUSD
          clipPrice = pUSD
          break
        default:
          continue
      }

      matches.push({ perSecRate: perSec, fixedPrice: fixed, rateForClip: clipPrice })
    }

    if (matches.length === 0) return null
    if (matches.length === 1) return matches[0]

    // Check for ambiguity across multiple matching lines
    const firstPrice = matches[0].rateForClip
    const allSame = matches.every((m) => Math.abs(m.rateForClip - firstPrice) < 0.0001)
    if (allSame) return matches[0]

    return 'ambiguous'
  }

  const ratesByRes: Record<string, string> = { ...emptyRates }
  const unitRatesByRes: Record<string, number | undefined> = { ...emptyUnitRates }
  const totalsByRes: Record<string, number | undefined> = { ...emptyTotals }

  for (const r of ['720p', '1080p', '4k'] as const) {
    const resCandidate = matchCandidateForRes(r)
    if (resCandidate && resCandidate !== 'ambiguous') {
      const clipPrice = resCandidate.rateForClip
      const totalForR = clipPrice * count
      totalsByRes[r] = totalForR
      if (resCandidate.fixedPrice !== undefined) {
        unitRatesByRes[r] = resCandidate.fixedPrice
        ratesByRes[r] = count > 1
          ? `$${totalForR.toFixed(2)} ($${resCandidate.fixedPrice.toFixed(2)}/clip)`
          : `$${resCandidate.fixedPrice.toFixed(2)}/clip`
      } else if (resCandidate.perSecRate !== undefined) {
        unitRatesByRes[r] = resCandidate.perSecRate
        ratesByRes[r] = count > 1
          ? `$${totalForR.toFixed(2)} ($${resCandidate.perSecRate.toFixed(2)}/s)`
          : `$${clipPrice.toFixed(2)} ($${resCandidate.perSecRate.toFixed(2)}/s)`
      }
    }
  }

  const activeRes = normalizeVideoResKey(resolution)
  const activeCandidate = matchCandidateForRes(activeRes)

  if (!activeCandidate || activeCandidate === 'ambiguous') {
    return {
      ratePerSec: undefined,
      rateForClip: undefined,
      totalPrice: undefined,
      fixedPrice: undefined,
      formattedSummary: 'Pricing unavailable · Model pricing unverified or unsupported',
      ratesByResolution: ratesByRes,
      unitRatesByResolution: unitRatesByRes,
      totalsByResolution: totalsByRes,
      isVerified: false,
      priceStatus: 'unknown',
    }
  }

  const rateForClip = activeCandidate.rateForClip
  const totalPrice = rateForClip * count
  const ratePerSec = activeCandidate.perSecRate
  const fixedPrice = activeCandidate.fixedPrice

  let formattedSummary: string
  if (fixedPrice !== undefined) {
    formattedSummary = count > 1
      ? `$${totalPrice.toFixed(2)} Total ($${fixedPrice.toFixed(2)}/clip × ${count} clips) · Verified catalog`
      : `$${totalPrice.toFixed(2)} Total (${durationSeconds}s clip) · Verified catalog`
  } else if (ratePerSec !== undefined) {
    formattedSummary = count > 1
      ? `$${totalPrice.toFixed(2)} Total ($${ratePerSec.toFixed(2)}/sec × ${durationSeconds}s × ${count} clips) · Verified catalog`
      : `$${totalPrice.toFixed(2)} Total ($${ratePerSec.toFixed(2)}/sec × ${durationSeconds}s clip) · Verified catalog`
  } else {
    formattedSummary = `$${totalPrice.toFixed(2)} Total · Verified catalog`
  }

  return {
    ratePerSec,
    rateForClip,
    totalPrice,
    fixedPrice,
    formattedSummary,
    ratesByResolution: ratesByRes,
    unitRatesByResolution: unitRatesByRes,
    totalsByResolution: totalsByRes,
    isVerified: true,
    priceStatus: 'verified',
  }
}

/**
 * Returns allowed video durations for a model and active resolution, respecting resolution-dependent constraints.
 * E.g., Veo 1080p may require 8s duration, while 720p supports 4s, 6s, 8s.
 */
export function resolveAllowedVideoDurations(
  genOptions?: MediaCatalogGenerationOptions,
  resolution?: string
): number[] {
  if (!genOptions) return [4, 6, 8]
  const resKey = (resolution || '').toLowerCase().trim()
  if (resKey && genOptions.resolution_durations && genOptions.resolution_durations[resKey]) {
    const list = genOptions.resolution_durations[resKey]
    if (list && list.length > 0) return list
  }
  if (genOptions.durations && genOptions.durations.length > 0) {
    return genOptions.durations
  }
  return []
}

/**
 * Returns allowed video resolutions supported by the model.
 */
export function resolveAllowedVideoResolutions(
  genOptions?: MediaCatalogGenerationOptions
): string[] {
  if (genOptions?.resolutions && genOptions.resolutions.length > 0) {
    return genOptions.resolutions
  }
  return ['720p', '1080p', '4k']
}

/**
 * Returns allowed video aspect ratios supported by the model.
 */
export function resolveAllowedVideoAspectRatios(
  genOptions?: MediaCatalogGenerationOptions
): string[] {
  if (genOptions?.aspect_ratios && genOptions.aspect_ratios.length > 0) {
    return genOptions.aspect_ratios
  }
  return ['16:9', '9:16']
}

/**
 * Validates that an attachment for a video task is strictly a supported image reference.
 * Non-image files (docs, audio, video) are rejected.
 */
export function validateVideoAttachment(
  file: { name?: string; type?: string; kind?: string },
  genOptions?: MediaCatalogGenerationOptions
): { valid: boolean; error?: string } {
  if (genOptions?.initial_image && !genOptions.initial_image.supported) {
    return {
      valid: false,
      error: 'Selected video model does not support initial image reference inputs',
    }
  }

  const name = (file.name || '').toLowerCase().trim()
  const mime = (file.type || '').toLowerCase().trim()
  const kind = (file.kind || '').toLowerCase().trim()

  if (kind !== '' && kind !== 'image') {
    return {
      valid: false,
      error: `Unsupported attachment kind "${kind}" for video; only images (.png, .jpg, .webp, .heic, .svg) are supported as reference inputs.`,
    }
  }

  const isImageMime = mime.startsWith('image/')
  const hasImageExt = SUPPORTED_VIDEO_IMAGE_EXTENSIONS.some((ext) => name.endsWith(ext))

  if (!isImageMime && !hasImageExt) {
    return {
      valid: false,
      error: `Unsupported file type for video reference. Only images (.png, .jpg, .webp, .heic, .svg) are allowed.`,
    }
  }

  return { valid: true }
}

/**
 * Resolves the canonical model identifier from the selected option.
 */
export function resolveQualifiedVideoModel(
  option?: TaskModalModelOption,
  fallbackModel?: string
): string | undefined {
  if (option?.id) return option.id
  if (option?.model) return option.model
  return fallbackModel || undefined
}
