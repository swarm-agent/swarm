// Purpose: TaskRequirements and the task-card approval predicate must expose
// both persisted outcomes and the executable plan; malformed/loading documents
// fail closed. Static rendering is the narrowest layer proving visible content.
import React from 'react'
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

// Requirement: TaskPlanDetails is a read-only, allowlisted presentation boundary,
// including persisted JSON/envelopes and legacy aliases. Unknown fields and internal
// prompts must not leak into the DOM or become fabricated requirements. SSR is the
// narrowest layer for exhaustive source-shape checks; browser tests prove visibility.
import { TaskPlanDetails } from './task-plan-details'

const program = {
  id: 'internal-program',
  stages: [{ id: 'prepare', title: 'Prepare storage', description: 'Keep the existing format' }, { id: 'ship', title: 'Ship changes', depends_on: ['prepare'], dependency_evidence: 'Migration is compatible' }],
  jobs: [{ id: 'writer', stage_id: 'ship', title: 'Atomic writer', deliverable: 'Crash-safe preferences', owned_scope: ['src/preferences.ts'], acceptance_criteria: ['Interrupted writes keep old values'], meta_prompt: 'PRIVATE_AGENT_INSTRUCTIONS', output_requirements: { schema: 'PRIVATE_SCHEMA' } }],
}

test('structured and persisted details retain substantive fields, order and dependencies without wire data', () => {
  const plan = { ...document, info: { ...document.info, context: 'Existing users keep their settings', constraints: ['No format change'], validation_strategy: 'Interrupt a write', notes: 'Preserve the backup' }, checkpoints: [
    { id: 'second', order: 2, title: 'Verify recovery', tasks: ['Restart after interruption'], acceptance_criteria: ['Settings survive restart'], task_program: program },
    { id: 'first', order: 1, title: 'Persist settings', subtasks: [{ title: 'Write atomically', notes: 'Flush before rename' }, { private_field: 'PRIVATE_TASK' }], acceptanceCriteria: [{ criterion: 'Never truncate preferences' }], notes: 'Use the portable file API' },
  ], current_run_id: 'PRIVATE_ROUTING' }
  for (const source of [plan, JSON.stringify(plan), { document: plan }, JSON.stringify({ document: plan }), { plan_document: JSON.stringify(plan) }]) {
    const html = renderToStaticMarkup(<TaskPlanDetails document={source} />)
    for (const content of ['Keep settings after restart', 'Existing users keep their settings', 'No format change', 'Interrupt a write', 'Preserve the backup', 'Write atomically', 'Flush before rename', 'Never truncate preferences', 'Use the portable file API', 'Prepare storage', 'Ship changes', 'Migration is compatible', 'Atomic writer', 'Crash-safe preferences', 'src/preferences.ts', 'Interrupted writes keep old values', 'Depends on']) assert.ok(html.includes(content), content)
    assert.ok(html.indexOf('1. Persist settings') < html.indexOf('2. Verify recovery'))
    assert.doesNotMatch(html, /PRIVATE_|meta_prompt|output_requirements|current_run_id|&quot;checkpoints&quot;|<pre/)
  }
})

test('markdown and direct serialized sources render safely, while missing content stays honest', () => {
  const markdown = '## Storage goal\n\nKeep **all preferences**.\n\n- [ ] Write safely\n- Verify restart\n\n<script>alert(1)</script>\n\n[bad](javascript:alert(1))'
  const html = renderToStaticMarkup(<TaskPlanDetails markdown={markdown} description="Preserve existing preferences" />)
  assert.match(html, /<h2[^>]*><span>Storage goal<\/span><\/h2>/)
  assert.match(html, /<strong><span>all preferences<\/span><\/strong>/)
  assert.match(html, /Write safely/)
  assert.doesNotMatch(html, /<script|href="javascript:|## Storage goal/)
  const serialized = renderToStaticMarkup(<TaskPlanDetails markdown={'```json\n' + JSON.stringify(document) + '\n```'} />)
  assert.match(serialized, /Keep settings after restart/)
  assert.doesNotMatch(serialized, /<pre|&quot;checkpoints&quot;/)
  for (const source of [undefined, {}, { meta_prompt: 'PRIVATE_PROMPT' }, '{"checkpoints":', { checkpoints: [{ tasks: [{ secret: 'PRIVATE_TASK' }] }] }]) {
    const missing = renderToStaticMarkup(<TaskPlanDetails document={source} />)
    assert.doesNotMatch(missing, /PRIVATE_|&quot;checkpoints&quot;/)
    if (!(source && typeof source === 'object' && 'checkpoints' in source)) assert.match(missing, /Readable plan details are unavailable/)
  }
})
