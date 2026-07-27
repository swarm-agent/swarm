import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('Desktop exposes Designer as a locked single-model compiled system agent', () => {
  assert.match(source, /const DESIGNER_AGENT_NAME = 'system-designer'/)
  assert.match(source, /normalizeDesignerAgentSettings\(uiSettings\)/)
  assert.match(source, /profile\.name === DESIGNER_AGENT_NAME \? 'designer'/)
  assert.match(source, /read: \{ enabled: true/)
  assert.match(source, /write: \{ enabled: true/)
  assert.match(source, /edit: \{ enabled: true/)
  assert.doesNotMatch(source, /designerProfile[\s\S]{0,1200}bash:/i)
})
