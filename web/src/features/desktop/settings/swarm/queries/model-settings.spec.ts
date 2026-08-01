import assert from 'node:assert/strict'
import test from 'node:test'
import { getSwarmModelSettings } from './get-model-settings'
import { saveSwarmModelSettings } from '../mutations/save-model-settings'

const originalFetch = globalThis.fetch

test.afterEach(() => {
  globalThis.fetch = originalFetch
})

test('GET maps required Action and enabled Plan favorite assignments', async () => {
  globalThis.fetch = async () => new Response(JSON.stringify({
    ok: true,
    model_settings: { action_favorite_id: 'mp_action', plan_enabled: true, plan_favorite_id: 'mp_plan', updated_at: 42 },
  }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  assert.deepEqual(await getSwarmModelSettings(), {
    actionFavoriteId: 'mp_action', planEnabled: true, planFavoriteId: 'mp_plan', updatedAt: 42,
  })
})

test('GET rejects malformed or contradictory canonical settings', async () => {
  globalThis.fetch = async () => new Response(JSON.stringify({
    ok: true, model_settings: { action_favorite_id: 'mp_action', plan_enabled: true, updated_at: 42 },
  }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  await assert.rejects(getSwarmModelSettings(), /plan_favorite_id/)
})

test('PUT sends the canonical assignment payload and parses the response', async () => {
  let request: RequestInit | undefined
  globalThis.fetch = async (_input, init) => {
    request = init
    return new Response(JSON.stringify({
      ok: true, model_settings: { action_favorite_id: 'mp_action', plan_enabled: false, updated_at: 43 },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }
  assert.deepEqual(await saveSwarmModelSettings({ actionFavoriteId: ' mp_action ', planEnabled: false }), {
    actionFavoriteId: 'mp_action', planEnabled: false, updatedAt: 43,
  })
  assert.equal(request?.method, 'PUT')
  assert.deepEqual(JSON.parse(String(request?.body)), { action_favorite_id: 'mp_action', plan_enabled: false })
})

test('PUT validates assignments before making a request and surfaces server errors', async () => {
  let called = false
  globalThis.fetch = async () => {
    called = true
    return new Response(JSON.stringify({ error: 'favorite missing' }), { status: 409, headers: { 'Content-Type': 'application/json' } })
  }
  await assert.rejects(saveSwarmModelSettings({ actionFavoriteId: 'mp_action', planEnabled: true }), /Plan favorite is required/)
  await assert.rejects(saveSwarmModelSettings({ actionFavoriteId: 'mp_action', planEnabled: false, planFavoriteId: 'mp_plan' }), /must be omitted/)
  assert.equal(called, false)
  await assert.rejects(saveSwarmModelSettings({ actionFavoriteId: 'mp_action', planEnabled: false }), /favorite missing/)
})
