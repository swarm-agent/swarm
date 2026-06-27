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
      return jsonResponse({
        ok: true,
        session_id: decodeURIComponent(v3PreferenceMatch[1]),
        preference: { provider: 'codex', model: 'gpt-5.4', thinking: 'medium', service_tier: 'flex', context_mode: 'large', updated_at: 2 },
        context_window: 1000000,
        max_output_tokens: 8192,
      })
    }

    const v3UsageMatch = /^\/v3\/sessions\/([^/?]+)\/usage$/.exec(url)
    if (v3UsageMatch) {
      const sessionId = decodeURIComponent(v3UsageMatch[1])
      return jsonResponse({
        ok: true,
        session_id: sessionId,
        has_usage_summary: true,
        usage_summary: { session_id: sessionId, provider: 'codex', model: 'gpt-5.4', source: 'provider', context_window: 1000, total_tokens: 42, remaining_tokens: 958, updated_at: 6 },
      })
    }

    const v3PermissionsMatch = /^\/v3\/sessions\/([^/?]+)\/permissions\?status=pending&limit=200$/.exec(url)
    if (v3PermissionsMatch) {
      const sessionId = decodeURIComponent(v3PermissionsMatch[1])
      return jsonResponse({
        ok: true,
        session_id: sessionId,
        count: 1,
        permissions: [
          { id: `${sessionId}-perm-1`, session_id: sessionId, run_id: 'run-1', call_id: 'call-1', tool_name: 'bash', tool_arguments: '{}', status: 'pending', requirement: 'approval', mode: 'auto', created_at: 6, updated_at: 6 },
        ],
      })
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

    const v3ActivePlanMatch = /^\/v3\/sessions\/([^/?]+)\/plans\/active$/.exec(url)
    if (v3ActivePlanMatch) {
      const sessionId = decodeURIComponent(v3ActivePlanMatch[1])
      return jsonResponse({
        ok: true,
        session_id: sessionId,
        has_active: true,
        active_plan: v3PlanPayload('plan-1'),
      })
    }

    const v3PlanHistoryMatch = /^\/v3\/sessions\/([^/?]+)\/plans\/([^/?]+)\/history\?limit=100$/.exec(url)
    if (v3PlanHistoryMatch) {
      const sessionId = decodeURIComponent(v3PlanHistoryMatch[1])
      const planId = decodeURIComponent(v3PlanHistoryMatch[2])
      return jsonResponse({
        ok: true,
        session_id: sessionId,
        plan_id: planId,
        count: 1,
        revisions: [v3PlanPayload(planId, 1)],
      })
    }

    const v3PlansMatch = /^\/v3\/sessions\/([^/?]+)\/plans$/.exec(url)
    if (v3PlansMatch) {
      const sessionId = decodeURIComponent(v3PlansMatch[1])
      return jsonResponse({
        ok: true,
        session_id: sessionId,
        plan: v3PlanPayload('plan-1'),
      })
    }

    if (url === '/v3/sync/bootstrap' || url === '/v3/sync/hydrate') {
      const body = JSON.parse(String(init?.body ?? '{}')) as { session_ids?: string[]; selector?: { session_ids?: string[]; recent?: { limit?: number } }; history?: { mode?: string }; resources?: { messages?: boolean } }
      const explicitSessionIds = Array.isArray(body.session_ids) && body.session_ids.length > 0
        ? body.session_ids
        : Array.isArray(body.selector?.session_ids) && body.selector.session_ids.length > 0
          ? body.selector.session_ids
          : []
      const sessionIds = explicitSessionIds.length > 0
        ? explicitSessionIds
        : ['session-sync-a', 'session-sync-b'].slice(0, body.selector?.recent?.limit ?? 2)
      return jsonResponse(v3WorksetPayload(sessionIds, {
        includeMessages: body.resources?.messages === true || body.history?.mode === 'tail' || body.history?.mode === 'full',
      }))
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


function v3WorksetPayload(sessionIds: string[], options: { includeMessages?: boolean } = {}) {
  const sessionsById: Record<string, unknown> = {}
  const projectionsBySession: Record<string, unknown> = {}
  const messagesBySession: Record<string, unknown[]> = {}
  const eventsBySession: Record<string, unknown[]> = {}
  const runIntentsBySession: Record<string, unknown[]> = {}
  const historyManifestsBySession: Record<string, unknown[]> = {}

  for (const sessionId of sessionIds) {
    const payload = v3HydratedSessionPayload(sessionId)
    sessionsById[sessionId] = payload.session
    projectionsBySession[sessionId] = payload.projection
    messagesBySession[sessionId] = options.includeMessages ? payload.messages : []
    eventsBySession[sessionId] = []
    runIntentsBySession[sessionId] = payload.active_run_intent ? [payload.active_run_intent] : []
    historyManifestsBySession[sessionId] = []
  }

  return {
    ok: true,
    rev: 1,
    sessions_by_id: sessionsById,
    projections_by_session: projectionsBySession,
    messages_by_session: messagesBySession,
    events_by_session: eventsBySession,
    run_intents_by_session: runIntentsBySession,
    current_run_intent_by_session: Object.fromEntries(Object.entries(runIntentsBySession).flatMap(([sessionId, intents]) => intents[0] ? [[sessionId, intents[0]]] : [])),
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
    document: {
      id: planId,
      title: 'Current Plan',
      status: 'draft',
      info: { goal: 'Ship V3 plan hydration', relevant_files: ['web/src/features/desktop/v3-runtime/v3-store.ts'] },
      checkpoints: [{ id: 'cp-1', title: 'Hydrate plan', status: 'pending' }],
    },
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
    assert.deepEqual(requestUrls(calls), ['/v3/sync/hydrate'])
    assertNoV1OrV2SessionDataCalls(calls)
  })
})

test('Desktop V3 bootstrap fetches raw route session IDs from /v3/sync/hydrate only', async () => {
  const { fetchSession } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const session = await fetchSession('session-raw')

    assert.equal(session?.id, 'session-raw')
    assert.equal(session?.sessionApi, 'v3')
    assert.deepEqual(requestUrls(calls), ['/v3/sync/hydrate'])
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


test('sessionMessagesQueryOptions loads the bounded V3 tail page through the dedicated messages endpoint', async () => {
  const { sessionMessagesQueryOptions } = await import('../../../queries/query-options')
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })

  await withFetchStub(async (calls) => {
    const messages = await queryClient.fetchQuery(sessionMessagesQueryOptions('session-messages', queryClient))

    assert.deepEqual(messages.map((message) => message.id), ['session-messages-msg-2', 'session-messages-msg-3'])
    assert.deepEqual(requestUrls(calls), ['/v3/sessions/session-messages/messages?tail=true&limit=200'])
    assertNoV1OrV2SessionDataCalls(calls)
  })

  queryClient.clear()
})


test('prefetchSessionRuntimeData hydrates metadata then the 200-message V3 tail page', async () => {
  const { prefetchSessionRuntimeData } = await import('../../../queries/query-options')
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })

  await withFetchStub(async (calls) => {
    await prefetchSessionRuntimeData(queryClient, 'session-prefetch')

    assert.deepEqual(requestUrls(calls), [
      '/v3/sync/hydrate',
      '/v3/sessions/session-prefetch/messages?tail=true&limit=200',
    ])
    const syncBody = JSON.parse(String(calls[0]?.init?.body ?? '{}')) as { surface?: string; selector_kind?: string; history?: { mode?: string }; resources?: { messages?: boolean } }
    assert.equal(syncBody.surface, 'desktop')
    assert.equal(syncBody.selector_kind, 'session_ids')
    assert.equal(syncBody.history?.mode, 'none')
    assert.equal(syncBody.resources?.messages, undefined)
    assertNoV1OrV2SessionDataCalls(calls)
  })

  queryClient.clear()
})

test('sessionPermissionsQueryOptions uses the dedicated V3 session permissions API', async () => {
  const { sessionPermissionsQueryOptions } = await import('../../../queries/query-options')
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })

  await withFetchStub(async (calls) => {
    const permissions = await queryClient.fetchQuery(sessionPermissionsQueryOptions('session-permissions', queryClient))

    assert.equal(permissions[0]?.id, 'session-permissions-perm-1')
    assert.deepEqual(requestUrls(calls), ['/v3/sessions/session-permissions/permissions?status=pending&limit=200'])
    assertNoV1OrV2SessionDataCalls(calls)
  })

  queryClient.clear()
})

test('sessionUsageQueryOptions uses the dedicated V3 session usage API', async () => {
  const { sessionUsageQueryOptions } = await import('../../../queries/query-options')
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })

  await withFetchStub(async (calls) => {
    const usage = await queryClient.fetchQuery(sessionUsageQueryOptions('session-usage', queryClient))

    assert.equal(usage?.remainingTokens, 958)
    assert.deepEqual(requestUrls(calls), ['/v3/sessions/session-usage/usage'])
    assertNoV1OrV2SessionDataCalls(calls)
  })

  queryClient.clear()
})

test('sessionPreferenceQueryOptions uses the dedicated V3 session preference API', async () => {
  const { sessionPreferenceQueryOptions } = await import('../../../queries/query-options')
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })

  await withFetchStub(async (calls) => {
    const preference = await queryClient.fetchQuery(sessionPreferenceQueryOptions('session-preference', queryClient))

    assert.equal(preference.preference.provider, 'codex')
    assert.equal(preference.preference.model, 'gpt-5.4')
    assert.deepEqual(requestUrls(calls), ['/v3/sessions/session-preference/preference'])
    assertNoV1OrV2SessionDataCalls(calls)
  })

  queryClient.clear()
})

test('DesktopChatPanel uses V3-native preference and mode paths', async () => {
  const source = await readFile(new URL('../components/desktop-chat-panel.tsx', import.meta.url), 'utf8')

  assert.match(source, /fetchAndApplyDesktopV3SessionMessagesTail\(sessionId, \{ signal: controller\.signal \}\)/)
  assert.match(source, /fetchAndApplyDesktopV3PlanSnapshot\(sessionId\)/)
  assert.match(source, /const loadingMessages = messagesLoading/)
  assert.match(source, /applySessionMode\(sessionId, nextMode\)/)
  assert.match(source, /updateDesktopV3SessionPreference\(sessionId, normalizedNext\)/)
  assert.match(source, /applySessionAgent\(sessionId, nextAgent\)/)
  assert.doesNotMatch(source, /currentMetadata\.subagent\s*=/)
  assert.match(source, /applySessionMetadata\(sessionId, currentMetadata\)/)
  assert.match(source, /saveDesktopV3SessionPlan\(sessionId,/)
  assert.doesNotMatch(source, /const snapshot = await fetchAndApplyDesktopV3SessionSnapshot\(sessionId\)/)
  assert.match(source, /from '\.\.\/\.\.\/state\/desktop-state-store'/)
  assert.match(source, /useDesktopPreference\(sessionId\)/)
  assert.match(source, /sessionPreferenceQueryOptions\(sessionId \?\? '', queryClient\)/)
  assert.match(source, /sessionUsageQueryOptions\(sessionId \?\? '', queryClient\)/)
  assert.match(source, /sessionPermissionsQueryOptions\(sessionId \?\? '', queryClient\)/)
  assert.match(source, /useDesktopMessages\(sessionId\)/)
  assert.match(source, /useDesktopActiveRun\(sessionId\)/)
  assert.doesNotMatch(source, /sessionMessagesQueryOptions\(sessionId \?\? '', queryClient\)/)
  assert.doesNotMatch(source, /fetchSessionPreference|updateSessionPreference|fetchSessionMode|updateSessionMode/)
  assert.doesNotMatch(source, /\/v1\/sessions|\/v2\/sessions/)
})


test('Desktop route loader does not block route commit on the V3 session snapshot', async () => {
  const routerSource = await readFile(new URL('../../../../app/router.tsx', import.meta.url), 'utf8')

  assert.match(routerSource, /import \{ queryClient \} from '\.\/query-client'/)
  assert.match(routerSource, /import \{ prefetchSessionRuntimeData \} from '\.\.\/features\/queries\/query-options'/)
  assert.match(routerSource, /path: '\/\$workspaceSlug\/\$sessionId',[\s\S]*loader: \(\{ params \}\) => \{[\s\S]*void prefetchSessionRuntimeData\(queryClient, sessionId\)[\s\S]*return \{ sessionId \}/)
  assert.doesNotMatch(routerSource, /ensureDesktopDBSessionSnapshot\(queryClient, sessionId\)/)
  assert.doesNotMatch(routerSource, /fetchSession\(/)
  assert.doesNotMatch(routerSource, /\/v1\/sessions|\/v2\/sessions/)
  assert.doesNotMatch(routerSource, new RegExp('v3session' + '_'))
})

test('DesktopAppPage derives route readiness and cached switching from the external Desktop state store', async () => {
  const source = await readFile(new URL('../../layout/desktop-app-page.tsx', import.meta.url), 'utf8')

  assert.match(source, /from '\.\.\/state\/desktop-state-store'/)
  assert.match(source, /fetchDesktopSessionDiscovery\(request, abortController\.signal\)/)
  assert.match(source, /applyV3RuntimeEnvelope\(createV3SnapshotEnvelope\(\{[\s\S]*\.\.\.snapshot,[\s\S]*reconcileSessionScope: \{ workspacePaths \},[\s\S]*\}, \{ mode: 'reconcile'/)
  assert.match(source, /workspacePaths,/)
  assert.match(source, /recent: \{ limit: 50 \}/)
  assert.doesNotMatch(source, /includeActive: true/)
  assert.doesNotMatch(source, /maxMessagesPerSession/)
  assert.match(source, /useDesktopRouteReadiness\(\{ workspacePath: selectedWorkspacePath \}, routeSessionId\)/)
  assert.match(source, /fetchDesktopStateSnapshot\(\{[\s\S]*sessionIds: \[routeCriticalSessionId\],[\s\S]*workspacePaths: desktopV3WorksetScopeKey \? desktopV3WorksetScopeKey\.split\('\\u0000'\) : \[\],[\s\S]*recent: \{ limit: 50 \}/)
  assert.match(source, /const routeSession = routeSessionId && routeReadiness\?\.ready \? dbRouteSession : null/)
  assert.match(source, /const routeReadinessStatus = routeSessionId \? routeReadiness\?\.status \?\? 'loading' : 'idle'/)
  assert.match(source, /const session = sessionById\.get\(normalizedSessionId\)/)
  assert.match(source, /useDesktopWorkspaceSessions\(\{ workspacePaths: mergedSidebarWorkspaceEntries\.map\(\(workspace\) => workspace\.path\) \}\)/)
  assert.doesNotMatch(source, /from '\.\.\/state\/desktop-db'/)
  assert.doesNotMatch(source, /readDesktopDbSession\(normalizedSessionId\)/)
  assert.doesNotMatch(source, /ensureDesktopDBRouteSession/)
  assert.match(source, /data-v3-route-readiness=/)
  assert.doesNotMatch(source, /desktopV3Bootstrap/)
  assert.doesNotMatch(source, /DesktopV3BootstrapState/)
  assert.doesNotMatch(source, /effect:v3-workset-route-gap/)
  assert.doesNotMatch(source, /readDesktopDBHydratedSession\(queryClient, routeSessionId\)/)
  assert.doesNotMatch(source, /readDesktopDBWorksetSession\(queryClient, routeCriticalSessionId\)/)
  assert.doesNotMatch(source, /desktopV3WorksetHasOmission\(queryClient, normalizedSessionId\)/)
  assert.doesNotMatch(source, /routeSessionSnapshotQuery/)
  assert.doesNotMatch(source, /backgroundBootstrapSessionIds/)
  assert.doesNotMatch(source, /await fetchAndApplyDesktopDBSessionSnapshot\(queryClient, normalizedSessionId\)/)
  assert.doesNotMatch(source, /const handleSelectSession = useCallback\(async/)
  assert.doesNotMatch(source, /addSessionId\(routeSessionId\)/)
  assert.doesNotMatch(source, /const bootstrapSessionIds = useMemo/)
  assert.doesNotMatch(source, /refreshSessionPermissions\(normalizedRouteSessionId\)/)
  assert.doesNotMatch(source, /fetchSession\(/)
  assert.doesNotMatch(source, /prefetchSessionRuntimeData/)
  assert.doesNotMatch(source, /sessionNeedsRefresh/)
  assert.doesNotMatch(source, /fetchDesktopStateSnapshot\(request, abortController\.signal\)/)
  assert.doesNotMatch(source, /\/v3\/sessions:workset[\s\S]*mode: 'replace'/)
  assert.doesNotMatch(source, /\/v1\/sessions|\/v2\/sessions/)
  assert.doesNotMatch(source, new RegExp('v3session' + '_'))
})

test('Desktop realtime applies durable session events into the external Desktop state store', async () => {
  const source = await readFile(new URL('../../state/desktop-ui-store.ts', import.meta.url), 'utf8')

  assert.match(source, /applyV3RuntimeEnvelope\(normalizedEnvelope\)/)
  assert.match(source, /const runtimeOutcome = applyV3RuntimeEnvelope\(normalizedEnvelope\)/)
  assert.match(source, /if \(runtimeOutcome\.applied\) \{[\s\S]*applyV3SessionStreamFrame\(state, frameSessionId, payload as DesktopV3RealtimeFrame, ts\)/)
  assert.doesNotMatch(source, /applyDurableEventToDesktopDB/)
  assert.doesNotMatch(source, /mergeDesktopDBDurablePatch/)
  assert.doesNotMatch(source, /applyRunIntentToDesktopDB/)
  assert.doesNotMatch(source, /applyOptimisticRunStartToDesktopDB/)
  assert.doesNotMatch(source, /ensureDesktopDBRouteSession/)
  assert.doesNotMatch(source, /desktopPlansCollection/)
  assert.doesNotMatch(source, /mergeDesktopDBSnapshotPatch\(queryClient/)
  assert.doesNotMatch(source, /requestAuthoritativeSessionSnapshot/)
  assert.doesNotMatch(source, /fetchSession\(/)
  assert.doesNotMatch(source, /fetchSessionPendingPermissions|fetchSessionUsageSummary/)
  assert.doesNotMatch(source, /\/v1\/sessions|\/v2\/sessions/)
  assert.doesNotMatch(source, new RegExp('v3session' + '_'))

  const { applyDesktopDurableEventEnvelope, getDesktopSnapshot, replaceDesktopFromSnapshot } = await import('../../state/desktop-state-store')

  const sessionId = 'session-external-realtime'
  replaceDesktopFromSnapshot({ rev: 0 })

  applyDesktopDurableEventEnvelope({
    global_seq: 41,
    source_seq: 41,
    event_type: 'session.run_intent.recorded',
    entity_id: sessionId,
    ts_unix_ms: 410,
    payload: {
      session_id: sessionId,
      run_intent: { session_id: sessionId, run_id: 'run-external', status: 'pending_executor', created_at: 123, updated_at: 124, event_seq: 41 },
    },
  })
  applyDesktopDurableEventEnvelope({
    global_seq: 42,
    source_seq: 42,
    event_type: 'session.message.appended',
    entity_id: sessionId,
    ts_unix_ms: 420,
    payload: {
      session_id: sessionId,
      message: { id: 'msg-external-1', session_id: sessionId, global_seq: 42, role: 'assistant', content: 'external durable', created_at: 420 },
    },
  })

  const snapshot = getDesktopSnapshot()
  assert.equal(snapshot.runIntentsBySessionId[sessionId]?.runId, 'run-external')
  assert.equal(snapshot.sessionsById[sessionId]?.runIntent?.runId, 'run-external')
  assert.equal(snapshot.sessionsById[sessionId]?.live.startedAt, 123)
  assert.deepEqual(snapshot.messagesBySessionId[sessionId]?.map((message) => message.id), ['msg-external-1'])
})

test('Desktop V3 pending permission and usage helpers live outside legacy chat queries', async () => {
  const source = await readFile(new URL('./chat-queries.ts', import.meta.url), 'utf8')

  assert.doesNotMatch(source, /export async function fetchSessionPendingPermissions/)
  assert.doesNotMatch(source, /export async function fetchSessionUsageSummary/)
  assert.doesNotMatch(source, /PendingPermissionsResponseWire|SessionUsageResponseWire/)
  assert.doesNotMatch(source, /permissions\?status=pending/)
  assert.doesNotMatch(source, /\/usage`/)
})

test('Desktop V3 plan modal uses dedicated plan endpoints instead of workset hydration', async () => {
  const { fetchAndApplyDesktopV3PlanSnapshot, saveDesktopV3SessionPlan } = await import('../../state/desktop-v3-session-api')

  await withFetchStub(async (calls) => {
    const planSnapshot = await fetchAndApplyDesktopV3PlanSnapshot('session-plan')

    assert.equal(planSnapshot.hasActivePlan, true)
    assert.equal(planSnapshot.activePlan?.id, 'plan-1')
    assert.equal(planSnapshot.activePlan?.document?.info.goal, 'Ship V3 plan hydration')
    assert.equal(planSnapshot.planRevisions.length, 1)
    assert.deepEqual(requestUrls(calls), [
      '/v3/sessions/session-plan/plans/active',
      '/v3/sessions/session-plan/plans/plan-1/history?limit=100',
    ])
    assertNoV1OrV2SessionDataCalls(calls)
  })

  await withFetchStub(async () => {
    const { normalizeDesktopSessionPlanRevisions } = await import('../services/session-plan-record')

    assert.deepEqual(normalizeDesktopSessionPlanRevisions(undefined), [])
    assert.deepEqual(normalizeDesktopSessionPlanRevisions({ revisions: [] }), [])
    assert.deepEqual(normalizeDesktopSessionPlanRevisions([undefined, null, 'bad']).map((revision) => revision.id), ['', '', ''])
  })

  await withFetchStub(async (calls) => {
    const saved = await saveDesktopV3SessionPlan('session-plan', { id: 'plan-1', title: 'Current Plan', plan: '# Updated' })

    assert.equal(saved.activePlan?.id, 'plan-1')
    assert.deepEqual(requestUrls(calls), [
      '/v3/sessions/session-plan/plans',
      '/v3/sessions/session-plan/plans/active',
      '/v3/sessions/session-plan/plans/plan-1/history?limit=100',
    ])
    assert.equal(calls[0]?.init?.method, 'POST')
    assertNoV1OrV2SessionDataCalls(calls)
  })
})


test('Desktop V3 exit_plan_mode approval refreshes the active plan snapshot without history before sidebar render', async () => {
  const [existingPane, planExecutionApi] = await Promise.all([
    readFile(new URL('../components/desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../../session-v3/plan-execution-api.ts', import.meta.url), 'utf8'),
  ])

  assert.match(existingPane, /fetchAndApplyDesktopV3PlanSnapshot/)
  assert.match(existingPane, /toolName === 'exit_plan_mode'[\s\S]*fetchAndApplyDesktopV3PlanSnapshot\(permission\.sessionId, \{ includeHistory: false \}\)/)
  assert.match(planExecutionApi, /if \(plan\) applyDesktopPlanExecutionResult\(normalizedSessionId, plan\)/)

  const { fetchAndApplyDesktopV3PlanSnapshot } = await import('../../state/desktop-v3-session-api')
  await withFetchStub(async (calls) => {
    const planSnapshot = await fetchAndApplyDesktopV3PlanSnapshot('session-plan', { includeHistory: false })

    assert.equal(planSnapshot.hasActivePlan, true)
    assert.equal(planSnapshot.activePlan?.id, 'plan-1')
    assert.deepEqual(requestUrls(calls), ['/v3/sessions/session-plan/plans/active'])
    assertNoV1OrV2SessionDataCalls(calls)
  })
})


test('Desktop V3 slash command handlers are wired from app page into composers', async () => {
  const [appPage, existingPane, newPane, composer] = await Promise.all([
    readFile(new URL('../../layout/desktop-app-page.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../components/desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../components/desktop-v3-new-session-pane.tsx', import.meta.url), 'utf8'),
    readFile(new URL('../components/desktop-v3-agentic-composer.tsx', import.meta.url), 'utf8'),
  ])

  assert.match(composer, /onSlashCommand\?: \(command: DesktopSlashCommand, draft: string\)/)
  assert.match(existingPane, /onSlashCommand\?: \(command: DesktopSlashCommand, draft: string\)/)
  assert.match(existingPane, /onSlashCommand=\{onSlashCommand\}/)
  assert.match(newPane, /onSlashCommand\?: \(command: DesktopSlashCommand, draft: string\)/)
  assert.match(newPane, /onSlashCommand=\{onSlashCommand\}/)
  assert.match(appPage, /const handleSlashCommand = useCallback\(\(command: DesktopSlashCommand\)/)
  assert.match(appPage, /<DesktopV3ExistingConversationPane[\s\S]*?onSlashCommand=\{handleSlashCommand\}/)
  assert.match(appPage, /<DesktopV3NewSessionPane[\s\S]*?onSlashCommand=\{handleSlashCommand\}/)
})


test('Desktop session metadata hydration is not gated on plan hydration', async () => {
  const { sessionRequiresSnapshotHydration } = await import('../../state/session-snapshot-hydration')
  const session = {
    ...v3HydratedSessionPayload('session-plan-independent').session,
    workspacePath: '/repo',
    workspaceName: 'repo',
    createdAt: 1,
    id: 'session-plan-independent',
  }

  assert.equal(sessionRequiresSnapshotHydration(session, 'session.mode.updated'), false)
})


test('Desktop routine sync hydration callers are metadata-only and never request unbounded full history', async () => {
  const files = [
    '../../state/desktop-state-snapshot.ts',
    '../../state/desktop-ui-store.ts',
    '../../state/desktop-v3-session-api.ts',
    '../../state/desktop-state-stream.ts',
    '../../layout/desktop-app-page.tsx',
  ] as const
  const sources = await Promise.all(files.map(async (file) => ({
    file,
    source: await readFile(new URL(file, import.meta.url), 'utf8'),
  })))

  const allSource = sources.map(({ file, source }) => `\n@@FILE:${file}@@\n${source}`).join('\n')
  const unboundedFullHistoryCallers = findUnboundedFullHistoryWorksetRequests(allSource)
  assert.deepEqual(unboundedFullHistoryCallers, [], `routine sync callers must not request unbounded history.mode='full': ${unboundedFullHistoryCallers.join(', ')}`)

  const snapshotSource = sources.find((entry) => entry.file.endsWith('desktop-state-snapshot.ts'))?.source ?? ''
  assert.match(snapshotSource, /const DEFAULT_SNAPSHOT_HISTORY = \{\n\s+mode: 'none' as const,/, 'default sync snapshot history must be metadata-only')
  assert.match(snapshotSource, /apiFetch\(desktopSyncSnapshotEndpoint\(input\)/, 'Desktop snapshot fetches must route through canonical sync endpoint selection')
  assert.match(snapshotSource, /'\/v3\/sync\/bootstrap'/, 'Desktop bootstrap sync endpoint must be canonical')
  assert.match(snapshotSource, /'\/v3\/sync\/hydrate'/, 'Desktop hydrate sync endpoint must be canonical')
  assert.doesNotMatch(allSource, /includeActive:\s*true/, 'routine Desktop sync callers must not request include_active')
  assert.doesNotMatch(snapshotSource, /\/v3\/sessions:workset|\/v3\/sessions:discover/, 'Desktop snapshot transport must not call legacy workset/discovery endpoints')

  const mutationSource = sources.find((entry) => entry.file.endsWith('desktop-v3-session-api.ts'))?.source ?? ''
  assert.doesNotMatch(mutationSource, /refreshSessionAfterMutation|fetchAndApplyDesktopV3SessionSnapshot|mergeDesktopStateSnapshot/, 'Desktop V3 mutation helpers must not hydrate after mutation as repair')
})

function findUnboundedFullHistoryWorksetRequests(source: string): string[] {
  const offenders: string[] = []
  for (const match of source.matchAll(/history:\s*\{[^}]*mode:\s*'full'[^}]*\}/g)) {
    const request = match[0]
    if (/maxMessagesPerSession\s*:\s*\d+/.test(request)) {
      continue
    }
    const offset = match.index ?? 0
    const prefix = source.slice(0, offset)
    const line = prefix.split('\n').length
    const fileMarkerOffset = prefix.lastIndexOf('@@FILE:')
    const fileMarkerEnd = fileMarkerOffset >= 0 ? prefix.indexOf('@@', fileMarkerOffset + '@@FILE:'.length) : -1
    const file = fileMarkerOffset >= 0 && fileMarkerEnd > fileMarkerOffset
      ? prefix.slice(fileMarkerOffset + '@@FILE:'.length, fileMarkerEnd)
      : 'unknown'
    const fileLine = fileMarkerOffset >= 0 ? prefix.slice(fileMarkerEnd + '@@'.length, offset).split('\n').length : line
    offenders.push(`${file}:${fileLine}`)
  }
  return offenders
}
