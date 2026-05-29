import test from 'node:test'
import assert from 'node:assert/strict'

import type { DesktopChatRoute } from '../services/chat-routing'

const managedHostRoute: DesktopChatRoute = {
  id: 'swarm:managed-swarm:/managed/workspace',
  label: 'managed host',
  swarmId: 'managed-swarm',
  targetKind: 'host',
  targetRelationship: 'managed',
  hostSwarmId: 'host-swarm-id',
  hostSwarmName: 'Host Swarm',
  hostWorkspacePath: '/workspaces/host-swarm',
  hostWorkspaceName: 'host swarm',
  runtimeWorkspacePath: '/managed/workspace',
}

async function withFetchStub(
  run: (calls: Array<{ input: RequestInfo | URL; init?: RequestInit }>) => Promise<void>,
): Promise<void> {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)
    if (url === '/v1/swarm/managed-hosts/sessions/open') {
      return new Response(JSON.stringify({
        ok: true,
        session: {
          id: 'managed-session',
          title: 'Managed',
          workspace_path: '/managed/workspace',
          workspace_name: 'workspace',
          mode: 'auto',
          metadata: { swarm_managed_host_session: true, swarm_managed_host_swarm_id: 'managed-swarm' },
          created_at: 1,
          updated_at: 1,
        },
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (url === '/v1/swarm/managed-hosts/sessions/run') {
      return new Response(JSON.stringify({ ok: true, session_id: 'managed-session', run_id: 'run-managed', status: 'accepted' }), {
        status: 202,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url === '/v1/swarm/managed-hosts/sessions/message') {
      return new Response(JSON.stringify({ ok: true }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (url === '/v1/swarm/managed-hosts/sessions/stop') {
      return new Response(JSON.stringify({ ok: true }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    if (url.startsWith('/v1/sessions')) {
      throw new Error(`unexpected container/local session API: ${url}`)
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    await run(calls)
  } finally {
    globalThis.fetch = originalFetch
  }
}

test('managed host chat create with worktree on uses source workspace and managed-host endpoints', async () => {
  const { createSession, sendSessionMessage, startSessionRun, stopSessionRun } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await createSession({
      workspacePath: managedHostRoute.runtimeWorkspacePath,
      workspaceName: 'workspace',
      mode: 'auto',
      agentName: 'swarm',
      preference: { provider: 'codex', model: 'gpt-5.4', thinking: 'medium', serviceTier: '', contextMode: '' },
      route: managedHostRoute,
      worktreeMode: 'on',
    })
    await sendSessionMessage('managed-session', 'user', 'hello managed', managedHostRoute)
    await startSessionRun({ sessionId: 'managed-session', route: managedHostRoute, prompt: 'hello managed', agentName: 'swarm' })
    await stopSessionRun('managed-session', 'run-managed', managedHostRoute)

    const urls = calls.map((entry) => String(entry.input))
    assert.deepEqual(urls, [
      '/v1/swarm/managed-hosts/sessions/open',
      '/v1/swarm/managed-hosts/sessions/message',
      '/v1/swarm/managed-hosts/sessions/run',
      '/v1/swarm/managed-hosts/sessions/stop',
    ])

    const openBody = JSON.parse(String(calls[0]?.init?.body ?? '{}')) as Record<string, unknown>
    assert.equal(openBody.target_swarm_id, 'managed-swarm')
    assert.equal(openBody.workspace_path, '/workspaces/host-swarm')
    assert.equal(openBody.host_workspace_path, '/workspaces/host-swarm')
    assert.equal(openBody.runtime_workspace_path, '/managed/workspace')
    assert.equal(openBody.worktree_mode, 'on')

    const messageBody = JSON.parse(String(calls[1]?.init?.body ?? '{}')) as Record<string, unknown>
    assert.equal(messageBody.target_swarm_id, 'managed-swarm')
    assert.equal(messageBody.session_id, 'managed-session')

    const runBody = JSON.parse(String(calls[2]?.init?.body ?? '{}')) as Record<string, unknown>
    assert.equal(runBody.target_swarm_id, 'managed-swarm')
    assert.equal(runBody.session_id, 'managed-session')
  })
})
