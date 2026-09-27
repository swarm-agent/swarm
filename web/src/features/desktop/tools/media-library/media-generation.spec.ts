import assert from 'node:assert/strict'
import test from 'node:test'
import {
  calculateGenerationCost,
  extractBillingLines,
  normalizeResKey,
  resolveInitialModel,
  resolveInitialSetting,
} from './media-generation'
import type { MediaCatalogModelOption } from '../../settings/media/queries/get-media-settings'

test('normalizeResKey normalizes common resolution strings properly', () => {
  assert.equal(normalizeResKey('1024x1024'), '1k')
  assert.equal(normalizeResKey(' standard '), '1k')
  assert.equal(normalizeResKey('2048x2048'), '2k')
  assert.equal(normalizeResKey('HD'), '2k')
  assert.equal(normalizeResKey('4096x4096'), '4k')
  assert.equal(normalizeResKey('720p'), '720p')
  assert.equal(normalizeResKey('1280x720'), '720p')
  assert.equal(normalizeResKey('1080p'), '1080p')
  assert.equal(normalizeResKey('1920x1080'), '1080p')
})

test('extractBillingLines strictly allows image/second/video/clip, divides quantity > 0, and rejects tokens/invalid lines', () => {
  const pricing = {
    billing: {
      status: 'verified',
      lines: [
        { billable: 'image_output', unit: 'image', price_usd: 0.03, conditions: { resolution: '1k' } },
        { billable: 'image_output', unit: 'image', price_usd: 30, quantity: 1000, conditions: { resolution: '2k' } },
        { billable: 'image_output', unit: 'token', price_usd: 0.00003 }, // Token unit MUST be rejected
        { billable: 'image_output', unit: 'image', price_usd: 0, conditions: { resolution: '1k' } }, // Zero price is valid finite
        { billable: 'image_output', unit: 'image', price_usd: -5 }, // Negative price rejected
        { billable: 'image_output', unit: 'image', price_usd: 10, quantity: 0 }, // Non-positive quantity rejected
        { billable: 'video_output', unit: 'second', price_usd: 0.05, conditions: { resolution: '720p' } },
        { billable: 'video_output', unit: 'clip', price_usd: 1.20 },
        null,
      ],
    },
  }
  const lines = extractBillingLines(pricing)
  // Expected valid lines:
  // 1: 0.03 (qty 1)
  // 2: 30 / 1000 = 0.03 (qty 1000)
  // 3: 0.00 (qty 1)
  // 4: 0.05 (second)
  // 5: 1.20 (clip)
  assert.equal(lines.length, 5)
  assert.equal(lines[0].priceUsd, 0.03)
  assert.equal(lines[0].unit, 'image')
  assert.equal(lines[1].priceUsd, 0.03)
  assert.equal(lines[1].quantity, 1000)
  assert.equal(lines[2].priceUsd, 0)
  assert.equal(lines[3].unit, 'second')
  assert.equal(lines[3].priceUsd, 0.05)
  assert.equal(lines[4].unit, 'clip')
  assert.equal(lines[4].priceUsd, 1.20)
})

test('calculateGenerationCost returns unavailable when pricing metadata is missing, empty, or token-only', () => {
  // 1. Missing modelOption
  const res1 = calculateGenerationCost({
    modelOption: undefined,
    action: 'fine_tune',
    count: 1,
    settings: {},
  })
  assert.equal(res1.isAvailable, false)
  assert.equal(res1.formattedTotal, 'Pricing unavailable')

  // 2. Empty pricing object
  const res2 = calculateGenerationCost({
    modelOption: {
      id: 'test-model',
      provider: 'google',
      model: 'test-model',
      display_name: 'Test Model',
      kind: 'image_generation',
      ready: true,
      pricing: {},
    },
    action: 'fine_tune',
    count: 1,
    settings: {},
  })
  assert.equal(res2.isAvailable, false)

  // 3. Token-only pricing (e.g. unit 'token') MUST NOT be fabricated as flat image price
  const res3 = calculateGenerationCost({
    modelOption: {
      id: 'token-model',
      provider: 'google',
      model: 'token-model',
      display_name: 'Token Model',
      kind: 'image_generation',
      ready: true,
      pricing: {
        billing: {
          status: 'verified',
          lines: [
            { billable: 'image_output', unit: 'token', price_usd: 0.00003 },
          ],
        },
      },
    },
    action: 'fine_tune',
    count: 1,
    settings: {},
  })
  assert.equal(res3.isAvailable, false)
  assert.equal(res3.formattedTotal, 'Pricing unavailable')
})

test('calculateGenerationCost handles resolution mismatch without arbitrary fallback', () => {
  const modelOption: MediaCatalogModelOption = {
    id: 'imagen-3',
    provider: 'google',
    model: 'imagen-3.0',
    display_name: 'Imagen 3',
    kind: 'image_generation',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        lines: [
          { billable: 'image_output', unit: 'image', price_usd: 0.03, conditions: { resolution: '1k' } },
          { billable: 'image_output', unit: 'image', price_usd: 0.06, conditions: { resolution: '2k' } },
        ],
      },
    },
  }

  // Requesting 4k resolution when model metadata only declares 1k and 2k
  // Must NOT arbitrarily fall back to the 1k line!
  const mismatch = calculateGenerationCost({
    modelOption,
    action: 'fine_tune',
    count: 1,
    settings: { resolution: '4k' },
  })
  assert.equal(mismatch.isAvailable, false)
  assert.equal(mismatch.formattedTotal, 'Pricing unavailable')

  // Requesting matching 2k resolution matches accurately
  const match2k = calculateGenerationCost({
    modelOption,
    action: 'fine_tune',
    count: 1,
    settings: { resolution: '2k' },
  })
  assert.equal(match2k.isAvailable, true)
  assert.equal(match2k.unitPrice, 0.06)
  assert.equal(match2k.totalPrice, 0.06)
})

test('calculateGenerationCost scales billing quantity and multi-output requests correctly', () => {
  const modelOption: MediaCatalogModelOption = {
    id: 'batch-model',
    provider: 'google',
    model: 'batch-model',
    display_name: 'Batch Model',
    kind: 'image_generation',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        lines: [
          { billable: 'image_output', unit: 'image', price_usd: 50, quantity: 1000, conditions: { resolution: '1k' } },
        ],
      },
    },
  }

  const res = calculateGenerationCost({
    modelOption,
    action: 'iterate',
    count: 4,
    settings: { resolution: '1k' },
  })
  assert.equal(res.isAvailable, true)
  assert.equal(res.unitPrice, 0.05) // 50 / 1000 = 0.05 per image
  assert.equal(res.totalPrice, 0.20) // 0.05 * 4 = 0.20
  assert.equal(res.formattedTotal, '$0.20')
})

test('calculateGenerationCost rejects lines with unknown or non-standard conditions', () => {
  const modelOption: MediaCatalogModelOption = {
    id: 'unknown-cond-model',
    provider: 'google',
    model: 'unknown-cond',
    display_name: 'Unknown Cond Model',
    kind: 'image_generation',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        lines: [
          // Line with unknown condition key
          { billable: 'image_output', unit: 'image', price_usd: 0.01, conditions: { resolution: '1k', unsupported_custom_option: true } },
          // Line with non-standard service tier
          { billable: 'image_output', unit: 'image', price_usd: 0.01, conditions: { resolution: '1k', service_tier: 'flex' } },
        ],
      },
    },
  }

  const res = calculateGenerationCost({
    modelOption,
    action: 'fine_tune',
    count: 1,
    settings: { resolution: '1k' },
  })
  assert.equal(res.isAvailable, false)
})

test('calculateGenerationCost treats zero price as valid finite price and rejects negative prices', () => {
  // Free / zero price model
  const freeModel: MediaCatalogModelOption = {
    id: 'free-model',
    provider: 'google',
    model: 'free-model',
    display_name: 'Free Model',
    kind: 'image_generation',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        lines: [
          { billable: 'image_output', unit: 'image', price_usd: 0, conditions: { resolution: '1k' } },
        ],
      },
    },
  }

  const freeRes = calculateGenerationCost({
    modelOption: freeModel,
    action: 'fine_tune',
    count: 4,
    settings: { resolution: '1k' },
  })
  assert.equal(freeRes.isAvailable, true)
  assert.equal(freeRes.unitPrice, 0)
  assert.equal(freeRes.totalPrice, 0)
  assert.equal(freeRes.formattedTotal, '$0.00')

  // Negative price model
  const negModel: MediaCatalogModelOption = {
    id: 'neg-model',
    provider: 'google',
    model: 'neg-model',
    display_name: 'Negative Model',
    kind: 'image_generation',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        lines: [
          { billable: 'image_output', unit: 'image', price_usd: -0.05, conditions: { resolution: '1k' } },
        ],
      },
    },
  }

  const negRes = calculateGenerationCost({
    modelOption: negModel,
    action: 'fine_tune',
    count: 1,
    settings: { resolution: '1k' },
  })
  assert.equal(negRes.isAvailable, false)
})

test('calculateGenerationCost requires explicit duration for second-based video and does not assume 8s', () => {
  const videoModel: MediaCatalogModelOption = {
    id: 'veo-3.1',
    provider: 'google',
    model: 'veo-3.1',
    display_name: 'Veo 3.1',
    kind: 'video_generation',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        lines: [
          { billable: 'video_output', unit: 'second', price_usd: 0.05, conditions: { resolution: '720p' } },
        ],
      },
    },
  }

  // Missing duration: must NOT assume 8 seconds! Price is unavailable.
  const missingDur = calculateGenerationCost({
    modelOption: videoModel,
    action: 'to_video',
    count: 1,
    settings: { resolution: '720p' }, // No durationSeconds provided
  })
  assert.equal(missingDur.isAvailable, false)

  // With explicit duration: calculates accurately
  const explicitDur = calculateGenerationCost({
    modelOption: videoModel,
    action: 'to_video',
    count: 2,
    settings: { resolution: '720p', durationSeconds: 6 },
  })
  assert.equal(explicitDur.isAvailable, true)
  assert.equal(explicitDur.unitPrice, 0.05)
  // 0.05 * 6s = 0.30 per clip * 2 clips = 0.60
  assert.equal(explicitDur.totalPrice, 0.60)
  assert.equal(explicitDur.formattedTotal, '$0.60')
})

test('resolveInitialSetting only preselects source ratio/resolution/duration if in supported options', () => {
  // Aspect ratio in supported options with case normalization returning actual metadata spelling
  const ratioMatch = resolveInitialSetting(' 16:9 ', ['16:9', '9:16'], '16:9')
  assert.equal(ratioMatch, '16:9')

  // Aspect ratio not in supported options falls back to model default
  const ratioFallback = resolveInitialSetting('21:9', ['16:9', '1:1'], '16:9')
  assert.equal(ratioFallback, '16:9')

  // Aspect ratio when metadata is absent (empty/undefined supportedValues) returns undefined
  const ratioAbsent = resolveInitialSetting('16:9', undefined, undefined)
  assert.equal(ratioAbsent, undefined)

  // Resolution in supported options with normalization ('1024x1024' -> '1k')
  const resMatch = resolveInitialSetting('1024x1024', ['1k', '2k', '4k'], '1k')
  assert.equal(resMatch, '1k')

  // Resolution case normalized returning metadata casing
  const resCaseMatch = resolveInitialSetting('1K', ['1k', '2k'], '1k')
  assert.equal(resCaseMatch, '1k')

  // Resolution not in supported options falls back to default
  const resFallback = resolveInitialSetting('4k', ['1k', '2k'], '1k')
  assert.equal(resFallback, '1k')

  // Resolution when metadata absent returns undefined (no invented options)
  const resAbsent = resolveInitialSetting('1080p', [], '1080p')
  assert.equal(resAbsent, undefined)

  // Duration matching supported durations
  const durMatch = resolveInitialSetting(8, [4, 6, 8], 8)
  assert.equal(durMatch, 8)

  // Duration not in supported durations falls back to default
  const durFallback = resolveInitialSetting(15, [4, 6, 8], 6)
  assert.equal(durFallback, 6)

  // Duration when metadata absent returns undefined
  const durAbsent = resolveInitialSetting(8, undefined, undefined)
  assert.equal(durAbsent, undefined)
})

test('resolveInitialModel preselects source model if available in model list, else falls back to default', () => {
  const available: MediaCatalogModelOption[] = [
    { id: 'veo-3.1-generate-preview', provider: 'google', model: 'veo-3.1-generate-preview', display_name: 'Veo 3.1', kind: 'video_generation', ready: true },
    { id: 'gemini-omni-1.1-flash', provider: 'google', model: 'gemini-omni-1.1-flash', display_name: 'Gemini Omni', kind: 'video_generation', ready: true },
  ]

  // Source model matches
  const match = resolveInitialModel('gemini-omni-1.1-flash', available, 'veo-3.1-generate-preview')
  assert.equal(match, 'gemini-omni-1.1-flash')

  // Source model absent / not available falls back to default
  const fallback = resolveInitialModel('unknown-model', available, 'veo-3.1-generate-preview')
  assert.equal(fallback, 'veo-3.1-generate-preview')
})
