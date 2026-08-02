import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('setup keeps compiled identities directly model-configurable', () => {
  assert.match(source, /const COMPACT_AGENT_NAME = 'system-compact'/)
  assert.match(source, /const CODER_AGENT_NAME = 'system-coder'/)
  assert.match(source, /const FINDER_AGENT_NAME = 'system-finder'/)
  assert.match(source, /const SWARM_AGENT_NAME = 'swarm'/)
  assert.match(source, /label: 'Agents', profiles: \[\.\.\.\(swarmProfile \? \[swarmProfile\] : \[\]\), \.\.\.primaryProfiles\]/)
  assert.match(source, /label: 'System agents'[\s\S]*isCompiledSystemAgent\(agent\.name\)/)
  assert.match(source, /Configure this system agent’s model directly/)
  assert.match(source, /saveSystemAgentSettings/)
  assert.match(source, /title="Action model"/)
  assert.match(source, /title="Plan model"/)
  assert.doesNotMatch(source, /Saved profiles|Profile settings|aria-label="Saved model profiles"/)
})
