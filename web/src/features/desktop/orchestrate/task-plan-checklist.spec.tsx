import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { TaskPlanChecklist, isTaskPlanReviewable } from './task-requirements'

// Requirement: presentation preserves ordered, bound requirement text without
// truncation or completion state. TaskPlanChecklist owns display, while
// isTaskPlanReviewable independently owns approval. Server component rendering
// is the narrowest layer for precedence, legacy fallback and malformed data.
test('checklist preserves all bound text, ignores generic criteria when requirements exist', () => {
  const text = 'Show every matching filename. '.repeat(80)
  const document = { requirements: [{ id: 'r', text, checkpoint_id: 'cp' }], checkpoints: [{ id: 'cp', title: 'Build', acceptance_criteria: ['Deliverable ready', text] }] }
  const html = renderToStaticMarkup(<TaskPlanChecklist document={document} />)
  assert.ok(html.includes(text))
  assert.doesNotMatch(html, /Deliverable ready|line-clamp|truncate|type="checkbox"|checked/)
  assert.equal(isTaskPlanReviewable(document), false)
})

// Requirement: old plans show persisted criteria rather than fabricated summaries;
// invalid authored bindings never silently substitute legacy gates. Rendering is
// sufficient to prove these non-mutating presentation failure cases.
test('legacy criteria and absent or invalid data are represented honestly', () => {
  const legacy = { checkpoints: [{ id: 'cp', title: 'Search', acceptance_criteria: ['Deliverable ready', 'Results show filenames', 'Branch clean', 'Search handles empty input'] }] }
  const html = renderToStaticMarkup(<TaskPlanChecklist document={legacy} />)
  assert.ok(html.indexOf('Results show filenames') < html.indexOf('Search handles empty input'))
  assert.doesNotMatch(html, /Deliverable ready|Branch clean/)
  assert.equal(isTaskPlanReviewable(legacy), false)
  assert.match(renderToStaticMarkup(<TaskPlanChecklist document={null} />), /No bound requirements/)
  const invalid = { ...legacy, requirements: [{ id: 'r', text: 'Obsolete outcome', checkpoint_id: 'missing' }] }
  const invalidHtml = renderToStaticMarkup(<TaskPlanChecklist document={invalid} />)
  assert.match(invalidHtml, /not bound to the current plan/)
  assert.doesNotMatch(invalidHtml, /Results show filenames|Obsolete outcome/)
})
