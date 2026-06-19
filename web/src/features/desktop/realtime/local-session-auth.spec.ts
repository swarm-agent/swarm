import test from 'node:test'
import assert from 'node:assert/strict'

test('Desktop local session bootstrap exposes canonical account/user identity without a second request', async () => {
  const fetchCalls: string[] = []
  const originalFetch = globalThis.fetch

  globalThis.fetch = (async (input: RequestInfo | URL) => {
    const url = String(input)
    fetchCalls.push(url)
    if (url === '/v1/auth/desktop/session') {
      return new Response(JSON.stringify({
        ok: true,
        user_id: 'user-local',
        account_scope_id: 'acct-local',
        username: 'local-user',
        expires_at: '2030-01-01T00:00:00Z',
      }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    const { ensureDesktopSession, getDesktopSessionIdentitySnapshot } = await import('../../../app/api')

    const identity = await ensureDesktopSession(true)

    assert.deepEqual(identity, {
      userId: 'user-local',
      accountScopeId: 'acct-local',
      username: 'local-user',
      expiresAt: '2030-01-01T00:00:00Z',
    })
    assert.deepEqual(getDesktopSessionIdentitySnapshot(), identity)

    const cachedIdentity = await ensureDesktopSession()
    assert.deepEqual(cachedIdentity, identity)
    assert.deepEqual(fetchCalls, ['/v1/auth/desktop/session'])
  } finally {
    globalThis.fetch = originalFetch
  }
})
