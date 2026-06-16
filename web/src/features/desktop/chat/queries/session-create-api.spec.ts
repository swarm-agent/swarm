import test from 'node:test'
import assert from 'node:assert/strict'

import type { DesktopChatRoute } from '../services/chat-routing'

const localContainerRoute: DesktopChatRoute = {
  id: 'swarm:child-swarm:binding:binding-child',
  label: 'child swarm',
  swarmId: 'child-swarm',
  targetKind: 'local-container',
  targetRelationship: 'child',
  hostSwarmId: 'host-swarm-id',
  hostSwarmName: 'Host Swarm',
  hostWorkspacePath: '/host/device/path/swarm-go',
  hostWorkspaceName: 'swarm-go',
  runtimeWorkspacePath: '/container/device/path/swarm-go',
  workspaceBindingId: 'binding-child',
  workspaceName: 'swarm-go',
}

const primaryRoute: DesktopChatRoute = {
  ...localContainerRoute,
  id: 'swarm:primary-swarm:binding:binding-primary',
  label: 'primary self',
  swarmId: 'primary-swarm',
  targetKind: 'host',
  targetRelationship: 'self',
  runtimeWorkspacePath: '/host/device/path/swarm-go',
  workspaceBindingId: 'binding-primary',
  requiresWorkspaceBinding: true,
}

const forbiddenCreateBodyKeys = [
  'host_workspace_path',
  'runtime_workspace_path',
  'target_swarm_id',
  'backend_url',
  'child_backend_url',
  'target_backend_url',
]

async function withFetchStub(
  run: (calls: Array<{ input: RequestInfo | URL; init?: RequestInit }>) => Promise<void>,
): Promise<void> {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)
    if (url === '/v3/sessions' || url === '/v2/sessions/local-containers') {
      const body = JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>
      const primary = url === '/v3/sessions'
      return new Response(JSON.stringify({
        ok: true,
        session: {
          id: primary ? 'primary-session' : 'local-session',
          title: String(body.title ?? ''),
          workspace_path: primary ? String(body.workspace_path ?? '') : '/container/device/path/swarm-go',
          workspace_name: primary ? String(body.workspace_name ?? '') : 'swarm-go',
          mode: String(body.mode ?? 'auto'),
          metadata: { server_metadata: true },
          session_api: primary ? 'v3' : undefined,
          last_event_seq: primary ? 1 : undefined,
          projection_high_watermark_seq: primary ? 1 : undefined,
          created_at: 1,
          updated_at: 1,
        },
        projection: primary ? {
          session_id: 'primary-session',
          last_event_seq: 1,
          projection_high_watermark_seq: 1,
          updated_at: 1,
        } : undefined,
        session_execution: primary ? undefined : {
          session_id: 'local-session',
          swarm_id: body.swarm_id,
          workspace_binding_id: body.workspace_binding_id,
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

function requestBody(call: { init?: RequestInit } | undefined): Record<string, unknown> {
  return JSON.parse(String(call?.init?.body ?? '{}')) as Record<string, unknown>
}

function assertNoForbiddenCreateBodyFields(url: string, body: Record<string, unknown>) {
  assert.equal(url.includes('?swarm_id='), false)
  for (const key of forbiddenCreateBodyKeys) {
    assert.equal(Object.hasOwn(body, key), false, `body must not include ${key}`)
  }
}

function assertNoV2CreateAuthorityFields(url: string, body: Record<string, unknown>) {
  assertNoForbiddenCreateBodyFields(url, body)
  assert.equal(Object.hasOwn(body, 'workspace_name'), false)
  assert.equal(Object.hasOwn(body, 'workspace_path'), false)
}

test('primary/self host route creates via Sessions API v3 primary endpoint', async () => {
  const { createSession } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await createSession({
      title: 'Primary',
      workspacePath: '/frontend/device/path/swarm-go',
      workspaceName: 'swarm-go',
      mode: 'auto',
      agentName: 'swarm',
      preference: { provider: 'codex', model: 'gpt-5.4', thinking: 'medium', serviceTier: 'flex', contextMode: 'large' },
      route: primaryRoute,
      worktreeMode: 'on',
      worktreeUseCurrentBranch: false,
      worktreeBaseBranch: 'dev',
      worktreeBranchName: 'agent/session-v2',
    })

    assert.equal(String(calls[0]?.input), '/v3/sessions')
    const body = requestBody(calls[0])
    assert.equal(Object.hasOwn(body, 'client_request_id'), true)
    assert.equal(body.swarm_id, 'primary-swarm')
    assert.equal(body.workspace_binding_id, 'binding-primary')
    assert.equal(body.workspace_path, '/frontend/device/path/swarm-go')
    assert.equal(body.workspace_name, 'swarm-go')
    assert.equal(body.title, 'Primary')
    assert.equal(body.mode, 'auto')
    assert.equal(body.agent_name, 'swarm')
    assert.equal(body.worktree_mode, 'on')
    assert.equal(body.worktree_use_current_branch, false)
    assert.equal(body.worktree_base_branch, 'dev')
    assert.equal(body.worktree_branch_name, 'agent/session-v2')
    assert.deepEqual(body.preference, {
      provider: 'codex',
      model: 'gpt-5.4',
      thinking: 'medium',
      service_tier: 'flex',
      context_mode: 'large',
    })
    assertNoForbiddenCreateBodyFields(String(calls[0]?.input), body)
    const session = await createSession({
      title: 'Primary cursor',
      workspacePath: '/frontend/device/path/swarm-go',
      workspaceName: 'swarm-go',
      mode: 'auto',
      preference: { provider: '', model: '', thinking: '', serviceTier: '', contextMode: '' },
      route: primaryRoute,
    })
    assert.equal(session.sessionApi, 'v3')
    assert.equal(session.lastEventSeq, 1)
    assert.equal(session.projectionHighWatermarkSeq, 1)
  })
})

test('local-container child route is rejected without fallback create request', async () => {
  const { createSession } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await assert.rejects(
      createSession({
        title: 'Local container',
        workspacePath: '/frontend/device/path/swarm-go',
        workspaceName: 'swarm-go',
        mode: 'auto',
        agentName: 'swarm',
        preference: { provider: 'codex', model: 'gpt-5.4', thinking: 'medium', serviceTier: '', contextMode: '' },
        route: localContainerRoute,
        worktreeMode: 'off',
      }),
      /primary self V3 target/,
    )
    assert.equal(calls.length, 0)
  })
})

test('missing workspaceBindingId fails closed without v1 fallback or workspace_path payload', async () => {
  const { createSession } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await assert.rejects(
      createSession({
        title: 'Missing binding',
        workspacePath: '/frontend/device/path/swarm-go',
        workspaceName: 'swarm-go',
        mode: 'auto',
        agentName: 'swarm',
        preference: { provider: 'codex', model: 'gpt-5.4', thinking: 'medium', serviceTier: '', contextMode: '' },
        route: { ...localContainerRoute, workspaceBindingId: '' },
      }),
      /workspace_binding_id/,
    )
    assert.equal(calls.length, 0)
  })
})

test('metadata sanitization preserves annotations and removes route authority keys', async () => {
  const { createSession } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await createSession({
      title: 'Metadata',
      workspacePath: '/frontend/device/path/swarm-go',
      workspaceName: 'swarm-go',
      mode: 'auto',
      agentName: 'swarm',
      preference: { provider: 'codex', model: 'gpt-5.4', thinking: 'medium', serviceTier: '', contextMode: '' },
      route: primaryRoute,
      metadata: {
        safe_annotation: 'kept',
        launch_source: 'test',
        workspace_name: 'forbidden',
        workspace_path: '/forbidden',
        host_workspace_path: '/forbidden-host',
        runtime_workspace_path: '/forbidden-runtime',
        backend_url: 'http://127.0.0.1:1',
        child_backend_url: 'http://127.0.0.1:2',
        target_backend_url: 'http://127.0.0.1:3',
        target_swarm_id: 'forbidden-target',
        next_hop_swarm_id: 'forbidden-next-hop',
        next_hop_backend_url: 'http://127.0.0.1:4',
        swarm_route_label: 'forbidden-route',
        swarm_routed_workspace_binding_id: 'forbidden-routed',
        swarm_managed_host_swarm_id: 'forbidden-managed',
        swarm_v2_execution_id: 'forbidden-v2',
        local_workspace_binding_id: 'forbidden-local-binding',
        hosted_session_id: 'forbidden-hosted-session',
        managed_host_id: 'forbidden-managed-host',
        owner_transport: 'forbidden-transport',
        custom_routing_hint: 'forbidden-routing-looking-key',
        custom_path_hint: '/forbidden-path-looking-key',
      },
    })

    const body = requestBody(calls[0])
    const metadata = body.metadata as Record<string, unknown>
    assert.deepEqual(metadata, { safe_annotation: 'kept', launch_source: 'test' })
  })
})

test('create response accepts session_execution as returned server state only', async () => {
  const { createSession } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const session = await createSession({
      title: 'Response',
      workspacePath: '/frontend/device/path/swarm-go',
      workspaceName: 'swarm-go',
      mode: 'auto',
      agentName: 'swarm',
      preference: { provider: 'codex', model: 'gpt-5.4', thinking: 'medium', serviceTier: '', contextMode: '' },
      route: primaryRoute,
    })

    assert.equal(session.id, 'primary-session')
    assert.equal(calls.length, 1)
    const body = requestBody(calls[0])
    assert.equal(Object.hasOwn(body, 'session_execution'), false)
    assert.equal(body.swarm_id, 'primary-swarm')
    assert.equal(body.workspace_binding_id, 'binding-primary')
  })
})
