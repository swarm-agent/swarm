import test from 'node:test'
import assert from 'node:assert/strict'
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

// Purpose:
// - Requirement: Video pricing must be derived strictly from verified USD catalog billing lines,
//   eliminating hardcoded/guessed rates (.05/.08/.20). Clip count (1..8) and duration must scale
//   dynamically. Unknown or unverified models must be marked unavailable, not $0 or 8s.
// - Threat/regression: Presenting incorrect pricing estimates or charging unexpected amounts
//   due to guessed default rates or failure to respect model-specific constraints.
// - Boundary/authority: resolveVideoPricing in videoTaskSettings.ts consuming snapshot-backed pricing.
// - Test layer: Unit tests exercising snapshot fixtures for Standard, Fast, Lite, Omni, and negative cases.

test('resolveVideoPricing returns unavailable for undefined option or missing pricing without guessing', () => {
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

test('resolveVideoPricing returns unavailable for zero or negative duration', () => {
  const model: TaskModalModelOption = {
    id: 'veo-test',
    label: 'Veo Test',
    ready: true,
    pricing: {
      currency: 'USD',
      billing: {
        status: 'verified',
        lines: [
          { billable: 'video_output', unit: 'second', price_usd: 0.10, conditions: { resolution: '720p' } },
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

test('resolveVideoPricing rejects non-USD currencies as unavailable', () => {
  const eurModel: TaskModalModelOption = {
    id: 'veo-eur',
    label: 'Veo EUR',
    ready: true,
    pricing: {
      currency: 'EUR',
      billing: {
        status: 'verified',
        lines: [
          { billable: 'video_output', unit: 'second', price_usd: 0.10, conditions: { resolution: '720p' } },
        ],
      },
    },
  }
  const result = resolveVideoPricing(eurModel, '720p', 8, 1)
  assert.equal(result.isVerified, false)
  assert.equal(result.totalPrice, undefined)
  assert.ok(result.formattedSummary.includes('Unsupported currency EUR'))
})

test('resolveVideoPricing rejects unverified billing status without fallback', () => {
  const unverifiedModel: TaskModalModelOption = {
    id: 'veo-unverified',
    label: 'Veo Unverified',
    ready: true,
    pricing: {
      currency: 'USD',
      billing: {
        status: 'unverified',
        lines: [
          { billable: 'video_output', unit: 'second', price_usd: 0.05, conditions: { resolution: '720p' } },
        ],
      },
    },
  }
  const result = resolveVideoPricing(unverifiedModel, '720p', 8, 1)
  assert.equal(result.isVerified, false)
  assert.equal(result.totalPrice, undefined)
  assert.ok(result.formattedSummary.includes('Pricing unavailable'))
})

test('resolveVideoPricing correctly calculates Veo Standard snapshot pricing (.40/.40/.60 per second)', () => {
  const veoStandard: TaskModalModelOption = {
    id: 'veo-3.1-generate-preview',
    label: 'Veo 3.1 Standard',
    ready: true,
    pricing: {
      currency: 'USD',
      billing: {
        status: 'verified',
        lines: [
          { billable: 'video_output', unit: 'second', price_usd: 0.40, conditions: { resolution: '720p', includes_audio: true, service_tier: 'standard' } },
          { billable: 'video_output', unit: 'second', price_usd: 0.40, conditions: { resolution: '1080p', includes_audio: true, service_tier: 'standard' } },
          { billable: 'video_output', unit: 'second', price_usd: 0.60, conditions: { resolution: '4k', includes_audio: true, service_tier: 'standard' } },
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
  const veoFast: TaskModalModelOption = {
    id: 'veo-3.1-fast-generate-preview',
    label: 'Veo 3.1 Fast',
    ready: true,
    pricing: {
      currency: 'USD',
      billing: {
        status: 'verified',
        lines: [
          { billable: 'video_output', unit: 'second', price_usd: 0.10, conditions: { resolution: '720p', includes_audio: true } },
          { billable: 'video_output', unit: 'second', price_usd: 0.12, conditions: { resolution: '1080p', includes_audio: true } },
          { billable: 'video_output', unit: 'second', price_usd: 0.30, conditions: { resolution: '4k', includes_audio: true } },
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
  const veoLite: TaskModalModelOption = {
    id: 'veo-3.1-lite-generate-preview',
    label: 'Veo 3.1 Lite',
    ready: true,
    pricing: {
      currency: 'USD',
      billing: {
        status: 'verified',
        lines: [
          { billable: 'video_output', unit: 'second', price_usd: 0.05, conditions: { resolution: '720p', includes_audio: true } },
          { billable: 'video_output', unit: 'second', price_usd: 0.08, conditions: { resolution: '1080p', includes_audio: true } },
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

  // If user requests 4k on Lite, pricing must be unavailable
  const p4k = resolveVideoPricing(veoLite, '4k', 8, 1)
  assert.equal(p4k.isVerified, false)
  assert.equal(p4k.totalPrice, undefined)
  assert.ok(p4k.formattedSummary.includes('Pricing unavailable'))
})

test('resolveVideoPricing marks Gemini Omni with unknown duration as unavailable', () => {
  const geminiOmni: TaskModalModelOption = {
    id: 'gemini-omni-1.1-flash',
    label: 'Gemini Omni',
    ready: true,
    pricing: {
      currency: 'USD',
      billing: {
        status: 'verified',
        lines: [], // No video_output duration lines
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

test('resolveAllowedVideoDurations enforces resolution-dependent rules', () => {
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

  // 1080p resolution strictly enforces 8s duration
  const durs1080 = resolveAllowedVideoDurations(genOptions, '1080p')
  assert.deepEqual(durs1080, [8])

  // 720p resolution allows 4s, 6s, 8s
  const durs720 = resolveAllowedVideoDurations(genOptions, '720p')
  assert.deepEqual(durs720, [4, 6, 8])

  // Omni with no durations returns empty array
  const omniOptions = {
    aspect_ratios: ['16:9'],
    resolutions: ['1080p'],
    durations: [],
  }
  const dursOmni = resolveAllowedVideoDurations(omniOptions, '1080p')
  assert.deepEqual(dursOmni, [])
})

test('resolveAllowedVideoResolutions extracts model resolutions or standard fallbacks', () => {
  const liteOptions = {
    resolutions: ['720p', '1080p'], // No 4k
  }
  assert.deepEqual(resolveAllowedVideoResolutions(liteOptions), ['720p', '1080p'])

  assert.deepEqual(resolveAllowedVideoResolutions(undefined), ['720p', '1080p', '4k'])
})

test('resolveAllowedVideoAspectRatios extracts model aspect ratios', () => {
  const customRatios = {
    aspect_ratios: ['16:9'],
  }
  assert.deepEqual(resolveAllowedVideoAspectRatios(customRatios), ['16:9'])
  assert.deepEqual(resolveAllowedVideoAspectRatios(undefined), ['16:9', '9:16'])
})

test('validateVideoAttachment enforces image-only reference files', () => {
  // Supported images
  assert.equal(validateVideoAttachment({ name: 'shot.png', type: 'image/png' }).valid, true)
  assert.equal(validateVideoAttachment({ name: 'frame.jpg', type: 'image/jpeg' }).valid, true)
  assert.equal(validateVideoAttachment({ name: 'keyframe.webp', type: 'image/webp' }).valid, true)

  // Non-images are strictly rejected
  const textDoc = validateVideoAttachment({ name: 'spec.md', type: 'text/markdown', kind: 'doc' })
  assert.equal(textDoc.valid, false)
  assert.ok(textDoc.error?.includes('Unsupported'))

  const videoFile = validateVideoAttachment({ name: 'clip.mp4', type: 'video/mp4', kind: 'video' })
  assert.equal(videoFile.valid, false)

  const audioFile = validateVideoAttachment({ name: 'track.wav', type: 'audio/wav', kind: 'audio' })
  assert.equal(audioFile.valid, false)

  // Rejection if model does not support initial image
  const noImageModel = {
    initial_image: { supported: false },
  }
  const rejected = validateVideoAttachment({ name: 'shot.png', type: 'image/png' }, noImageModel)
  assert.equal(rejected.valid, false)
  assert.ok(rejected.error?.includes('does not support initial image'))
})

test('resolveQualifiedVideoModel resolves canonical model ID', () => {
  const opt: TaskModalModelOption = {
    id: 'veo-3.1-generate-preview',
    label: 'Veo 3.1',
    ready: true,
    model: 'veo-3.1-generate-preview',
    provider: 'google',
  }
  assert.equal(resolveQualifiedVideoModel(opt), 'veo-3.1-generate-preview')

  const orOpt: TaskModalModelOption = {
    id: 'google/veo-3.1',
    label: 'Google: Veo 3.1',
    ready: true,
    model: 'google/veo-3.1',
    provider: 'openrouter',
  }
  assert.equal(resolveQualifiedVideoModel(orOpt), 'google/veo-3.1')
  assert.equal(resolveQualifiedVideoModel(undefined, 'fallback-model'), 'fallback-model')
})
