import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import { QueryClient } from '@tanstack/react-query'

async function withFetchStub(
  run: (calls: Array<{ input: RequestInfo | URL; init?: RequestInit }>) => Promise<void>,
): Promise<void> {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)

    const v3ModeMatch = /^\/v3\/sessions\/([^/?]+)\/mode$/.exec(url)
    if (v3ModeMatch) {
      const sessionId = decodeURIComponent(v3ModeMatch[1])
      const payload = v3HydratedSessionPayload(sessionId)
      payload.session.mode = 'plan'
      return jsonResponse(payload)
    }

    const v3AgentMatch = /^\/v3\/sessions\/([^/?]+)\/agent$/.exec(url)
    if (v3AgentMatch) {
      const sessionId = decodeURIComponent(v3AgentMatch[1])
      const payload = v3HydratedSessionPayload(sessionId)
      const body = JSON.parse(String(init?.body ?? '{}')) as { agent_name?: string }
      payload.session.metadata = {
        agent_name: body.agent_name ?? '',
        resolved_agent_name: body.agent_name ?? '',
        agent_mode: 'subagent',
      }
      return jsonResponse(payload)
    }

    const v3PreferenceMatch = /^\/v3\/sessions\/([^/?]+)\/preference$/.exec(url)
    if (v3PreferenceMatch) {
      const sessionId = decodeURIComponent(v3PreferenceMatch[1])
      const payload = v3HydratedSessionPayload(sessionId)
      if (sessionId === 'session-config') {
        payload.session.mode = 'plan'
      }
      return jsonResponse(payload)
    }

    const v3MetadataMatch = /^\/v3\/sessions\/([^/?]+)\/metadata$/.exec(url)
    if (v3MetadataMatch) {
      const sessionId = decodeURIComponent(v3MetadataMatch[1])
      const payload = v3HydratedSessionPayload(sessionId)
      if (sessionId === 'session-config') {
        payload.session.mode = 'plan'
      }
      const body = JSON.parse(String(init?.body ?? '{}')) as { metadata?: Record<string, unknown> }
      payload.session.metadata = body.metadata ?? {}
      return jsonResponse(payload)
    }

    const v3PlansMatch = /^\/v3\/sessions\/([^/?]+)\/plans$/.exec(url)
    if (v3PlansMatch) {
      const sessionId = decodeURIComponent(v3PlansMatch[1])
      const payload = v3HydratedSessionPayload(sessionId)
      if (sessionId === 'session-config') {
        payload.session.mode = 'plan'
      }
      return jsonResponse(payload)
    }

    if (url === '/v3/sessions:workset') {
      const body = JSON.parse(String(init?.body ?? '{}')) as { session_ids?: string[]; recent?: { limit?: number }; workspace?: { workspace_path?: string } }
      const sessionIds = Array.isArray(body.session_ids) && body.session_ids.length > 0
        ? body.session_ids
        : ['session-workset-a', 'session-workset-b'].slice(0, body.recent?.limit ?? 2)
      return jsonResponse(v3WorksetPayload(sessionIds))
    }

    const v3SessionMatch = /^\/v3\/sessions\/([^/?]+)$/.exec(url)
    if (v3SessionMatch) {
      return jsonResponse(v3HydratedSessionPayload(decodeURIComponent(v3SessionMatch[1])))
    }

    const v3MessagesMatch = /^\/v3\/sessions\/([^/?]+)\/messages\?/.exec(url)
    if (v3MessagesMatch) {
      const sessionId = decodeURIComponent(v3MessagesMatch[1])
      return jsonResponse({
        ok: true,
        session_id: sessionId,
        messages: [
          { id: `${sessionId}-msg-2`, session_id: sessionId, global_seq: 3, role: 'user', content: 'second', created_at: 3 },
          { id: `${sessionId}-msg-3`, session_id: sessionId, global_seq: 4, role: 'assistant', content: 'third', created_at: 4 },
        ],
        oldest_seq: 3,
        newest_seq: 4,
        next_after_seq: 4,
        has_more_newer: true,
      })
    }

    const v3PermissionResolveMatch = /^\/v3\/sessions\/([^/?]+)\/permissions\/([^/?]+)\/resolve$/.exec(url)
    if (v3PermissionResolveMatch) {
      const sessionId = decodeURIComponent(v3PermissionResolveMatch[1])
      const permissionId = decodeURIComponent(v3PermissionResolveMatch[2])
      return jsonResponse({
        ok: true,
        session_id: sessionId,
        permission: { id: permissionId, session_id: sessionId, run_id: 'run-1', call_id: 'call-1', tool_name: 'bash', tool_arguments: '{}', status: 'approved', requirement: 'approval', mode: 'auto', created_at: 6, updated_at: 7, resolved_at: 7 },
      })
    }

    const v2MessagesMatch = /^\/v2\/sessions\/([^/?]+)\/messages\?/.exec(url)
    if (v2MessagesMatch) {
      const sessionId = decodeURIComponent(v2MessagesMatch[1])
      return jsonResponse({
        ok: true,
        session_id: sessionId,
        messages: [
          { id: `${sessionId}-legacy-msg`, session_id: sessionId, global_seq: 1, role: 'user', content: 'legacy', created_at: 1 },
        ],
      })
    }

    const v2PreferenceMatch = /^\/v2\/sessions\/([^/?]+)\/preference$/.exec(url)
    if (v2PreferenceMatch) {
      return jsonResponse({
        ok: true,
        preference: { provider: 'codex', model: 'gpt-5.4', thinking: 'medium', service_tier: 'flex', context_mode: 'large', updated_at: 2 },
        context_window: 1000000,
        max_output_tokens: 8192,
      })
    }

    const v2SessionMatch = /^\/v2\/sessions\/([^/?]+)$/.exec(url)
    if (v2SessionMatch) {
      const sessionId = decodeURIComponent(v2SessionMatch[1])
      return jsonResponse({
        ok: true,
        session: {
          id: sessionId,
          title: 'Legacy session',
          workspace_path: '/repo',
          workspace_name: 'repo',
          mode: 'auto',
          created_at: 1,
          updated_at: 2,
        },
      })
    }

    const v1SessionMatch = /^\/v1\/sessions\/([^/?]+)/.exec(url)
    if (v1SessionMatch) {
      const sessionId = decodeURIComponent(v1SessionMatch[1])
      return jsonResponse({ ok: true, session: { id: sessionId } })
    }

    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    await run(calls)
  } finally {
    globalThis.fetch = originalFetch
  }
}

function v3HydratedSessionPayload(sessionId: string) {
  return {
    ok: true,
    session: {
      id: sessionId,
      title: 'V3 session',
      workspace_path: '/repo',
      workspace_name: 'repo',
      mode: 'auto',
      preference: { provider: 'codex', model: 'gpt-5.4', thinking: 'medium', service_tier: 'flex', context_mode: 'large', updated_at: 2 },
      metadata: { route: 'primary' },
      created_at: 1,
      updated_at: 5,
    },
    projection: {
      session_id: sessionId,
      last_event_seq: 7,
      projection_high_watermark_seq: 6,
      updated_at: 5,
    },
    messages: [
      { id: `${sessionId}-msg-1`, session_id: sessionId, global_seq: 2, role: 'user', content: 'hello', created_at: 2 },
    ],
    events: [],
    pending_permissions: [
      { id: `${sessionId}-perm-1`, session_id: sessionId, run_id: 'run-1', call_id: 'call-1', tool_name: 'bash', tool_arguments: '{}', status: 'pending', requirement: 'approval', mode: 'auto', created_at: 6, updated_at: 6 },
    ],
    usage_summary: { session_id: sessionId, provider: 'codex', model: 'gpt-5.4', source: 'provider', context_window: 1000, total_tokens: 42, remaining_tokens: 958, updated_at: 6 },
    active_run_intent: sessionId === 'session-v3-active'
      ? { session_id: sessionId, run_id: 'run-active', status: 'running', created_at: 1000, updated_at: 4000, event_seq: 9 }
      : null,
    agent_model_policy: {
      agent_name: 'explorer',
      resolved_agent_name: 'explorer',
      source: 'agent_preset',
      locked: true,
      reason: 'Agent model is set in agent settings; set the agent model to Default to choose a different model.',
      preference: { provider: 'codex', model: 'gpt-5.4', thinking: 'medium', service_tier: 'flex', context_mode: 'large', updated_at: 2 },
      context_window: 1000,
      max_output_tokens: 8192,
    },
    has_active_plan: true,
    active_plan: v3PlanPayload('plan-1'),
    plan_revisions: [v3PlanPayload('plan-1', 1)],
  }
}


function v3WorksetPayload(sessionIds: string[]) {
  const sessionsById: Record<string, unknown> = {}
  const projectionsBySession: Record<string, unknown> = {}
  const messagesBySession: Record<string, unknown[]> = {}
  const eventsBySession: Record<string, unknown[]> = {}
  const permissionsBySession: Record<string, unknown[]> = {}
  const usageBySession: Record<string, unknown> = {}
  const preferencesBySession: Record<string, unknown> = {}
  const agentModelPolicyBySession: Record<string, unknown> = {}
  const runIntentsBySession: Record<string, unknown[]> = {}
  const plansBySession: Record<string, unknown> = {}
  const planRevisionsBySession: Record<string, unknown[]> = {}
  const historyManifestsBySession: Record<string, unknown[]> = {}

  for (const sessionId of sessionIds) {
    const payload = v3HydratedSessionPayload(sessionId)
    sessionsById[sessionId] = payload.session
    projectionsBySession[sessionId] = payload.projection
    messagesBySession[sessionId] = payload.messages
    eventsBySession[sessionId] = payload.events
    permissionsBySession[sessionId] = payload.pending_permissions
    usageBySession[sessionId] = payload.usage_summary
    preferencesBySession[sessionId] = payload.preference ?? payload.session.preference
    agentModelPolicyBySession[sessionId] = payload.agent_model_policy
    runIntentsBySession[sessionId] = payload.active_run_intent ? [payload.active_run_intent] : []
    plansBySession[sessionId] = payload.active_plan
    planRevisionsBySession[sessionId] = payload.plan_revisions
    historyManifestsBySession[sessionId] = []
  }

  return {
    ok: true,
    sessions_by_id: sessionsById,
    projections_by_session: projectionsBySession,
    messages_by_session: messagesBySession,
    events_by_session: eventsBySession,
    plans_by_session: plansBySession,
    plan_revisions_by_session: planRevisionsBySession,
    permissions_by_session: permissionsBySession,
    usage_by_session: usageBySession,
    preferences_by_session: preferencesBySession,
    agent_model_policy_by_session: agentModelPolicyBySession,
    run_intents_by_session: runIntentsBySession,
    history_manifests_by_session: historyManifestsBySession,
    history_chunks_by_id: {},
    omissions: [],
    pagination: { has_more: false },
    watermarks: { loaded_at: 10, max_updated_at: 5 },
    session_order: sessionIds,
  }
}

function v3PlanPayload(planId: string, version = 0) {
  return {
    id: planId,
    session_id: 'session-v3',
    title: 'Current Plan',
    plan: '# Plan',
    status: 'draft',
    approval_state: '',
    updated_at: 8,
    version,
  }
}

function jsonResponse(payload: unknown): Response {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

function requestUrls(calls: Array<{ input: RequestInfo | URL; init?: RequestInit }>) {
  return calls.map((entry) => String(entry.input))
}

function assertNoV1OrV2SessionDataCalls(calls: Array<{ input: RequestInfo | URL; init?: RequestInit }>) {
  const urls = requestUrls(calls)
  assert.equal(urls.some((url) => url.startsWith('/v1/sessions')), false, `unexpected v1 session call: ${urls.join(', ')}`)
  assert.equal(urls.some((url) => url.startsWith('/v2/sessions')), false, `unexpected v2 session call: ${urls.join(', ')}`)
}

test('fetchSession uses raw canonical IDs with explicit Sessions API v3', async () => {
  const { fetchSession } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const session = await fetchSession('session-v3', { sessionApi: 'v3' })

    assert.equal(session?.id, 'session-v3')
    assert.equal(session?.sessionApi, 'v3')
    assert.equal(session?.lastEventSeq, 7)
    assert.equal(session?.projectionHighWatermarkSeq, 6)
    assert.equal(session?.permissionsHydrated, true)
    assert.equal(session?.pendingPermissionCount, 1)
    assert.equal(session?.pendingPermissions[0]?.id, 'session-v3-perm-1')
    assert.equal(session?.usage?.remainingTokens, 958)
    assert.deepEqual(requestUrls(calls), ['/v3/sessions/session-v3'])
    assertNoV1OrV2SessionDataCalls(calls)
  })
})

test('Desktop V3 bootstrap fetches raw route session IDs from /v3/sessions only', async () => {
  const { fetchSession } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const session = await fetchSession('session-raw')

    assert.equal(session?.id, 'session-raw')
    assert.equal(session?.sessionApi, 'v3')
    assert.deepEqual(requestUrls(calls), ['/v3/sessions/session-raw'])
    assertNoV1OrV2SessionDataCalls(calls)
  })
})

test('fetchSession hydrates active V3 run state from durable run intent created_at', async () => {
  const { fetchSession } = await import('./chat-queries')

  await withFetchStub(async () => {
    const session = await fetchSession('session-v3-active', { sessionApi: 'v3' })

    assert.equal(session?.runIntent?.runId, 'run-active')
    assert.equal(session?.runIntent?.status, 'running')
    assert.equal(session?.live.runId, 'run-active')
    assert.equal(session?.live.status, 'running')
    assert.equal(session?.live.startedAt, 1000)
    assert.equal(session?.live.lastEventAt, 4000)
  })
})


test('fetchSessionMessages loads V3 message history from Sessions API v3 only and preserves seq order', async () => {
  const { fetchSessionMessages } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const result = await fetchSessionMessages('session-v3', undefined, 2, { sessionApi: 'v3' })

    assert.deepEqual(result.messages.map((message) => message.globalSeq), [3, 4])
    assert.deepEqual(result.messages.map((message) => message.id), ['session-v3-msg-2', 'session-v3-msg-3'])
    assert.equal(result.appliedSeq, 4)
    assert.equal(result.highWatermark, 4)
    assert.equal(result.nextAfterSeq, 4)
    assert.equal(result.hasMoreNewer, true)
    assert.deepEqual(requestUrls(calls), ['/v3/sessions/session-v3/messages?limit=100&after_seq=2'])
    assertNoV1OrV2SessionDataCalls(calls)
  })
})

test('Desktop V3 permission resolve uses Sessions API v3 only', async () => {
  const { resolveSessionPermission } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const resolved = await resolveSessionPermission('session-v3', 'perm-1', 'approve', 'ok', undefined, { sessionApi: 'v3' })

    assert.equal(resolved.id, 'perm-1')
    assert.equal(resolved.status, 'approved')
    assert.deepEqual(requestUrls(calls), ['/v3/sessions/session-v3/permissions/perm-1/resolve'])
    assert.equal(calls[0]?.init?.method, 'POST')
    assertNoV1OrV2SessionDataCalls(calls)
  })
})


test('Desktop DB direct-route recovery hydrates a missing session through one scoped workset request', async () => {
  const { ensureDesktopDBRouteSession, readDesktopDbMessages, readDesktopDbSession, readDesktopDbSessionReadiness } = await import('../../state/desktop-db')

  await withFetchStub(async (calls) => {
    const readiness = await ensureDesktopDBRouteSession('', 'session-switch')

    assert.equal(readiness.ready, true)
    assert.equal(readiness.status, 'ready')
    assert.deepEqual(requestUrls(calls), ['/v3/sessions:workset'])
    assert.deepEqual(JSON.parse(String(calls[0]?.init?.body ?? '{}')).session_ids, ['session-switch'])
    assertNoV1OrV2SessionDataCalls(calls)
    assert.equal(readDesktopDbSession('session-switch')?.id, 'session-switch')
    assert.equal(readDesktopDbMessages('session-switch').map((message) => message.id).join(','), 'session-switch-msg-1')
    assert.equal(readDesktopDbSessionReadiness('session-switch')?.ready, true)
    assert.equal(readDesktopDbSessionReadiness('session-switch')?.status, 'ready')

    const callCountAfterDbReady = calls.length
    await ensureDesktopDBRouteSession('', 'session-switch')
    assert.equal(calls.length, callCountAfterDbReady, 'DB-ready route/session switching must not fetch')
  })
})

test('Desktop V3 workset hydration restores active run intent from the canonical route source', async () => {
  const { hydrateDesktopV3WorksetSession, readDesktopV3CachedSession } = await import('../../state/desktop-v3-cache')
  const { readDesktopDbSession } = await import('../../state/desktop-db')
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })

  await withFetchStub(async (calls) => {
    const snapshot = await hydrateDesktopV3WorksetSession(queryClient, 'session-v3-active')
    const cached = readDesktopV3CachedSession(queryClient, 'session-v3-active')

    assert.deepEqual(requestUrls(calls), ['/v3/sessions:workset'])
    assertNoV1OrV2SessionDataCalls(calls)
    assert.equal(snapshot?.session.runIntent?.runId, 'run-active')
    assert.equal(cached?.runIntent?.status, 'running')
    assert.equal(readDesktopDbSession('session-v3-active')?.runIntent?.runId, 'run-active')
    assert.equal(cached?.live.runId, 'run-active')
    assert.equal(cached?.live.status, 'running')
    assert.equal(cached?.live.startedAt, 1000)
    assert.equal(cached?.live.lastEventAt, 4000)
  })

  queryClient.clear()
})



test('sessionMessagesQueryOptions uses the canonical V3 snapshot cache for Desktop V3 messages', async () => {
  const { sessionMessagesQueryOptions } = await import('../../../queries/query-options')
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })

  await withFetchStub(async (calls) => {
    const messages = await queryClient.fetchQuery(sessionMessagesQueryOptions('session-messages', queryClient))

    assert.deepEqual(messages.map((message) => message.id), ['session-messages-msg-1'])
    assert.deepEqual(requestUrls(calls), ['/v3/sessions:workset'])
    assertNoV1OrV2SessionDataCalls(calls)
  })

  queryClient.clear()
})

test('sessionPreferenceQueryOptions uses the canonical V3 snapshot cache for Desktop V3 preference', async () => {
  const { sessionPreferenceQueryOptions } = await import('../../../queries/query-options')
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })

  await withFetchStub(async (calls) => {
    const preference = await queryClient.fetchQuery(sessionPreferenceQueryOptions('session-preference', queryClient))

    assert.equal(preference.preference.provider, 'codex')
    assert.equal(preference.preference.model, 'gpt-5.4')
    assert.equal(queryClient.getQueryData<{ agentModelPolicy: { locked: boolean; source: string } }>(['desktop-v3-session-snapshot', 'session-preference'])?.agentModelPolicy?.locked, true)
    assert.equal(queryClient.getQueryData<{ agentModelPolicy: { locked: boolean; source: string } }>(['desktop-v3-session-snapshot', 'session-preference'])?.agentModelPolicy?.source, 'agent_preset')
    assert.deepEqual(requestUrls(calls), ['/v3/sessions:workset'])
    assertNoV1OrV2SessionDataCalls(calls)
  })

  queryClient.clear()
})

test('Desktop V3 preference, mode, agent, metadata, and plan mutations use V3 session update endpoints only', async () => {
  const { sessionPreferenceQueryKey } = await import('../../../queries/query-options')
  const { desktopV3SessionSnapshotQueryKey, saveDesktopV3SessionPlan, updateDesktopV3SessionAgent, updateDesktopV3SessionMetadata, updateDesktopV3SessionMode, updateDesktopV3SessionPreference } = await import('../../state/desktop-v3-cache')
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })

  await withFetchStub(async (calls) => {
    const modeSnapshot = await updateDesktopV3SessionMode(queryClient, 'session-config', 'plan')
    const preference = await updateDesktopV3SessionPreference(queryClient, 'session-config', { provider: 'codex', model: 'gpt-5.4', thinking: 'medium' })
    const agentSnapshot = await updateDesktopV3SessionAgent(queryClient, 'session-config', 'explorer')
    const metadataSnapshot = await updateDesktopV3SessionMetadata(queryClient, 'session-config', { compact_threshold: 80 })
    const planSnapshot = await saveDesktopV3SessionPlan(queryClient, 'session-config', { title: 'Current Plan', plan: '# Plan' })

    assert.equal(modeSnapshot.session.mode, 'plan')
    assert.equal(preference.preference.model, 'gpt-5.4')
    assert.equal(agentSnapshot.session.metadata?.agent_name, 'explorer')
    assert.equal(metadataSnapshot.session.metadata?.compact_threshold, 80)
    assert.equal(planSnapshot.activePlan?.id, 'plan-1')
    assert.deepEqual(requestUrls(calls), [
      '/v3/sessions/session-config/mode',
      '/v3/sessions/session-config/preference',
      '/v3/sessions/session-config/agent',
      '/v3/sessions/session-config/metadata',
      '/v3/sessions/session-config/plans',
    ])
    assert.equal(JSON.parse(String(calls[2].init?.body ?? '{}')).agent_name, 'explorer')
    assert.equal(calls[0].init?.method, 'POST')
    assert.equal(calls[1].init?.method, 'POST')
    assert.equal(calls[2].init?.method, 'POST')
    assert.equal(calls[3].init?.method, 'POST')
    assert.equal(calls[4].init?.method, 'POST')
    assertNoV1OrV2SessionDataCalls(calls)
    assert.equal(queryClient.getQueryData<{ session: { id: string; mode: string } }>(desktopV3SessionSnapshotQueryKey('session-config'))?.session.mode, 'plan')
    assert.equal(queryClient.getQueryData<{ preference: { model: string } }>(sessionPreferenceQueryKey('session-config'))?.preference.model, 'gpt-5.4')
  })

  queryClient.clear()
})

test('Desktop V3 preference and mode mutations reject non-canonical update responses', async () => {
  const { updateDesktopV3SessionMode, updateDesktopV3SessionPreference } = await import('../../state/desktop-v3-cache')
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const originalFetch = globalThis.fetch
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    return jsonResponse({ ok: true, mode: 'plan', preference: { provider: 'codex', model: 'gpt-5.4' } })
  }) as typeof fetch

  try {
    await assert.rejects(
      () => updateDesktopV3SessionMode(queryClient, 'session-config', 'plan'),
      /requires a hydrated canonical session snapshot/,
    )
    await assert.rejects(
      () => updateDesktopV3SessionPreference(queryClient, 'session-config', { provider: 'codex', model: 'gpt-5.4' }),
      /requires a hydrated canonical session snapshot/,
    )
  } finally {
    globalThis.fetch = originalFetch
    queryClient.clear()
  }

  assert.deepEqual(requestUrls(calls), [
    '/v3/sessions/session-config/mode',
    '/v3/sessions/session-config/preference',
  ])
  assertNoV1OrV2SessionDataCalls(calls)
})


test('DesktopChatPanel uses V3-native preference and mode paths', async () => {
  const source = await readFile(new URL('../components/desktop-chat-panel.tsx', import.meta.url), 'utf8')

  assert.match(source, /updateDesktopV3SessionMode\(queryClient, sessionId, nextMode\)/)
  assert.match(source, /updateDesktopV3SessionPreference\(queryClient, sessionId, normalizedNext\)/)
  assert.match(source, /updateDesktopV3SessionAgent\(queryClient, sessionId, nextAgent\)/)
  assert.doesNotMatch(source, /currentMetadata\.subagent\s*=/)
  assert.match(source, /updateDesktopV3SessionMetadata\(queryClient, sessionId, currentMetadata\)/)
  assert.match(source, /saveDesktopV3SessionPlan\(queryClient, sessionId,/)
  assert.match(source, /useDesktopPreference\(sessionId\)/)
  assert.match(source, /useDesktopMessages\(sessionId\)/)
  assert.match(source, /useDesktopActiveRun\(sessionId\)/)
  assert.doesNotMatch(source, /sessionPreferenceQueryOptions\(sessionId \?\? '', queryClient\)/)
  assert.doesNotMatch(source, /sessionMessagesQueryOptions\(sessionId \?\? '', queryClient\)/)
  assert.doesNotMatch(source, /fetchSessionPreference|updateSessionPreference|fetchSessionMode|updateSessionMode/)
  assert.doesNotMatch(source, /\/v1\/sessions|\/v2\/sessions/)
})


test('Desktop route loader does not block route commit on the V3 session snapshot', async () => {
  const routerSource = await readFile(new URL('../../../../app/router.tsx', import.meta.url), 'utf8')

  assert.match(routerSource, /import \{ queryClient \} from '\.\/query-client'/)
  assert.match(routerSource, /import \{ prefetchSessionRuntimeData \} from '\.\.\/features\/queries\/query-options'/)
  assert.match(routerSource, /path: '\/\$workspaceSlug\/\$sessionId',[\s\S]*loader: \(\{ params \}\) => \{[\s\S]*void prefetchSessionRuntimeData\(queryClient, sessionId\)[\s\S]*return \{ sessionId \}/)
  assert.doesNotMatch(routerSource, /ensureDesktopV3SessionSnapshot\(queryClient, sessionId\)/)
  assert.doesNotMatch(routerSource, /fetchSession\(/)
  assert.doesNotMatch(routerSource, /\/v1\/sessions|\/v2\/sessions/)
  assert.doesNotMatch(routerSource, new RegExp('v3session' + '_'))
})

test('DesktopAppPage derives route readiness and cached switching from TanStack DB only', async () => {
  const source = await readFile(new URL('../../layout/desktop-app-page.tsx', import.meta.url), 'utf8')

  assert.match(source, /hydrateDesktopV3Workset\(queryClient, \{/)
  assert.match(source, /workspacePaths,/)
  assert.match(source, /recent: \{ limit: 50 \}/)
  assert.match(source, /useDesktopRouteReadiness\(\{ workspacePath: selectedWorkspacePath \}, routeSessionId\)/)
  assert.match(source, /ensureDesktopDBRouteSession\(\{ workspacePath: selectedWorkspacePath \}, routeCriticalSessionId\)/)
  assert.match(source, /const routeSession = routeSessionId && routeReadiness\?\.ready \? dbRouteSession : null/)
  assert.match(source, /const routeReadinessStatus = routeSessionId \? routeReadiness\?\.status \?\? 'loading' : 'idle'/)
  assert.match(source, /const session = sessionById\.get\(normalizedSessionId\)/)
  assert.match(source, /useDesktopWorkspaceSessions\(\{ workspacePaths: mergedSidebarWorkspaceEntries\.map\(\(workspace\) => workspace\.path\) \}\)/)
  assert.doesNotMatch(source, /readDesktopDbSession\(normalizedSessionId\)/)
  assert.match(source, /data-v3-route-readiness=/)
  assert.doesNotMatch(source, /desktopV3Bootstrap/)
  assert.doesNotMatch(source, /DesktopV3BootstrapState/)
  assert.doesNotMatch(source, /effect:v3-workset-route-gap/)
  assert.doesNotMatch(source, /sessionIds: \[routeCriticalSessionId\]/)
  assert.doesNotMatch(source, /readDesktopV3CachedSession\(queryClient, routeSessionId\)/)
  assert.doesNotMatch(source, /getCachedDesktopV3WorksetSession\(queryClient, routeCriticalSessionId\)/)
  assert.doesNotMatch(source, /desktopV3WorksetHasOmission\(queryClient, normalizedSessionId\)/)
  assert.doesNotMatch(source, /routeSessionSnapshotQuery/)
  assert.doesNotMatch(source, /backgroundBootstrapSessionIds/)
  assert.doesNotMatch(source, /await hydrateDesktopV3SessionSnapshot\(queryClient, normalizedSessionId\)/)
  assert.doesNotMatch(source, /const handleSelectSession = useCallback\(async/)
  assert.match(source, /PAIRING_REQUEST_INITIAL_REFRESH_DELAY_MS = 1_250/)
  assert.match(source, /window\.setTimeout\(refreshPairingRequests, PAIRING_REQUEST_INITIAL_REFRESH_DELAY_MS\)/)
  assert.doesNotMatch(source, /addSessionId\(routeSessionId\)/)
  assert.doesNotMatch(source, /const bootstrapSessionIds = useMemo/)
  assert.doesNotMatch(source, /refreshSessionPermissions\(normalizedRouteSessionId\)/)
  assert.doesNotMatch(source, /fetchSession\(/)
  assert.doesNotMatch(source, /prefetchSessionRuntimeData/)
  assert.doesNotMatch(source, /sessionNeedsRefresh/)
  assert.doesNotMatch(source, /\/v1\/sessions|\/v2\/sessions/)
  assert.doesNotMatch(source, new RegExp('v3session' + '_'))
})

test('Desktop realtime reconciles through canonical V3 snapshots only', async () => {
  const source = await readFile(new URL('../../state/use-desktop-store.ts', import.meta.url), 'utf8')

  assert.match(source, /hydrateDesktopV3WorksetSession\(queryClient, normalizedSessionId\)/)
  assert.match(source, /getCachedDesktopV3SessionSnapshot\(queryClient, sessionId\)/)
  assert.match(source, /if \(eventType\.startsWith\('session\.'\) && hasCanonicalV3Snapshot\)/)
  assert.match(source, /invalidateQueries\(\{ queryKey: desktopV3SessionSnapshotQueryKey\(normalizedSessionId\) \}\)/)
  assert.match(source, /requestScopedSessionWorkset\(sessionId\)/)
  assert.doesNotMatch(source, /requestAuthoritativeSessionSnapshot/)
  assert.doesNotMatch(source, /fetchSession\(/)
  assert.doesNotMatch(source, /fetchSessionPendingPermissions|fetchSessionUsageSummary/)
  assert.doesNotMatch(source, /\/v1\/sessions|\/v2\/sessions/)
  assert.doesNotMatch(source, new RegExp('v3session' + '_'))
})

test('Desktop V3 has no standalone permission list or usage subresource helpers', async () => {
  const source = await readFile(new URL('./chat-queries.ts', import.meta.url), 'utf8')

  assert.doesNotMatch(source, /export async function fetchSessionPendingPermissions/)
  assert.doesNotMatch(source, /export async function fetchSessionUsageSummary/)
  assert.doesNotMatch(source, /PendingPermissionsResponseWire|SessionUsageResponseWire/)
  assert.doesNotMatch(source, /permissions\?status=pending/)
  assert.doesNotMatch(source, /\/usage`/)
})

test('Desktop V3 durable reducer idempotently merges hydration, socket, and page messages with cursors', async () => {
  const { mergeDesktopV3DurableCachePatch, desktopV3SessionSnapshotQueryKey } = await import('../../state/desktop-v3-durable-reducer')
  const { sessionMessagesQueryKey } = await import('../../../queries/query-options')
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })

  await withFetchStub(async () => {
    const { fetchDesktopV3SessionSnapshot } = await import('../../state/desktop-v3-cache')
    const snapshot = await fetchDesktopV3SessionSnapshot('session-reducer')
    assert.ok(snapshot)
    const hydrated = mergeDesktopV3DurableCachePatch(queryClient, { snapshot })
    assert.equal(hydrated?.appliedSeq, 7)
    assert.equal(hydrated?.highWatermark, 7)

    const socketMessage = { id: 'session-reducer-msg-2', sessionId: 'session-reducer', globalSeq: 8, role: 'assistant', content: 'durable socket', createdAt: 8 }
    mergeDesktopV3DurableCachePatch(queryClient, { sessionId: 'session-reducer', messages: [socketMessage], appliedSeq: 8, highWatermark: 9 })
    mergeDesktopV3DurableCachePatch(queryClient, { sessionId: 'session-reducer', messages: [socketMessage], appliedSeq: 8, highWatermark: 9 })
    mergeDesktopV3DurableCachePatch(queryClient, {
      sessionId: 'session-reducer',
      messages: [{ id: 'session-reducer-msg-0', sessionId: 'session-reducer', globalSeq: 1, role: 'user', content: 'older page', createdAt: 1 }],
      appliedSeq: 1,
      highWatermark: 1,
    })

    assert.deepEqual(
      queryClient.getQueryData<Array<{ id: string }>>(sessionMessagesQueryKey('session-reducer'))?.map((message) => message.id),
      ['session-reducer-msg-0', 'session-reducer-msg-1', 'session-reducer-msg-2'],
    )
    const { fetchSessionMessages } = await import('./chat-queries')
    await fetchSessionMessages('session-reducer', undefined, 8, { sessionApi: 'v3', queryClient })
    const reduced = queryClient.getQueryData<{ appliedSeq: number; highWatermark: number }>(desktopV3SessionSnapshotQueryKey('session-reducer'))
    assert.equal(reduced?.appliedSeq, 8)
    assert.equal(reduced?.highWatermark, 9)
  })

  queryClient.clear()
})
