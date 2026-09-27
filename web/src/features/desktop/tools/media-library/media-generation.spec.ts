import assert from 'node:assert/strict'
import test from 'node:test'
import { calculateGenerationCost, extractBillingLines, normalizeResKey } from './media-generation'
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

test('extractBillingLines parses lines from nested billing object and validates positive prices', () => {
  const pricing = {
    billing: {
      status: 'verified',
      lines: [
        { billable: 'image_output', unit: 'image', price_usd: 0.03, conditions: { resolution: '1k' } },
        { billable: 'image_output', unit: 'image', price_usd: '0.06', conditions: { resolution: '2k' } },
        { billable: 'invalid', unit: 'image', price_usd: -5 },
        null,
      ],
    },
  }
  const lines = extractBillingLines(pricing)
  assert.equal(lines.length, 2)
  assert.equal(lines[0].billable, 'image_output')
  assert.equal(lines[0].priceUsd, 0.03)
  assert.equal(lines[0].conditions?.resolution, '1k')
  assert.equal(lines[1].priceUsd, 0.06)
  assert.equal(lines[1].conditions?.resolution, '2k')
})

test('calculateGenerationCost returns unavailable when pricing metadata is missing, empty, or token-only', () => {
  // Missing modelOption
  const res1 = calculateGenerationCost({
    modelOption: undefined,
    action: 'fine_tune',
    count: 1,
    settings: {},
  })
  assert.equal(res1.isAvailable, false)
  assert.equal(res1.formattedTotal, 'Pricing unavailable')

  // Empty pricing object
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

  // Token-only pricing (e.g. 0.00003 per token) should NOT be fabricated as flat image price
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

test('calculateGenerationCost matches image resolution condition and computes dynamic quantity costs', () => {
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

  // 1k resolution, 1 variant
  const single1k = calculateGenerationCost({
    modelOption,
    action: 'fine_tune',
    count: 1,
    settings: { resolution: '1k' },
  })
  assert.equal(single1k.isAvailable, true)
  assert.equal(single1k.isVerified, true)
  assert.equal(single1k.unitPrice, 0.03)
  assert.equal(single1k.totalPrice, 0.03)
  assert.equal(single1k.formattedTotal, '$0.03')

  // 2k resolution, 4 variants
  const four2k = calculateGenerationCost({
    modelOption,
    action: 'iterate',
    count: 4,
    settings: { resolution: '2k' },
  })
  assert.equal(four2k.isAvailable, true)
  assert.equal(four2k.unitPrice, 0.06)
  assert.equal(four2k.totalPrice, 0.24)
  assert.equal(four2k.formattedTotal, '$0.24')
})

test('calculateGenerationCost scales video output across duration and resolution with second-based unit rate', () => {
  const modelOption: MediaCatalogModelOption = {
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
          { billable: 'video_output', unit: 'second', price_usd: 0.05, conditions: { resolution: '720p', service_tier: 'standard' } },
          { billable: 'video_output', unit: 'second', price_usd: 0.08, conditions: { resolution: '1080p', service_tier: 'standard' } },
        ],
      },
    },
  }

  // 720p at 8s duration
  const video720p = calculateGenerationCost({
    modelOption,
    action: 'to_video',
    count: 1,
    settings: { resolution: '720p', durationSeconds: 8 },
  })
  assert.equal(video720p.isAvailable, true)
  assert.equal(video720p.isVerified, true)
  assert.equal(video720p.unitPrice, 0.05)
  assert.equal(video720p.totalPrice, 0.40)
  assert.equal(video720p.formattedTotal, '$0.40')

  // 1080p at 16s duration across 2 continuations
  const video1080p = calculateGenerationCost({
    modelOption,
    action: 'next_scene',
    count: 2,
    settings: { resolution: '1080p', durationSeconds: 16 },
  })
  assert.equal(video1080p.isAvailable, true)
  assert.equal(video1080p.unitPrice, 0.08)
  // 0.08 * 16 = 1.28 per clip * 2 clips = 2.56
  assert.equal(video1080p.totalPrice, 2.56)
  assert.equal(video1080p.formattedTotal, '$2.56')
})
