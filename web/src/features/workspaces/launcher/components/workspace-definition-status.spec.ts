import assert from 'node:assert/strict'
import test from 'node:test'
import { readFile } from 'node:fs/promises'

const source = await readFile(new URL('./workspace-definition-status.tsx', import.meta.url), 'utf8')

test('workspace manager renders pending, completed, and exhausted failure states from backend fields', () => {
  assert.match(source, /workspace\.definitionStatus === 'pending'/)
  assert.match(source, /Router is analyzing this workspace/)
  assert.match(source, /workspace\.definitionStatus === 'failed'/)
  assert.match(source, /workspace\.definitionError/)
  assert.match(source, /workspace\.definitionSuggestion/)
  assert.match(source, /Change the Router model in Settings/)
  assert.match(source, /<Link to="\/settings"/)
  assert.match(source, /workspace\.definitionStatus !== 'completed'/)
  assert.match(source, /<details/)
  assert.match(source, /\{workspace\.definition\}/)
})
