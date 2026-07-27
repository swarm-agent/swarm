import test from 'node:test'
import assert from 'node:assert/strict'

type FetchCall = { input: RequestInfo | URL; init?: RequestInit }

async function withMockFetch(run: (calls: FetchCall[]) => Promise<void>): Promise<void> {
  const originalFetch = globalThis.fetch
  const calls: FetchCall[] = []
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)
    if (url.endsWith('/preference')) {
      return jsonResponse({ ok: true, preference: { model: 'gpt-5.4' } })
    }
    if (url.endsWith('/model-profile')) {
      return jsonResponse({ ok: true, model_profile: { source: 'agent_default' } })
    }
    if (url.endsWith('/mode')) {
      return jsonResponse({ ok: true, mode: 'auto' })
    }
    if (url.endsWith('/agent')) {
      return jsonResponse({ ok: true, agent: { name: 'swarm' } })
    }
    if (url.includes('/permissions/perm-1/resolve')) {
      return jsonResponse({
        ok: true,
        permission: {
          id: 'perm-1',
          session_id: 'session-raw',
          status: 'resolved',
          created_at: 1,
          updated_at: 2,
        },
      })
    }
    if (url.endsWith('/permissions/resolve_all')) {
      return jsonResponse({ ok: true, resolved: [] })
    }
    if (url.endsWith('/run/stop')) {
      return jsonResponse({ ok: true, session_id: 'session-raw', run_id: 'run-1', status: 'cancel_requested' })
    }
    throw new Error(`unexpected fetch: ${url}`)
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

test('Desktop V3 removed standalone permission, usage, metadata, and plan subresource helper exports from legacy chat queries', async () => {
  const queries = await import('./chat-queries')
  assert.equal('fetchSessionPendingPermissions' in queries, false)
  assert.equal('fetchSessionUsageSummary' in queries, false)
  assert.equal('fetchSessionMetadata' in queries, false)
  assert.equal('updateSessionMetadata' in queries, false)
  assert.equal('fetchActiveSessionPlan' in queries, false)
  assert.equal('activateSessionPlan' in queries, false)
  assert.equal('fetchSessionPlans' in queries, false)
  assert.equal('fetchSessionPlan' in queries, false)
  assert.equal('saveSessionPlan' in queries, false)
  assert.equal('fetchSessionPlanHistory' in queries, false)
})

test('Desktop V3 preference APIs use explicit Sessions API v3 helpers, not legacy lifecycle helpers', async () => {
  await withMockFetch(async (calls) => {
    const { fetchSessionV3Preference, updateSessionV3Preference } = await import('../../session-v3/api')

    await fetchSessionV3Preference('session-raw')
    await updateSessionV3Preference('session-raw', { provider: '', model: 'gpt-5.4', thinking: undefined })

    assert.deepEqual(calls.map((call) => String(call.input)), [
      '/v3/sessions/session-raw/preference',
      '/v3/sessions/session-raw/preference',
    ])
    assert.equal(calls[1]?.init?.method, 'POST')
    const preferenceBody = JSON.parse(String(calls[1]?.init?.body ?? '{}'))
    assert.equal(preferenceBody.model, 'gpt-5.4')
    assert.match(preferenceBody.client_request_id, /^desktop-preference:/)
    await assert.rejects(
      () => updateSessionV3Preference('session-raw', { provider: '', model: '', thinking: '' }),
      /non-empty preference change/,
    )
  })
})

test('Desktop V3 session model settings mutations supply durable client request IDs', async () => {
  await withMockFetch(async (calls) => {
    const { updateSessionV3Agent, updateSessionV3Mode, updateSessionV3ModelProfile } = await import('../../session-v3/api')

    await updateSessionV3ModelProfile('session-raw', { kind: 'agent-default' })
    await updateSessionV3Mode('session-raw', 'auto')
    await updateSessionV3Agent('session-raw', 'swarm')

    assert.deepEqual(calls.map((call) => String(call.input)), [
      '/v3/sessions/session-raw/model-profile',
      '/v3/sessions/session-raw/mode',
      '/v3/sessions/session-raw/agent',
    ])
    const modelProfileBody = JSON.parse(String(calls[0]?.init?.body ?? '{}'))
    const modeBody = JSON.parse(String(calls[1]?.init?.body ?? '{}'))
    const agentBody = JSON.parse(String(calls[2]?.init?.body ?? '{}'))
    assert.match(modelProfileBody.client_request_id, /^desktop-model-profile:/)
    assert.deepEqual(modelProfileBody.choice, { use_agent_default: true })
    assert.match(modeBody.client_request_id, /^desktop-mode:/)
    assert.equal(modeBody.mode, 'auto')
    assert.match(agentBody.client_request_id, /^desktop-agent:/)
    assert.equal(agentBody.agent_name, 'swarm')
  })
})

test('Desktop V3 permission resolution uses explicit Sessions API v3 helpers and cannot fall back to V2', async () => {
  await withMockFetch(async (calls) => {
    const { resolveSessionV3Permission, resolveAllSessionV3Permissions } = await import('../../session-v3/api')

    await resolveSessionV3Permission('session-raw', 'perm-1', { action: 'approve', reason: 'ok' })
    await resolveAllSessionV3Permissions('session-raw', { action: 'approve', reason: 'ok' })

    assert.deepEqual(calls.map((call) => String(call.input)), [
      '/v3/sessions/session-raw/permissions/perm-1/resolve',
      '/v3/sessions/session-raw/permissions/resolve_all',
    ])
    assert.equal(calls[0]?.init?.method, 'POST')
    assert.equal(calls[1]?.init?.method, 'POST')
  })
})

test('Desktop V3 stop uses explicit Sessions API v3 mutation helper, not V2 stop or per-session streams', async () => {
  await withMockFetch(async (calls) => {
    const { stopSessionV3Run } = await import('../../session-v3/api')

    await assert.rejects(
      () => stopSessionV3Run('session-raw', { runId: 'run-1', targetSwarmId: '' }),
      /Desktop V3 stop requires target_swarm_id/,
    )
    await stopSessionV3Run('session-raw', { runId: 'run-1', targetSwarmId: ' primary-swarm ' })

    assert.equal(calls.length, 1)
    assert.equal(String(calls[0].input), '/v3/sessions/session-raw/run/stop')
    assert.equal(calls[0].init?.method, 'POST')
    assert.deepEqual(JSON.parse(String(calls[0].init?.body ?? '{}')), {
      type: 'run.stop',
      run_id: 'run-1',
      target_swarm_id: 'primary-swarm',
    })
  })
})
