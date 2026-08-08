import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('agent setup orders core system agents before separated utilities', () => {
  assert.match(source, /CORE_SYSTEM_AGENT_NAMES = \[SWARM_AGENT_NAME, FINDER_AGENT_NAME, CODER_AGENT_NAME, DESIGNER_AGENT_NAME\]/)
  assert.match(source, /UTILITY_SYSTEM_AGENT_NAMES = \[COMPACT_AGENT_NAME, ROUTER_AGENT_NAME\]/)
  assert.match(source, /label: 'Core system agents', items: CORE_SYSTEM_AGENT_NAMES\.map\(systemItem\)/)
  assert.match(source, /label: 'Utilities', items: UTILITY_SYSTEM_AGENT_NAMES\.map\(systemItem\)/)
})
