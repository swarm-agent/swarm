import test from 'node:test'
import assert from 'node:assert/strict'

async function withFetchStub(
  run: (calls: Array<{ input: RequestInfo | URL; init?: RequestInit }>) => Promise<void>,
): Promise<void> {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)
    if (url === '/v2/sessions/session-test/permissions?status=pending&limit=200') {
      return new Response(JSON.stringify({
        ok: true,
        session_id: 'session-test',
        count: 1,
        permissions: [{
          id: 'perm-pending',
          session_id: 'session-test',
          status: 'pending',
          tool_name: 'bash',
        }],
      }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    await run(calls)
  } finally {
    globalThis.fetch = originalFetch
  }
}

test('fetchSessionPendingPermissions requests the pending-only permission API', async () => {
  const { fetchSessionPendingPermissions } = await import('./chat-queries')

  await withFetchStub(async (calls) => {
    const permissions = await fetchSessionPendingPermissions('session-test')

    assert.deepEqual(calls.map((entry) => String(entry.input)), [
      '/v2/sessions/session-test/permissions?status=pending&limit=200',
    ])
    assert.equal(permissions.length, 1)
    assert.equal(permissions[0]?.id, 'perm-pending')
    assert.equal(permissions[0]?.status, 'pending')
  })
})
