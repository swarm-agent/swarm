import assert from 'node:assert/strict'
import test from 'node:test'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MediaTaskCard } from './media-task-card'
import { designMediaItem, designNodeId, designReadyItems, readyDesignRevision } from './design-media-task'
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
    assert.match(html, /1\/2 ready/)
    assert.match(html, /validation_failed/)
    assert.doesNotMatch(html, /Turn into video|Generate media|Delegated designs/)
    assert.match(html, /aria-label="Preview Landing page candidate 1"/)
  }
})
