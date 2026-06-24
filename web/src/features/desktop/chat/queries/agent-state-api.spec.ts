import test from 'node:test'
import assert from 'node:assert/strict'

async function withFetchStub(
  run: (calls: Array<{ input: RequestInfo | URL; init?: RequestInit }>) => Promise<void>,
): Promise<void> {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    return jsonResponse({
      ok: true,
      provider_defaults_preview: null,
      state: {
        active_primary: 'swarm',
        active_subagent: {},
        version: 1,
        profiles: [{
          name: 'swarm',
          mode: 'primary',
          description: 'Swarm',
          provider: 'codex',
          model: 'gpt-5-codex',
          thinking: 'medium',
          prompt: 'large settings-only prompt',
          runtime_mode: 'plan_auto',
          execution_setting: '',
          exit_plan_mode_enabled: true,
          tool_contract: { preset: 'read_write' },
          enabled: true,
          updated_at: 42,
        }],
      },
      tool_inventory: { tools: [], presets: [] },
    })
  }) as typeof fetch

  try {
    await run(calls)
  } finally {
    globalThis.fetch = originalFetch
  }
}

function jsonResponse(payload: unknown, status = 200): Response {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

test('Desktop chat agent-state fetch uses lean summary view', async () => {
  await withFetchStub(async (calls) => {
    const { fetchAgentStateSummary } = await import('./chat-queries')
    const state = await fetchAgentStateSummary()

    assert.equal(String(calls[0]?.input), '/v2/agents?limit=200&view=summary')
    assert.equal(state.profiles[0]?.name, 'swarm')
    assert.equal(state.profiles[0]?.prompt, 'large settings-only prompt')
    assert.equal(state.profiles[0]?.toolContract?.preset, 'read_write')
  })
})

test('full agent-state fetch remains available for Settings Agents editor', async () => {
  await withFetchStub(async (calls) => {
    const { fetchAgentState } = await import('./chat-queries')
    await fetchAgentState()

    assert.equal(String(calls[0]?.input), '/v2/agents?limit=200')
  })
})
