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

export interface MediaFeatureOption {
  status?: string
  supported?: boolean
  max_inputs?: number
  conditions?: Record<string, any>
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
  tier?: string
  [key: string]: any
}

export interface ModelBillingLine {
  kind?: string
  billable?: string
  unit?: string
  unit_per?: number | string
  price_usd?: number | string
  rate_multiplier?: number
  multiplier?: number
  conditions?: ModelBillingCondition
  variant?: string
  sku?: string
  service_tier?: string
}

export interface ModelBilling {
  status?: string
  currency?: string
  lines?: ModelBillingLine[]
}

export interface ModelPricing {
  currency?: string
  is_free?: boolean
  billing?: ModelBilling
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

export const SUPPORTED_VIDEO_RESOLUTIONS = ['360p', '720p', '1080p', '4k'] as const
export type SupportedVideoResolution = (typeof SUPPORTED_VIDEO_RESOLUTIONS)[number]

export const SUPPORTED_VIDEO_IMAGE_EXTENSIONS = [
  '.png',
  '.jpg',
  '.jpeg',
] as const

export const SUPPORTED_VIDEO_IMAGE_MIME_TYPES = [
  'image/png',
  'image/jpeg',
] as const

/**
 * Normalizes user- or catalog-specified resolution keys into standard canonical strings.
 */
export function normalizeVideoResKey(cond: string): '360p' | '720p' | '1080p' | '4k' | string {
  const c = (cond || '').toLowerCase().trim()
  if (c === '360p' || c === '360' || c === 'sd') return '360p'
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
    '360p': 'Unavailable',
    '720p': 'Unavailable',
    '1080p': 'Unavailable',
    '4k': 'Unavailable',
  }
  const emptyUnitRates: Record<string, number | undefined> = {
    '360p': undefined,
    '720p': undefined,
    '1080p': undefined,
    '4k': undefined,
  }
  const emptyTotals: Record<string, number | undefined> = {
    '360p': undefined,
    '720p': undefined,
    '1080p': undefined,
    '4k': undefined,
  }

  // Reject NaN / Infinity / non-integer / out-of-bounds clip count
  if (
    typeof clipCount !== 'number' ||
    !Number.isFinite(clipCount) ||
    isNaN(clipCount) ||
    !Number.isInteger(clipCount) ||
    clipCount < 1 ||
    clipCount > 8
  ) {
    return {
      ratePerSec: undefined,
      rateForClip: undefined,
      totalPrice: undefined,
      fixedPrice: undefined,
      formattedSummary: 'Pricing unavailable · Invalid clip count',
      ratesByResolution: emptyRates,
      unitRatesByResolution: emptyUnitRates,
      totalsByResolution: emptyTotals,
      isVerified: false,
      priceStatus: 'unknown',
    }
  }

  // Reject NaN / Infinity / negative duration
  if (
    typeof durationSeconds !== 'number' ||
    !Number.isFinite(durationSeconds) ||
    isNaN(durationSeconds) ||
    durationSeconds < 0
  ) {
    return {
      ratePerSec: undefined,
      rateForClip: undefined,
      totalPrice: undefined,
      fixedPrice: undefined,
      formattedSummary: 'Pricing unavailable · Invalid duration',
      ratesByResolution: emptyRates,
      unitRatesByResolution: emptyUnitRates,
      totalsByResolution: emptyTotals,
      isVerified: false,
      priceStatus: 'unknown',
    }
  }

  const count = clipCount
  const reqTier = (options?.serviceTier || 'standard').toLowerCase().trim()
  const reqAudio = options?.includesAudio !== undefined ? options.includesAudio : true

  if (!option || !option.pricing) {
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

  // Strict currency validation: read billing.currency first, then pricing.currency. Only USD supported.
  const billingCurrency = (typeof p.billing?.currency === 'string' ? p.billing.currency : '').trim().toUpperCase()
  const pricingCurrency = (typeof p.currency === 'string' ? p.currency : '').trim().toUpperCase()
  const currency = billingCurrency || pricingCurrency || 'USD'
  if (currency !== 'USD') {
    return {
      ratePerSec: undefined,
      rateForClip: undefined,
      totalPrice: undefined,
      fixedPrice: undefined,
      formattedSummary: `Pricing unavailable · Unsupported currency ${billingCurrency || pricingCurrency}`,
      ratesByResolution: emptyRates,
      unitRatesByResolution: emptyUnitRates,
      totalsByResolution: emptyTotals,
      isVerified: false,
      priceStatus: 'unknown',
    }
  }

  // Strict billing status check: don't invent pricing from is_free absent authority
  const billingStatus = (p.billing?.status || '').toLowerCase().trim()
  const hasVerifiedStatus = billingStatus === 'verified' || billingStatus === 'partially_verified'

  if (!hasVerifiedStatus) {
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

  if (p.is_free === true) {
    const freeRates: Record<string, string> = {
      '360p': '$0.00',
      '720p': '$0.00',
      '1080p': '$0.00',
      '4k': '$0.00',
    }
    const freeTotals: Record<string, number | undefined> = {
      '360p': 0,
      '720p': 0,
      '1080p': 0,
      '4k': 0,
    }
    const freeUnits: Record<string, number | undefined> = {
      '360p': 0,
      '720p': 0,
      '1080p': 0,
      '4k': 0,
    }
    return {
      ratePerSec: 0,
      rateForClip: 0,
      totalPrice: 0,
      fixedPrice: 0,
      formattedSummary: 'Catalog estimate: $0.00 (not a billed charge)',
      ratesByResolution: freeRates,
      unitRatesByResolution: freeUnits,
      totalsByResolution: freeTotals,
      isVerified: true,
      priceStatus: 'free',
    }
  }

  const rawLines = Array.isArray(p.billing?.lines) ? p.billing.lines : []

  interface MatchedCandidate {
    perSecRate?: number
    fixedPrice?: number
    rateForClip?: number
    perMillionTokens?: number
  }

  const matchCandidateForRes = (targetRes: string): MatchedCandidate | null | 'ambiguous' => {
    const normalizedTarget = normalizeVideoResKey(targetRes)
    const matches: MatchedCandidate[] = []

    for (const line of rawLines) {
      if (!line || typeof line !== 'object') continue
      if (line.kind && line.kind !== 'billing_rate') continue
      const billable = (line.billable || '').toLowerCase().trim()
      if (billable !== 'video_output' && billable !== 'video') continue

      const pUSD = typeof line.price_usd === 'number' ? line.price_usd : parseFloat(String(line.price_usd))
      if (isNaN(pUSD) || pUSD < 0 || !Number.isFinite(pUSD)) continue

      // Support unit_per and rate multipliers from actual snapshot pricing
      let unitPer = 1
      if (line.unit_per !== undefined && line.unit_per !== null) {
        const up = typeof line.unit_per === 'number' ? line.unit_per : parseFloat(String(line.unit_per))
        if (isNaN(up) || up <= 0 || !Number.isFinite(up)) continue
        unitPer = up
      }

      let effectivePrice = pUSD / unitPer
      const multiplier = line.rate_multiplier || line.multiplier
      if (typeof multiplier === 'number' && Number.isFinite(multiplier) && multiplier > 0) {
        effectivePrice *= multiplier
      }

      const conds = (line.conditions || {}) as Record<string, any>
      const condKeys = Object.keys(conds)

      // Reject lines with unresolved unknown conditions (provider_sku, region, device, etc.)
      const knownCondKeys = new Set(['resolution', 'variant', 'sku', 'includes_audio', 'service_tier', 'tier', 'charged_only_on_success'])
      const hasUnknownCond = condKeys.some((k) => !knownCondKeys.has(k.toLowerCase()))
      if (hasUnknownCond) {
        continue
      }

      if (conds.tier !== undefined && conds.tier !== 'paid') continue
      if (conds.charged_only_on_success !== undefined && typeof conds.charged_only_on_success !== 'boolean') continue

      // Reject lines with unresolved SKU conditions
      const lineSKU = (line.sku || conds.sku || '').trim()
      if (lineSKU !== '') {
        continue
      }

      const resCond = normalizeVideoResKey(conds.resolution || '')
      const lineVariant = normalizeVideoResKey(line.variant || conds.variant || '')

      // Resolution condition matching
      if (resCond !== '') {
        if (resCond !== normalizedTarget) continue
        // If line also has a variant condition, it must either match normalizedTarget or continue
        if (lineVariant !== '' && lineVariant !== normalizedTarget) continue
      } else if (lineVariant !== '') {
        if (lineVariant === '360p' || lineVariant === '720p' || lineVariant === '1080p' || lineVariant === '4k') {
          if (lineVariant !== normalizedTarget) continue
        } else {
          // Unresolved variant condition not matching resolution
          continue
        }
      } else if (normalizedTarget === '') {
        // Line has no resolution constraint and target is unspecified
      }

      // Audio condition matching
      if (conds.includes_audio !== undefined) {
        if (Boolean(conds.includes_audio) !== reqAudio) continue
      }

      // Service tier condition matching
      const lineTier = (conds.service_tier || line.service_tier || conds.tier || '').toLowerCase().trim()
      if (lineTier !== '' && lineTier !== 'paid' && lineTier !== reqTier) continue

      // Compute rate for clip based on unit.
      // Fixed-per-video prices can be known with omitted duration (per second cannot).
      const unit = (line.unit || '').toLowerCase().trim()
      let perSec: number | undefined
      let fixed: number | undefined
      let clipPrice = 0

      switch (unit) {
        case 'million_tokens':
          // A known output-token rate is not a known clip total. Never assume
          // output token counts or treat a rounded equivalent as the billing rate.
          matches.push({ perMillionTokens: effectivePrice })
          continue
        case 'second':
        case 'sec':
          if (durationSeconds <= 0 || !Number.isFinite(durationSeconds)) {
            continue
          }
          perSec = effectivePrice
          clipPrice = effectivePrice * durationSeconds
          break
        case 'minute':
        case 'min':
          if (durationSeconds <= 0 || !Number.isFinite(durationSeconds)) {
            continue
          }
          perSec = effectivePrice / 60
          clipPrice = (effectivePrice / 60) * durationSeconds
          break
        case 'video':
        case 'generation':
        case 'clip':
          fixed = effectivePrice
          clipPrice = effectivePrice
          break
        default:
          continue
      }

      matches.push({ perSecRate: perSec, fixedPrice: fixed, rateForClip: clipPrice })
    }

    if (matches.length === 0) return null
    if (matches.length === 1) return matches[0]

    // Check for ambiguity across multiple matching lines
    const first = matches[0]
    const allSame = matches.every((m) =>
      m.perMillionTokens === first.perMillionTokens && m.rateForClip === first.rateForClip
    )
    if (allSame) return matches[0]

    return 'ambiguous'
  }

  // Model-level constraint validation
  const modelAllowedResolutions = resolveAllowedVideoResolutions(option.generationOptions)
  const normalizedActiveRes = normalizeVideoResKey(resolution)
  if (modelAllowedResolutions.length > 0 && !modelAllowedResolutions.map(normalizeVideoResKey).includes(normalizedActiveRes)) {
    return {
      ratePerSec: undefined,
      rateForClip: undefined,
      totalPrice: undefined,
      fixedPrice: undefined,
      formattedSummary: `Pricing unavailable · Resolution ${resolution} unsupported for model`,
      ratesByResolution: emptyRates,
      unitRatesByResolution: emptyUnitRates,
      totalsByResolution: emptyTotals,
      isVerified: false,
      priceStatus: 'unknown',
    }
  }

  const modelAllowedDurations = resolveAllowedVideoDurations(option.generationOptions, normalizedActiveRes)
  if (modelAllowedDurations.length > 0 && durationSeconds > 0 && !modelAllowedDurations.includes(durationSeconds)) {
    return {
      ratePerSec: undefined,
      rateForClip: undefined,
      totalPrice: undefined,
      fixedPrice: undefined,
      formattedSummary: `Pricing unavailable · Duration ${durationSeconds}s unsupported for resolution ${resolution}`,
      ratesByResolution: emptyRates,
      unitRatesByResolution: emptyUnitRates,
      totalsByResolution: emptyTotals,
      isVerified: false,
      priceStatus: 'unknown',
    }
  }

  const ratesByRes: Record<string, string> = { ...emptyRates }
  const unitRatesByRes: Record<string, number | undefined> = { ...emptyUnitRates }
  const totalsByRes: Record<string, number | undefined> = { ...emptyTotals }

  const resolutionsToCheck = new Set<string>(['360p', '720p', '1080p', '4k'])
  if (modelAllowedResolutions.length > 0) {
    for (const r of modelAllowedResolutions) {
      resolutionsToCheck.add(normalizeVideoResKey(r))
    }
  }

  for (const r of resolutionsToCheck) {
    if (modelAllowedResolutions.length > 0 && !modelAllowedResolutions.map(normalizeVideoResKey).includes(r)) {
      ratesByRes[r] = 'Unavailable'
      unitRatesByRes[r] = undefined
      totalsByRes[r] = undefined
      continue
    }

    const resCandidate = matchCandidateForRes(r)
    if (resCandidate && resCandidate !== 'ambiguous') {
      if (resCandidate.perMillionTokens !== undefined) {
        ratesByRes[r] = `$${resCandidate.perMillionTokens.toFixed(2)}/1M output tokens`
        continue
      }
      const clipPrice = resCandidate.rateForClip
      if (clipPrice === undefined) continue
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

  const activeCandidate = matchCandidateForRes(normalizedActiveRes)
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

  if (activeCandidate.perMillionTokens !== undefined) {
    return {
      formattedSummary: `Video output: $${activeCandidate.perMillionTokens.toFixed(2)}/1M tokens · Clip total unknown until output usage is known; input charges additional`,
      ratesByResolution: ratesByRes,
      unitRatesByResolution: unitRatesByRes,
      totalsByResolution: totalsByRes,
      isVerified: false,
      priceStatus: 'unknown',
    }
  }

  const rateForClip = activeCandidate.rateForClip!
  const totalPrice = rateForClip * count
  const ratePerSec = activeCandidate.perSecRate
  const fixedPrice = activeCandidate.fixedPrice

  let formattedSummary: string
  if (fixedPrice !== undefined) {
    if (durationSeconds > 0) {
      formattedSummary = count > 1
        ? `$${totalPrice.toFixed(2)} Total ($${fixedPrice.toFixed(2)}/clip × ${count} clips) · Catalog estimate (not a billed charge)`
        : `$${totalPrice.toFixed(2)} Total (${durationSeconds}s clip) · Catalog estimate (not a billed charge)`
    } else {
      formattedSummary = count > 1
        ? `$${totalPrice.toFixed(2)} Total ($${fixedPrice.toFixed(2)}/clip × ${count} clips) · Catalog estimate (not a billed charge)`
        : `$${totalPrice.toFixed(2)} Total · Catalog estimate (not a billed charge)`
    }
  } else if (ratePerSec !== undefined) {
    formattedSummary = count > 1
      ? `$${totalPrice.toFixed(2)} Total ($${ratePerSec.toFixed(2)}/sec × ${durationSeconds}s × ${count} clips) · Catalog estimate (not a billed charge)`
      : `$${totalPrice.toFixed(2)} Total ($${ratePerSec.toFixed(2)}/sec × ${durationSeconds}s clip) · Catalog estimate (not a billed charge)`
  } else {
    formattedSummary = `$${totalPrice.toFixed(2)} Total · Catalog estimate (not a billed charge)`
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
 * Returns empty array [] on unavailable metadata; never invents fallback durations.
 */
export function resolveAllowedVideoDurations(
  genOptions?: MediaCatalogGenerationOptions,
  resolution?: string
): number[] {
  if (!genOptions) return []
  const resKey = normalizeVideoResKey(resolution || '')
  if (resKey && genOptions.resolution_durations && genOptions.resolution_durations[resKey]) {
    const list = genOptions.resolution_durations[resKey]
    if (Array.isArray(list) && list.length > 0) return list
  }
  const rawKey = (resolution || '').toLowerCase().trim()
  if (rawKey && genOptions.resolution_durations && genOptions.resolution_durations[rawKey]) {
    const list = genOptions.resolution_durations[rawKey]
    if (Array.isArray(list) && list.length > 0) return list
  }
  if (Array.isArray(genOptions.durations) && genOptions.durations.length > 0) {
    return genOptions.durations
  }
  return []
}

/**
 * Returns allowed video resolutions supported by the model.
 * Returns empty array [] on unavailable metadata; never invents fallback resolutions.
 */
export function resolveAllowedVideoResolutions(
  genOptions?: MediaCatalogGenerationOptions
): string[] {
  if (Array.isArray(genOptions?.resolutions) && genOptions.resolutions.length > 0) {
    return genOptions.resolutions
  }
  return []
}

/**
 * Returns allowed video aspect ratios supported by the model.
 * Returns empty array [] on unavailable metadata; never invents fallback ratios.
 */
export function resolveAllowedVideoAspectRatios(
  genOptions?: MediaCatalogGenerationOptions
): string[] {
  if (Array.isArray(genOptions?.aspect_ratios) && genOptions.aspect_ratios.length > 0) {
    return genOptions.aspect_ratios
  }
  return []
}

/**
 * Validates that an attachment for a video task is strictly a supported image reference.
 * Fails closed unless genOptions.initial_image.supported is true.
 * Allows only backend locally decoded PNG and JPEG images.
 * Rejects non-images, SVG, HEIC/HEIF, and mismatched/fake extensions or MIME types.
 */
export function validateVideoAttachment(
  file: { name?: string; type?: string; kind?: string },
  genOptions?: MediaCatalogGenerationOptions
): { valid: boolean; error?: string } {
  // Fail closed: model must explicitly advertise initial_image support
  if (!genOptions?.initial_image || genOptions.initial_image.supported !== true) {
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
      error: `Unsupported attachment kind "${kind}" for video; only locally decoded images (.png, .jpg, .jpeg, .webp) are supported as reference inputs.`,
    }
  }

  // Reject SVG, HEIC, HEIF explicitly along with any other non-raster formats
  const hasUnsupportedExt = name.endsWith('.heic') || name.endsWith('.heif') || name.endsWith('.svg')
  const hasUnsupportedMime = mime.includes('heic') || mime.includes('heif') || mime.includes('svg')
  if (hasUnsupportedExt || hasUnsupportedMime) {
    return {
      valid: false,
      error: 'Unsupported image format. Only locally decoded images (.png, .jpg, .jpeg, .webp) are supported as video reference inputs.',
    }
  }

  const hasAllowedExt = SUPPORTED_VIDEO_IMAGE_EXTENSIONS.some((ext) => name.endsWith(ext))
  const hasAllowedMime = SUPPORTED_VIDEO_IMAGE_MIME_TYPES.some((m) => mime === m)

  // Guard against fake non-image MIME named with image extension (e.g. type: "text/plain", name: "foo.png")
  if (mime !== '' && !hasAllowedMime) {
    return {
      valid: false,
      error: `Unsupported MIME type "${mime}" for video reference. Only PNG and JPEG images are allowed.`,
    }
  }

  // Guard against non-image file with image MIME or missing/wrong extension when filename provided
  if (name !== '' && !hasAllowedExt) {
    return {
      valid: false,
      error: 'Unsupported file extension for video reference. Only .png, .jpg, .jpeg, and .webp are allowed.',
    }
  }

  if (!hasAllowedExt && !hasAllowedMime) {
    return {
      valid: false,
      error: 'Invalid image attachment: missing valid PNG, JPEG, or WebP extension or MIME type.',
    }
  }

  // Enforce model-level supported_mime_types if specified
  if (Array.isArray(genOptions.initial_image.supported_mime_types) && genOptions.initial_image.supported_mime_types.length > 0) {
    const modelAllowedMimes = genOptions.initial_image.supported_mime_types.map((m) => m.toLowerCase().trim())
    let effectiveMime = mime
    if (!effectiveMime) {
      if (name.endsWith('.png')) effectiveMime = 'image/png'
      else if (name.endsWith('.jpg') || name.endsWith('.jpeg')) effectiveMime = 'image/jpeg'
    }
    if (effectiveMime && !modelAllowedMimes.includes(effectiveMime)) {
      return {
        valid: false,
        error: `MIME type "${effectiveMime}" is not supported by the selected video model (allowed: ${modelAllowedMimes.join(', ')})`,
      }
    }
  }

  return { valid: true }
}

/**
 * Resolves the provider-qualified model identifier from the selected option.
 * Uses option.provider and model/id, avoiding doubled prefixes (e.g. openrouter/google/veo-3.1).
 */
export function resolveQualifiedVideoModel(
  option?: TaskModalModelOption,
  fallbackModel?: string
): string | undefined {
  const provider = (option?.provider || '').trim().toLowerCase()
  const raw = (option?.model || option?.id || fallbackModel || '').trim()
  if (!raw) return undefined

  if (!provider) {
    return raw
  }

  const prefix = `${provider}:`
  if (raw.toLowerCase().startsWith(prefix)) return raw
  // Model IDs such as google/veo-3.1 belong to OpenRouter and must remain intact.
  return `${provider}:${raw}`
}
