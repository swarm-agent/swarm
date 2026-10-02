import assert from 'node:assert/strict'
import test from 'node:test'
import { apiFetch, ensureDesktopSession } from '../../../app/api'
import { desktopDesigns } from './desktop-design-runtime'

// Purpose: exercise the actual app/api auth-reset subscription, not an unreachable reset helper.
// Forced identity refresh and 401 recovery must clear cached private metadata and fence old reads.
// This runtime-level test is the narrowest layer proving the lazy facade registers with
// app/api and retains DesktopDesignState resource identities without weakening reset.
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
  const facade = desktopDesigns
  const resource = desktopDesigns.catalog('auth-fixture')
  const history = desktopDesigns.history('auth-fixture', 'artifact')
  assert.equal(desktopDesigns.catalog('auth-fixture'), resource)
  assert.equal(desktopDesigns.history('auth-fixture', 'artifact'), history)
  const release = resource.subscribe(() => {})
  try {
    await resource.refresh()
    assert.equal(resource.getSnapshot().data!.requests[0].id, 'private')
    await ensureDesktopSession(true)
    assert.equal(resource.getSnapshot().data, undefined)
    assert.equal(desktopDesigns, facade)
    assert.equal(desktopDesigns.catalog('auth-fixture'), resource)
    assert.equal(desktopDesigns.history('auth-fixture', 'artifact'), history)
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

// Purpose: canonical project.updated frames must refresh the scoped discovery resource
// while chat is unmounted; unrelated project frames must not read private metadata.
test('project design invalidation is scoped and reconnect can refresh subscribed discovery', { timeout: 5000 }, async () => {
  const { acceptDesktopDesignEvent } = await import('./desktop-design-runtime')
  const original = globalThis.fetch
  const calls: string[] = []
  globalThis.fetch = async input => { calls.push(String(input)); return Response.json({ designs: [], next_cursor: '' }) }
  const resource = desktopDesigns.project('project-fixture'); const release = resource.subscribe(() => {})
  try {
    acceptDesktopDesignEvent({ kind: 'project.updated', project_id: 'other' })
    await Promise.resolve(); assert.deepEqual(calls, [])
    acceptDesktopDesignEvent({ kind: 'project.updated', project_id: 'project-fixture' })
    await resource.refresh()
    assert.equal(calls.length, 1)
    assert.match(calls[0], /projects\/project-fixture\/designs/)
    desktopDesigns.invalidate(); await resource.refresh()
    assert.equal(calls.length, 2)
  } finally { release(); desktopDesigns.reset(); globalThis.fetch = original }
})
