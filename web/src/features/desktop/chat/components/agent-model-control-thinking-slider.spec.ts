import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('flat favorite editor keeps provider, model, thinking, and service tier together', () => {
  assert.match(source, /grid-cols-\[minmax\(130px,0\.7fr\)_minmax\(220px,1\.4fr\)_minmax\(130px,0\.7fr\)_minmax\(130px,0\.7fr\)\][\s\S]*label="Provider"[\s\S]*label="Model"[\s\S]*label="Thinking"[\s\S]*label="Service tier"/)
})

test('thinking options remain catalog driven without split editors', () => {
  assert.match(source, /const thinkingOptions = thinkingOptionsForOption\(selectedOption\)/)
  assert.match(source, /thinking: normalizeDraftThinking\(current\.provider, model, modelOptions, current\.thinking\)/)
  assert.doesNotMatch(source, /ThinkingSlider|Plan agent model|Action agent model|planDraft|autoDraft/)
})
