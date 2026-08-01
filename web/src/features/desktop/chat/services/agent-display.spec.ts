import assert from 'node:assert/strict'
import test from 'node:test'

import { displayAgentName } from './agent-display'

test('compiled Compact uses its product label', () => {
  assert.equal(displayAgentName('system-compact'), 'Compact')
})

test('compiled Designer uses its product label', () => {
  assert.equal(displayAgentName('system-designer'), 'Designer')
})
