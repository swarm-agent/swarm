import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  resolveVideoPricing,
  resolveAllowedVideoDurations,
  resolveAllowedVideoResolutions,
  resolveAllowedVideoAspectRatios,
  validateVideoAttachment,
  resolveQualifiedVideoModel,
  normalizeVideoResKey,
  type TaskModalModelOption,
} from './videoTaskSettings'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)

// Purpose:
// - Requirement: Video pricing must be derived strictly from verified USD catalog billing lines,
//   eliminating hardcoded/guessed rates (.05/.08/.20). Clip count (1..8) and duration must scale
//   dynamically. Unknown or unverified models must be marked unavailable, not $0 or 8s.
// - Threat/regression: Presenting incorrect pricing estimates or charging unexpected amounts
//   due to guessed default rates or failure to respect model-specific constraints.
// - Boundary/authority: resolveVideoPricing in videoTaskSettings.ts consuming snapshot-backed pricing.
// - Test layer: Unit tests exercising snapshot fixtures for Standard, Fast, Lite, Omni, and negative cases.

test('resolveVideoPricing returns unavailable for undefined option or missing pricing without guessing', () => {
  // Requirement: Missing model option or unpriced records must return priceStatus: 'unknown'.
  // Threat: Silently guessing default prices when no model pricing is configured.
  // Boundary: resolveVideoPricing guard checks.
  const unpriced = resolveVideoPricing(undefined, '1080p', 8, 1)
  assert.equal(unpriced.isVerified, false)
  assert.equal(unpriced.priceStatus, 'unknown')
  assert.equal(unpriced.totalPrice, undefined)
  assert.equal(unpriced.rateForClip, undefined)
  assert.equal(unpriced.ratePerSec, undefined)
  assert.equal(unpriced.ratesByResolution['720p'], 'Unavailable')
  assert.equal(unpriced.ratesByResolution['1080p'], 'Unavailable')
  assert.equal(unpriced.ratesByResolution['4k'], 'Unavailable')
  assert.ok(unpriced.formattedSummary.includes('Pricing unavailable'))
})

test('resolveVideoPricing returns unavailable for zero or negative duration on per-second models', () => {
  // Requirement: Per-second billing requires positive durationSeconds; zero or negative must return unknown.
  // Threat: Undercharging or zeroing out charges when duration is not provided for per-second billing.
  // Boundary: resolveVideoPricing durationSeconds validation.
  const model: TaskModalModelOption = {
    id: 'veo-test',
    label: 'Veo Test',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        currency: 'USD',
        lines: [
          { billable: 'video_output', unit: 'second', unit_per: 1, price_usd: 0.10, conditions: { resolution: '720p' } },
        ],
      },
    },
  }
  const zeroDur = resolveVideoPricing(model, '720p', 0, 1)
  assert.equal(zeroDur.isVerified, false)
  assert.equal(zeroDur.totalPrice, undefined)

  const negDur = resolveVideoPricing(model, '720p', -5, 1)
  assert.equal(negDur.isVerified, false)
  assert.equal(negDur.totalPrice, undefined)
})

test('resolveVideoPricing allows fixed-per-video pricing with zero or omitted duration', () => {
  // Requirement: Fixed-per-video lines do not require duration and can resolve with durationSeconds = 0.
  // Threat: Incorrectly marking fixed-per-video models unavailable because duration was omitted.
  // Boundary: resolveVideoPricing fixed price candidate branch.
  const model: TaskModalModelOption = {
    id: 'fixed-video-model',
    label: 'Fixed Video Model',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        currency: 'USD',
        lines: [
          { billable: 'video_output', unit: 'video', unit_per: 1, price_usd: 0.50, conditions: { resolution: '720p' } },
        ],
      },
    },
  }
  const result = resolveVideoPricing(model, '720p', 0, 2)
  assert.equal(result.isVerified, true)
  assert.equal(result.priceStatus, 'verified')
  assert.equal(result.fixedPrice, 0.50)
  assert.equal(result.rateForClip, 0.50)
  assert.equal(result.totalPrice, 1.00)
  assert.ok(result.formattedSummary.includes('$1.00 Total ($0.50/clip × 2 clips)'))
})

test('resolveVideoPricing rejects non-USD currencies inside billing.currency or pricing.currency', () => {
  // Requirement: Only USD currency is supported for verified video pricing calculations.
  // Threat: Mixing currencies (EUR/GBP) without currency conversion, misquoting rates to users.
  // Boundary: resolveVideoPricing currency check on billing.currency and pricing.currency.
  const eurBillingModel: TaskModalModelOption = {
    id: 'veo-eur-billing',
    label: 'Veo EUR Billing',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        currency: 'EUR',
        lines: [
          { billable: 'video_output', unit: 'second', unit_per: 1, price_usd: 0.10, conditions: { resolution: '720p' } },
        ],
      },
    },
  }
  const resultBilling = resolveVideoPricing(eurBillingModel, '720p', 8, 1)
  assert.equal(resultBilling.isVerified, false)
  assert.equal(resultBilling.totalPrice, undefined)
  assert.ok(resultBilling.formattedSummary.includes('Unsupported currency EUR'))

  const eurPricingModel: TaskModalModelOption = {
    id: 'veo-eur-pricing',
    label: 'Veo EUR Pricing',
    ready: true,
    pricing: {
      currency: 'EUR',
      billing: {
        status: 'verified',
        lines: [
          { billable: 'video_output', unit: 'second', unit_per: 1, price_usd: 0.10, conditions: { resolution: '720p' } },
        ],
      },
    },
  }
  const resultPricing = resolveVideoPricing(eurPricingModel, '720p', 8, 1)
  assert.equal(resultPricing.isVerified, false)
  assert.equal(resultPricing.totalPrice, undefined)
  assert.ok(resultPricing.formattedSummary.includes('Unsupported currency EUR'))
})

test('resolveVideoPricing rejects unverified billing status and does not invent pricing from is_free absent authority', () => {
  // Requirement: An unverified billing status must return unknown, even if is_free is true.
  // Threat: Inventing $0.00 pricing based on unverified is_free flag without verified billing authority.
  // Boundary: resolveVideoPricing billing status check preceding is_free evaluation.
  const unverifiedFreeModel: TaskModalModelOption = {
    id: 'veo-unverified-free',
    label: 'Veo Unverified Free',
    ready: true,
    pricing: {
      is_free: true,
      billing: {
        status: 'unverified',
        currency: 'USD',
        lines: [],
      },
    },
  }
  const result = resolveVideoPricing(unverifiedFreeModel, '720p', 8, 1)
  assert.equal(result.isVerified, false)
  assert.equal(result.priceStatus, 'unknown')
  assert.equal(result.totalPrice, undefined)
  assert.ok(result.formattedSummary.includes('Pricing unavailable · Model pricing unverified or unsupported'))
})

test('resolveVideoPricing rejects NaN, Infinity, non-integer, and invalid clip counts', () => {
  // Requirement: clipCount must be a finite integer between 1 and 8.
  // Threat: Corrupting pricing math or bypassing clip limits with NaN, Infinity, or out-of-range counts.
  // Boundary: resolveVideoPricing clipCount validation.
  const model: TaskModalModelOption = {
    id: 'veo-test',
    label: 'Veo Test',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        currency: 'USD',
        lines: [
          { billable: 'video_output', unit: 'second', unit_per: 1, price_usd: 0.10, conditions: { resolution: '720p' } },
        ],
      },
    },
  }
  assert.equal(resolveVideoPricing(model, '720p', 8, NaN as any).priceStatus, 'unknown')
  assert.equal(resolveVideoPricing(model, '720p', 8, Infinity as any).priceStatus, 'unknown')
  assert.equal(resolveVideoPricing(model, '720p', 8, 0).priceStatus, 'unknown')
  assert.equal(resolveVideoPricing(model, '720p', 8, 9).priceStatus, 'unknown')
  assert.equal(resolveVideoPricing(model, '720p', 8, 1.5).priceStatus, 'unknown')
})

test('resolveVideoPricing reads and applies line unit_per and rate multipliers', () => {
  // Requirement: snapshot unit_per scales base rate (price_usd / unit_per), and rate multipliers apply.
  // Threat: Overcharging by ignoring unit_per (e.g. 10 units per rate).
  // Boundary: resolveVideoPricing line parsing for unit_per and multiplier.
  const model: TaskModalModelOption = {
    id: 'veo-scaled',
    label: 'Veo Scaled',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        currency: 'USD',
        lines: [
          {
            billable: 'video_output',
            unit: 'second',
            unit_per: 2, // $0.20 per 2 seconds => $0.10/s
            price_usd: 0.20,
            rate_multiplier: 1.5, // 1.5x multiplier => $0.15/s
            conditions: { resolution: '720p' },
          },
        ],
      },
    },
  }
  const result = resolveVideoPricing(model, '720p', 8, 1)
  assert.equal(result.isVerified, true)
  assert.equal(result.ratePerSec, 0.15)
  assert.equal(result.rateForClip, 1.20)
  assert.equal(result.totalPrice, 1.20)
})

test('resolveVideoPricing rejects lines with unresolved unknown conditions (e.g. provider_sku, device)', () => {
  // Requirement: Lines with unresolved unknown condition keys must be rejected even if resolution matches.
  // Threat: Accidental matching of specialized SKU pricing (e.g. reserved TPU rates) to standard requests.
  // Boundary: resolveVideoPricing condition key validation against known condition keys.
  const model: TaskModalModelOption = {
    id: 'veo-special-sku',
    label: 'Veo Special SKU',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        currency: 'USD',
        lines: [
          {
            billable: 'video_output',
            unit: 'second',
            unit_per: 1,
            price_usd: 0.05,
            conditions: { resolution: '720p', provider_sku: 'tpu_v5_reserved' },
          },
        ],
      },
    },
  }
  const result = resolveVideoPricing(model, '720p', 8, 1)
  assert.equal(result.isVerified, false)
  assert.equal(result.priceStatus, 'unknown')
})

test('resolveVideoPricing rejects lines with unresolved SKU condition even when resolution matches', () => {
  // Requirement: Lines with non-empty line.sku or conds.sku must not match standard requests without SKU.
  // Threat: Applying discounted or specialized SKU billing lines to general video runs.
  // Boundary: resolveVideoPricing SKU check in matchCandidateForRes.
  const model: TaskModalModelOption = {
    id: 'veo-sku-line',
    label: 'Veo SKU Line',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        currency: 'USD',
        lines: [
          {
            billable: 'video_output',
            unit: 'second',
            unit_per: 1,
            price_usd: 0.05,
            sku: 'batch_discount_sku',
            conditions: { resolution: '720p' },
          },
        ],
      },
    },
  }
  const result = resolveVideoPricing(model, '720p', 8, 1)
  assert.equal(result.isVerified, false)
  assert.equal(result.priceStatus, 'unknown')
})

test('resolveVideoPricing correctly calculates Veo Standard snapshot pricing with unit_per and billing.currency', () => {
  // Requirement: Veo Standard (.40/.40/.60 per second) scales correctly across resolution and clips.
  // Threat: Miscalculating multi-clip or 4k duration scaling.
  // Boundary: resolveVideoPricing with standard Veo pricing line fixtures.
  const veoStandard: TaskModalModelOption = {
    id: 'veo-3.1-generate-preview',
    label: 'Veo 3.1 Standard',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        currency: 'USD',
        lines: [
          { billable: 'video_output', unit: 'second', unit_per: 1, price_usd: 0.40, conditions: { resolution: '720p', includes_audio: true, service_tier: 'standard' } },
          { billable: 'video_output', unit: 'second', unit_per: 1, price_usd: 0.40, conditions: { resolution: '1080p', includes_audio: true, service_tier: 'standard' } },
          { billable: 'video_output', unit: 'second', unit_per: 1, price_usd: 0.60, conditions: { resolution: '4k', includes_audio: true, service_tier: 'standard' } },
        ],
      },
    },
  }

  // 1. 720p at 8s duration: $0.40/s * 8 = $3.20
  const p720 = resolveVideoPricing(veoStandard, '720p', 8, 1)
  assert.equal(p720.isVerified, true)
  assert.equal(p720.ratePerSec, 0.40)
  assert.equal(p720.rateForClip, 3.20)
  assert.equal(p720.totalPrice, 3.20)
  assert.equal(p720.ratesByResolution['720p'], '$3.20 ($0.40/s)')
  assert.equal(p720.ratesByResolution['1080p'], '$3.20 ($0.40/s)')
  assert.equal(p720.ratesByResolution['4k'], '$4.80 ($0.60/s)')
  assert.ok(p720.formattedSummary.includes('$3.20 Total ($0.40/sec × 8s clip)'))

  // 2. 1080p at 8s duration: $0.40/s * 8 = $3.20
  const p1080 = resolveVideoPricing(veoStandard, '1080p', 8, 1)
  assert.equal(p1080.rateForClip, 3.20)

  // 3. 4k at 8s duration: $0.60/s * 8 = $4.80
  const p4k = resolveVideoPricing(veoStandard, '4k', 8, 1)
  assert.equal(p4k.rateForClip, 4.80)
  assert.equal(p4k.totalPrice, 4.80)

  // 4. Scaling across clip count (e.g. 2 clips at 720p 8s = $6.40)
  const p720_2clips = resolveVideoPricing(veoStandard, '720p', 8, 2)
  assert.equal(p720_2clips.totalPrice, 6.40)
  assert.ok(p720_2clips.formattedSummary.includes('$6.40 Total ($0.40/sec × 8s × 2 clips)'))
})

test('resolveVideoPricing correctly calculates Veo Fast snapshot pricing (.10/.12/.30 per second)', () => {
  // Requirement: Veo Fast (.10/.12/.30) calculates unit and total pricing accurately.
  // Threat: Drift in Fast pricing rates.
  // Boundary: resolveVideoPricing with Veo Fast line fixtures.
  const veoFast: TaskModalModelOption = {
    id: 'veo-3.1-fast-generate-preview',
    label: 'Veo 3.1 Fast',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        currency: 'USD',
        lines: [
          { billable: 'video_output', unit: 'second', unit_per: 1, price_usd: 0.10, conditions: { resolution: '720p', includes_audio: true } },
          { billable: 'video_output', unit: 'second', unit_per: 1, price_usd: 0.12, conditions: { resolution: '1080p', includes_audio: true } },
          { billable: 'video_output', unit: 'second', unit_per: 1, price_usd: 0.30, conditions: { resolution: '4k', includes_audio: true } },
        ],
      },
    },
  }

  const p720 = resolveVideoPricing(veoFast, '720p', 8, 1)
  assert.equal(p720.rateForClip, 0.80)
  assert.equal(p720.ratePerSec, 0.10)

  const p1080 = resolveVideoPricing(veoFast, '1080p', 8, 1)
  assert.equal(p1080.rateForClip, 0.96)
  assert.equal(p1080.ratePerSec, 0.12)

  const p4k = resolveVideoPricing(veoFast, '4k', 8, 1)
  assert.equal(p4k.rateForClip, 2.40)
  assert.equal(p4k.ratePerSec, 0.30)
})

test('resolveVideoPricing correctly calculates Veo Lite (.05/.08 and marks 4k unavailable)', () => {
  // Requirement: Veo Lite supports 720p and 1080p; 4k must be marked Unavailable.
  // Threat: Exposing unconfigured 4k pricing on models that do not offer 4k.
  // Boundary: resolveVideoPricing resolution matching against candidate lines and generationOptions.
  const veoLite: TaskModalModelOption = {
    id: 'veo-3.1-lite-generate-preview',
    label: 'Veo 3.1 Lite',
    ready: true,
    generationOptions: {
      resolutions: ['720p', '1080p'],
    },
    pricing: {
      billing: {
        status: 'verified',
        currency: 'USD',
        lines: [
          { billable: 'video_output', unit: 'second', unit_per: 1, price_usd: 0.05, conditions: { resolution: '720p', includes_audio: true } },
          { billable: 'video_output', unit: 'second', unit_per: 1, price_usd: 0.08, conditions: { resolution: '1080p', includes_audio: true } },
        ],
      },
    },
  }

  const p720 = resolveVideoPricing(veoLite, '720p', 8, 1)
  assert.equal(p720.isVerified, true)
  assert.equal(p720.rateForClip, 0.40)
  assert.equal(p720.ratesByResolution['720p'], '$0.40 ($0.05/s)')
  assert.equal(p720.ratesByResolution['1080p'], '$0.64 ($0.08/s)')
  assert.equal(p720.ratesByResolution['4k'], 'Unavailable')

  const p4k = resolveVideoPricing(veoLite, '4k', 8, 1)
  assert.equal(p4k.isVerified, false)
  assert.equal(p4k.totalPrice, undefined)
  assert.ok(p4k.formattedSummary.includes('Pricing unavailable'))
})

test('resolveVideoPricing supports 360p resolution for models supporting 360p (Omni)', () => {
  // Requirement: Models supporting 360p (like Gemini Omni 1.1) must match 360p billing lines.
  // Threat: 360p resolution dropped or unrecognized, returning unavailable despite valid catalog lines.
  // Boundary: normalizeVideoResKey and matchCandidateForRes supporting 360p.
  const omniWith360p: TaskModalModelOption = {
    id: 'gemini-omni-1.1-flash',
    label: 'Gemini Omni',
    ready: true,
    generationOptions: {
      resolutions: ['360p', '720p'],
    },
    pricing: {
      billing: {
        status: 'verified',
        currency: 'USD',
        lines: [
          { billable: 'video_output', unit: 'video', unit_per: 1, price_usd: 0.02, conditions: { resolution: '360p' } },
          { billable: 'video_output', unit: 'video', unit_per: 1, price_usd: 0.04, conditions: { resolution: '720p' } },
        ],
      },
    },
  }
  const result = resolveVideoPricing(omniWith360p, '360p', 0, 1)
  assert.equal(result.isVerified, true)
  assert.equal(result.fixedPrice, 0.02)
  assert.equal(result.totalPrice, 0.02)
  assert.equal(result.ratesByResolution['360p'], '$0.02/clip')
})

test('resolveVideoPricing marks Gemini Omni with unknown duration as unavailable when duration-dependent', () => {
  // Requirement: Models with no video billing lines or unknown duration must report unknown pricing.
  // Threat: Stale defaults or $0 quotes for unpriced multimodal models.
  // Boundary: resolveVideoPricing with empty lines array.
  const geminiOmni: TaskModalModelOption = {
    id: 'gemini-omni-1.1-flash',
    label: 'Gemini Omni',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        currency: 'USD',
        lines: [],
      },
    },
  }
  const result = resolveVideoPricing(geminiOmni, '1080p', 8, 1)
  assert.equal(result.isVerified, false)
  assert.equal(result.priceStatus, 'unknown')
  assert.equal(result.totalPrice, undefined)
  assert.equal(result.ratesByResolution['720p'], 'Unavailable')
  assert.equal(result.ratesByResolution['1080p'], 'Unavailable')
  assert.equal(result.ratesByResolution['4k'], 'Unavailable')
})

test('resolveAllowedVideoDurations returns empty array on undefined or missing metadata (no invented durations)', () => {
  // Requirement: Unavailable generation metadata must yield [] for durations, never invented 4,6,8.
  // Threat: Displaying arbitrary duration selectors when the model does not document durations.
  // Boundary: resolveAllowedVideoDurations fail-closed check.
  assert.deepEqual(resolveAllowedVideoDurations(undefined), [])
  assert.deepEqual(resolveAllowedVideoDurations({}), [])
})

test('resolveAllowedVideoDurations enforces resolution-dependent rules', () => {
  // Requirement: Resolution-specific duration restrictions must be strictly enforced.
  // Threat: Allowing 4s or 6s duration on 1080p when the model requires 8s.
  // Boundary: resolveAllowedVideoDurations resolution_durations map check.
  const genOptions = {
    aspect_ratios: ['16:9', '9:16'],
    resolutions: ['720p', '1080p', '4k'],
    durations: [4, 6, 8],
    resolution_durations: {
      '720p': [4, 6, 8],
      '1080p': [8],
      '4k': [8],
    },
  }

  const durs1080 = resolveAllowedVideoDurations(genOptions, '1080p')
  assert.deepEqual(durs1080, [8])

  const durs720 = resolveAllowedVideoDurations(genOptions, '720p')
  assert.deepEqual(durs720, [4, 6, 8])

  const omniOptions = {
    aspect_ratios: ['16:9'],
    resolutions: ['360p'],
    durations: [],
  }
  const dursOmni = resolveAllowedVideoDurations(omniOptions, '360p')
  assert.deepEqual(dursOmni, [])
})

test('resolveAllowedVideoResolutions returns empty array on undefined (no invented resolutions)', () => {
  // Requirement: Unavailable generation metadata must yield [] for resolutions, never invented 720p/1080p/4k.
  // Threat: Presenting unconfigured resolutions to users.
  // Boundary: resolveAllowedVideoResolutions fail-closed check.
  assert.deepEqual(resolveAllowedVideoResolutions(undefined), [])
  assert.deepEqual(resolveAllowedVideoResolutions({}), [])
  assert.deepEqual(resolveAllowedVideoResolutions({ resolutions: ['360p', '720p'] }), ['360p', '720p'])
})

test('resolveAllowedVideoAspectRatios returns empty array on undefined (no invented ratios)', () => {
  // Requirement: Unavailable generation metadata must yield [] for aspect ratios, never invented 16:9/9:16.
  // Threat: Forcing 16:9 defaults onto models that do not advertise aspect ratio capabilities.
  // Boundary: resolveAllowedVideoAspectRatios fail-closed check.
  assert.deepEqual(resolveAllowedVideoAspectRatios(undefined), [])
  assert.deepEqual(resolveAllowedVideoAspectRatios({}), [])
  assert.deepEqual(resolveAllowedVideoAspectRatios({ aspect_ratios: ['16:9'] }), ['16:9'])
})

test('validateVideoAttachment fails closed when initial_image is unsupported or metadata missing', () => {
  // Requirement: Models without initial_image.supported = true must reject any attachment.
  // Threat: Sending image payloads to video models that do not accept visual keyframes.
  // Boundary: validateVideoAttachment fail-closed check on genOptions.initial_image.
  assert.equal(validateVideoAttachment({ name: 'shot.png', type: 'image/png' }, undefined).valid, false)
  assert.equal(validateVideoAttachment({ name: 'shot.png', type: 'image/png' }, {}).valid, false)
  assert.equal(validateVideoAttachment({ name: 'shot.png', type: 'image/png' }, { initial_image: { supported: false } }).valid, false)
})

test('validateVideoAttachment allows only locally decoded PNG, JPEG, and WebP images', () => {
  // Requirement: Only PNG, JPEG, and WebP raster images are allowed as video keyframe references.
  // Threat: Passing unsupported codecs or corrupt binary inputs.
  // Boundary: validateVideoAttachment raster format check.
  const validModel = { initial_image: { supported: true } }
  assert.equal(validateVideoAttachment({ name: 'shot.png', type: 'image/png' }, validModel).valid, true)
  assert.equal(validateVideoAttachment({ name: 'photo.jpg', type: 'image/jpeg' }, validModel).valid, true)
  assert.equal(validateVideoAttachment({ name: 'photo.jpeg', type: 'image/jpeg' }, validModel).valid, true)
  assert.equal(validateVideoAttachment({ name: 'frame.webp', type: 'image/webp' }, validModel).valid, true)
})

test('validateVideoAttachment rejects SVG and HEIC/HEIF images', () => {
  // Requirement: SVG vector files and HEIC/HEIF camera containers must be rejected.
  // Threat: Failing backend rasterization or corrupting model pipelines with un-decoded formats.
  // Boundary: validateVideoAttachment explicit SVG/HEIC rejection.
  const validModel = { initial_image: { supported: true } }
  assert.equal(validateVideoAttachment({ name: 'diagram.svg', type: 'image/svg+xml' }, validModel).valid, false)
  assert.equal(validateVideoAttachment({ name: 'photo.heic', type: 'image/heic' }, validModel).valid, false)
  assert.equal(validateVideoAttachment({ name: 'photo.heif', type: 'image/heif' }, validModel).valid, false)
})

test('validateVideoAttachment rejects fake non-image MIME named .png', () => {
  // Requirement: A file with a non-image MIME type named with an image extension must be rejected.
  // Threat: Extension spoofing concealing text or binary payloads as .png.
  // Boundary: validateVideoAttachment dual extension and MIME validation.
  const validModel = { initial_image: { supported: true } }
  const result = validateVideoAttachment({ name: 'payload.png', type: 'text/plain' }, validModel)
  assert.equal(result.valid, false)
  assert.ok(result.error?.includes('Unsupported MIME type'))
})

test('validateVideoAttachment rejects non-image filename extension with image MIME', () => {
  // Requirement: Non-image file extensions must be rejected even if MIME claims image.
  // Threat: Executable scripts or archive files mislabeled as images.
  // Boundary: validateVideoAttachment extension allowlist check.
  const validModel = { initial_image: { supported: true } }
  const result = validateVideoAttachment({ name: 'script.sh', type: 'image/png' }, validModel)
  assert.equal(result.valid, false)
  assert.ok(result.error?.includes('Unsupported file extension'))
})

test('validateVideoAttachment enforces model supported_mime_types when specified', () => {
  // Requirement: If the model generationOptions specifies supported_mime_types, attachment must conform.
  // Threat: Passing WebP to a model that only accepts image/png.
  // Boundary: validateVideoAttachment supported_mime_types check.
  const pngOnlyModel = {
    initial_image: {
      supported: true,
      supported_mime_types: ['image/png'],
    },
  }
  assert.equal(validateVideoAttachment({ name: 'shot.png', type: 'image/png' }, pngOnlyModel).valid, true)
  const webpRes = validateVideoAttachment({ name: 'shot.webp', type: 'image/webp' }, pngOnlyModel)
  assert.equal(webpRes.valid, false)
  assert.ok(webpRes.error?.includes('not supported by the selected video model'))
})

test('resolveQualifiedVideoModel qualifies with option.provider/model and avoids doubled prefixes', () => {
  // Requirement: Returns provider-qualified model string using provider/model, avoiding doubled prefixes.
  // Threat: Emitting un-routed bare model IDs or invalid openrouter/openrouter/... prefixes.
  // Boundary: resolveQualifiedVideoModel prefix normalization.
  const googleOpt: TaskModalModelOption = {
    id: 'veo-3.1-generate-preview',
    label: 'Veo 3.1',
    ready: true,
    model: 'veo-3.1-generate-preview',
    provider: 'google',
  }
  assert.equal(resolveQualifiedVideoModel(googleOpt), 'google/veo-3.1-generate-preview')

  const openRouterOpt: TaskModalModelOption = {
    id: 'google/veo-3.1',
    label: 'Google: Veo 3.1',
    ready: true,
    model: 'google/veo-3.1',
    provider: 'openrouter',
  }
  assert.equal(resolveQualifiedVideoModel(openRouterOpt), 'openrouter/google/veo-3.1')

  // Avoid doubled openrouter/ prefix
  const alreadyPrefixedOpt: TaskModalModelOption = {
    id: 'openrouter/google/veo-3.1',
    label: 'Google: Veo 3.1',
    ready: true,
    model: 'openrouter/google/veo-3.1',
    provider: 'openrouter',
  }
  assert.equal(resolveQualifiedVideoModel(alreadyPrefixedOpt), 'openrouter/google/veo-3.1')

  // Avoid doubled google/ prefix
  const googleAlreadyPrefixed: TaskModalModelOption = {
    id: 'google/veo-3.1',
    label: 'Veo 3.1',
    ready: true,
    model: 'google/veo-3.1',
    provider: 'google',
  }
  assert.equal(resolveQualifiedVideoModel(googleAlreadyPrefixed), 'google/veo-3.1')

  // Avoid doubled google: colon prefix
  const colonPrefixed: TaskModalModelOption = {
    id: 'google:veo-3.1',
    label: 'Veo 3.1',
    ready: true,
    model: 'google:veo-3.1',
    provider: 'google',
  }
  assert.equal(resolveQualifiedVideoModel(colonPrefixed), 'google:veo-3.1')

  assert.equal(resolveQualifiedVideoModel(undefined, 'fallback-model'), 'fallback-model')
})

test('resolveVideoPricing verifies against actual pinned snapshot data from snapshot.json', (t) => {
  // Requirement: Pricing resolution must be verified against actual pinned snapshot data from disk.
  // Threat: Inventing synthetic models or verifying against stale unpinned fixtures.
  // Boundary: Reading swarmd/internal/model/snapshotdata/snapshot.json via node:fs.
  const snapshotPath = path.resolve(__dirname, '../../../../../swarmd/internal/model/snapshotdata/snapshot.json')
  if (!fs.existsSync(snapshotPath)) {
    t.skip('snapshot.json not found on disk')
    return
  }

  const raw = fs.readFileSync(snapshotPath, 'utf8')
  const snapshot = JSON.parse(raw)
  assert.ok(snapshot.snapshot_id, 'Snapshot must have a snapshot_id')
  assert.ok(Array.isArray(snapshot.models), 'Snapshot must have models array')

  // Find actual Google Veo model in snapshot
  const veo = snapshot.models.find(
    (m: any) => m.model_id === 'veo-3.1-generate-preview' || m.model === 'veo-3.1-generate-preview'
  )

  if (veo && veo.pricing) {
    const rawPricing = typeof veo.pricing === 'string' ? JSON.parse(veo.pricing) : veo.pricing
    const modelOpt: TaskModalModelOption = {
      id: veo.model_id || 'veo-3.1-generate-preview',
      label: veo.display_name || 'Veo 3.1',
      ready: true,
      provider: veo.provider_id || 'google',
      model: veo.model_id || 'veo-3.1-generate-preview',
      pricing: rawPricing,
    }

    // If pricing has verified status, resolveVideoPricing must verify; if unverified, must reject
    const status = (rawPricing.billing?.status || '').toLowerCase()
    const result = resolveVideoPricing(modelOpt, '720p', 8, 1)

    if (status === 'verified' || status === 'partially_verified') {
      assert.equal(result.isVerified, true, 'Verified snapshot record must resolve as verified')
      assert.equal(result.priceStatus, 'verified')
      assert.ok(result.totalPrice !== undefined && result.totalPrice > 0)
    } else {
      assert.equal(result.isVerified, false, 'Unverified snapshot record must not invent verified status')
      assert.equal(result.priceStatus, 'unknown')
      assert.equal(result.totalPrice, undefined)
    }
  }
})
