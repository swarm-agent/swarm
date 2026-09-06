import assert from 'node:assert/strict'
import { afterEach, test } from 'node:test'

import { apiFetch, ensureDesktopSession, getDesktopSessionIdentitySnapshot, requestJson, requestStartupJson } from './api'
import { STARTUP_REQUEST_TIMEOUT_MS } from './request-lifecycle'

const originalFetch = globalThis.fetch

afterEach(() => {
  globalThis.fetch = originalFetch
})

test('requestJson disables browser cache by default', async () => {
  let captured: RequestInit | undefined
  globalThis.fetch = (async (_input: RequestInfo | URL, init?: RequestInit) => {
    captured = init
    return new Response(JSON.stringify({ ok: true }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch

  await requestJson<{ ok: boolean }>('/v1/ui/settings', undefined, false)

  assert.equal(captured?.cache, 'no-store')
})

test('requestJson preserves explicit cache option', async () => {
  let captured: RequestInit | undefined
  globalThis.fetch = (async (_input: RequestInfo | URL, init?: RequestInit) => {
    captured = init
    return new Response(JSON.stringify({ ok: true }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    })
  }) as typeof fetch

  await requestJson<{ ok: boolean }>('/v1/ui/settings', { cache: 'reload' }, false)

  assert.equal(captured?.cache, 'reload')
})

// Purpose: apiFetch/requestJson/auth bootstrap own finite startup waits and
// cancellation-safe 401 recovery. Fake transports include headers-only/error-body
// hangs and late identity responses; assert rejection AND no stale/auth resend.
const flush = async () => { for (let i = 0; i < 20; i++) await Promise.resolve() }

test('startup budget includes success and error response bodies and supports retry', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  for (const status of [200, 503]) {
    let signal!: AbortSignal
    globalThis.fetch = async (_input, init) => {
      signal = init!.signal!
      return new Response(new ReadableStream({ start() {} }), { status })
    }
    const pending = requestStartupJson('/v1/onboarding', undefined, false)
    const rejected = assert.rejects(pending, { name: 'TimeoutError' })
    await flush()
    t.mock.timers.tick(STARTUP_REQUEST_TIMEOUT_MS)
    await rejected
    assert.equal(signal.aborted, true)
  }
  globalThis.fetch = async () => Response.json({ ok: true })
  assert.deepEqual(await requestStartupJson('/v1/onboarding', undefined, false), { ok: true })
})

test('shared auth timeout releases pending state and cannot publish a late identity', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  let finish!: (response: Response) => void
  let signal!: AbortSignal
  globalThis.fetch = async (_input, init) => {
    signal = init!.signal!
    return new Promise((resolve) => { finish = resolve })
  }
  const first = ensureDesktopSession(true)
  const second = ensureDesktopSession()
  const rejected = Promise.all([assert.rejects(first, { name: 'TimeoutError' }), assert.rejects(second, { name: 'TimeoutError' })])
  t.mock.timers.tick(STARTUP_REQUEST_TIMEOUT_MS)
  await rejected
  assert.equal(signal.aborted, true)
  globalThis.fetch = async () => Response.json({ user_id: 'fresh', account_scope_id: 'account' })
  assert.equal((await ensureDesktopSession()).userId, 'fresh')
  finish(Response.json({ user_id: 'stale', account_scope_id: 'account' }))
  await flush()
  assert.equal(getDesktopSessionIdentitySnapshot()?.userId, 'fresh')
})

test('abort before a 401 or while waiting on shared auth never resends', async () => {
  const before = new AbortController()
  let calls = 0
  globalThis.fetch = async () => { calls++; before.abort(); return new Response('', { status: 401 }) }
  await assert.rejects(apiFetch('/resource', { signal: before.signal }), { name: 'AbortError' })
  assert.equal(calls, 1)

  const caller = new AbortController()
  let finish!: (response: Response) => void
  const paths: string[] = []
  globalThis.fetch = async (input) => {
    paths.push(String(input))
    if (String(input) === '/v1/auth/desktop/session') return new Promise((resolve) => { finish = resolve })
    return new Response('', { status: 401 })
  }
  const pending = apiFetch('/resource', { signal: caller.signal })
  await flush()
  const live = ensureDesktopSession()
  caller.abort()
  await assert.rejects(pending, { name: 'AbortError' })
  finish(Response.json({ user_id: 'live', account_scope_id: 'account' }))
  assert.equal((await live).userId, 'live')
  assert.deepEqual(paths, ['/resource', '/v1/auth/desktop/session'])
})

test('raw streams and long mutations have no startup deadline', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] })
  let finish!: (response: Response) => void
  let signal!: AbortSignal
  globalThis.fetch = async (_input, init) => { signal = init!.signal!; return new Promise((resolve) => { finish = resolve }) }
  const mutation = requestJson('/mutation', { method: 'POST' }, false)
  t.mock.timers.tick(120_000)
  assert.equal(signal.aborted, false)
  finish(Response.json({ ok: true }))
  await mutation
  const body = new ReadableStream({ start() {} })
  globalThis.fetch = async () => new Response(body)
  const response = await apiFetch('/stream', undefined, false)
  t.mock.timers.tick(120_000)
  assert.equal(response.body, body)
  await response.body?.cancel()
})
