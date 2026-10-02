import assert from 'node:assert/strict'
import test from 'node:test'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MediaTaskCard } from './media-task-card'
import { designMediaItem, designNodeId, designReadyItems, designThreads, designRequestId, readyDesignRevision } from './design-media-task'
import type { ProjectDesign } from '../session-v3/design-api'
import { getMediaIterationOutputs } from '../tools/media-library/media-iteration-thread'
import { validateMediaGenerationRequest } from '../tools/media-library/media-generation'

const ref = { artifact_id: 'design', revision: 1, sha256: 'digest' }
const row: ProjectDesign = { project_id: 'project', title: 'Landing page', request: { id: 'request', parent_session_id: 'session', state: 'partial_success', candidates: [
  { spec: { artifact_id: 'design', kind: 'html' }, state: 'succeeded', attempts: [{ number: 1, state: 'succeeded', result: ref }] },
  { spec: { artifact_id: 'failed', kind: 'html' }, state: 'failed', failure_reason: 'validation_failed' },
] } }
// Purpose: presentation conversion is the narrowest layer proving independent reference
// identity and no Artifact V3 fabrication. Failed candidates cannot become ready outputs,
// and same-title/session/candidate siblings must never collapse into one node.
test('ready nodes retain exact session-qualified identity without URLs or artifacts', () => {
  const items = designReadyItems([row, row])
  assert.equal(items.length, 1)
  assert.equal(items[0].source, 'independent-design')
  assert.equal(items[0].artifact, undefined)
  assert.equal(items[0].directUrl, '')
  assert.notEqual(designNodeId('other', ref), items[0].id)
  assert.notEqual(designNodeId('session', { ...ref, sha256: 'other' }), items[0].id)
  assert.equal(readyDesignRevision(row.request.candidates[1]), undefined)
  assert.deepEqual(getMediaIterationOutputs({ id: 'turn', sourceId: items[0].id, title: '', status: 'succeeded', count: 1, outputIds: [items[0].id] }, items), items)
})
// Purpose: exact historical branching must survive presentation, while the existing
// image/video mutation validator must reject designs even if a caller misroutes one.
test('historical base is explicit and generation cannot cross the source boundary', () => {
  const revision = { ref: { ...ref, revision: 3 }, kind: 'html', base: ref, attempt: { number: 1, state: 'succeeded' } }
  const item = designMediaItem(row, 0, revision)
  assert.equal(item.parentId, designNodeId('session', ref))
  assert.deepEqual(item.design?.revision.base, ref)
  assert.equal(validateMediaGenerationRequest({ action: 'iterate', item, model: '', prompt: 'edit', settings: {} }).valid, false)
})

// Purpose: MediaTaskCard's real renderer must report server state/partial counts and
// persistent failures, without offering image mutation or project-task approval on designs.
test('design cards render queued running failed cancelled and partial outcomes truthfully', () => {
  for (const state of ['queued', 'running', 'failed', 'cancelled', 'partial_success']) {
    const html = renderToStaticMarkup(createElement(MediaTaskCard, { source: 'independent-design', design: { ...row, request: { ...row.request, state } }, onDesignPreview: () => {} }))
    assert.ok(html.includes(state.split('_').join(' ')))
    assert.match(html, /1 turn · 1 ready/)
    assert.equal((html.match(/class="creative-candidate-chip"/g) || []).length, 2)
    assert.match(html, /validation_failed/)
    assert.doesNotMatch(html, /Turn into video|Generate media|Delegated designs/)
    assert.match(html, /Open selected output/)
    assert.equal((html.match(/role="tab"/g) || []).length, 1)
  }
})

// Purpose: designThreads must resolve complete session-qualified immutable bases,
// preserve request/candidate identity and avoid merging same-title independent work.
// This adapter test catches authority loss before any thumbnail or viewer mounts.
test('design lineage uses exact session, revision and digest and keeps candidate siblings in one request', () => {
  const edit: ProjectDesign = { ...row, request: { ...row.request, id: 'edit', state: 'running', candidates: [
    { spec: { artifact_id: 'design', kind: 'html', base: ref }, state: 'running' },
    { spec: { artifact_id: 'alternate', kind: 'html', base: ref }, state: 'queued' },
  ] } }
  const foreign = { ...edit, request: { ...edit.request, parent_session_id: 'foreign' } }
  const wrongDigest = { ...edit, request: { ...edit.request, id: 'wrong-digest', candidates: [{ ...edit.request.candidates[0], spec: { ...edit.request.candidates[0].spec, base: { ...ref, sha256: 'different' } } }] } }
  const wrongRevision = { ...edit, request: { ...edit.request, id: 'wrong-revision', candidates: [{ ...edit.request.candidates[0], spec: { ...edit.request.candidates[0].spec, base: { ...ref, revision: 99 } } }] } }
  const independent = { ...row, request: { ...row.request, id: 'independent', candidates: [] } }
  const threads = designThreads([edit, foreign, wrongDigest, wrongRevision, independent, row, row])
  assert.deepEqual(threads.map(thread => ({ id: thread.id, turns: thread.turns.map(designRequestId) })), [
    { id: designRequestId(row), turns: [designRequestId(row), designRequestId(edit)] },
    ...[foreign, wrongDigest, wrongRevision, independent].map(value => ({ id: designRequestId(value), turns: [designRequestId(value)] })),
  ])
  const item = designMediaItem(row, 0, readyDesignRevision(row.request.candidates[0])!)
  assert.equal(item.design?.revision.request_id, 'request')
  assert.equal(item.design?.revision.candidate, 0)
  assert.deepEqual(item.design?.revision.ref, ref)
})

// Purpose: archive filtering in MediaTaskCard must not renumber a surviving
// candidate or change its exact source. SSR plus the adapter closure is sufficient
// here; archive CAS is owned by the unchanged DesignArchiveButton/API boundary.
test('archived siblings do not renumber the remaining exact candidate', () => {
  const design: ProjectDesign = { ...row, request: { ...row.request, candidates: [
    { ...row.request.candidates[0], archived: true },
    { ...row.request.candidates[0], spec: { artifact_id: 'second', kind: 'html' }, attempts: [{ number: 1, state: 'succeeded', result: { ...ref, artifact_id: 'second' } }] },
  ] } }
  const opened: unknown[] = []
  const tree = MediaTaskCard({ source: 'independent-design', design, onDesignPreview: item => opened.push(item.design) })!
  const html = renderToStaticMarkup(tree)
  assert.match(html, /Candidate 2 · ready/)
  assert.doesNotMatch(html, /Candidate 1 · ready/)
  tree.props.turns[0].outputs[0].open()
  assert.deepEqual(opened, [designMediaItem(design, 1, readyDesignRevision(design.request.candidates[1])!).design])
})
