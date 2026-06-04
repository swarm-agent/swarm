import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'

async function withFetchStub(
  run: (calls: Array<{ input: RequestInfo | URL; init?: RequestInit }>) => Promise<void>,
): Promise<void> {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)

    if (url === '/v3/sessions/session-v3' || url === '/v3/sessions/v3session_auto') {
      return jsonResponse({
        ok: true,
        session: {
          id: url.endsWith('/v3session_auto') ? 'v3session_auto' : 'session-v3',
          title: 'V3 session',
          workspace_path: '/repo',
          workspace_name: 'repo',
          mode: 'auto',
          metadata: { route: 'primary' },
          created_at: 1,
          updated_at: 5,
        },
        projection: {
          session_id: url.endsWith('/v3session_auto') ? 'v3session_auto' : 'session-v3',
          last_event_seq: 7,
          projection_high_watermark_seq: 6,
          updated_at: 5,
        },
        messages: [
          { id: 'msg-1', session_id: url.endsWith('/v3session_auto') ? 'v3session_auto' : 'session-v3', global_seq: 2, role: 'user', content: 'hello', created_at: 2 },
        ],
        events: [],
      })
    }

    if (url === '/v2/sessions/session-v2') {
      return jsonResponse({
        ok: true,
        session: {
          id: 'session-v2',
          title: 'V2 session',
          workspace_path: '/repo',
          workspace_name: 'repo',
          mode: 'auto',
          created_at: 1,
          updated_at: 2,
        },
      })
    }

    if (url === '/v3/sessions/session-v3/messages?limit=100&after_seq=2' || url === '/v3/sessions/v3session_auto/messages?limit=100') {
      return jsonResponse({
        ok: true,
        session_id: url.includes('/v3session_auto/') ? 'v3session_auto' : 'session-v3',
        messages: [
          { id: 'msg-2', session_id: url.includes('/v3session_auto/') ? 'v3session_auto' : 'session-v3', global_seq: 3, role: 'user', content: 'second', created_at: 3 },
          { id: 'msg-3', session_id: url.includes('/v3session_auto/') ? 'v3session_auto' : 'session-v3', global_seq: 4, role: 'assistant', content: 'third', created_at: 4 },
        ],
      })
    }

    if (url === '/v2/sessions/session-v2/messages?limit=100') {
      return jsonResponse({
        ok: true,
        session_id: 'session-v2',
        messages: [
          { id: 'msg-v2', session_id: 'session-v2', global_seq: 1, role: 'user', content: 'legacy', created_at: 1 },
        ],
      })
    }

    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    await run(calls)
  } finally {
    globalThis.fetch = originalFetch
  }
}

function jsonResponse(payload: unknown): Response {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

function assertNoV1OrV2SessionDataCalls(calls: Array<{ input: RequestInfo | URL; init?: RequestInit }>) {
  const urls = calls.map((entry) => String(entry.input))
  assert.equal(urls.some((url) => url.startsWith('/v1/sessions')), false, `unexpected v1 session call: ${urls.join(', ')}`)
  assert.equal(urls.some((url) => url.startsWith('/v2/sessions')), false, `unexpected v2 session call: ${urls.join(', ')}`)
}

test('fetchSession hydrates V3 sessions from Sessions API v3 only', async () => {
  const { fetchSession } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const session = await fetchSession('session-v3', { sessionApi: 'v3' })

    assert.equal(session?.id, 'session-v3')
    assert.equal(session?.sessionApi, 'v3')
    assert.equal(session?.lastEventSeq, 7)
    assert.equal(session?.projectionHighWatermarkSeq, 6)
    assert.deepEqual(calls.map((entry) => String(entry.input)), ['/v3/sessions/session-v3'])
    assertNoV1OrV2SessionDataCalls(calls)
  })
})

test('fetchSessionMessages loads V3 message history from Sessions API v3 only and preserves seq order', async () => {
  const { fetchSessionMessages } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const messages = await fetchSessionMessages('session-v3', undefined, 2, { sessionApi: 'v3' })

    assert.deepEqual(messages.map((message) => message.globalSeq), [3, 4])
    assert.deepEqual(messages.map((message) => message.id), ['msg-2', 'msg-3'])
    assert.deepEqual(calls.map((entry) => String(entry.input)), ['/v3/sessions/session-v3/messages?limit=100&after_seq=2'])
    assertNoV1OrV2SessionDataCalls(calls)
  })
})

test('v3session-prefixed hydrate and messages auto-select Sessions API v3 without explicit options', async () => {
  const { fetchSession, fetchSessionMessages } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const session = await fetchSession('v3session_auto')
    const messages = await fetchSessionMessages('v3session_auto')

    assert.equal(session?.id, 'v3session_auto')
    assert.equal(session?.sessionApi, 'v3')
    assert.deepEqual(messages.map((message) => message.sessionId), ['v3session_auto', 'v3session_auto'])
    assert.deepEqual(calls.map((entry) => String(entry.input)), [
      '/v3/sessions/v3session_auto',
      '/v3/sessions/v3session_auto/messages?limit=100',
    ])
    assertNoV1OrV2SessionDataCalls(calls)
  })
})

test('legacy non-V3 hydrate and messages keep existing Sessions API v2 endpoints', async () => {
  const { fetchSession, fetchSessionMessages } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const session = await fetchSession('session-v2')
    const messages = await fetchSessionMessages('session-v2')

    assert.equal(session?.id, 'session-v2')
    assert.equal(messages[0]?.id, 'msg-v2')
    assert.deepEqual(calls.map((entry) => String(entry.input)), [
      '/v2/sessions/session-v2',
      '/v2/sessions/session-v2/messages?limit=100',
    ])
  })
})

test('authoritative snapshot hydration preserves V3 API identity when refetching', async () => {
  const source = await readFile(new URL('../../state/use-desktop-store.ts', import.meta.url), 'utf8')

  assert.match(source, /fetchSession\(normalizedSessionId, \{ sessionApi: normalizedSessionApi \}\)/)
  assert.match(source, /requestAuthoritativeSessionSnapshot\(sessionId, merged\.sessionApi\)/)
})
