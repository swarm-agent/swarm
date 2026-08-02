import assert from 'node:assert/strict'
import test from 'node:test'
import { getSwarmModelSettings } from './get-model-settings'
import { saveSwarmModelSettings } from '../mutations/save-model-settings'

const originalFetch = globalThis.fetch
const action = { provider: 'codex', model: 'gpt-action', thinking: 'high', service_tier: 'fast', context_mode: '' }
const plan = { provider: 'codex', model: 'gpt-plan', thinking: 'xhigh', service_tier: 'fast', context_mode: 'large' }

test.afterEach(() => {
  globalThis.fetch = originalFetch
})

test('GET maps direct Action and Plan selections', async () => {
  globalThis.fetch = async () => new Response(JSON.stringify({
    ok: true,
    model_settings: { action, plan, updated_at: 42 },
  }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  assert.deepEqual(await getSwarmModelSettings(), {
    action: { provider: 'codex', model: 'gpt-action', thinking: 'high', serviceTier: 'fast', contextMode: '' },
    plan: { provider: 'codex', model: 'gpt-plan', thinking: 'xhigh', serviceTier: 'fast', contextMode: 'large' },
    updatedAt: 42,
  })
})

test('GET rejects missing or malformed direct selections', async () => {
  globalThis.fetch = async () => new Response(JSON.stringify({
    ok: true, model_settings: { action, updated_at: 42 },
  }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  await assert.rejects(getSwarmModelSettings(), /missing plan/)
})

test('PUT sends direct Action and Plan payloads and parses the response', async () => {
  let request: RequestInit | undefined
  globalThis.fetch = async (_input, init) => {
    request = init
    return new Response(JSON.stringify({ ok: true, model_settings: { action, plan, updated_at: 43 } }), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    })
  }
  await saveSwarmModelSettings({
    action: { provider: ' codex ', model: ' gpt-action ', thinking: ' high ', serviceTier: ' fast ', contextMode: '' },
    plan: { provider: 'codex', model: 'gpt-plan', thinking: 'xhigh', serviceTier: 'fast', contextMode: ' LARGE ' },
  })
  assert.equal(request?.method, 'PUT')
  assert.deepEqual(JSON.parse(String(request?.body)), { action, plan })
})

test('PUT validates both selections before making a request', async () => {
  let called = false
  globalThis.fetch = async () => { called = true; throw new Error('unexpected') }
  await assert.rejects(saveSwarmModelSettings({
    action: { provider: '', model: 'gpt-action', thinking: 'high', serviceTier: '', contextMode: '' },
    plan: { provider: 'codex', model: 'gpt-plan', thinking: 'xhigh', serviceTier: '', contextMode: '' },
  }), /Action provider, model, and thinking are required/)
  assert.equal(called, false)
})
