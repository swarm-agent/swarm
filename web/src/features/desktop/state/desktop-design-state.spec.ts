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
  assert.equal(resource.getSnapshot().data, 1)
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
// repeatedly appending duplicates; retained successful data remains available.
test('non-advancing cursor is rejected without replacing retained catalog', async () => {
  const state = new DesktopDesignState({ catalog: async () => ({ requests: [], next_cursor: 'same' }), history: async () => history([]) })
  const resource = state.catalog('s')
  await resource.refresh()
  const retained = resource.getSnapshot().data
  await state.moreRequests('s')
  assert.equal(resource.getSnapshot().data, retained)
  assert.match(resource.getSnapshot().error!, /did not advance/)
})
