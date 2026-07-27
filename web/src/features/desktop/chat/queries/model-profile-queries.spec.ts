import assert from 'node:assert/strict'
import test from 'node:test'
import { fetchModelProfiles } from './model-profile-queries'

const originalFetch = globalThis.fetch

test.afterEach(() => {
  globalThis.fetch = originalFetch
})

test('fetchModelProfiles maps all saved selection fields and default identity', async () => {
  globalThis.fetch = async () => new Response(JSON.stringify({
    ok: true,
    default_profile_id: 'mp_1',
    model_profiles: [{
      profile_id: 'mp_1', name: 'Recommended', model_mode: 'single', is_default: true,
      single: { provider: 'openai', model: 'gpt-test', thinking: 'high', service_tier: '', context_mode: 'full' },
      created_at: 10, updated_at: 20,
    }],
  }), { status: 200, headers: { 'Content-Type': 'application/json' } })

  const state = await fetchModelProfiles()
  assert.equal(state.defaultProfileId, 'mp_1')
  assert.deepEqual(state.profiles[0]?.single, {
    provider: 'openai', model: 'gpt-test', thinking: 'high', serviceTier: '', contextMode: 'full',
  })
  assert.equal(state.profiles[0]?.isDefault, true)
})
