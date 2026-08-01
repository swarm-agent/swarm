import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('setup keeps compiled identities and flat favorites separate', () => {
  assert.match(source, /const COMPACT_AGENT_NAME = 'system-compact'/)
  assert.match(source, /const CODER_AGENT_NAME = 'system-coder'/)
  assert.match(source, /const FINDER_AGENT_NAME = 'system-finder'/)
  assert.match(source, /const SWARM_AGENT_NAME = 'swarm'/)
  assert.match(source, /label: 'Agents', profiles: \[\.\.\.\(swarmProfile \? \[swarmProfile\] : \[\]\), \.\.\.primaryProfiles\]/)
  assert.match(source, /label: 'System agents'[\s\S]*isCompiledSystemAgent\(agent\.name\)/)
  assert.doesNotMatch(source, /system-plan-sidechat|system-ai-sidechat/)
  assert.match(source, /aria-label="Saved model profiles"/)
  assert.match(source, /savedFavoriteModelLabel\(profile\)/)
  assert.match(source, /identity, prompt, runtime, and tool contract remain code-owned/)
  assert.match(source, /ROUTER_AGENT_NAME \? 'Router'/)
  assert.doesNotMatch(source, /savedProfileModelLabels|profile\.modelMode|profile\.single|profile\.plan|profile\.auto/)
})
