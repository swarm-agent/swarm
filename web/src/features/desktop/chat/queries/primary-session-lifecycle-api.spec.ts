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
      if (init?.method === 'POST') {
        return jsonResponse({ ok: true, session: { id: 'session-primary', title: 'Primary', workspace_path: '/repo', workspace_name: 'repo', mode: 'auto', metadata: body.metadata } })
      }
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
    if (url.endsWith('/run/stop') && url.startsWith('/v3/sessions/')) {
      if (body.target_swarm_id === '') {
        return jsonResponse({ error: 'target_swarm_id is required' }, 400)
      }
      if (body.target_swarm_id === 'container-swarm') {
        return jsonResponse({ error: 'target_swarm_id "container-swarm" is not the primary runtime' }, 400)
      }
      return jsonResponse({ ok: true, session_id: url.split('/')[3], run_id: body.run_id, status: 'cancelled', target_swarm_id: body.target_swarm_id })
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

test('primary desktop lifecycle helpers use native v2 mode and codex endpoints', async () => {
  const { fetchSessionMode, fetchSessionCodexConfig, updateSessionCodexConfig } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const mode = await fetchSessionMode('session-primary')
    const codex = await fetchSessionCodexConfig('session-primary')
    const updatedCodex = await updateSessionCodexConfig('session-primary', { serviceTier: 'flex', contextMode: '1m' })

    assert.equal(mode, 'auto')
    assert.equal(codex.sessionId, 'session-primary')
    assert.equal(codex.serviceTier, 'flex')
    assert.equal(codex.contextMode, 'large')
    assert.equal(updatedCodex.serviceTier, 'flex')
    assert.equal(updatedCodex.contextMode, '1m')
    assert.equal(callFor(calls, '/v2/sessions/session-primary/mode').init?.method, undefined)
    assert.equal(callFor(calls, '/v2/sessions/session-primary/codex').init?.method, undefined)
    assert.equal(calls.filter((entry) => String(entry.input) === '/v2/sessions/session-primary/codex' && entry.init?.method === 'POST').length, 1)
  })
})

test('primary desktop session metadata helper exports are removed for Desktop V3', async () => {
  const queries = await import('./chat-queries')
  assert.equal('fetchSessionMetadata' in queries, false)
  assert.equal('updateSessionMetadata' in queries, false)
})


test('primary desktop permission helper cannot fall back to legacy V2 permission resolution', async () => {
  const { resolveAllSessionPermissions } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await assert.rejects(
      () => resolveAllSessionPermissions('session-primary', 'approve', 'ok', 25),
      /requires explicit Sessions API v3 context/,
    )
    assert.equal(calls.length, 0)
  })
})

test('desktop stop helper sends unsupported route to V3 stop API and surfaces backend target error', async () => {
  const { stopSessionRun } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await assert.rejects(
      () => stopSessionRun('session-container', 'run-1', {
        id: 'container:binding:local-binding',
        label: 'container',
        swarmId: 'container-swarm',
        targetKind: 'local-container',
        targetRelationship: 'child',
        hostSwarmId: 'primary-swarm',
        hostSwarmName: 'primary',
        hostWorkspacePath: '/repo',
        hostWorkspaceName: 'swarm-go',
        runtimeWorkspacePath: '/workspaces/repo',
        workspaceBindingId: 'local-binding',
      }),
      /container-swarm.*not the primary runtime/
    )
    const stopCall = callFor(calls, '/v3/sessions/session-container/run/stop')
    const stopBody = JSON.parse(String(stopCall.init?.body ?? '{}')) as Record<string, unknown>
    assert.equal(stopCall.init?.method, 'POST')
    assert.equal(stopBody.type, 'run.stop')
    assert.equal(stopBody.run_id, 'run-1')
    assert.equal(stopBody.target_swarm_id, 'container-swarm')
    assert.equal(calls.filter((entry) => String(entry.input).includes('/run/stop')).length, 1)
  })
})

test('primary desktop stop helper uses single V3 stop path with swarm target', async () => {
  const { stopSessionRun } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await stopSessionRun('session-primary', 'run-1', {
      id: 'host:binding:local-binding',
      label: 'primary',
      swarmId: 'primary-swarm',
      targetKind: 'host',
      targetRelationship: 'self',
      hostSwarmId: 'primary-swarm',
      hostSwarmName: 'primary',
      hostWorkspacePath: '/repo',
      hostWorkspaceName: 'swarm-go',
      runtimeWorkspacePath: '/repo',
      workspaceBindingId: 'local-binding',
    })

    const stopCall = callFor(calls, '/v3/sessions/session-primary/run/stop')
    const stopBody = JSON.parse(String(stopCall.init?.body ?? '{}')) as Record<string, unknown>
    assert.equal(stopCall.init?.method, 'POST')
    assert.equal(stopBody.type, 'run.stop')
    assert.equal(stopBody.target_swarm_id, 'primary-swarm')
    assert.equal(stopBody.run_id, 'run-1')
    assert.equal(Object.hasOwn(stopBody, 'session_id'), false)
    assert.equal(calls.filter((entry) => String(entry.input).includes('/run/stop')).length, 1)
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
