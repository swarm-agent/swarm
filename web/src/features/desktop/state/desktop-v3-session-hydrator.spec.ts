import test from 'node:test'
import assert from 'node:assert/strict'
import { ensureDesktopSession } from '../../../app/api'
import { selectAndHydrateDesktopV3Session } from './desktop-v3-session-hydrator'
import { getDesktopV3CacheSnapshot } from './desktop-v3-cache-store'

// Purpose: selectAndHydrateDesktopV3Session owns selected transcript hydration.
// Rapid A → B → A navigation must replace aborted A, not report a successful
// empty transcript. Deferred HTTP failures prove cancellation/deduplication and
// cleanup without mounting the entire Desktop or using a live daemon.
test('rapid return to a session replaces aborted hydration without deleting its replacement', { timeout: 5000 }, async () => {
  const originalFetch = globalThis.fetch
  const requests: Array<{ signal: AbortSignal; reject: (error: Error) => void }> = []
  globalThis.fetch = async (url, init) => {
    if (String(url).includes('/auth/desktop/session')) return new Response(JSON.stringify({ user_id: 'test-user', account_scope_id: 'test-account' }))
    return new Promise<Response>((_resolve, reject) => {
      const signal = init!.signal as AbortSignal
      if (signal.aborted) { reject(new DOMException('Aborted', 'AbortError')); return }
      requests.push({ signal, reject })
    })
  }
  const flush = async () => { for (let i = 0; i < 20; i++) await Promise.resolve() }
  try {
    await ensureDesktopSession()
    const first = selectAndHydrateDesktopV3Session('rapid-a')
    await flush()
    const second = selectAndHydrateDesktopV3Session('rapid-b')
    // Return before aborted A's promise cleanup gets a microtask.
    const replacement = selectAndHydrateDesktopV3Session('rapid-a')
    const replacementFailure = assert.rejects(replacement, /replacement failure/)
    await flush()
    assert.equal(requests.length, 3, 'A, B and replacement A each own a distinct request')
    assert.notEqual(first, replacement)
    assert.equal(requests[0].signal.aborted, true)
    assert.equal(requests[1].signal.aborted, true)
    assert.equal(requests[2].signal.aborted, false)
    requests[0].reject(new DOMException('Aborted', 'AbortError'))
    requests[1].reject(new DOMException('Aborted', 'AbortError'))
    await Promise.all([first, second])
    assert.equal(selectAndHydrateDesktopV3Session('rapid-a'), replacement, 'old finally must not remove replacement')
    assert.equal(getDesktopV3CacheSnapshot().selectedSessionId, 'rapid-a')
    assert.ok(getDesktopV3CacheSnapshot().hydrateInFlightBySession['rapid-a'] > 0, 'aborted cleanup must preserve replacement loading')
    requests[2].reject(new Error('replacement failure'))
    await replacementFailure
    assert.equal(getDesktopV3CacheSnapshot().hydrateInFlightBySession['rapid-a'] ?? 0, 0)
    assert.equal(getDesktopV3CacheSnapshot().messagesBySession['rapid-a'], undefined, 'failed reads must not invent history')
  } finally {
    for (const request of requests) request.reject(new DOMException('Aborted', 'AbortError'))
    globalThis.fetch = originalFetch
  }
})

// Purpose: account reset must retire selected hydration as well as child queues.
// The production selectAndHydrateDesktopV3Session boundary is exercised with a
// deferred transport to prove old-account requests cannot be reused or published.
test('account reset aborts selected hydration and permits a fresh same-ID read', { timeout: 5000 }, async () => {
  const originalFetch = globalThis.fetch
  const requests: Array<{ signal: AbortSignal; reject: (error: Error) => void }> = []
  globalThis.fetch = async (url, init) => {
    if (String(url).includes('/auth/desktop/session')) return new Response(JSON.stringify({ user_id: 'test-user', account_scope_id: 'test-account' }))
    return new Promise<Response>((_resolve, reject) => { requests.push({ signal: init!.signal as AbortSignal, reject }) })
  }
  const flush = async () => { for (let i = 0; i < 20; i++) await Promise.resolve() }
  try {
    await ensureDesktopSession(true)
    const old = selectAndHydrateDesktopV3Session('reset-session')
    await flush()
    await ensureDesktopSession(true)
    assert.equal(requests[0].signal.aborted, true)
    const current = selectAndHydrateDesktopV3Session('reset-session')
    const failure = assert.rejects(current, /current failure/)
    await flush()
    assert.notEqual(old, current)
    assert.equal(requests.length, 2)
    requests[0].reject(new DOMException('Aborted', 'AbortError'))
    await old
    assert.equal(selectAndHydrateDesktopV3Session('reset-session'), current)
    requests[1].reject(new Error('current failure'))
    await failure
    assert.equal(getDesktopV3CacheSnapshot().messagesBySession['reset-session'], undefined)
  } finally {
    for (const request of requests) request.reject(new DOMException('Aborted', 'AbortError'))
    globalThis.fetch = originalFetch
  }
})
