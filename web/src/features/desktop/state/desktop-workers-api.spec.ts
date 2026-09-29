import assert from 'node:assert/strict'
import test from 'node:test'
import { mutateWorker, readWorkers } from './desktop-workers-api'
import { realtimeFrameToActions } from './desktop-v3-cache-wire'

// Requirement: Desktop uses the canonical /v3/workers contracts and revision guards.
// Threat: a malformed response or failed stop is mistaken for success, or a worker
// notification is rejected by the V3 frame codec. API and codec are the narrowest
// deterministic layers; a live authenticated daemon still needs testbench validation.
test('worker realtime invalidation is a valid content-free control frame', () => {
  const actions = realtimeFrameToActions({ protocol: 'v3.realtime', protocol_version: 1, kind: 'worker.updated', endpoint_cursor: 'opaque' })
  assert.equal(actions.length, 1)
  assert.equal(actions[0].type, 'realtime.control')
  assert.throws(() => realtimeFrameToActions({ protocol: 'v3.realtime', protocol_version: 1, kind: 'worker.updated', endpoint_cursor: '' }), /endpoint cursor/)
  assert.throws(() => realtimeFrameToActions({ protocol: 'v3.realtime', protocol_version: 1, kind: 'worker.updated', endpoint_cursor: 'opaque', session: { id: 'leak' } as never }), /only an endpoint cursor/)
})
test('revision and malformed acknowledgement prevent worker mutations from appearing successful', async () => {
  await assert.rejects(mutateWorker({ action: 'pause', workerId: 'worker', expected_revision: 0 }), /revision/)
  const previous = globalThis.fetch
  const calls: Array<{ url: string; init: RequestInit }> = []
  globalThis.fetch = (async (url: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ url: String(url), init: init ?? {} })
    if (String(url).includes('/v1/auth/desktop/session')) return new Response(JSON.stringify({ user_id: 'user', account_scope_id: 'account' }), { status: 200 })
    return new Response(JSON.stringify({ ok: true }), { status: 200 })
  }) as typeof fetch
  try {
    await assert.rejects(mutateWorker({ action: 'pause', workerId: 'worker', expected_revision: 2 }), /not confirmed/)
    assert.equal(calls.at(-1)?.url, '/v3/workers/worker/pause')
    assert.deepEqual(JSON.parse(String(calls.at(-1)?.init.body)), { expected_revision: 2 })
  } finally { globalThis.fetch = previous }
})
test('bounded paginated worker read rejects malformed envelope', async () => {
  const previous = globalThis.fetch
  globalThis.fetch = (async (url: RequestInfo | URL) => {
    if (String(url).includes('/v1/auth/desktop/session')) return new Response(JSON.stringify({ user_id: 'user', account_scope_id: 'account' }), { status: 200 })
    assert.match(String(url), /\/v3\/workers\?cursor=opaque&limit=20/)
    return new Response(JSON.stringify({ workers: null }), { status: 200 })
  }) as typeof fetch
  try { await assert.rejects(readWorkers({ kind: 'list', accountScopeId: 'account', cursor: 'opaque', limit: 20 }), /Invalid worker page/) }
  finally { globalThis.fetch = previous }
})
