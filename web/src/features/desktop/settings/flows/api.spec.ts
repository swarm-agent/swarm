import test from 'node:test'
import assert from 'node:assert/strict'

import type { CreateFlowInput } from './api'

async function withFetchStub(run: (calls: Array<{ input: RequestInfo | URL; init?: RequestInit }>) => Promise<void>): Promise<void> {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)
    if (url === '/v3/flows?limit=200') {
      return new Response(JSON.stringify({ ok: true, flows: [] }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url === '/v3/flows' && init?.method === 'POST') {
      const body = JSON.parse(String(init.body))
      return new Response(JSON.stringify({
        ok: true,
        flow: {
          definition: {
            flow_id: 'flow-ui',
            revision: 1,
            name: body.name,
            enabled: body.enabled,
            target: body.target,
            agent: body.agent,
            workspace: body.workspace,
            schedule: body.schedule,
            catch_up_policy: body.catch_up_policy,
            intent: body.intent,
            created_at: '2025-01-01T00:00:00Z',
            updated_at: '2025-01-01T00:00:00Z',
          },
          target_detail: { swarm_id: 'host-swarm-id', kind: 'self', name: 'Local', online: true, selectable: true, current: true },
          agent_detail: { name: 'memory', mode: 'background', enabled: true },
          workspace_detail: { workspace_path: '/tmp/workspace' },
          assignment_statuses: [],
          history: [],
          outbox: [],
          history_count: 0,
        },
        result: { pending_sync: false, delivered: true },
      }), {
        status: 201,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url === '/v3/flows/flow-ui' && (!init?.method || init.method === 'GET')) {
      return new Response(JSON.stringify({
        ok: true,
        definition: {
          flow_id: 'flow-ui',
          revision: 1,
          name: 'UI flow',
          enabled: true,
          target: { kind: 'self', name: 'local' },
          agent: { profile_name: 'memory', profile_mode: 'background' },
          workspace: { workspace_path: '.' },
          schedule: { cadence: 'daily', time: '09:00', timezone: 'UTC' },
          catch_up_policy: { mode: 'once' },
          intent: { prompt: 'refresh memory' },
        },
        assignment_statuses: [],
        history: [],
        outbox: [],
        history_count: 0,
      }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url === '/v3/flows/flow-ui/run-now' && init?.method === 'POST') {
      return new Response(JSON.stringify({ ok: true, flow: { definition: { flow_id: 'flow-ui', revision: 1, name: 'UI flow', enabled: true, target: { kind: 'self', name: 'local' }, agent: { profile_name: 'memory', profile_mode: 'background' }, workspace: { workspace_path: '.' }, schedule: { cadence: 'daily', time: '09:00', timezone: 'UTC' }, catch_up_policy: { mode: 'once' }, intent: { prompt: 'refresh memory' } }, assignment_statuses: [], history: [], outbox: [], history_count: 0 }, run: { command_id: 'cmd-run', pending_sync: false } }), {
        status: 202,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url === '/v3/flows/flow-ui' && init?.method === 'DELETE') {
      return new Response(JSON.stringify({ ok: true, flow: { definition: { flow_id: 'flow-ui', revision: 1, name: 'UI flow', enabled: true, target: { kind: 'self', name: 'local' }, agent: { profile_name: 'memory', profile_mode: 'background' }, workspace: { workspace_path: '.' }, schedule: { cadence: 'daily', time: '09:00', timezone: 'UTC' }, catch_up_policy: { mode: 'once' }, intent: { prompt: 'refresh memory' }, deleted_at: '2025-01-01T00:00:00Z' }, assignment_statuses: [], history: [], outbox: [], history_count: 0 } }), {
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

test('flows API uses canonical v3 endpoints', async () => {
  const { createFlow, deleteFlow, fetchFlow, fetchFlows, runFlowNow } = await import('./api')

  await withFetchStub(async (calls) => {
    await fetchFlows()
    const input: CreateFlowInput = {
      name: 'UI flow',
      enabled: true,
      target: { kind: 'self', name: 'local' },
      agent: { profile_name: 'memory', profile_mode: 'background' },
      workspace: { workspace_path: '.' },
      schedule: { cadence: 'daily', time: '09:00', timezone: 'UTC' },
      catch_up_policy: { mode: 'once' },
      intent: { prompt: 'refresh memory' },
    }
    await createFlow(input)
    await fetchFlow('flow-ui')
    await runFlowNow('flow-ui')
    await deleteFlow('flow-ui')

    assert.equal(String(calls[0]?.input), '/v3/flows?limit=200')
    assert.equal(String(calls[1]?.input), '/v3/flows')
    assert.equal(calls[1]?.init?.method, 'POST')
    assert.equal(new Headers(calls[1]?.init?.headers).get('Content-Type'), 'application/json')
    const createBody = JSON.parse(String(calls[1]?.init?.body ?? '{}')) as Record<string, unknown>
    assert.equal(createBody.name, 'UI flow')
    assert.deepEqual(createBody.agent, { profile_name: 'memory', profile_mode: 'background' })
    assert.equal(String(calls[2]?.input), '/v3/flows/flow-ui')
    assert.equal(calls[3]?.init?.method, 'POST')
    assert.equal(String(calls[3]?.input), '/v3/flows/flow-ui/run-now')
    assert.equal(String(calls[4]?.input), '/v3/flows/flow-ui')
    assert.equal(calls[4]?.init?.method, 'DELETE')
  })
})
