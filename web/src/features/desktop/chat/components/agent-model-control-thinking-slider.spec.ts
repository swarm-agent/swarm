import assert from 'node:assert/strict'
import test from 'node:test'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('./agent-model-control.tsx', import.meta.url), 'utf8')

test('agent model editor keeps provider, model, and service tier on one row with centered thinking below', () => {
  assert.match(source, /md:grid-cols-\[minmax\(140px,0\.75fr\)_minmax\(220px,1\.5fr\)_minmax\(140px,0\.75fr\)\][\s\S]*label="Provider"[\s\S]*label="Model"[\s\S]*label="Service tier"/)
  assert.match(source, /mx-auto mt-4 w-full max-w-lg[\s\S]*<ThinkingSlider/)
})

test('thinking slider is indexed only by the selected model thinking options', () => {
  assert.match(source, /const thinkingOptions = thinkingOptionsForOption\(selectedOption\)/)
  assert.match(source, /type="range"[\s\S]*max=\{Math\.max\(0, options\.length - 1\)\}[\s\S]*onChange=\{\(event\) => onChange\(options\[Number\(event\.target\.value\)\]/)
  assert.match(source, /thinking: normalizeDraftThinking\(current\.provider, model, modelOptions, current\.thinking\)/)
})
