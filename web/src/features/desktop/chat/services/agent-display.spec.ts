import assert from 'node:assert/strict'
import test from 'node:test'

import { displayAgentName } from './agent-display'

test('compiled Compact uses its product label', () => {
  assert.equal(displayAgentName('system-compact'), 'Compact')
})

test('compiled Designer uses its product label', () => {
  assert.equal(displayAgentName('system-designer'), 'Designer')
})

test('compiled Router uses its product label', () => {
  assert.equal(displayAgentName('system-router'), 'Router')
})

test('compiled Orchestrator uses its Plan / Orchestrator label', () => {
  assert.equal(displayAgentName('system-orchestrator'), 'Plan / Orchestrator')
  assert.equal(displayAgentName('swarm-orchestrator'), 'Plan / Orchestrator')
  assert.equal(displayAgentName('orchestrator'), 'Plan / Orchestrator')
})
