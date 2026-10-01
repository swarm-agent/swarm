import assert from 'node:assert/strict'
import test from 'node:test'
import { designDownloadName, designEditBody, designRefKey, designSandbox, designSelectionBody, fetchDesignView, postDesign, type DesignRevision } from './design-api'

const ref = (revision: number) => ({ artifact_id: 'design', revision, sha256: `hash-${revision}` })
const revision = (n: number, kind = 'html'): DesignRevision => ({ ref: ref(n), kind, attempt: { number: 1, state: 'succeeded' } })

// Purpose: request builders are the narrowest boundary proving edits retain a viewed
// historical base, independent of selected/latest. CAS includes exact current + version
// to prevent ABA; retry keys must not be replaced by a hidden new admission.
test('two edit intents preserve exact bases and selection CAS is separate', () => {
  const first = designEditBody(ref(1), 'first edit', 'key-1')
  const second = designEditBody(ref(2), 'second edit', 'key-2')
  const reopened = designEditBody(ref(1), 'branch from original', 'key-3')
  assert.deepEqual(first.ref, ref(1)); assert.deepEqual(second.ref, ref(2)); assert.deepEqual(reopened.ref, ref(1))
  assert.deepEqual(designEditBody(ref(1), 'first edit', 'key-1'), first)
  const selected = designSelectionBody(ref(1), { id: 'design', kind: 'html', revision_count: 3, selection_version: 7, selected: ref(3) }, 'select-key')
  assert.deepEqual(selected, { action: 'select', ref: ref(1), expected_version: 7, expected_current: ref(3), idempotency_key: 'select-key' })
  assert.notEqual(designRefKey(ref(1)), designRefKey({ ...ref(1), sha256: 'different' }))
})

// Purpose: HTTP action boundary must never load authored HTML into the credentialed
// Desktop renderer. Verify PNG wrapper action, abort propagation, safe sandbox and
// explicit attachment download; conflict responses must reject without auto-retarget.
test('preview uses only trusted wrapper, plan reads text, CAS conflict remains an error', async () => {
  const original = globalThis.fetch
  const calls: Array<{ body: Record<string, unknown>; signal?: AbortSignal | null }> = []
  globalThis.fetch = (async (_input: RequestInfo | URL, init?: RequestInit) => {
    const body = JSON.parse(String(init?.body)) as Record<string, unknown>
    calls.push({ body, signal: init?.signal })
    if (body.action === 'select') return new Response('stale revision', { status: 409 })
    return new Response(body.action === 'preview_html' ? '<img src="data:image/png;base64,example">' : 'plan text')
  }) as typeof fetch
  try {
    const controller = new AbortController()
    await fetchDesignView('session', revision(1), controller.signal)
    await fetchDesignView('session', revision(2, 'plan'))
    assert.equal(calls[0].body.action, 'preview_html')
    assert.equal(calls[0].signal, controller.signal)
    controller.abort(); assert.equal(calls[0].signal?.aborted, true)
    assert.equal(calls[1].body.action, 'read')
    assert.equal(designSandbox, '')
    assert.equal(designDownloadName(revision(1)), 'design-r1.html')
    assert.equal(designDownloadName(revision(2, 'plan')), 'design-r2.txt')
    await assert.rejects(postDesign('session', ref(1), designSelectionBody(ref(1), { id: 'design', kind: 'html', revision_count: 2, selection_version: 1, selected: ref(2) }, 'cas')), /409: stale revision/)
    assert.equal(calls.length, 3)
    assert.deepEqual(calls[2].body.ref, ref(1))
    await postDesign('session', ref(1), { action: 'download', ref: ref(1) })
    assert.deepEqual(calls[3].body, { action: 'download', ref: ref(1) })
  } finally { globalThis.fetch = original }
})

// Purpose: canonical user-message metadata is only a requested edit, never acceptance.
// Parse exact base identity and reject assistant/invalid metadata at the API boundary.
test('durable edit messages retain exact bases without manufacturing accepted state', async () => {
  const { designEditRequests, fetchProjectDesigns } = await import('./design-api')
  const message = { id: 'message', session_id: 's', global_seq: 1, role: 'user', content: 'edit', created_at: 1, metadata: { design_edit_request: { state: 'requested', client_request_id: 'key', base: ref(1) } } }
  assert.deepEqual(designEditRequests([message]), [{ messageId: 'message', clientRequestId: 'key', base: ref(1), brief: 'edit' }])
  assert.deepEqual(designEditRequests([{ ...message, role: 'assistant' }]), [])
  const original = globalThis.fetch
  let url = ''; let cache: RequestCache | undefined
  globalThis.fetch = async (input, init) => { url = String(input); cache = init?.cache; return Response.json({ designs: [], next_cursor: 'opaque/two' }) }
  try {
    assert.equal((await fetchProjectDesigns('p /', 'opaque/one')).next_cursor, 'opaque/two')
    assert.equal(url, '/v3/projects/p%20%2F/designs?limit=20&after=opaque%2Fone')
    assert.equal(cache, 'no-store')
  } finally { globalThis.fetch = original }
})
