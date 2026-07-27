import assert from 'node:assert/strict'
import test from 'node:test'

import { displayAgentName } from './agent-display'

test('compiled Designer uses its product label', () => {
  assert.equal(displayAgentName('system-designer'), 'Designer')
})
