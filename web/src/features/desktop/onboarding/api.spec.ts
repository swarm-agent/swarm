import assert from 'node:assert/strict'
import test from 'node:test'

import { buildDesktopOnboardingPayload, buildWorkspaceOnboardingSessionPayload, startWorkspaceOnboardingSession } from './api'

async function withMockFetch<T>(handler: typeof fetch, run: () => Promise<T>): Promise<T> {
  const previous = globalThis.fetch
  globalThis.fetch = handler
  try {
    return await run()
  } finally {
    globalThis.fetch = previous
  }
}

test('desktop onboarding payload keeps username and swarm name separate without swarm mode', () => {
  assert.deepEqual(buildDesktopOnboardingPayload({
    username: 'alice',
    swarmName: 'Alice Laptop',
  }), {
    username: 'alice',
    swarm_name: 'Alice Laptop',
  })
})

test('workspace onboarding assistant payload binds one exact pre-admission folder without workspace authority fields', () => {
  const payload = buildWorkspaceOnboardingSessionPayload({
    path: ' /workspace/existing ',
    expectedResolvedPath: ' /workspace/existing ',
    clientRequestId: ' onboarding-1 ',
    prompt: ' Review the folder ',
  })

  assert.deepEqual(payload, {
    path: '/workspace/existing',
    expected_resolved_path: '/workspace/existing',
    client_request_id: 'onboarding-1',
    input: 'Review the folder',
  })
  assert.equal(Object.prototype.hasOwnProperty.call(payload, 'workspace_binding_id'), false)
  assert.equal(Object.prototype.hasOwnProperty.call(payload, 'swarm_id'), false)
  assert.equal(Object.prototype.hasOwnProperty.call(payload, 'agent_name'), false)
})

test('workspace onboarding assistant payload rejects missing stale-selection authority', () => {
  assert.throws(() => buildWorkspaceOnboardingSessionPayload({
    path: '/workspace/existing',
    expectedResolvedPath: '',
    clientRequestId: 'onboarding-2',
  }), /selected folder and its current canonical path/)
})

test('workspace onboarding assistant rejects a successful response with crossed session identity', async () => {
  await withMockFetch(async () => new Response(JSON.stringify({
    ok: true,
    session_id: 'session-1',
    repository: { state: 'needs_assisted_setup', path: '/workspace/existing' },
    session: { id: 'session-2' },
    first_message: { session_id: 'session-1' },
    projection: { session_id: 'session-1' },
    mutation: { session_id: 'session-1' },
  }), { status: 200, headers: { 'Content-Type': 'application/json' } }), async () => {
    await assert.rejects(startWorkspaceOnboardingSession({
      path: '/workspace/existing',
      expectedResolvedPath: '/workspace/existing',
      clientRequestId: 'onboarding-crossed',
    }), /inconsistent session identity/)
  })
})

test('workspace onboarding assistant rejects a response that is no longer eligible for assisted setup', async () => {
  await withMockFetch(async () => new Response(JSON.stringify({
    ok: true,
    session_id: 'session-1',
    repository: { state: 'ready', path: '/workspace/existing', head_commit: 'abc' },
    session: { id: 'session-1' },
    first_message: { session_id: 'session-1' },
    projection: { session_id: 'session-1' },
    mutation: { session_id: 'session-1' },
  }), { status: 200, headers: { 'Content-Type': 'application/json' } }), async () => {
    await assert.rejects(startWorkspaceOnboardingSession({
      path: '/workspace/existing',
      expectedResolvedPath: '/workspace/existing',
      clientRequestId: 'onboarding-ready',
    }), /eligible existing-file folder/)
  })
})

test('workspace onboarding assistant preserves provider and permission failures as rejected starts', async () => {
  await withMockFetch(async () => new Response(JSON.stringify({
    ok: false,
    code: 'workspace_onboarding_unavailable',
    error: 'workspace onboarding requires a configured Swarm Action provider and model',
  }), { status: 503, headers: { 'Content-Type': 'application/json' } }), async () => {
    await assert.rejects(startWorkspaceOnboardingSession({
      path: '/workspace/existing',
      expectedResolvedPath: '/workspace/existing',
      clientRequestId: 'onboarding-provider-failed',
    }), /configured Swarm Action provider and model/)
  })
})

test('desktop onboarding payload never invents team fields or swarm mode', () => {
  const payload = buildDesktopOnboardingPayload({
    username: 'alice',
    swarmName: 'Alice Laptop',
  })

  assert.equal(Object.prototype.hasOwnProperty.call(payload, 'team'), false)
  assert.equal(Object.prototype.hasOwnProperty.call(payload, 'team_id'), false)
  assert.equal(Object.prototype.hasOwnProperty.call(payload, 'team_name'), false)
  assert.equal(Object.prototype.hasOwnProperty.call(payload, 'swarm' + '_mode'), false)
  assert.equal(Object.prototype.hasOwnProperty.call(payload, 'child'), false)
  assert.equal(Object.prototype.hasOwnProperty.call(payload, 'advertise_host'), false)
  assert.equal(Object.prototype.hasOwnProperty.call(payload, 'peer_transport_port'), false)
})
