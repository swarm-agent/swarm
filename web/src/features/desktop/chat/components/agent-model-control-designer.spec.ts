import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('Desktop exposes Designer as a compiled flat-model system agent', () => {
  assert.match(source, /const DESIGNER_AGENT_NAME = 'system-designer'/)
  assert.match(source, /systemAgents\.designer/)
  assert.match(source, /profile\.name === DESIGNER_AGENT_NAME \? 'designer'/)
  assert.match(source, /saveSystemAgentModelSettings/)
  assert.match(source, /read: \{ enabled: true/)
  assert.match(source, /write: \{ enabled: true/)
  assert.match(source, /edit: \{ enabled: true/)
  assert.doesNotMatch(source, /designerProfile[\s\S]{0,1200}bash:/i)
  assert.doesNotMatch(source, /designerProfile[\s\S]{0,1200}modelMode|planProvider|autoProvider/)
})
