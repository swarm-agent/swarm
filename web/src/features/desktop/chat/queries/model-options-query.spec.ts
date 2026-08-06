import assert from 'node:assert/strict'
import test from 'node:test'

import { fetchModelOptions } from './chat-queries'

const originalFetch = globalThis.fetch

test.afterEach(() => {
  globalThis.fetch = originalFetch
})

test('fetchModelOptions bounds OpenRouter catalog loading and preserves routed family identity', async () => {
  const catalogRecords = Array.from({ length: 600 }, (_, index) => ({
    provider: 'openrouter',
    model: index === 499 ? 'google/gemini-3.1-pro' : `vendor/model-${String(index).padStart(3, '0')}`,
    context_window: index === 50 ? 128_000 : 64_000,
    thinking_options: index === 50 ? ['off', 'high'] : [],
    default_thinking: index === 50 ? 'high' : '',
    context_modes: index === 50
      ? [
          { mode: 'standard', default: true, context_window: 128_000 },
          { mode: 'long', context_window: 256_000 },
        ]
      : [],
  }))
  const requests: string[] = []

  globalThis.fetch = async (input) => {
    const url = String(input)
    requests.push(url)
    if (url === '/v1/providers') {
      return Response.json({
        providers: [
          { id: 'openrouter', ready: true, runnable: true },
          { id: 'not-ready', ready: false, runnable: true },
        ],
      })
    }
    if (url === '/v1/models/favorites?provider=openrouter&limit=200') {
      return Response.json({
        records: [{ provider: 'openrouter', model: 'vendor/model-050', label: 'Favorite model', thinking: 'off' }],
      })
    }
    const catalogMatch = /^\/v1\/model\/catalog\?provider=openrouter&limit=(\d+)$/.exec(url)
    if (catalogMatch) {
      return Response.json({ records: catalogRecords.slice(0, Number(catalogMatch[1])) })
    }
    throw new Error(`unexpected request: ${url}`)
  }

  const options = await fetchModelOptions()

  assert.deepEqual(
    requests.filter((url) => url.startsWith('/v1/model/catalog?provider=openrouter')),
    ['/v1/model/catalog?provider=openrouter&limit=500'],
  )
  assert.equal(requests.some((url) => url.includes('provider=not-ready')), false)
  const routedGoogle = options.find((option) => option.model === 'google/gemini-3.1-pro' && !option.favorite)
  assert.ok(routedGoogle)
  assert.equal(routedGoogle.provider, 'openrouter')
  assert.equal(routedGoogle.upstreamFamily, 'google')
  assert.equal(options.some((option) => option.model === 'vendor/model-500'), false)

  const favorite = options.find((option) => option.model === 'vendor/model-050' && option.contextMode === '')
  assert.ok(favorite)
  assert.equal(favorite.label, 'Favorite model')
  assert.equal(favorite.favorite, true)
  assert.equal(favorite.contextWindow, 128_000)
  assert.deepEqual(favorite.thinkingOptions, ['off', 'high'])
  assert.equal(favorite.defaultThinking, 'high')
  assert.equal(options[0]?.model, 'vendor/model-050')

  const longContext = options.find((option) => option.model === 'vendor/model-050' && option.contextMode === 'long')
  assert.ok(longContext)
  assert.equal(longContext.favorite, true)
  assert.equal(longContext.contextWindow, 256_000)
  assert.equal(options.length, 501)
})
