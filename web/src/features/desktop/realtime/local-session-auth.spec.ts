import test from 'node:test'
import assert from 'node:assert/strict'

import { buildDesktopV3RealtimeTransportSocketURL } from './client'

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


test('Desktop V3 realtime transport URL contains only opaque endpoint cursor', () => {
  const url = buildDesktopV3RealtimeTransportSocketURL({
    endpointCursor: 'v3c1.payload with spaces.signature/opaque',
    protocol: 'ws:',
    host: '127.0.0.1:5555',
  })

  assert.equal(url.toString(), 'ws://127.0.0.1:5555/v3/realtime/stream?endpoint_cursor=v3c1.payload+with+spaces.signature%2Fopaque')
  assert.equal(url.pathname, '/v3/realtime/stream')
  assert.deepEqual(Array.from(url.searchParams.keys()), ['endpoint_cursor'])
  assert.equal(url.searchParams.get('endpoint_cursor'), 'v3c1.payload with spaces.signature/opaque')
  assert.equal(url.searchParams.get('token'), null)
  assert.equal(url.searchParams.get('auth_token'), null)
  assert.equal(url.searchParams.get('after_seq'), null)
  assert.equal(url.searchParams.get('after_rev'), null)
})
