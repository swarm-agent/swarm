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


test('fetchSessionMessages loads V3 message history from Sessions API v3 only and preserves seq order', async () => {
  const { fetchSessionMessages } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const messages = await fetchSessionMessages('session-v3', undefined, 2, { sessionApi: 'v3' })

    assert.deepEqual(messages.map((message) => message.globalSeq), [3, 4])
    assert.deepEqual(messages.map((message) => message.id), ['session-v3-msg-2', 'session-v3-msg-3'])
    assert.deepEqual(requestUrls(calls), ['/v3/sessions/session-v3/messages?limit=100&after_seq=2'])
    assertNoV1OrV2SessionDataCalls(calls)
  })
})

test('Desktop V3 session switching prewarm uses the full-session source only', async () => {
  const { ensureSessionRuntimeData, sessionMessagesQueryKey } = await import('../../../queries/query-options')
  const { desktopV3SessionQueryKey, readDesktopV3CachedSession } = await import('../../state/desktop-v3-cache')
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })

  await withFetchStub(async (calls) => {
    await ensureSessionRuntimeData(queryClient, 'session-switch')

    assert.deepEqual(requestUrls(calls), ['/v3/sessions/session-switch'])
    assertNoV1OrV2SessionDataCalls(calls)
    assert.equal(queryClient.getQueryData<{ session?: { id?: string } }>(desktopV3SessionQueryKey('session-switch'))?.session?.id, 'session-switch')
    assert.equal(readDesktopV3CachedSession(queryClient, 'session-switch')?.id, 'session-switch')
    assert.deepEqual(
      queryClient.getQueryData<Array<{ id: string }>>(sessionMessagesQueryKey('session-switch'))?.map((message) => message.id),
      ['session-switch-msg-1'],
    )
  })

  queryClient.clear()
})



test('sessionMessagesQueryOptions uses the canonical V3 snapshot cache for Desktop V3 messages', async () => {
  const { sessionMessagesQueryOptions } = await import('../../../queries/query-options')
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })

  await withFetchStub(async (calls) => {
    const messages = await queryClient.fetchQuery(sessionMessagesQueryOptions('session-messages', 'v3', queryClient))

    assert.deepEqual(messages.map((message) => message.id), ['session-messages-msg-1'])
    assert.deepEqual(requestUrls(calls), ['/v3/sessions/session-messages'])
    assertNoV1OrV2SessionDataCalls(calls)
  })

  queryClient.clear()
})


test('DesktopAppPage must use cache-first V3-only route and hover switching', async () => {
  const source = await readFile(new URL('../../layout/desktop-app-page.tsx', import.meta.url), 'utf8')

  assert.match(source, /readDesktopV3CachedSession\(queryClient, routeSessionId\)/)
  assert.match(source, /readDesktopV3CachedSession\(queryClient, sessionId\)/)
  assert.match(source, /hydrateDesktopV3SessionSnapshot\(queryClient, sessionId\)/)
  assert.doesNotMatch(source, /fetchSession\(/)
  assert.doesNotMatch(source, /prefetchSessionRuntimeData/)
  assert.doesNotMatch(source, /sessionNeedsRefresh/)
  assert.doesNotMatch(source, /\/v1\/sessions|\/v2\/sessions/)
  assert.doesNotMatch(source, new RegExp('v3session' + '_'))
})

test('Desktop realtime reconciles through canonical V3 snapshots only', async () => {
  const source = await readFile(new URL('../../state/use-desktop-store.ts', import.meta.url), 'utf8')

  assert.match(source, /hydrateDesktopV3SessionSnapshot\(queryClient, normalizedSessionId\)/)
  assert.match(source, /getCachedDesktopV3SessionSnapshot\(queryClient, sessionId\)/)
  assert.match(source, /if \(eventType\.startsWith\('session\.'\) && hasCanonicalV3Snapshot\)/)
  assert.match(source, /invalidateQueries\(\{ queryKey: desktopV3SessionSnapshotQueryKey\(normalizedSessionId\) \}\)/)
  assert.doesNotMatch(source, /fetchSession\(/)
  assert.doesNotMatch(source, /\/v1\/sessions|\/v2\/sessions/)
  assert.doesNotMatch(source, new RegExp('v3session' + '_'))
})
