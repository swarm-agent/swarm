// Purpose: TaskRequirements presents authored outcomes rather than execution
// prompts. Server rendering is the narrowest check of visible text and stable IDs;
// it does not prove browser geometry or change-request transport.
import test from 'node:test'
import assert from 'node:assert/strict'
import React from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { TaskRequirements } from './task-requirements'

test('requirements and their changes are readable without execution details', () => {
  const html = renderToStaticMarkup(<TaskRequirements document={{
    requirements: [{ id: 'save', text: 'Save preferences automatically', checkpoint_id: 'cp' }],
    requirement_changes: ['Changed: Save preferences automatically'],
    checkpoints: [{ id: 'cp', title: 'Verbose technical execution instructions' }],
  }} />)
  assert.match(html, /What will change/)
  assert.match(html, /data-requirement-id="save"/)
  assert.match(html, /Save preferences automatically/)
  assert.match(html, /Changed requirements/)
  assert.doesNotMatch(html, /Verbose technical/)
})

test('legacy documents do not fabricate a requirements checklist', () => {
  const html = renderToStaticMarkup(<TaskRequirements document={null} />)
  assert.match(html, /not been authored yet/)
  assert.doesNotMatch(html, /data-requirement-id/)
})
