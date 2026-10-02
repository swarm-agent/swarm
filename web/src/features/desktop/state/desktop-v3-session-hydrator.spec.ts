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
      requests.push({ signal: init!.signal as AbortSignal, reject })
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
    assert.equal(requests.length, 2, 'B is cancelled before its authenticated fetch starts')
    assert.notEqual(first, replacement)
    assert.equal(requests[0].signal.aborted, true)
    assert.equal(requests[1].signal.aborted, false)
    requests[0].reject(new DOMException('Aborted', 'AbortError'))
    await Promise.all([first, second])
    assert.equal(selectAndHydrateDesktopV3Session('rapid-a'), replacement, 'old finally must not remove replacement')
    assert.equal(getDesktopV3CacheSnapshot().selectedSessionId, 'rapid-a')
    assert.ok(getDesktopV3CacheSnapshot().hydrateInFlightBySession['rapid-a'] > 0, 'aborted cleanup must preserve replacement loading')
    requests[1].reject(new Error('replacement failure'))
    await replacementFailure
    assert.equal(getDesktopV3CacheSnapshot().hydrateInFlightBySession['rapid-a'] ?? 0, 0)
    assert.equal(getDesktopV3CacheSnapshot().messagesBySession['rapid-a'], undefined, 'failed reads must not invent history')
  } finally { globalThis.fetch = originalFetch }
})
