import assert from 'node:assert/strict'
import test from 'node:test'
import { apiFetch, ensureDesktopSession } from '../../../app/api'
import { desktopDesigns } from './desktop-design-runtime'

// Purpose: exercise the actual app/api auth-reset subscription, not an unreachable reset helper.
// Forced identity refresh and 401 recovery must clear cached private metadata and fence old reads.
test('canonical auth reset clears design data and fences an outstanding response', { timeout: 5000 }, async () => {
  const original = globalThis.fetch
  let finish!: (response: Response) => void
  let blocked = false
  globalThis.fetch = async input => {
    if (String(input) === '/v1/auth/desktop/session') return Response.json({ user_id: 'user', account_scope_id: 'account' })
    if (String(input) === '/protected') return new Response('', { status: 401 })
    if (blocked) return new Promise(resolve => { finish = resolve })
    return Response.json({ requests: [{ id: 'private', state: 'queued', candidates: [] }], next_cursor: '' })
  }
  const resource = desktopDesigns.catalog('auth-fixture')
  const release = resource.subscribe(() => {})
  try {
    await resource.refresh()
    assert.equal(resource.getSnapshot().data!.requests[0].id, 'private')
    await ensureDesktopSession(true)
    assert.equal(resource.getSnapshot().data, undefined)
    blocked = true
    const flight = resource.refresh()
    // Bounded microtask drain gets the deferred transport installed without a timer.
    for (let i = 0; i < 10 && !finish; i++) await Promise.resolve()
    assert.ok(finish)
    await apiFetch('/protected')
    finish(Response.json({ requests: [{ id: 'late-private', state: 'queued', candidates: [] }], next_cursor: '' }))
    await flight
    assert.deepEqual(resource.getSnapshot(), { loading: false })
  } finally { release(); desktopDesigns.reset(); globalThis.fetch = original }
})
