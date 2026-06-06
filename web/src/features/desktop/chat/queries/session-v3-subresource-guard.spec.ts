import test from 'node:test'
import assert from 'node:assert/strict'

async function assertLegacyHelperUsesV2(action: () => Promise<unknown>, expectedPath: string): Promise<void> {
  const originalFetch = globalThis.fetch
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    return new Response(JSON.stringify(legacyResponseForPath(String(input))), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch
  try {
    await action()
  } finally {
    globalThis.fetch = originalFetch
  }
  assert.equal(String(calls[0]?.input ?? ''), expectedPath)
}

function legacyResponseForPath(path: string): unknown {
  if (path.endsWith('/preference')) {
    return { ok: true, preference: { model: 'gpt-5.4' } }
  }
  if (path.endsWith('/mode')) {
    return { ok: true, mode: 'plan' }
  }
  if (path.endsWith('/metadata')) {
    return { ok: true, session: { id: 'session-raw' }, metadata: {} }
  }
  if (path.endsWith('/codex')) {
    return { ok: true }
  }
  if (path.endsWith('/run/stream')) {
    return { ok: true, run_id: 'run-1' }
  }
  if (path.includes('/permissions/perm-1/resolve')) {
    return { ok: true, permission: { id: 'perm-1', status: 'resolved' } }
  }
  if (path.endsWith('/usage')) {
    return { ok: true, usage_summary: null }
  }
  if (path.endsWith('/plans/active')) {
    return { ok: true, has_active: false, active_plan: null }
  }
  if (path.includes('/plans/plan-1/history')) {
    return { ok: true, revisions: [] }
  }
  if (path.includes('/plans/plan-1')) {
    return { ok: true, plan: { id: 'plan-1' } }
  }
  if (path.includes('/plans?limit=100')) {
    return { ok: true, active_plan_id: '', plans: [] }
  }
  if (path.endsWith('/plans')) {
    return { ok: true, plan: { id: 'plan-1' } }
  }
  if (path.endsWith('/permissions/resolve_all')) {
    return { ok: true, resolved: [] }
  }
  if (path.includes('/permissions?status=pending')) {
    return { ok: true, permissions: [] }
  }
  return { ok: true }
}

const primaryRoute = {
  id: 'primary',
  label: 'Primary',
  swarmId: 'primary-swarm',
  targetKind: 'host',
  targetRelationship: 'self',
  hostSwarmId: 'primary-swarm',
  hostSwarmName: 'primary',
  hostWorkspacePath: '/repo',
  hostWorkspaceName: 'repo',
  runtimeWorkspacePath: '/repo',
}

test('legacy lifecycle helpers do not infer V3 from session ID shape', async () => {
  const {
    fetchSessionPreference,
    updateSessionPreference,
    fetchSessionMode,
    updateSessionMode,
    fetchSessionMetadata,
    updateSessionMetadata,
    fetchSessionCodexConfig,
    updateSessionCodexConfig,
  } = await import('./chat-queries')

  await assertLegacyHelperUsesV2(() => fetchSessionPreference('session-raw'), '/v2/sessions/session-raw/preference')
  await assertLegacyHelperUsesV2(() => updateSessionPreference('session-raw', { model: 'gpt-5.4' }), '/v2/sessions/session-raw/preference')
  await assertLegacyHelperUsesV2(() => fetchSessionMode('session-raw'), '/v2/sessions/session-raw/mode')
  await assertLegacyHelperUsesV2(() => updateSessionMode('session-raw', 'plan'), '/v2/sessions/session-raw/mode')
  await assertLegacyHelperUsesV2(() => fetchSessionMetadata('session-raw'), '/v2/sessions/session-raw/metadata')
  await assertLegacyHelperUsesV2(() => updateSessionMetadata('session-raw', { ticket: 'T-1' }), '/v2/sessions/session-raw/metadata')
  await assertLegacyHelperUsesV2(() => fetchSessionCodexConfig('session-raw'), '/v2/sessions/session-raw/codex')
  await assertLegacyHelperUsesV2(() => updateSessionCodexConfig('session-raw', { serviceTier: 'flex' }), '/v2/sessions/session-raw/codex')
})

test('legacy run, plan, permission, and usage helpers do not infer V3 from session ID shape', async () => {
  const {
    startSessionRun,

    resolveSessionPermission,
    fetchSessionUsageSummary,
    fetchActiveSessionPlan,
    activateSessionPlan,
    fetchSessionPlans,
    fetchSessionPlan,
    saveSessionPlan,
    fetchSessionPlanHistory,
    resolveAllSessionPermissions,
    fetchSessionPendingPermissions,
  } = await import('./chat-queries')

  await assertLegacyHelperUsesV2(() => startSessionRun({ sessionId: 'session-raw', prompt: 'run', route: primaryRoute }), '/v2/sessions/session-raw/run/stream')
  await assertLegacyHelperUsesV2(() => resolveSessionPermission('session-raw', 'perm-1', 'approve', 'ok'), '/v2/sessions/session-raw/permissions/perm-1/resolve')
  await assertLegacyHelperUsesV2(() => fetchSessionUsageSummary('session-raw'), '/v2/sessions/session-raw/usage')
  await assertLegacyHelperUsesV2(() => fetchActiveSessionPlan('session-raw'), '/v2/sessions/session-raw/plans/active')
  await assertLegacyHelperUsesV2(() => activateSessionPlan('session-raw', 'plan-1'), '/v2/sessions/session-raw/plans/active')
  await assertLegacyHelperUsesV2(() => fetchSessionPlans('session-raw'), '/v2/sessions/session-raw/plans?limit=100')
  await assertLegacyHelperUsesV2(() => fetchSessionPlan('session-raw', 'plan-1'), '/v2/sessions/session-raw/plans/plan-1')
  await assertLegacyHelperUsesV2(() => saveSessionPlan('session-raw', { title: 'Plan', plan: '# Plan' }), '/v2/sessions/session-raw/plans')
  await assertLegacyHelperUsesV2(() => fetchSessionPlanHistory('session-raw', 'plan-1'), '/v2/sessions/session-raw/plans/plan-1/history?limit=100')
  await assertLegacyHelperUsesV2(() => resolveAllSessionPermissions('session-raw', 'approve', 'ok'), '/v2/sessions/session-raw/permissions/resolve_all')
  await assertLegacyHelperUsesV2(() => fetchSessionPendingPermissions('session-raw'), '/v2/sessions/session-raw/permissions?status=pending&limit=200')
})


test('explicit V3 stop helper calls Sessions API v3 cancel endpoint, not V2 stop', async () => {
  const originalFetch = globalThis.fetch
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    return new Response(JSON.stringify({ ok: true, session_id: 'session-raw', run_id: 'run-1', status: 'cancel_requested' }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch

  try {
    const { stopSessionRun } = await import('./chat-queries')
    await stopSessionRun('session-raw', 'run-1', primaryRoute, { sessionApi: 'v3' })
  } finally {
    globalThis.fetch = originalFetch
  }

  assert.equal(calls.length, 1)
  assert.equal(String(calls[0].input), '/v3/sessions/session-raw/run/stop')
  assert.equal(calls[0].init?.method, 'POST')
  assert.deepEqual(JSON.parse(String(calls[0].init?.body ?? '{}')), { type: 'run.stop', run_id: 'run-1' })
})
