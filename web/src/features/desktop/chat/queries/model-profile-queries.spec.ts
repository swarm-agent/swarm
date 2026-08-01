import assert from 'node:assert/strict'
import test from 'node:test'
import { createModelProfile, fetchModelProfiles, reorderModelProfiles, updateModelProfile } from './model-profile-queries'

const originalFetch = globalThis.fetch
const profile = {
  profile_id: 'mp_1', name: 'Recommended', provider: 'openai', model: 'gpt-test', thinking: 'high',
  service_tier: '', context_mode: 'full', created_at: 10, updated_at: 20, sort_order: 3, is_default: true,
}

test.afterEach(() => {
  globalThis.fetch = originalFetch
})

test('fetchModelProfiles maps the canonical flat favorite and default identity', async () => {
  globalThis.fetch = async () => new Response(JSON.stringify({
    ok: true, default_profile_id: 'mp_1', model_profiles: [profile],
  }), { status: 200, headers: { 'Content-Type': 'application/json' } })

  const state = await fetchModelProfiles()
  assert.deepEqual(state, {
    defaultProfileId: 'mp_1',
    profiles: [{
      profileId: 'mp_1', name: 'Recommended', provider: 'openai', model: 'gpt-test', thinking: 'high',
      serviceTier: '', contextMode: 'full', createdAt: 10, updatedAt: 20, sortOrder: 3, isDefault: true,
    }],
  })
})

test('fetchModelProfiles rejects legacy and malformed responses', async () => {
  globalThis.fetch = async () => new Response(JSON.stringify({
    ok: true, default_profile_id: 'mp_1', model_profiles: [{
      profile_id: 'mp_1', name: 'Malformed', model: 'gpt-test', thinking: 'high', service_tier: '', context_mode: '',
      created_at: 10, updated_at: 20, sort_order: 0, is_default: true,
    }],
  }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  await assert.rejects(fetchModelProfiles(), /provider/)

  globalThis.fetch = async () => new Response(JSON.stringify({
    ok: true, default_profile_id: 'mp_missing', model_profiles: [profile],
  }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  await assert.rejects(fetchModelProfiles(), /default_profile_id/)
})

test('create and update send only canonical flat favorite fields', async () => {
  const requests: Array<{ url: string; init?: RequestInit }> = []
  globalThis.fetch = async (input, init) => {
    requests.push({ url: String(input), init })
    return new Response(JSON.stringify({ ok: true, model_profile: profile }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
  const input = { name: ' Recommended ', provider: ' openai ', model: ' gpt-test ', thinking: ' high ', serviceTier: ' fast ', contextMode: ' FULL ' }
  await createModelProfile(input)
  await updateModelProfile('mp_1', input)
  const expected = { name: 'Recommended', provider: 'openai', model: 'gpt-test', thinking: 'high', service_tier: 'fast', context_mode: 'full' }
  assert.deepEqual(JSON.parse(String(requests[0]?.init?.body)), expected)
  assert.deepEqual(JSON.parse(String(requests[1]?.init?.body)), expected)
  assert.equal(requests[0]?.init?.method, 'POST')
  assert.equal(requests[1]?.init?.method, 'PUT')
})

test('reorderModelProfiles sends the complete profile order and rejects missing response arrays', async () => {
  let request: RequestInit | undefined
  globalThis.fetch = async (_input, init) => {
    request = init
    return new Response(JSON.stringify({ ok: true, model_profiles: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
  await reorderModelProfiles(['mp_2', 'mp_1'])
  assert.equal(request?.method, 'PATCH')
  assert.deepEqual(JSON.parse(String(request?.body)), { profile_ids: ['mp_2', 'mp_1'] })

  globalThis.fetch = async () => new Response(JSON.stringify({ ok: true }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  await assert.rejects(reorderModelProfiles(['mp_1']), /model_profiles/)
})

test('request errors are surfaced unchanged', async () => {
  globalThis.fetch = async () => new Response(JSON.stringify({ error: 'favorite conflict' }), { status: 409, headers: { 'Content-Type': 'application/json' } })
  await assert.rejects(fetchModelProfiles(), /favorite conflict/)
})
