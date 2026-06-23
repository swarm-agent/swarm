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

    if (url === '/v3/sessions/session-v3/compact') {
      return jsonResponse({
        ok: true,
        session_id: 'session-v3',
        run_id: 'run-compact',
        status: 'completed',
        run_intent: {
          session_id: 'session-v3',
          run_id: 'run-compact',
          status: 'completed',
          event_seq: 9,
          created_at: 7,
          updated_at: 9,
        },
        compaction: {
          run_id: 'run-compact',
          status: 'completed',
          owner_transport: 'manual_compact',
        },
        terminal: {
          event_type: 'session.lifecycle.updated',
          phase: 'completed',
        },
        realtime_outbox: {
          endpoint_seq: 456,
          endpoint_cursor: 'v3c1.terminal-compact-cursor',
          session_id: 'session-v3',
        },
      })
    }

    if (url === '/v3/sessions/session-v3/messages' || url === '/v3/sessions/session-auto/messages' || url === '/v3/sessions/session-v2/messages') {
      const body = JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>
      const match = url.match(/^\/v3\/sessions\/([^/]+)\/messages$/)
      const sessionId = match?.[1] ?? 'session-v3'
      return jsonResponse({
        ok: true,
        session: {
          id: sessionId,
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
          session_id: sessionId,
          last_event_seq: 3,
          projection_high_watermark_seq: 3,
          updated_at: 7,
        },
        message: {
          id: 'msg-v3-1',
          session_id: sessionId,
          global_seq: 2,
          role: body.role,
          content: body.content,
          created_at: 6,
        },
        run_intent: {
          session_id: sessionId,
          run_id: 'v3run-session-v3-2',
          status: 'pending_executor',
          event_seq: 3,
          created_at: 7,
          updated_at: 7,
        },
        messages: [
          { id: 'msg-v3-1', session_id: sessionId, global_seq: 2, role: body.role, content: body.content, created_at: 6 },
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

test('V3 send requires explicit API selection for raw canonical session IDs', async () => {
  const { sendSessionMessage } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const response = await sendSessionMessage('session-auto', 'user', 'hello v3 auto', null, { sessionApi: 'v3' })

    assert.deepEqual(calls.map((entry) => String(entry.input)), ['/v3/sessions/session-auto/messages'])
    assertNoV1OrV2SessionCalls(calls)
    const body = requestBody(calls[0])
    assert.equal(body.role, 'user')
    assert.equal(body.content, 'hello v3 auto')
    assert.equal(typeof body.client_request_id, 'string')
    assert.match(String(body.client_request_id), /^desktop-v3-message:session-auto:/)

    assert.ok(response && typeof response === 'object' && 'session' in response)
    const result = response as Awaited<ReturnType<typeof sendSessionMessage>> & {
      session?: { id?: string; sessionApi?: string }
    }
    assert.equal(result.session?.id, 'session-auto')
    assert.equal(result.session?.sessionApi, 'v3')
  })
})

test('compactSessionV3 uses only the native Sessions V3 compact endpoint', async () => {
  const { compactSessionV3 } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const response = await compactSessionV3('session-v3', {
      note: 'keep constraints',
      agentName: 'swarm',
      clientRequestId: 'desktop-v3-compact:session-v3:test-request',
    })

    assert.deepEqual(calls.map((entry) => String(entry.input)), ['/v3/sessions/session-v3/compact'])
    assertNoV1OrV2SessionCalls(calls)
    const body = requestBody(calls[0])
    assert.equal(body.client_request_id, 'desktop-v3-compact:session-v3:test-request')
    assert.equal(body.note, 'keep constraints')
    assert.equal(body.agent_name, 'swarm')
    assert.equal(Object.hasOwn(body, 'target_swarm_id'), false)
    assert.equal(Object.hasOwn(body, 'session_id'), false)

    assert.equal(response.sessionId, 'session-v3')
    assert.equal(response.runId, 'run-compact')
    assert.equal(response.status, 'completed')
    assert.equal(response.ownerTransport, 'manual_compact')
    assert.equal(response.terminal?.eventType, 'session.lifecycle.updated')
    assert.equal(response.terminal?.phase, 'completed')
    assert.equal(response.runIntent?.status, 'completed')
    assert.equal(response.realtimeOutbox?.endpointCursor, 'v3c1.terminal-compact-cursor')
  })
})

test('default desktop send uses Sessions API v3 and rejects legacy endpoint fallback', async () => {
  const { sendSessionMessage } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    await sendSessionMessage('session-v2', 'user', 'hello legacy')

    assert.deepEqual(calls.map((entry) => String(entry.input)), ['/v3/sessions/session-v2/messages'])
    assertNoV1OrV2SessionCalls(calls)
    const body = requestBody(calls[0])
    assert.equal(body.role, 'user')
    assert.equal(body.content, 'hello legacy')
    assert.equal(typeof body.client_request_id, 'string')
  })
})

test('Desktop V3 existing-conversation pane submits through Path B flow only', async () => {
  const source = await readFile(new URL('../../chat/components/desktop-v3-existing-conversation-pane.tsx', import.meta.url), 'utf8')

  assert.match(source, /data-testid="desktop-v3-existing-conversation-pane"/)
  assert.match(source, /<DesktopV3AgenticComposer/)
  assert.match(source, /onCompact={handleCompact}/)
  assert.match(source, /continueDesktopV3Conversation\(operation\)/)
  assert.match(source, /createDesktopV3ExistingMessageOperation/)
  assert.match(source, /parseStructuredToolMessage\(message\.content\)/)
  assert.match(source, /buildStructuredToolMessage/)
  assert.match(source, /pathId: 'run\.v3\.provider-tool-result\.v1'/)
  assert.match(source, /durationMs: tool\.durationMs/)
  assert.match(source, /function DesktopV3AssistantMessage/)
  assert.match(source, /function DesktopV3UserMessage/)
  assert.doesNotMatch(source, /DesktopChatPanel|upsertDesktopDbRecord|desktopMessagesCollection|desktopSessionsCollection/)
  assert.doesNotMatch(source, /postDesktopV3CreateSession|new-session-flow|commitDesktopV3Message/)
})
