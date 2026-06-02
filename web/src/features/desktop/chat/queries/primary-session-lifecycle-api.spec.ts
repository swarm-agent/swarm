import test from 'node:test'
import assert from 'node:assert/strict'

async function withFetchStub(
  run: (calls: Array<{ input: RequestInfo | URL; init?: RequestInit }>) => Promise<void>,
): Promise<void> {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)
    const body = init?.body
      ? (JSON.parse(String(init.body)) as Record<string, unknown>)
      : {}

    if (url === '/v2/sessions/session-primary/metadata') {
      return jsonResponse({ ok: true, session_id: 'session-primary', metadata: { ticket: 'T-1' } })
    }
    if (url === '/v2/sessions/session-primary/mode') {
      return jsonResponse({ ok: true, session_id: 'session-primary', mode: body.mode ?? 'auto' })
    }
    if (url === '/v2/sessions/session-primary/codex') {
      return jsonResponse({ ok: true, session_id: 'session-primary', provider: 'codex', model: 'gpt-5.4', thinking: 'medium', service_tier: body.service_tier ?? 'flex', context_mode: body.context_mode ?? 'large', effective_context_window: 1000000, updated_at: 42 })
    }
    if (url === '/v2/sessions/session-primary/plans/active') {
      return jsonResponse({ ok: true, session_id: 'session-primary', has_active: true, active_plan: { id: body.plan_id ?? 'plan-1', title: 'Plan', plan: '# Plan' } })
    }
    if (url === '/v2/sessions/session-primary/plans?limit=100') {
      return jsonResponse({ ok: true, session_id: 'session-primary', active_plan_id: 'plan-1', plans: [{ id: 'plan-1', title: 'Plan', plan: '# Plan' }] })
    }
    if (url === '/v2/sessions/session-primary/plans/plan-1') {
      return jsonResponse({ ok: true, session_id: 'session-primary', plan: { id: 'plan-1', title: 'Plan', plan: '# Plan' } })
    }
    if (url === '/v2/sessions/session-primary/permissions/resolve_all') {
      return jsonResponse({ ok: true, session_id: 'session-primary', count: 1, resolved: [{ id: 'perm-1', session_id: 'session-primary', status: 'approved' }] })
    }
    if (url === '/v2/sessions/session-primary/run') {
      return jsonResponse({ ok: true, session_id: 'session-primary', run_id: 'run-1', status: 'accepted' }, 202)
    }
    if (url === '/v2/sessions/session-primary/run/stream') {
      return jsonResponse({ ok: true, session_id: 'session-primary', run_id: 'run-stream-1', status: 'accepted' }, 202)
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

function callFor(calls: Array<{ input: RequestInfo | URL; init?: RequestInit }>, url: string) {
  const call = calls.find((entry) => String(entry.input) === url)
  assert.ok(call, `expected ${url}`)
  return call
}

test('primary desktop lifecycle helpers use native v2 metadata, mode, and codex endpoints', async () => {
  const { fetchSessionMetadata, fetchSessionMode, fetchSessionCodexConfig, updateSessionCodexConfig } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const metadata = await fetchSessionMetadata('session-primary')
    const mode = await fetchSessionMode('session-primary')
    const codex = await fetchSessionCodexConfig('session-primary')
    const updatedCodex = await updateSessionCodexConfig('session-primary', { serviceTier: 'flex', contextMode: '1m' })

    assert.deepEqual(metadata, { ticket: 'T-1' })
    assert.equal(mode, 'auto')
    assert.equal(codex.sessionId, 'session-primary')
    assert.equal(codex.serviceTier, 'flex')
    assert.equal(codex.contextMode, 'large')
    assert.equal(updatedCodex.serviceTier, 'flex')
    assert.equal(updatedCodex.contextMode, '1m')
    assert.equal(callFor(calls, '/v2/sessions/session-primary/metadata').init?.method, undefined)
    assert.equal(callFor(calls, '/v2/sessions/session-primary/mode').init?.method, undefined)
    assert.equal(callFor(calls, '/v2/sessions/session-primary/codex').init?.method, undefined)
    assert.equal(calls.filter((entry) => String(entry.input) === '/v2/sessions/session-primary/codex' && entry.init?.method === 'POST').length, 1)
  })
})

test('primary desktop plan and permission helpers use native v2 lifecycle endpoints', async () => {
  const { activateSessionPlan, fetchSessionPlans, fetchSessionPlan, resolveAllSessionPermissions } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const active = await activateSessionPlan('session-primary', 'plan-1')
    const plans = await fetchSessionPlans('session-primary')
    const plan = await fetchSessionPlan('session-primary', 'plan-1')
    const resolved = await resolveAllSessionPermissions('session-primary', 'approve', 'ok', 25)

    assert.equal(active.id, 'plan-1')
    assert.equal(plans.activePlanId, 'plan-1')
    assert.equal(plans.plans.length, 1)
    assert.equal(plan.id, 'plan-1')
    assert.equal(resolved[0]?.id, 'perm-1')
    assert.equal(callFor(calls, '/v2/sessions/session-primary/plans/active').init?.method, 'POST')
    assert.equal(callFor(calls, '/v2/sessions/session-primary/plans?limit=100').init?.method, undefined)
    assert.equal(callFor(calls, '/v2/sessions/session-primary/plans/plan-1').init?.method, undefined)
    assert.equal(callFor(calls, '/v2/sessions/session-primary/permissions/resolve_all').init?.method, 'POST')
  })
})

test('primary desktop run helper can use native non-stream run endpoint without changing stream default', async () => {
  const { startSessionRun } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await startSessionRun({ sessionId: 'session-primary', prompt: 'hello', stream: false })
    await startSessionRun({ sessionId: 'session-primary', prompt: 'hello stream' })

    const runCall = callFor(calls, '/v2/sessions/session-primary/run')
    const streamCall = callFor(calls, '/v2/sessions/session-primary/run/stream')
    const runBody = JSON.parse(String(runCall.init?.body ?? '{}')) as Record<string, unknown>
    const streamBody = JSON.parse(String(streamCall.init?.body ?? '{}')) as Record<string, unknown>
    assert.equal(runCall.init?.method, 'POST')
    assert.equal(streamCall.init?.method, 'POST')
    for (const body of [runBody, streamBody]) {
      assert.equal(body.agent_name, '')
      assert.equal(Object.hasOwn(body, 'target_swarm_id'), false)
      assert.equal(Object.hasOwn(body, 'session_id'), false)
      assert.equal(Object.hasOwn(body, 'target_kind'), false)
      assert.equal(Object.hasOwn(body, 'target_name'), false)
      assert.equal(Object.hasOwn(body, 'tool_scope'), false)
      assert.equal(Object.hasOwn(body, 'execution_context'), false)
    }
  })
})
