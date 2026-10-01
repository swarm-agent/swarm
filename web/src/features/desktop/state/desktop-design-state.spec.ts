import assert from 'node:assert/strict'
import test from 'node:test'
import { DesktopDesignState, DesignResource, designEventSession } from './desktop-design-state'
import type { DesignCatalog, DesignHistory, DesignRevision } from '../session-v3/design-api'

const revision = (n: number): DesignRevision => ({ ref: { artifact_id: 'design', revision: n, sha256: `hash-${n}` }, kind: 'html', attempt: { number: 1, state: 'succeeded' }, ...(n > 1 ? { base: { artifact_id: 'design', revision: n - 1, sha256: `hash-${n - 1}` } } : {}) })
const history = (revisions: DesignRevision[]): DesignHistory => ({ artifact: { id: 'design', kind: 'html', revision_count: revisions.length, selection_version: 0 }, revisions })

// Purpose: DesktopDesignState is the narrow read-model boundary for immutable history.
// Prevent an edit/refresh from overwriting old revisions or hiding one failed sibling.
test('two edits retain all parentage and independent failed siblings', async () => {
  const revisions = [revision(1)]
  const state = new DesktopDesignState({
    catalog: async () => ({ next_cursor: '', requests: [{ id: 'request', state: 'partial_success', candidates: [
      { spec: { artifact_id: 'design', kind: 'html' }, state: 'succeeded' },
      { spec: { artifact_id: 'failed-design', kind: 'html' }, state: 'failed', failure_reason: 'validation_failed' },
    ] }] }),
    history: async () => history([...revisions]),
  })
  const resource = state.history('session', 'design')
  await resource.refresh()
  const original = resource.getSnapshot().data!.revisions[0]
  revisions.push(revision(2)); await resource.refresh()
  revisions.push(revision(3)); await resource.refresh()
  assert.deepEqual(resource.getSnapshot().data!.revisions, [revision(1), revision(2), revision(3)])
  assert.deepEqual(original, revision(1))
  assert.equal(resource.getSnapshot().data!.artifact.selected, undefined)
  await state.catalog('session').refresh()
  assert.deepEqual(state.catalog('session').getSnapshot().data!.requests[0].candidates.map(row => row.state), ['succeeded', 'failed'])
})

// Purpose: DesignResource serializes hydration. A reconnect/event during an in-flight
// read must cause one follow-up, not concurrent requests or recurring polling.
test('in-flight invalidations coalesce and errors stay visible until explicit retry', async () => {
  let finish!: (value: number) => void
  let calls = 0
  const resource = new DesignResource(async () => { calls++; if (calls === 1) return new Promise<number>(resolve => { finish = resolve }); if (calls === 2) throw new Error('unavailable'); return 3 })
  const first = resource.refresh()
  await Promise.resolve()
  resource.refresh(); resource.refresh()
  assert.equal(calls, 1)
  finish(1)
  await first
  assert.equal(calls, 2)
  assert.equal(resource.getSnapshot().data, undefined)
  assert.equal(resource.getSnapshot().error, 'unavailable')
  await resource.refresh()
  assert.deepEqual(resource.getSnapshot(), { data: 3, loading: false })
})

// Purpose: designEventSession and DesktopDesignState own event scoping; unrelated
// sessions/usage cannot trigger reads. Reconnect repairs only subscribed resources.
test('scoped events ignore chatter and reconnect hydrates active sessions only', async () => {
  const calls: string[] = []
  const state = new DesktopDesignState({ catalog: async session => { calls.push(session); return { requests: [], next_cursor: '' } }, history: async () => history([]) })
  const a = state.catalog('a'); const b = state.catalog('b')
  const unsubscribe = a.subscribe(() => {})
  assert.equal(designEventSession({ kind: 'event', event: { event_type: 'session.usage.updated', session_id: 'a' } }), undefined)
  assert.equal(designEventSession({ kind: 'event', event: { event_type: 'design.updated', session_id: 'b' } }), 'b')
  state.invalidate('b'); await Promise.resolve(); assert.deepEqual(calls, [])
  state.invalidate('a'); await a.refresh(); assert.deepEqual(calls, ['a'])
  state.invalidate(); await a.refresh(); assert.deepEqual(calls, ['a', 'a'])
  assert.equal(b.getSnapshot().data, undefined)
  unsubscribe()
})

// Purpose: bounded page loading in DesktopDesignState must preserve immutable pages
// and forward opaque catalog cursors, while rejecting a non-advancing response.
test('history and catalog pagination preserve earlier records', async () => {
  const pages: number[] = []
  const cursors: string[] = []
  const state = new DesktopDesignState({
    history: async (_session, _artifact, after) => { pages.push(after); return { ...history([]), artifact: { ...history([]).artifact, revision_count: 51 }, revisions: after === 0 ? Array.from({ length: 50 }, (_, i) => revision(i + 1)) : [revision(51)] } },
    catalog: async (_session, after): Promise<DesignCatalog> => { cursors.push(after); return { requests: [{ id: after || 'first', state: 'queued', candidates: [] }], next_cursor: after ? '' : 'opaque cursor' } },
  })
  await state.history('s', 'design').refresh()
  await state.moreHistory('s', 'design')
  assert.deepEqual(pages, [0, 0, 50])
  assert.equal(state.history('s', 'design').getSnapshot().data!.revisions.length, 51)
  await state.catalog('s').refresh(); await state.moreRequests('s')
  assert.deepEqual(cursors, ['', '', 'opaque cursor'])
  assert.equal(state.catalog('s').getSnapshot().data!.requests.length, 2)
})

// Purpose: DesktopDesignState must fail visibly on malformed pagination rather than
// repeatedly appending duplicates or exposing stale private data after failure.
test('non-advancing cursor is rejected and clears stale catalog', async () => {
  const state = new DesktopDesignState({ catalog: async () => ({ requests: [], next_cursor: 'same' }), history: async () => history([]) })
  const resource = state.catalog('s')
  await resource.refresh()
  await state.moreRequests('s')
  assert.equal(resource.getSnapshot().data, undefined)
  assert.match(resource.getSnapshot().error!, /did not advance/)
})

// Purpose: DesignResource reset is the privacy boundary on teardown/auth change.
// Deferred success/failure and their finalizers must not resurrect data or disturb a new flight.
test('reset fences old responses and finalizers while allowing fresh hydration', async () => {
  const finishes: Array<(value: string) => void> = []
  const resource = new DesignResource(() => new Promise<string>(resolve => finishes.push(resolve)))
  const old = resource.refresh(); await Promise.resolve()
  resource.reset()
  const fresh = resource.refresh(); await Promise.resolve()
  finishes[0]('private old data'); await old
  assert.deepEqual(resource.getSnapshot(), { loading: true, error: undefined })
  assert.equal(resource.refresh(), fresh)
  finishes[1]('fresh data')
  // The explicit refresh coalesces one further read after completion.
  for (let i = 0; i < 10 && finishes.length < 3; i++) await Promise.resolve()
  assert.equal(finishes.length, 3)
  finishes[2]('current data'); await fresh
  assert.equal(resource.getSnapshot().data, 'current data')
  resource.dispose(); await resource.refresh()
  assert.deepEqual(resource.getSnapshot(), { loading: false })
  assert.equal(finishes.length, 3)
})

// Purpose: useSyncExternalStore unsubscribe is the real gallery teardown boundary.
// Last-consumer release clears bytes, but one view cannot clear another active view/session.
test('last unsubscribe clears only that resource and session switching starts empty', async () => {
  const state = new DesktopDesignState({
    catalog: async session => ({ requests: [{ id: session, state: 'queued', candidates: [] }], next_cursor: '' }),
    history: async () => history([]),
  })
  const a = state.catalog('a'); const b = state.catalog('b')
  const releaseA = a.subscribe(() => {}); const releaseSecondA = a.subscribe(() => {})
  const releaseB = b.subscribe(() => {})
  await a.refresh(); await b.refresh()
  releaseA()
  assert.equal(a.getSnapshot().data!.requests[0].id, 'a')
  releaseSecondA()
  assert.equal(a.getSnapshot().data, undefined)
  assert.equal(b.getSnapshot().data!.requests[0].id, 'b')
  assert.equal(state.catalog('c').getSnapshot().data, undefined)
  assert.equal(state.catalog('a').getSnapshot().data, undefined)
  releaseB()
})

// Purpose: cache eviction bounds inactive sessions/history without evicting active views.
// A deferred response owned by an evicted entry cannot restore its private metadata.
test('bounded inactive eviction disposes old catalog and history resources', async () => {
  let finish!: (value: DesignCatalog) => void
  const state = new DesktopDesignState({ catalog: () => new Promise(resolve => { finish = resolve }), history: async () => history([]) })
  const active = state.catalog('active'); const release = active.subscribe(() => {})
  const old = state.catalog('old'); const flight = old.refresh(); await Promise.resolve()
  const oldHistory = state.history('old', 'design'); await oldHistory.refresh()
  for (let i = 0; i < 40; i++) { state.catalog(`s-${i}`); state.history(`s-${i}`, 'design') }
  finish({ requests: [{ id: 'private', state: 'queued', candidates: [] }], next_cursor: '' }); await flight
  assert.equal(old.getSnapshot().data, undefined)
  assert.equal(oldHistory.getSnapshot().data, undefined)
  assert.notEqual(state.catalog('old'), old)
  assert.equal(state.catalog('active'), active)
  release()
})

// Purpose: catalog refresh always starts at the first page, echoing opaque cursors.
// More than twenty records and a newly inserted request must remain visible after refresh.
test('refresh after pagination includes newest rows and echoes opaque cursors', async () => {
  const rows = Array.from({ length: 25 }, (_, i) => ({ id: `request-${i}`, state: 'queued', candidates: [] }))
  const cursors: string[] = []
  const state = new DesktopDesignState({
    catalog: async (_session, cursor) => {
      cursors.push(cursor)
      return cursor ? { requests: rows.slice(20), next_cursor: '' } : { requests: rows.slice(0, 20), next_cursor: 'opaque:page/two==' }
    }, history: async () => history([]),
  })
  const resource = state.catalog('s')
  await resource.refresh(); await state.moreRequests('s')
  assert.equal(resource.getSnapshot().data!.requests.length, 25)
  rows.unshift({ id: 'newest', state: 'queued', candidates: [] })
  await resource.refresh()
  assert.deepEqual(cursors, ['', '', 'opaque:page/two==', '', 'opaque:page/two=='])
  assert.equal(resource.getSnapshot().data!.requests[0].id, 'newest')
  assert.equal(resource.getSnapshot().data!.requests.length, 26)
})

// Purpose: the teardown fence must reject late failures as well as successes.
// A rejected old read cannot replace freshly authenticated data with its error.
test('last subscriber teardown fences deferred failure and remount can reload', async () => {
  let reject!: (error: Error) => void
  let calls = 0
  const resource = new DesignResource(async () => ++calls === 1 ? new Promise<string>((_resolve, fail) => { reject = fail }) : 'fresh')
  const release = resource.subscribe(() => {})
  const old = resource.refresh(); await Promise.resolve()
  release()
  assert.deepEqual(resource.getSnapshot(), { loading: false })
  const releaseNew = resource.subscribe(() => {})
  await resource.refresh()
  reject(new Error('old private error')); await old
  assert.deepEqual(resource.getSnapshot(), { loading: false, data: 'fresh' })
  releaseNew()
})

// Purpose: project discovery owns bounded pagination and reset privacy. Empty membership
// pages must retain continuation; repeated requests deduplicate by session/request, and
// reconnect re-traverses from the beginning instead of appending stale snapshots.
test('project pages include empty continuations, deduplicate and refresh scoped resources', async () => {
  const calls: string[] = []
  const row = { project_id: 'p', title: 'Design', request: { id: 'r', parent_session_id: 's', revision: 1, state: 'queued', candidates: [] } }
  const state = new DesktopDesignState({ catalog: async () => ({ requests: [], next_cursor: '' }), history: async () => history([]), project: async (project, cursor) => {
    calls.push(`${project}:${cursor}`)
    if (!cursor) return { designs: [], next_cursor: 'opaque/one' }
    if (cursor === 'opaque/one') return { designs: [row], next_cursor: 'opaque/two' }
    return { designs: [{ ...row, request: { ...row.request, revision: 2, state: 'running' } }], next_cursor: '' }
  } })
  const resource = state.project('p'); const release = resource.subscribe(() => {})
  await resource.refresh(); await state.moreProject('p'); await state.moreProject('p')
  assert.equal(resource.getSnapshot().data!.designs.length, 1)
  assert.equal(resource.getSnapshot().data!.designs[0].request.state, 'running')
  state.invalidateProject('other'); await Promise.resolve()
  assert.equal(calls.length, 6)
  state.invalidate(); await resource.refresh()
  assert.deepEqual(calls.slice(-3), ['p:', 'p:opaque/one', 'p:opaque/two'])
  release(); assert.equal(resource.getSnapshot().data, undefined)
})
// Purpose: DesignResource must actively abort transport on identity/last-consumer reset,
// not only hide its eventual value. A transport ignoring abort still cannot restore data.
test('reset aborts project transport and fences its late response', async () => {
  let signal!: AbortSignal; let finish!: (value: string) => void
  const resource = new DesignResource<string>(input => { signal = input; return new Promise(resolve => { finish = resolve }) })
  const release = resource.subscribe(() => {})
  const flight = resource.refresh(); await Promise.resolve()
  release(); assert.equal(signal.aborted, true)
  finish('private'); await flight
  assert.deepEqual(resource.getSnapshot(), { loading: false })
})

// Purpose: canonical edit request pages must survive reopen/reconnect and retain the
// message identity needed for acceptance correlation; a non-advancing page fails closed.
test('older edit requests retain message identity and reset drops all pages', async () => {
  const calls: number[] = []
  const state = new DesktopDesignState({ catalog: async () => ({ requests: [], next_cursor: '' }), history: async () => history([]), edits: async (_session, before) => {
    calls.push(before)
    return { edits: [{ messageId: before ? 'old' : 'new', clientRequestId: before ? 'old-key' : 'new-key', base: revision(1).ref, brief: 'Edit' }], nextBefore: before ? 0 : 50 }
  } })
  const resource = state.editRequests('s')
  await resource.refresh(); await state.moreEdits('s')
  assert.deepEqual(calls, [0, 0, 50])
  assert.deepEqual(resource.getSnapshot().data!.edits.map(edit => edit.messageId), ['new', 'old'])
  state.reset(); assert.equal(resource.getSnapshot().data, undefined)
  await resource.refresh(); assert.equal(resource.getSnapshot().data!.edits.length, 1)
})
// Purpose: malicious/stale project continuations cannot create infinite traversal or
// duplicate metadata; the canonical resource rejects cycles and exposes no partial data.
test('project cursor cycle rejects without publishing partial results', async () => {
  const state = new DesktopDesignState({ catalog: async () => ({ requests: [], next_cursor: '' }), history: async () => history([]), project: async () => ({ designs: [], next_cursor: 'cycle' }) })
  const resource = state.project('p'); await resource.refresh(); await state.moreProject('p')
  assert.match(resource.getSnapshot().error!, /did not advance/)
  assert.equal(resource.getSnapshot().data, undefined)
})
