import test from 'node:test'
import assert from 'node:assert/strict'

async function withFetchTrap(run: () => Promise<void>): Promise<void> {
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    throw new Error(`unexpected fetch: ${String(input)}`)
  }) as typeof fetch
  try {
    await run()
  } finally {
    globalThis.fetch = originalFetch
  }
}

async function assertRejectsWithoutFetch(action: () => Promise<unknown>, expectedSubresource: string): Promise<void> {
  await withFetchTrap(async () => {
    await assert.rejects(
      action,
      (error) => {
        assert.ok(error instanceof Error)
        assert.match(error.message, /Sessions API v3 does not support/)
        assert.match(error.message, new RegExp(expectedSubresource))
        assert.match(error.message, /refusing to call legacy Sessions API v2/)
        assert.match(error.message, /v3session_guard/)
        return true
      },
    )
  })
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

test('v3session-prefixed lifecycle helpers fail closed before V2 metadata/mode/preference/codex calls', async () => {
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

  await assertRejectsWithoutFetch(() => fetchSessionPreference('v3session_guard'), 'session preference')
  await assertRejectsWithoutFetch(() => updateSessionPreference('v3session_guard', { model: 'gpt-5.4' }), 'session preference')
  await assertRejectsWithoutFetch(() => fetchSessionMode('v3session_guard'), 'session mode')
  await assertRejectsWithoutFetch(() => updateSessionMode('v3session_guard', 'plan'), 'session mode')
  await assertRejectsWithoutFetch(() => fetchSessionMetadata('v3session_guard'), 'session metadata')
  await assertRejectsWithoutFetch(() => updateSessionMetadata('v3session_guard', { ticket: 'T-1' }), 'session metadata')
  await assertRejectsWithoutFetch(() => fetchSessionCodexConfig('v3session_guard'), 'session Codex config')
  await assertRejectsWithoutFetch(() => updateSessionCodexConfig('v3session_guard', { serviceTier: 'flex' }), 'session Codex config')
})

test('v3session-prefixed run, plan, permission, and usage helpers fail closed before V2 subresource calls', async () => {
  const {
    startSessionRun,
    stopSessionRun,
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

  await assertRejectsWithoutFetch(() => startSessionRun({ sessionId: 'v3session_guard', prompt: 'run', route: primaryRoute }), 'session run dispatch')
  await assertRejectsWithoutFetch(() => stopSessionRun('v3session_guard', 'run-1', primaryRoute), 'session run stop')
  await assertRejectsWithoutFetch(() => resolveSessionPermission('v3session_guard', 'perm-1', 'approve', 'ok'), 'session permissions')
  await assertRejectsWithoutFetch(() => fetchSessionUsageSummary('v3session_guard'), 'session usage summary')
  await assertRejectsWithoutFetch(() => fetchActiveSessionPlan('v3session_guard'), 'session plans')
  await assertRejectsWithoutFetch(() => activateSessionPlan('v3session_guard', 'plan-1'), 'session plans')
  await assertRejectsWithoutFetch(() => fetchSessionPlans('v3session_guard'), 'session plans')
  await assertRejectsWithoutFetch(() => fetchSessionPlan('v3session_guard', 'plan-1'), 'session plans')
  await assertRejectsWithoutFetch(() => saveSessionPlan('v3session_guard', { title: 'Plan', plan: '# Plan' }), 'session plans')
  await assertRejectsWithoutFetch(() => fetchSessionPlanHistory('v3session_guard', 'plan-1'), 'session plans')
  await assertRejectsWithoutFetch(() => resolveAllSessionPermissions('v3session_guard', 'approve', 'ok'), 'session permissions')
  await assertRejectsWithoutFetch(() => fetchSessionPendingPermissions('v3session_guard'), 'session permissions')
})
