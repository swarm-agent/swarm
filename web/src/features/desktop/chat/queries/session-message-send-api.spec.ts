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

    if (url === '/v3/sessions/session-v3/messages' || url === '/v3/sessions/v3session_auto/messages') {
      const body = JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>
      return jsonResponse({
        ok: true,
        session: {
          id: url.includes('/v3session_auto/') ? 'v3session_auto' : 'session-v3',
          title: 'V3 session',
          workspace_path: '/repo',
          workspace_name: 'repo',
          mode: 'auto',
          session_api: 'v3',
          message_count: 1,
          updated_at: 7,
          created_at: 1,
        },
        projection: {
          session_id: url.includes('/v3session_auto/') ? 'v3session_auto' : 'session-v3',
          last_event_seq: 3,
          projection_high_watermark_seq: 3,
          updated_at: 7,
        },
        message: {
          id: 'msg-v3-1',
          session_id: url.includes('/v3session_auto/') ? 'v3session_auto' : 'session-v3',
          global_seq: 2,
          role: body.role,
          content: body.content,
          created_at: 6,
        },
        run_intent: {
          session_id: url.includes('/v3session_auto/') ? 'v3session_auto' : 'session-v3',
          run_id: 'v3run-session-v3-2',
          status: 'pending_executor',
          event_seq: 3,
          created_at: 7,
          updated_at: 7,
        },
        messages: [
          { id: 'msg-v3-1', session_id: url.includes('/v3session_auto/') ? 'v3session_auto' : 'session-v3', global_seq: 2, role: body.role, content: body.content, created_at: 6 },
        ],
        events: [],
      })
    }

    if (url === '/v2/sessions/session-v2/messages') {
      return jsonResponse({ ok: true, session_id: 'session-v2' })
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

function requestBody(call: { init?: RequestInit } | undefined): Record<string, unknown> {
  return JSON.parse(String(call?.init?.body ?? '{}')) as Record<string, unknown>
}

function assertNoV1OrV2SessionCalls(calls: Array<{ input: RequestInfo | URL; init?: RequestInit }>) {
  const urls = calls.map((entry) => String(entry.input))
  assert.equal(urls.some((url) => url.startsWith('/v1/sessions')), false, `unexpected v1 session call: ${urls.join(', ')}`)
  assert.equal(urls.some((url) => url.startsWith('/v1/swarm/managed-hosts/sessions')), false, `unexpected managed-host session call: ${urls.join(', ')}`)
  assert.equal(urls.some((url) => url.startsWith('/v2/sessions')), false, `unexpected v2 session call: ${urls.join(', ')}`)
}

test('sendSessionMessage commits V3 desktop messages through Sessions API v3 only', async () => {
  const { sendSessionMessage } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const response = await sendSessionMessage('session-v3', 'user', 'hello v3', null, {
      sessionApi: 'v3',
      clientRequestId: 'desktop-v3-message:session-v3:test-request',
    })

    assert.deepEqual(calls.map((entry) => String(entry.input)), ['/v3/sessions/session-v3/messages'])
    assertNoV1OrV2SessionCalls(calls)
    const body = requestBody(calls[0])
    assert.equal(body.role, 'user')
    assert.equal(body.content, 'hello v3')
    assert.equal(body.client_request_id, 'desktop-v3-message:session-v3:test-request')
    assert.equal(Object.hasOwn(body, 'target_swarm_id'), false)
    assert.equal(Object.hasOwn(body, 'session_id'), false)

    assert.ok(response && typeof response === 'object' && 'message' in response)
    const result = response as Awaited<ReturnType<typeof sendSessionMessage>> & {
      message?: { id?: string } | null
      runIntent?: { status?: string } | null
      session?: { sessionApi?: string; projectionHighWatermarkSeq?: number }
    }
    assert.equal(result.message?.id, 'msg-v3-1')
    assert.equal(result.runIntent?.status, 'pending_executor')
    assert.equal(result.session?.sessionApi, 'v3')
    assert.equal(result.session?.projectionHighWatermarkSeq, 3)
  })
})

test('v3session-prefixed send auto-selects Sessions API v3 without explicit options', async () => {
  const { sendSessionMessage } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const response = await sendSessionMessage('v3session_auto', 'user', 'hello v3 auto')

    assert.deepEqual(calls.map((entry) => String(entry.input)), ['/v3/sessions/v3session_auto/messages'])
    assertNoV1OrV2SessionCalls(calls)
    const body = requestBody(calls[0])
    assert.equal(body.role, 'user')
    assert.equal(body.content, 'hello v3 auto')
    assert.equal(typeof body.client_request_id, 'string')
    assert.match(String(body.client_request_id), /^desktop-v3-message:v3session_auto:/)

    assert.ok(response && typeof response === 'object' && 'session' in response)
    const result = response as Awaited<ReturnType<typeof sendSessionMessage>> & {
      session?: { id?: string; sessionApi?: string }
    }
    assert.equal(result.session?.id, 'v3session_auto')
    assert.equal(result.session?.sessionApi, 'v3')
  })
})

test('legacy non-V3 send keeps existing v2 message endpoint', async () => {
  const { sendSessionMessage } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await sendSessionMessage('session-v2', 'user', 'hello legacy')

    assert.deepEqual(calls.map((entry) => String(entry.input)), ['/v2/sessions/session-v2/messages'])
    assert.deepEqual(requestBody(calls[0]), { role: 'user', content: 'hello legacy' })
  })
})

test('desktop store V3 submit path uses message commit helper instead of V2 run dispatch', async () => {
  const source = await readFile(new URL('../../state/use-desktop-store.ts', import.meta.url), 'utf8')

  assert.match(source, /clientRequestId = providedClientRequestId\?\.trim\(\) \|\| `desktop-v3-message:\$\{targetSessionId\}:\$\{submitStartedAt\}`/)
  assert.match(source, /sendSessionMessage\(targetSessionId, 'user', trimmedPrompt, route, \{ sessionApi: 'v3', clientRequestId \}\)/)
  const panelSource = await readFile(new URL('../components/desktop-chat-panel.tsx', import.meta.url), 'utf8')
  assert.match(panelSource, /clientRequestId: pendingMessageId \? `desktop-v3-message:\$\{pendingMessageId\}` : undefined/)
  assert.match(source, /effectiveSessionApi === 'v3' && !compact/)
  assert.match(source, /applyV3MessageCommitResult/)
  assert.match(source, /set\(\{ realtimeDesired: true \}\)/)
  assert.match(source, /await get\(\)\.connect\(\)/)
  assert.doesNotMatch(source, /requireV3RealtimeController/)
  assert.doesNotMatch(source, /requireRunStreamController\(\)\.ensure\(targetSessionId, committedRunId\)/)
})
