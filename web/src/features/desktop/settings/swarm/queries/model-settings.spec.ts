import assert from 'node:assert/strict'
import test from 'node:test'
import { getAgentModelSettings } from './get-agent-model-settings'
import { restoreAgentModelDefaults, saveSwarmAgentModelSettings, saveSystemAgentModelSettings } from '../mutations/save-agent-model-settings'

const originalFetch = globalThis.fetch
const assignment = (model: string) => ({ provider: 'codex', model, thinking: 'high', service_tier: 'fast', context_mode: '' })
const responseRecord = (updatedAt = 42) => ({
  swarm: { action: assignment('gpt-action'), plan: assignment('gpt-plan') },
  system_agents: {
    compact: assignment('gpt-compact'), finder: assignment('gpt-finder'), coder: assignment('gpt-coder'),
    designer: assignment('gpt-designer'), router: assignment('gpt-router'),
  },
  updated_at: updatedAt,
})

test.afterEach(() => {
  globalThis.fetch = originalFetch
})

test('GET maps the complete unified assignment record', async () => {
  globalThis.fetch = async () => new Response(JSON.stringify({
    ok: true,
    agent_model_settings: responseRecord(),
  }), { status: 200, headers: { 'Content-Type': 'application/json' } })
  assert.deepEqual(await getAgentModelSettings(), {
    swarm: {
      action: { provider: 'codex', model: 'gpt-action', thinking: 'high', serviceTier: 'fast', contextMode: '' },
      plan: { provider: 'codex', model: 'gpt-plan', thinking: 'high', serviceTier: 'fast', contextMode: '' },
    },
    systemAgents: {
      compact: { provider: 'codex', model: 'gpt-compact', thinking: 'high', serviceTier: 'fast', contextMode: '' },
      finder: { provider: 'codex', model: 'gpt-finder', thinking: 'high', serviceTier: 'fast', contextMode: '' },
      coder: { provider: 'codex', model: 'gpt-coder', thinking: 'high', serviceTier: 'fast', contextMode: '' },
      designer: { provider: 'codex', model: 'gpt-designer', thinking: 'high', serviceTier: 'fast', contextMode: '' },
      router: { provider: 'codex', model: 'gpt-router', thinking: 'high', serviceTier: 'fast', contextMode: '' },
    },
    updatedAt: 42,
  })
})

test('GET rejects incomplete system-agent assignments', async () => {
  const record = responseRecord()
  delete (record.system_agents as Partial<typeof record.system_agents>).router
  globalThis.fetch = async () => new Response(JSON.stringify({ ok: true, agent_model_settings: record }), {
    status: 200, headers: { 'Content-Type': 'application/json' },
  })
  await assert.rejects(getAgentModelSettings(), /missing system_agents.router/)
})

test('PATCH replaces Action and Plan together', async () => {
  let request: RequestInit | undefined
  globalThis.fetch = async (_input, init) => {
    request = init
    return new Response(JSON.stringify({ ok: true, agent_model_settings: responseRecord(43) }), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    })
  }
  await saveSwarmAgentModelSettings({
    action: { provider: ' CODEX ', model: ' gpt-action ', thinking: ' high ', serviceTier: ' FAST ', contextMode: '' },
    plan: { provider: 'codex', model: 'gpt-plan', thinking: 'high', serviceTier: 'fast', contextMode: '' },
  })
  assert.equal(request?.method, 'PATCH')
  assert.deepEqual(JSON.parse(String(request?.body)), {
    swarm: { action: assignment('gpt-action'), plan: assignment('gpt-plan') },
  })
})

test('PATCH updates one system agent without sending sibling assignments', async () => {
  let request: RequestInit | undefined
  globalThis.fetch = async (_input, init) => {
    request = init
    return new Response(JSON.stringify({ ok: true, agent_model_settings: responseRecord(44) }), {
      status: 200, headers: { 'Content-Type': 'application/json' },
    })
  }
  await saveSystemAgentModelSettings({
    agent: 'coder',
    assignment: { provider: 'CODEX', model: 'gpt-coder', thinking: 'high', serviceTier: 'FAST', contextMode: '' },
  })
  assert.equal(request?.method, 'PATCH')
  assert.deepEqual(JSON.parse(String(request?.body)), { system_agents: { coder: assignment('gpt-coder') } })
})

// Purpose: the restore mutation sends only an optimistic-concurrency token to
// its dedicated authority; failed refresh/stale responses never become settings.
test('restore uses explicit endpoint and rejects failures', async () => {
  let calls = 0
  globalThis.fetch = async (input, init) => {
    calls++
    assert.match(String(input), /\/v1\/agent-model-settings\/restore-defaults$/)
    assert.equal(init?.method, 'POST')
    assert.deepEqual(JSON.parse(String(init?.body)), { expected_updated_at: 42 })
    return new Response(JSON.stringify({ ok: true, agent_model_settings: responseRecord(43) }), { status: 200 })
  }
  assert.equal((await restoreAgentModelDefaults(42)).updatedAt, 43)
  assert.equal(calls, 1)
  globalThis.fetch = async () => new Response(JSON.stringify({ error: 'catalog unavailable' }), { status: 502 })
  await assert.rejects(restoreAgentModelDefaults(42))
})
