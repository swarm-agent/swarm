import test from 'node:test'
import assert from 'node:assert/strict'

async function withFetchStub(run: (calls: Array<{ input: RequestInfo | URL; init?: RequestInit }>) => Promise<void>): Promise<void> {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)
    if (url === '/v2/flows?limit=200') {
      return new Response(JSON.stringify({ ok: true, flows: [] }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url === '/v2/flows' && init?.method === 'POST') {
      return new Response(JSON.stringify({
        ok: true,
        flow: {
          definition: {
            flow_id: 'flow-ui',
            revision: 1,
            assignment: JSON.parse(String(init.body)),
            created_at: '2025-01-01T00:00:00Z',
            updated_at: '2025-01-01T00:00:00Z',
          },
          target_detail: { swarm_id: 'host-swarm-id', kind: 'self', name: 'Local', online: true, selectable: true, current: true },
          agent_detail: { name: 'memory', mode: 'background', enabled: true },
          workspace_detail: { workspace_path: '/tmp/workspace' },
          assignment_statuses: [],
          history: [],
        },
      }), {
        status: 201,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url === '/v2/flows/flow-ui/run-now' && init?.method === 'POST') {
      return new Response(JSON.stringify({ ok: true, run: { command_id: 'cmd-run', pending_sync: false } }), {
        status: 202,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url === '/v2/flows/flow-ui' && init?.method === 'DELETE') {
      return new Response(JSON.stringify({ ok: true }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    await run(calls)
  } finally {
    globalThis.fetch = originalFetch
  }
}

test('flows API uses native host-only v2 endpoints', async () => {
  const { createFlow, deleteFlow, fetchFlows, runFlowNow } = await import('./api')

  await withFetchStub(async (calls) => {
    await fetchFlows()
    await createFlow({
      name: 'UI flow',
      enabled: true,
      target: { kind: 'self', name: 'local' },
      agent: { target_kind: 'background', target_name: 'memory' },
      workspace: { workspace_path: '.' },
      schedule: { cadence: 'daily', time: '09:00', timezone: 'UTC' },
      catch_up_policy: { mode: 'once' },
      intent: { prompt: 'refresh memory' },
    })
    await runFlowNow('flow-ui')
    await deleteFlow('flow-ui')

    assert.equal(String(calls[0]?.input), '/v2/flows?limit=200')
    assert.equal(String(calls[1]?.input), '/v2/flows')
    assert.equal(calls[1]?.init?.method, 'POST')
    assert.equal(new Headers(calls[1]?.init?.headers).get('Content-Type'), 'application/json')
    const createBody = JSON.parse(String(calls[1]?.init?.body ?? '{}')) as Record<string, unknown>
    assert.equal(createBody.name, 'UI flow')
    assert.equal(String(calls[2]?.input), '/v2/flows/flow-ui/run-now')
    assert.equal(calls[2]?.init?.method, 'POST')
    assert.equal(String(calls[3]?.input), '/v2/flows/flow-ui')
    assert.equal(calls[3]?.init?.method, 'DELETE')
  })
})
