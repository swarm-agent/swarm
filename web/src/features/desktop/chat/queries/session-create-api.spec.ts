import test from 'node:test'
import assert from 'node:assert/strict'

import type { DesktopChatRoute } from '../services/chat-routing'

const routedRoute: DesktopChatRoute = {
  id: 'swarm:child-swarm:binding:binding-primary',
  label: 'child swarm',
  swarmId: 'child-swarm',
  targetKind: 'local-container',
  targetRelationship: 'child',
  hostSwarmId: 'host-swarm-id',
  hostSwarmName: 'Host Swarm',
  hostWorkspacePath: '/host/device/path/swarm-go',
  hostWorkspaceName: 'swarm-go',
  runtimeWorkspacePath: '/container/device/path/swarm-go',
  workspaceBindingId: 'binding-primary',
}

async function withFetchStub(
  run: (calls: Array<{ input: RequestInfo | URL; init?: RequestInit }>) => Promise<void>,
): Promise<void> {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)
    if (url === '/v1/sessions?swarm_id=child-swarm' || url === '/v1/sessions?swarm_id=primary-swarm') {
      return new Response(JSON.stringify({
        ok: true,
        session: {
          id: 'routed-session',
          title: 'Routed',
          workspace_path: '/container/device/path/swarm-go',
          workspace_name: 'swarm-go',
          mode: 'auto',
          metadata: {},
          created_at: 1,
          updated_at: 1,
        },
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    await run(calls)
  } finally {
    globalThis.fetch = originalFetch
  }
}

test('routed session create sends workspace identity and binding without path authority', async () => {
  const { createSession } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await createSession({
      title: 'Routed',
      workspacePath: '/frontend/device/path/swarm-go',
      workspaceName: 'swarm-go',
      mode: 'auto',
      agentName: 'swarm',
      preference: { provider: 'codex', model: 'gpt-5.4', thinking: 'medium', serviceTier: '', contextMode: '' },
      route: routedRoute,
      worktreeMode: 'off',
    })

    assert.equal(String(calls[0]?.input), '/v1/sessions?swarm_id=child-swarm')
    const body = JSON.parse(String(calls[0]?.init?.body ?? '{}')) as Record<string, unknown>
    assert.equal(body.workspace_name, 'swarm-go')
    assert.equal(body.workspace_binding_id, 'binding-primary')
    assert.equal(Object.hasOwn(body, 'workspace_path'), false)
    assert.equal(Object.hasOwn(body, 'host_workspace_path'), false)
    assert.equal(Object.hasOwn(body, 'runtime_workspace_path'), false)
  })
})


test('self host route with swarm identity does not use managed-host endpoint or path authority', async () => {
  const { createSession } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const strictRoute: DesktopChatRoute = {
      ...routedRoute,
      id: 'swarm:primary-swarm:binding:binding-primary-self',
      label: 'primary self',
      swarmId: 'primary-swarm',
      targetKind: 'host',
      targetRelationship: 'self',
      workspaceBindingId: 'binding-primary-self',
      requiresWorkspaceBinding: true,
    }
    await createSession({
      title: 'Routed',
      workspacePath: '/frontend/device/path/swarm-go',
      workspaceName: 'swarm-go',
      mode: 'auto',
      agentName: 'swarm',
      preference: { provider: 'codex', model: 'gpt-5.4', thinking: 'medium', serviceTier: '', contextMode: '' },
      route: strictRoute,
      worktreeMode: 'off',
    })

    assert.equal(String(calls[0]?.input), '/v1/sessions?swarm_id=primary-swarm')
    const body = JSON.parse(String(calls[0]?.init?.body ?? '{}')) as Record<string, unknown>
    assert.equal(body.workspace_binding_id, 'binding-primary-self')
    assert.equal(Object.hasOwn(body, 'target_swarm_id'), false)
    assert.equal(Object.hasOwn(body, 'workspace_path'), false)
    assert.equal(Object.hasOwn(body, 'host_workspace_path'), false)
    assert.equal(Object.hasOwn(body, 'runtime_workspace_path'), false)
  })
})
