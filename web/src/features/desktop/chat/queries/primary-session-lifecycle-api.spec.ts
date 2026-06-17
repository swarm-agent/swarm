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

test('primary desktop session metadata helper exports are removed for Desktop V3', async () => {
  const queries = await import('./chat-queries')
  assert.equal('fetchSessionMetadata' in queries, false)
  assert.equal('updateSessionMetadata' in queries, false)
})


test('primary desktop stop helper export is removed from legacy chat queries', async () => {
  const queries = await import('./chat-queries')
  assert.equal('stopSessionRun' in queries, false)
})
