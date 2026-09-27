import assert from 'node:assert/strict'
import test from 'node:test'
import {
  calculateGenerationCost,
  extractBillingLines,
  getLineResolution,
  normalizeResKey,
  resolveAudioContext,
  resolveInitialModel,
  resolveInitialSetting,
} from './media-generation'
import type { MediaCatalogModelOption } from '../../settings/media/queries/get-media-settings'

test('normalizeResKey normalizes common resolution strings properly', () => {
  assert.equal(normalizeResKey('1024x1024'), '1k')
  assert.equal(normalizeResKey(' standard '), '1k')
  assert.equal(normalizeResKey('up_to_1024x1024'), '1k')
  assert.equal(normalizeResKey('up to 1024x1024'), '1k')
  assert.equal(normalizeResKey('1K'), '1k')
  assert.equal(normalizeResKey('2048x2048'), '2k')
  assert.equal(normalizeResKey('HD'), '2k')
  assert.equal(normalizeResKey('up_to_2048x2048'), '2k')
  assert.equal(normalizeResKey('2K'), '2k')
  assert.equal(normalizeResKey('4096x4096'), '4k')
  assert.equal(normalizeResKey('4K'), '4k')
  assert.equal(normalizeResKey('720p'), '720p')
  assert.equal(normalizeResKey('1280x720'), '720p')
  assert.equal(normalizeResKey('1080p'), '1080p')
  assert.equal(normalizeResKey('1920x1080'), '1080p')
})

test('extractBillingLines strictly allows image/second/video/clip, divides quantity > 0, and rejects tokens/inputs/invalid lines', () => {
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
        { billable: 'image_input', unit: 'image', price_usd: 0.005 }, // Input rate MUST be rejected
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

test('calculateGenerationCost accurately prices real Google Gemini image equivalent_cost with output_tokens and up_to_1024x1024 variant', () => {
  // Real snapshot shape from snapshotdata/snapshot.json for gemini-2.5-flash-image
  const geminiImageModel: MediaCatalogModelOption = {
    id: 'gemini-2.5-flash-image',
    provider: 'google',
    model: 'gemini-2.5-flash-image',
    display_name: 'Nano Banana',
    kind: 'image_generation',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        lines: [
          {
            kind: 'billing_rate',
            billable: 'text_input',
            unit: 'million_tokens',
            price_usd: 0.3,
            variant: 'standard',
            conditions: { tier: 'paid', service_tier: 'standard' },
          },
          {
            kind: 'billing_rate',
            billable: 'image_input',
            unit: 'million_tokens',
            price_usd: 0.3,
            variant: 'standard',
            conditions: { tier: 'paid', service_tier: 'standard' },
          },
          {
            kind: 'billing_rate',
            billable: 'image_output',
            unit: 'million_tokens',
            price_usd: 30,
            variant: 'standard',
            conditions: { tier: 'paid', service_tier: 'standard' },
          },
          {
            kind: 'equivalent_cost',
            billable: 'image_output',
            unit: 'image',
            price_usd: 0.039,
            variant: 'up_to_1024x1024',
            conditions: {
              tier: 'paid',
              service_tier: 'standard',
              output_tokens: 1290,
            },
          },
          {
            kind: 'equivalent_cost',
            billable: 'image_output',
            unit: 'image',
            price_usd: 0.0195,
            variant: 'up_to_1024x1024',
            conditions: {
              tier: 'paid',
              service_tier: 'batch',
            },
          },
        ],
      },
    },
  }

  // Matches 1k resolution against standard equivalent_cost line (ignoring batch line and tokens)
  const res1k = calculateGenerationCost({
    modelOption: geminiImageModel,
    action: 'fine_tune',
    count: 2,
    settings: { resolution: '1k' },
  })
  assert.equal(res1k.isAvailable, true)
  assert.equal(res1k.unitPrice, 0.039)
  assert.equal(res1k.totalPrice, 0.078)
  assert.equal(res1k.formattedPerUnit, '$0.039/image')
  assert.equal(res1k.formattedTotal, '$0.08')
  assert.equal(res1k.isVerified, true)

  // Resolution mismatch: model only declared up_to_1024x1024 ('1k')
  const res2k = calculateGenerationCost({
    modelOption: geminiImageModel,
    action: 'fine_tune',
    count: 1,
    settings: { resolution: '2k' },
  })
  assert.equal(res2k.isAvailable, false)
})

test('calculateGenerationCost accurately prices real Google Veo video with includes_audio:true and charged_only_on_success:true', () => {
  // Real snapshot shape from snapshotdata/snapshot.json for veo-3.1-generate-preview
  const veoModel: MediaCatalogModelOption = {
    id: 'veo-3.1-generate-preview',
    provider: 'google',
    model: 'veo-3.1-generate-preview',
    display_name: 'Veo 3.1',
    kind: 'video_generation',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        lines: [
          {
            kind: 'billing_rate',
            billable: 'video_output',
            unit: 'second',
            price_usd: 0.4,
            variant: '720p',
            conditions: {
              tier: 'paid',
              service_tier: 'standard',
              resolution: '720p',
              includes_audio: true,
              charged_only_on_success: true,
            },
          },
          {
            kind: 'billing_rate',
            billable: 'video_output',
            unit: 'second',
            price_usd: 0.4,
            variant: '1080p',
            conditions: {
              tier: 'paid',
              service_tier: 'standard',
              resolution: '1080p',
              includes_audio: true,
              charged_only_on_success: true,
            },
          },
          {
            kind: 'billing_rate',
            billable: 'video_output',
            unit: 'second',
            price_usd: 0.6,
            variant: '4K',
            conditions: {
              tier: 'paid',
              service_tier: 'standard',
              resolution: '4K',
              includes_audio: true,
              charged_only_on_success: true,
            },
          },
        ],
      },
    },
  }

  // 1. 720p video for 8 seconds, 1 clip
  const res720p = calculateGenerationCost({
    modelOption: veoModel,
    action: 'to_video',
    count: 1,
    settings: { resolution: '720p', durationSeconds: 8 },
  })
  assert.equal(res720p.isAvailable, true)
  assert.equal(res720p.unitPrice, 0.4)
  assert.equal(res720p.totalPrice, 3.20) // 0.4 * 8s = 3.20
  assert.equal(res720p.formattedPerUnit, '$0.400/sec ($3.20/clip)')
  assert.equal(res720p.formattedTotal, '$3.20')
  assert.equal(res720p.unitLabel, '8s clip')

  // 2. 1080p video for 6 seconds, 2 clips
  const res1080p = calculateGenerationCost({
    modelOption: veoModel,
    action: 'to_video',
    count: 2,
    settings: { resolution: '1080p', durationSeconds: 6 },
  })
  assert.equal(res1080p.isAvailable, true)
  assert.equal(res1080p.unitPrice, 0.4)
  assert.equal(res1080p.totalPrice, 4.80) // 0.4 * 6s = 2.40 * 2 = 4.80
  assert.equal(res1080p.formattedTotal, '$4.80')

  // 3. 4K video for 8 seconds, 1 clip (4K @ $0.60/s)
  const res4k = calculateGenerationCost({
    modelOption: veoModel,
    action: 'to_video',
    count: 1,
    settings: { resolution: '4k', durationSeconds: 8 },
  })
  assert.equal(res4k.isAvailable, true)
  assert.equal(res4k.unitPrice, 0.6)
  assert.equal(res4k.totalPrice, 4.80) // 0.6 * 8s = 4.80
  assert.equal(res4k.formattedTotal, '$4.80')
})

test('calculateGenerationCost matches image resolution from variant (e.g. variant:1K, 2K) when conditions.resolution is omitted', () => {
  const modelWithVariantRes: MediaCatalogModelOption = {
    id: 'variant-res-model',
    provider: 'google',
    model: 'variant-res-model',
    display_name: 'Variant Res Model',
    kind: 'image_generation',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        lines: [
          {
            kind: 'billing_rate',
            billable: 'image_output',
            unit: 'image',
            price_usd: 0.03,
            variant: '1K',
            conditions: { tier: 'paid', service_tier: 'standard' },
          },
          {
            kind: 'billing_rate',
            billable: 'image_output',
            unit: 'image',
            price_usd: 0.06,
            variant: '2K',
            conditions: { tier: 'paid', service_tier: 'standard' },
          },
        ],
      },
    },
  }

  // Requesting 1k matches 1K variant
  const res1k = calculateGenerationCost({
    modelOption: modelWithVariantRes,
    action: 'fine_tune',
    count: 1,
    settings: { resolution: '1k' },
  })
  assert.equal(res1k.isAvailable, true)
  assert.equal(res1k.unitPrice, 0.03)

  // Requesting 2k matches 2K variant
  const res2k = calculateGenerationCost({
    modelOption: modelWithVariantRes,
    action: 'fine_tune',
    count: 3,
    settings: { resolution: '2k' },
  })
  assert.equal(res2k.isAvailable, true)
  assert.equal(res2k.unitPrice, 0.06)
  assert.equal(res2k.totalPrice, 0.18)

  // Requesting 4k has no matching line
  const res4k = calculateGenerationCost({
    modelOption: modelWithVariantRes,
    action: 'fine_tune',
    count: 1,
    settings: { resolution: '4k' },
  })
  assert.equal(res4k.isAvailable, false)
})

test('calculateGenerationCost properly routes video iteration using model kind instead of treating it as image', () => {
  const videoModel: MediaCatalogModelOption = {
    id: 'veo-3.1-generate-preview',
    provider: 'google',
    model: 'veo-3.1-generate-preview',
    display_name: 'Veo 3.1',
    kind: 'video_generation',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        lines: [
          {
            kind: 'billing_rate',
            billable: 'video_output',
            unit: 'second',
            price_usd: 0.4,
            variant: '720p',
            conditions: {
              tier: 'paid',
              service_tier: 'standard',
              resolution: '720p',
              includes_audio: true,
              charged_only_on_success: true,
            },
          },
        ],
      },
    },
  }

  // action: 'iterate' with kind: 'video_generation' MUST be treated as video, NOT image!
  const iterRes = calculateGenerationCost({
    modelOption: videoModel,
    action: 'iterate',
    count: 3,
    settings: { resolution: '720p', durationSeconds: 8 },
  })
  assert.equal(iterRes.isAvailable, true)
  assert.equal(iterRes.unitPrice, 0.4)
  // 0.4 * 8s = 3.20 per clip * 3 clips = 9.60
  assert.equal(iterRes.totalPrice, 9.60)
  assert.equal(iterRes.formattedTotal, '$9.60')
  assert.equal(iterRes.unitLabel, '8s clip')
})

test('calculateGenerationCost rejects input billing rates (e.g. image_input, video_input)', () => {
  const inputOnlyModel: MediaCatalogModelOption = {
    id: 'input-only',
    provider: 'google',
    model: 'input-only',
    display_name: 'Input Only Model',
    kind: 'image_generation',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        lines: [
          {
            kind: 'billing_rate',
            billable: 'image_input',
            unit: 'image',
            price_usd: 0.005,
          },
        ],
      },
    },
  }

  const res = calculateGenerationCost({
    modelOption: inputOnlyModel,
    action: 'fine_tune',
    count: 1,
    settings: {},
  })
  assert.equal(res.isAvailable, false)
  assert.equal(res.formattedTotal, 'Pricing unavailable')
})

test('calculateGenerationCost handles ambiguous vs unambiguous audio inclusion and rejects mismatched audio request', () => {
  // 1. Dual-rate model: separate prices for audio vs silent video
  const dualModel: MediaCatalogModelOption = {
    id: 'dual-video',
    provider: 'google',
    model: 'dual-video',
    display_name: 'Dual Video',
    kind: 'video_generation',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        lines: [
          {
            billable: 'video_output',
            unit: 'second',
            price_usd: 0.10,
            conditions: { resolution: '720p', includes_audio: true },
          },
          {
            billable: 'video_output',
            unit: 'second',
            price_usd: 0.05,
            conditions: { resolution: '720p', includes_audio: false },
          },
        ],
      },
    },
  }

  // Without specifying includesAudio: ambiguous -> unavailable
  const ambRes = calculateGenerationCost({
    modelOption: dualModel,
    action: 'to_video',
    count: 1,
    settings: { resolution: '720p', durationSeconds: 6 },
  })
  assert.equal(ambRes.isAvailable, false)

  // With explicit includesAudio: true -> matches audio line ($0.10/s * 6 = $0.60)
  const audioTrueRes = calculateGenerationCost({
    modelOption: dualModel,
    action: 'to_video',
    count: 1,
    settings: { resolution: '720p', durationSeconds: 6, includesAudio: true },
  })
  assert.equal(audioTrueRes.isAvailable, true)
  assert.equal(audioTrueRes.unitPrice, 0.10)
  assert.equal(audioTrueRes.totalPrice, 0.60)

  // With explicit includesAudio: false -> matches silent line ($0.05/s * 6 = $0.30)
  const audioFalseRes = calculateGenerationCost({
    modelOption: dualModel,
    action: 'to_video',
    count: 1,
    settings: { resolution: '720p', durationSeconds: 6, includesAudio: false },
  })
  assert.equal(audioFalseRes.isAvailable, true)
  assert.equal(audioFalseRes.unitPrice, 0.05)
  assert.equal(audioFalseRes.totalPrice, 0.30)

  // 2. Veo model (all lines have includes_audio: true): explicit includesAudio: false MUST be rejected
  const veoModel: MediaCatalogModelOption = {
    id: 'veo-all-audio',
    provider: 'google',
    model: 'veo-all-audio',
    display_name: 'Veo',
    kind: 'video_generation',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        lines: [
          {
            billable: 'video_output',
            unit: 'second',
            price_usd: 0.40,
            conditions: { resolution: '720p', includes_audio: true, charged_only_on_success: true },
          },
        ],
      },
    },
  }
  const veoSilentMismatch = calculateGenerationCost({
    modelOption: veoModel,
    action: 'to_video',
    count: 1,
    settings: { resolution: '720p', durationSeconds: 8, includesAudio: false },
  })
  assert.equal(veoSilentMismatch.isAvailable, false)
})

test('calculateGenerationCost rejects lines with non-standard service_tier in variant (batch/flex/priority)', () => {
  const batchVariantModel: MediaCatalogModelOption = {
    id: 'batch-variant',
    provider: 'google',
    model: 'batch-variant',
    display_name: 'Batch Variant Model',
    kind: 'image_generation',
    ready: true,
    pricing: {
      billing: {
        status: 'verified',
        lines: [
          {
            billable: 'image_output',
            unit: 'image',
            price_usd: 0.01,
            variant: 'batch',
          },
          {
            billable: 'image_output',
            unit: 'image',
            price_usd: 0.01,
            variant: 'flex',
          },
        ],
      },
    },
  }

  const res = calculateGenerationCost({
    modelOption: batchVariantModel,
    action: 'fine_tune',
    count: 1,
    settings: {},
  })
  assert.equal(res.isAvailable, false)
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
