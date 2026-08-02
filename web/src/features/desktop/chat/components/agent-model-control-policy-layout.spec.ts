import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('agent setup configures agents directly without profile management', () => {
  assert.doesNotMatch(source, /Saved profiles|Profile settings|aria-label="Saved model profiles"/)
  assert.match(source, /title="Default Model"/)
  assert.match(source, /title="Plan Model"/)
  assert.match(source, /Configure this system agent’s model directly/)
  assert.match(source, /label="Provider"[\s\S]*label="Model"[\s\S]*label="Thinking"[\s\S]*label="Service tier"/)
  assert.doesNotMatch(source, /Make account default|Continue for this chat only|Save as new/)
})
