// Purpose: TaskRequirements and the task-card approval predicate must expose
// both persisted outcomes and the executable plan; malformed/loading documents
// fail closed. Static rendering is the narrowest layer proving visible content.
import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { TaskRequirements, isTaskPlanReviewable } from './task-requirements'

const document = {
  title: 'Repair preferences', info: { goal: 'Keep settings after restart' },
  requirements: [{ id: 'r1', text: 'Settings survive restart', checkpoint_id: 'cp-1' }],
  checkpoints: [{ id: 'cp-1', title: 'Persist settings', tasks: ['Write settings atomically'], acceptance_criteria: ['Settings survive restart'] }],
}

test('persisted review shows both checklist and readable plan after reload', () => {
  const restored = JSON.parse(JSON.stringify(document))
  assert.equal(isTaskPlanReviewable(restored), true)
  const html = renderToStaticMarkup(<TaskRequirements document={restored} />)
  for (const text of ['What will change', 'Settings survive restart', 'Proposed plan', 'Persist settings', 'Write settings atomically']) assert.ok(html.includes(text))
})

test('missing and malformed review content is unavailable, never approvable', () => {
  for (const invalid of [null, {}, { ...document, requirements: [] }, { ...document, checkpoints: [] }, { ...document, requirements: [{ id: 'r1', text: ' ', checkpoint_id: 'cp-1' }] }, { ...document, requirements: [{ id: 'r1', text: 'Unbound outcome', checkpoint_id: 'cp-1' }] }]) {
    assert.equal(isTaskPlanReviewable(invalid), false)
    const html = renderToStaticMarkup(<TaskRequirements document={invalid} />)
    assert.match(html, /role="alert"/)
    assert.match(html, /unavailable or invalid/)
    assert.doesNotMatch(html, /<h4/)
  }
})
