import test from 'node:test'
import assert from 'node:assert/strict'

import type { DesktopChatRoute } from '../services/chat-routing'

const managedHostRoute: DesktopChatRoute = {
  id: 'swarm:managed-swarm:binding:binding-managed',
  label: 'managed host',
  swarmId: 'managed-swarm',
  targetKind: 'host',
  targetRelationship: 'managed',
  hostSwarmId: 'host-swarm-id',
  hostSwarmName: 'Host Swarm',
  hostWorkspacePath: '/workspaces/host-swarm',
  hostWorkspaceName: 'host swarm',
  runtimeWorkspacePath: '/managed/workspace',
  workspaceBindingId: 'binding-managed',
}

async function withFetchStub(
  run: (calls: Array<{ input: RequestInfo | URL; init?: RequestInit }>) => Promise<void>,
): Promise<void> {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)
    if (url === '/v1/swarm/managed-hosts/sessions/stop') {
      return new Response(JSON.stringify({ ok: true }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    await run(calls)
  } finally {
    globalThis.fetch = originalFetch
  }
}

test('managed desktop create is disabled until managed Sessions API v2 exists', async () => {
  const { createSession } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await assert.rejects(
      createSession({
        workspacePath: '/frontend/device/path/workspace',
        workspaceName: 'workspace',
        mode: 'auto',
        agentName: 'swarm',
        preference: { provider: 'codex', model: 'gpt-5.4', thinking: 'medium', serviceTier: '', contextMode: '' },
        route: managedHostRoute,
        worktreeMode: 'on',
      }),
      /Managed-host v2 session create is not implemented yet\./,
    )

    assert.equal(calls.length, 0)
    assert.equal(calls.some((entry) => String(entry.input) === '/v1/swarm/managed-hosts/sessions/open'), false)
    assert.equal(calls.some((entry) => String(entry.input).startsWith('/v1/sessions')), false)
  })
})

test('managed desktop stop helper keeps managed-host stop path separate', async () => {
  const { stopSessionRun } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await stopSessionRun('session-managed', 'run-managed', managedHostRoute)

    assert.equal(calls.length, 1)
    assert.equal(String(calls[0]?.input), '/v1/swarm/managed-hosts/sessions/stop')
    const body = JSON.parse(String(calls[0]?.init?.body ?? '{}')) as Record<string, unknown>
    assert.equal(calls[0]?.init?.method, 'POST')
    assert.deepEqual(body, { type: 'run.stop', target_swarm_id: 'managed-swarm', session_id: 'session-managed', run_id: 'run-managed' })
  })
})

test('managed desktop create does not send target_swarm_id or legacy managed-host open body', async () => {
  const { createSession } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await assert.rejects(
      createSession({
        workspacePath: '/frontend/device/path/workspace',
        workspaceName: 'workspace',
        mode: 'auto',
        agentName: 'swarm',
        preference: { provider: 'codex', model: 'gpt-5.4', thinking: 'medium', serviceTier: '', contextMode: '' },
        route: managedHostRoute,
        worktreeMode: 'on',
      }),
      /Managed-host v2 session create is not implemented yet\./,
    )

    const bodies = calls.map((entry) => String(entry.init?.body ?? ''))
    assert.equal(bodies.some((body) => body.includes('target_swarm_id')), false)
    assert.equal(bodies.some((body) => body.includes('managed-swarm')), false)
  })
})
