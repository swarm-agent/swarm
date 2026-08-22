import assert from 'node:assert/strict'
import test from 'node:test'

import {
  normalizeDesktopV3RoutedSessionStartResponse,
  postDesktopV3BackgroundRouterSessionStart,
  postDesktopV3RoutedSessionStart,
} from './write-api'

const originalFetch = globalThis.fetch
const workspaceAuthority = {
  workspace_path: '/workspace/source', host_workspace_path: '/workspace/source', runtime_workspace_path: '/workspace/runtime',
  workspace_binding_id: 'binding-self', swarm_id: 'swarm-self', target_kind: 'host' as const, target_relationship: 'self' as const,
}

function routedResponse(overrides: Record<string, unknown> = {}) {
  const session = {
    id: 'session-routed',
    workspace_path: '/workspace/runtime',
    workspace_name: 'workspace',
    title: 'Canonical routed title',
    mode: 'plan',
    created_at: 1,
    updated_at: 2,
    message_count: 1,
    last_message_at: 2,
  }
  const projection = {
    session_id: session.id,
    last_event_seq: 2,
    projection_high_watermark_seq: 2,
    updated_at: 2,
  }
  const firstMessage = {
    id: 'message-first',
    session_id: session.id,
    global_seq: 2,
    role: 'user',
    content: 'Build the routed flow',
    created_at: 2,
  }
  return {
    ok: true,
    session_id: session.id,
    title: session.title,
    starting_mode: session.mode,
    replayed: false,
    session,
    session_view: {
      identity: {
        session_id: session.id,
        title: session.title,
        source_workspace_name: 'workspace',
        source_workspace_path: '/workspace/source',
        runtime_workspace_path: session.workspace_path,
        worktree_enabled: true,
        worktree_branch: 'agent/routed-flow',
      },
      agentic_settings: {},
      media_capability: { status: 'unavailable', contract_version: 1, capabilities: [] },
      pending_permissions: [],
    },
    first_message: firstMessage,
    projection,
    mutation: {
      session_id: session.id,
      message: firstMessage,
      projection,
    },
    ...overrides,
  }
}

test('postDesktopV3RoutedSessionStart sends only routed input authority with stable idempotency', async () => {
  const calls: Array<{ url: string; init?: RequestInit }> = []
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init })
    return new Response(JSON.stringify(routedResponse()), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch

  try {
    const response = await postDesktopV3RoutedSessionStart({
      ...workspaceAuthority,
      input: '  Build the routed flow  ',
      client_request_id: 'desktop-routed:stable-1',
      agent_name: ' swarm ',
      metadata: { source: 'desktop-v3' },
      managed_worktree_requested: true,
      plan_mode_requested: true,
      media: [{ staging_id: 'staged-1', modality: 'image', file_type: 'png' }],
    })
    assert.equal(response.session_id, 'session-routed')
    assert.equal(response.starting_mode, 'plan')
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.equal(calls.length, 1)
  assert.equal(calls[0]?.url, '/v3/sessions:routed')
  assert.equal(calls[0]?.init?.method, 'POST')
  const headers = new Headers(calls[0]?.init?.headers)
  assert.equal(headers.get('Content-Type'), 'application/json')
  assert.equal(headers.get('Idempotency-Key'), 'desktop-routed:stable-1')
  const body = JSON.parse(String(calls[0]?.init?.body)) as Record<string, unknown>
  assert.deepEqual(body, {
    ...workspaceAuthority,
    input: 'Build the routed flow',
    client_request_id: 'desktop-routed:stable-1',
    idempotency_key: 'desktop-routed:stable-1',
    agent_name: 'swarm',
    metadata: { source: 'desktop-v3' },
    managed_worktree_requested: true,
    plan_mode_requested: true,
    media: [{ staging_id: 'staged-1', modality: 'image', file_type: 'png' }],
  })
  for (const forbidden of [
    'title', 'mode', 'starting_mode',
    'preference', 'model', 'model_profile', 'favorite_id', 'branch',
    'worktree_mode', 'worktree_branch_name',
  ]) {
    assert.equal(Object.hasOwn(body, forbidden), false, `request must not preselect ${forbidden}`)
  }
})

test('background Router start uses its dedicated endpoint without exposing worktree disablement', async () => {
  const calls: Array<{ url: string; init?: RequestInit }> = []
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(input), init })
    return new Response(JSON.stringify(routedResponse({
      starting_mode: 'auto',
      session: { ...(routedResponse().session as Record<string, unknown>), mode: 'auto' },
    })), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch

  try {
    await postDesktopV3BackgroundRouterSessionStart({
      ...workspaceAuthority,
      input: '  Implement in the background  ',
      client_request_id: 'desktop-background-router:stable-1',
      agent_name: ' swarm ',
      metadata: { source: 'desktop-v3-task-command' },
      plan_mode_requested: false,
    })
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.equal(calls.length, 1)
  assert.equal(calls[0]?.url, '/v3/sessions:background-router')
  const body = JSON.parse(String(calls[0]?.init?.body)) as Record<string, unknown>
  assert.deepEqual(body, {
    ...workspaceAuthority,
    input: 'Implement in the background',
    client_request_id: 'desktop-background-router:stable-1',
    idempotency_key: 'desktop-background-router:stable-1',
    agent_name: 'swarm',
    metadata: { source: 'desktop-v3-task-command' },
    plan_mode_requested: false,
  })
  assert.equal(Object.hasOwn(body, 'managed_worktree_requested'), false)
})

test('routed start accepts staging IDs without inventing route selections', async () => {
  let body: Record<string, unknown> = {}
  globalThis.fetch = (async (_input: RequestInfo | URL, init?: RequestInit) => {
    body = JSON.parse(String(init?.body)) as Record<string, unknown>
    return new Response(JSON.stringify(routedResponse({ replayed: true })), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch

  try {
    const response = await postDesktopV3RoutedSessionStart({
      ...workspaceAuthority,
      input: 'Replay this',
      client_request_id: 'desktop-routed:stable-2',
      agent_name: 'swarm',
      managed_worktree_requested: false,
      plan_mode_requested: false,
      staging_ids: ['staged-2'],
    })
    assert.equal(response.replayed, true)
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.deepEqual(body.staging_ids, ['staged-2'])
  assert.equal(body.media, undefined)
})

test('routed response normalization rejects mismatched canonical durable state', () => {
  const mismatch = routedResponse({
    projection: {
      session_id: 'different-session',
      last_event_seq: 2,
      projection_high_watermark_seq: 2,
      updated_at: 2,
    },
  })
  assert.throws(
    () => normalizeDesktopV3RoutedSessionStartResponse(mismatch),
    /projection does not match session_id/,
  )
})

test('routed start rejects missing agent_name before transport', async () => {
  await assert.rejects(
    postDesktopV3RoutedSessionStart({
      ...workspaceAuthority,
      input: 'Do work',
      client_request_id: 'desktop-routed:missing-agent',
      agent_name: '   ',
      managed_worktree_requested: false,
      plan_mode_requested: false,
    }),
    /agent_name/,
  )
})

test('routed start rejects conflicting idempotency identities before transport', async () => {
  await assert.rejects(
    postDesktopV3RoutedSessionStart({
      ...workspaceAuthority,
      input: 'Do work',
      client_request_id: 'desktop-routed:one',
      idempotency_key: 'desktop-routed:two',
      agent_name: 'swarm',
      managed_worktree_requested: false,
      plan_mode_requested: false,
    }),
    /one stable client_request_id\/idempotency identity/,
  )
})
