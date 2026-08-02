import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('setup keeps compiled identities directly model-configurable', () => {
  assert.match(source, /const COMPACT_AGENT_NAME = 'system-compact'/)
  assert.match(source, /const CODER_AGENT_NAME = 'system-coder'/)
  assert.match(source, /const FINDER_AGENT_NAME = 'system-finder'/)
  assert.match(source, /const SWARM_AGENT_NAME = 'swarm'/)
  assert.match(source, /label: 'Agents', items: \[\{ name: SWARM_AGENT_NAME, profile: null \}, \.\.\.primaryProfiles\.map\(item\)\]/)
  assert.match(source, /const draftProfile = draftAgentName === SWARM_AGENT_NAME \? null/)
  assert.match(source, /draftAgentName === SWARM_AGENT_NAME \? \(/)
  assert.match(source, /label: 'System agents'[\s\S]*isCompiledSystemAgent\(agent\.name\)/)
  assert.match(source, /Configure this system agent’s model directly/)
  assert.match(source, /saveSystemAgentSettings/)
  assert.match(source, /title="Default Model"/)
  assert.match(source, /title="Plan Model"/)
  assert.match(source, /const requestedAgentName = initialAgentName\.trim\(\)/)
  assert.match(source, /const agentName = requestedAgentName === SWARM_AGENT_NAME \|\| requestedProfile \? requestedAgentName : SWARM_AGENT_NAME/)
  assert.ok(source.indexOf('title="Default Model"') < source.indexOf('title="Plan Model"'))
  assert.doesNotMatch(source, /Saved profiles|Profile settings|aria-label="Saved model profiles"/)
})
